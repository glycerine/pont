// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

// This file contains the implementation of Go channels.

// Invariants:
//  At least one of c.sendq and c.recvq is empty,
//  except for the case of an unbuffered channel with a single goroutine
//  blocked on it for both sending and receiving using a select statement,
//  in which case the length of c.sendq and c.recvq is limited only by the
//  size of the select statement.
//
//  Addition: except for a pipe(full only)-receive. We can have
//  a pipe receiver waiting while the channel is partly full.
//
// For buffered channels, also:
//  c.qcount > 0 implies that c.recvq is empty, unless it contains a pipe-receive.
//  c.qcount < c.dataqsiz implies that c.sendq is empty.

import (
	"internal/abi"
	"internal/runtime/atomic"
	"internal/runtime/math"
	"internal/runtime/sys"
	"unsafe"
)

const (
	maxAlign  = 8
	hchanSize = unsafe.Sizeof(hchan{}) + uintptr(-int(unsafe.Sizeof(hchan{}))&(maxAlign-1))
	debugChan = false
)

type hchan struct {
	qcount   uint           // total data in the queue
	dataqsiz uint           // size of the circular queue
	buf      unsafe.Pointer // points to an array of dataqsiz elements
	elemsize uint16
	closed   uint32
	timer    *timer // timer feeding this chan
	elemtype *_type // element type
	sendx    uint   // send index
	recvx    uint   // receive index
	recvq    waitq  // list of recv waiters
	sendq    waitq  // list of send waiters
	bubble   *synctestBubble

	// sticky of 0 means no sticky values, regular channel.
	// Else sticky-1 gives the index of the sticky value
	// in the buf. When sticky-1 == recvx, then the
	// sticky value sticks, as it is at the front of the
	// queue.
	// The sticky value, if present (when sticky > 0),
	// is always the last value in the queue, cannot be consumed,
	// and any later regular send or sticky send will
	// replace it.
	sticky uint
	final  uint

	// unique field for full/pipe-receive to acquire/release.
	// It appears at the moment we can use just c instead.
	//fullpipe uint

	// for deterministic version of sortkey()
	determkey uintptr

	// To allow a final sticky value to be
	// garbage collected from a closed (immutable)
	// channel, the channel can be deleted.
	// Any subsequent action on the
	// channel will panic.
	deleted uint32

	// lock protects all fields in hchan, as well as several
	// fields in sudogs blocked on this channel.
	//
	// Do not change another G's status while holding this lock
	// (in particular, do not ready a G), as this can deadlock
	// with stack shrinking.
	lock mutex
}

type waitq struct {
	first *sudog
	last  *sudog
}

//go:linkname reflect_makechan reflect.makechan
func reflect_makechan(t *chantype, size int) *hchan {
	return makechan(t, size)
}

func makechan64(t *chantype, size int64) *hchan {
	if int64(int(size)) != size {
		panic(plainError("makechan: size out of range"))
	}

	return makechan(t, int(size))
}

var chanSerialNumber atomic.Int64

func makechan(t *chantype, size int) *hchan {
	elem := t.Elem

	// compiler checks this but be safe.
	if elem.Size_ >= 1<<16 {
		throw("makechan: invalid channel element type")
	}
	if hchanSize%maxAlign != 0 || elem.Align_ > maxAlign {
		throw("makechan: bad alignment")
	}

	mem, overflow := math.MulUintptr(elem.Size_, uintptr(size))
	if overflow || mem > maxAlloc-hchanSize || size < 0 {
		panic(plainError("makechan: size out of range"))
	}

	// Hchan does not contain pointers interesting for GC when elements stored in buf do not contain pointers.
	// buf points into the same allocation, elemtype is persistent.
	// SudoG's are referenced from their owning thread so they can't be collected.
	// TODO(dvyukov,rlh): Rethink when collector can move allocated objects.
	var c *hchan
	switch {
	case mem == 0:
		// Queue or element size is zero.
		c = (*hchan)(mallocgc(hchanSize, nil, true))
		// Race detector uses this location for synchronization.
		c.buf = c.raceaddr()
	case !elem.Pointers():
		// Elements do not contain pointers.
		// Allocate hchan and buf in one call.
		c = (*hchan)(mallocgc(hchanSize+mem, nil, true))
		c.buf = add(unsafe.Pointer(c), hchanSize)
	default:
		// Elements contain pointers.
		c = new(hchan)
		c.buf = mallocgc(mem, elem, true)
	}

	c.elemsize = uint16(elem.Size_)
	c.elemtype = elem
	c.dataqsiz = uint(size)
	if b := getg().bubble; b != nil {
		c.bubble = b
	}
	lockInit(&c.lock, lockRankHchan)

	if debugChan {
		print("makechan: chan=", c, "; elemsize=", elem.Size_, "; dataqsiz=", size, "\n")
	}
	// jea
	c.determkey = uintptr(chanSerialNumber.Add(1))
	return c
}

// chanbuf(c, i) is pointer to the i'th slot in the buffer.
//
// chanbuf should be an internal detail,
// but widely used packages access it using linkname.
// Notable members of the hall of shame include:
//   - github.com/fjl/memsize
//
// Do not remove or change the type signature.
// See go.dev/issue/67401.
//
//go:linkname chanbuf
func chanbuf(c *hchan, i uint) unsafe.Pointer {
	return add(c.buf, uintptr(i)*uintptr(c.elemsize))
}

// full reports whether a send on c would block (that is, the channel is full).
// It uses a single word-sized read of mutable state, so although
// the answer is instantaneously true, the correct answer may have changed
// by the time the calling function receives the return value.
func full(c *hchan) bool {
	// c.dataqsiz is immutable (never written after the channel is created)
	// so it is safe to read at any time during channel operation.
	if c.dataqsiz == 0 {
		// Assumes that a pointer read is relaxed-atomic.
		return c.recvq.first == nil
	}
	// Assumes that a uint read is relaxed-atomic.
	return c.qcount == c.dataqsiz
}

// entry point for c <- x from compiled code.
//
//go:nosplit
func chansend1(c *hchan, elem unsafe.Pointer) {
	//println("chansend1 called") // ah. GC background scavenger calls us too. /usr/local/go/src/runtime/mgcscavenge.go:652
	chansend(c, elem, true, sys.GetCallerPC(), false, false)
}

// entry point for sticky send, c <$ x from compiled code.
//
//go:nosplit
func chansend1sticky(c *hchan, elem unsafe.Pointer) {
	//println("chansend1sticky called")
	chansend(c, elem, true, sys.GetCallerPC(), true, false)
}

