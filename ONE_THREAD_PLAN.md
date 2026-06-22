# Pont `-onethread` Runtime Design and Implementation Plan

This document describes a native Pont mode that builds amd64/arm64 binaries whose Go runtime uses exactly one operating-system thread. The goal is stronger deterministic execution by removing OS thread scheduling from the runtime's behavior. The model is inspired by Go's wasm support, but targets ordinary native platforms without wasm's 32-bit linear-memory ceiling and without wasm execution overhead.

The plan has two parts:

1. A design plan for the semantics and runtime architecture.
2. A detailed implementation approach that names the source files, functions, control-flow branches, invariants, tests, and likely staging order.

The paths in this document refer to the current Pont/Go source tree rooted at:

```text
/usr/local/dev-go/go
```

## 1. Problem Statement

`GOMAXPROCS=1` is not enough.

With `GOMAXPROCS=1`, the Go scheduler has one logical processor (`P`), but the runtime may still create or rely on several OS threads (`M`s):

- the sysmon thread
- extra Ms for cgo callbacks
- the template thread used to create Ms from a known-good thread state
- replacement Ms while a goroutine is in a syscall
- Ms created by `startm`, `wakep`, `handoffp`, and related scheduler paths
- signal/profiler/preemption machinery that depends on OS-level signal delivery
- race/MSan/ASan runtimes and foreign code paths

For Pont determinism, the hard invariant should be:

```text
one process, one Go-created OS thread, one runtime M, one P
```

The runtime should never create a second OS thread, never rely on a second OS thread to make progress, and never allow OS thread scheduling to influence Go scheduling.

## 2. Desired Semantics

`go build -onethread` should produce a native binary with these properties:

- `runtime.NumCPU()` reports 1, or at least the runtime scheduler behaves as if the CPU count is 1.
- `runtime.GOMAXPROCS(0)` is always 1.
- attempts to set `GOMAXPROCS` above 1 have no effect and return/report 1.
- the runtime starts no sysmon thread.
- the runtime starts no template thread.
- the runtime starts no cgo extra M.
- the runtime starts no replacement M for syscall handoff.
- async signal-based preemption is disabled.
- cgo is disabled or rejected.
- race/MSan/ASan are rejected.
- plugin/shared/c-archive/c-shared modes are initially rejected.
- blocking syscall paths are rejected in strict mode.
- timers and netpoll readiness are handled by the sole M.
- all goroutines are scheduled cooperatively on the sole P.

The mode should be described as:

```text
single-OS-thread runtime scheduling
```

not as:

```text
complete deterministic execution for all possible programs
```

External I/O, external signals, wall-clock time, filesystem latency, network packet order, CPU features, and explicit nondeterminism can still affect a program. The runtime mode removes OS thread scheduling from the Pont runtime as a source of nondeterminism.

## 3. What Wasm Already Does

Wasm is the best local reference because Go's wasm runtime already lives under a no-threads assumption.

### 3.1 CPU Count

File:

```text
/usr/local/dev-go/go/src/runtime/os_wasm.go
```

Relevant behavior:

```go
func osinit() {
        physPageSize = 64 * 1024
        initBloc()
        blocMax = uintptr(currentMemory()) * physPageSize
        numCPUStartup = getCPUCount()
        getg().m.procid = 2
}

func getCPUCount() int32 {
        return 1
}
```

Wasm reports one CPU to the runtime. Native `onethread` should do the same, either by overriding `numCPUStartup` during `osinit` or by forcing `GOMAXPROCS` during `schedinit`. For clarity and consistency, do both:

- make `numCPUStartup` become 1 in `onethread`
- force `procs := int32(1)` in `schedinit`

### 3.2 Thread Creation

File:

```text
/usr/local/dev-go/go/src/runtime/os_wasm.go
```

Relevant behavior:

```go
func newosproc(mp *m) {
        throw("newosproc: not implemented")
}
```

Native `onethread` should not silently fail to create threads. It should fail loudly if code reaches a thread creation path. That is a runtime invariant violation.

### 3.3 Async Preemption

File:

```text
/usr/local/dev-go/go/src/runtime/os_wasm.go
```

Relevant behavior:

```go
const preemptMSupported = false

func preemptM(mp *m) {
        // No threads, so nothing to do.
}
```

Native async preemption uses signals. Signals arrive at OS-chosen instruction points and therefore introduce scheduling nondeterminism. `onethread` should disable async preemption and rely on cooperative preemption points.

### 3.4 Signal Handling

File:

```text
/usr/local/dev-go/go/src/runtime/os_wasm.go
```

Relevant behavior:

```go
type sigset struct{}

func sigsave(p *sigset) {}
func msigrestore(sigmask sigset) {}
func clearSignalHandlers() {}
func sigblock(exiting bool) {}
func minit() {}
func unminit() {}
func mdestroy(mp *m) {}

const _NSIG = 0

func initsig(preinit bool) {}
```

Native `onethread` cannot completely remove signals because native platforms still need synchronous fault handling for panics, nil pointer faults, divide-by-zero, crash reporting, and process termination signals. But it should not use signals for async preemption, and CPU profiling should be treated as unsupported or explicitly nondeterministic.

### 3.5 Locks and Notes

Files:

```text
/usr/local/dev-go/go/src/runtime/lock_js.go
/usr/local/dev-go/go/src/runtime/lock_wasip1.go
/usr/local/dev-go/go/src/runtime/lock_futex.go
```

Wasm uses much simpler lock behavior because there is no concurrent runtime thread:

```go
func lock2(l *mutex) {
        if l.key == mutex_locked {
                throw("self deadlock")
        }
        gp := getg()
        gp.m.locks++
        l.key = mutex_locked
}
```

Linux native currently uses futex-backed sleeping:

```go
func notesleep(n *note) {
        gp := getg()
        if gp != gp.m.g0 {
                throw("notesleep not on g0")
        }
        for atomic.Load(key32(&n.key)) == 0 {
                gp.m.blocked = true
                futexsleep(key32(&n.key), 0, ns)
                gp.m.blocked = false
        }
}
```

Important distinction:

- In wasm, lock contention should only be self-contention.
- In native `onethread`, this becomes true only after all second-thread and signal-preemption paths are eliminated.

So lock simplification should be a later stage, after the scheduler and thread-creation invariants are solid.

### 3.6 Idle Scheduling

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current scheduler path:

```go
gp, otherReady := beforeIdle(now, pollUntil)
if gp != nil {
        ...
        return gp, false, false
}
if otherReady {
        goto top
}
```

For wasm/js, `beforeIdle` is where the runtime integrates with the host event loop. For native `onethread`, this hook is a strong hint: the single-thread runtime needs a special idle path. It cannot use the normal native path of dropping the P, parking the M, and relying on another M or sysmon to wake it.

## 4. Core Runtime Design

### 4.1 Runtime Topology

In `onethread`:

```text
GOMAXPROCS = 1
len(allp) = 1
mcount() = 1
sysmon = absent
template thread = absent
cgo extra M = absent
async preemption = absent
race/MSan/ASan = absent
```

The sole thread owns the sole P whenever it is running Go code or intentionally sleeping in the runtime idle loop.

### 4.2 Scheduler Model

The scheduler remains the Go scheduler, not a new scheduler. Goroutines still block on channels, mutexes, timers, netpoll, and GC coordination. The difference is that every transition must be cooperative on a single M.

When runnable work exists:

```text
run goroutine on sole M/P
```

When no runnable work exists:

```text
run timers
poll netpoll with the next timer deadline as timeout
inject any ready goroutines onto the sole run queue
loop
```

When a goroutine attempts an operation that would require a replacement M:

```text
throw or panic with a clear onethread unsupported-operation message
```

### 4.3 Sysmon Replacement

