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
#
# you must run all.bash after setting TMPDIR (in the shell where
# you are about to run ./all.bash) to a directory that
# does not cross filesystem boundaries (with respect to GOROOT)
# so git stays happy.
# Also, this TMPDIR must not be inside GOROOT as shown by "go env".
#
# cd go/src # the directory where all.bash resides.
# export TMPDIR="$(cd ../.. && pwd)/go-tmp" ## must not be inside GOROOT
# mkdir -p "$TMPDIR"
# echo "set TMPDIR to $TMPDIR"
# ./all.bash

# Keep build/test scratch dirs on the same filesystem as the repo.
# Without this, t.TempDir() can land on a different mount (e.g. /tmp),
# and git's upward directory walk hits a filesystem boundary, producing
# "error obtaining VCS status: exit status 128" in tests that shell out
# to git from a temp build dir (see cmd/cgo/internal/testerrors/ptr_test.go).

. ./make.bash "$@" --no-banner
bash run.bash --no-rebuild
../bin/go tool dist banner # print build info
