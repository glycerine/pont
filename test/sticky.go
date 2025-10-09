// run

// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Test close(c), receive of closed sticky channel.

package main

import (
	"fmt"
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

	ch <$ 9
	i := <-ch
	fmt.Printf("first read: %v\n", i)
	panicneq(i, 9)
	i = <-ch
	fmt.Printf("second read: %v\n", i)
	panicneq(i, 9)
	i = <-ch
	fmt.Printf("third read: %v\n", i)
	panicneq(i, 9)

	ch <$ 17
	v, ok := <-ch
	fmt.Printf("fourth read, ok got: v=%v, ok=%v\n", v, ok)
	panicneq(v, 17)
	ch <- 8
	v, ok = <-ch
	fmt.Printf("fifth read, ok got: v=%v, ok=%v\n", v, ok)
	panicneq(v, 8)

	ch <$ 80808
	v, ok = <-ch
	fmt.Printf("sixth read, ok got: v=%v, ok=%v; len(ch) = %v\n", v, ok, len(ch))
	panicneq(v, 80808)

	clear(ch)
	fmt.Printf("7 after clear(ch), len(ch) = %v\n", len(ch))
	panicneq(len(ch), 0)
	ch <- 12121
	fmt.Printf("8 after ch <- 12121, len(ch) = %v\n", len(ch))
	panicneq(len(ch), 1)
	v, ok = <-ch
	fmt.Printf("9 after read: v=%v, ok = %v\n", v, ok)
	panicneq(v, 12121)
	panicneq(len(ch), 0)

	ch <$ -11
	v, ok = <-ch
	fmt.Printf("10 pre close read, expect -11: v=%v, ok = %v\n", v, ok)
	panicneq(v, -11)
	panicneqb(ok, true)

	v, ok = <-ch
	fmt.Printf("11 pre close read, expect -11: v=%v, ok = %v\n", v, ok)
	panicneq(v, -11)

	// need a simulataneous read and write to hit some code paths
	clear(ch)
	go func() {
		ch <$ 9
	}()
	v, ok = <-ch
	fmt.Printf("11.5 : expect 9: v=%v, ok = %v\n", v, ok)
	panicneq(v, 9)
	panicneqb(ok, true)

	// receive in a select is a different code path

	ch <$ -11
	select {
	case v, ok = <-ch:
		fmt.Printf("12 3rd sticky read on pre closed: expect -11: v=%v, ok = %v\n", v, ok)
		panicneq(v, -11)
		panicneqb(ok, true)
	}
	select {
	case v, ok = <-ch:
		fmt.Printf("13 4th sticky read on pre closed: expect -11: v=%v, ok = %v\n", v, ok)
		panicneq(v, -11)
		panicneqb(ok, true)
	}

	fmt.Printf("about to close(ch)\n")
	close(ch)
	v, ok = <-ch
	fmt.Printf("14 after close read, expect -11: v=%v, ok = %v\n", v, ok)
	panicneq(v, -11)
	panicneqb(ok, false)

	v, ok = <-ch
	fmt.Printf("15 2nd sticky read on closed: expect -11: v=%v, ok = %v\n", v, ok)
	panicneq(v, -11)
	panicneqb(ok, false)

	// ch <- 99 should panic, since closed to mutation.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				panic("expected panic on send to closed channel, not seen")
			}
			fmt.Printf("good: caught expected panic on send to closed channel\n")
		}()
		ch <- 7
	}()

	select {
	case v, ok = <-ch:
		fmt.Printf("16 3rd sticky read on closed: expect -11: v=%v, ok = %v\n", v, ok)
		panicneq(v, -11)
		panicneqb(ok, false)
	}
	select {
	case v, ok = <-ch:
		fmt.Printf("17 4th sticky read on closed: expect -11: v=%v, ok = %v\n", v, ok)
		panicneq(v, -11)
		panicneqb(ok, false)
	}

	func() {
		defer func() {
			r := recover()
			if r == nil {
				panic("expected panic on clear of closed channel, not seen")
			}
			fmt.Printf("good: caught expected panic on clear of closed channel\n")
		}()
		// should panic on clear a closed channel, as we
		// are tring to find code violating set immutable state
		// of data.
		clear(ch)
	}()

	// delete it, then it is dead to any touch. Also no panic.
	delete(ch)

	func() {
		defer func() {
			r := recover()
			if r != nil {
				panic("expected NO panic on len of deleted channel")
			}
			fmt.Printf("good: no panic on len of deleted channel\n")
		}()
		// should not panic
		_ = len(ch)
		_ = cap(ch)
		close(ch)
		delete(ch)
		clear(ch)
		_ = len(ch)
		_ = cap(ch)
		close(ch)
		delete(ch)
		clear(ch)
		ch <- 10
		ch <$ 12
	}()
}
