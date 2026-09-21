# Implementation plan: agents in containers

2026-09-21. Derived from [recommended-architecture.md](recommended-architecture.md).

The recommendation states the design. This states the work: what is built, in
which repository, in what order, and how each step is proved. It also rules on
six questions the recommendation leaves open, and proposes answers to the five
it lists in its section 13. Both sets of rulings are for approval; each names
the fact that forced it.

Phase and item numbers match the recommendation's section 12.

## 1. Where the work lands

Three repositories, three languages. Nothing in the design is one repository's
alone.

| Repository | Language | Gains | Items |
|-|-|-|-|
| `minos` | Go | `minosb` the broker, `minosa` the shim, grants, author kind, submission deadline, quota | 1, 2, 4, 5, 6, 7, 8, 13 |
| `pma` | Rust | dispatch through a container, verb taxonomy, payload validation, decision scope, knowledge classification | 9, 10, 11, 12, 14 |
| `sanduk` | Python | host-path mount, uid mapping, the socket mount, agent stdio on pipes | 3 |

Current state, for sizing. `minos` has the wire, the timeline, a Go client with
cursor and gap repair (`go/internal/client/protocol.go:673`, `:891`), and a
conformance suite that spawns the server. `sanduk` already runs eight agents in
containers, maps the image uid (`src/sanduk/runtime.py:176`) and relays to the
model provider from the host (`src/sanduk/proxy.py`). `pma` dispatches an agent
into a worktree and has a routing policy with approval levels
(`src/route.rs:22`).

## 2. Rulings

**2.1 The broker is Go, and ships from `minos` as `go/cmd/minosb`.**

The recommendation's 3.4 says the broker is the client `pma` needs anyway.
That is true of the role and false of the language: `pma` is Rust, and the only
client of this wire is Go. Writing the broker in Rust implements the wire a
second time, which is the cost 3.4 exists to avoid. Writing it in Go reuses
`go/internal/client` whole, including the gap repair that requirement 3 rests
on. The residual is that `pma` gains no in-process client; it does not need
one, because every conversational act it performs can go through the same
binary.

**2.2 `pma` drives the broker as a child process, not as a library.**

`pma` starts `minosb` with the run's parameters and reads its stdout. The
broker's death ends the run. This keeps the recommendation's layer 4 intact:
`pma` holds the container handle and kills it directly, never through the
broker. An FFI binding would tie the two lifetimes together, which is the one
property layer 4 forbids.

**2.3 Phase 0 runs a room for conversation and a channel for decisions.**

`submit`, `await` and a decision are channel operations today, not room
operations (wire-contract.md section 9). A room has no submission queue. Phase
0 promises no server change, so the run gets both: a room for relay, `say` and
the developer's instructions, and a channel whose moderators are the developer
and `pma`, carrying submissions. The shim's six operations do not change; the
broker holds both ids and routes by operation. Phase 1 may move submissions to
rooms; that is a wire change and it is not free, so it waits until a second run
shows the two-object model is actually awkward.

**2.4 Phase 0 workers share one configured account.**

Accounts are a static map in the environment (`go/internal/config/config.go:60`).
There is no mint. So Phase 0 adds a `worker` account and isolates runs by room
grant alone. Two consequences to state plainly: one broker's session could read
another run's room if it asked, and the room's author names do not distinguish
workers. Neither is reachable from a container, because the container reaches
six operations and never the session. Item 5 is what makes the isolation a rule
rather than a convention, and item 6 is what makes the author names true.

**2.5 A history shortfall is an error the shim reports.**

`history` caps at 200 on the tail (`config.go:23`), so a broker more than 200
behind cannot page back. Requirement 3 forbids advancing the cursor over the
gap. `minosa messages` therefore prints the gap as a record of its own and
exits non-zero, and the broker refuses to advance. A worker that cannot read
what it missed must stop, not guess.

**2.6 `pma` cannot dispatch into a container today, and that comes first.**