Sysmon currently performs many duties:

- netpoll fallback
- retaking Ps from syscalls
- async preempting long-running goroutines
- periodic force-GC
- scavenger wakeups
- GOMAXPROCS updates
- schedtrace timing

In `onethread`:

- netpoll is handled by the sole scheduler loop.
- retaking Ps from syscalls is impossible by design.
- async preemption is disabled by design.
- periodic force-GC can be disabled initially or checked at scheduler checkpoints.
- scavenger wakeups can use normal goroutine readiness paths or be checked at scheduler checkpoints.
- GOMAXPROCS updates are disabled because GOMAXPROCS is always 1.
- schedtrace can be reduced, disabled, or emitted only at scheduler checkpoints.

### 4.4 Syscall Policy

This is the fundamental design constraint:

```text
With one OS thread, a blocking syscall blocks the entire runtime.
```

There is no way to let other goroutines keep running during a blocking syscall without another OS thread.

Recommended v1 policy:

```text
strict onethread mode
```

Strict mode:

- allows nonblocking netpoll-backed I/O
- allows quick syscalls that do not call `entersyscallblock`
- rejects cgo
- rejects `entersyscallblock`
- rejects foreign-thread callbacks

Future policy:

```text
permissive onethread mode
```

Permissive mode could allow blocking syscalls and document that they freeze all goroutines. That is useful for simple programs, but it weakens the determinism and liveness story.

### 4.5 Preemption Policy

Disable signal-based async preemption.

Keep cooperative preemption:

- safe points at function calls
- stack growth checks
- scheduler calls
- blocking operations
- optionally compiler-inserted loop checks from `GOEXPERIMENT=preemptibleloops`

For maximum determinism and fairness, Pont may want `preemptibleloops` enabled together with `onethread`, but that is a policy choice. Loop preemption at compiler-defined points is much more deterministic than signal delivery at OS-chosen points.

### 4.6 Netpoll and Timers

Native netpoll already has the pieces needed:

- Linux: `/usr/local/dev-go/go/src/runtime/netpoll_epoll.go`
- Darwin/BSD: `/usr/local/dev-go/go/src/runtime/netpoll_kqueue.go`

Both support:

```go
func netpoll(delay int64) (gList, int32)
func netpollBreak()
```

`onethread` should use these from the sole scheduler loop. The critical change is that the sole M must not drop its P and wait for another M to acquire a P. It should block in `netpoll(delay)` while the scheduler knows this is the one allowed idle wait.

### 4.7 Deterministic Ordering

Some orderings are already deterministic when `GOMAXPROCS=1`, but netpoll can still return events in OS order. If Pont simulations care about reproducible I/O event ordering, add a stable sorting layer for ready poll descriptors.

Initial v1 can document:

```text
netpoll readiness order is host controlled
```

Later deterministic I/O mode can:

- gather `gList` entries from netpoll
- attach stable poll descriptor IDs
- sort by fd or runtime-created poll sequence
- inject in stable order

### 4.8 Public API

Avoid adding public API in v1. Let the build mode appear in:

- `GOEXPERIMENT=onethread`
- `go build -onethread`
- `runtime.Version()` experiment suffix
- `go version <binary>`
- `go env GOEXPERIMENT` if explicitly set

Optional later public API:

```go
package runtime

func OneThread() bool
```

But this is not needed initially, and adding API has long-term compatibility cost.

## 5. Detailed Implementation Approach

This section is intentionally mechanical. It lists the source changes in the order I would make them.

## Phase 0: Establish the Experiment Plumbing

### 0.1 Add `Onethread` to `internal/goexperiment`

File:

```text
/usr/local/dev-go/go/src/internal/goexperiment/flags.go
```

Add a field to `type Flags struct`:

```go
// Onethread builds a native runtime that never creates more than one
// operating-system thread and forces GOMAXPROCS to 1.
Onethread bool
```

Good location: near other runtime-shaping experiments, probably after `PreemptibleLoops` or near `RuntimeFreegc`.

Then regenerate constants:

```sh
cd /usr/local/dev-go/go/src/internal/goexperiment
go generate
```

Expected generated files:

```text
/usr/local/dev-go/go/src/internal/goexperiment/exp_onethread_on.go
/usr/local/dev-go/go/src/internal/goexperiment/exp_onethread_off.go
```

Expected content shape:

```go
// Code generated by mkconsts.go. DO NOT EDIT.

//go:build goexperiment.onethread

package goexperiment

const Onethread = true
const OnethreadInt = 1
```

and:

```go
// Code generated by mkconsts.go. DO NOT EDIT.

//go:build !goexperiment.onethread

package goexperiment

const Onethread = false
const OnethreadInt = 0
```

Why this comes first:

- gives runtime code a constant `goexperiment.Onethread`
- gives package selection the tag `goexperiment.onethread`
- gives assembly the macro `GOEXPERIMENT_onethread`
- gives build-cache separation
- gives binary version/build-info visibility

### 0.2 Do not add `Onethread` to the baseline

File:

```text
/usr/local/dev-go/go/src/internal/buildcfg/exp.go
```

The baseline currently enables things like `RegabiWrappers`, `RegabiArgs`, `Dwarf5`, `RandomizedHeapBase64`, and `GreenTeaGC`.

Do not enable `Onethread` by default:

```go
baseline := goexperiment.Flags{
        RegabiWrappers:       regabiSupported,
        RegabiArgs:           regabiSupported,
        Dwarf5:               dwarf5Supported,
        RandomizedHeapBase64: true,
        GreenTeaGC:           true,
}
```

Leave `Onethread` false unless explicitly requested.

### 0.3 Add command-side state for `-onethread`

File:

```text
/usr/local/dev-go/go/src/cmd/go/internal/cfg/cfg.go
```

Add:

```go
BuildOnethread bool // -onethread flag
```

Near:

```go
BuildRace bool
BuildMSan bool
BuildASan bool
```

### 0.4 Add the `-onethread` flag

File:

```text
/usr/local/dev-go/go/src/cmd/go/internal/work/build.go
```

In `AddBuildFlags`, add:

```go
cmd.Flag.BoolVar(&cfg.BuildOnethread, "onethread", false, "")
```

Reasonable location:

```go
cmd.Flag.BoolVar(&cfg.BuildMSan, "msan", false, "")
cmd.Flag.BoolVar(&cfg.BuildOnethread, "onethread", false, "")
cmd.Flag.StringVar(&cfg.BuildPGO, "pgo", "auto", "")
cmd.Flag.StringVar(&cfg.BuildPkgdir, "pkgdir", "", "")
cmd.Flag.BoolVar(&cfg.BuildRace, "race", false, "")
```

Also update help text in:

```text
/usr/local/dev-go/go/src/cmd/go/internal/work/build.go
/usr/local/dev-go/go/src/cmd/go/alldocs.go
```

The docs generator may update `alldocs.go`; do not hand-edit generated docs if the repo has a generator path for this.

Suggested help text:

```text
-onethread
        build with the single-OS-thread Pont runtime. This implies
        GOEXPERIMENT=onethread and disables cgo. It is incompatible with
        -race, -msan, -asan, plugin/shared/c-archive/c-shared build modes.
```

### 0.5 Normalize `-onethread` into `GOEXPERIMENT=onethread`

This part matters. `-onethread` must be visible to package selection as `goexperiment.onethread`, not merely passed to the linker.

Current experiment setup:

File:

```text
/usr/local/dev-go/go/src/cmd/go/internal/cfg/cfg.go
```

Current flow:

