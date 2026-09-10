# Changelog

Notable changes to minos. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Nothing is released
yet, so everything so far sits under Unreleased.

## [Unreleased]

Both web front ends are gone. `tui/` is the only client, `go/` is the server,
and the wire format between them is still OS.js's without any OS.js code.

### Removed

- `client/`, the web desktop, and the last JavaScript in the tree with it: Vite,
  Vitest, TypeScript, `client/node_modules`, and the `client` and `dev` make
  targets. It spoke the retired conversation model -- rooms as sets of people,
  `merge`, membership edited by dragging -- so it had not connected to this
  server since the model changed. Removed rather than ported, because `tui/`
  already reaches every operation and a second front end is a second thing to
  keep in step with the model.

  Its 115 tests went too, and no coverage did: they drove a mocked socket, so
  they passed against a server they could not talk to, and the routes
  `client/tests/api.test.ts` pinned are pinned against a running server by
  `tests/conformance/test_http.py`. Nothing builds `dist/` any more; both
  servers already served the API alone when it is empty.

- The OS.js v3 reference client and the whole `src/` tree: the bootstrap and
  config under `src/client/`, the CLI config under `src/cli/`, and the
  `MonoBlueTheme` package. It built to `dist/osjs.html`, which is now a 404.
- Its build: `webpack.config.js`, the root `package.json` and
  `package-lock.json`, and with them every `@osjs/*` dependency.
- The `osjs` and `lint` make targets. `lint` ran stylelint over the OS.js theme
  CSS and had nothing else to cover; there is no stylelint config any more.
- `DistWatcher` and its polling thread. It pushed `osjs/dist:changed` and
  `osjs/packages:metadata:changed`, both of which had lost their consumer: the
  manifest was written by `osjs-cli package:discover`, and the new client logs
  any frame it does not handle. Vite emits fingerprinted files under
  `dist/assets/` while the watcher only scanned the top level, so it could no
  longer fire at all.
- `WATCH_DIST` and `WATCH_INTERVAL` from `server/config.py`, so the
  `MINOS_WATCH_DIST` environment variable is no longer read.
- `tests/test_theme_package.py` (guards on the MonoBlueTheme package contract)
  and `tests/test_watcher.py`.

### Added

- Submissions and moderation (`chat-concepts.md` 5), in `go/` only. An
  administrator appoints moderators with `channel.appoint` and `channel.dismiss`.
  A channel with moderators takes `channel.submit` from its subscribers, and a
  moderator decides with `submission.approve` or `submission.reject` and may
  publish directly. Approval appends the text under its author's name with the
  next `seq`; submitting and rejecting issue none, so the sequence stays
  contiguous. A rejection is kept, and reported in `sync`, until its author sends
  `submission.acknowledge`. The operations are `docs/wire-contract.md` section 9.

  `moderators` goes on the channel object beside `restrictedTo`, rather than
  behind an operation of its own, so it rides the `room` push the core already
  sends. That extends two shapes core tests pin exactly, `sync` and the room
  push; each test now adds the new fields under `MINOS_CONFORMANCE_SCOPE=full`,
  so a scope that does not match its server fails instead of skipping.
  `make test` now runs `conformance-go`, the only black-box run of section 5. The
  schema is version 3 in `go/`, and the Python server, which stays at 2, refuses
  a database Go has upgraded.

  In `tui/`, the composer submits when a channel has moderators and the caller is
  neither one nor an administrator. New commands: `/channel appoint|dismiss`,
  `/queue`, `/approve`, `/reject`, `/submissions` and `/ack`.

- Two decisions in `chat-concepts.md` 5, both moved out of its open questions. A
  channel may have no moderator, and such a channel accepts no submissions: a
  price feed publishes to an audience and there is nothing to curate, so a
  submission to a moderator-less channel is refused rather than queued. And a
  rejected submission is deleted -- it never held a sequence number, so nothing is
  left for a subscriber to re-request.

  Read-only is not the reason for the first. Every channel is read-only to its
  audience, so that cannot separate a curated one from a broadcast; what separates
  them is whether submissions are taken, and the moderator set is how that is
  said.

  Both lifetime questions this raised are settled too. A rejection is deleted
  once its author acknowledges it, because deleting it when a moderator makes it
  would make "the author always learns the outcome" true only for an author who
  was connected. Dismissing the last moderator rejects the queue.

- Go tests for the outbound queue in `go/internal/socket`: the depth a connection
  absorbs, the hang-up on the frame past it, a send to a closed connection,
  ordering through the writer goroutine, and a fan-out that reaches the reading
  peer while reporting the saturated one as unreached. Unit tests rather than
  conformance ones, because none of it is on the wire -- the depth is a constant,
  and a client that stops reading without closing is not something the suite's
  websocket can express. The send past a full queue runs under a deadline, so a
  send that blocks fails one assertion instead of timing the package out.

- `make demo`, a narrated run of the audience rule against the compiled server:
  three real websockets, its own database, and the refusals and pushes printed
  as they happen. It launches and drives the server through
  `tests/conformance/harness.py` and `wire.py` rather than a second copy of the
  protocol, so it cannot drift from the contract without the suite noticing
  first. In `docs/dev/demo_audience.py`.

