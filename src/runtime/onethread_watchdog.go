// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package runtime

// GODEBUG=onethreadwatchdog=N (milliseconds) diagnostic for -onethread.
//
// With a single OS thread, an unbounded blocking syscall (regular-file I/O on a
// slow filesystem, flock, a blocking cgo call, ...) freezes the whole runtime
// with no second thread to notice. These paths are documented as inherent
// limitations (ONE_THREAD_PLAN.md §15.6); the watchdog turns such a silent hang
// into a loud, diagnosable crash.
//
// When armed (see onethreadArmWatchdog in onethread_watchdog_timer.go), a
// repeating ITIMER_REAL fires SIGALRM every N ms. The handler (sighandler ->
// onethreadWatchdogTick) crashes if the sole thread has been blocked in the
// same syscall or cgo call across a full interval, since that means no
// goroutine has been able to run.

// onethreadWatchdogOn reports whether the watchdog is armed. Set by
// onethreadArmWatchdog and read from the SIGALRM path in sighandler.
var onethreadWatchdogOn bool

const (
	onethreadWdNone = iota
	onethreadWdSyscall
	onethreadWdCgo
)

// Previous-tick state. Touched only by the SIGALRM handler, which runs on the
// sole thread, so no synchronization is needed.
var (
	onethreadWdKind int8
	onethreadWdID   uint64
)

// onethreadWatchdogTick is invoked from sighandler on each watchdog SIGALRM.
// gp is the goroutine the alarm interrupted. It does not return if it detects a
// stall; otherwise it records the current state for comparison next tick.
func onethreadWatchdogTick(gp *g) {
	mp := gp.m

	kind := int8(onethreadWdNone)
	var id uint64
	switch {
	case readgstatus(gp)&^_Gscan == _Gsyscall:
		// mp.syscalltick changes on every entersyscall, so the same value at
		// two consecutive ticks means the very same syscall is still in flight
		// (distinguishing a genuine stall from a tight loop of quick syscalls).
		kind, id = onethreadWdSyscall, uint64(mp.syscalltick)
	case mp.incgo:
		// ncgocall changes on every outbound cgo call, used the same way.
		kind, id = onethreadWdCgo, uint64(mp.ncgocall)
	}

	if kind == onethreadWdNone {
		// Running Go code, or legitimately idle in netpoll: healthy.
		onethreadWdKind = onethreadWdNone
		return
	}
	if onethreadWdKind == kind && onethreadWdID == id {
		// The same blocking operation was already in progress a full watchdog
		// interval ago and is still here: the sole thread is stalled.
		if kind == onethreadWdCgo {
			throw("onethread: sole OS thread stalled in a blocking cgo call (GODEBUG=onethreadwatchdog); the runtime is frozen")
		}
		throw("onethread: sole OS thread stalled in a blocking syscall (GODEBUG=onethreadwatchdog); the runtime is frozen")
	}
	onethreadWdKind = kind
	onethreadWdID = id
}