```go
RawGOEXPERIMENT = envOr("GOEXPERIMENT", buildcfg.DefaultGOEXPERIMENT)
CleanGOEXPERIMENT = RawGOEXPERIMENT

func init() {
        computeExperiment()
}

func computeExperiment() {
        Experiment, ExperimentErr = buildcfg.ParseGOEXPERIMENT(Goos, Goarch, RawGOEXPERIMENT)
        ...
        for _, exp := range exps {
                expTags = append(expTags, "goexperiment."+exp)
        }
        BuildContext.ToolTags = append(expTags, BuildContext.ToolTags...)
}
```

Because flags are parsed after `cfg.init`, `-onethread` needs an explicit post-parse normalization step before package loading.

Add an exported helper in `cmd/go/internal/cfg/cfg.go`:

```go
func EnableExperiment(name string) error {
        if name == "" {
                return nil
        }

        fields := strings.Split(RawGOEXPERIMENT, ",")
        for _, f := range fields {
                if f == name {
                        return recomputeExperimentClean()
                }
                if f == "no"+name {
                        return fmt.Errorf("-%s conflicts with GOEXPERIMENT=no%s", name, name)
                }
        }
        if RawGOEXPERIMENT == "" {
                RawGOEXPERIMENT = name
        } else {
                RawGOEXPERIMENT += "," + name
        }
        return recomputeExperimentClean()
}
```

But implement it carefully:

- strip previous `goexperiment.*` entries from `BuildContext.ToolTags` before calling `computeExperiment`
- preserve non-experiment tool tags such as `race`, `msan`, `asan`, architecture tags, etc.
- return `ExperimentErr` instead of deferring it to `cmd/go/main.go`

Sketch:

```go
func recomputeExperimentClean() error {
        var keep []string
        for _, tag := range BuildContext.ToolTags {
                if !strings.HasPrefix(tag, "goexperiment.") {
                        keep = append(keep, tag)
                }
        }
        BuildContext.ToolTags = keep
        computeExperiment()
        return ExperimentErr
}
```

Then call it before package loading. The current `BuildInit` begins:

```go
func BuildInit(loaderstate *modload.State) {
        ...
        modload.Init(loaderstate)
        instrumentInit()
        buildModeInit()
        ...
}
```

Add an `onethreadInitEarly` before `modload.Init(loaderstate)`:

```go
func BuildInit(loaderstate *modload.State) {
        ...
        onethreadInitEarly()
        modload.Init(loaderstate)
        instrumentInit()
        buildModeInit()
        ...
}
```

`onethreadInitEarly` should:

```go
func onethreadInitEarly() {
        if !cfg.BuildOnethread {
                return
        }
        if err := cfg.EnableExperiment("onethread"); err != nil {
                base.Fatal(err)
        }
        cfg.BuildContext.CgoEnabled = false
}
```

This makes `-onethread` equivalent to `GOEXPERIMENT=onethread` for package selection.

### 0.6 Make `-onethread` visible in build settings

File:

```text
/usr/local/dev-go/go/src/cmd/go/internal/load/pkg.go
```

The build settings include `-race`, `-msan`, `-asan`, `-buildmode`, `CGO_ENABLED`, etc. Add:

```go
if cfg.BuildOnethread {
        appendSetting("-onethread", "true")
}
```

This is not strictly necessary if `GOEXPERIMENT=onethread` appears in version/build info, but it makes `go version -m` easier to understand.

### 0.7 Reject incompatible flags early

File:

```text
/usr/local/dev-go/go/src/cmd/go/internal/work/init.go
```

Add:

```go
func onethreadInitEarly() {
        if !cfg.BuildOnethread && (cfg.Experiment == nil || !cfg.Experiment.Onethread) {
                return
        }
        ...
}

func onethreadInitLate() {
        if !cfg.BuildOnethread && (cfg.Experiment == nil || !cfg.Experiment.Onethread) {
                return
        }
        if cfg.BuildRace {
                base.Fatalf("-onethread is incompatible with -race")
        }
        if cfg.BuildMSan {
                base.Fatalf("-onethread is incompatible with -msan")
        }
        if cfg.BuildASan {
                base.Fatalf("-onethread is incompatible with -asan")
        }
        if cfg.BuildBuildmode != "default" && cfg.BuildBuildmode != "exe" {
                base.Fatalf("-onethread is incompatible with -buildmode=%s", cfg.BuildBuildmode)
        }
        if cfg.BuildLinkshared {
                base.Fatalf("-onethread is incompatible with -linkshared")
        }
        if cfg.BuildContext.CgoEnabled {
                base.Fatalf("-onethread requires cgo to be disabled")
        }
}
```

Recommended behavior:

- `go build -onethread` should force `cfg.BuildContext.CgoEnabled = false`.
- `GOEXPERIMENT=onethread CGO_ENABLED=1 go build` should fail with a clear message.

Why both?

- The flag is user-friendly and can imply the safe default.
- The raw experiment is lower-level and should not silently override an explicit `CGO_ENABLED=1`.

Call order:

```go
onethreadInitEarly()
modload.Init(loaderstate)
instrumentInit()
onethreadInitLate()
buildModeInit()
```

or:

```go
onethreadInitEarly()
modload.Init(loaderstate)
instrumentInit()
buildModeInit()
onethreadInitLate()
```

The late check needs to run after `buildModeInit` if it wants to inspect normalized `cfg.BuildBuildmode`, but before actions are built. The early part must run before package loading.

## Phase 1: Runtime Constants and Topology

### 1.1 Import `internal/goexperiment` in runtime files that need it

Several runtime files already import internal packages. Add:

```go
import "internal/goexperiment"
```

only where needed.

Likely files:

```text
/usr/local/dev-go/go/src/runtime/proc.go
/usr/local/dev-go/go/src/runtime/os_linux.go
/usr/local/dev-go/go/src/runtime/os_darwin.go
/usr/local/dev-go/go/src/runtime/signal_unix.go
/usr/local/dev-go/go/src/runtime/cgocall.go
/usr/local/dev-go/go/src/runtime/cpuprof.go
```

Keep imports minimal.

### 1.2 Add a runtime predicate

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Add near other scheduler constants/helpers:

```go
const onethread = goexperiment.Onethread
```

or use `goexperiment.Onethread` directly. A local constant makes repeated checks readable and lets the compiler eliminate dead branches.

Potential issue:

`proc.go` already has many imports. Add `internal/goexperiment` and use it enough to avoid unused import.

