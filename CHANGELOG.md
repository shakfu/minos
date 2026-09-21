# Changelog

Notable changes to minos. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Nothing is released yet, so everything so far sits under Unreleased.

## [Unreleased]

Both web front ends and the Python server are gone. `go/` holds the server and the terminal client, and the wire format between them is still OS.js's without any OS.js code.

### Removed

- The Python tree: `server/`, `messaging/`, `tui/`, the pytest suite, `pyproject.toml`, the uv venv, and the `install`, `serve-go` and `conformance-go` make targets. A second implementation of the core cost about 55% more code per feature, and it described behaviour `go/` already had. Three parts were ported rather than dropped: the conformance suite, the only black-box test of the server; the terminal client, its only client; and the audience demo.

- `client/`, the web desktop, and the last JavaScript in the tree with it: Vite, Vitest, TypeScript, `client/node_modules`, and the `client` and `dev` make targets. It spoke the retired conversation model -- rooms as sets of people, `merge`, membership edited by dragging -- so it had not connected to this server since the model changed. Removed rather than ported, because `tui/` already reaches every operation and a second front end is a second thing to keep in step with the model.

  Its 115 tests went too, and no coverage did: they drove a mocked socket, so they passed against a server they could not talk to, and the routes `client/tests/api.test.ts` pinned are pinned against a running server by `tests/conformance/test_http.py`. Nothing builds `dist/` any more; both servers already served the API alone when it is empty.

- The OS.js v3 reference client and the whole `src/` tree: the bootstrap and config under `src/client/`, the CLI config under `src/cli/`, and the `MonoBlueTheme` package. It built to `dist/osjs.html`, which is now a 404.

- Its build: `webpack.config.js`, the root `package.json` and `package-lock.json`, and with them every `@osjs/*` dependency.

- The `osjs` and `lint` make targets. `lint` ran stylelint over the OS.js theme CSS and had nothing else to cover; there is no stylelint config any more.

- `DistWatcher` and its polling thread. It pushed `osjs/dist:changed` and `osjs/packages:metadata:changed`, both of which had lost their consumer: the manifest was written by `osjs-cli package:discover`, and the new client logs any frame it does not handle. Vite emits fingerprinted files under `dist/assets/` while the watcher only scanned the top level, so it could no longer fire at all.

- `WATCH_DIST` and `WATCH_INTERVAL` from `server/config.py`, so the `MINOS_WATCH_DIST` environment variable is no longer read.

- `tests/test_theme_package.py` (guards on the MonoBlueTheme package contract) and `tests/test_watcher.py`.

### Fixed

- An `opened` push for a channel item that had already been archived left a mark behind for a message no longer in the log, and nothing cleared it: `dropThrough` only removes marks at or below what it drops, and this one was already below. Opening an item is answered with a push to the opener as well as a reply, so the two can arrive in either order. The client now records the highest sequence archived out of each space and ignores a mark at or below it.

- `minosb -socket` deleted whatever was at the path, not only an earlier socket: a mistyped `-socket ~/notes.txt` removed the file, and an empty directory there went too. A path holding anything but a socket is now refused.

- A `-socket` path past the unix socket limit, 103 bytes on macOS and 107 on Linux, failed as `bind: invalid argument`. It is now refused with the limit named. The macOS `$TMPDIR` alone is 49 bytes, so a path nested under it can pass the limit.

  Both are checked before `minosb` logs in, so a bad path no longer logs in and joins the room first.

### Added

- `minosb`, one run's broker on the host, and `minosa`, the shim its container carries. The broker holds the session, the room's cursor and the run's unix socket; the container reaches six operations -- `messages`, `say`, `submit`, `await`, `progress`, `status` -- and no seventh, so it cannot reach a file write or a settings replacement at any credential. On the host rather than in the container: a client inside would speak the whole wire, which answers a 100 MiB `writefile` on the same connection as chat, and would have to be cut back by a capability system that does not exist yet. See [docs/dev/recommended-architecture.md](docs/dev/recommended-architecture.md) and [docs/dev/implementation-plan.md](docs/dev/implementation-plan.md).

  A run holds a room for the conversation and, optionally, a channel for decisions, because a submission is a channel operation on this wire and a room has no queue. A payload travels in the message body under a `minos-payload` fence, and no subcommand takes JSON on a command line. Delivery that cannot be repaired ends the run: history is capped on the tail, so `messages` reports the gap and exits 3, `progress` refuses to move, and `minosb` reports `torn`. The client gained the two handlers this needs, a shortfall event and a submission event -- approval deletes a submission rather than storing its state, so a caller waiting on one cannot poll for it.