// entry point for final (auto closing + sticky) send, c <@ x from compiled code.
//
//go:nosplit
func chansend1stickyFinal(c *hchan, elem unsafe.Pointer) {
	//println("chansend1stickyFinal called")
	chansend(c, elem, true, sys.GetCallerPC(), true, true)
}

/*
 * generic single channel send/recv
 * If block is not nil,
 * then the protocol will not
 * sleep but return if it could
 * not complete.
 *
 * sleep can wake up with g.param == nil
 * when a channel involved in the sleep has
 * been closed.  it is easiest to loop and re-run
 * the operation; we'll see that it's now closed.
 */
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr, sticky, final bool) bool {
	if c == nil {
		if !block {
			return false
		}
		gopark(nil, nil, waitReasonChanSendNilChan, traceBlockForever, 2)
		throw("unreachable")
	}

	if debugChan {
		print("chansend: chan=", c, "\n")
	}

	if raceenabled {
		racereadpc(c.raceaddr(), callerpc, abi.FuncPCABIInternal(chansend))
	}

	if c.deleted != 0 {
		// send on deleted channel.
		return false
	}

	if c.bubble != nil && getg().bubble != c.bubble {
		fatal("send on synctest channel from outside bubble")
	}

	if final && !sticky {
		fatal("internal error in chansend: all final sends must also be sticky")
	}

	if sticky && c.dataqsiz == 0 {
		panic(plainError("a sticky-send on an unbuffered channel is not allowed."))
	}

	//println("chansend: sticky =", sticky, "  ; final =", final)

	// Fast path: check for failed non-blocking operation without acquiring the lock.
	//
	// After observing that the channel is not closed, we observe that the channel is
	// not ready for sending. Each of these observations is a single word-sized read
	// (first c.closed and second full()).
	// Because a closed channel cannot transition from 'ready for sending' to
	// 'not ready for sending', even if the channel is closed between the two observations,
	// they imply a moment between the two when the channel was both not yet closed
	// and not ready for sending. We behave as if we observed the channel at that moment,
	// and report that the send cannot proceed.
	//
	// It is okay if the reads are reordered here: if we observe that the channel is not
	// ready for sending and then observe that it is not closed, that implies that the
	// channel wasn't closed during the first observation. However, nothing here
	// guarantees forward progress. We rely on the side effects of lock release in
	// chanrecv() and closechan() to update this thread's view of c.closed and full().
	if !block && c.closed == 0 && full(c) {
		return false
	}

	var t0 int64
	if blockprofilerate > 0 {
		t0 = cputicks()
	}

	lock(&c.lock)

	if c.closed != 0 {
		unlock(&c.lock)
		panic(plainError("send on closed channel"))
	}

	var ok, mustBlock bool
	if sticky {
		// c <$ x, sticky send.
		// INVAR: we are buffered, above asserted c.dataqsiz > 0.

		if c.sticky != 0 {

			// 2nd sticky send: update in place the trailing
			// sticky value. There is only ever
			// at most one sticky value in a channel, and
			// it is always last. Regular reads don't
			// remove it from the queue. Only clear(chan)
			// or a regular send (<-) will remove the sticky value.

			// what if the first sticky is not yet at the
			// head of the queue? same plan: just replace the
			// old sticky value with the new one, where-ever
			// it is in the queue.

			// println("sticky replacment behavior... c.sticky = ", c.sticky, " ; c.recvx = ", c.recvx, " ; c.sendx = ", c.sendx)
			qp := chanbuf(c, c.sticky-1)
			if raceenabled {
				racenotify(c, c.sticky-1, nil)
			}
			typedmemmove(c.elemtype, qp, ep)
			if final {
				c.final = c.sticky
				if c.closed == 0 {
					c.closed = 1
				}
			}
			unlock(&c.lock)
			return true

		} else {
			// INVAR: c.sticky == 0
			ok, mustBlock = chansendHelperFirstSticky(c, ep, block, final)
			if !mustBlock {
				// lock has been released
				return ok
			}
			// We still hold the lock when mustBlock is true.
			// We want to drop down to the code below that says,
			// "Block on the channel. Some receiver will complete
			// our operation for us."
		}
	} // end if sticky send.

	if !mustBlock {
		// c <- x, regular "pin" send, disappears sticky values.
		if c.sticky > 0 {
			// bump out the sticky value at the
			// back of the queue. Go back to un-sticky channel behavior.

			//println("back to regular chan behavior: regular send bumps out sticky. c.stick=", c.sticky, " ; c.qcount = ", c.qcount, " ; c.sendx= ", c.sendx)
			// c.sticky > 0, so this is a
			// regular send which discards the current sticky value
			// from the back of the queue, which might
			// also be the front of course. We transition
			// back to acting like a regular non-sticky channel.
			qp := chanbuf(c, c.sticky-1)
			if raceenabled {
				racenotify(c, c.sticky-1, nil)
			}
			typedmemclr(c.elemtype, qp)
			c.qcount--
			if c.sendx == 0 {
				c.sendx = c.dataqsiz - 1
			} else {
				c.sendx--
			}
			//println("after bump out. c.sticky = ", c.sticky, " -> 0; c.qcount = ", c.qcount, " ; c.sendx= ", c.sendx, " ; c.recvx = ", c.recvx)
			c.sticky = 0

			//if raceenabled {
			//	// might not be needed.
			//	//raceacquire(unsafe.Pointer(&c.fullpipe))
			//	//racerelease(unsafe.Pointer(&c.fullpipe))
			//}
		}
		// classic chansend behavior below

		// pipe-receive behavior
		skipBypass := false
		wakePipeReceiver := false
		if c.qcount < c.dataqsiz {
			// pipe-receive behavior changes the original invariants, in
			// that we can have waiting pipe-receivers and
			// have a non-empty channel.

			// if sg.piperecv then we do not want to bypass
			// the buffer at all.
			// TODO(jea): what if the pipe-receiver is the 2nd one in the recvq? do we need to scan the recvq? for now we just do the usual random wake up order. A competing receive could win against a pipe-receive. This seems natural, but also a pipe-receive can get starved when it might not have had to starve. We would have to give the pipe-receive (now final-receive) priority to fix this.
			first := c.recvq.first
			if first != nil {
				if first.piperecv {
					//println("chansend sees a piperecv waiting; c.qcount =", c.qcount)
					// send has a pre-condition that it only
					// operates on an empty channel.
					// how did we used know it was empty? we _used_ to know
					// because we have a waiting receiver now.
					//
					// but... with pipe-receive that does not hold, so we have to
					// "bypass the bypass" of the buffer with this flag.
					skipBypass = true

					if c.qcount == c.dataqsiz-1 {
						wakePipeReceiver = true
					}
				}
			}
		}

		if !skipBypass {
			if sg := c.recvq.dequeue(); sg != nil {
				// Found a waiting receiver. We pass the value we want to send
				// directly to the receiver, bypassing the channel buffer (if any).

				// pipe-receive: waiting receiver no longer means empty queue
				// A pipe-receiver waits until the queue is full, and
				// cannot use send anyway (assumes buffer is empty).
				// TODO(jea): this logic gives priority to non-pipe-receives.
				// is that fair/desired? skipBypass should address this some.
				// TODO(jea) does skipBypass work reliably enough that
				// we can elide this backstop?
				skipSend := false
				if sg.piperecv {
					// put the pipe-receiver back on the queue,
					// take the next instead.
					piper := sg
					next := c.recvq.dequeue()
					c.recvq.enqueue(piper)
					if next != nil {
						sg = next
					} else {
						skipSend = true
					}
				}

				if !skipSend {
					// 3 calls to send() incl select.go:529
					send(c, sg, ep, func() { unlock(&c.lock) }, 3) // call A to send()
					return true
				}
			}
		}

		if c.qcount < c.dataqsiz {
			// Space is available in the channel buffer. Enqueue the element to send.
			qp := chanbuf(c, c.sendx)
			var wakePipeReceiverSendx uint
			if raceenabled {
				racenotify(c, c.sendx, nil) // does racereleaseacquire(qp)

				if wakePipeReceiver {
					wakePipeReceiverSendx = c.sendx
					// the send must happen before the full/pipe-receive.
					// the pipe-receive must happen after the send.
					//println("wakePipeReceiver true")
				}
			}
			typedmemmove(c.elemtype, qp, ep)
			c.sendx++
			if c.sendx == c.dataqsiz {
				c.sendx = 0
			}
			c.qcount++
			if sticky {
				c.sticky = c.qcount // track index of sticky value
				if final {
					c.final = c.sticky
					if c.closed == 0 {
						c.closed = 1
					}
				}
			}
			if raceenabled {
				// needed to prevent race detector false alarms.
				//raceacquire(unsafe.Pointer(&c.fullpipe))
				//racerelease(unsafe.Pointer(&c.fullpipe))
				raceacquire(unsafe.Pointer(c))
				racerelease(unsafe.Pointer(c))
			}

			if wakePipeReceiver {
				// INVAR: c.qcount == c.dataqsiz
				chansendHelperFullBufferPipeReceive(c, func() { unlock(&c.lock) }, 3, wakePipeReceiverSendx)
				return true
			}
			//println("sender queued value ", ep)
			unlock(&c.lock)
			return true
		}

		if !block {
			unlock(&c.lock)
			return false
		}
	}

	// Block on the channel. Some receiver will complete our operation for us.
	gp := getg()
	mysg := acquireSudog()
	mysg.releasetime = 0
	if t0 != 0 {
		mysg.releasetime = -1
	}
	// No stack splits between assigning elem and enqueuing mysg
	// on gp.waiting where copystack can find it.
	mysg.elem.set(ep)
	mysg.stickysend = sticky
	mysg.finalsend = final

	mysg.waitlink = nil
	mysg.g = gp
	mysg.isSelect = false
	mysg.c.set(c)
	gp.waiting = mysg
	gp.param = nil
	c.sendq.enqueue(mysg)
	// Signal to anyone trying to shrink our stack that we're about
	// to park on a channel. The window between when this G's status
	// changes and when we set gp.activeStackChans is not safe for
	// stack shrinking.
	gp.parkingOnChan.Store(true)
	reason := waitReasonChanSend
	if c.bubble != nil {
		reason = waitReasonSynctestChanSend
	}
	gopark(chanparkcommit, unsafe.Pointer(&c.lock), reason, traceBlockChanSend, 2)
	// Ensure the value being sent is kept alive until the
	// receiver copies it out. The sudog has a pointer to the
	// stack object, but sudogs aren't considered as roots of the
	// stack tracer.
	KeepAlive(ep)

	// someone woke us up.
	if mysg != gp.waiting {
		throw("G waiting list is corrupted")
	}
	gp.waiting = nil
	gp.activeStackChans = false
	closed := !mysg.success
	gp.param = nil
	if mysg.releasetime > 0 {
		blockevent(mysg.releasetime-t0, 2)
	}
	mysg.c.set(nil)
	releaseSudog(mysg)
	if closed {
		if c.closed == 0 {
			throw("chansend: spurious wakeup")
		}
		panic(plainError("send on closed channel"))
	}
	return true
}