- `channel.create` and `channel.publish`, both administrators only. A channel is
  founded with a name unique among channels and optionally restricted at once
  (`{"op": "channel.create", "title": "Announcements", "groups": ["<id>"]}`),
  and the administrator writes to it as its producer, the way the machine writes
  to `system`. `send` to a channel stays refused: a channel is read-only to its
  audience, and the operation that writes to one is not the operation a
  participant uses in a room.

  A new channel has no subscribers, so nothing is pushed to it; the server
  announces it on `system` instead, which is how anyone learns there is
  something to subscribe to. Founding is not subscribing -- an admin who wants
  to read what they publish subscribes like anybody else -- and the terminal
  client's composer publishes when the selected space is a channel, leaving the
  authority check to the server.

  This is the core's own answer to who may publish, not section 5's. Moderators
  widen the set of publishers; they do not define it.

- A channel's audience rule, the last unimplemented part of the core model
  (`chat-concepts.md` 2.4). `channel_audience` stores the groups a channel
  admits, `subscribe` refuses anyone outside them with `That channel is
  restricted`, and an administrator sets the rule with two new operations:

      {"op": "channel.admit",  "channel": "system", "group": "<id>"}
      {"op": "channel.revoke", "channel": "system", "group": "<id>"}

  A channel with no groups is open, so revoking the last one reopens it rather
  than closing it to everybody: open is the absence of a rule, and there is no
  way to write "nobody". Room and channel objects gained `restrictedTo`, empty
  on every room and on an open channel. `/channel admit|revoke <id> <group>`
  reaches it from the terminal client.

  Eligibility is re-read on every delivery rather than fixed when the
  subscription was stored. Someone removed from the last group that admitted
  them keeps their subscription and leaves the audience: they stop receiving the
  channel, it leaves their `sync`, `history` on it is refused, and they are sent
  a `roomGone`. Re-admitting the group restores all of it without their acting
  again. The alternative was to delete the subscription, which is what a room's
  group grant does to access -- rejected because a subscription is the
  subscriber's own act, and the server would be destroying a choice it could not
  give back. The cost is a group resolution per fan-out.

- A schema version marker. Both servers stamp `PRAGMA user_version` with
  `SCHEMA_VERSION` / `SchemaVersion`, both check it when they open the timeline
  database, and one carrying a different number -- or none, which is every
  database written before this change -- is refused by name instead of read.
  Without it the Go server opened a database an older Python server had left
  and failed later on the first query naming a column that was not there
  (`no such column: empty_since`); two implementations now write this file, so
  neither could tell a database it understands from one it does not.

  A pragma rather than a version table: it sits in the header of every SQLite
  file, so an empty file and an unmarked one are told apart without creating
  anything to ask. The marker covers the tables both servers read; `presence`
  and `occupants` belong to the Python server alone. There is no upgrade path
  yet -- the refusal says to move the file aside, and migrations attach where
  the version is compared.

- An upgrade path between versions. `MIGRATIONS` / `migrations` map each version
  to the statements that reach it from the one before, and both servers run
  every step between the version on disk and their own inside the transaction
  that stamps it -- so a failed upgrade leaves the version it started at. A
  version with no entry is refused rather than stamped over, which is what stops
  a change that cannot be made in place from being treated as if it could.

  Version 2 is the audience rule, whose step is empty: the table is new, and
  `CREATE TABLE IF NOT EXISTS` in the schema covers it. A version 1 database
  written by either server therefore opens in either server with its rows
  intact, rather than being refused.

- `go/`, the server. One process, a goroutine per connection, an in-process
  fan-out and SQLite; `make serve-go` runs it and `make conformance-go` holds it
  to the contract. `server/` and `messaging/` become the specification it was
  written from rather than a deployment target, and `tui/` drives either without
  modification.

  The message bus does not survive the port, which is the whole point of having
  made it: `Broker`, the sender and relay threads, the thread-local PUSH
  sockets, `broker.lock`, `WorkerLease`, `sweep_dead_workers`, the
  `worker-*.lock` files, and the `presence` and `occupants` tables are absent
  rather than rewritten. All of them existed because CPython needs several
  worker processes. `rooms.empty_since` stays, because it outlives the
  connections: start-up stamps every transient room not already counting down,
  which is what carries the promise of deletion across a restart.

  Two faults the port found and the specification does not have. A fan-out that
  wrote synchronously let one unresponsive peer stall every other recipient for
  a full write timeout, so each connection now has an outbound queue and a
  client that cannot keep up is disconnected rather than waited for. And a
  websocket whose lifetime is `r.Context()` can be closed the moment it opens,
  because the upgrade ends the request and `net/http` may cancel that context
  under a hijacked connection.