- `minosb -- <command>` runs the agent and owns its pipes. Each turn's final message is posted to the room under the worker's name, so a run that says nothing is still on the record; a turn too long for the wire is cut rather than dropped. What the room says is pushed onto the agent's stdin, the one lane that reaches a model that never calls the shim, and a message arriving mid-turn is held until the turn ends and reported as `held` first. Held rather than interrupted, because no interrupt frame for Claude Code's stream-json has been verified; `TODO.md` carries it. One adapter per agent CLI holds the stream format, and `claude` is the one implemented.

  Tested against a real server with a scripted agent on pipes. Not yet run with the socket bind-mounted into a container.

- Tags on a project, a scope on a room, and open or closed on both. `project.create` takes `tags` and `project.tag`/`project.untag` change them; a tag is folded to lower case and is one word, because a tag exists to be filtered on and `Go` and `go` filtering apart would divide the projects rather than classify them. `project.file` takes `scope` and `task`: a room filed under a project is about the project as a whole or about one task, and the task label is opaque -- the server stores and returns it and never parses it, because which task it names is `pma`'s business. `room.close` and `room.reopen` say whether the work in a place is done; a closed place is kept, readable and writable, and is simply no longer one of the places work is happening in. Schema version 7; wire contract sections 12 and 13; 9 more conformance tests.

  Closed rather than deleted or archived because the three answer different questions: archival is about how long messages live, deletion is about a room ceasing to exist, and this is about whether anybody should still be looking. A transient room is refused both, since its grace period already decides when it ends and closing would name a second, contradictory end.

- An `OVERVIEW` tab, first in the header bar and where a session opens. It ranks the five projects whose open places had the most said in them in the last seven days, and under the table says what there is and what is waiting on you. Two facts, not one: the `ACTIVE` count is places somebody left open, and the `SAID` rank is what was said lately. A recency-only reading would make a task room go quiet and read as finished while its worker agent is mid-run; an open/closed-only reading would rank a long finished thread above live work.

  `PROJECTS` now opens a project's own page -- its tags, what it holds, then its places -- rather than a bare list of rooms. A place's row carries its scope, its task and what was said in it. `a` shows the closed places. The header bar carries the name, as `gwiki`'s does, so the status line no longer repeats it.

- Projects: a container of rooms and channels, the object design.md section 7.2 calls a space. It holds no messages and decides no access, so it has no audience and every caller sees every project; a room names the one it is filed under, or none. `project.create`, `project.file` and `project.dissolve` are administrator-only, and `sync` carries `projects`. Names are unique without case, as a permanent room's title is, because a project is named where a room is filed. A transient room is refused: it is discarded when everyone leaves, so filing it would record a place about to stop existing. Dissolving keeps the rooms, filed under none. Schema version 6; wire contract section 12; 8 conformance tests.

  Named `project` rather than `space`, because `space` is already the terminal client's word for a room-or-channel in 124 places, and design.md's own example -- `projects > cynn > task/31` -- reads the container as a project. Archival inheritance, which section 7.2 also proposes, is not built: a room still carries its own period.

- The terminal client is a tab bar over three tables, after `gwiki`'s. `PROJECTS` lists the containers with what each holds; Enter on one lists its places, and Enter on a place goes in. `ROOMS` is the same table unscoped, for a place with no project or whose project the user does not know. `PEOPLE` is the roster. Tab moves between tabs, Up and Down move the cursor, Enter goes one level in and Esc one level out. `/project new|file|rm` and `/projects` reach the same from the composer.

  The sidebar is gone, and with it the 24 columns it held and the Tab cycle over every space and then every person. That cycle could not express two levels, and a flat list of every room was what a project exists to break up. A room is still only ever entered by Enter on its row, so "highlighting is a preview" survives the change.