// helper. c.lock must be held. We will release it with funlock()
// before returning. Returns true if the receive was done.
// PRE: c.dataqsiz == c.qcount (the c buffer must be full).
func chansendHelperFullBufferPipeReceive(c *hchan, unlockf func(), skip int, wakePipeReceiverSendx uint) (receiveDone bool) {
	//println("chansendHelperFullBufferPipeReceive() top: wakePipeReceiver = true")
	if c.dataqsiz != c.qcount {
		fatal("chan.go internal error: chansendHelperFullBufferPipeReceive requires a full buffer")
	}
	rg := c.recvq.dequeue()
	if rg == nil {
		//println("chansendHelperFullBufferPipeReceive() no receiver waiting")
		unlockf()
		return
	}
	if !rg.piperecv {
		//println("chansendHelperFullBufferPipeReceive() not a pipe-receiver... hmm... ")
		unlockf()
		return
	}
	//println("Found a waiting pipe-receiver.")

	head := chanbuf(c, c.recvx)
	if raceenabled {
		// the current goro is the sender that made the channel full.
		// the receiver goro being unblocked on <| is rg.

		// this is absolutely needed.
		//raceacquireg(rg.g, unsafe.Pointer(&c.fullpipe))
		//racereleaseg(rg.g, unsafe.Pointer(&c.fullpipe))
		raceacquireg(rg.g, unsafe.Pointer(c))
		racereleaseg(rg.g, unsafe.Pointer(c))
	}
	// copy data from queue to receiver
	if rg.elem.get() != nil {
		sendDirect(c.elemtype, rg, head)
		rg.elem.set(nil)
		// TODO(jea): we might want this actually? or similar.
		//if raceenabled {
		//	raceacquire(unsafe.Pointer(rg.elem))
		//	racereleaseg(rg.g, unsafe.Pointer(rg.elem))
		//}
	}

	typedmemclr(c.elemtype, head)
	c.qcount--
	c.recvx++
	if c.recvx == c.dataqsiz {
		c.recvx = 0
	}
	//c.sendx = c.recvx // c.sendx = (c.sendx + 1) % c.dataqsiz

	// wake up the pipe receiver goroutine
	gp := rg.g
	unlockf()
	gp.param = unsafe.Pointer(rg)
	rg.success = true
	if rg.releasetime != 0 {
		rg.releasetime = cputicks()
	}
	goready(gp, skip+1)
	return true
}