### 1.3 Force `sched.maxmcount`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
sched.maxmcount = 10000
```

Change:

```go
if goexperiment.Onethread {
        sched.maxmcount = 1
} else {
        sched.maxmcount = 10000
}
```

This is a guardrail, not the primary enforcement. The primary enforcement is throwing before thread creation.

### 1.4 Force one P in `schedinit`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Find the `schedinit` section that computes `procs` and calls `procresize(procs)`.

Expected shape in Go runtimes:

```go
procs := ncpu
if n := atoi32(gogetenv("GOMAXPROCS")); n > 0 {
        procs = n
}
if procresize(procs) != nil {
        throw("unknown runnable goroutine during bootstrap")
}
```

In current Pont there may also be default `GOMAXPROCS` update logic. Change it so:

```go
procs := defaultGOMAXPROCS(numCPUStartup)
if goexperiment.Onethread {
        procs = 1
} else if n := atoi32(gogetenv("GOMAXPROCS")); n > 0 {
        procs = n
}
if procresize(procs) != nil {
        throw("unknown runnable goroutine during bootstrap")
}
```

Policy choice:

- Ignore `GOMAXPROCS` in `onethread`.
- Or if `GOMAXPROCS` is set to anything except `1`, print a warning or throw.

Recommended:

```text
ignore and force 1
```

because environment-controlled behavior hurts reproducibility.

### 1.5 Make `runtime.GOMAXPROCS` clamp to 1

File:

```text
/usr/local/dev-go/go/src/runtime/debug.go
```

or wherever `GOMAXPROCS` is implemented in this tree.

Find:

```go
func GOMAXPROCS(n int) int
```

Change behavior under `goexperiment.Onethread`:

```go
if goexperiment.Onethread {
        return gomaxprocs
}
```

More precise:

- if `n <= 0`, return current value, which should be 1.
- if `n > 0`, do not resize, leave it at 1, return old value.
- optionally if `n != 1`, throw in debug builds, but not in normal builds.

Recommended user behavior:

```go
old := runtime.GOMAXPROCS(100)
fmt.Println(old, runtime.GOMAXPROCS(0)) // 1 1
```

This follows Go's convention that `GOMAXPROCS` returns the previous setting, but makes the mode immutable.

### 1.6 Make `runtime.NumCPU` return 1

`NumCPU` ultimately uses runtime CPU count. There are two implementation choices:

1. Override `getCPUCount` in OS files under `goexperiment.Onethread`.
2. Leave OS CPU count alone but force `numCPUStartup = 1`.

Use both if simple.

Files:

```text
/usr/local/dev-go/go/src/runtime/os_linux.go
/usr/local/dev-go/go/src/runtime/os_darwin.go
```

Linux current:

```go
func getCPUCount() int32 {
        ...
        return n
}
```

Change:

```go
func getCPUCount() int32 {
        if goexperiment.Onethread {
                return 1
        }
        ...
}
```

Darwin current:

```go
func getCPUCount() int32 {
        ...
        return int32(out)
}
```

Change similarly.

If importing `internal/goexperiment` into OS files creates cycles or import friction, do the override in `osinit`:

```go
numCPUStartup = getCPUCount()
if goexperiment.Onethread {
        numCPUStartup = 1
}
```

### 1.7 Disable sysmon

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
const haveSysmon = GOARCH != "wasm"
```

Change:

```go
const haveSysmon = GOARCH != "wasm" && !goexperiment.Onethread
```

Possible issue:

`GOARCH` is a runtime constant and `goexperiment.Onethread` is also a constant. This should remain a constant expression.

Current startup:

```go
if haveSysmon {
        systemstack(func() {
                newm(sysmon, nil, -1)
        })
}
```

Under `onethread`, this must not run.

### 1.8 Disable automatic GOMAXPROCS updates

Files:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Search for:

```text
SetDefaultGOMAXPROCS
defaultGOMAXPROCS
updateMaxProcsGoroutine
sysmonUpdateGOMAXPROCS
```

In `onethread`, every update should resolve to 1 and should not start helper work whose only purpose is dynamic GOMAXPROCS sizing.

Expected change:

```go
if goexperiment.Onethread {
        return 1
}
```

in `defaultGOMAXPROCS` or equivalent.

Also ensure `sysmonUpdateGOMAXPROCS` is not relied on, because sysmon is disabled.

## Phase 2: Hard-Block Thread Creation

### 2.1 Guard `newm`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func newm(fn func(), pp *p, id int64) {
        acquirem()
        mp := allocm(pp, fn, id)
        ...
        newm1(mp)
        releasem(getg().m)
}
```

Add at the top:

```go
if goexperiment.Onethread {
        throw("newm in onethread mode")
}
```

But note bootstrap:

- the bootstrap M exists before `newm`
- `newm` should not be needed for bootstrap
- sysmon creation must be disabled before this guard is hit

If some path legitimately calls `newm` during bootstrap before the invariant is established, that path needs a specific exception. The expected answer is no exception.

### 2.2 Guard `newm1`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func newm1(mp *m) {
        if iscgo {
                ...
                asmcgocall(_cgo_thread_start, unsafe.Pointer(&ts))
                return
        }
        execLock.rlock()
        newosproc(mp)
        execLock.runlock()
}
```

Add:

```go
if goexperiment.Onethread {
        throw("newm1 in onethread mode")
}
```

This is redundant with `newm`, but useful as a second guard.

### 2.3 Guard OS thread creation

Files:

```text
/usr/local/dev-go/go/src/runtime/os_linux.go
/usr/local/dev-go/go/src/runtime/os_darwin.go
```

Linux current:

```go
func newosproc(mp *m) {
        stk := unsafe.Pointer(mp.g0.stack.hi)
        ...
        ret := retryOnEAGAIN(func() int32 {
                r := clone(...)
                ...
        })
        ...
}
```

Change:

```go
func newosproc(mp *m) {
        if goexperiment.Onethread {
                throw("newosproc in onethread mode")
        }
        ...
}
```

Darwin current:

```go
func newosproc(mp *m) {
        ...
        pthread_create(...)
}
```

Change similarly.

### 2.4 Guard `newosproc0`

Files:

```text
/usr/local/dev-go/go/src/runtime/os_linux.go
/usr/local/dev-go/go/src/runtime/os_darwin.go
```

`newosproc0` creates a raw OS thread without a valid G. Guard it too:

```go
func newosproc0(stacksize uintptr, fn unsafe.Pointer) {
        if goexperiment.Onethread {
                writeErrStr("runtime: newosproc0 in onethread mode\n")
                exit(1)
        }
        ...
}
```

Use `throw` if safe in that context; if not, use `writeErrStr` and `exit(1)`.

### 2.5 Guard `startTemplateThread`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func startTemplateThread() {
        if GOARCH == "wasm" {
                return
        }
        ...
        newm(templateThread, nil, -1)
}
```

Change:

```go
func startTemplateThread() {
        if GOARCH == "wasm" || goexperiment.Onethread {
                return
        }
        ...
}
```

This should return rather than throw because some paths may call it defensively. Any actual attempt to use the template thread later should still fail clearly.

### 2.6 Guard `startm`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func startm(pp *p, spinning, lockheld bool) {
        ...
        nmp := mget()
        if nmp == nil {
                ...
                newm(fn, pp, id)
                ...
                return
        }
        ...
        notewakeup(&nmp.park)
}
```

In `onethread`, `startm` should never create or wake another M.

Add:

```go
if goexperiment.Onethread {
        if pp != nil {
                // The only valid P should already be owned by the current M,
                // or this path represents a forbidden syscall/idle handoff.
        }
        throw("startm in onethread mode")
}
```

But be careful: some code may call `wakep` or `startm` when work is already on the current P. The better final shape is:

- avoid calling `startm` from `onethread` paths
- leave this guard to catch missed paths

