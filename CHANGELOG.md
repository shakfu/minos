# Changelog

Notable changes to minos. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Nothing is released
yet, so everything so far sits under Unreleased.

## [Unreleased]

The OS.js reference client is gone. `client/` is now the only front end, and
the server keeps the OS.js wire format without carrying any OS.js code.

### Removed

- The OS.js v3 reference client and the whole `src/` tree: the bootstrap and
  config under `src/client/`, the CLI config under `src/cli/`, and the
  `MonoBlueTheme` package. It built to `dist/osjs.html`, which is now a 404.
- Its build: `webpack.config.js`, the root `package.json` and
  `package-lock.json`, and with them every `@osjs/*` dependency. The only
  JavaScript toolchain left is the one under `client/`.
- The `osjs` and `lint` make targets. `lint` ran stylelint over the OS.js theme
  CSS and had nothing else to cover; there is no stylelint config any more.
- `DistWatcher` and its polling thread. It pushed `osjs/dist:changed` and
  `osjs/packages:metadata:changed`, both of which had lost their consumer: the
  manifest was written by `osjs-cli package:discover`, and the new client logs
  any frame it does not handle. Vite emits fingerprinted files under
  `dist/assets/` while the watcher only scanned the top level, so it could no
  longer fire at all. Hot reload during development is Vite's, via `make dev`.
- `WATCH_DIST` and `WATCH_INTERVAL` from `server/config.py`, so the
  `MINOS_WATCH_DIST` environment variable is no longer read.
- `tests/test_theme_package.py` (guards on the MonoBlueTheme package contract)
  and `tests/test_watcher.py`.

### Added

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
- `make dev` opens a browser at the Vite URL, through Vite's own `--open`.
  `BROWSER=none make dev` starts the server without one. `npm run dev` inside
  `client/` is unchanged and still opens nothing.

### Fixed

- `tests/conformance/wire.py` provokes a reply when the handshake does not
  arrive promptly. `simple_websocket`'s client blocks on the socket before
  draining what its parser already holds, so a server fast enough to put the
  first frame in the same TCP segment as the 101 response leaves that frame
  stranded until unrelated traffic appears. The Go server is fast enough and
  Werkzeug usually is not, which is why this surfaced only after the port. The
  assertion is unchanged: `osjs/core:connected` must still be the first control
  frame on the connection.

### Changed

- `make install` builds the Python venv with [uv](https://docs.astral.sh/uv/)
  rather than `python3 -m venv` plus pip. The stdlib path fails outright on
  distributions that ship Python without `ensurepip`.
- That `uv venv` now passes `--allow-existing`. The rule fires whenever
  `pyproject.toml` is newer than `.venv/bin/pytest`, and `uv venv` refuses a
  directory that already holds a venv -- so editing dependencies made every
  subsequent `make test` fail until `.venv` was deleted by hand.
- The Makefile falls back to corepack's npm where the node install has none,
  so `make dev` and `make client` work on a Debian `nodejs` package.
- `client/vite.config.ts` sets `emptyOutDir: true`. It was off only because the
  OS.js build wrote into the same `dist/`; `make client` now clears the
  directory first.
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