// helper. c.lock must be held. we release it unless mustBlock is set to true.
func chansendHelperFirstSticky(c *hchan, ep unsafe.Pointer, block, final bool) (sentOK, mustBlock bool) {
	// We see a first sticky value.
	//
	// The sticky value doesn't stick until it
	// reaches the head. It advances
	// closer to head position with each
	// receive in turn.
	//
	// We have to be careful below to get
	// the sticky value both saved to the queue and
	// a copy sent to any waiting receiver
	// if we are adding it to any empty queue.

	if c.qcount == c.dataqsiz {
		// no place to stash our sticky value,
		// so even with a receiver waiting we must block
		// if block requested.
		if !block {
			unlock(&c.lock)
			return
		}
		mustBlock = true
		return
	}
	// we have space for the sticky value.
	// Copy same code from below. (jea)TODO: separate
	// sticky logic into a separate chansendSticky()?

	// Space is available in the channel buffer. Enqueue the element to send.
	qp := chanbuf(c, c.sendx)
	if raceenabled {
		racenotify(c, c.sendx, nil)
	}
	typedmemmove(c.elemtype, qp, ep)

	// always sticky now in this subroutine
	c.sticky = c.sendx + 1 // track index+1 of sticky value

	//println("chansendHelperFirstSticky(): c.sticky = ", c.sticky, " ; c.sendx = ", c.sendx)

	c.sendx++
	if c.sendx == c.dataqsiz {
		c.sendx = 0
	}
	c.qcount++
	sentOK = true

	if final {
		if c.closed == 0 {
			c.closed = 1
		}
		c.final = c.sticky
		//println("chansendHelperFirstSticky(): c.final = ", c.final)
	}

	// (jea) note: checking if c.qcount == 1 (was 0) is not a reliable way
	// of knowing that you have no receivers; empirically
	// the top level invariant does not hold here.
	// When we tried to rely on that, we got spurious
	// deadlocks in simple (test/sticky3.go) code.

	if sg := c.recvq.dequeue(); sg != nil {
		//println("Found a waiting receiver.")

		// They should get the next read in the queue,
		// which is not us, because the we know here
		// that the queue was not empty before we came.

		// 3 calls to send() including select.go:529
		send(c, sg, ep, func() { unlock(&c.lock) }, 3) // call B to send()
		return
	}

	unlock(&c.lock)
	return
}

// send processes a send operation on an empty channel c.
// The value ep sent by the sender is copied to the receiver sg.
// The receiver is then woken up to go on its merry way.
// Channel c must be empty and locked.  send unlocks c with unlockf.
// sg must already be dequeued from c.
// ep must be non-nil and point to the heap or the caller's stack.
//
// (jea)TODO: if sticky, we want to set c.sticky in here?
func send(c *hchan, sg *sudog, ep unsafe.Pointer, unlockf func(), skip int) {
	if c.bubble != nil && getg().bubble != c.bubble {
		unlockf()
		fatal("send on synctest channel from outside bubble")
	}
	if raceenabled {
		if c.dataqsiz == 0 {
			racesync(c, sg)
		} else {
			// Pretend we go through the buffer, even though
			// we copy directly. Note that we need to increment
			// the head/tail locations only when raceenabled.
			racenotify(c, c.recvx, nil)
			racenotify(c, c.recvx, sg)
			c.recvx++
			if c.recvx == c.dataqsiz {
				c.recvx = 0
			}
			c.sendx = c.recvx // c.sendx = (c.sendx+1) % c.dataqsiz
		}
	}
	if sg.elem.get() != nil {
		sendDirect(c.elemtype, sg, ep)
		sg.elem.set(nil)
	}
	gp := sg.g
	unlockf()
	gp.param = unsafe.Pointer(sg)
	sg.success = true
	if sg.releasetime != 0 {
		sg.releasetime = cputicks()
	}
	goready(gp, skip+1)
}

// timerchandrain removes all elements in channel c's buffer.
// It reports whether any elements were removed.
// Because it is only intended for timers, it does not
// handle waiting senders at all (all timer channels
// use non-blocking sends to fill the buffer).
func timerchandrain(c *hchan) bool {
	// Note: Cannot use empty(c) because we are called
	// while holding c.timer.sendLock, and empty(c) will
	// call c.timer.maybeRunChan, which will deadlock.
	// We are emptying the channel, so we only care about
	// the count, not about potentially filling it up.
	if atomic.Loaduint(&c.qcount) == 0 {
		return false
	}
	lock(&c.lock)
	any := false
	for c.qcount > 0 {
		any = true
		typedmemclr(c.elemtype, chanbuf(c, c.recvx))
		c.recvx++
		if c.recvx == c.dataqsiz {
			c.recvx = 0
		}
		c.qcount--
	}
	unlock(&c.lock)
	return any
}

// Sends and receives on unbuffered or empty-buffered channels are the
// only operations where one running goroutine writes to the stack of
// another running goroutine. The GC assumes that stack writes only
// happen when the goroutine is running and are only done by that
// goroutine. Using a write barrier is sufficient to make up for
// violating that assumption, but the write barrier has to work.
// typedmemmove will call bulkBarrierPreWrite, but the target bytes
// are not in the heap, so that will not help. We arrange to call
// memmove and typeBitsBulkBarrier instead.

func sendDirect(t *_type, sg *sudog, src unsafe.Pointer) {
	// src is on our stack, dst is a slot on another stack.

	// Once we read sg.elem out of sg, it will no longer
	// be updated if the destination's stack gets copied (shrunk).
	// So make sure that no preemption points can happen between read & use.
	dst := sg.elem.get()
	typeBitsBulkBarrier(t, uintptr(dst), uintptr(src), t.Size_)
	// No need for cgo write barrier checks because dst is always
	// Go memory.
	memmove(dst, src, t.Size_)
}

func recvDirect(t *_type, sg *sudog, dst unsafe.Pointer) {
	// dst is on our stack or the heap, src is on another stack.
	// The channel is locked, so src will not move during this
	// operation.
	src := sg.elem.get()
	typeBitsBulkBarrier(t, uintptr(dst), uintptr(src), t.Size_)
	memmove(dst, src, t.Size_)
}

