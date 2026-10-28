package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"runtime"
)

func readSequence() []byte {
	out := make([]byte, 0, 2048)
	for _, read := range []func([]byte) (int, error){rand.Read, rand.Reader.Read} {
		for _, size := range []int{0, 1, 7, 8, 9, 513} {
			b := make([]byte, size)
			n, err := read(b)
			if n != size || err != nil {
				panic("short or failed random read")
			}
			out = append(out, b...)
		}
	}
	return out
}

func main() {
	if !runtime.InSimulationMode() {
		panic("simulation mode not enabled by GO_DSIM_SEED")
	}
	// Capture startup output before resetting, for cross-process replay checks.
	initial := readSequence()
	runtime.ResetDsimSeed(123)
	first := readSequence()
	next := readSequence()
	if bytes.Equal(first, next) {
		panic("random state did not advance")
	}
	runtime.ResetDsimSeed(123)
	if !bytes.Equal(first, readSequence()) {
		panic("seed reset did not replay")
	}
	runtime.ResetDsimSeed(456)
	if bytes.Equal(first, readSequence()) {
		panic("different seeds gave identical output")
	}
	// Both public entry points must consume the same runtime stream.
	var a, b [513]byte
	runtime.ResetDsimSeed(123)
	rand.Read(a[:])
	runtime.ResetDsimSeed(123)
	io.ReadFull(rand.Reader, b[:])
	if a != b {
		panic("Read and Reader disagree")
	}
	fmt.Printf("%x\n", initial)
}
