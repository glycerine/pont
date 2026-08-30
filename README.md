# Pont: a superset of Go with friendly channels and DST

* If building with `src/all.bash` gives vet test failures 
like FAIL: `TestVet/unused`, then you
have a polluted vet cache. Assuming your pont is checked
at /usr/local/dev-go/go, for example, clear your vet
cache like this before doing `clean.bash` and `all.bash` again.
~~~
GOROOT=/usr/local/dev-go/go /usr/local/dev-go/go/bin/go clean -cache
~~~

* News 2026 June 27: the -onethread flag now works on darwin (MacOS), in addition to Linux.

* News 2026 June 24: the -onethread flag. Running `go build -onethread` will generate a go binary that only ever uses a single thread, to reduce the non-determinism of thread scheduling. This applies even to CGO, and we still support Go -> C -> Go; although panics from the Go callback cannot be recover()-ed across the C call. Using recover(), if needed, must happen before the CGO callback to Go returns. Works on Linux, and on Linux with synctest where it is particularly useful. Darwin still in progress.

* News 2026 June 21: we rebased Pont onto go1.26.4. Previously we were on top of go1.25.3.

Meet Ponti, the Pont mascot.

[Ponti is a field vole, species _M. agrestis_](https://en.wikipedia.org/wiki/Short-tailed_field_vole). 

<img width="1024" height="1024" alt="image" src="https://github.com/user-attachments/assets/4b26bc80-7bb7-49a9-8c54-40d46a87dfaf" />

A new programming language obviously requires 
a new mascot for it to be real thing.

Since Pont is focused on channels and connecting
things, I chose the word Pont because it means
"bridge" in many Romance (Latin derived) languages (French,
Welsh, Catalan, Spanish(puente), Italian(ponte), 
Portuguese(ponte), Romanian(pod), etc); from
the Latin root _pons_. The aquaduct imagery
behind Ponti above reminds me of bridges carrying
channels of water, and Pont bridges old and new.

# table of contents

I. [the motivation for a superset](#motivation)

II. [a more deterministic runtime](#pont-more-determinism-on-demand)

III. [the new rules for channels](#pont-the-new-rules-for-channels)

IV. [the FAQ (discussion)](#faq)

V. [license (BSD-3-Clause, same as Go)](#license)

VI. [installation, or how to build from source](#installation)

# motivation

A. enabling DST

Deterministic simulation testing (DST) is one of the few
ways that reliable distributed systems can be developed
in a reasonable amount of time. The old approach
is no longer acceptable: we cannot afford to
test our systems in production for ten
years, and wait for users to find the bugs.

[A case study of this is Etcd, the central coordination 
database for Kubernetes, which is written in Go and
implements Raft for distributed consensus.
After 12 years they still could not locate and eliminate
all of the bugs, and have finally turned to Antithesis, a commerical
vendor offering a DST service for help.](https://github.com/etcd-io/etcd/pull/19739)
See [also this description.](https://github.com/etcd-io/etcd/issues/19299)

Pont adds the GO_DSIM_SEED env variable to seed a
pseudo random number generator used for all 
randomness in the runtime. [See this section
below for more](#pont-more-determinism-on-demand).

The other desiderata are unrelated and orthogonal to DST.

B. I frequently want Go channels that
can broadcast a non-zero value. 

<img width="1250" height="824" alt="image" src="https://github.com/user-attachments/assets/b7086aaa-6f3e-48d0-9b52-10e658f1934d" />

Caption: In the above figure, the channel has a buffer of size 5 but
only one slot, at the head position, is in use. The other 
4 slots remain empty and un-used. The value at the 
head is marked as sticky, and regular recieves
do not remove the sticky value from the head slot.

I want to write:

~~~
ch := make(chan int, 5)
ch <- 9   // a regular Go send on a channel; versus
ch <$ 10  // a sticky-send.

nine := <- ch  // receives 9
ten  := <- ch  // reads 10, does not remove it
also_ten := <- ch // reads 10, because 10 is sticky
still_ten := <- ch // reads 10, because 10 is sticky
again_ten := <- ch // reads 10, because 10 is sticky...
...
~~~

and have 10 be broadcast on channel ch to all
receivers until I clear it:

~~~
clear(ch)  // empty the channel atomically
i_block := <- ch // blocks because ch is now empty
~~~

or atomically replace it:

~~~
ch <$ 11
// only broadcast 11 from now on. 10 is gone.

...

ch <$ 12
// only broadcast 12 from now on. 11 is gone.
~~~

And if I have a sticky-value in my channel, I want
to be able to return my channel to non-sticky behavior:

~~~
ch <$ 13
// 13 is the new sticky value.

...

ch <- 14

// the next receive will consume 14; 
// the sticky value 13 is gone.
// Because we did not use sticky-send, 14
// does not stick in the channel, but
// rather is consumed/removed from the
// channel on any receive such as
<- ch
~~~

To insure cleanup of sticky values, I want to write
~~~
delete(ch) 
// any sticky value can be garbage collected now. 
~~~
All operations on a deleted channel should be no-ops.

C. Channel close should be idempotent. 

This should never be a panic:
~~~
ch := make(chan int)
close(ch)

// This would panic in Golang.
// It is fine in Pont; a no-op.
close(ch)
~~~

During sub-system shutdown, there are many possible
interleavings of goroutines finishing. Since
close provides the broadcast of shutdown state to all
interested parties, establishing a single order
of shutdown is untenable. Close must be idempotent
to enable goroutines to be properly shutdown, and
this is critical for systematic testing.

D. We can do better than sync.WaitGroup

It plays poorly with channels and timeouts.

golang.org/x/sync/errgroup is likewise unsatisfactory.
We don't always want a single error to 
[cancel an entire batch of work](https://pkg.go.dev/golang.org/x/sync/errgroup#Group.Go). 
errgroup.Wait plays poorly with cannels and timeouts; although
its context use offers improved but still
incomplete functionality.

I want to write (simplified to focus on the new behavior):

~~~
n := 10
ch := make(chan int, n)
for i := range n {
    go func(i int) {
        ch <- i
    }(i)
}

// the <| is called "full receive".
// It only receives once ch is full.
// So this will block until all 10
// goroutines have sent in their int
// values:
first := <| ch  // the | looks like a wall (barrier) to me.

assert(len(ch) == n-1)

// INVAR: all n of my goroutines have 
// sent their integer on ch.
// (which goro sent first is still non-deterministic).

~~~

If you ever wished that condition 
variables played nicely with Go
channels, then I invite you to
explore this project, Pont.

All of the above is legal in the Pont language.

All of the above is implemented. You can try it today.

Pont is a fork of Golang, 
layered on top of the released 
Go version 1.26.4 sources.

# Pont: more determinism on demand

As pioneered by FoundationDB and now commercialized
by Antithesis and leveraged by TigerBeetle, deterministic simulation 
testing (DST) is an important methodology for testing 
distributed systems. Interestingly, [determinism can be productively 
thought of as a scalar rather than binary, an
insightful point made by Antithesis Senior Engineer 
Alex Pshenichkin in this talk on how they built
their determinstic hypervisor.](https://antithesis.com/blog/deterministic_hypervisor/) DST benefits from as much determinism as possible. 
Sufficient determinism allows efficient 
debugging (of classically hard to reproduce distributed 
systems bugs) by starting again with the same seed that 
was found to elicit a bug.

To allow DST in Pont, we use the flag: `-onethread` and set these env var:

~~~
GODEBUG=asyncpreemptoff=1
GOMAXPROCS=1 
GO_DSIM_SEED=1 
~~~

where the last, `GO_DSIM_SEED`, is new. It sets the seed for the
pseudo-random-number-generator used for all 
randomness in the Pont (née Go) runtime. It can be
varied to produce different goroutine select
(and map) interleavings. 

Inside an already running test, a call to the
new API,

~~~
runtime.ResetDsimSeed(seed)
~~~

does the same thing. It reseeds the pseudo
random number generators, sets GOMAXPROCS=1,
and sets the internal GODEBUG=asyncpreemptoff=1.

The Go src/runtime/rand.go functions
like randinit(), rand(), rand32(), randn(), and
cheaprand() have been modified to 
use a pseudo random number generator when the
GO_DSIM_SEED env variable is present and set to a decimal
number in the range [0, 1<<64-1]. Underscore
place separators are allowed and ignored, so
the maximum seed 1<<64-1 (a uint64) can be written as
`export GO_DSIM_SEED=18_446_744_073_709_551_615`

[This Polar Signals blog post inspired
this approach, and discusses the minor changes
that were needed.](https://www.polarsignals.com/blog/posts/2024/05/28/mostly-dst-in-go)

Pont, however, is not limited to WASM. Pont
works on any architecture/OS that the standard Go
gc compiler supports. The -onethread flag is
currently supported on Linux and Darwin.

The GODEBUG=asyncpreemptoff=1 turns off goroutine pre-emption
for more deterministic execution.

The GOMAXPROCS=1 requests the use of a single thread
for user goroutines to try and avoid non-determinism
from OS thread scheduling. Pont does not presently
prevent the runtime from creating the traditional Go
background thread pool, but our aim (hope) is that they
will not be used during DST that makes no system
calls for network or file IO.

The -onethread flag to Go causes the entire Go runtime
to run on only a single thread. This eliminates a
major source of non-determinism: the OS thread switching
schedule. Combined with testing/synctest and the
above env var settings, DST becomes viable.

# Pont: the new rules for channels

As an overview and summary:

Pont introduces three new operations on channels.

`ch <$ x` is a sticky-send. The value x sticks in the channel.

`ch <# x` is a final-send. x closes and then sticks in the channel.

`<| ch` is a full-receive. It receives only when ch is full.

Sticky values allow broadcasting non-zero values. 
Full-receives allow channels to manage a batch of goroutines.
These will be elaborated on below.

Here are the 12 points where the Pont language 
differs from Go. In contrast to Golang, in Pont

## 1. channel close is idempotent. 

Moreover, if there is a sticky value in the channel it
will continue to be read, even after
the channel is closed. 

The 2nd return value, the `ok` in

~~~
z, ok := <- ch
~~~

will be false, as usual, for a closed 
channel. You can think of `ok`
as the "mutable" bit rather than
the "open" bit.

## 2. `ch <$ x` is a sticky send of value `x` on channel `ch`.

Sticky sends require a channel buffer size
of at least one. A panic will result if the
channel is unbuffered.

The value `x` in `ch <$ x` sticks 
when it reaches the head of queue. All subsequent
regular 
~~~
z := <- ch
~~~
reads will return the
sticky value `x` into `z`, until the 
sticky value is updated or removed.

The dollar sign in `<$` looks like an "S". And
"S" is for sticky. Plus it really stands out
as "this is different!" which is important.

Plus, "its money". (As Vince Vaughn loved to say
in the 1996 film Swingers).

I tried `<~` but it just wasn't eye catching enough.


## 3. `clear(ch)` will remove any sticky value.

In fact it will clear all values from the channel, sticky or not.

`clear(ch)` will leave the channel `ch` empty, atomically.

## 4. a regular send `ch <- x` will discard any sticky value. 

If you think of a sticky value as a big
balloon that won't fit through the 
head of the channel, the `<-` is a pin that pops it.
In this metaphor, the balloon can be observed any number
of times until it is popped.

If the channel had a sticky
value at its head prior to `ch <- x`, then the
next read value will be `x`, rather than the displaced (and
discarded from the channel) previous sticky value.

Thus "classic" or "pin" sends will return
the channel to "classic" style reads
until such time as another sticky send
is deployed and reaches the head of the channel.

## 5. `ch <$ x; ch <$ y` puts `y` in `x`'s place.

Assuming no other channel operation from
another goroutine was interleaved,
the new sticky value in `ch` will be `y` instead of the
prior sticky value `x`. The value of `y` will reside at 
the same queue position `x` had. Thus if 
`x` was not yet in head position, `y` will not be either.

The count of sticky values in a channel is only
ever zero or one. If present, the sticky 
value is always the last value in the channel.
These are invariants.

Note that each of the two sticky sends above is atomic
individually, but do not form an atomic 
transaction together. Another goroutine could 
have cleared or even deleted the channel in
between. Another goroutine might have
filled up the channel, forcing one or both
of these sends to block. 

Sticky sends block on entering the queue in the same way
that regular sends do. They move towards the
front of the channel in the same FIFO
sequence as usual. It is only when they
get to head position that there is any
difference. Well, the popping behavior 
of a Go pin-send is also different. A 
regular pin-send does not usually delete 
anything just before it in the queue.

## 6. delete(ch) makes all following actions no-ops. ch becomes inert.

Although a closed channel with a sticky
value is immutable, garbage collection
of sticky values may be needed. 

Therefore, `delete(ch)` will clear the 
channel, even if already closed, 
and cause any further actions 
on the channel to be no-ops. 

A second call to `delete` itself is
idempotent, to ensure easy cleanup
from any goroutine in any situation.

Shutdown of part or the whole of a
system inevitable involves logical
coordination races, and using `delete`
allows you to insure that your channels do not
leak memory during cleanup. This may
be needed if your channels have sticky
values in them at cleanup time.

## 7. Sticky values are enqueued after pre-existing values in the channel.

Sticky values do not change the FIFO
order of channel reads. They only affect the channel behavior 
when they reach the head (next to be read) 
position in the channel.

If a channel has a sticky value in
it, it is always the last element
in the channel. There is never any element behind
the sticky element, if present. There is
only ever zero or one sticky value present
in a channel.

The most useful buffer size is one -- for channels
that use a sticky value. We expect larger
buffer sizes to be rare. For completeness, however, we defined
sticky semantics for any size buffer. This allows
any schedule of sends that ends in a sticky 
value to be enqueued. A sticky value can thus
replace most uses of close. And close can
designate the immutable final value of a channel.

`len(ch)` and `cap(ch)` work as usual.

## 8. Sends on deleted channels are no-ops.

A send to a closed channel continues to panic.

This is the same as ever. This applies to both sticky and regular sends.

Sends on deleted channels are no-ops, as above.

Closed can now be used to designate read-only
immutability of the channel's value, which
is a very useful property.

As ever, all channel operations are atomic.

## 9. x := <$ ch is a sticky receive. 

A sticky-receive dequeues either a sticky-value or a regular value;
it ignores the stickiness of the value in the channel when receiving.

## 10. With version numbers, readers can poll conditions.

The channel reader can keep locally the latest version number 
received when polling for a condition change
to notice when the broadcast value has changed. 

Hence one can sticky-send increasing integer
versions on a `chan int`, or include a
version number in the channel payload struct.

If our 2nd return value from a channel
read is false (indicating the channel
is no longer mutable), then the value 
obtained is the final value, and there can be
no other (different) value observed later.

If you don't want to force your clients
to poll, you can simply clear the channel
and then send a sticky value to tell
clients that a new condition/value is ready. 

Clients will block as usual on a read from an empty
channel. If you send non-sticky values,
you have a "Signal" effect that 
will wake only one recipient. Compare this
to the sync.Cond conditional variable Signal.

Conversely, sending a sticky value will
be observed by all readers until it
is updated, cleared, pin-popped, or
consumed with a sticky-receive:
~~~
x := <$ ch // a sticky-receive dequeues a sticky-value or a regular value.
~~~
Thus sending a sticky value to an empty channel
is like a condition variable "Broadcast".

But, you may ask, a broadcast on a condition 
variables wakes everyone once. What about
goroutines that observe the sticky value
a second time?

You did record your version number right?
The goroutine itself can tell if they
have already processed that version.

Moreover you should probably deploy a second
empty channel as the "finish line" barrier;
rather than re-using the "starting flag".
After all, the first past the starting flag
could always reach the starting flag again
before any of the other goroutines has
left the starting point.

<img width="1024" height="1024" alt="image" src="https://github.com/user-attachments/assets/b1b17af4-586e-47c8-b570-97ef11e5a259" />


With sticky sends, you can ping-pong 
between clear() and sticky sends.
You can create as many work phases as you like
by deploying an additional channel per
phase.

How do I know I have everyone (e.g. all 
worker goroutines)? What if there
are stragglers? sync.Cond variables don't 
make such guarantees either. In Go,
you would need to use a sync.WaitGroup,
or write your own custom batch processor
(my typical solution).

In Pont, you can instead use a full-receive,
which is based on channels and thus plays
well with select and thus with timeouts.

## 11. full receive x = <| ch only reads from a full channel ch.

The full-receive operator, `<| ch` on a channel `ch` blocks
until `ch` is full. This lets channel operations
replace sync.WaitGroup. sync.WaitGroup does not
allow timeouts and does not interact with
select statements. Thus it is inappropriate
for production code that must be able to abort
a task.

The `x` value received from `x = <| ch` will be,
as usual, the head or first value in the channel.

The `|` character in full-receive `<|` was chosen 
because it looks like a wall. Since full-receive 
acts as a barrier or wall until the channel is full;
a full-receive on an un-full channel "hits the wall" and blocks.

As general advice, avoid sharing your channel 
with other goroutine receivers, if you full-receive on it.

We recommend, in general, that you share such 
channels only with senders. Any other receiver 
could starve the full-receiver goroutine.

If other goroutines interleave regular receives, they will 
consume from the channel and prevent
it from becoming full. The full-receiving goroutine could
then be starved. If you must share to other
receivers, be sure to do your full-receive in 
a select with other channels such as 
bailout or timeout channels, to account for
starvation scenarios. You probably don't want
to do a full-receive with more than one receiver
goroutine.

## 12. ch <# x is a final sticky send of x on ch: atomically enqueue x as sticky, and close ch.

The '#' in the final send operator is eye catching, as it should be.

Without the final send '<#' operator, there would be no atomic way
to both enqueue a sticky value and to mark the channel as closed.

Technically the channel is closed an instant before the
final value (x in the example) reaches the head of 
the channel. Hence, for instance, a range over the channel 
will stop before receiving the final value. Of course
you should never range over channels in anything
written for production, as it precludes task cancellation
and subsystem shutdown.

## what else?

Your thoughts?


There are examples/tests of the new Pont behavior
in 
~~~
test/finalsend.go
test/finalrange.go
test/piperecv.go
test/piperecv2.go
test/pipeselect.go
test/stickyrecv.go
test/sticky.go
test/sticky2.go
test/sticky3.go
~~~

------
# license

The Pont additions are

Copyright (C) 2025 Jason E. Aten, Ph.D.

LICENSE: BSD-3 clause, same as Go. See the LICENSE file.

# installation

This Github repository is a fork of the Go language
v1.26.4 source code.

Check out the `pont` branch to build Pont.

[Follow the usual build from source directions](https://go.dev/doc/install/source), and 
run ./all.bash from the src/ directory. 

https://github.com/glycerine/pont/tree/pont


# FAQ

## Q: Nice design! When will it be ready for me to try?

A: Thanks. It is ready now. The design above is 
fully implemented herein. It took about 4 days to write,
because I knew almost nothing about the Go compiler.

An experienced person familiar with the code base 
could probably have done it in a day or two.

Play with it and let me know what you think.

Please open issues on this repo to discuss Pont.

## Q: Do you expect to get this merged into regular Go?

A: No. I made it for myself. It is a fork.

I have become convinced that the Go project will 
never address the poor ergonomics of its channel
design.

There are workarounds, I have made some of them
myself. But they are still inelgant if not clunky. They do
not solve all the issues that Pont addresses.

I tried. For example, https://github.com/glycerine/loquet
is an attempt at broadcasting non-zero values
without any language change. The
lack of atomic receive meant I got races I did
not want.

So. Broadcasting a non-zero value is not solved with
workarounds. Integrating conditions with select
actions such as timers is also not solved.

Thus a fork is required. 

I make no futher conjecture. As the man 
wrote several hundred years back, "hypotheses non fingo".

## Q: Is Pont backwards compatabible with Go v1.26.4?

A: Yes, except for one thing. That one thing is
a behavior you are almost surely not depending on,
but may have cursed excessively at in the past: closing
a channel twice in Go causes a panic. Closing a
channel in Pont never panics, no matter how many
times you close it.

Does your code's correctness depend on having
a panic when you close a channel twice? 

That would be highly unusual. However, if you depend on that
rare case for correctness, then Pont will still compile
your code, but running your code will break
correctness under Pont. 

Otherwise, Pont will compile and run your Go code fine. 
Pont is a superset of Go, except that closing a channel 
is now idempotent.

This is such an obvious design blunder in Go that it is worth
fixing and breaking backwards compatability in a de minimis
count of cases.

What probably happened is that the designers of Go
were not sure what to do there. Arguably there is
evidence that they were not aware of how important
channel close is to reliable sub-system shutdown.

We conjecture that they made close non-idempotent 
to be super conservative. They made a double channel 
close panic, thinking that it could be 
altered later but not put back if allowed. 

Well, time has told us this is an incredible 
painful behavior, and the time has come to fix it.

A personal note:

Does your code really depend on having non-idempotent channel closes?
Usually such code is already broken under Go, and the
developer would have repaired that long ago.

I have written Go for a long time, since go1.2,
and I have only ever wanted that behavior exactly once to
debug a very particular situation --and that 
one time still had, and has, a trivial alternative.

Conversely I use https://github.com/glycerine/idem all 
the time to get idempotently closable channels, which
are essential for clean sub-system shutdown (and thus essential
for testing your systems).

To sum up, besides that singular semantic change in close, 
Pont is fully backwards compatible with Go v1.26.4. 
The Go test suite is fully green. (We deleted the 
test for double close causing a panic).

-------

the original Go README from https://github.com/golang/go
follows for reference.

------

# The Go Programming Language

Go is an open source programming language that makes it easy to build simple,
reliable, and efficient software.

![Gopher image](https://golang.org/doc/gopher/fiveyears.jpg)
*Gopher image by [Renee French][rf], licensed under [Creative Commons 4.0 Attribution license][cc4-by].*

Our canonical Git repository is located at https://go.googlesource.com/go.
There is a mirror of the repository at https://github.com/golang/go.

Unless otherwise noted, the Go source files are distributed under the
BSD-style license found in the LICENSE file.

### Download and Install

#### Binary Distributions

Official binary distributions are available at https://go.dev/dl/.

After downloading a binary release, visit https://go.dev/doc/install
for installation instructions.

#### Install From Source

If a binary distribution is not available for your combination of
operating system and architecture, visit
https://go.dev/doc/install/source
for source installation instructions.

### Contributing

Go is the work of thousands of contributors. We appreciate your help!

To contribute, please read the contribution guidelines at https://go.dev/doc/contribute.

Note that the Go project uses the issue tracker for bug reports and
proposals only. See https://go.dev/wiki/Questions for a list of
places to ask questions about the Go language.

[rf]: https://reneefrench.blogspot.com/
[cc4-by]: https://creativecommons.org/licenses/by/4.0/