func closechan(c *hchan) {
	if c == nil {
		panic(plainError("close of nil channel"))
	}
	if c.bubble != nil && getg().bubble != c.bubble {
		fatal("close of synctest channel from outside bubble")
	}

	lock(&c.lock)
	if c.closed != 0 {
		unlock(&c.lock)
		// close() should always have been idempotent. now it is.
		return
	}

	if raceenabled {
		callerpc := sys.GetCallerPC()
		racewritepc(c.raceaddr(), callerpc, abi.FuncPCABIInternal(closechan))
		racerelease(c.raceaddr())
	}

	c.closed = 1

	var glist gList

	// release all readers
	for {
		sg := c.recvq.dequeue()
		if sg == nil {
			break
		}
		if sg.elem.get() != nil {
			typedmemclr(c.elemtype, sg.elem.get())
			sg.elem.set(nil)
		}
		if sg.releasetime != 0 {
			sg.releasetime = cputicks()
		}
		gp := sg.g
		gp.param = unsafe.Pointer(sg)
		sg.success = false
		if raceenabled {
			raceacquireg(gp, c.raceaddr())
		}
		glist.push(gp)
	}

	// release all writers (they will panic)
	for {
		sg := c.sendq.dequeue()
		if sg == nil {
			break
		}
		sg.elem.set(nil)
		if sg.releasetime != 0 {
			sg.releasetime = cputicks()
		}
		gp := sg.g
		gp.param = unsafe.Pointer(sg)
		sg.success = false
		if raceenabled {
			raceacquireg(gp, c.raceaddr())
		}
		glist.push(gp)
	}
	unlock(&c.lock)

	// Ready all Gs now that we've dropped the channel lock.
	for !glist.empty() {
		gp := glist.pop()
		gp.schedlink = 0
		goready(gp, 3)
	}
}

// empty reports whether a read from c would block (that is, the channel is
// empty).  It is atomically correct and sequentially consistent at the moment
// it returns, but since the channel is unlocked, the channel may become
// non-empty immediately afterward.
func empty(c *hchan) bool {
	// c.dataqsiz is immutable.
	if c.dataqsiz == 0 {
		return atomic.Loadp(unsafe.Pointer(&c.sendq.first)) == nil
	}
	// c.timer is also immutable (it is set after make(chan) but before any channel operations).
	// All timer channels have dataqsiz > 0.
	if c.timer != nil {
		c.timer.maybeRunChan(c)
	}
	return atomic.Loaduint(&c.qcount) == 0
}

// entry points for <- c from compiled code.
//
//go:nosplit
func chanrecv1(c *hchan, elem unsafe.Pointer) {
	chanrecv(c, elem, true, false, false)
}

//go:nosplit
func chanrecv2(c *hchan, elem unsafe.Pointer) (received bool) {
	_, received = chanrecv(c, elem, true, false, false)
	return
}

// entry points for <$ c from compiled code.
//
//go:nosplit
func chanrecv1sticky(c *hchan, elem unsafe.Pointer) {
	chanrecv(c, elem, true, true, false)
}

//go:nosplit
func chanrecv2sticky(c *hchan, elem unsafe.Pointer) (received bool) {
	_, received = chanrecv(c, elem, true, true, false)
	return
}

// entry points for <| c from compiled code.
//
//go:nosplit
func chanrecv1pipe(c *hchan, elem unsafe.Pointer) {
	chanrecv(c, elem, true, false, true)
}

//go:nosplit
func chanrecv2pipe(c *hchan, elem unsafe.Pointer) (received bool) {
	_, received = chanrecv(c, elem, true, false, true)
	return
}

