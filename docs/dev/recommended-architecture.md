# Recommended architecture: agents in containers

2026-09-21. A recommendation, for approval before an implementation plan is written.

`pma` ranks outstanding work across many repositories and dispatches each task to a model in a container. `minosd` carries the conversation: the supervising model instructs the worker, the developer reads and intervenes, the worker asks for what it may not decide. This document states how the container reaches that conversation, what it may do when it gets there, and in what order to build it.

## 1. The four requirements

Everything below is scored against these.

| # | Requirement | Consequence if missed |
|-|-|-|
| 1 | The container chooses no destination | A repository that must not leave the host leaves it |
| 2 | The developer can reach a worker while the supervising model is down or wrong | The recovery path runs through the thing it exists to recover from |
| 3 | No message is silently skipped | A worker acts on stale instructions and nobody can tell |
| 4 | A run can be stopped when anything is broken | The only stop depends on the components most likely to fail |

Requirement 1 is what a sealed container buys and what any route out of it spends. It is the binding constraint, and the architecture below is chosen to spend as little of it as possible.

## 2. The recommendation in one paragraph

**A per-run broker on the host holds the credential, the conversation and the agent's pipes. The container holds a shim and nothing else.** `pma` mints a grant and starts a broker. The broker creates the run's socket, dials `minosd` with the grant, owns the room's cursor, and starts the container through `sanduk` with that socket mounted and the agent's stdio on its own pipes. It answers six operations on the socket; the agent reaches the conversation by running a command on its `PATH`. `pma` keeps container control for itself, so stopping a run never goes through the broker. Nothing inside the container holds a credential, knows the wire, or can name an address.

```
  HOST                                     SEALED CONTAINER
  ----                                     ----------------
  developer -- TUI --.
                     |
  pma-agent ---------+--> minosd <--.
  (untrusted model)  |  one listener |
                     |               | grant: one room, windowed,
  pma (trusted) -- admin             | expiring with the run
    |    |                           |
    |    `-- mints the grant,   +----+-----+
    |        starts the broker  |  minosb  |
    |                           | (per run)|
    |                           +-+------+-+
    |                             |      |
    |        stdin and stdout,    |      |  unix socket, mounted
    |        stream-json, through |      |  outside /work;
    |        sanduk's adapter     |      |  six operations
    |                         ....|......|..................
    |                         :   v      ^                 :
    |                         : agent -> minosa            :
    |                         : process  (shim on PATH)    :
    `-- stops the container ->:                            :
        directly              :............................:
                                          |
                              bind mount: /work at its host path
```

## 3. Why the broker is on the host

Four properties follow from that one placement. They are the case for the design.

**3.1 Containment does not wait for server work.** The broker offers six operations. It has no code for file write, no code for settings, no code for founding a room or inviting a principal. A container cannot reach what is not implemented, at any credential, on the first day. Put the client inside the container instead and it must reach `minosd` itself, which answers a 100 MiB file write and a settings replacement on the same connection as chat; those then have to be denied by a capability system that does not exist yet. **The denial should be the absence of an implementation, not a check that is scheduled.**

**3.2 No credential enters the container.** The broker holds the grant. The container holds a socket file. There is nothing inside to read, steal, copy to the bind mount, or keep after the run. Revoking is closing the socket, which needs neither `minosd` nor the agent's cooperation.

**3.3 The cursor and the pipes have one owner.** Requirement 3 needs one reader per room, one cursor, and a backfill when a sequence number gap appears. The same process must own the agent's stdin, because a message pushed into the turn and a message handed over the socket are the same message; two owners means two deliveries, no ordering between them, and no repair that can see either. Both owners are the broker.

**3.4 The wire is implemented once.** `pma` needs a client regardless. The broker is that client. The alternative puts a second implementation inside the container and leaves `pma` still needing its own.

The cost of the placement is one host process per run that must not die, and a container interface that is specific to this dispatcher rather than a general minos client. Section 10 prices both.

## 4. The container side

