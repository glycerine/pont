// Copyright 2026 The Pont Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime_test

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOnethreadSmoke(t *testing.T) {
	mustSupportOnethread(t)
	out := runOnethreadProgram(t, `package main

import (
	"fmt"
	"runtime"
)

func main() {
	fmt.Println(runtime.NumCPU(), runtime.GOMAXPROCS(8), runtime.GOMAXPROCS(0))
}
`)
	if got, want := strings.TrimSpace(out), "1 1 1"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestOnethreadCgoMalloc(t *testing.T) {
	mustSupportOnethread(t)
	testenv.MustHaveCGO(t)
	out := runOnethreadProgram(t, `package main

/*
#include <stdlib.h>
*/
import "C"

import "fmt"

func main() {
	p := C.malloc(16)
	C.free(p)
	fmt.Println("cgo ok")
}
`, "CGO_ENABLED=1")
	if got, want := strings.TrimSpace(out), "cgo ok"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func mustSupportOnethread(t *testing.T) {
	t.Helper()
	// These tests spawn real `go run -onethread` subprocesses. That is
	// onethread logic, so it must not run during an ordinary `go test` /
	// all.bash invocation where the caller did not ask for onethread: a normal
	// build/test run should never exercise -onethread (and onethread is not yet
	// working on every supported platform, e.g. it currently hangs on darwin,
	// which would otherwise stall the whole runtime test and all.bash). Require
	// explicit opt-in.
	if os.Getenv("GO_TEST_ONETHREAD") == "" {
		t.Skip("skipping -onethread integration test; set GO_TEST_ONETHREAD=1 to enable")
	}
	testenv.MustHaveGoBuild(t)
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "linux/arm64", "linux/riscv64",
		"darwin/amd64", "darwin/arm64":
	default:
		t.Skipf("-onethread is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func runOnethreadProgram(t *testing.T, src string, env ...string) string {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte(src), 0666); err != nil {
		t.Fatal(err)
	}

	cmd := testenv.CleanCmdEnv(testenv.Command(t, testenv.GoToolPath(t), "run", "-onethread", file))
	cmd.Env = append(cmd.Env,
		"GOROOT="+testenv.GOROOT(t),
		"GOCACHE="+filepath.Join(t.TempDir(), "gocache"),
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %v\n%s", cmd, err, out)
	}
	return string(out)
}
