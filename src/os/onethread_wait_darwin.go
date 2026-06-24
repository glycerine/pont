// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin

package os

import (
	"internal/poll"
	"syscall"
)

// onethreadWaitPidReady blocks the calling goroutine cooperatively, via the
// runtime poller, until pid has exited and can be reaped without blocking.
//
// Darwin has no pidfd, so pidWait would otherwise issue a blocking Wait4 that
// freezes the sole OS thread under -onethread. Instead we watch the process
// with a private kqueue (EVFILT_PROC / NOTE_EXIT) and poll that kqueue fd
// through the runtime poller: a kqueue descriptor becomes readable when it has
// a pending event (the standard "nested kqueue" technique), so the runtime
// wakes us when the child exits.
//
// On any setup error it returns nil so the caller falls back to a blocking
// reap — no worse than the pre-onethread behavior.
func onethreadWaitPidReady(pid int) error {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil // fall back to blocking Wait4
	}
	syscall.CloseOnExec(kq)

	// Register interest in the process exiting. A zero timeout makes this a
	// non-blocking registration. If the process is already gone the change
	// fails (e.g. ESRCH); in that case fall back to the blocking reap, which
	// returns immediately for a zombie.
	change := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: int16(syscall.EVFILT_PROC),
		Flags:  uint16(syscall.EV_ADD | syscall.EV_CLEAR),
		Fflags: uint32(syscall.NOTE_EXIT),
	}
	var zero syscall.Timespec
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{change}, nil, &zero); err != nil {
		syscall.Close(kq)
		return nil
	}

	// Poll the kqueue fd through the runtime poller. It becomes readable when
	// the registered NOTE_EXIT fires (or is already pending, caught by the
	// first probe below).
	pfd := poll.FD{Sysfd: kq}
	if err := pfd.Init("kqueue", true); err != nil {
		syscall.Close(kq)
		return nil // poller can't take the kqueue fd: fall back to blocking reap
	}
	defer pfd.Close() // closes kq and unregisters it from the poller

	var events [1]syscall.Kevent_t
	return pfd.RawRead(func(fd uintptr) bool {
		// Non-blocking drain: has NOTE_EXIT been delivered?
		n, err := syscall.Kevent(int(fd), nil, events[:], &zero)
		if err != nil {
			return true // stop; the caller's reap will surface any error
		}
		return n > 0
	})
}
