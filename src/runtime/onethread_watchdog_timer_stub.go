// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !((linux && (amd64 || arm64 || riscv64)) || (darwin && (amd64 || arm64)))

package runtime

// The -onethread experiment (and thus its syscall-stall watchdog) targets only
// linux on amd64/arm64/riscv64 and darwin on amd64/arm64. Elsewhere arming is
// a no-op so runtime.main compiles on every platform.
func onethreadArmWatchdog() {}
