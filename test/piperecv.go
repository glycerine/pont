// run

// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Test close(c), receive of closed sticky channel.

package main

import (
	"fmt"
)

var _ = fmt.Printf

func main() {

	panicneq := func(got, want int) {
		if got != want {
			panic(fmt.Sprintf("got %v, want %v", got, want))
		}
	}
	panicneqb := func(got, want bool) {
		if got != want {
			panic(fmt.Sprintf("got %v, want %v", got, want))
		}
	}
	_ = panicneq
	_ = panicneqb
	//println("begin stickrecv.go test program:")

	//fmt.Printf("xunOps = '%#v'; xunOps[RecvSticky] = %v\n", xunOps, xunOps[RecvSticky])

	ch := make(chan int, 3)

	select {
	case <|ch:
		panic("premature pipe receive, ch not full")
	default:
	}
	ch <- 1

	select {
	case <|ch:
		panic("premature pipe receive, ch not full")
	default:
	}
	ch <- 2

	select {
	case <|ch:
		panic("premature pipe receive, ch not full")
	default:
	}
	ch <- 3

	select {
	case a := <|ch:
		fmt.Printf("good: pipe receive got a = %v\n", a)
		if a != 1 {
			panic(fmt.Sprintf("expected 1, got %v", a))
		}
	default:
		panic("ch full, but did not pipe receive!")
	}

	// now channel is not full, so pipe receive should block again.
	select {
	case <|ch:
		panic("premature pipe receive, ch not full")
	default:
	}

	fmt.Printf("got to end!")
}
