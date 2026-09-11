# TODO

Work that is known and not done. Questions about the *model* rather than the
code live in [chat-concepts.md](chat-concepts.md) -- three open in the core,
four more in the sections deferred there -- and are not duplicated here.

## Done

### The tree is Go only

`server/`, `messaging/` and the pytest suite are deleted. The conformance suite,
the terminal client and the demo are ported to `go/conformance`, `go/cmd/minos`
and `go/cmd/demo`. The entries below predate this and describe the tree at the
time; the CHANGELOG has the details.

### The server is reimplemented in Go

`go/` is the server. `server/` and `messaging/` are the specification it was
written from, and both pass `tests/conformance` -- 161 tests over HTTP and a
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

### The web client is gone

`client/` spoke the retired protocol -- rooms as sets of people, `merge`,
membership edited by dragging -- and the server had stopped answering any of it.
Removed rather than ported: `tui/` already reaches every operation, and porting
would have meant a second front end to keep in step with each change to the
model.

Its 115 tests went with it, which is no loss of coverage: they drove a mocked
socket, so they reported green on a front end that could not connect, and every
route they pinned is pinned against a running server by
`tests/conformance/test_http.py`. The JavaScript toolchain is gone with them --
`make client`, `make dev`, and the npm and corepack fallback in the Makefile.

### Channels have an audience rule

`chat-concepts.md` 2.4 is implemented. `channel_audience` stores the groups a
channel admits, `subscribe` refuses anyone outside them, and `channel.admit` /
`channel.revoke` are how an admin sets the rule. A channel with no groups is
open, which is what `system` is.

The question that was worth deciding first was the third one: eligibility is
re-read on every delivery rather than fixed when the subscription was stored.
Someone removed from the last group that admitted them keeps the subscription
and leaves the audience -- the server does not destroy a choice it cannot
restore, and re-admitting them needs nothing from them. The cost is a group
resolution per fan-out and a row that can name someone who currently reads
nothing.

### The timeline database is versioned and upgradeable

Both servers stamp `PRAGMA user_version`, refuse a database whose version they
cannot reach, and run the steps between an older version and their own. The
marker covers the tables both read; `presence` and `occupants` are the Python
server's own, and their absence from a database `go/` wrote is not a mismatch.

Two things are worth knowing before the first non-additive change. Every step so
far is additive, so `MIGRATIONS[2]` is empty and the schema script does the work
-- a step that moves data will be the first to test the machinery. And nothing
downgrades: a database opened by a later server is refused by an earlier one,
which is correct and means a rollback needs the file kept aside first.

### Channels are founded and published to over the wire

`channel.create` and `channel.publish` are administrators only, and the core
answers who may publish without waiting for section 5: publication is not the
subscribers', and moderators widen that set rather than defining it. A channel
is announced on `system` when it is founded, because nobody is subscribed to it
yet.

What section 5 still owes is submissions: a user's message becoming a proposal a
moderator approves, held outside the channel's sequence so a rejection leaves no
gap for a client to re-request forever.

### The specification is frozen at the core

`server/` and `messaging/` specify section 2 and stop there. Sections 4 and 5 are
written in Go alone.

The two-implementation proof has already paid for itself -- it is what
`docs/wire-contract.md` and 161 conformance tests were built on, and porting
found two faults the specification did not have. What it costs per feature is
measurable: the audience rule was 597 lines of Go and 329 of specification, so
the second copy adds 55%. Submissions are larger than the audience rule and
would be written twice in two languages for one client.

So `tests/conformance/` splits. The suite that both servers pass covers the core
and stays that way; anything section 5 adds is Go-only, and a test that reaches
it does not run against `server/`.

### The credentials are a fixture, not a placeholder

`config.USERS` and `config.ADMINS` are three plain-text accounts and one
administrator, in both servers. No adapter is coming, because this server is not
to be exposed. That is now said where they are defined rather than standing here
as work.

### Submissions and moderation are built, in `go/`

`chat-concepts.md` section 5, beyond the core. The wire additions are
`docs/wire-contract.md` section 9, and the CHANGELOG has the rest.

## Open

Nothing in the core. See `chat-concepts.md` section 4, and the three questions
in section 3.