`pma/src/dispatch.rs` runs the agent in a worktree directly; nothing in `pma`
mentions `sanduk`. The architecture assumes that path exists. It does not. It
is item 0 below, it is on the critical path, and it is the largest single piece
of integration in Phase 0.

## 3. Proposed answers to the open questions

Section 13 of the recommendation, answered. Each is a plan input, not a
finding.

1. **Server unreachable at start: fail the run.** A run with no conversation
   cannot be corrected, cannot ask, and cannot be read. Its output would still
   need review, so nothing is saved by starting it. `pma` marks the dispatch
   deferred and keeps the task in the backlog.
2. **An unanswered submission is the person's failure, and the run's timeout.**
   The worker did the one thing available to it. So the terminal state is
   `expired`, distinct from `rejected`, the run ends without a change, and the
   task returns to the backlog carrying the rule text the refusal named. A
   worker is never penalised for asking.
3. **The archive is authoritative for what was decided; the relay log for what
   reached a model.** They answer different questions and neither substitutes.
   The archive's retention must therefore be at least the relay's, which is a
   check in `pma` at dispatch, refusing a run whose room retention is shorter
   than its own run-record retention.
4. **`pma` must be able to recover a room without its index.** So every
   workflow room is filed with `project`, `scope` and `task` (section 12 of the
   wire contract), which makes `sync` a full map of the fleet's conversations.
   `pma`'s index becomes a cache, not the only record.
5. **Nothing deletes a completed workflow room, and nothing will.** Deletion is
   out of scope for these phases. The room is the audit, and retention already
   ages the archive. Revisit when storage is measured, not before.

## 4. Phase 0 -- a worker in a room, with no server change

Five items. Item 0 is the prerequisite 2.6 names. Each item is usable, and
each has a test that fails before it and passes after.

**0. `pma` dispatches through `sanduk`.** `pma/src/dispatch.rs`,
`pma/src/worker.rs`.

Add a container dispatch path beside the direct one, selected per route. `pma`
builds the worktree as it does now, then invokes `sanduk` with the agent, the
image, the worktree and the run's prompt. The agent's argument building stays
in `worker.rs`; only the process that ends up running it changes. Keep the
direct path: it is the control when a container run misbehaves.

Done when: a dispatch runs an agent in a container, writes to the worktree as
the host uid, and `pma review` shows the diff exactly as it does today.
Tests: one integration test per path in `pma`, asserting the same diff from the
same task.

**1. The broker.** New: `go/cmd/minosb`, `go/internal/broker`.

- Dial the existing listener, log in, enter the run's room, subscribe to the
  run's channel.
- The six operations, over a unix socket, framed as one JSON request and one
  JSON reply per connection.
- A queue per room with the cursor, applying the delivery rules of wire-contract
  section 8, repairing on a gap through `client.repair`, and refusing to advance
  on a shortfall (ruling 2.5).
- A reader split: the connection drain never blocks on a model call, an agent
  write, or a `say`. This is the requirement-3 property, and it is the one piece
  of concurrency in the design worth testing directly.
- Refusals are sentences (recommendation 7.1), and a refusal names the rule that
  would permit it (7.2).

Done when: a broker holds a room, answers all six operations, and survives a
server restart under it without losing a message.
Tests: unit tests in `go/internal/broker` against `internal/testserver`; a
gap-and-repair test that drops pushes; a shortfall test that pushes the room 300
messages ahead of a stopped broker and asserts a refusal rather than a jump.

**2. `minosa`.** New: `go/cmd/minosa`.

Static, multicall, no dependency on the client package. It opens the socket,
writes one request, copies bytes, exits with the broker's status. Payloads come
from a file or stdin; no subcommand takes JSON on a command line
(recommendation 4.2). `status` reports the grant, the window, the expiry and
whether the broker holds a connection.

Done when: `minosa` is under 400 lines, links statically, and has no import
from `internal/client`.
Tests: a table test per subcommand against a fake broker socket; a test that
asserts a malformed reply exits non-zero with a readable line.

