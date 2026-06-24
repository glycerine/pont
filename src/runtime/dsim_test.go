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