### 2.7 Guard `wakep`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func wakep() {
        if sched.nmspinning.Load() != 0 || !sched.nmspinning.CompareAndSwap(0, 1) {
                return
        }
        ...
        startm(pp, true, false)
}
```

In `onethread`, waking a P means either:

- the current M will observe work at the next scheduler checkpoint, or
- the sole M is blocked in netpoll and needs `netpollBreak`.

Change:

```go
func wakep() {
        if goexperiment.Onethread {
                if sched.lastpoll.Load() == 0 {
                        netpollBreak()
                }
                return
        }
        ...
}
```

Potential issue:

`netpollBreak` is only valid if netpoll is initialized. Guard with `netpollinited()` if needed.

### 2.8 Guard `handoffp`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current `handoffp` starts another M if the P has work, trace work, GC work, or netpoll work.

In `onethread`, a P handoff should not happen during ordinary operation. The principal caller to forbid is `entersyscallblock`, which releases the P and calls `handoffp(releasep())`.

Add near the top:

```go
if goexperiment.Onethread {
        throw("handoffp in onethread mode")
}
```

If later permissive blocking syscalls are supported, this needs a different branch:

- keep P attached while blocking the whole process
- or mark process-blocked in a special way

For strict v1, throw.

### 2.9 Guard cgo extra M paths

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Relevant functions:

```text
needm
newextram
oneNewExtraM
dropm
```

Add guards:

```go
if goexperiment.Onethread {
        throw("cgo callback in onethread mode")
}
```

Specifically:

- `needm` means a callback/signal on a foreign thread needs an M.
- `newextram` means runtime is creating spare Ms for callbacks.
- `oneNewExtraM` allocates/starts an extra M.

These must not happen.

### 2.10 Guard `cgocall`

File:

```text
/usr/local/dev-go/go/src/runtime/cgocall.go
```

Current:

```go
func cgocall(fn, arg unsafe.Pointer) int32 {
        if !iscgo && GOOS != "solaris" && GOOS != "illumos" && GOOS != "windows" {
                throw("cgocall unavailable")
        }
        ...
}
```

Add at the top:

```go
if goexperiment.Onethread {
        throw("cgocall in onethread mode")
}
```

This should be unreachable because the go command rejects cgo, but runtime checks are important for direct linkname paths and future regressions.

## Phase 3: Replace the Native Idle Path

This is the center of the runtime work.

### 3.1 Understand the current `findRunnable` split

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current `findRunnable`:

1. checks local run queue
2. checks global run queue
3. checks netpoll nonblocking
4. steals work from other Ps
5. checks idle GC
6. calls `beforeIdle`
7. snapshots all Ps
8. drops P
9. possibly blocks in `netpoll`
10. calls `stopm`
11. loops

The current native blocking branch does:

```go
if releasep() != pp {
        throw("findRunnable: wrong p")
}
now = pidleput(pp, now)
unlock(&sched.lock)
...
list, delta := netpoll(delay)
...
pp, _ := pidleget(now)
...
acquirep(pp)
```

This is wrong for `onethread` because:

- it releases the sole P
- it assumes another M may exist or be started
- it may call `stopm`
- it uses multi-M handoff logic

### 3.2 Add `findRunnableOnethreadIdle`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Add a helper around the point immediately after `beforeIdle` and before the "drop P" branch:

```go
if goexperiment.Onethread {
        gp, inheritTime, tryWakeP := findRunnableOnethreadIdle(pp, now, pollUntil)
        if gp != nil {
                return gp, inheritTime, tryWakeP
        }
        goto top
}
```

Helper sketch:

```go
func findRunnableOnethreadIdle(pp *p, now, pollUntil int64) (*g, bool, bool) {
        if getg().m.p.ptr() != pp {
                throw("onethread idle without p")
        }

        // Recheck global queue under sched.lock before sleeping.
        lock(&sched.lock)
        if sched.gcwaiting.Load() || pp.runSafePointFn != 0 {
                unlock(&sched.lock)
                return nil, false, false
        }
        if !sched.runq.empty() {
                gp, q := globrunqgetbatch(int32(len(pp.runq)) / 2)
                unlock(&sched.lock)
                if gp == nil {
                        throw("global runq empty with non-zero runqsize")
                }
                if runqputbatch(pp, &q); !q.empty() {
                        throw("Couldn't put Gs into empty local runq")
                }
                return gp, false, false
        }
        unlock(&sched.lock)

        // Recheck local queue.
        if gp, inheritTime := runqget(pp); gp != nil {
                return gp, inheritTime, false
        }

        // Check timers and get next wake time.
        now, pollUntil, _ = pp.timers.check(now, nil)
        if gp, inheritTime := runqget(pp); gp != nil {
                return gp, inheritTime, false
        }

        delay := int64(-1)
        if pollUntil != 0 {
                if now == 0 {
                        now = nanotime()
                }
                delay = pollUntil - now
                if delay < 0 {
                        delay = 0
                }
        }
        if faketime != 0 {
                delay = 0
        }

        if netpollinited() && (netpollAnyWaiters() || pollUntil != 0) {
                sched.pollUntil.Store(pollUntil)
                sched.lastpoll.Store(0)
                list, delta := netpoll(delay)
                now = nanotime()
                sched.pollUntil.Store(0)
                sched.lastpoll.Store(now)
                if !list.empty() {
                        gp := list.pop()
                        injectglist(&list)
                        netpollAdjustWaiters(delta)
                        trace := traceAcquire()
                        casgstatus(gp, _Gwaiting, _Grunnable)
                        if trace.ok() {
                                trace.GoUnpark(gp, 0)
                                traceRelease(trace)
                        }
                        return gp, false, false
                }
                netpollAdjustWaiters(delta)
                return nil, false, false
        }

        // No netpoll. Sleep until next timer if there is one.
        if pollUntil != 0 {
                if delay > 0 {
                        usleep(uint32(delay / 1000))
                }
                return nil, false, false
        }

        // Deadlock detection should still happen. Reuse checkdead path if possible.
        checkdead()
        return nil, false, false
}
```

This sketch will need adjustment for:

- exact `timers.check` contract
- whether `injectglist` may call `startm` when current P is present
- lock ranks
- no-write-barrier constraints
- fake time
- deadlock detection

The important invariant:

```text
do not release the sole P
do not call stopm
do not call startm
```

### 3.3 Avoid spinning and stealing in `onethread`

In `findRunnable`, the work stealing branch is unnecessary when `gomaxprocs == 1`.

Current:

```go
if mp.spinning || 2*sched.nmspinning.Load() < gomaxprocs-sched.npidle.Load() {
        ...
        gp, inheritTime, tnow, w, newWork := stealWork(now)
        ...
}
```

Under `onethread`, skip this entire branch:

```go
if !goexperiment.Onethread && (mp.spinning || 2*sched.nmspinning.Load() < gomaxprocs-sched.npidle.Load()) {
        ...
}
```

Reason:

- there are no other Ps to steal from
- there are no other Ms to spin
- spinning counters can trigger `startm` paths

### 3.4 Make `injectglist` onethread-safe

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current `injectglist`:

- marks Gs runnable
- if current P is nil, puts them on global queue and starts idle Ms
- if current P exists, uses idle Ps and may start Ms

Under `onethread`, current P should exist when netpoll is called from the onethread idle loop. In that case, it should place all ready Gs onto the current P's local queue or global queue without starting Ms.

Add early branch:

```go
if goexperiment.Onethread {
        pp := getg().m.p.ptr()
        if pp == nil {
                throw("injectglist without P in onethread mode")
        }
        // Mark runnable as usual, convert to q as usual.
        // Put all on current P/global queue, but never call startm.
}
```

Potential implementation:

- reuse the existing status transition code
- convert `gList` to `gQueue`
- call `runqputbatch(pp, &q)` if capacity permits
- if queue remains, put rest on global queue under `sched.lock`
- do not call `startIdle`

Need to respect current `runqputbatch` behavior. If it expects an empty local queue or has capacity assumptions, use global queue for overflow.

### 3.5 Make `wakeNetPoller` onethread-safe

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func wakeNetPoller(when int64) {
        if sched.lastpoll.Load() == 0 {
                pollerPollUntil := sched.pollUntil.Load()
                if pollerPollUntil == 0 || pollerPollUntil > when {
                        netpollBreak()
                }
        } else {
                if GOOS != "plan9" {
                        wakep()
                }
        }
}
```

Change:

```go
func wakeNetPoller(when int64) {
        if goexperiment.Onethread {
                if netpollinited() && sched.lastpoll.Load() == 0 {
                        pollerPollUntil := sched.pollUntil.Load()
                        if pollerPollUntil == 0 || pollerPollUntil > when {
                                netpollBreak()
                        }
                }
                return
        }
        ...
}
```

Reason:

- if the sole M is in blocking netpoll, break it
- if the sole M is running Go code, it will observe work at the next scheduler checkpoint
- never call `wakep`, because that attempts to start another M

### 3.6 Deadlock behavior

Normal Go deadlock detection often happens when all Ms are asleep.

In `onethread`, the sole M may be in the scheduler idle loop. If there are:

