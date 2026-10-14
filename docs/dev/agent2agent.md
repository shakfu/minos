# Agent2Agent (A2A) support

2026-10-14. Analysis, decision record, and report on `minosg`. Code: `go/internal/gateway`, `go/cmd/minosg`.

## 1. Summary

`minosg` exposes one worker's room as an [A2A v1.0](https://a2a-protocol.org/latest/specification) agent. It serves the JSON-RPC binding on loopback, behind a bearer token, built on [a2a-go](https://github.com/a2aproject/a2a-go) v2.6.0. The peer is an external orchestrator. `minosg` is an ordinary minos client on the host, so `minosd`, `minosb`, `minosa` and the container are unchanged.

The main weakness is reply attribution. The gateway reads the room, and the room cannot say which turn answered which message. Section 9.1 proposes a fix in `minosb` of about 20 lines.

## 2. A2A, as far as it matters here

Source: the [specification](https://a2a-protocol.org/latest/specification), read in part (about 100k of 256k characters), and the a2a-go source.

- **Bindings.** JSON-RPC, gRPC, HTTP+JSON. `spec/a2a.proto` is normative.
- **Methods.** `SendMessage`, `SendStreamingMessage`, `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`, four push-config methods, `GetExtendedAgentCard`.
- **Task states.** `SUBMITTED`, `WORKING`, `INPUT_REQUIRED`, `AUTH_REQUIRED`, and the terminal states `COMPLETED`, `FAILED`, `CANCELED`, `REJECTED`.
- **Streaming.** Server-Sent Events of `task`, `message`, `statusUpdate`, `artifactUpdate`.
- **Push.** HTTP POST webhooks to a URL the client supplies.
- **Discovery.** A public agent card at `/.well-known/agent-card.json`, optionally signed.
- **Context.** `contextId` groups tasks. A client may supply one; a2a-go accepts it (`a2asrv/agentexec.go:267`).

a2a-go facts that shaped the code:

- The JSON-RPC server links only `google/uuid` and `golang.org/x/sync`. Neither the server nor the client links grpc.
- An executor runs under `context.WithoutCancel`, so a client disconnect does not stop it. A cancel does stop it, through the execution's errgroup.
- The inactivity timeout is off by default. A wait of 30 minutes for a worker is safe.
- Without a push config store, every push method returns `ErrPushNotificationNotSupported`.
- v2.6.0 declares `go 1.26.0`, which raised this module's minimum from 1.25.

## 3. Where minos stood

| Hop | Protocol |
|-|-|
| Client to `minosd` | HTTP with a signed session cookie |
| Client to `minosd`, live | Websocket, OS.js frame format, frozen |
| `minosa` to `minosb` | Unix socket, one JSON line each way, six operations |
| `minosb` to agent | stream-json on stdin and stdout |
| `minosb` to dispatcher | JSON lines on stdout and stdin |

Three recorded decisions bear on A2A:

- [design.md](design.md) section 12 put A2A out of scope as "agent discovery".
- [recommended-architecture.md](recommended-architecture.md) section 9 rules out webhooks, discovery, and TLS on a link that cannot leave the host.
- Requirement 1 of the same document: the container chooses no destination.

## 4. Placements considered

| Placement | Verdict |
|-|-|
| **A. Gateway: a host-side client** | Chosen. Nothing else changes, and the room stays the record. |
| B. Native in `minosd` | Rejected. It adds a second wire to a frozen server, outside `go/conformance`, and reverses section 12 outright. |
| C. A2A between `minosb` and the agent | Rejected. It needs agent CLIs that serve A2A. Claude Code speaks stream-json. |
| D. The container as an A2A client | Rejected. It breaks requirement 1. |

The gateway narrows section 12 instead of reversing it. It publishes a card for one worker that `pma` already started. It does not discover, federate, or give a container a route out.

## 5. What was built

### Mapping

| A2A | minos |
|-|-|
| Agent card | One worker in one room. Public. |
| `SendMessage` | A send to the room under the gateway's account. The worker's next text message is the artifact. The task then completes. |
| `SendStreamingMessage` | The same, as `SUBMITTED`, `WORKING`, artifact, `COMPLETED`. |
| `CancelTask` | `/interrupt` in the room. It takes effect only if the gateway's account is in `minosb -interrupters`. |
| `contextId` | Not mapped. Every context lands in the one room. |
| Push notifications | Refused. The card declares `pushNotifications: false`. |
| File and data parts | Refused with `ErrUnsupportedContentType`. |
| Empty text | Refused with `ErrInvalidParams`, before anything is posted. |
| No reply in `-timeout` (default 30m) | `FAILED`, with the reason as the status message. |
| A gap the client cannot backfill | Every waiting task fails. The reply may be among the lost messages. |

### Access

- A bearer token from `MINOS_A2A_TOKEN` guards `/a2a`. The comparison runs in constant time.
- `minosg` refuses a non-loopback `-listen`, because the token would travel in clear.
- Secrets come from the environment only, as in `minosb`.

### Files

| Path | Is |
|-|-|
| `go/internal/gateway/gateway.go` | Executor, card, token check, reply matching. About 300 lines. |
| `go/internal/gateway/gateway_test.go` | 10 tests against `testserver` and the SDK's own A2A client. |
| `go/cmd/minosg/` | Flags, the loopback check, and its tests. |

## 6. Verification

- **`make test`.** Every package passes, the wire contract included.
- **Gateway tests.** They cover the public card, refusal without the token (4 header shapes), completion by the worker only, one reply for two tasks, timeout, cancel, stream order, part refusals, push refusal, and `Open` preconditions.
- **Mutation checks.**
  - Matching any author's reply instead of only the worker's: caught.
  - Accepting every token: caught.
  - Removing the cancel branch in `Execute`: not caught. The SDK discards events after a cancel, so the branch was deleted.
- **Live run.** `minosd`, `minosg`, and a scripted worker, with curl as the orchestrator:
  - The card was served.
  - An RPC without the token got 401.
  - A blocking `SendMessage` returned `TASK_STATE_COMPLETED` with the worker's reply as the artifact.
  - `CreateTaskPushNotificationConfig` returned `-32003 PUSH_NOTIFICATION_NOT_SUPPORTED`.
- **Not run.** The [A2A TCK](https://github.com/a2aproject/a2a-tck). See 9.9.

## 7. Known limits

1. **Reply attribution.** A relayed turn and a `say` look the same in the room. A mid-turn `say` completes a waiting task. So can a turn that began before the task was posted.
2. **Shared replies.** A turn answers everything held while it ran, so tasks in flight together get the same artifact.
3. **Cancel is not per task.** `/interrupt` stops the whole turn, including work for other tasks and for the developer.
4. **One context.** `contextId` is ignored.
5. **One account.** Every orchestrator speaks as the gateway's account. This repeats the phase 0 single-account gap (implementation-plan 2.4).
6. **Tasks in memory.** A restart forgets them, and in-flight tasks end without a final state.
7. **Oversized bodies.** A message over `messaging.BodyLimit` creates a task and then fails it. It should be refused before the task exists.
8. **One process per room.** An orchestrator with 20 workers needs 20 gateways on 20 ports.

## 8. Variants

### V1. One gateway per room (built)

- **For.** It matches `minosb`: one process, one room. The card describes a real worker.
- **Against.** Limits 1, 2, 4 and 8.

### V2. One gateway, many rooms

`contextId` names a room. A message without one is refused, or goes to a default room.

- **For.** One endpoint and one token per orchestrator. `ListTasks` by context lists a room's tasks. Removes limits 4 and 8.
- **Against.**
  - The card describes a router, not a worker. Per-room skills would leak room names on a public card. They belong on the extended card, which requires auth.
  - Every room the account can read becomes reachable through one token. That is wider than V1 unless phase 1 grants exist.
- **Cost.** About 60 lines: per-room waiters, and contextId validation against `chat.Space`.

### V3. Turn receipts from the relay

`minosb` already knows the exact correlation. `forPush` returns everything through a sequence number `K`. `deliver` writes it as one frame. The next final message `Read` relays ends that turn. So the relay can mark its post with "this turn answers through seq K", in a fenced block like `minos-payload`:

````text
all 41 pass

```minos-turn
{"through": 1287}
```
````

The gateway then completes exactly the tasks whose message sequence is at or below `K`, and only on a receipt. A `say` becomes progress instead of an answer.

- **For.** Fixes limit 1. It makes limit 2 correct instead of approximate: tasks answered by the same turn share its reply by construction. Any room reader gains the same knowledge, the TUI and `pma` included.
- **Against.** It changes the room text every reader sees, and the fence convention stays informal. The receipt is only as trustworthy as the relaying account, which is fine while one broker owns that account.
- **Cost.** About 20 lines in the relay, 10 in the gateway, and tests on both sides. No wire change.

### V4. The A2A listener inside `minosb`

`minosb` serves A2A on a host loopback port next to its socket.

- **For.** Exact correlation without a room convention. A cancel can interrupt directly, without `-interrupters`. One process per run, not two.
- **Against.**
  - The broker gains a network listener and an HTTP dependency. Its process must not die, and more code means more ways to die.
  - A2A traffic bypasses the room unless the broker also posts it, so the broker would have to do both.
  - It ties A2A to container runs. A room with a person in it could not be fronted.
- **Verdict.** V3 gets the correlation for a fraction of the risk.

### V5. Native in `minosd`

This is placement B. The server would answer A2A for every room, with server-side task state.

- **For.** No extra process. Tasks could be durable in the timeline.
- **Against.** Everything in section 4. Revisit only if A2A replaces the OS.js wire, which nothing proposes.

### V6. A bridge: a remote A2A agent as a room participant

This is the reverse direction. A host process holds a minos account in a room and forwards messages addressed to it to a remote A2A agent with `a2aclient`. It posts the replies back.

- **For.** The supervisor, the developer, or a worker can consult an external agent, and the room records the exchange. The container still chooses no destination: the bridge does, and the bridge is configured on the host.
- **Against.** It is new outbound traffic from the host. The remote agent's output enters a worker's context, which [recommended-architecture.md](recommended-architecture.md) section 8 says must be classified by its least-trusted writer. It needs an addressing rule, such as a prefix or a dedicated room.
- **Verdict.** Worth building only when a named external agent exists.

### V7. Other bindings

- **HTTP+JSON.** `a2asrv.NewRESTHandler` on the same handler. About 3 lines, no new dependencies.
- **gRPC.** `a2agrpc` pulls in grpc and protobuf, roughly 30 modules.
- **Verdict.** Add REST if an orchestrator needs it. Skip gRPC unless an orchestrator requires it.

## 9. Improvements, ranked by value over cost

1. **Turn receipts (V3).** Fixes the main correctness gap. Small, and no wire change.
2. **Refuse oversized bodies up front.** Check `messaging.BodyLimit` in `textOf` and return `ErrInvalidParams`. One line, and it fixes limit 7.
3. **Payload mapping.** Map an incoming `DataPart` to a `minos-payload` fence. Map a reply's fence to a `DataPart` artifact. `pma` already reads that fence, so structured requests survive the trip both ways.
4. **Per-task cancel.** If other tasks are in flight, cancel locally and do not post `/interrupt`. Interrupt only when the task is the only one waiting. This fixes limit 3 for concurrent use.
5. **Shutdown with a final state.** On SIGTERM, fail in-flight tasks with "the gateway stopped", then shut down. Today they vanish (limit 6).
6. **Progress as artifact updates.** With receipts, stream a worker's `say` as an `artifactUpdate` with `append`, and complete on the receipt. A streaming orchestrator then sees work as it happens.
7. **Tokens mapped to accounts.** A token table, one minos account per orchestrator, so the room shows who asked. This depends on phase 1 grants to mean anything (limit 5).
8. **Durable tasks.** Implement `taskstore.Store` over SQLite, so `GetTask` survives a restart. Low value while tasks are short-lived and the room keeps the record.
9. **Run the A2A TCK.** `./run_tck.py --sut-host http://127.0.0.1:8100 --transport jsonrpc`. It needs Python and uv, so it belongs in an optional make target like `diagrams`, not in `make test`.
10. **`INPUT_REQUIRED`.** Map a worker's question to `INPUT_REQUIRED`. This needs a payload verb that marks a question, which is phase 2's verb taxonomy. Do not guess from text.

Not recommended:

- **Agent card signing (`a2acrypto`).** The card is served on loopback to a caller that already holds the token.
- **Push notifications.** They are the webhook section 12 rules out. `SubscribeToTask` gives the same updates by pull.

## 10. Open questions

1. Is the orchestrator one process or several? V2 and improvement 7 matter only for several.
2. Should a receipt be a room convention (V3) or a wire field? A wire field needs a server change and a conformance test, but stops a person from forging one by typing the fence.
3. Who may cancel? Today, anyone with the token interrupts the turn, if the gateway account is an interrupter. Should a cancel instead need the developer's confirmation in the room?
4. Does a task's lifetime belong to the turn, or to a decision? If an A2A task should end when a submission is approved, it maps to `INPUT_REQUIRED` and the moderation flow, not to a reply.
