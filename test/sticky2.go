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

	ch := make(chan int, 5)

	if true {
		for stickyVal := 3; stickyVal < 10; stickyVal += 3 {

			fmt.Printf("about to do regulary ch <- 0 which will bump out the sticky\n")
			ch <- 0
			fmt.Printf("about ch <- 1\n")
			ch <- 1
			fmt.Printf("about ch <- 2\n")
			ch <- 2
			fmt.Printf("about ch <$ %v\n", stickyVal)
			ch <$ stickyVal

			fmt.Printf("done with ch <$ %v    about to <-ch read back\n", stickyVal)

			for i := range 5 {
				v, ok := <-ch
				fmt.Printf("i=%v ...  v=%v, ok=%v\n", i, v, ok)
				if !ok {
					panic("should be ok. never closed!")
				}
				if i < 3 {
					if v != i {
						panic(fmt.Sprintf("got %v, want %v", v, i))
					}
				} else {
					if v != stickyVal {
						panic(fmt.Sprintf("got %v, want %v; on i = %v", v, stickyVal, i))
					}
				}
			}
		}
	} // end if false

	if true { // some leftover stuff from this impacts the next test!
		fmt.Printf("check replacement of sticky at front\n")
		clear(ch)
		ch <$ 1
		v := <-ch
		if v != 1 {
			panic(fmt.Sprintf("got %v, want %v", v, 1))
		}
		ch <$ 2
		v = <-ch
		if v != 2 {
			panic(fmt.Sprintf("got %v, want %v", v, 2))
		}
		ch <$ 3
		v = <-ch
		if v != 3 {
			panic(fmt.Sprintf("got %v, want %v", v, 3))
		}
		fmt.Printf("done with sticky at front test\n")
	}

	fmt.Printf("check replacement of sticky not at front.\n")
	clear(ch)
	fmt.Printf("called clear(ch).\n")
	//ch = make(chan int, 5) // green. clear is not equivalent yet!

	var v int
	fmt.Printf("about to ch <- 1\n")
	ch <- 1
	fmt.Printf("back from ch <- 1\n")

	fmt.Printf("about to ch <- 2\n")
	ch <- 2
	fmt.Printf("back from ch <- 2\n")

	fmt.Printf("about to ch <$ 88\n")
	ch <$ 88
	fmt.Printf("back from ch <$ 88\n")

	fmt.Printf("about to ch <$ 3 which should hit sticky replacement...\n")
	ch <$ 3
	fmt.Printf("back from ch <$ 3\n")

	for i := 1; i <= 3; i++ {
		v = <-ch
		fmt.Printf("on i = %v, see v = %v\n", i, v)
		if v != i {
			panic(fmt.Sprintf("on i= %v, got %v, want %v", i, v, i))
		}
	}
	// verify that 3 has stuck
	for i := 1; i <= 3; i++ {
		v = <-ch
		if v != 3 {
			panic(fmt.Sprintf("got %v, want %v", v, i))
		}
	}
}
