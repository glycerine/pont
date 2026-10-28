// Copyright 2026 The Pont Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime_test

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDsimSeedEarlyEnvNoAlloc(t *testing.T) {
	testenv.MustHaveGoBuild(t)

	switch runtime.GOOS {
	case "aix", "darwin", "ios", "dragonfly", "freebsd", "netbsd", "openbsd", "illumos", "solaris", "linux":
	default:
		t.Skipf("GO_DSIM_SEED is not read early on %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\nfunc main() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}

	cmd := testenv.CleanCmdEnv(testenv.Command(t, testenv.GoToolPath(t), "run", file))
	cmd.Env = append(cmd.Env,
		"GO_DSIM_SEED=1",
		"GOROOT="+testenv.GOROOT(t),
		"GOCACHE="+filepath.Join(t.TempDir(), "gocache"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v failed: %v\n%s", cmd, err, out)
	}
}

func TestDsimCryptoRand(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	switch runtime.GOOS {
	case "aix", "darwin", "ios", "dragonfly", "freebsd", "netbsd", "openbsd", "illumos", "solaris", "linux":
	default:
		t.Skip("GO_DSIM_SEED is not read early on this platform")
	}
	exe := filepath.Join(t.TempDir(), "dsimrand")
	cmd := testenv.CleanCmdEnv(testenv.Command(t, testenv.GoToolPath(t), "build", "-o", exe, "./testdata/dsimrand"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(seed string) string {
		t.Helper()
		cmd := testenv.CleanCmdEnv(testenv.Command(t, exe))
		cmd.Env = append(cmd.Env, "GO_DSIM_SEED="+seed, "GOMAXPROCS=1", "GODEBUG=asyncpreemptoff=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("seed %s: %v\n%s", seed, err, out)
		}
		return string(out)
	}
	first := run("123")
	if first != run("123") {
		t.Fatal("same startup seed did not replay crypto/rand output")
	}
	if first == run("456") {
		t.Fatal("different startup seeds produced identical crypto/rand output")
	}
}
