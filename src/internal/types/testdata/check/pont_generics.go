// Copyright 2026 The Pont Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Pont sends should infer generic function arguments just like ordinary sends.
package pontgenerics

func identity[T any](v T) T { return v }

func sendFunctions() {
	final := make(chan func(int) int, 1)
	final <# identity
	sticky := make(chan func(string) string, 1)
	sticky <$ identity
}