- no runnable goroutines
- no timers
- no netpoll waiters
- no finalizers
- no GC work

then the runtime should still report:

```text
fatal error: all goroutines are asleep - deadlock!
```

The onethread idle helper must call or preserve `checkdead()` in this condition.

Be careful not to call `checkdead` while holding locks it does not expect.

## Phase 4: Syscall and Blocking Paths

### 4.1 Guard `entersyscallblock`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
func entersyscallblock() {
        gp := getg()
        ...
        systemstack(func() {
                if trace.ok() {
                        trace.GoSysCall()
                }
                handoffp(releasep())
        })
        ...
        casgstatus(gp, _Grunning, _Gsyscall)
        ...
}
```

This is forbidden in strict `onethread`.

Add near top:

```go
if goexperiment.Onethread {
        throw("blocking syscall in onethread mode")
}
```

This catches:

- note sleeps from user G paths that call `entersyscallblock`
- syscalls known to block
- runtime paths that would release the sole P

### 4.2 Decide what to do with `entersyscall`

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

`entersyscall` is used for syscalls that may not be explicitly blocking. It transitions the goroutine into syscall state and may release/retake P depending on timing.

Initial policy:

- allow `entersyscall` only if it does not release the P in normal fast syscall paths
- do not allow sysmon retake because sysmon is absent
- document that a slow syscall freezes the runtime

Stricter policy:

```go
if goexperiment.Onethread {
        // Either throw here, or enter a special syscall state that keeps P.
}
```

Recommendation:

- v1: allow `entersyscall` for compatibility with common quick syscalls
- v1: forbid `entersyscallblock`
- v2: add a `GODEBUG=onethreadstrictsyscall=1` or Pont-specific stricter mode if needed

### 4.3 Audit direct `entersyscallblock` users

Search:

```sh
rg -n "entersyscallblock" /usr/local/dev-go/go/src/runtime /usr/local/dev-go/go/src/syscall /usr/local/dev-go/go/src/internal
```

Known runtime users include:

- Darwin signal note sleep in `/usr/local/dev-go/go/src/runtime/os_darwin.go`
- Solaris syscall wrappers
- futex note waits through `notetsleepg`

For linux/amd64 and darwin/arm64 target support, prioritize:

```text
/usr/local/dev-go/go/src/runtime/os_linux.go
/usr/local/dev-go/go/src/runtime/os_darwin.go
/usr/local/dev-go/go/src/runtime/lock_futex.go
/usr/local/dev-go/go/src/runtime/lock_sema.go
```

### 4.4 Replace `notetsleepg` for onethread

Files:

```text
/usr/local/dev-go/go/src/runtime/lock_futex.go
/usr/local/dev-go/go/src/runtime/lock_sema.go
```

Linux current:

```go
func notetsleepg(n *note, ns int64) bool {
        gp := getg()
        if gp == gp.m.g0 {
                throw("notetsleepg on g0")
        }

        entersyscallblock()
        ok := notetsleep_internal(n, ns)
        exitsyscall()
        return ok
}
```

This calls `entersyscallblock`, so it will throw.

For v1, decide whether any required path uses `notetsleepg` in ordinary programs. If yes, add an onethread branch:

```go
func notetsleepg(n *note, ns int64) bool {
        if goexperiment.Onethread {
                return notetsleepg_onethread(n, ns)
        }
        ...
}
```

Helper modeled on wasip1:

```go
func notetsleepg_onethread(n *note, ns int64) bool {
        gp := getg()
        if gp == gp.m.g0 {
                throw("notetsleepg on g0")
        }
        deadline := int64(0)
        if ns >= 0 {
                deadline = nanotime() + ns
        }
        for atomic.Load(key32(&n.key)) == 0 {
                Gosched()
                if ns >= 0 && nanotime() >= deadline {
                        return false
                }
        }
        return true
}
```

Potential issue:

Calling `Gosched` from low-level runtime code may not always be valid. If invalid, use `gopark` with a timer or a runtime-internal cooperative wait pattern.

### 4.5 CPU profiler

File:

```text
/usr/local/dev-go/go/src/runtime/cpuprof.go
```

Current:

```go
func SetCPUProfileRate(hz int)
```

CPU profiling uses SIGPROF and OS timer delivery. That is nondeterministic.

In `onethread`, either:

- make `SetCPUProfileRate(hz > 0)` throw
- make it no-op and print a diagnostic
- return silently but do not enable profiling

Recommended:

```go
if goexperiment.Onethread && hz > 0 {
        throw("CPU profiling is unsupported in onethread mode")
}
```

If throwing in public API is too harsh, use:

```go
if goexperiment.Onethread && hz > 0 {
        return
}
```

But silent no-op may surprise users. A fatal runtime throw is consistent with strict determinism.

### 4.6 Signal-based async preemption

File:

```text
/usr/local/dev-go/go/src/runtime/signal_unix.go
```

Current:

```go
const preemptMSupported = true
```

Change this so `onethread` sees false.

Problem:

Go constants cannot call runtime variables. But `goexperiment.Onethread` is a constant, so this should work if `signal_unix.go` imports `internal/goexperiment`:

```go
const preemptMSupported = !goexperiment.Onethread
```

If there are GOOS where async preemption is already unsupported, preserve that:

```go
const preemptMSupported = !goexperiment.Onethread
```

for unix, and keep wasm/plan9 false as they are.

Also guard signal handler preemption branches:

```go
if sig == sigPreempt && preemptMSupported && debug.asyncpreemptoff == 0 {
        ...
}
```

These should naturally become false.

### 4.7 Disable `preemptM`

File:

```text
/usr/local/dev-go/go/src/runtime/signal_unix.go
```

Current:

```go
func preemptM(mp *m) {
        ...
        signalM(mp, sigPreempt)
}
```

Add:

```go
if goexperiment.Onethread {
        return
}
```

This should be redundant if `preemptMSupported` is false, but it is a useful guard.

## Phase 5: GC, Scavenger, and Sysmon Duties

### 5.1 Force-GC helper

File:

```text
/usr/local/dev-go/go/src/runtime/proc.go
```

Current:

```go
go forcegchelper()
```

The helper goroutine itself is fine. It is a goroutine, not an OS thread. But sysmon normally wakes it:

```go
if t := (gcTrigger{kind: gcTriggerTime, now: now}); t.test() && forcegc.idle.Load() {
        ...
        injectglist(&list)
}
```

With no sysmon, time-based forced GC will not happen unless moved elsewhere.

Initial v1:

- accept that time-based forced GC does not happen
- allocation-triggered GC still happens

Better v2:

- in the onethread scheduler idle loop, check the force-GC trigger before sleeping:

```go
if t := (gcTrigger{kind: gcTriggerTime, now: now}); t.test() && forcegc.idle.Load() {
        wakeForceGC()
}
```

Implement as a helper so sysmon and onethread can share it.

### 5.2 Background scavenger

File:

```text
/usr/local/dev-go/go/src/runtime/mgcscavenge.go
```

The scavenger is a goroutine. It can run on one P. But `scavenger.ready()` signals sysmon:

```go
func (s *scavengerState) ready() {
        s.sysmonWake.Store(1)
}
```

Sysmon later does:

```go
if scavenger.sysmonWake.Load() != 0 {
        scavenger.wake()
}
```

In `onethread`, either:

1. make `scavenger.ready()` directly call `scavenger.wake()`, or
2. have the onethread scheduler idle loop check `scavenger.sysmonWake`.

Recommended:

```go
func (s *scavengerState) ready() {
        if goexperiment.Onethread {
                s.wake()
                return
        }
        s.sysmonWake.Store(1)
}
```

Need to check lock context. If `ready` can be called while holding locks that `wake` needs, use option 2:

```go
if scavenger.sysmonWake.Load() != 0 {
        scavenger.wake()
}
```

inside the scheduler idle loop.

### 5.3 GC workers

GC background mark workers are goroutines. With one P, they will interleave cooperatively with mutator goroutines.

Do not disable them by default.

But audit code that assumes multiple Ps:

- `gcController.findRunnableGCWorker`
- `gcShouldScheduleWorker`
- idle mark worker logic in `findRunnable`

With `gomaxprocs == 1`, this should mostly work. The key is preventing the scheduler from starting another M for GC work.

### 5.4 STW and safe points

Stop-the-world with one P should be simpler, but code may still call `preemptall` expecting sysmon/async preemption.

Files:

```text
/usr/local/dev-go/go/src/runtime/proc.go
/usr/local/dev-go/go/src/runtime/mgcpacer.go
/usr/local/dev-go/go/src/runtime/mgcmark.go
```

Keep cooperative preemption checks:

- `gp.preempt`
- stackguard preemption
- safe point functions

But do not rely on async signal delivery.

Potential issue:

A CPU-bound goroutine in a loop with no function calls may starve the runtime if async preemption is disabled and preemptible loops are not enabled. This is already true for wasm-like environments. Pont should either:

- document this
- enable `GOEXPERIMENT=preemptibleloops` together with `onethread`
- teach `-onethread` to also append `preemptibleloops`

Recommendation:

```text
Do not force preemptibleloops in the first patch.
Document it as a companion experiment.
```

Then, after correctness is proven:

```text
consider making -onethread imply GOEXPERIMENT=onethread,preemptibleloops
```

## Phase 6: Lock and Note Simplification

This phase is optional for v1 but desirable for a pure single-thread runtime.

### 6.1 Keep native locks initially

Do not start by replacing all native locks. The first milestone should keep futex/pthread locks because:

- they are already correct
- most lock operations will not block when there is only one M
- changing lock internals increases risk

Instead, focus on:

- no thread creation
- no sysmon
- one P
- same-thread idle loop
- no blocking syscall handoff

### 6.2 Add `lock_onethread.go` later

Possible file:

```text
/usr/local/dev-go/go/src/runtime/lock_onethread.go
```

Build tag:

```go
//go:build goexperiment.onethread && (linux || darwin)
```

But this conflicts with existing files such as:

```text
lock_futex.go    //go:build dragonfly || freebsd || linux
lock_sema.go     // likely darwin and others
```

To avoid duplicate symbols, existing lock files would need build tag exclusions:

```go
//go:build (dragonfly || freebsd || linux) && !goexperiment.onethread
```

and equivalent for darwin.

This is invasive. Defer it.

### 6.3 If simplified, copy wasm semantics

Use wasm style:

```go
func lock2(l *mutex) {
        if l.key == mutex_locked {
                throw("self deadlock")
        }
        gp := getg()
        gp.m.locks++
        l.key = mutex_locked
}

