# Agents in containers: the network and the client

2026-09-20

How an agent inside a sealed container reaches `minosd`, what its client holds, and how the model drives it. Nothing here is built.

[design.md](design.md) decides trust, grants, capabilities and stopping. This document decides nothing it decided. It answers the three questions design.md leaves as "a static binary that is more than a client" (design.md 11):

1. What address does the container dial, given that it has no route out.

2. What subset of the wire does that client implement.

3. How does a model, which speaks neither websocket nor JSON frames, use it.

Vocabulary is [chat-concepts.md](chat-concepts.md) and [wire-contract.md](../wire-contract.md).

![architecture](../media/architecture.svg)

Who talks to whom. Sources are `docs/media/*.d2`; regenerate with the command at the head of each.

## 1. The constraint stack

Six facts. Every option below is scored against them.

| Fact | Where |
|-|-|
| A sealed container's network is `--internal`; the host-side relay is the only reachable address | `sanduk/runtime.py:228`, [sanduk_feedback.md](sanduk_feedback.md) 2 |
| The relay is HTTP/1.1 on `BaseHTTPRequestHandler` with no upgrade handling; its allowlist is three exact paths | `sanduk/proxy.py:38`, sanduk_feedback 6 |
| `minosd` binds one TCP address | `go/cmd/minosd/main.go:91` |
| An upgrade with no `Origin` is accepted, so a non-browser client needs no header dance | `go/internal/httpapi/httpapi.go:513`, wire-contract 5 |
| A connection's outbound queue holds 256 frames, and a client that stops draining is disconnected | `go/internal/socket/socket.go:42` |
| Grants, capabilities and the `/vfs` and `/settings` denials are unbuilt; a container presents a 12-hour cookie today | design.md 8.1-8.3, 11 |

The last one bounds everything: until grants exist, whatever is built here is an ordinary account with an ordinary session. That is a reason to build the transport first and the credential second, not a reason to wait.

## 2. Four ways in

| # | Route | Container holds | `minosd` change | Relay change | Sealing |
|-|-|-|-|-|-|
| 1 | a second TCP listener on the internal network | an address | a second listener, and per-listener op gating (design.md 8.6) | none | weakened: `minosd` is a second exit, reachable by every container on the network |
| 2 | the relay proxies the websocket | the relay's address, already allowlisted | none | websocket upgrade and frame relay | held at the relay |
| 3 | a unix socket, bind-mounted | a socket file | `net.Listen("unix", ...)` | none | held: no address exists to reach |
| 4 | nothing; the client runs on the host beside the container | nothing | none | none | held |

**Recommendation: 3, with 4 as the alternative framing in section 7.**

![reach](../media/network.svg)

What can reach what, once option 3 is taken. The agent's two pipes are in it because they are a reach fact: they do not cross the container boundary, and nothing outside addresses the agent process.

**Why not 1.** design.md 8.6 calls separable listeners "the one mitigation" for a dual-homed `minosd`. A listener on the internal network is reachable by every container on it, so the mitigation is partial: it separates administrative operations from agent ones and leaves every agent reachable by every other. A socket file is reachable by the containers it is mounted into and nothing else.

**Why not 2.** A relay that cannot read the envelope cannot scope a room, so it would enforce nothing a websocket carries; sanduk_feedback 6 reaches the same split -- routes, rate and quota at the relay, rooms and capabilities in `minosd`. Under option 3 the relay keeps exactly that job for the model provider's API and never sees chat traffic. Websocket relaying is new code in a handler that has none.

**Why 3 is cheap.** `net.Listen("tcp", address)` at `go/cmd/minosd/main.go:91` becomes a switch on the configured address, and `server.Serve(listener)` is unchanged. On the client side, `NewSocket` borrows the HTTP client's transport (`go/internal/client/transport.go`), so a `DialContext` returning `net.Dial("unix", path)` carries both the session and the socket. Base URL `http://minos.local`, which `websocketURL` maps to `ws://minos.local/`; the host is a Host header and nothing resolves it.

**What the socket does not do.** It removes the address. It does not scope anything. `/vfs` and `/settings` answer over the same socket, so design.md 8.3's two blanket 403s are still required, and the socket's file mode is not a substitute: a container running as root ignores it. Transport removes reach; the credential removes authority. Build both.

**One socket or one per container.** One socket, mounted into each container, means identity rides the grant alone. One socket per container gives the server a second identity axis it does not have to trust the token for, at the cost of container lifecycle leaking into the server -- `minosd` would be told to open and close socket files as runs start and end. Start with one; the per-container variant is additive.