**4.1 One shim binary, multicall.** `minosa`, static, on `PATH` in the image, with subcommands. It opens the socket, sends one request, pumps bytes, exits with the broker's status. It holds no policy, no credential and no state; replacing it with a hostile program changes nothing, because it never had authority. The tool names live in the container; the tool definitions live in the broker; only the broker decides.

**4.2 Six operations, and no seventh.**

| Operation | Behaviour |
|-|-|
| `minosa messages [--since N]` | drain the broker's queue; backfill from history on a gap |
| `minosa say --file F` / `--stdin` | post to the room, with an optional payload |
| `minosa submit --subject S --file F` | propose something needing a decision; returns an id |
| `minosa await ID --timeout T` | block until approved, rejected or timed out |
| `minosa progress N` | advance the durable read marker |
| `minosa status` | the grant's room, window, expiry and connection state |

A payload is JSON and is read from a file or from stdin. **The shim never requires the model to quote JSON on a command line**; that is the single most reliable way to produce a malformed request.

**4.3 MCP is a second front end, not the first.** `minosa mcp` serves the same six operations over stdio for agents that speak MCP. It is built after the shim because `exec` is the interface every agent CLI speaks and MCP is not: registering an MCP server differs per CLI in its config format, its launch flag and its permission model, so MCP does not remove per-agent code, it relocates it. The shim registers once, in the image, for every agent at once. MCP's advantage is that the model is handed the argument names rather than told them in the prompt; that is worth having and it is worth having second.

**4.4 The container runs as a non-root uid, and the socket's mode admits only it.** The socket is the credential in this design, so the file mode is the gate on it. A root container ignores a file mode, which makes the uid mapping a prerequisite rather than an improvement. The socket is mounted at a path that is not the work mount, because the agent writes the work mount.

## 5. The three lanes between the broker and the agent

They differ in direction and in who initiates, not in importance.

| Lane | Direction | Carries | Notes |
|-|-|-|-|
| stdout, stream-json | agent to broker | the whole turn: text, tool trace, cost | dominant by volume; the broker's only view of the work |
| stdin, stream-json | broker to agent | a pushed message, queued until the turn ends | the only lane that delivers without the agent's cooperation |
| the socket | agent to broker, request and reply | the six operations | the only lane on which the agent can ask |

Each is required and none substitutes for another. An interrupt is not a lane: it is a separate act, and it must be issued before a correction is pushed, because an interrupt may discard what is queued.

**The room gets the turn, whether or not the agent cooperates.** The broker posts each turn's final assistant message to the room under the worker's name. `say` remains, for speaking before a turn ends and for attaching a payload. Relay is the floor and `say` is the choice; neither subsumes the other. Without relay, an agent can work for twenty minutes and post nothing, and the room stops being the record. Without `say`, a long run is silent until it finishes.

The trace and the cost go to the run record, not the room. The room carries whole replies, which is what a person reads and what a cursor can mark.

## 6. Authority, in four layers

Ordered by what each survives.

| Layer | Enforced by | Survives |
|-|-|-|
| 1. Six operations | the broker's implementation | anything the agent says; needs no server feature |
| 2. The grant: one room, a read window, a capability set, expiry with the run | `minosd` | a subverted broker only if the grant is narrow |
| 3. Rate and quota | `minosd`, plus a local limit in the broker for a fast refusal | a cooperating agent and an uncooperative one alike |
| 4. Container control: build, start, kill | `pma`, to the container engine directly | `minosd` being down, the broker being wedged, the model being unreachable |

Layer 1 is available before any server change. Layer 4 never depends on layers 1 to 3, which is what makes it the stop of last resort.

**The read window matters as much as the room.** A workflow runs in stages, each stage a fresh grant over the same room, the predecessor's grant revoked before the successor's is minted. What a stage inherits is one field: its own start, so artifacts only; the predecessor's handoff message; or the whole room. Default to the first. A validating stage that reads the stage it validates is steerable by it.