- A room's first entry is recorded as a visit, on the server, so an invitation is open until then and is the same fact on every device. `sync` carries `visited`, and the schema is version 5. Upgrading fills visits from read cursors, because a user who has read a room has been in it; a room entered and never read is missed, and counts as an open invitation until the next entry. Nothing reopens an invitation, including being removed and invited again. The terminal client counts open invitations apart from unread messages, which it counts only in visited rooms, and marks a room not yet entered `(invited)`.

- A user occupies at most one room. `enter` first releases the user's place in any other room, on every connection, and tells the connection that held it with a new `exited` push. Two devices in the same room are kept. The rule is the server's rather than the client's, so a second device cannot put the same person in two meetings.

  In the terminal client, Tab highlights a room and Enter goes in. Inside, the screen is that room: no sidebar, Tab ignored, and a status line that counts open invitations and unread messages apart. `/exit` steps out and keeps your place; `/leave` still gives it up. An invitation is offered rather than entered, and one that arrives while you are in a room waits until you step out. The zoom also ends when the room closes, or when you enter a room on another device. Inside a room, `/open`, `/meet`, `/create` and `/subscribe` ask before taking you out of it. A room you only highlight is a preview, and nothing in it is marked read.

- The terminal client takes a channel's name, its id, or the start of its id wherever it asked for the full id: `/subscribe` and `/channel
  admit|revoke|appoint|dismiss`. It learns names of channels it is not
  subscribed to from their announcement on `system`, the one place the model lets anyone discover a channel. The argument is one word, with `_` for a space in a name, because a multi-word argument followed by a user or group would have to be split by guessing. An ambiguous prefix is refused.

- The channel feed and archival, in the server. Channel messages and submissions carry a `subject`, taken from the body's first line when none is given. `channel.open` marks one item opened per subscriber, and `read` on a channel is now refused. `archive.set`, `archive.read` and `archive.search` give each permanent room and channel an age limit and a searchable archive. The sweep moves aged messages out from the oldest, without renumbering. The schema is version 4: existing channel messages and submissions get their first line as a subject, and version 3 now creates the submissions table itself, because version 4 alters it before the schema runs. 22 conformance tests cover it.

  In the terminal client, every channel but `system` lists its items by subject, pending first and then opened, each newest first. Up and Down move a cursor over them. Enter opens the item under it and shows its body, and the
  cursor follows it to the opened list. `subject | body` in the composer sets
  the headline; without the bar the server takes the first line. Channels no longer show an unread count, and an `archived` push drops what it names.
  `/archive [<period>|never] [searchable|private]` shows or sets a space's
  archival, with a period such as `30d` or `12h`. `/archived [<after>]` pages through the archive, and `/search <text>` searches it. Their results replace the conversation until Esc, because the notice area holds six lines and a page holds two hundred.

- `go/conformance/`, the conformance suite in Go: 164 tests, one per Python test, 176 cases with subtests. `MINOS_CONFORMANCE_SCOPE` is gone, because one server claims the whole contract. The handshake workaround for `simple_websocket` went with that library. With `MINOS_CONFORMANCE_CMD` unset, the suite builds `cmd/minosd` itself.

- `go/cmd/minos`, the terminal client, on tcell; `make tui` runs it with `-server`, `-user` and `-password`. It behaves like `tui/`, with two exceptions. Control characters show as `?` rather than `^[`. Wrapping collapses runs of spaces.

- Rooms can be raised without knowing a command. Tab walks on from the channels to the people in the sidebar, and Enter on one raises a room with them, sending anything typed first as its first message. A user with no rooms is told so at start-up, and the hint line in a room names `/invite`. Before this, `system` was always selected, so the one hint that named `/open` never showed.

- Submissions and moderation (`chat-concepts.md` 5), in `go/` only. An administrator appoints moderators with `channel.appoint` and `channel.dismiss`. A channel with moderators takes `channel.submit` from its subscribers, and a moderator decides with `submission.approve` or `submission.reject` and may publish directly. Approval appends the text under its author's name with the next `seq`; submitting and rejecting issue none, so the sequence stays contiguous. A rejection is kept, and reported in `sync`, until its author sends `submission.acknowledge`. The operations are `docs/wire-contract.md` section 9.

  `moderators` goes on the channel object beside `restrictedTo`, rather than behind an operation of its own, so it rides the `room` push the core already sends. That extends two shapes core tests pin exactly, `sync` and the room push; each test now adds the new fields under `MINOS_CONFORMANCE_SCOPE=full`, so a scope that does not match its server fails instead of skipping. `make test` now runs `conformance-go`, the only black-box run of section 5. The schema is version 3 in `go/`, and the Python server, which stays at 2, refuses a database Go has upgraded.

  In `tui/`, the composer submits when a channel has moderators and the caller is
  neither one nor an administrator. New commands: `/channel appoint|dismiss`,
  `/queue`, `/approve`, `/reject`, `/submissions` and `/ack`.

- Two decisions in `chat-concepts.md` 5, both moved out of its open questions. A channel may have no moderator, and such a channel accepts no submissions: a price feed publishes to an audience and there is nothing to curate, so a submission to a moderator-less channel is refused rather than queued. And a rejected submission is deleted -- it never held a sequence number, so nothing is left for a subscriber to re-request.

  Read-only is not the reason for the first. Every channel is read-only to its audience, so that cannot separate a curated one from a broadcast; what separates them is whether submissions are taken, and the moderator set is how that is said.

  Both lifetime questions this raised are settled too. A rejection is deleted once its author acknowledges it, because deleting it when a moderator makes it would make "the author always learns the outcome" true only for an author who was connected. Dismissing the last moderator rejects the queue.

- Go tests for the outbound queue in `go/internal/socket`: the depth a connection absorbs, the hang-up on the frame past it, a send to a closed connection, ordering through the writer goroutine, and a fan-out that reaches the reading peer while reporting the saturated one as unreached. Unit tests rather than conformance ones, because none of it is on the wire -- the depth is a constant, and a client that stops reading without closing is not something the suite's websocket can express. The send past a full queue runs under a deadline, so a send that blocks fails one assertion instead of timing the package out.

- `make demo`, a narrated run of the audience rule against the compiled server: three real websockets, its own database, and the refusals and pushes printed as they happen. It launches and drives the server through `tests/conformance/harness.py` and `wire.py` rather than a second copy of the protocol, so it cannot drift from the contract without the suite noticing first. In `docs/dev/demo_audience.py`.

- `channel.create` and `channel.publish`, both administrators only. A channel is founded with a name unique among channels and optionally restricted at once (`{"op": "channel.create", "title": "Announcements", "groups": ["<id>"]}`), and the administrator writes to it as its producer, the way the machine writes to `system`. `send` to a channel stays refused: a channel is read-only to its audience, and the operation that writes to one is not the operation a participant uses in a room.

  A new channel has no subscribers, so nothing is pushed to it; the server announces it on `system` instead, which is how anyone learns there is something to subscribe to. Founding is not subscribing -- an admin who wants to read what they publish subscribes like anybody else -- and the terminal client's composer publishes when the selected space is a channel, leaving the authority check to the server.

  This is the core's own answer to who may publish, not section 5's. Moderators widen the set of publishers; they do not define it.

- A channel's audience rule, the last unimplemented part of the core model (`chat-concepts.md` 2.4). `channel_audience` stores the groups a channel admits, `subscribe` refuses anyone outside them with `That channel is restricted`, and an administrator sets the rule with two new operations:

      {"op": "channel.admit",  "channel": "system", "group": "<id>"}
      {"op": "channel.revoke", "channel": "system", "group": "<id>"}

  A channel with no groups is open, so revoking the last one reopens it rather than closing it to everybody: open is the absence of a rule, and there is no way to write "nobody". Room and channel objects gained `restrictedTo`, empty
  on every room and on an open channel. `/channel admit|revoke <id> <group>`
  reaches it from the terminal client.

  Eligibility is re-read on every delivery rather than fixed when the subscription was stored. Someone removed from the last group that admitted them keeps their subscription and leaves the audience: they stop receiving the channel, it leaves their `sync`, `history` on it is refused, and they are sent a `roomGone`. Re-admitting the group restores all of it without their acting again. The alternative was to delete the subscription, which is what a room's group grant does to access -- rejected because a subscription is the subscriber's own act, and the server would be destroying a choice it could not give back. The cost is a group resolution per fan-out.

- A schema version marker. Both servers stamp `PRAGMA user_version` with `SCHEMA_VERSION` / `SchemaVersion`, both check it when they open the timeline database, and one carrying a different number -- or none, which is every database written before this change -- is refused by name instead of read. Without it the Go server opened a database an older Python server had left and failed later on the first query naming a column that was not there (`no such column: empty_since`); two implementations now write this file, so neither could tell a database it understands from one it does not.

  A pragma rather than a version table: it sits in the header of every SQLite file, so an empty file and an unmarked one are told apart without creating anything to ask. The marker covers the tables both servers read; `presence` and `occupants` belong to the Python server alone. There is no upgrade path yet -- the refusal says to move the file aside, and migrations attach where the version is compared.

- An upgrade path between versions. `MIGRATIONS` / `migrations` map each version to the statements that reach it from the one before, and both servers run every step between the version on disk and their own inside the transaction that stamps it -- so a failed upgrade leaves the version it started at. A version with no entry is refused rather than stamped over, which is what stops a change that cannot be made in place from being treated as if it could.

  Version 2 is the audience rule, whose step is empty: the table is new, and `CREATE TABLE IF NOT EXISTS` in the schema covers it. A version 1 database written by either server therefore opens in either server with its rows intact, rather than being refused.

- `go/`, the server. One process, a goroutine per connection, an in-process fan-out and SQLite; `make serve-go` runs it and `make conformance-go` holds it to the contract. `server/` and `messaging/` become the specification it was written from rather than a deployment target, and `tui/` drives either without modification.

  The message bus does not survive the port, which is the whole point of having made it: `Broker`, the sender and relay threads, the thread-local PUSH sockets, `broker.lock`, `WorkerLease`, `sweep_dead_workers`, the `worker-*.lock` files, and the `presence` and `occupants` tables are absent rather than rewritten. All of them existed because CPython needs several worker processes. `rooms.empty_since` stays, because it outlives the connections: start-up stamps every transient room not already counting down, which is what carries the promise of deletion across a restart.

  Two faults the port found and the specification does not have. A fan-out that wrote synchronously let one unresponsive peer stall every other recipient for a full write timeout, so each connection now has an outbound queue and a client that cannot keep up is disconnected rather than waited for. And a websocket whose lifetime is `r.Context()` can be closed the moment it opens, because the upgrade ends the request and `net/http` may cancel that context under a hijacked connection.

- `docs/wire-contract.md`, and `tests/conformance/` checking it. The suite drives a server over HTTP and a websocket and imports nothing from `server/`, `messaging/` or `tui/`, so the same 146 tests can be pointed at a reimplementation: `MINOS_CONFORMANCE_CMD` launches one, `MINOS_CONFORMANCE_URL` addresses one already running, and `make conformance` runs them. The rest of `tests/` cannot do this -- it calls `app.test_client()` or imports `messaging` -- so nothing outside Python could previously be held to the contract at all. `docs/dev/conformance-plan.md` records why the suite is shaped as it is.

- `MINOS_WS_PING` and `MINOS_ROOM_SWEEP`, overriding the keepalive interval and the transient-room sweep. Both were constants, and both are timings the conformance suite has to wait out: a run that observed a keepalive and an expiring room at the defaults would take three minutes.

### Fixed

- `copy` onto the same file emptied it, because the destination was truncated before the source was read. `copy` of a directory into its own subtree nested about 2,000 levels before failing on path length. Both now answer 400. The check compares files rather than names, so a hard link to the source is refused too.

- The VFS followed a symlink out of its mountpoint: the prefix check was lexical, and `os.Open` resolves links. Every operation now runs through an `os.Root`, over resolving paths with `EvalSymlinks` first, which would leave a gap between the check and the use. `unlink` and `rename` of a mountpoint's root are refused with 403; `unlink home:/` used to delete the home directory.

- Closing one of a user's two connections announced them offline to everyone. Presence is now announced on the first connection and after the last.

- `uninvite` left the room in the removed user's client and kept their place in it, so a removed user could hold a transient room open. Losing access by `leave`, `uninvite` or `group.unassign` now releases the user's places and sends `roomGone`.

- A transient room that nobody entered was never deleted until the server restarted: the grace period counts from emptying, and such a room never empties. It is now deleted 15 minutes after it was raised, set by `MINOS_ROOM_UNENTERED`. A separate period rather than the grace period, because two minutes is too short for invitees to arrive.

- A transient room could be deleted with someone in it. `Exit` decided the room was empty, released its lock, then stamped `empty_since`; an entry between the two left an occupied room counting down. The sweep had the same gap between listing expired rooms and deleting them. Both now happen under the occupancy lock, and the delete checks expiry again.

- A session used at least every 12 hours never expired and never lost its admin role, and `/logout` only cleared the caller's cookie. A session now ends 7 days after login. Logout revokes it on the server and closes its sockets. Revocations are kept in memory, so a restart forgets them; the 7-day limit still applies.

- The websocket skipped its `Origin` check, on the mistaken premise that an upgrade carries none. Browsers always send one, and `SameSite=Lax` does not separate ports on one host. An upgrade naming another origin is now refused with 403, and a JSON POST without `Content-Type: application/json` with 415.

- The terminal client never reconnected, though the server hangs up on a client 256 frames behind on the understanding that it would. It now logs in again with backoff, syncs, and enters again the room it was in.

- The terminal client marked messages read with a blocking request from inside `draw`, and kept its local cursor when the server refused. The request no longer blocks the screen, and a refused read puts the cursor back.

- `system` accepted `channel.publish` and `channel.appoint` from an administrator, and an `unsubscribe` from it was undone at every start. All three are refused.

- A file named `x opened the channel Ops (id)` made the terminal client resolve `Ops` to an id of the uploader's choosing. Only a `system` event with a one-word founder counts now, and a control character in an announced path is replaced by `?`.

- The terminal client drew one cell per character, so CJK text and most emoji overlapped. It measures cells, and draws bidi controls as `?`.

- Subscribing to a channel showed none of what it already held. The client fetches history only at sync or when an arriving message reveals a gap, and nothing is pushed to someone outside the audience, so a new subscriber saw the channel's past only after its next post. It now backfills on subscribing, and on any `room` push whose `lastSeq` is ahead. Found by driving the real client in a pseudo-terminal; every earlier test subscribed before publishing.

- `/help` showed only its last six entries, the key hints, because it wrote a notice per entry and the notice area keeps six lines. It now fills the pane like archive results, and a test fails if a command is missing from it.

- A client that subscribed to a channel created after it connected received nothing published to it. `Messaging.subscribe` never watched the channel's topic, so the Python server's process sat in the audience of a channel it was not listening to, and only a reconnect repaired it. Unreachable until now, because every channel existed before every connection.

- The bus lost a message published just after a subscription. A subscription has to reach every publisher before it matches anything, and `open` followed by `send` is one round trip -- so the first message in a new room was dropped, and a client cannot repair a gap it has no way to know is there. The proxy now subscribes to everything, and which topics a process wants is a dict it tests when the message lands rather than a filter that has to travel. A subscription holds the moment it is asked for; the cost is that every process reads every message. `test_a_message_is_stored_before_it_is_published` failed every run against `server/`, and the two delivery tests failed intermittently.

- A `Broker` whose bind failed left its context and sockets open, so the process hung in `zmq_ctx_term` at exit instead of reporting what went wrong. Under pytest that turned one bad fixture into a suite that never returned.

- `MINOS_RUN` no longer sits under pytest's `tmp_path`. A Unix socket path may not exceed 103 bytes and `tmp_path` spends most of that on the test's name, so on macOS -- where the temporary root is 50 characters before pytest adds anything -- every `ipc://` bind in the suite failed. `make test` and `make conformance` could not run there at all.

- `tests/conformance/wire.py` provokes a reply when the handshake does not arrive promptly. `simple_websocket`'s client blocks on the socket before draining what its parser already holds, so a server fast enough to put the first frame in the same TCP segment as the 101 response leaves that frame stranded until unrelated traffic appears. The Go server is fast enough and Werkzeug usually is not, which is why this surfaced only after the port. The assertion is unchanged: `osjs/core:connected` must still be the first control frame on the connection.

### Changed

- Size limits: a request body is at most 1 MiB and an upload 100 MiB, answered 413 above that. A socket frame is at most 1 MiB, closed with 1009. A message body or rejection comment is at most 64 KiB, and a title or group name 200 characters, each refused with a readable error. Before, only the library's 32 KiB frame limit applied, and it closed the socket.

- A room whose last grant is withdrawn is deleted if ad-hoc. For a permanent room, withdrawing the last grant is refused. A room with no grants was reachable by nobody and was never deleted, and a permanent one still held its name.

- Room lists read each room's latest message by key instead of scanning all of its messages on every `sync`.

- `/logout` without a session answers 403, as the contract says of every route but `/`, `/ping` and `/login`. `/ping` refreshes a session it is given. The socket is served at `/` only. A missing room id is quoted as `null` rather than `None`. The server warns at start when `MINOS_SECRET` is unset.

- `chat-concepts.md`, now at `docs/dev/`, specifies the channel feed and archival by age. Every channel message has a subject and a body, and each subscriber opens items individually. The admin sets an archival period per channel and per admin-created room, and whether its archive can be searched. `docs/wire-contract.md` sections 10 and 11 give the operations, fields and pushes.

- A `presence` push no longer reaches the person it is about. It is a fact about a user rather than a connection, and the client it would go back to is the one that caused it. It also made a client's own arrival race the connection that provoked it, which is what the conformance suite kept catching.

- `make test` runs `go test ./...` after pytest. The store's version check is not visible on the wire, so the conformance suite cannot reach it.

- `make install` builds the Python venv with [uv](https://docs.astral.sh/uv/) rather than `python3 -m venv` plus pip. The stdlib path fails outright on distributions that ship Python without `ensurepip`.

- That `uv venv` now passes `--allow-existing`. The rule fires whenever `pyproject.toml` is newer than `.venv/bin/pytest`, and `uv venv` refuses a directory that already holds a venv -- so editing dependencies made every subsequent `make test` fail until `.venv` was deleted by hand.

- `chat-concepts.md` reconciled against the code. Three of its six open core questions were answered by building the core and are now stated in the model: a participant may give up a grant naming them but not one inherited from a group; presence is global and occupancy is the separate per-room fact; and `system` names the machine channel alone, an admin-created room being *permanent*. Section 7 described the port to this model as pending work and now records it as done -- it still cited `merge`, `require_member` and `Timeline._last_seq`, none of which exist. Question 2 gains what the code decided without arguing it: `create` ignores a requested retention, so no room is both admin-founded and transient.

- Section 5's four open questions are answered. A rejection is reported to its author with an optional moderator comment; a moderator may not edit a submission, which makes attribution a fact rather than a rule; the chat admin appoints moderators. The fourth changes the core rather than section 5 and moved to 2.4: a channel is open or restricted to named groups, so channels are no longer the model's one unconditionally open object. Subscription stays distinct from invitation -- a room decides who, a restricted channel decides which group, and neither admits anyone who did not choose to be there. Unimplemented; recorded in `TODO.md`.

- The websocket broadcast tests use `osjs/vfs:watch:change` as their sample frame. They exercise `Registry.broadcast`, and the name they used before is no longer sent by anything.

### Unchanged

The server still speaks the OS.js contract, and none of it moved: the `@osjs/server` route shapes, the `osjs/*` websocket message names, the read-only `osjs:` mountpoint over `dist/`, and settings at `home:/.osjs/settings.json`. `Session.patchDesktop` still merges rather than overwrites, because homes created under the old client carry `osjs/*` keys.
