// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package os

import (
	"syscall"
	"time"
)

// onethreadPidWait reaps the process cooperatively under -onethread, where a
// blocking Wait4 would freeze the sole OS thread. It polls with Wait4(WNOHANG)
// and yields between attempts via a short, capped timer backoff (time.Sleep is
// cooperative under -onethread, parking the goroutine on a runtime timer), so
// other goroutines and the netpoller keep running while the child is alive.
//
// This is the fallback / Darwin path; Linux normally uses the poller-backed
// pidfd path (pidfdWait).
func (p *Process) onethreadPidWait() (*ProcessState, error) {
	var (
		status syscall.WaitStatus
		rusage syscall.Rusage
	)
	backoff := 200 * time.Microsecond
	const maxBackoff = 20 * time.Millisecond
	for {
		wpid, err := ignoringEINTR2(func() (int, error) {
			return syscall.Wait4(p.Pid, &status, syscall.WNOHANG, &rusage)
		})
		if err != nil {
			return nil, NewSyscallError("wait", err)
		}
		if wpid != 0 {
			p.doRelease(statusDone)
			return &ProcessState{
				pid:    wpid,
				status: status,
				rusage: &rusage,
			}, nil
		}
		// Child still running; yield cooperatively and re-check.
		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}
