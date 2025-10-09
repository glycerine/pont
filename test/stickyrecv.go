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

	ch := make(chan int, 1)

	ch <- 1

	x, ok := <-ch
	panicneq(x, 1)
	panicneqb(ok, true)
	panicneq(len(ch), 0)

	ch <$ 1

	x, ok = <-ch
	panicneq(x, 1)
	panicneqb(ok, true)
	panicneq(len(ch), 1)

	x, ok = <$ch
	panicneq(x, 1)
	panicneqb(ok, true)
	panicneq(len(ch), 0)

	ch <$ 2
	x, ok = <$ch
	panicneq(x, 2)
	panicneqb(ok, true)
	panicneq(len(ch), 0)

	ch <$ 3
	panicneq(len(ch), 1)
	<-ch
	panicneq(len(ch), 1)
	<-ch
	panicneq(len(ch), 1)
	<-ch
	panicneq(len(ch), 1)

	select {
	case x, ok = <$ch:
	}
	panicneq(x, 3)
	panicneqb(ok, true)
	panicneq(len(ch), 0)

	select {
	case x, ok = <$ch: // deadlock! we want this to give us zero value and false.
	default:
	}
	panicneq(x, 3)
	panicneqb(ok, true)
	panicneq(len(ch), 0)

	ch <$ 4
	panicneq(len(ch), 1)
	<-ch
	panicneq(len(ch), 1)
	<-ch
	panicneq(len(ch), 1)
	<$ch
	panicneq(len(ch), 0)

	// sticky receive on closed channel should panic

	ch <$ 10

	close(ch)
	// <$ch should panic, since closed means closed to mutation
	func() {
		defer func() {
			r := recover()
			if r == nil {
				panic("expected panic on sticky-receive on closed channel, not seen")
			}
			fmt.Println("good: caught expected panic on sticky-receive on closed channel")
		}()
		a := <$ch
		fmt.Printf("ugh! wanted to see a panic when attempting a sticky-receive on a closed chan; instead we got got a = %v\n", a)
	}()

	func() {
		defer func() {
			r := recover()
			if r == nil {
				panic("expected panic on sticky-receive on closed channel, not seen")
			}
			fmt.Println("good: caught expected panic on sticky-receive on closed channel.")
		}()
		select {
		case <$ch:
		}
	}()

	func() {
		defer func() {
			r := recover()
			if r == nil {
				panic("expected panic on sticky-receive on closed channel, not seen")
			}
			fmt.Println("good: caught expected panic on sticky-receive on closed channel.")
		}()
		a, ok := <$ ch
		_ = a
		_ = ok
	}()
	fmt.Println("got to end!")
}
