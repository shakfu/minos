# The conformance suite

A plan for the tests that decide whether a server -- this one, or a
reimplementation of it -- speaks the contract in
[wire-contract.md](../wire-contract.md).

## Why the existing suite cannot do this

`tests/` has 152 tests and only `test_socket_integration.py` reaches a server
over a socket. The rest call `app.test_client()` (`tests/conftest.py`), import
`messaging` directly, or drive `sockets.dispatch` in process. None of that can
be pointed at a binary written in another language.

The claim in `TODO.md` that "two clients speak it, and both become conformance
tests" was true while the web client worked. It stopped working and has since
been removed, so the contract has one speaker, and a Go server would be verified
by whether the terminal client happened to look right.

## Shape

A black-box suite. It launches a server as a subprocess, talks HTTP and
WebSocket to it, and asserts on frames. It imports nothing from `server/`,
`messaging/` or `tui/` -- enforced by `test_isolation.py`, which reads the
suite's own source.

```
tests/conformance/
  wire.py            the client: HTTP session, socket, push queue
  harness.py         launching a server under test, and finding a free port
  conftest.py        fixtures: server, sessions, unique names
  test_isolation.py  the suite imports no implementation module
  test_http.py       routes, auth, settings, VFS, security headers
  test_socket.py     upgrade, handshake, framing, forgery, keepalive
  test_chat_ops.py   every operation: replies and refusals
  test_delivery.py   who receives a push, and who does not
  test_sequence.py   the delivery contract: gaps, repair, ordering
  test_transient.py  occupancy and the grace period
  test_submissions.py  section 5, beyond the core
```

## Selecting the implementation

Three environment variables, and nothing else:

| Variable | Meaning |
|-|-|
| `MINOS_CONFORMANCE_CMD` | argv to launch a server. Default `python -m server.app` |
| `MINOS_CONFORMANCE_URL` | test a server already running; nothing is launched |
| `MINOS_CONFORMANCE_SCOPE` | `core` (default) or `full`: how much of the contract the server claims |

The launcher passes the server its configuration through the environment
(`MINOS_HOST`, `MINOS_PORT`, `MINOS_RUN`, `MINOS_VFS`, `MINOS_DIST`,
`MINOS_ROOM_GRACE`, `MINOS_WS_PING`). That set is part of the contract: a Go
server that ignores `MINOS_PORT` cannot be tested at all, and one that ignores
`MINOS_ROOM_GRACE` cannot have its transient rooms tested in under two minutes.

Running the Go server is then `make conformance-go`, which also declares
`MINOS_CONFORMANCE_SCOPE=full`.

## Scope

`server/` is frozen at the core, and sections beyond it exist in `go/` alone.
Their tests carry `pytest.mark.beyond_core` and are skipped unless
`MINOS_CONFORMANCE_SCOPE=full`. The scope is declared rather than detected, so a
server that loses an operation fails instead of being skipped.

Two core tests pin an exact key set, `sync` and the room push, and each adds the
fields beyond the core under `full`. A scope that does not match its server
therefore fails both ways: Python under `full` fails the section 5 tests, and Go
under `core` fails the two shape tests.

## Isolation

One server for the session, not one per test. A black-box server holds durable
state, so per-test cleanup would mean per-test restart, and a restart costs
more than the whole suite.

The consequence is a rule: **no test may assume it is alone.** Rooms are
addressed by the id the server returns. Permanent room names and group names
carry a per-test suffix, because permanent names are unique server-wide. No
test counts rooms, groups or users globally.

Two fixtures escape it. `fresh_server` boots a second instance on its own run
directory, for anything about start-up. `graced_server` does the same with a
short `MINOS_ROOM_GRACE`, for transient deletion.

## Asynchrony

Pushes arrive over the bus, after the reply that caused them. There are no
sleeps: every socket runs a reader thread into a queue, and assertions are
`expect(predicate, timeout)` -- drain until a frame matches or the deadline
passes. A test that wants to prove a push *did not* arrive states what it
waited for instead, and waits for a second frame that must arrive after it.

## What is deliberately not tested

Internals a reimplementation is free to discard:

- ZeroMQ, its topics, and the `|` terminator on them
- the SQLite schema, `high_seq`, and the shape of `.run/`
- worker leases, `worker-*.lock`, and the sweep that clears them
- thread counts, connection registries, in-process fan-out

`tests/` keeps covering those for the Python server. The two suites answer
different questions and neither replaces the other.

## Order of work

1. `wire.py`, `harness.py`, `conftest.py` -- nothing asserts yet.
2. `test_http.py` and `test_socket.py`: the frozen OS.js half, which is the
   part with two independent descriptions already.
3. `test_chat_ops.py`: one test per operation, refusals included, since a
   refusal message is what a client shows a user.
4. `test_delivery.py` and `test_sequence.py`: the half that is genuinely hard
   to reimplement, and the reason the sequence number survives the rewrite.
5. `test_transient.py` last: it is the only part that spends real time.

## When this is done

The Go work can start. The suite is what says the port is finished, and it is
worth having whether or not the port ever happens -- it is the first test in
this repository that would catch the Python server breaking its own contract
through a route it does not take internally.
