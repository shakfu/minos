# The conformance suite

The tests that decide whether a server speaks the contract in [wire-contract.md](../wire-contract.md).

Status: built, first in Python under `tests/conformance/`, then ported to Go as `go/conformance/` when the Python server was removed. The Python version held two servers to one contract; the Go version holds one.

## Shape

A black-box suite. It launches a server as a subprocess, talks HTTP and WebSocket to it, and asserts on frames. It imports no package under `minos/internal` or `minos/cmd` -- enforced by `isolation_test.go`, which parses the suite's own source.

```
go/conformance/
  wire.go                     the client: HTTP session, socket, push queue
  harness.go                  launching a server under test, and finding a free port
  main_test.go                the shared server, and the per-test helpers
  isolation_test.go           the suite imports no implementation package
  http_test.go                routes, auth, settings, VFS, security headers
  socket_test.go              upgrade, handshake, framing, forgery, keepalive
  chat_ops_test.go            every operation: replies and refusals
  delivery_test.go            who receives a push, and who does not
  sequence_test.go            the delivery contract: gaps, repair, ordering
  transient_test.go           occupancy and the grace period
  channel_audience_test.go    the audience rule
  submissions_test.go         section 9
  feed_test.go                section 10: subjects and opening
  archive_test.go             section 11: archival and search
```

`wire.go` and `harness.go` are ordinary files rather than test files, so `go/cmd/demo` can drive a server with the same client.

## Selecting the server

Two environment variables, and nothing else:

| Variable | Meaning |
|-|-|
| `MINOS_CONFORMANCE_CMD` | argv to launch a server. Unset, the suite builds `cmd/minosd` |
| `MINOS_CONFORMANCE_URL` | test a server already running; nothing is launched |

The launcher passes the server its configuration through the environment (`MINOS_HOST`, `MINOS_PORT`, `MINOS_RUN`, `MINOS_VFS`, `MINOS_DIST`, `MINOS_ROOM_GRACE`, `MINOS_ROOM_SWEEP`, `MINOS_WS_PING`). That set is part of the contract: a server that ignores `MINOS_PORT` cannot be tested at all, and one that ignores `MINOS_ROOM_GRACE` cannot have its transient rooms tested in under two minutes.

## Isolation

One server for the run, not one per test. A black-box server holds durable state, so per-test cleanup would mean per-test restart, and a restart costs more than the whole suite.

The consequence is a rule: **no test may assume it is alone.** Rooms are addressed by the id the server returns. Permanent room names and group names carry a per-test suffix, because permanent names are unique server-wide. No test counts rooms, groups or users globally.

Tests about start-up or transient deletion boot a second server on its own run directory, the latter with a short `MINOS_ROOM_GRACE`. Under `MINOS_CONFORMANCE_URL` they skip.

## Asynchrony

Pushes arrive after the reply that caused them. There are no sleeps: every socket runs a reader goroutine into a queue, and assertions drain until a frame matches or a deadline passes. A test that wants to prove a push *did not* arrive states what it waited for instead, and waits for a second frame that must arrive after it.

## What is deliberately not tested

Internals a server is free to change:

- the SQLite schema, `high_seq`, and the shape of `.run/`

- connection registries, outbound queues and how a fan-out finds its recipients

`go test` in the internal packages covers those.