// chanrecv receives on channel c and writes the received data to ep.
// ep may be nil, in which case received data is ignored.
// If block == false and no elements are available, returns (false, false).
// Otherwise, if c is closed, zeros *ep and returns (true, false).
// Otherwise, fills in *ep with an element and returns (true, true).
// A non-nil ep must point to the heap or the caller's stack.
func chanrecv(c *hchan, ep unsafe.Pointer, block, stickyrecv, piperecv bool) (selected, received bool) {
	// raceenabled: don't need to check ep, as it is always on the stack
	// or is new memory allocated by reflect.

	if debugChan {
		print("chanrecv: chan=", c, "\n")
	}

	if c == nil {
		if !block {
			return
		}
		gopark(nil, nil, waitReasonChanReceiveNilChan, traceBlockForever, 2)
		throw("unreachable")
	}

	if c.bubble != nil && getg().bubble != c.bubble {
		fatal("receive on synctest channel from outside bubble")
	}

	if c.timer != nil {
		c.timer.maybeRunChan(c)
	}

	if c.deleted != 0 {
		// receive on deleted channel.
		return
	}

	if piperecv && c.dataqsiz == 0 {
		// just a regular receive since channel is unbuffered.
		piperecv = false
	}

	//if stickyrecv {
	//	println("chanrecv: we might pop off a sticky wicket, because stickyrecv is true.")
	//}

	// Fast path: check for failed non-blocking operation without acquiring the lock.
	if !block && empty(c) {
		// After observing that the channel is not ready for receiving, we observe whether the
		// channel is closed.
		//
		// Reordering of these checks could lead to incorrect behavior when racing with a close.
		// For example, if the channel was open and not empty, was closed, and then drained,
		// reordered reads could incorrectly indicate "open and empty". To prevent reordering,
		// we use atomic loads for both checks, and rely on emptying and closing to happen in
		// separate critical sections under the same lock.  This assumption fails when closing
		// an unbuffered channel with a blocked send, but that is an error condition anyway.
		if atomic.Load(&c.closed) == 0 {
			// Because a channel cannot be reopened, the later observation of the channel
			// being not closed implies that it was also not closed at the moment of the
			// first observation. We behave as if we observed the channel at that moment
			// and report that the receive cannot proceed.
			return
		}

		if stickyrecv {
			panic(plainError("sticky-receive on closed channel would violate the immutability of the final channel value"))
		}

		// The channel is irreversibly closed. Re-check whether the channel has any pending data
		// to receive, which could have arrived between the empty and closed checks above.
		// Sequential consistency is also required here, when racing with such a send.
		if empty(c) {
			// The channel is irreversibly closed and empty.
			if raceenabled {
				raceacquire(c.raceaddr())
			}
			if ep != nil {
				typedmemclr(c.elemtype, ep)
			}
			return true, false
		}
	}

	var t0 int64
	if blockprofilerate > 0 {
		t0 = cputicks()
	}

	lock(&c.lock)

	isClosed := (c.closed != 0)

	//println("chanrecv() called. isClosed = ", isClosed)

	if piperecv {
		full := c.qcount == c.dataqsiz
		//println("piperecv seen; full =", full)
		if !full {
			// pipe receive can only receive on a full channel.
			// since the channel is less than full, we
			// must block (or not if we have default: i.e. block==false)
			goto blockOrNot
		}
		//println("full buffer detected with piperecv")
	}

	if isClosed {
		if stickyrecv {
			unlock(&c.lock)
			panic(plainError("sticky-receive on closed channel would violate the immutability of the final channel value"))
		}
		if c.qcount == 0 {
			if raceenabled {
				raceacquire(c.raceaddr())
			}
			unlock(&c.lock)
			if ep != nil {
				typedmemclr(c.elemtype, ep)
			}
			return true, false
		}
		// The channel has been closed, but the channel's buffer have data.
	} else {
		// channel is not closed

		// Just found waiting sender with not closed.
		if sg := c.sendq.dequeue(); sg != nil {
			// Found a waiting sender. If buffer is size 0, receive value
			// directly from sender. Otherwise, receive from head of queue
			// and add sender's value to the tail of the queue (both map to
			// the same buffer slot because the queue is full).

			// We are in chanrecv here.
			// The only other call to recv() is select.go:497
			// to implement the select statement.
			recv(c, sg, ep, func() { unlock(&c.lock) }, 3, stickyrecv)
			return true, true
		}
	}

	if c.qcount > 0 {
		// Receive directly from queue
		qp := chanbuf(c, c.recvx)
		if raceenabled {
			racenotify(c, c.recvx, nil)
		}
		if ep != nil {
			typedmemmove(c.elemtype, ep, qp)
		}
		if c.final > 0 && c.final-1 == c.recvx {
			if c.closed == 0 {
				//println("doing auto-close b/c reached c.final = ", c.final)
				c.closed = 1
			}
		}
		if c.sticky > 0 {
			if c.sticky-1 == c.recvx {
				// A no-op: this is the essence of being sticky.
				// A receive from a queue whose head value
				// is sticky does not consume the value. It
				// stays put at the head of the queue. That's
				// why it is called sticky.

				if stickyrecv {
					// A sticky receive, <$c cancels it.
					// A sticky receive pops off a sticky
					// value at the head, cancelling any
					// further sticky receives until
					// a new sticky value is enqueued and reaches
					// the head.
					// i.e. a normal receive ignoring the sticky value.
					c.sticky = 0

				} else {

					// When a channel is sticky (has a sticky head
					// value), we still allow the closed status
					// to convey its bit, but now we think of
					// it as the "mutable" bit, rather than
					// the "closed" bit. The 2nd return value tells
					// us if the value might change in the future.
					// If it is closed, the value is now immutable,
					// and will never change in the future.
					//
					// A receive from a closed channel will
					// always return as its first value the last
					// (sticky) value it held before being closed.
					// As usual, this is the zero value if the
					// channel is empty.
					//
					// The optional second return value from
					// a channel receive will always be
					// false for a closed channel, and this has
					// only changed the meaning of false: now we
					// say the false mutation bit is saying this
					// channel is closed to any further mutation.
					//println("recv: no-op due to sticky value at c.recvx = ", c.recvx, " ; c.sticky = ", c.sticky)
					unlock(&c.lock)
					return true, !isClosed
				}
			}
		}
		typedmemclr(c.elemtype, qp)
		c.recvx++
		if c.recvx == c.dataqsiz {
			c.recvx = 0
		}
		c.qcount--
		unlock(&c.lock)
		return true, true
	}

blockOrNot:
	if !block {
		unlock(&c.lock)
		return false, false
	}

	// no sender available: block on this channel.
	gp := getg()
	mysg := acquireSudog()
	mysg.releasetime = 0
	if t0 != 0 {
		mysg.releasetime = -1
	}
	// No stack splits between assigning elem and enqueuing mysg
	// on gp.waiting where copystack can find it.
	mysg.elem.set(ep)
	mysg.waitlink = nil
	gp.waiting = mysg

	mysg.g = gp
	mysg.isSelect = false

	mysg.stickyrecv = stickyrecv
	mysg.piperecv = piperecv
	//if raceenabled {
	//	if piperecv {
	//		// might not be needed?
	//		//raceacquire(unsafe.Pointer(&c.fullpipe))
	//		//racerelease(unsafe.Pointer(&c.fullpipe))
	//	}
	//}
	mysg.c.set(c)
	gp.param = nil
	c.recvq.enqueue(mysg)
	if c.timer != nil {
		blockTimerChan(c)
	}

	// Signal to anyone trying to shrink our stack that we're about
	// to park on a channel. The window between when this G's status
	// changes and when we set gp.activeStackChans is not safe for
	// stack shrinking.
	gp.parkingOnChan.Store(true)
	reason := waitReasonChanReceive
	if c.bubble != nil {
		reason = waitReasonSynctestChanReceive
	}
	gopark(chanparkcommit, unsafe.Pointer(&c.lock), reason, traceBlockChanRecv, 2)

	// someone woke us up
	if mysg != gp.waiting {
		throw("G waiting list is corrupted")
	}
	if c.timer != nil {
		unblockTimerChan(c)
	}

	//if raceenabled {
	//	if piperecv {
	//		// might not be needed.
	//		//racerelease(unsafe.Pointer(&c.fullpipe))
	//		//raceacquire(unsafe.Pointer(&c.fullpipe))
	//	}
	//}

	gp.waiting = nil
	gp.activeStackChans = false
	if mysg.releasetime > 0 {
		blockevent(mysg.releasetime-t0, 2)
	}
	success := mysg.success
	gp.param = nil
	mysg.c.set(nil)
	releaseSudog(mysg)
	return true, success
}

