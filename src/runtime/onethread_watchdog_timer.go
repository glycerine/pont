// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (linux || darwin) && (amd64 || arm64)

package runtime

import (
	"internal/abi"
	"internal/goexperiment"
	"internal/runtime/atomic"
)

// onethreadArmWatchdog installs the GODEBUG=onethreadwatchdog stall detector: a
// repeating ITIMER_REAL whose SIGALRM is handled by sighandler ->
// onethreadWatchdogTick. It is a no-op unless -onethread is active and the
// GODEBUG is a positive number of milliseconds.
//
// SIGALRM is commandeered while the watchdog is on. This is a diagnostic mode
// (and Go's own timers do not use SIGALRM), so that is acceptable.
func onethreadArmWatchdog() {
	if !goexperiment.Onethread || debug.onethreadwatchdog <= 0 {
		return
	}

	// Install the Go signal handler for SIGALRM, recording any prior handler
	// (mirrors setProcessCPUProfilerTimer for SIGPROF).
	if atomic.Cas(&handlingSig[_SIGALRM], 0, 1) {
		h := getsig(_SIGALRM)
		if h == _SIG_DFL {
			h = _SIG_IGN
		}
		atomic.Storeuintptr(&fwdSig[_SIGALRM], h)
		setsig(_SIGALRM, abi.FuncPCABIInternal(sighandler))
	}
	unblocksig(_SIGALRM)
	onethreadWatchdogOn = true

	ms := int64(debug.onethreadwatchdog)
	var it itimerval
	it.it_interval.tv_sec = ms / 1000
	it.it_interval.set_usec(int32((ms % 1000) * 1000))
	it.it_value = it.it_interval
	setitimer(_ITIMER_REAL, &it, nil)
}
