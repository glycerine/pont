// Copyright 2026 The Pont Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import "internal/goexperiment"

const onethreadMinCStack = 1 << 20

func onethreadCheckCStack() {
	if !goexperiment.Onethread {
		return
	}
	gp := getg()
	if gp.m != &m0 || gp.m.g0 != &g0 {
		throw("onethread: runtime did not start on m0/g0")
	}
	if gp.m.g0.stack.hi <= gp.m.g0.stack.lo ||
		gp.m.g0.stack.hi-gp.m.g0.stack.lo < onethreadMinCStack {
		throw("onethread: constructed C stack smaller than 1 MiB")
	}
}
