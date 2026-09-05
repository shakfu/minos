# TODO

Work that is known and not done. Questions about the *model* rather than the
code live in [chat-concepts.md](chat-concepts.md) -- five open in the core,
eight more in the sections deferred there -- and are not duplicated here.

## Parked

### Reimplement the server in Rust

Worth doing eventually, and worth not doing yet.

**It is not a performance argument, and it should not be mistaken for one.**
`pyzmq` is a binding to libzmq, so the framing, the socket I/O, the internal
queueing and the poller are already C, with the GIL released across those calls.
zmq.rs is a from-scratch reimplementation whose pitch is the absence of a C
dependency rather than throughput; against libzmq's fifteen years of tuning it
is as likely to be slower. Whatever a rewrite gains comes from removing Python,
never from removing libzmq -- which also means the choice between zmq.rs and
`rust-zmq` is about dependencies and async ergonomics, and never about speed.

**The case for it.** The hard parts of this server are all concurrency, and they
are enforced by prose rather than by anything that checks. `messaging/bus.py`
spends a paragraph explaining that ZeroMQ contexts are thread-safe while sockets
are not, so no socket may be touched by two threads -- publishers hand frames to
a sender thread, subscription changes go to the relay, and each owns its sockets
outright. That is `Send`/`Sync` written out longhand and policed by review. A
compiler would police it instead. The same goes for `release_thread()`, which
exists because a ZeroMQ socket holds a file descriptor that garbage collection
will not reclaim, and flask-sock runs a thread per websocket.

**Most of the bus would not survive the port, which is the point.** The message
bus exists because of the GIL: the connection registry is a set in memory, which
is the whole story until gunicorn runs several worker processes, and several
processes exist because CPython cannot use several cores in one. Rust has no
such constraint. One process with a thread pool serves every connection and the
fan-out is an in-process broadcast, which deletes:

- `messaging/bus.py` entirely -- `Broker`, the sender and relay threads, the
  thread-local PUSH sockets, `release_thread`
- the `broker.lock` claim, and the argument about why a lock beats a failed bind
- `WorkerLease`, `sweep_dead_workers`, and the `worker-*.lock` files
- the `SETTLE` delay, and every `PROPAGATION` sleep in the tests

That is an *architectural* tax rather than a throughput one: the broker, the
liveness locks and the fd bookkeeping exist because CPython cannot use several
cores in one process, not because ZeroMQ is slow. It is also the share of this
codebase with the subtlest failure modes, which is the better reason to want it
gone.

**What survives is the sequence number.** It is not only a repair for what
ZeroMQ drops: it covers a reconnect, and a client that was away for an hour, and
a lagging receiver -- which a `tokio` broadcast channel drops exactly as PUB/SUB
does. The delivery contract is transport-independent, which is why it is the
part worth keeping.

**On [zmq.rs](https://github.com/zeromq/zmq.rs) in particular**, if a bus is
wanted at all. It covers what minos uses -- PUB/SUB with XPUB/XSUB, over
`tcp://` and `ipc://`. Two gaps, both survivable: there is no `zmq_proxy`, so
`Broker` becomes a hand-written forward loop of about fifteen lines; and there
is no `inproc://`, which only matters because `bus.py` uses it for the queues
that keep sockets off other threads, and those are a channel in Rust. The real
caveat is its own README's: it does not implement all of ZeroMQ and is not
offered as production-ready. Check recent activity on crates.io before depending
on it. `rust-zmq` is the mature alternative, at the cost of the C dependency a
rewrite is presumably escaping -- and, per the note above, at no cost in speed
either way.

**If it happens, the order matters.** Keep `deliver(audience, event)` as the
seam and implement it in-process first; reach for a bus only when there is
genuinely a second host. Adding ZeroMQ on day one would port the workaround
along with the thing it works around.

**Cheaper wins come first.** Where Python actually costs something here is the
work *around* the transport, and that is addressable without leaving it. The
fan-out used to run one `json.dumps` per recipient for an identical frame;
`Registry.broadcast` now encodes once, which removes a cost that grew with the
size of a room rather than with the number of messages in it. Anything else of
that shape should be found and fixed before a rewrite is argued for, because
each one makes the argument weaker.

**Why not yet.** The model settled recently and the core has been implemented
for less time still. A transport rewrite does not advance it, and the 150 tests
that currently encode its semantics exist only in Python. The frozen wire
contract is what makes the rewrite safe when it comes: two clients speak it, and
`tui/` makes no assumption about what language answers, so both become
conformance tests for whatever replaces the server.

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
