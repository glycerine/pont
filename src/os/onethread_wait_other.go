// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (unix || (js && wasm) || wasip1) && !darwin

package os

// onethreadWaitPidReady has no poller-backed implementation on this platform.
// On Linux the pidfd path (pidfdWait) handles -onethread waits, so pidWait is
// only the rare fallback there; elsewhere the blocking Wait4 in pidWait still
// blocks the sole thread, as documented in ONE_THREAD_PLAN.md §15.6.
func onethreadWaitPidReady(pid int) error { return nil }
