# TODO

Work that is known and not done. Questions about the *model* rather than the
code live in [chat-concepts.md](chat-concepts.md) -- three open in the core,
eight more in the sections deferred there -- and are not duplicated here.

## Done

### The server is reimplemented in Go

`go/` is the server. `server/` and `messaging/` are the specification it was
written from, and both pass `tests/conformance` -- 149 tests over HTTP and a
websocket, run by `make conformance-go` and `make conformance`. `tui/` drives
either without knowing which.

**It was never a performance argument.** `pyzmq` binds libzmq, so the framing,
the socket I/O and the poller were already C with the GIL released across them.
What the port bought is architectural, and it is what the note that stood here
predicted: one process with a goroutine per connection removes the reason the
message bus existed at all. Gone rather than ported --

- `messaging/bus.py` entirely: the broker, the sender and relay threads, the
  thread-local PUSH sockets, `release_thread`
- the `broker.lock` claim, and the argument about why a lock beats a failed bind
- `WorkerLease`, `sweep_dead_workers`, and the `worker-*.lock` files
- the `presence` and `occupants` tables. They describe live connections, and the
  process that holds the connections is now the process that answers for them
- the `SETTLE` delay, and every `PROPAGATION` sleep

`rooms.empty_since` stayed, because it outlives the connections. Start-up stamps
every transient room not already counting down, which is what carries the promise
of deletion across a restart instead of losing it there.

**What survived is the sequence number**, for the reason given before the port:
it never repaired the bus alone. A reconnect, an hour offline and a lagging
reader all outlive any transport, and the terminal client's gap repair works
against the Go server unmodified.

**ZeroMQ is not in it, and NATS is not needed yet.** Cross-host delivery is the
only thing that would bring a broker back, and `deliver(audience, event)` is
still the single callback it would go behind. Until then a fan-out is a function
call over a map of connections.

Two faults the port found, neither of which the specification had:

- A fan-out that wrote synchronously let one unresponsive peer stall every other
  recipient for a full write timeout. Each connection now has an outbound queue
  and a writer goroutine, and a client that cannot keep up is disconnected rather
  than waited for -- it reconnects and repairs from its cursor.
- Tying a websocket's lifetime to `r.Context()` is wrong: the upgrade ends the
  request, and `net/http` may cancel that context under a hijacked connection.
  The socket's lifetime is now the server's, ended by `httpapi.Server.Close`.

**Cheaper wins still come first for `server/`.** It is a specification now, so
clarity beats throughput there, and any behaviour worth having is worth stating
in `docs/wire-contract.md` before it is written in either language.

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

### Channels have no audience rule

`chat-concepts.md` 2.4 says a channel is open or restricted to named groups.
Every channel here is open: `subscribe` checks that the channel exists and
nothing else. It has not mattered, because `system` is the only channel and
every account is subscribed to it at start-up.

Three things are needed, and the third is the one worth deciding before the
first two: a stored set of eligible groups, an eligibility check on `subscribe`,
and an answer for the subscriber who leaves the last group that admitted them.
Dropping them is consistent with a room's group grant, which is re-evaluated
rather than snapshotted -- but a subscription was their own act, and revoking it
is not the same as never having granted it.

### The timeline database has no migrations

The schema changed with the model and `Timeline.init` only runs
`CREATE TABLE IF NOT EXISTS`, so an existing `.run/timeline.db` from before is
neither upgraded nor rejected -- it simply lacks the tables and columns the
server now reads. Deleting `.run/` is the current answer, which is fine while
this is a demo and stops being fine the moment anything is worth keeping.

The port added a second writer of the same file with a slightly different
schema: `go/` never creates `presence` or `occupants`, and ignores them where a
database written by `server/` has them. That happens to work and is not a design
-- there is no version marker, so neither implementation can tell a database it
understands from one it does not.

### Administrators are a set in a config file

`config.ADMINS` names them, and the role reaches the rest of the server on the
session profile's `groups`. That is enough to demonstrate the authority split
and is the same shape of placeholder as `config.USERS`; both want a real adapter
before this server is exposed.

