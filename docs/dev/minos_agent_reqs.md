# minos agent requirements

What `minosd` needs before `pma` may run contained agents on it. 2026-09-19. Nothing here is built.

[agents.md](agents.md) tests the general hypothesis: an agent is a user. This document specifies one deployment and settles what that deployment needs.

## The deployment

Bob maintains many repositories. [pma](https://github.com/shakfu/pma) scans them, ranks the outstanding tasks, and dispatches each to an agent in a disposable container ([sanduk](https://github.com/shakfu/sanduk) `--mode sealed`). `minosd` carries the conversation between them. Bob reads that conversation and intervenes when he must.

Four principals, one server:

| Principal | Where | Trust | Holds |
|-|-|-|-|
| `pma`, the tool | host | trusted; it is code | an admin session, container control, git and `gh` |
| `pma-agent` | a model pair, currently claude/opus | untrusted; reads worker reports | a grant over every task room |
| worker | container | untrusted; reads repository content | a grant over one task room |
| Bob | host | decides what `pma-agent` will not | his own account |

`pma` the tool assigns the `pma-agent` role to a model. That model is not the tool. Every item below depends on the separation.

### Topology

**`control`**, a channel restricted to group `workers`. `pma` and Bob publish; workers and `pma-agent` subscribe. A channel is read-only to its audience (chat-concepts 5), so no worker can broadcast to the fleet. `pma-agent` may submit to it, which puts a fleet-wide instruction in front of Bob before it reaches anyone.

**`task/<id>`**, a room per task. Members: that task's worker, `pma-agent`, Bob, and colleagues he admits. A task outlives its runs, and `pma review --rework` runs the agent again in the same worktree, so the room is scoped to the task rather than the run. The second run reads what the first was told.

A room per task across 95 scanned repositories is a flat, unbounded list. [spaces.md](spaces.md) proposes the object that groups them: one space per project, carrying the archival period its task rooms inherit. Access stays with a group per project, invited to each room. A space decides no admission, so nothing a worker can reach widens by being filed.

No room holds two workers. That is a grant policy, not a protocol limit; see [Agent to agent](#agent-to-agent).

### The three conversations

| Conversation | Frequency | Must work when |
|-|-|-|
| `pma-agent` and a worker | every run | normal |
| Bob and `pma-agent` | routine | normal |
| Bob and a worker, direct | rare | `pma-agent` is down or wrong |

The third is why a worker is a principal on the bus rather than something `pma` relays for. A path that exists for `pma-agent` failing cannot run through `pma-agent`.

Its two cases differ in what `pma-agent` must learn. When `pma-agent` is down, it cannot know of the intervention until it returns. When it is up and wrong about one worker, it must see the correction at once. Both are answered by putting Bob's message in the task room: `pma-agent` is a member, so a live one reads it immediately and a returning one replays it from its cursor.

Bob intervening on most runs means `pma-agent` is not doing its job. Record interventions per run beside the rest of the run row. That rate is the signal that decides whether the design works.

## Threat direction

agents.md puts the disclosure risk outbound through the model provider, where the server cannot see it. Here the container is sealed, so `minosd` is the only route out. Three consequences:

1. **`minosd` is the containment boundary.** Bob reaches it and the worker reaches it, so it sits on both networks by construction. `sealed` stops meaning "nothing leaves" and starts meaning "nothing leaves except what `minosd` accepts".

2. **`pma-agent` is not trusted.** Its input is worker reports, which carry repository content. The path runs: repository, worker, report, `pma-agent`, then dispatch, approve or ship. Every authority in the system sits at the end of that path. agents.md 3.6 gives the rule -- an agent's authority must not exceed that of the least-trusted principal who can write to it -- and `pma-agent` is subject to it like any other agent.

3. **Every byte channel is an exfiltration channel.** A worker holds a repository that must not leave. A chat body is capped at 64 KiB and the message count is not, so the cap alone bounds nothing. `/vfs` accepts 100 MiB per write.

## What already fits

Do not rebuild these.

- **Terminal decisions.** A decided submission is refused with `That submission has been decided` (wire-contract 9). Exactly-once answering, already specified.

- **Replay is the correctness property, not a convenience.** `sync` carries the caller's own submissions, and `history` with `lastSeq` closes a gap (wire-contract 8). When `pma-agent` loses connectivity while Bob stops three workers, `pma-agent` reconnects and reads those stops in order before it decides anything. A webhook or a relay queue gives this only if the receiver builds a durable inbox.

- **A durable progress marker.** The server-side `read` cursor never moves backwards (wire-contract 6), so "handled up to 41" survives a redeploy.

- **Push on decision.** The `submission` push reaches the author on a decision, so an agent waits rather than polls.

- **A rejection carries a reason.** `submission.reject` takes `comment`, which is `pma review --rework "feedback"`.

- **A channel is read-only to its audience.** Broadcast to the fleet with no path for a worker to forge one (chat-concepts 5).

- **Submissions gate fleet-wide acts.** `pma-agent` submits, Bob approves, and approval publishes the text unchanged under its author's name. A moderator may not edit (chat-concepts 5), so attribution survives.

- **A body ceiling.** 64 KiB per body under a 1 MiB frame (agents.md 3.8).

Request and reply is about 80 percent built. Submissions are the right primitive; what is missing is an answer payload, a deadline and a rate limit.

## P0 -- without these, do not connect a contained agent

### 1. A grant, scoped and non-refreshable by its holder

`POST /login {username, password}` yielding a 12-hour cookie, refreshed per request to a 7-day ceiling, is wrong for this caller. It puts a reusable credential in an untrusted box and outlives the run by orders of magnitude.

Required: `pma` mints a credential per task, carrying one room, no admin group, and no capability it was not given. Three properties:

- **Non-refreshable by the holder.** A worker cannot extend its own reach. This is the property that matters.
- **Re-mintable by `pma`.** A rework issues a new grant over the same room. Continuation is always decided outside the container.
- **Revocable.** Revocation closes every websocket on the grant, as `/logout` already does for a session.

Expiry tracks the task with an outer bound, not the run. A multi-run task keeps one room and one history; each run gets a fresh grant over it.

Shape, respecting the route freeze (wire-contract 3): mint as a socket operation from `pma`'s admin session, not a new HTTP route. Sections 9 to 11 set the precedent that operations extend and routes do not. The agent presents the grant as an `Authorization` header on the upgrade and on HTTP, which agents.md 3.10 names as the available shape. A header is not a route.

A bearer token also drops what a cookie carries for nobody's benefit here: `SameSite`, the CSP `connect-src` dance, and a cookie jar on disk inside the container.

The existing rule that a session keeps the rights it was issued with (wire-contract 2: read the role from the session, not the user table) extends to a grant unchanged. That is what makes this safe to build.

### 2. Capabilities, denied by default

Admin is binary today (agents.md 3.6), so "not an admin" is the only gradation available. A non-admin account can still write files, enumerate people, open rooms and change settings.

Required: a grant carries an explicit capability set, and anything absent is refused with the existing error shape. Two shapes are needed.

| | worker | `pma-agent` |
|-|-|-|
| read | one task room, `control` | every task room, `control` |
| send | that room | every task room |
| submit | that room | that room and `control` |
| acknowledge a decision | yes | yes |
| everything else | refused | refused |

`pma-agent` differs from a worker in room scope. It is not a separate class, and in particular it does not found rooms, approve submissions or mint grants. Those are the tool's.

### 3. `/vfs` refused for a grant

`home:` is writable per user and a `writefile` body may be 100 MiB (wire-contract 3, 4). One session covers chat and all of `/vfs`, so granting chat grants file transfer. A worker writes the repository to `home:/` and someone on the host reads it out.

Detection exists and prevention does not: a successful VFS mutation posts an event to `system` (wire-contract 7), which makes exfiltration noisy rather than impossible.

Required: every `/vfs` method answers 403 for a grant. Not a size limit -- the whole namespace. A worker that must hand over a file writes it to the bind mount, where `pma` reads it and the container cannot reach the reader.

### 4. `/settings` refused for a grant

`POST /settings` replaces `home:/.osjs/settings.json` wholesale with any JSON object under the 1 MiB body cap. It is a second file channel.

### 5. No roster or topology disclosure

A grant must not learn what it cannot reach. `sync` returns users, rooms and groups; for a grant it returns its own rooms and nothing else. The rationale is reconnaissance, not privacy: a worker's output is read by Bob, and a list of real accounts is material for a convincing forgery.

### 6. Separable listeners

The agent-facing address and the human-facing address must be configurable independently, so administrative operations are unreachable from the internal network at any authority level. Grant minting is reachable only from the host side.

This is the one mitigation for `minosd` being dual-homed. Without it, item 1's minting operation is reachable by the principals it exists to constrain -- including `pma-agent`, which runs on the host and would otherwise be able to mint itself a wider grant than it was given.

### 7. A per-grant send rate and quota

agents.md 3.5 identifies the absent rate limit and names a per-account send rate as the only server-enforced guard. It was hypothetical while agents did not converse. Here `pma-agent` and a worker are two models answering each other by design, so an unbounded loop is the default failure, not an edge case.

The two cheap guards in 3.5 -- never reply to an author whose `kind` is `agent`, and reply only when addressed -- are agent-side policy running on untrusted input. Neither holds.

Required: messages per second and messages per grant, enforced by the server. A quota also bounds item 3 of the threat direction: 64 KiB per body times a capped message count is a bounded channel, and without the cap it is not.

### 8. A decision is an operation, not a convention

A worker lacking the capability must be unable to emit anything a reader takes for a decision. This follows from item 2 and is worth a test of its own: a grant attempting `submission.approve` is refused.

Forgery of an approval is the attack that matters, because "`pma-agent` approved this" is what Bob acts on.

### 9. Author kind on every message

agents.md 3.2 proposes `kind: "user" | "agent"` on the user object. Required here, with `author: "system"` staying the server's alone, which is already true and should stay stated.

Three kinds suffice. `pma` the tool speaks as `system`, because its output is server-side fact: a run started, a grant was minted, verify failed. `pma-agent` and every worker are `agent`. Bob and his colleagues are `user`.

A worker reading its own room sees instructions from a `user` and from an `agent`. When they disagree, the worker chooses. There is no capability-level answer to that, so the precedence rule -- Bob outranks `pma-agent` -- belongs in the worker's prompt, and `kind` is what makes it applicable.

## P1 -- what makes the channel work

### 10. An answer payload on a decision

Approval turns a submission into a channel message authored by the submitter (wire-contract 9), so today there is nowhere to put a reply. agents.md section 6 reaches the same conclusion: "A new operation, *answer a submission*, would be required."

Required: a decision carries a payload back to the author. The cheapest form is `comment` on approve as well as reject, already pushed to the author. A distinct `submission.answer` is cleaner if approval must keep meaning publication.

### 11. A control verb on a message

`body` is text. A worker's request has fields: what kind of permission, the projected cost, the path, the command. An instruction to a worker also has a verb: continue, interrupt, stop, abandon. `pma-agent` cannot apply policy to prose, and a harness cannot decide from prose whether to interrupt the turn in flight.

Required: a declared payload alongside the body. Keep the schema out of the wire contract -- the server passes it through unread -- because it is `pma` policy, not a wire shape.

Without this the harness must either interrupt on every message, which makes a status note destructive, or on none, which makes correction impossible.

### 12. A deadline, enforced by the server

A worker blocked on a decision nobody makes burns its run timeout and then dies with the work half done. The worker cannot be trusted to time itself out.

Required: a submission may carry a deadline. Past it the server decides it as `timed_out` and pushes that to the author, a third terminal state beside approved and rejected. A run then fails with a stated cause instead of a killed container.

### 13. A task and run identifier on every object

Two keys joining four records: `pma`'s task row and run row, `sanduk`'s report and relay log, and the minos archive. Without them, reconstructing what an agent did means matching timestamps across three clocks. The task id is what makes a room's history readable across reworks.

## P2 -- record

### 14. Retention at least as long as the relay's

A sealed run's audit trail is complete today because the relay sees every call. This channel ends that. The archive keeps the rest, so agent rooms need a retention period at least equal to the relay's log retention, or the two records cover different periods and neither is whole.

Decide which is authoritative for a dispute. Recommendation: the relay for what reached a model, the archive for what was decided.

### 15. Grant lifecycle on `system`

Minted, used, revoked, expired -- announced as events, in the pattern a VFS mutation already follows. An operator who cannot see a grant being issued cannot audit item 1.

### 16. Refuse a grant in a transient room

agents.md 3.1 recommends this for history disclosure. It holds here for a second reason: a transient room retains nothing and item 14 requires the opposite. The two cannot both apply to one space.

## P3 -- efficiency

### 17. One long socket per run

Not a connection per request. The push model in wire-contract 7 and 8 already supports it.

### 18. A harness in the image, not only a client

`go/internal/client` is `internal`, so out-of-tree code cannot import it; `go/conformance/wire.go` is the 581-line reference an out-of-tree agent reimplements against (agents.md 4.1). A container image already carries three flavours of base, none with Node or Python guaranteed, so this ships as a static binary.

It is more than a wire client. It owns the agent process: it starts it, feeds room messages to it as input, sends the agent's interrupt when a control verb says to, and posts its output back. See [Stopping a worker](#stopping-a-worker).

It must separate its reader from its blocking work. A connection's outbound queue holds 256 frames (`go/internal/socket/socket.go:42`) and a reader that stops to wait on a model call is disconnected (agents.md 3.9).

`pma` needs the same wire in Rust. Two implementations of the envelope unless the harness is also Rust.

### 19. Backpressure refuses rather than buffers

Past the item 7 limits, the server answers with an error the agent can read. A readable refusal is already the convention (agents.md 2), and an LLM changes course on one.

### 20. No TLS on the internal network

Say so explicitly, so nobody adds a certificate lifecycle to a link that cannot leave the host. It follows that the agent-facing listener must never be the one bound to a routable interface -- item 6.

## Stopping a worker

Stop has four layers. Only the first two are conversations.

| Layer | Mechanism | Depends on | Loses |
|-|-|-|-|
| 1. Interrupt | the harness sends the agent's own interrupt | `minosd`, the harness, an interruptible agent | nothing; the turn ends and the session stands |
| 2. Graceful stop | interrupt, a final instruction, then exit | the same | nothing |
| 3. Hard kill | `pma` stops the container | a terminal | the turn in flight, and the session unless it is on the bind mount |
| 4. Revoke | `pma` withdraws the grant; every socket on it closes | a terminal | the channel; the container may still run |

Layers 3 and 4 are `pma` the tool. They need no model, no `minosd`, and no cooperation from the worker. Build them first: they are the floor the rest stands on, and they are the only layers that work when `minosd` or the model provider is the thing that failed.

**Interruption is resumable, and it is an agent capability.** Claude Code ends the turn on SIGINT and leaves it unfinished on SIGTERM; a session resumes with `--resume <session-id>` or `--continue`, and the Agent SDK exposes `interrupt()` ([headless](https://code.claude.com/docs/en/headless)). Its `system/init` event carries a `capabilities` array naming values such as `interrupt_receipt_v1`, which is feature detection without comparing version strings. Other agents differ, and some cannot be interrupted at all.

`sanduk` already declares each agent's wire protocols on the handler and refuses an unsupported pairing before it builds anything (`sanduk/agent.py:142`). Interruptibility belongs in the same place. `pma` then knows at dispatch whether a task can be corrected or only killed, and can decline to dispatch an uninterruptible agent to a task it expects to correct.

**Resumption needs the session to survive the container.** Claude Code stores transcripts as `.jsonl` under a project directory on the machine. A disposable container takes them with it. Resumable interruption therefore requires the session store on the bind mount, which `sanduk` does not do today.

**An interrupt is not prompt.** A worker mid-inference is not draining its socket. A broadcast on `control` is read when the turn ends, which may be minutes. Layer 3 is the answer when that is too slow.

## Agent to agent

Two workers conversing is not ruled out. It is a grant policy.

- One worker per room is a star: `pma-agent` at the hub, workers at the leaves.
- Two workers in one room is one edge of a mesh.

Same mechanism, one grant apart. What the grant decides is trust rather than routing. agents.md 3.6 is transitive across a shared room, so two workers in one room means each is bounded by the other repository's content. Stated per room that is legible and reversible; stated as a protocol property it would be neither.

Build rooms and grants, scope them to one worker, and leave the scope configurable.

## Not required

- **Agent discovery.** `pma` starts every agent and knows what each one is for. A2A's AgentCards answer capability discovery across organisations. This deployment has one host and one dispatcher.

- **Federation.** AMP addresses agents as `agent@tenant.provider` to route across providers. Every principal here is on one host.

- **Webhooks.** Rejected in agents.md 5.1, and worse here: an outbound request from `minosd` to an address an agent influenced crosses the boundary item 1 exists to hold.

- **In-process agents.** Rejected in agents.md 5.1. Also moot: the worker is in a container by requirement.

- **Streaming.** Whole replies, per agents.md 3.7. A request and a decision are not a token stream.

- **Occupancy.** Agents do not enter rooms (agents.md 3.4). Writing needs access, not occupancy.

## Conformance

`go/conformance/` talks HTTP and a websocket and imports no server code, enforced by `isolation_test.go`. Every item above is testable there, and the denials are the tests that matter:

- a grant gets 403 at `/vfs` and 403 at `/settings`
- a grant is refused on `submission.approve`
- a grant's `sync` returns its own rooms and no roster
- a worker's grant is refused on a room it was not given
- a worker is refused publishing to `control`
- a grant past the send rate is refused, not buffered
- a grant is refused everything after its expiry, and after revocation

Sections 9 to 11 of the contract were each a conformance test first. This should be section 13, written the same way.

## Open questions

1. Does `minosd` run as a container `pma` starts, or as an operator's service `pma` finds? The second is cleaner and means `pma` must degrade to a bind-mount mailbox when it is absent.

2. Is a capability set (item 2) general, or a single `agent` role with a room list? General costs more now and avoids a second special case later.

3. Who is accountable for a grant: the admin whose session minted it, or the task it was minted for? agents.md open question 4 asks this of accounts; a grant makes it answerable, because a grant has one issuer and one purpose.

4. Does a timed-out submission (item 12) count against the worker's failures, or against the person who did not answer?

5. What deletes a task room? Founding rooms as an administrator keeps invitation an administrator's act, which is what stops a worker inviting a second worker into its own room -- and nothing then deletes the room, because chat-concepts open question 1 has not decided who may. The ad-hoc alternative deletes on its last grant and gives up that control. [spaces.md](spaces.md) 7 states the fork; it bounds what accumulates and does not resolve it.

6. Can `pma` recover a task room that its own state lost? If `pma` founds rooms and its database is the only index, a `pma` failure leaves Bob unable to find the room he needs in exactly the case the room exists for.