func unlock2(l *mutex) {
        if l.key == mutex_unlocked {
                throw("unlock of unlocked lock")
        }
        gp := getg()
        gp.m.locks--
        l.key = mutex_unlocked
}
```

Notes:

- `notesleep` on g0 can block the process only if the scheduler intentionally sleeps.
- `notetsleepg` should park/yield cooperatively, not call `entersyscallblock`.

## Phase 7: Tests

### 7.1 Command tests

Directory:

```text
/usr/local/dev-go/go/src/cmd/go/testdata/script
```

Add a script test:

```text
go build -onethread .
go version -m ./binary
stdout 'GOEXPERIMENT=.*onethread'
stdout '-onethread=true'
```

Add negative tests:

```text
! go build -onethread -race .
stderr '-onethread is incompatible with -race'

! go build -onethread -msan .
stderr '-onethread is incompatible with -msan'

! go build -onethread -asan .
stderr '-onethread is incompatible with -asan'

! env CGO_ENABLED=1 GOEXPERIMENT=onethread go build .
stderr 'onethread requires cgo to be disabled'

! go build -onethread -buildmode=plugin .
stderr '-onethread is incompatible with -buildmode=plugin'
```

### 7.2 Runtime topology test

Directory:

```text
/usr/local/dev-go/go/src/runtime
```

Add a test that only builds/runs under `goexperiment.onethread`.

File:

```text
/usr/local/dev-go/go/src/runtime/onethread_test.go
```

Build tag:

```go
//go:build goexperiment.onethread
```

Test behaviors:

```go
func TestOnethreadTopology(t *testing.T) {
        if runtime.GOMAXPROCS(0) != 1 {
                t.Fatalf("GOMAXPROCS = %d, want 1", runtime.GOMAXPROCS(0))
        }
        if runtime.NumCPU() != 1 {
                t.Fatalf("NumCPU = %d, want 1", runtime.NumCPU())
        }
        old := runtime.GOMAXPROCS(64)
        if old != 1 || runtime.GOMAXPROCS(0) != 1 {
                t.Fatalf("GOMAXPROCS changed in onethread mode")
        }
}
```

Need a way to assert M count. There is no public API. Options:

- add `runtime/debug` API later
- use `export_test.go` to expose `mcount()` to runtime tests
- inspect `/proc/self/task` on Linux in a platform-specific test

For runtime package tests, add in:

```text
/usr/local/dev-go/go/src/runtime/export_test.go
```

```go
func MCount() int32 { return mcount() }
```

Then:

```go
if runtime.MCount() != 1 { ... }
```

But `export_test.go` is package runtime, while external tests may be `runtime_test`. Follow existing patterns.

### 7.3 Scheduler progress test

Test:

- many goroutines
- channels
- timers
- `time.Sleep`
- no second M

Example:

```go
func TestOnethreadSchedulerProgress(t *testing.T) {
        const n = 1000
        ch := make(chan int, n)
        for i := 0; i < n; i++ {
                i := i
                go func() {
                        time.Sleep(time.Microsecond)
                        ch <- i
                }()
        }
        seen := make(map[int]bool)
        for i := 0; i < n; i++ {
                seen[<-ch] = true
        }
        if len(seen) != n {
                t.Fatalf("got %d results, want %d", len(seen), n)
        }
        if MCount() != 1 {
                t.Fatalf("mcount = %d, want 1", MCount())
        }
}
```

This validates timers and scheduler progress.

### 7.4 Netpoll test

Use `net.Pipe` if it exercises runtime netpoll on the platform. If not, use TCP loopback.

Potential issue:

Loopback networking may have host-order nondeterminism, but this test is for liveness, not deterministic ordering.

Test:

- listener accepts
- goroutine reads
- another goroutine writes after timer
- runtime remains one M

### 7.5 Unsupported blocking syscall test

Add a test program that calls a known blocking path and expects a fatal error.

Be careful: tests that intentionally fatal the runtime need subprocess helpers.

Pattern:

- use existing `runTestProg`
- add program under `runtime/testdata/testprog`
- run it with `GOEXPERIMENT=onethread`
- expect `blocking syscall in onethread mode`

### 7.6 Skip or adjust existing tests

Tests likely needing skips under `goexperiment.onethread`:

- tests that require `GOMAXPROCS > 1`
- tests that assert multiple threads
- cgo tests
- race tests
- async preemption tests
- CPU profile tests
- sysmon/GOMAXPROCS auto-update tests
- LockOSThread template-thread race tests

Use:

```go
import "internal/goexperiment"

