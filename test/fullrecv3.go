// run

// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Test full-receive <| replacement for sync.WaitGroup

package main

import (
	"fmt"
)

var _ = fmt.Printf

func main() {
	// for race detector; try to provoke races
	N := uint64(100)
	a := make([]int, N)
	b := make(chan uint64, N)
	for i := uint64(0); i < N; i++ {
		go func(j uint64) {
			for range 1_000 {
				a[j]++ // race detector false alarm was on this write.
			}
			b <- j // must happen-before our <|b full receive.
		}(i)
	}
	<|b
	// face detector used to false alarm this read of a.
	fmt.Printf("a = '%v'", a)
}