## 3. What the agent client is

`go/internal/client/protocol.go` is 1543 lines and implements 38 operations for a person driving a terminal. The agent client implements 7.

| Kept | Op | Why |
|-|-|-|
| sync | `sync` | its own rooms; no roster (design.md 8.3) |
| backfill | `history` | not optional: section 8 of the contract is a correctness property |
| speak | `send` | one room (D4) |
| propose | `channel.submit` | anything needing approval (design.md 10) |
| close a decision | `submission.acknowledge` | a rejection stays until acknowledged |
| progress | `read` | a durable marker that never moves backwards, so "handled up to 41" survives a restart |
| control items | `channel.open` | the channel analogue of `read` |

| Dropped | Why |
|-|-|
| `enter`, `exit` | agents do not occupy rooms (D13) |
| `open`, `create`, `invite`, `uninvite`, `leave` | a grant founds nothing and admits nobody (D3, D9) |
| `group.*`, `project.*` | administrative |
| `channel.create`, `publish`, `admit`, `revoke`, `appoint`, `dismiss` | administrative |
| `channel.queue`, `submission.approve`, `submission.reject` | a worker moderates nothing |
| `archive.*` | administrative, and a read of history the window exists to deny |
| `subscribe`, `unsubscribe` | `control` membership is the grant's, not the agent's |
| `room.close`, `room.reopen` | `pma` closes a workflow room |
| presence, roster, unread counts, the whole of `go/internal/tui` | no reader |
| `/vfs`, `/settings` | design.md 8.3; files move on the bind mount (D1) |

Three behaviours it must keep whole, because each answers a failure the human client already hit:

- **Cursor and gap repair.** Compare `seq` against the cursor, request `history` on a gap, and report a shortfall rather than advancing over it (wire-contract 8). An agent that silently skips the developer's correction is worse than one that stops.

- **Reader and worker split.** The socket reader drains into a local queue; the model turn consumes the queue. A turn that reads the wire directly stops draining for the length of an inference, and 256 frames later the server disconnects it (`go/internal/socket/socket.go:42`).

- **Reconnect with backfill.** A dropped socket is normal. Reconnect, sync, backfill from the cursor. The session's 12-hour lifetime is not refreshed by anything in `go/internal/client`, so a long run reconnects at least once; under a grant, expiry tracks the run instead (design.md 8.1) and the socket ends when the run does.

**Where the code comes from.** `go/internal/client` is `internal`, so nothing out of tree imports it, and `go/conformance/wire.go` is 581 lines of second implementation (design.md 11). Before writing a third, move the envelope -- frames, `pid` matching, cursors, repair -- to an importable package. The agent client is then the 7 operations above plus a queue, in the low hundreds of lines.

## 4. How the model uses it

The model speaks neither frames nor Go. Something translates a room into turns. The line that decides the shape:

**An MCP tool is pull. Anything that must arrive without the agent's cooperation cannot be an MCP tool.**

That rule is right about MCP and it does not decide the design, because it says what one lane cannot carry rather than how many lanes there are. Inbound traffic is three classes, not two, and the middle one is the developer's correction: it must not wait for the agent to look, and it must not destroy the turn in flight.

| Class | Example | Must arrive | Lane |
|-|-|-|-|
| the agent asks | what is in the room, is my submission decided | when the agent asks | MCP tool call, and its reply |
| the client pushes | a developer's correction, a `pma-agent` instruction | without the agent asking | a user message on the agent's stdin, queued for the turn's end |
| the client interrupts | stop now | mid-inference | an interrupt, then SIGINT, then the container engine (design.md 9) |

