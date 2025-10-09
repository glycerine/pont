// run

// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Test close(c), receive of closed sticky channel.

package main

import (
	"fmt"
	"runtime"
)

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

	ch := make(chan int, 1)
	go func() {
		ch <$ 9
	}()
	v, ok := <-ch
	fmt.Printf("expect 9: v=%v, ok = %v\n", v, ok)
	panicneq(v, 9)
	panicneqb(ok, true)

	clear(ch)
	go func() {
		ch <- 12
	}()
	v, ok = <-ch
	fmt.Printf("expect 12: v=%v, ok = %v\n", v, ok)
	panicneq(v, 12) // got 9, want 12
	panicneqb(ok, true)

	clear(ch)
	go func() {
		select {
		case ch <- 77:
		}
	}()
	v, ok = <-ch
	fmt.Printf("expect 77: v=%v, ok = %v\n", v, ok)
	panicneq(v, 77) // got 9, want 12
	panicneqb(ok, true)

	// now with full channel already, not starting from empty

	go func() {
		select {
		case ch <- 88:
		}
	}()
	runtime.Gosched()
	v, ok = <-ch
	fmt.Printf("expect 88: v=%v, ok = %v\n", v, ok)
	panicneq(v, 88) // got 9, want 12
	panicneqb(ok, true)

	go func() {
		select {
		case ch <$ 99:
		}
	}()
	runtime.Gosched()
	v, ok = <-ch
	fmt.Printf("expect 99: v=%v, ok = %v\n", v, ok)
	panicneq(v, 99) // got 9, want 12
	panicneqb(ok, true)

	go func() {
		select {
		case ch <$ 101:
		}
	}()
	runtime.Gosched()
	v, ok = <-ch
	fmt.Printf("expect 101: v=%v, ok = %v\n", v, ok)
	panicneq(v, 101) // got 9, want 12
	panicneqb(ok, true)

}