if goexperiment.Onethread {
        t.Skip("requires multiple runtime threads")
}
```

Likely files:

```text
/usr/local/dev-go/go/src/runtime/proc_test.go
/usr/local/dev-go/go/src/runtime/crash_cgo_test.go
/usr/local/dev-go/go/src/runtime/pprof/pprof_test.go
/usr/local/dev-go/go/src/runtime/preempt_test.go or testdata/testprog/preempt.go
```

Do not blanket-skip `runtime` tests. Most channel, timer, map, GC, and scheduler tests should still pass.

## Phase 8: Documentation

### 8.1 `go help build`

Document:

```text
-onethread
        Build using Pont's single-OS-thread runtime. The resulting binary
        forces GOMAXPROCS to 1, disables runtime-created OS helper threads,
        disables async signal preemption, and rejects cgo, sanitizer, race,
        plugin, shared, c-archive, and c-shared modes.
```

### 8.2 Runtime docs

Add a Pont-specific doc file, perhaps:

```text
/usr/local/dev-go/go/doc/pont-onethread.md
```

Include:

- what it guarantees
- what it does not guarantee
- unsupported packages/features
- blocking syscall policy
- cgo policy
- profiler policy
- recommendation for deterministic time/random/map behavior

### 8.3 Error messages

Standardize fatal strings:

```text
newm in onethread mode
newosproc in onethread mode
startm in onethread mode
handoffp in onethread mode
blocking syscall in onethread mode
cgocall in onethread mode
CPU profiling is unsupported in onethread mode
```

Consistent strings help tests and debugging.

## Phase 9: Suggested Patch Order

### Patch 1: Experiment only

Changes:

- add `Onethread` to `internal/goexperiment.Flags`
- run `go generate`
- verify `GOEXPERIMENT=onethread go env GOEXPERIMENT`
- verify build tag test

Tests:

```sh
cd /usr/local/dev-go/go/src
go test internal/buildcfg internal/goexperiment
```

### Patch 2: `go build -onethread`

Changes:

- add `cfg.BuildOnethread`
- add flag in `AddBuildFlags`
- add `cfg.EnableExperiment`
- normalize `-onethread` before package loading
- disable cgo for flag form
- reject incompatible flags
- add command tests

Tests:

```sh
cd /usr/local/dev-go/go/src
go test cmd/go
```

### Patch 3: Runtime topology

Changes:

- force `sched.maxmcount = 1`
- force `GOMAXPROCS = 1`
- force `NumCPU = 1`
- disable sysmon
- disable GOMAXPROCS auto updates

Tests:

```sh
cd /usr/local/dev-go/go/src
GOEXPERIMENT=onethread go test runtime -run Onethread
```

At this stage, some tests may hang because idle scheduling is not fixed yet. Keep test scope narrow.

### Patch 4: Thread creation guards

Changes:

- guard `newm`
- guard `newm1`
- guard `newosproc`
- guard `newosproc0`
- guard `startTemplateThread`
- guard `startm`
- guard `handoffp`
- guard cgo paths

Tests:

```sh
GOEXPERIMENT=onethread go test runtime -run Onethread
```

Expected result before idle-loop patch:

- simple compute may pass
- timer/sleep/netpoll tests may hang or fail

### Patch 5: Same-thread idle loop

Changes:

- add onethread branch in `findRunnable`
- add `findRunnableOnethreadIdle`
- adjust `wakep`
- adjust `wakeNetPoller`
- adjust `injectglist`
- avoid stealing/spinning branches

Tests:

```sh
GOEXPERIMENT=onethread go test runtime -run 'Onethread|Timer|Sleep|Chan'
GOEXPERIMENT=onethread go test time
```

### Patch 6: Syscall strictness

Changes:

- guard `entersyscallblock`
- adjust `notetsleepg` if required
- guard `cgocall`
- add subprocess test for unsupported blocking syscall

Tests:

```sh
GOEXPERIMENT=onethread go test runtime -run Onethread
GOEXPERIMENT=onethread go test os -run '^TestOpen|^TestRead|^TestWrite'
```

### Patch 7: Preemption/profiler/signal cleanup

Changes:

- `preemptMSupported = !goexperiment.Onethread`
- no-op `preemptM`
- reject CPU profiling
- skip async-preemption-specific tests

Tests:

```sh
GOEXPERIMENT=onethread go test runtime -run Preempt
GOEXPERIMENT=onethread go test runtime/pprof
```

Some tests should intentionally skip.

### Patch 8: Broaden stdlib tests

Start with:

```sh
cd /usr/local/dev-go/go/src
GOEXPERIMENT=onethread go test runtime sync time context errors fmt bytes strings slices maps cmp math
```

Then:

```sh
GOEXPERIMENT=onethread go test net
GOEXPERIMENT=onethread go test os
GOEXPERIMENT=onethread go test ./...
```

Expect failures in:

- cgo packages
- race-related tests
- profiling tests
- tests requiring multiple CPUs
- tests requiring blocking syscall concurrency

## 10. Invariants to Assert in Debug Builds

Add internal assertions under `goexperiment.Onethread`:

```go
func assertOnethread() {
        if !goexperiment.Onethread {
                return
        }
        if gomaxprocs != 1 {
                throw("onethread: gomaxprocs != 1")
        }
        if mcount() > 1 {
                throw("onethread: mcount > 1")
        }
}
```

Call sites:

- end of `schedinit`
- before/after `procresize`
- beginning of `schedule`
- beginning of `findRunnable`
- after netpoll idle loop returns
- before starting GC worker

Do not call it in very low-level nosplit/no-P contexts unless it is safe.

## 11. Major Risks

### 11.1 Blocking syscall compatibility

Many packages assume blocking syscalls can coexist with other goroutines. That assumption requires extra OS threads.

Mitigation:

- strict v1 rejects `entersyscallblock`
- document clearly
- later add nonblocking wrappers for common file/network paths

### 11.2 CPU-bound goroutine starvation

Without async preemption, a tight loop can starve other goroutines.

Mitigation:

- document this
- recommend or imply `preemptibleloops`
- keep cooperative preemption checks working

### 11.3 Netpoll ordering

OS pollers may return ready events in nondeterministic order.

Mitigation:

- document in v1
- add stable sorting in later deterministic I/O mode

### 11.4 Signals

External signals are inherently host-driven.

Mitigation:

- disable async preemption signals
- disable CPU profiling
- keep only synchronous/fatal signal handling
- document external signal nondeterminism

### 11.5 Tests that assume normal Go

Many upstream Go tests assume multiple CPUs, sysmon, cgo, async preemption, or profiling.

Mitigation:

- skip only where assumptions conflict with `onethread`
- do not weaken normal-mode tests

## 12. Definition of Done

The first complete `-onethread` milestone is done when:

- `go build -onethread hello.go` works
- `go version -m hello` reports onethread
- `runtime.NumCPU()` reports 1
- `runtime.GOMAXPROCS(0)` reports 1
- `runtime.GOMAXPROCS(100)` does not change it
- runtime tests prove `mcount() == 1`
- sysmon is not started
- `newm`/`newosproc` are not reached in ordinary operation
- timers and `time.Sleep` work
- channel-heavy goroutine scheduling works
- basic netpoll-backed networking works or is explicitly marked phase 2
- cgo/race/MSan/ASan/buildmode incompatibilities fail early
- blocking syscall paths fail with a clear diagnostic
- normal non-onethread builds are unaffected

## 13. Recommended First Coding Target

The smallest useful target is:

```text
GOEXPERIMENT=onethread go test runtime -run Onethread
```

where `TestOnethreadSchedulerProgress` proves:

- `GOMAXPROCS == 1`
- `NumCPU == 1`
- many goroutines complete
- timers complete
- runtime M count remains 1

After that:

```text
go build -onethread ./hello
```

Then:

```text
GOEXPERIMENT=onethread go test runtime sync time
```

Then:

```text
GOEXPERIMENT=onethread go test net os
```

Only after these pass should Pont attempt broader `all.bash` coverage in onethread mode.