So the channel is two-way, in the sense a headless terminal is two-way: the client writes messages in and reads the turn out. It is worth being exact about the limit, because "like a terminal" invites the belief that a pushed message lands mid-thought. It does not, in either place. Streaming input mode processes queued messages sequentially and offers interruption as a separate act ([streaming input](https://code.claude.com/docs/en/agent-sdk/streaming-input)); single-message mode supports neither queueing nor interruption, which is the mode `sanduk` runs today (sanduk_feedback 3). A message pushed mid-turn is read when the turn ends, which may be minutes. Anything sooner is an interrupt.

**Ordering matters at that boundary.** A `system/init` event names capabilities such as `interrupt_receipt_v1` and `interrupt_cancel_queued_v1` ([headless](https://code.claude.com/docs/en/headless)), and the second says an interrupt may cancel what is queued. Push the correction and then interrupt, and the correction can go with the interrupt. Interrupt first, then push. Feature-detect from `capabilities` rather than comparing version strings.

### Who holds the pipes

The lanes above decide something this document earlier left to `sanduk`: the process is owned inside the container, by the client, which spawns the agent with streaming JSON in both directions.

The argument is not preference. It is the cursor. Section 3 requires one reader per room with one cursor and gap repair, because a client that silently skips a message is worse than one that stops. Split the lanes across the boundary -- `sanduk` on the host holding stdin, the client in the container holding the room -- and a pushed message reaches the agent by a path with no cursor, while the same message also arrives on the MCP lane. Two deliveries of one room, no ordering between them, and the repair mechanism cannot see either. One holder, or no guarantee.

The cost is the one sanduk_feedback 4 already priced, and it is not the envelope alone: nine argv builders, nine stream parsers, the timeout watchdog and the operator's live trace all move with process ownership. Its own mitigation is the answer -- the client shells out to a per-agent adapter and `sanduk` keeps them, which sanduk_feedback 4 names as the cheaper design. `sanduk` then owns the container rather than the process: build, start, kill, and the grant's revocation (design.md 9, layers 3 and 4), none of which need the agent's cooperation and all of which still work when `minosd` is what failed.

### The tools

The agent client serves MCP on stdio and the agent calls in. Five tools, each a request with a reply -- the agent is the MCP client here, the minos client is the server.

| Tool | Op behind it | Returns |
|-|-|-|
| `messages(since?)` | the local queue, plus `history` on a gap | author, author kind, seq, subject, body, payload |
| `say(body, payload?)` | `send` | ok, or the server's refusal string |
| `submit(subject, body, payload?)` | `channel.submit` | the submission id |
| `await(submission, timeout)` | the `submission` push | `approved`, `rejected` with the comment, or `timed_out` |
| `progress(seq)` | `read` | ok |

Four notes on the table:

- **Refusals pass through verbatim.** Every operation answers `{"error": "<message>"}` (wire-contract 6) and a model changes course on a readable string. A rate limit (D12, design.md 8.7) is therefore self-enforcing against a cooperating agent and still enforced against one that is not.

- **`payload` is opaque.** The server stores and returns it unread (D18). Its schema is `pma` policy: a verb for an instruction, a permission request and its cost for a worker.

- **`await` needs a server-side deadline.** design.md 8.5 adds one, with `timed_out` as a third terminal state. Without it an agent blocked on a decision nobody makes burns its run timeout and dies with the work half done.

- **`messages` and the push lane are the same queue.** The tool drains it when the agent asks; the push lane writes the head of it into the agent's stdin when the agent has not asked for a while, or when the message's payload carries a verb that says not to wait (D18). One queue, one cursor, so a message delivered by push is not delivered again by the tool.

- **`messages` cannot render D11 today.** A worker must tell the developer's instruction from `pma-agent`'s (D11), design.md 7.1 puts `kind` on the user object, and 8.3 gives a grant no roster. Until author kind lands on the message or in a narrowed `sync` (sanduk_feedback 7), the tool returns names the agent cannot classify. This is a blocker for the tool, not a nicety.

### The preamble

The harness states once, in the agent's system prompt, what the grant already enforces: one room, no invitations, no file transfer, three author kinds and what each means. Stating it does not secure it -- the server refuses either way -- but an agent that knows its bounds does not spend a turn discovering them.

### Blocking or polling

`messages` blocks or it does not. With the push lane in place the question shrinks: nothing urgent depends on the agent polling, so `messages` need not block. Recommendation: non-blocking `messages`, blocking `await` with an explicit timeout -- the agent reads when it chooses to look, blocks only when it has nothing else to do, and is pushed to when somebody needs it to know now.

## 5. A run, end to end

The architecture diagram above is the static cut. This is the temporal one.

1. `pma` mints a grant -- one room, a `since`, a capability set, expiry of the run plus a margin -- creates the socket file, and starts the container sealed, with `/work` and the socket bind-mounted.

2. The client dials the socket, presents the grant, syncs, spawns the agent with streaming JSON in and out, and serves MCP on its stdio.

3. The agent works, reads `messages`, writes `say`, `submit`s what needs a decision, records `progress`.

4. The run ends, or `pma` revokes. Revocation closes every socket on the grant (design.md 8.1); the tools then refuse and the agent reads why.

**Where the grant lives.** Not an environment variable, if container config is readable by other principals. A file on a mount that is not `/work` -- the same argument as sanduk_feedback 5, which found the session store could not live there either, because the agent edits `/work`.

## 6. Failure modes

| Failure | Symptom | Answer |
|-|-|-|
| `minosd` is down at start | the dial fails | decide: fail the run, or run offline with no conversation. Layers 3 and 4 of stopping work either way (design.md 9) |
| the socket drops mid-turn | frames above the cursor are lost | reconnect, backfill from the cursor (wire-contract 8) |
| the turn outlasts the queue | disconnected at 256 frames | the reader/queue split, section 3 |
| the agent never calls `messages` | corrections ignored | the push lane writes them to its stdin; the interrupt path when that is too slow |
| a correction is pushed, then the agent is interrupted | the correction is dropped with the queue | interrupt first, push second; `interrupt_cancel_queued_v1` in `capabilities` says whether this applies |
| the agent CLI speaks no streaming input | nothing can be pushed; only kill remains | detect at dispatch, as `sanduk` already refuses an unsupported pairing (sanduk_feedback 3) |
| the agent floods `say` | a refusal string | the server's rate and quota (design.md 8.7) |
| the grant is revoked mid-turn | every socket closes | tools refuse with a readable message; the client exits non-zero |
| `pma` loses the room id | nobody can find the conversation | unsolved; design.md 13.4 |

## 7. The alternative framing: no client in the container

Option 4 of section 2. The client runs on the host beside the container, and reaches the agent through the stdin and stdout `sanduk` already owns.

The case for it is not small:

- The container's network is untouched. No socket file, no mount, no listener, no relay change. Sealing stays literally true.

- The interrupt path and the message path are one path either way, and here it is a path that already crosses the boundary. Under option 3 the socket is a second route in, opened so the pipe's owner can also hold the room's cursor.

- Sealing never protected the repository from the model: the agent sends it to a hosted provider on every turn (sanduk_feedback 2). What sealing protects is the container's choice of destination, and a host-side client removes that choice entirely rather than narrowing it.

The case against, and it is the reason the recommendation stands:

- The agent cannot call a tool. Everything becomes a message injected into the turn, so `await`, `progress` and a refusal the agent can read on its own schedule have no expression.

- Every agent CLI differs. MCP over stdio is the one interface several already speak.

sanduk_feedback 4's price -- nine argv builders, nine stream parsers, the timeout, the trace -- is no longer a reason to prefer one over the other. Section 4 moves process ownership into the container under option 3 as well, so both options pay it, and both can pay it the same cheaper way: shell out to a per-agent adapter and leave those nine in `sanduk`.

What survives is the tool call. **Option 4 gives the agent no way to ask**, and `await`, `progress` and a refusal read on the agent's own schedule are asks. Option 3 keeps both lanes on one side of the boundary, which is also what keeps them on one cursor.

## 8. To decide

1. Unix socket, or the relay. Recommendation: the socket (section 2).

2. One socket, or one per container. Recommendation: one, then measure.

3. Does the client own the agent process, or does `sanduk`. Recommendation: the client, because the pipe's holder must hold the room's cursor (section 4). The open half is whether it carries the nine argv builders and parsers itself or shells out to a `sanduk` adapter -- recommendation: the adapter.

4. What triggers a push rather than leaving a message for the next `messages` call. A payload verb (D18) is the obvious rule; an idle timer is the fallback. Getting this wrong makes every status note an interruption, or none of them.

5. Where the grant is stored in the container.

6. Does a run proceed when `minosd` is unreachable at start.

7. Author kind on the message, or in a narrowed `sync`. The `messages` tool needs one of them (D11, sanduk_feedback 7).

8. Which package the envelope moves to, before it is implemented a third time.

## 9. Build order

Each step is usable before the next exists.

| Step | Depends on | Unblocks |
|-|-|-|
| 1. A unix listener in `minosd`, a dialer option in the transport | nothing | a container reaching `minosd` at all |
| 2. The envelope moved out of `internal` | nothing | one wire implementation instead of three |
| 3. The 7-operation client, with queue and cursor | 1, 2 | a container in a room, under a cookie |
| 4. The MCP server over it | 3 | an agent that can ask |
| 5. Process ownership: spawn the agent with streaming JSON both ways | 4, and streaming input in the agent CLI | the push lane, and interrupt without a second pipe holder |
| 6. Grants, capabilities, the two denials | design.md 8.1-8.3 | the isolation being a rule rather than a convention |
| 7. `sanduk` keeps the argv builders and parsers behind an adapter | 5 | one place per agent CLI instead of two |

Steps 1 to 4 ship an agent that talks and asks, with today's credential. Step 5 is what lets it be told. Step 6 is what makes the result defensible, and design.md 11 already says so: until it lands, "a worker agent in a container is an ordinary account with an ordinary session".
