// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !js && !wasip1

package runtime

import (
	"internal/goexperiment"
	"unsafe"
)

// Cooperative one-shot note waiting for the -onethread runtime.
//
// In a normal multi-threaded runtime, a user goroutine that must wait on a
// note (notably signal_recv waiting for an OS signal) blocks its M in a futex
// or semaphore via entersyscallblock. That is impossible under -onethread:
// blocking the sole OS thread would freeze every other goroutine, and
// entersyscallblock throws.
//
// Instead, the waiting goroutine goparks (so the scheduler can run other
// goroutines and block interruptibly in netpoll) and is readied by the
// scheduler when the note is woken. The wake side is unchanged: notewakeup
// sets the note key (an async-signal-safe atomic store, important because the
// signal handler calls it). The scheduler then observes the set key at its
// next checkpoint and readies the parked goroutine.
//
// The scan runs from two scheduler hooks already present for the js/wasm port:
//   - checkTimeouts, called on every gopark/Gosched/goyield, so a waiter is
//     readied promptly whenever any other goroutine yields; and
//   - beforeIdle, called in findRunnable before the thread blocks in netpoll,
//     so a waiter woken by a signal that interrupted netpoll is readied on the
//     next scheduler loop.
//
// onethreadNoteWoken reports whether a note has been woken. It is defined per
// note implementation (lock_futex.go, lock_sema.go).

type onethreadNoteWaiter struct {
	n  *note
	gp *g
}

// onethreadNoteWaiters is the set of goroutines parked in notetsleepg_onethread.
// It is only ever accessed by the sole OS thread in non-signal context
// (registration, removal and scanning all happen there), so it needs no lock.
// The signal handler never touches it; it only sets note keys via notewakeup.
var onethreadNoteWaiters []onethreadNoteWaiter

// onethreadHasNoteWaiters reports whether any goroutine is parked waiting on a
// note. The scheduler uses this to keep blocking in (signal-interruptible)
// netpoll rather than parking the M in notesleep, where signals are ignored.
func onethreadHasNoteWaiters() bool {
	return goexperiment.Onethread && len(onethreadNoteWaiters) > 0
}

func onethreadAddNoteWaiter(n *note, gp *g) {
	// Ensure the network poller is initialized. The idle scheduler blocks in
	// netpoll (which a delivered signal interrupts via EINTR) so it can rescan
	// note waiters; without an initialized poller it would park the M in
	// notesleep instead and miss the signal. Programs that do any I/O init the
	// poller anyway; this covers the signal-only case.
	if !netpollinited() {
		netpollGenericInit()
	}
	onethreadNoteWaiters = append(onethreadNoteWaiters, onethreadNoteWaiter{n, gp})
}

func onethreadRemoveNoteWaiter(gp *g) {
	ws := onethreadNoteWaiters
	for i := range ws {
		if ws[i].gp == gp {
			last := len(ws) - 1
			ws[i] = ws[last]
			ws[last] = onethreadNoteWaiter{}
			onethreadNoteWaiters = ws[:last]
			return
		}
	}
}

// onethreadScanNotes readies every parked note waiter whose note has been
// woken, and reports whether it readied any. It runs only on the sole thread.
func onethreadScanNotes() bool {
	readied := false
	ws := onethreadNoteWaiters
	for i := 0; i < len(ws); {
		if onethreadNoteWoken(ws[i].n) {
			gp := ws[i].gp
			last := len(ws) - 1
			ws[i] = ws[last]
			ws[last] = onethreadNoteWaiter{}
			ws = ws[:last]
			onethreadNoteWaiters = ws
			goready(gp, 1)
			readied = true
			// Do not advance i: a fresh entry now occupies slot i.
		} else {
			i++
		}
	}
	return readied
}

// onethreadNoteParkCommit is the gopark commit function for
// notetsleepg_onethread. It runs after the goroutine is marked waiting but
// before it stops; returning false aborts the park (so a wakeup that raced with
// registration is not lost).
func onethreadNoteParkCommit(gp *g, np unsafe.Pointer) bool {
	if onethreadNoteWoken((*note)(np)) {
		onethreadRemoveNoteWaiter(gp)
		return false // already woken: do not park
	}
	return true // park
}

// notetsleepg_onethread is the -onethread implementation of notetsleepg: it
// waits cooperatively instead of blocking the sole OS thread. It supports only
// the indefinite wait (ns < 0), which is all that production callers
// (signal_recv, the profiling buffer reader) use.
func notetsleepg_onethread(n *note, ns int64) bool {
	gp := getg()
	if gp == gp.m.g0 {
		throw("notetsleepg on g0")
	}
	if ns >= 0 {
		throw("onethread: notetsleepg with a deadline is not supported")
	}

	if onethreadNoteWoken(n) {
		return true
	}
	onethreadAddNoteWaiter(n, gp)
	gopark(onethreadNoteParkCommit, unsafe.Pointer(n), waitReasonZero, traceBlockGeneric, 1)
	// By the time we are scheduled again the waiter has been removed (by the
	// scan that readied us, or by the commit function if the park was aborted);
	// remove defensively in case of any other wakeup path.
	onethreadRemoveNoteWaiter(gp)
	return onethreadNoteWoken(n)
}
