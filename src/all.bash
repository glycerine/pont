#!/usr/bin/env bash
# Copyright 2009 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -e
if [ ! -f make.bash ]; then
	echo 'all.bash must be run from $GOROOT/src' 1>&2
	exit 1
fi

# jea: to solve the build failures that complain:
#         error obtaining VCS status: exit status 128
#         Use -buildvcs=false to disable VCS stamping.
export GOTMPDIR="$(cd "$(dirname "$0")/.." && pwd)/go-tmp"
mkdir -p "$GOTMPDIR"
# Keep build/test scratch dirs on the same filesystem as the repo.
# Without this, t.TempDir() can land on a different mount (e.g. /tmp),
# and git's upward directory walk hits a filesystem boundary, producing
# "error obtaining VCS status: exit status 128" in tests that shell out
# to git from a temp build dir (see cmd/cgo/internal/testerrors/ptr_test.go).
export GOTMPDIR="$(cd "$(dirname "$0")/.." && pwd)/go-tmp"
mkdir -p "$GOTMPDIR"

. ./make.bash "$@" --no-banner
bash run.bash --no-rebuild
../bin/go tool dist banner # print build info