**The window filters agent-authored messages alone.** The developer's standing instruction and the server's events cross every stage boundary. A workflow that drops the developer's correction at a stage boundary is a defect, not an isolation policy.

## 7. Refusals, decisions and payloads

**7.1 Every refusal is a readable string.** A model changes course on a sentence and needs no error table. This is why a rate limit is self-enforcing against a cooperating agent and still enforced against one that is not.

**7.2 A refusal names the rule that would permit it.** The worker then proposes a concrete policy line rather than guessing, and the developer answers a line rather than prose. A submission that times out leaves that rule text behind as its residue.

**7.3 A decision carries a scope.** Answering a submission takes `once`, `run` or `always`, plus how specifically it matches. `once` is the default and is today's behaviour. `run` expires with the grant. `always` appends a rule to `pma`'s routing policy, where the next occurrence is answered deterministically with no model and no person in the path. Without this, the developer answers the same question once per dispatch, across every repository, forever, and a submission deadline converts the backlog into failed runs.

The objection is real and the mitigation is stated: a rule written in response to an untrusted principal's request is a rule that principal shaped. So the developer authors the rule text rather than accepting the worker's phrasing; the policy artifact is reviewed per revision; and `always` is refused for any verb whose arguments cannot be constrained.

**7.4 `pma` validates the payload; the server does not.** The payload passes through `minosd` unread, which is correct -- it is dispatcher policy and does not belong on the wire -- and it means `minosd` cannot check it. `pma` parses agent-authored JSON to read a permission request, its cost, a path and a command, and `pma` holds mint, ship and push. The validator runs at that boundary, before the payload reaches a decision, and rejects with a readable string.

**7.5 One verb taxonomy, used twice.** Each payload verb carries two properties: whether it interrupts the turn rather than waiting for it to end, and whether it is eligible for a standing decision. One artifact in `pma` answers both questions. Without the first, a harness interrupts on every message, which makes a status note destructive, or on none, which makes correction impossible.

## 8. What the agent is allowed to read

Knowledge accumulates -- standing instructions, templates, skills, captured lessons -- and it reduces how often an agent must ask. It also creates write paths into future agents' context, and those are outside the room, so the read window does not reach them.

**Classify every source by its least-trusted writer.** Developer-written sources are unrestricted. Agent-written sources are readable only within the room and window that produced them, unless approved, and approval is what publishes them -- under their author's name, unedited. **A validating stage reads no agent-written source at all.** That last rule is one flag on a stage definition and it needs no server change.

## 9. Files, and what does not move through the bus

Artifacts move on the bind mount. A stage writes; `pma` reads; the container cannot reach the reader. **The worktree is mounted at its host path**, so absolute paths in a review, a patch or a stack trace resolve for the developer, and files the agent writes are owned by the developer rather than by root.

Nothing else is added to the bus. No file transfer, no webhooks -- an outbound request from the server to an address an agent influenced crosses the boundary this design exists to hold -- no federation, no agent discovery, and no TLS on a link that cannot leave the host.

## 10. Costs, honestly

| Cost | Mitigation | Residual |
|-|-|-|
| A host process per run that must not die | It is a child of dispatch; its death ends the run cleanly, and the kill path does not go through it | One more thing to supervise |
| The socket is the credential, so its file mode is the only gate | One socket per run, non-root uid, mounted outside the work mount | A root container defeats it; do not run one |
| The container's interface is dispatcher-specific | The shim is small and the broker is the client `pma` needs anyway | A future consumer wanting to join a room from inside a box needs a real client |
| Relay of the final message may be verbose or ill-formed for some agents | The filter lives in the per-agent adapter | One adapter change per agent CLI |
| The shim is callable by anything in the container, including repository code the worker executes | Non-root uid and socket mode; the grant bounds the container, not the process | An injected `Makefile` can speak in the room as the worker; the room is the audit |

**What would change the recommendation.** If every agent CLI actually dispatched to serves MCP, and the prompt preamble proves unreliable at getting the model to use the shim, MCP should move ahead of the shim. If broker supervision proves more expensive than a listener in the server, the in-container client becomes the better trade. Neither is knowable before the first runs; the order below is chosen so that either reversal is cheap.