// recv processes a receive operation on a full channel c.
// There are 2 parts:
//  1. The value sent by the sender sg is put into the channel
//     and the sender is woken up to go on its merry way.
//  2. The value received by the receiver (the current G) is
//     written to ep.
//
// For synchronous channels, both values are the same.
// For asynchronous channels, the receiver gets its data from
// the channel buffer and the sender's data is put in the
// channel buffer.
// Channel c must be full and locked. recv unlocks c with unlockf.
// sg must already be dequeued from c.
// A non-nil ep must point to the heap or the caller's stack.
//
// TODO(jea) For now pipe-receive does its own in helper, b/c it has
// to fire on a send rather than an active receive. Could it use
// recv() instead? not sure what the sg for the sender would be...
func recv(c *hchan, sg *sudog, ep unsafe.Pointer, unlockf func(), skip int, stickyrecv bool) {
	if c.bubble != nil && getg().bubble != c.bubble {
		unlockf()
		fatal("receive on synctest channel from outside bubble")
	}
	if c.dataqsiz == 0 {
		if raceenabled {
			racesync(c, sg)
		}
		if ep != nil {
			// copy data from sender
			recvDirect(c.elemtype, sg, ep)
		}
	} else {

		// Queue is full. Take the item at the
		// head of the queue. Make the sender enqueue
		// its item at the tail of the queue. Since the
		// queue is full, those are both the same slot.

		head := chanbuf(c, c.recvx)
		if raceenabled {
			racenotify(c, c.recvx, nil)
			racenotify(c, c.recvx, sg)
		}

		if c.sticky > 0 {
			// we have a sticky value somewhere in the queue.
			finalSend := sg.finalsend
			if finalSend {
				if c.closed == 0 {
					c.closed = 1
				}
			}

			if c.recvx == c.sticky-1 {

				// The sticky value has made its way to the
				// head or front of the queue where it can
				// be received again and again.

				// Below we will either replace it in place,
				// or delete it.

				// Note: stickyrecv only matters for these
				// next two cases -- when the sticky value
				// is at the head. Otherwise it is just
				// like a regular receive. In fact it negatives
				// the sg.stickysend.
				stickySend := sg.stickysend
				if stickySend && stickyrecv {
					//println("recv: no more sticky wickets: stickyrecv true")
					stickySend = false
				}

				if stickySend {
					// the send is sticky: we want to replace
					// the sticky value in place, at the head.

					// copy data from sender to queue, overwriting old sticky value.
					typedmemmove(c.elemtype, head, sg.elem.get())
					// copy (the same new data) from sender to receiver
					if ep != nil {
						typedmemmove(c.elemtype, ep, sg.elem.get())
					}
					// leave c.recvx on the sticky index.
					// c.qcount stays 1.
					// c.sendx stays the same.

				} else {
					// The sticky value is at the head of the queue,
					// and the new send is not sticky. The new
					// regular send will remove the sticky value,
					// and get transmitted to the receiver, leaving
					// the queue empty. (There is never anything
					// after a sticky value in the queue).
					//
					// The sticky value is like a balloon that
					// gets popped and disappears when met with
					// the pin that is a regular send (<-).
					typedmemclr(c.elemtype, head)
					c.sticky = 0
					c.recvx = 0
					c.sendx = 0
					c.qcount = 0

					// copy data from sender to receiver
					if ep != nil {
						typedmemmove(c.elemtype, ep, sg.elem.get())
					}
				}

			} else {
				// the current in-queue sticky value is not at the head.
				// This also means our c.qcount is >= 2.

				stickyp := chanbuf(c, c.sticky-1)

				// How to handle sticky <$ sticky if the first sticky
				// is not yet at the head of the queue? We replace the
				// old sticky value with the new sticky value.

				// It turns out the regular send case is the same.
				// With sticky not at head, and we are doing
				// sticky <- regular send. We also pop the
				// sticky balloon and write new value over top of it.

				// copy data from sender to queue, overwriting old sticky value.
				typedmemmove(c.elemtype, stickyp, sg.elem.get())

				// copy data from head to receiver, completing the receive.
				if ep != nil {
					typedmemmove(c.elemtype, ep, head)
				}
				// the receive consumes the head of the queue.
				typedmemclr(c.elemtype, head)
				c.recvx++
				if c.recvx == c.dataqsiz {
					c.recvx = 0
				}
				// The queue size will shrink by one, since
				// c.sendx stays the same
				c.qcount--
			}
		} else {
			// regular path, no sticky situations.

			// copy data from queue to receiver
			if ep != nil {
				typedmemmove(c.elemtype, ep, head)
			}
			// copy data from sender to queue
			typedmemmove(c.elemtype, head, sg.elem.get())
			c.recvx++
			if c.recvx == c.dataqsiz {
				c.recvx = 0
			}
			c.sendx = c.recvx // c.sendx = (c.sendx+1) % c.dataqsiz
		}
	}
	sg.elem.set(nil)
	gp := sg.g
	unlockf()
	gp.param = unsafe.Pointer(sg)
	sg.success = true
	if sg.releasetime != 0 {
		sg.releasetime = cputicks()
	}
	goready(gp, skip+1)
}

func chanparkcommit(gp *g, chanLock unsafe.Pointer) bool {
	// There are unlocked sudogs that point into gp's stack. Stack
	// copying must lock the channels of those sudogs.
	// Set activeStackChans here instead of before we try parking
	// because we could self-deadlock in stack growth on the
	// channel lock.
	gp.activeStackChans = true
	// Mark that it's safe for stack shrinking to occur now,
	// because any thread acquiring this G's stack for shrinking
	// is guaranteed to observe activeStackChans after this store.
	gp.parkingOnChan.Store(false)
	// Make sure we unlock after setting activeStackChans and
	// unsetting parkingOnChan. The moment we unlock chanLock
	// we risk gp getting readied by a channel operation and
	// so gp could continue running before everything before
	// the unlock is visible (even to gp itself).
	unlock((*mutex)(chanLock))
	return true
}

// compiler implements
//
//	select {
//	case c <- v:
//		... foo
//	default:
//		... bar
//	}
//
// as
//
//	if selectnbsend(c, v) {
//		... foo
//	} else {
//		... bar
//	}
func selectnbsend(c *hchan, elem unsafe.Pointer) (selected bool) {
	return chansend(c, elem, false, sys.GetCallerPC(), false, false)
}

func selectnbsendSticky(c *hchan, elem unsafe.Pointer) (selected bool) {
	return chansend(c, elem, false, sys.GetCallerPC(), true, false)
}

func selectnbsendStickyFinal(c *hchan, elem unsafe.Pointer) (selected bool) {
	return chansend(c, elem, false, sys.GetCallerPC(), true, true)
}

// compiler implements
//
//	select {
//	case v, ok = <-c:
//		... foo
//	default:
//		... bar
//	}
//
// as
//
//	if selected, ok = selectnbrecv(&v, c); selected {
//		... foo
//	} else {
//		... bar
//	}
func selectnbrecv(elem unsafe.Pointer, c *hchan) (selected, received bool) {
	return chanrecv(c, elem, false, false, false)
}

func selectnbrecvSticky(elem unsafe.Pointer, c *hchan) (selected, received bool) {
	return chanrecv(c, elem, false, true, false)
}

func selectnbrecvPipe(elem unsafe.Pointer, c *hchan) (selected, received bool) {
	return chanrecv(c, elem, false, false, true)
}

//go:linkname reflect_chansend reflect.chansend0
func reflect_chansend(c *hchan, elem unsafe.Pointer, nb, sticky, final bool) (selected bool) {
	return chansend(c, elem, !nb, sys.GetCallerPC(), sticky, final)
}

