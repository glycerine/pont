// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (js && wasm) || wasip1

package os

import "errors"

// onethreadPidWait is unreachable here: -onethread is not supported on js/wasm
// or wasip1 (goexperiment.Onethread is false, so pidWait never calls this), and
// these platforms lack Wait4/WNOHANG. It exists only so exec_unix.go compiles.
func (p *Process) onethreadPidWait() (*ProcessState, error) {
	return nil, errors.New("os: onethread process wait not supported on this platform")
}