## 11. What was rejected

**A full minos client inside the container, reaching the server over a bind-mounted socket.** It needs a new listener in the server; it exposes the server's whole surface -- file write, settings -- to the container until a capability system lands; it implements the wire a second time while `pma` still needs its own; and it forces the agent's process ownership into the container, dragging every per-agent argument builder and stream parser across the boundary with it. Its one real advantage is that the container becomes a first-class client, which nothing currently planned requires.

**A second network listener the container dials.** A listener on the container network is reachable by every container on it, and a transport with no caller identity can only ever make rules about the verb, never about who asked.

**No socket at all, with everything injected into the turn.** The agent then cannot ask: no blocking wait on a decision, no progress marker on its own schedule, no refusal it can read without ending its turn. The ability to ask is the whole reason a route into the container exists.

**A queue or a mailbox instead of the chat bus.** The bus gives one ordering and one archive across the fleet, fan-in to the supervisor without a receiver per worker, replay after a disconnect, a durable read cursor, and a decision flow that is already built. The decisive one is that the human end is a client that already exists: the developer must be able to reach a worker directly while the supervising model is down, and that is a reading and typing requirement before it is a protocol requirement. What the design then uses is a narrow slice of chat -- an append-only ordered log per workflow, a read window per principal, a cursor, and submissions. Occupancy, presence, rosters, typing and transient rooms are all refused.

## 12. Build order

Each phase is usable before the next exists.

**Phase 0 -- a worker in a room, with no server change.** The broker dials the existing listener with an ordinary session. Containment already holds at layer 1.

1. The broker: dial, the six operations, the queue, the cursor and gap repair, the reader split so a blocked model call never stops draining the connection.
2. `minosa`: the multicall shim and the socket protocol. One socket per run, outside the work mount.
3. `sanduk`: mount the worktree at its host path, map the host uid and gid at runtime and drop to it, and expose the run with its stdio on pipes.
4. The broker takes the pipes: push on stdin, relay the turn's final message to the room, interrupt before push.

**Phase 1 -- the server work that makes isolation a rule rather than a convention.**

5. Grants: mint and revoke, carrying one room, a read window, a capability set and an expiry that tracks the run. Revocation closes every connection on the grant, with a reason the client can tell from an ordinary drop -- otherwise a client that is supposed to stop reconnects instead.
6. Author kind, readable by a grant holder. A worker that cannot tell the developer's instruction from the supervisor's cannot obey the right one when they disagree.
7. A submission deadline with a third terminal state, and an answer payload.
8. A send rate and a per-grant quota.

**Phase 2 -- policy, in `pma`.**

9. The verb taxonomy: interrupts, and standing-eligible.
10. Payload validation at the boundary.
11. Decision scope, and the rule file that `always` writes to.
12. Knowledge sources classified by least-trusted writer; validating stages read none.

**Phase 3 -- reach.**

13. `minosa mcp` over the same socket.
14. Per-agent MCP registration in the dispatch adapters.

Phase 0 ships a worker that talks and asks. Phase 1 is what makes the result defensible. Phase 2 is what stops the developer answering the same question once per dispatch. Phase 3 is a convenience for the agents that prefer it.

## 13. Open questions the plan must answer

1. Does a run proceed when the server is unreachable at start -- fail the run, or run with no conversation? Stopping works either way.
2. Whose failure is a submission that nobody answered before its deadline: the worker's, or the person's?
3. Which record is authoritative in a dispute -- the relay log for what reached a model, the archive for what was decided -- and does the archive's retention cover at least the relay's?
4. Can `pma` recover a workflow room after losing its own index? If it founds the rooms and its state is the only map, its failure hides the conversation in exactly the case the conversation exists for.
5. Nothing deletes a completed workflow room. A finished workflow is an unambiguous trigger; who is entitled to act on it is not decided.