**3. `sanduk` gains four things.** `sanduk/src/sanduk/runtime.py`,
`agent.py`, `cli.py`.

- Mount the worktree at its own host path, rather than a fixed container path.
- Map the host uid and gid at runtime and drop to them. The image label already
  records the agent uid (`runtime.py:38`); this is the runtime half.
- Mount one unix socket per run, outside the work mount, with a mode that admits
  only that uid.
- Run the agent with stdin, stdout and stderr on pipes. Today stdin is
  `DEVNULL` (`agent.py:262`), which forecloses the push lane entirely.

Done when: a container run writes files owned by the developer, at paths that
resolve on the host, with a socket it can reach and no route off the host.
Tests: `sanduk`'s existing runtime tests, plus one that asserts the socket is
not under the work mount and one that asserts file ownership after a run.

**4. The broker takes the pipes.** `go/internal/broker`.

- Push a room message onto the agent's stdin as stream-json, queued until the
  turn ends.
- Relay each turn's final assistant message to the room under the worker's name.
- Interrupt before a push that corrects, never after (recommendation section 5).
- Per-agent framing lives in one adapter per agent CLI, so a verbose or
  ill-formed final message is one adapter's problem.

Done when: a developer types in the room, the worker reads it mid-run, and the
worker's reply appears in the room without the worker calling `say`.
Tests: an end-to-end test with a scripted fake agent that reads stream-json on
stdin and writes it on stdout. No model in the test path.

Phase 0 exit: one real task, dispatched by `pma`, run in a container, visible
in the TUI, interruptible from it, and stoppable with the broker killed.

## 5. Phase 1 -- the server work

Four items in `minos`. Each changes the wire, so each needs a
`docs/wire-contract.md` section and conformance tests in `go/conformance`
before it is done. The contract is the deliverable; the implementation is the
proof.

**5. Grants.** `go/internal/httpapi/session.go`, `go/internal/chat/chat.go`,
`go/internal/messaging`.

A grant is a credential that carries one room, a read window, a capability set
and an expiry. Minting and revoking are administrator operations. Revocation
closes every connection on the grant with a reason distinguishable from an
ordinary drop, or a client that should stop reconnects instead.

Authorisation today is `(username, isAdmin)` threaded through every messaging
method. A grant is a third authority, so the parameter becomes one principal
value and every method reads it. That is a wide, shallow change: expect every
file in `internal/messaging` to be touched and almost no logic to move.

The read window filters agent-authored messages alone. The developer's
messages and the server's events cross every boundary (recommendation section
6). Filtering must apply to `history`, to `sync` and to the push fan-out in
`chat.go`, or a window that holds on read leaks on delivery.

**6. Author kind.** `go/internal/timeline`, the message shape.

A message gains an author kind: developer, supervisor, worker, system. A worker
that cannot tell the developer's instruction from the supervisor's cannot obey
the right one when they disagree. Readable by a grant holder; it is the field
the window filters on.

**7. Submission deadline.** `go/internal/messaging`, submissions.

A third terminal state, `expired`, and an answer payload alongside the comment.
Ruling 3.2 makes expiry the person's failure and the run's end. A sweep expires
what is past its deadline, and the push tells the author.

**8. Rate and quota.** `go/internal/messaging`, `go/internal/config`.

A send rate and a per-grant message quota, refused with a sentence. The broker
keeps a local limit too, so a cooperating agent gets a fast refusal without a
round trip.

Phase 1 exit: a run holds a grant that names one room and one window, the grant
dies with the run, and a revoked grant cannot reconnect.

## 6. Phase 2 -- policy, in `pma`

Four items, all Rust, no server change.

**9. The verb taxonomy.** One artifact naming, per payload verb, whether it
interrupts the turn and whether it is eligible for a standing decision. Both
questions, one table. Without the first, a status note is destructive or a
correction is impossible.

**10. Payload validation at the boundary.** `pma` parses agent-authored JSON
before it reaches a decision, and rejects with a sentence. The server passes
payloads unread, which is correct and is why this check cannot live there
(recommendation 7.4).

