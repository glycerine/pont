// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build js || wasip1

package runtime

// The -onethread experiment does not target js/wasm or wasip1 (which are
// already single-threaded with their own cooperative notes in lock_js.go /
// lock_wasip1.go). Provide a stub so proc.go's shared idle path compiles.

func onethreadHasNoteWaiters() bool { return false }

func onethreadReadyNotes() {}
