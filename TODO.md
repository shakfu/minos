# TODO

Work that is known and not done. Questions about the *model* rather than the
code live in [chat-concepts.md](chat-concepts.md) -- five open in the core,
eight more in the sections deferred there -- and are not duplicated here.

## Parked

### Reimplement the server in a compiled language

Worth doing eventually, and worth not doing yet. **Go is the recommendation**;
the conditions under which Rust would be the better call are below.

**It is not a performance argument, and it should not be mistaken for one.**
`pyzmq` is a binding to libzmq, so the framing, the socket I/O, the internal
queueing and the poller are already C, with the GIL released across those calls.
A pure-language reimplementation of ZeroMQ -- `zmq.rs`, `go-zeromq/zmq4` -- is as
likely to be slower than libzmq as faster. Whatever a rewrite gains comes from
removing Python, never from removing libzmq.

**What it actually buys is architectural.** The message bus exists because of the
GIL: the connection registry is a set in memory, which is the whole story until
gunicorn runs several worker processes, and several processes exist because
CPython cannot use several cores in one. A compiled runtime has no such
constraint. One process with a goroutine or a task per connection serves
everybody, and the fan-out is in-process, which deletes:

- `messaging/bus.py` entirely -- `Broker`, the sender and relay threads, the
  thread-local PUSH sockets, `release_thread`
- the `broker.lock` claim, and the argument about why a lock beats a failed bind
- `WorkerLease`, `sweep_dead_workers`, and the `worker-*.lock` files
- the `SETTLE` delay, and every `PROPAGATION` sleep in the tests

That is the share of this codebase with the subtlest failure modes, which is the
better reason to want it gone than any throughput number.

**What survives is the sequence number.** It is not only a repair for what
ZeroMQ drops: it covers a reconnect, a client that was away for an hour, and a
lagging receiver -- which a Go channel or a `tokio` broadcast drops much as
PUB/SUB does. The delivery contract is transport-independent, which is why it is
the part worth keeping whatever replaces the rest.

#### Why Go

The workload is many long-lived websocket connections, small JSON frames fanned
out to subsets, and SQLite writes with a serialised sequence assignment. That is
I/O-bound, high-concurrency and low CPU per message -- close to what Go was
designed for. The current thread-per-websocket structure becomes
goroutine-per-connection at roughly 1:1, at a couple of KB each instead of an OS
thread. Garbage collection is a non-issue at this shape: the payloads are small
and short-lived, and a chat message does not care about a sub-millisecond pause.

Rust's costs apply to all of that and its advantages to a narrow slice. Async
Rust means choosing a runtime, `Send` bounds on futures, `Pin`, and function
colouring -- real friction for a server whose job is thousands of mostly idle
connections.

**Note what happened to the strongest argument for Rust.** It was that
`bus.py`'s "no socket is touched by two threads" is an invariant enforced by
prose, and that `Send`/`Sync` would enforce it instead. But the same
single-process reasoning that motivates the rewrite deletes `bus.py`. Rust's
best card here is played against code that would not survive.

**Rust becomes the better call if** connection counts grow large enough that
per-connection memory matters; if anything CPU-heavy arrives -- end-to-end
encryption, media relaying, CRDT merges; or if there are hard latency bounds.
None of those are on the table while the server is what `chat-concepts.md`
describes.

#### On ZeroMQ, which neither language loses

The options are the same shape in both: mature bindings to libzmq
(`pebbe/zmq4`, `rust-zmq`), or a pure-language reimplementation
([go-zeromq/zmq4](https://github.com/go-zeromq/zmq4),
[zmq.rs](https://github.com/zeromq/zmq.rs)). Both reimplementations carry
XPUB/XSUB and both are candid about being incomplete; the Go one is marked WIP
and its maintainer has asked for a successor, so check the state of either
before depending on it.

One asymmetry cuts against Go: a cgo call occupies an OS thread the goroutine
scheduler cannot preempt, so libzmq on a messaging hot path fights the model Go
was chosen for. Rust has no green-thread scheduler to disrupt. If libzmq were
central this would matter -- but it is not central, because the bus does not
survive the rewrite at all.

**When cross-host delivery is genuinely needed, the answer is probably NATS
rather than ZeroMQ.** It solves this exact problem, it is written in Go so the
client is first-class, and it improves on two things the current design works
around:

- Subject filtering is hierarchical rather than a byte prefix. `bus.py`
  terminates topics with `|` precisely because a SUB filter is a prefix match
  and `room.1` would otherwise swallow `room.11`. NATS subjects make those
  distinct with no hack.
- It authenticates. The README already admits the gap: anything that can reach
  the `ipc://` sockets in `.run/` can publish to any room.

The honest cost is an operational component: NATS is a server to run, where
ZeroMQ's brokerless `ipc://` is lighter for single-host multi-process -- which is
exactly the configuration that disappears.

Either way, `deliver(audience, event)` keeps the choice contained to one
callback, which is what makes this swappable rather than a second rewrite.

**Cheaper wins come first.** Where Python actually costs something here is the
work *around* the transport, and that is addressable without leaving it. The
fan-out used to run one `json.dumps` per recipient for an identical frame;
`Registry.broadcast` now encodes once, which removes a cost that grew with the
size of a room rather than with the number of messages in it. Anything else of
that shape should be found and fixed before a rewrite is argued for, because
each one makes the argument weaker.

**Why not yet.** The model settled recently and the core has been implemented
for less time still. A transport rewrite does not advance it, and the tests that
currently encode its semantics exist only in Python. The frozen wire contract is
what makes the rewrite safe when it comes: two clients speak it, and `tui/` makes
no assumption about what language answers, so both become conformance tests for
whatever replaces the server.

## Open

### The web client is stale at runtime

`client/` speaks the retired protocol: rooms as sets of people, `merge`,
membership edited by dragging. The server no longer answers any of it, so the
browser front end will not work against this server until it is either ported to
the current model or removed.

Its own tests still pass, which is the trap -- they drive a mocked socket, so
`make test` reports green on a front end that cannot connect. Whoever picks this
up should decide between porting and removing rather than leaving it in the
tree looking maintained.

### The timeline database has no migrations

The schema changed with the model and `Timeline.init` only runs
`CREATE TABLE IF NOT EXISTS`, so an existing `.run/timeline.db` from before is
neither upgraded nor rejected -- it simply lacks the tables and columns the
server now reads. Deleting `.run/` is the current answer, which is fine while
this is a demo and stops being fine the moment anything is worth keeping.

### Administrators are a set in a config file

`config.ADMINS` names them, and the role reaches the rest of the server on the
session profile's `groups`. That is enough to demonstrate the authority split
and is the same shape of placeholder as `config.USERS`; both want a real adapter
before this server is exposed.