//go:linkname reflect_chanrecv reflect.chanrecv
func reflect_chanrecv(c *hchan, nb bool, elem unsafe.Pointer, sticky, pipe bool) (selected bool, received bool) {
	return chanrecv(c, elem, !nb, sticky, pipe)
}

func chanlen(c *hchan) int {
	if c == nil {
		return 0
	}
	if c.deleted != 0 {
		// len of deleted chan
		return 0
	}
	async := debug.asynctimerchan.Load() != 0
	if c.timer != nil && async {
		c.timer.maybeRunChan(c)
	}
	if c.timer != nil && !async {
		// timer channels have a buffered implementation
		// but present to users as unbuffered, so that we can
		// undo sends without users noticing.
		return 0
	}
	return int(c.qcount)
}

func chancap(c *hchan) int {
	if c == nil {
		return 0
	}
	if c.deleted != 0 {
		// cap of deleted channel
		return 0
	}

	if c.timer != nil {
		async := debug.asynctimerchan.Load() != 0
		if async {
			return int(c.dataqsiz)
		}
		// timer channels have a buffered implementation
		// but present to users as unbuffered, so that we can
		// undo sends without users noticing.
		return 0
	}
	return int(c.dataqsiz)
}

//go:linkname reflect_chanlen reflect.chanlen
func reflect_chanlen(c *hchan) int {
	return chanlen(c)
}

//go:linkname reflectlite_chanlen internal/reflectlite.chanlen
func reflectlite_chanlen(c *hchan) int {
	return chanlen(c)
}

//go:linkname reflect_chancap reflect.chancap
func reflect_chancap(c *hchan) int {
	return chancap(c)
}

//go:linkname reflect_chanclose reflect.chanclose
func reflect_chanclose(c *hchan) {
	closechan(c)
}

func (q *waitq) enqueue(sgp *sudog) {
	sgp.next = nil
	x := q.last
	if x == nil {
		sgp.prev = nil
		q.first = sgp
		q.last = sgp
		return
	}
	sgp.prev = x
	x.next = sgp
	q.last = sgp
}

func (q *waitq) dequeue() *sudog {
	for {
		sgp := q.first
		if sgp == nil {
			return nil
		}
		y := sgp.next
		if y == nil {
			q.first = nil
			q.last = nil
		} else {
			y.prev = nil
			q.first = y
			sgp.next = nil // mark as removed (see dequeueSudoG)
		}

		// if a goroutine was put on this queue because of a
		// select, there is a small window between the goroutine
		// being woken up by a different case and it grabbing the
		// channel locks. Once it has the lock
		// it removes itself from the queue, so we won't see it after that.
		// We use a flag in the G struct to tell us when someone
		// else has won the race to signal this goroutine but the goroutine
		// hasn't removed itself from the queue yet.
		if sgp.isSelect {
			if !sgp.g.selectDone.CompareAndSwap(0, 1) {
				// We lost the race to wake this goroutine.
				continue
			}
		}

		return sgp
	}
}

func (c *hchan) raceaddr() unsafe.Pointer {
	// Treat read-like and write-like operations on the channel to
	// happen at this address. Avoid using the address of qcount
	// or dataqsiz, because the len() and cap() builtins read
	// those addresses, and we don't want them racing with
	// operations like close().
	return unsafe.Pointer(&c.buf)
}

func racesync(c *hchan, sg *sudog) {
	racerelease(chanbuf(c, 0))
	raceacquireg(sg.g, chanbuf(c, 0))
	racereleaseg(sg.g, chanbuf(c, 0))
	raceacquire(chanbuf(c, 0))
}

// Notify the race detector of a send or receive involving buffer entry idx
// and a channel c or its communicating partner sg.
// This function handles the special case of c.elemsize==0.
func racenotify(c *hchan, idx uint, sg *sudog) {
	// We could have passed the unsafe.Pointer corresponding to entry idx
	// instead of idx itself.  However, in a future version of this function,
	// we can use idx to better handle the case of elemsize==0.
	// A future improvement to the detector is to call TSan with c and idx:
	// this way, Go will continue to not allocating buffer entries for channels
	// of elemsize==0, yet the race detector can be made to handle multiple
	// sync objects underneath the hood (one sync object per idx)
	qp := chanbuf(c, idx)
	// When elemsize==0, we don't allocate a full buffer for the channel.
	// Instead of individual buffer entries, the race detector uses the
	// c.buf as the only buffer entry.  This simplification prevents us from
	// following the memory model's happens-before rules (rules that are
	// implemented in racereleaseacquire).  Instead, we accumulate happens-before
	// information in the synchronization object associated with c.buf.
	if c.elemsize == 0 {
		if sg == nil {
			raceacquire(qp) // read
			racerelease(qp) // write => read+write is full barrier to next acquire.
		} else {
			raceacquireg(sg.g, qp) // read
			racereleaseg(sg.g, qp) // write
		}
	} else {
		if sg == nil {
			racereleaseacquire(qp) // write-read
		} else {
			racereleaseacquireg(sg.g, qp) // write-read
		}
	}
}

// entry point for clear(c) from compiled code.
//
//go:nosplit
func clearchan(c *hchan) {
	if c == nil {
		return
	}
	if c.deleted != 0 {
		// clear of deleted channel
		return
	}
	lock(&c.lock)

	if c.deleted != 0 {
		unlock(&c.lock)
		// clear of deleted channel
		return
	}

	// clearing a closed "immutable" channel leads to panic.
	if c.closed != 0 {
		unlock(&c.lock)
		// since send on a closed channel is a panic,
		// for detecting code immutability violations,
		// we will make clear of a closed channel
		// also panic.
		panic(plainError("clear of closed channel"))
	}

	c.sticky = 0
	for c.qcount > 0 {
		typedmemclr(c.elemtype, chanbuf(c, c.recvx))
		c.recvx++
		if c.recvx == c.dataqsiz {
			c.recvx = 0
		}
		c.qcount--
	}
	// critical to do:
	c.sendx = 0
	c.recvx = 0

	unlock(&c.lock)
}

// entry point for delete(c) from compiled code.
//
//go:nosplit
func deletechan(c *hchan) {
	if c == nil {
		return
	}
	lock(&c.lock)
	if c.deleted != 0 {
		unlock(&c.lock)
		return
	}
	c.deleted = 1
	for c.qcount > 0 {
		typedmemclr(c.elemtype, chanbuf(c, c.recvx))
		c.recvx++
		if c.recvx == c.dataqsiz {
			c.recvx = 0
		}
		c.qcount--
	}
	unlock(&c.lock)
}