**11. Decision scope.** `once`, `run`, `always`, with a match specificity.
`always` appends to `pma`'s routing policy, which already exists
(`pma/src/route.rs:99`) and already round-trips through a stored form. Three
constraints hold, and they are the mitigation, not decoration: the developer
authors the rule text; the policy artifact is reviewed per revision; `always`
is refused for any verb whose arguments cannot be constrained.

**12. Knowledge sources classified by least-trusted writer.** Developer-written
sources are unrestricted. Agent-written sources are readable only within the
room and window that produced them, unless approved, and approval publishes
them unedited under their author's name. A validating stage reads no
agent-written source; that is one flag on a stage definition
(`pma/src/workflow.rs`).

Phase 2 exit: the developer answers a recurring permission question once, and
the next occurrence is answered with no model and no person in the path.

## 7. Phase 3 -- reach

**13. `minosa mcp`.** The same six operations over stdio, against the same
socket. No new authority, no new code in the broker.

**14. Per-agent MCP registration.** One config shape per agent CLI, in `pma`'s
dispatch adapters. This is the per-agent cost MCP does not remove; it relocates
it from the prompt to the config.

## 8. Test strategy

- **The wire.** Every Phase 1 item extends `docs/wire-contract.md` and
  `go/conformance`. The suite spawns the server through `MINOS_CONFORMANCE_CMD`
  and is the only place a wire claim is proved.
- **The broker.** Unit tests against `internal/testserver`. The three properties
  worth a dedicated test each: a gap is repaired, a shortfall refuses, and a
  blocked agent write never stops the connection drain.
- **The shim.** A fake broker socket. No container, no server.
- **The container.** `sanduk`'s own tests cover the mount, the uid and the
  socket mode. One end-to-end test in `pma` covers dispatch through to a diff.
- **The agent.** Never in a test path. A scripted fake reads stream-json and
  writes stream-json, so every lane is testable without a model.
- `make test` in `minos` runs unit tests and the conformance suite together, and
  must pass at the end of every item.

## 9. The critical path

| Item | Blocks | Blocked by |
|-|-|-|
| 0. `pma` through `sanduk` | 3, 4, everything end to end | -- |
| 1. broker | 2, 4 | -- |
| 2. `minosa` | 4 | 1, 3 (the socket mount) |
| 3. `sanduk` mounts, uid, pipes | 2, 4 | 0 |
| 4. pipes to the broker | Phase 0 exit | 1, 2, 3 |
| 5. grants | 6, 7, 8 | 1 |
| 6. author kind | 12 | 5 |
| 7. deadline | 11 | 5 |
| 8. rate and quota | -- | 5 |
| 9-12. policy | -- | 4 for 9 and 10; 7 for 11; 6 for 12 |
| 13-14. MCP | -- | 4 |

Items 0 and 1 are independent and are the two to start. Items 1 and 3 are the
ones whose interface must be agreed before either is written: the socket path,
its mode, and the stream-json framing on the pipes.

## 10. What would reverse a ruling

- **2.1, the broker in Go.** Reversed if `pma` turns out to need a conversation
  of its own that a child process cannot carry -- streaming the supervisor's
  turn, for instance. Then the wire is implemented twice and 3.4's cost is paid.
- **2.3, room plus channel.** Reversed if holding two objects per run makes the
  broker's routing ambiguous in practice. Then submissions move to rooms in
  Phase 1, which is a wire change, a conformance change and a client change.
- **The shim before MCP.** The recommendation's own condition: reversed if every
  agent actually dispatched to serves MCP, and the prompt preamble proves
  unreliable at getting the model to call the shim. Measurable after the first
  ten runs, not before.
- **The broker on the host.** Reversed if supervising one process per run costs
  more than a listener in the server. Measurable only once runs are concurrent.

Nothing above is reversible cheaply after Phase 1, because a grant is a wire
concept. Phases 0 and 2 are cheap to redo. That is the reason for the order.
