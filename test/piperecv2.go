// run

// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Test pipe-receive <| replacement for sync.WaitGroup

package main

import (
	"fmt"
	"runtime"
)

var _ = fmt.Printf

func main() {

	n := 10
	ch := make(chan int, n)
	check := make(map[int]bool)
	for i := range n {
		check[i] = true
		go func(i int) {
			if i == 0 {
				runtime.Gosched()
			}
			ch <- i
		}(i)
	}

	first := <|ch
	// I know all n of my goroutines have sent in their integer.
	delete(check, first)
	m := len(ch)
	if m != n-1 {
		panic(fmt.Sprintf("expected n-1=%v, got %v", n-1, m))
	}
	fmt.Printf("good: have n-1==%v left\n", m)
	//fmt.Printf("got first = %v\n", first)
loop:
	for {
		select {
		case x := <-ch:
			//fmt.Printf("read x = %v\n", x)
			delete(check, x)
		default:
			break loop
		}
	}
	if len(check) == 0 {
		fmt.Printf("good: all sent values received.\n")
	} else {
		fmt.Printf("bad: remaining sent value not received: '%#v'\n", check)
		panic("uh-oh!")
	}
}
