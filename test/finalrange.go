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

	ch := make(chan int, 4)

	ch <- 1
	ch <- 2
	ch <# 3

	for i := range ch {
		fmt.Printf("got %v\n", i)
	}
	fmt.Printf("close val is %v\n", <-ch)
	return

	a, open := <-ch // 1
	if a != 1 {
		panic(fmt.Sprintf("want 1, got %v\n", a))
	}
	if !open {
		panic("channel should still be open")
	}

	a, open = <-ch // 2
	if a != 2 {
		panic(fmt.Sprintf("want 2, got %v\n", a))
	}
	if !open {
		panic("channel should still be open")
	}

	a, open = <-ch // 3
	if a != 3 {
		panic(fmt.Sprintf("want 3, got %v\n", a))
	}
	if open {
		panic("channel should be closed")
	}
	fmt.Printf("good, on 3rd recv, got a=%v, open=%v; len is now %v\n", a, open, len(ch))

	fmt.Printf("about to try the read after final auto-close.\n")
	a, open = <-ch // 3
	if a != 3 {
		panic(fmt.Sprintf("want 3, got %v\n", a))
	}
	if open {
		panic("channel should be closed now")
	}

	fmt.Printf("got to end!")
}