- `docs/wire-contract.md`, and `tests/conformance/` checking it. The suite
  drives a server over HTTP and a websocket and imports nothing from `server/`,
  `messaging/` or `tui/`, so the same 146 tests can be pointed at a
  reimplementation: `MINOS_CONFORMANCE_CMD` launches one,
  `MINOS_CONFORMANCE_URL` addresses one already running, and `make conformance`
  runs them. The rest of `tests/` cannot do this -- it calls
  `app.test_client()` or imports `messaging` -- so nothing outside Python could
  previously be held to the contract at all. `docs/dev/conformance-plan.md`
  records why the suite is shaped as it is.
- `MINOS_WS_PING` and `MINOS_ROOM_SWEEP`, overriding the keepalive interval and
  the transient-room sweep. Both were constants, and both are timings the
  conformance suite has to wait out: a run that observed a keepalive and an
  expiring room at the defaults would take three minutes.

### Fixed

- A client that subscribed to a channel created after it connected received
  nothing published to it. `Messaging.subscribe` never watched the channel's
  topic, so the Python server's process sat in the audience of a channel it was
  not listening to, and only a reconnect repaired it. Unreachable until now,
  because every channel existed before every connection.

- The bus lost a message published just after a subscription. A subscription has
  to reach every publisher before it matches anything, and `open` followed by
  `send` is one round trip -- so the first message in a new room was dropped, and
  a client cannot repair a gap it has no way to know is there. The proxy now
  subscribes to everything, and which topics a process wants is a dict it tests
  when the message lands rather than a filter that has to travel. A subscription
  holds the moment it is asked for; the cost is that every process reads every
  message. `test_a_message_is_stored_before_it_is_published` failed every run
  against `server/`, and the two delivery tests failed intermittently.

- A `Broker` whose bind failed left its context and sockets open, so the process
  hung in `zmq_ctx_term` at exit instead of reporting what went wrong. Under
  pytest that turned one bad fixture into a suite that never returned.

- `MINOS_RUN` no longer sits under pytest's `tmp_path`. A Unix socket path may
  not exceed 103 bytes and `tmp_path` spends most of that on the test's name, so
  on macOS -- where the temporary root is 50 characters before pytest adds
  anything -- every `ipc://` bind in the suite failed. `make test` and
  `make conformance` could not run there at all.

- `tests/conformance/wire.py` provokes a reply when the handshake does not
  arrive promptly. `simple_websocket`'s client blocks on the socket before
  draining what its parser already holds, so a server fast enough to put the
  first frame in the same TCP segment as the 101 response leaves that frame
  stranded until unrelated traffic appears. The Go server is fast enough and
  Werkzeug usually is not, which is why this surfaced only after the port. The
  assertion is unchanged: `osjs/core:connected` must still be the first control
  frame on the connection.

### Changed

- A `presence` push no longer reaches the person it is about. It is a fact about
  a user rather than a connection, and the client it would go back to is the one
  that caused it. It also made a client's own arrival race the connection that
  provoked it, which is what the conformance suite kept catching.

- `make test` runs `go test ./...` after pytest. The store's version check is
  not visible on the wire, so the conformance suite cannot reach it.

- `make install` builds the Python venv with [uv](https://docs.astral.sh/uv/)
  rather than `python3 -m venv` plus pip. The stdlib path fails outright on
  distributions that ship Python without `ensurepip`.
- That `uv venv` now passes `--allow-existing`. The rule fires whenever
  `pyproject.toml` is newer than `.venv/bin/pytest`, and `uv venv` refuses a
  directory that already holds a venv -- so editing dependencies made every
  subsequent `make test` fail until `.venv` was deleted by hand.
- `chat-concepts.md` reconciled against the code. Three of its six open core
  questions were answered by building the core and are now stated in the model:
  a participant may give up a grant naming them but not one inherited from a
  group; presence is global and occupancy is the separate per-room fact; and
  `system` names the machine channel alone, an admin-created room being
  *permanent*. Section 7 described the port to this model as pending work and
  now records it as done -- it still cited `merge`, `require_member` and
  `Timeline._last_seq`, none of which exist. Question 2 gains what the code
  decided without arguing it: `create` ignores a requested retention, so no room
  is both admin-founded and transient.
- Section 5's four open questions are answered. A rejection is reported to its
  author with an optional moderator comment; a moderator may not edit a
  submission, which makes attribution a fact rather than a rule; the chat admin
  appoints moderators. The fourth changes the core rather than section 5 and
  moved to 2.4: a channel is open or restricted to named groups, so channels are
  no longer the model's one unconditionally open object. Subscription stays
  distinct from invitation -- a room decides who, a restricted channel decides
  which group, and neither admits anyone who did not choose to be there.
  Unimplemented; recorded in `TODO.md`.
- The websocket broadcast tests use `osjs/vfs:watch:change` as their sample
  frame. They exercise `Registry.broadcast`, and the name they used before is
  no longer sent by anything.

### Unchanged

The server still speaks the OS.js contract, and none of it moved: the
`@osjs/server` route shapes, the `osjs/*` websocket message names, the read-only
`osjs:` mountpoint over `dist/`, and settings at `home:/.osjs/settings.json`.
`Session.patchDesktop` still merges rather than overwrites, because homes
created under the old client carry `osjs/*` keys.
