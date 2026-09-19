# Agent design

How AI agents use minos. 2026-09-19. Nothing here is built.

This supersedes `agents.md`, `minos_agent_reqs.md` and `spaces.md`. It states decisions and the reasoning that survives them; the option enumeration those documents carried is gone. The model it changes is [chat-concepts.md](chat-concepts.md). The wire it changes is [wire-contract.md](../wire-contract.md). How to build it is `architecture.md`, which does not exist yet and should not until this is reviewed.

## 1. The problem

The developer maintains many open-source repositories. Three tools exist and two joins do not.

| | Built | Missing |
|-|-|-|
| [pma](https://github.com/shakfu/pma) | scan, matrix, dispatch, review, ship, sync, TUI. 95 repositories in about 15 seconds | runs agents as host processes in a git worktree; its own design notes that "isolation would need a container around both" |
| [sanduk](https://github.com/shakfu/sanduk) | container per run, bind mount, report file, container deleted. `sealed` creates the network `--internal`, so a host-side relay is the only reachable address | no control channel: a task goes in at the start and a report comes out at the end |
| [minos](https://github.com/shakfu/minos)  | rooms, groups, channels, submissions, a frozen wire contract, a conformance suite | everything below |

The target: `pma` ranks outstanding tasks, dispatches each to an agent, using `sanduk`, in a sealed container, and `minosd` carries the conversation. The developer reads it and intervenes when they must.

## 2. Principals and trust

| Principal | Where | Trust | Holds |
|-|-|-|-|
| `pma`, the tool | host | trusted; it is code | an admin session, container control, git and `gh` |
| `pma-agent` | a model pair, currently claude/opus | untrusted; reads worker output and repository text | a grant over the workflow rooms it supervises |
| worker | container | untrusted; reads repository content | a grant over one workflow room, windowed |
| developer | host | decides what `pma-agent` will not | their own account |

`pma` the tool assigns the `pma-agent` role to a model. That model is not the tool. Most of what follows depends on the separation.

**What `pma-agent` decides.** Not triage and not routing. `rank.rs` places tasks in the Eisenhower matrix and `route.rs` maps a task to an agent, a model, an approval mode and an escalation, both deterministically and both built. `route.rs` states the commitment: "the policy is an artifact, not a judgment per task. One decision per revision, applied deterministically to every dispatch." `pma-agent` sits above that policy rather than inside it.

| Decision | Scope | Authority needed |
|-|-|-|
| which workflow a situation gets | per trigger | a submission to `pma` |
| inputs the deterministic rules cannot read -- priority implied by prose, an undeclared dependency | per triage pass | a submission to `pma` |
| supervision: redirect, rework, escalate | per stage that declares it | `send` in that workflow's room |
| a revision of the routing policy itself | per revision | a submission to `pma` |

A workflow that declares no supervision runs without `pma-agent` entirely: `pma` triggers it, `route.rs` matches each stage, and the developer reads the result. That path carries no model in the dispatch decision at all.

## 3. The rule

**An agent's authority must not exceed that of the least-trusted principal who can write to it.**

Its consequences here:

- No agent account is in `config.Admins`. Admin is binary today (`go/internal/config`), so there is no partial grant to fall back on. Section 7 replaces it with a capability set.

- `pma-agent` is subject to the rule. Its input is worker output and repository text, both attacker-influenced. The path runs: repository, worker, report, `pma-agent`, then dispatch, approve or ship. Every authority in the system sits at the end of that path.

- `pma-agent` proposes; `pma` the tool executes. A proposal is a submission, which needs no new mechanism.

- **The rule is transitive.** If agent A may write to agent B, then B is bounded by A's inputs as well as its own. A shared room is therefore a trust decision, not a routing one.

## 4. Two threat directions

The deployment has two, and they are answered separately.

**Outbound through the model provider.** A newly invited participant sees the room's history (chat-concepts 2.3). For a person that disclosure stays on the server. For an agent backed by a hosted model, the history becomes context sent to the provider. Inviting an agent to a five-year-old room sends five years of it off the server, and the server cannot detect it. Transient rooms make it worse: the model promises "it retains nothing", and an agent that forwards messages breaks that promise silently.

**Outbound through `minosd`.** The container is sealed, so `minosd` is the only route out. It is on both networks by construction, which means `sealed` stops meaning "nothing leaves" and starts meaning "nothing leaves except what `minosd` accepts". A worker holds a repository that must not leave. A chat body is capped at 64 KiB and the message count is not, so the cap alone bounds nothing. `/vfs` accepts 100 MiB per write.

## 5. Topology

**`control`**, a channel restricted to group `workers`. `pma` and the developer publish; workers and `pma-agent` subscribe. A channel is read-only to its audience (chat-concepts 5), so no worker can broadcast to the fleet. `pma-agent` may submit to it, which puts a fleet-wide instruction in front of the developer before it reaches anyone.

**`workflow/<id>`**, a room per workflow instance, founded by `pma` the tool. Members: the stages' workers, `pma-agent` where the workflow declares supervision, the developer, and colleagues they admit. The room's scope is the workflow's scope -- a project, a task, a document -- so room grain is policy `pma` sets per workflow, not a constant this design fixes. D5.

**A space per project**, holding that project's workflow rooms. A space carries an archival period its rooms inherit and decides no admission. Section 6.

**A group per project**, invited to each workflow room. Assigning a colleague to `proj-cynn` admits them to every room that named it, including rooms founded later, because a grant tracks the group (chat-concepts 2.3).

No room holds two concurrent workers. That is a grant policy, not a protocol limit: see D10.

### Workflows

A workflow is a sequence of stages. Each stage is an agent and model pair with an approval mode, consuming artifacts and producing them. It is `pma` policy: minos does not know what a workflow is and carries only its id. `route.rs` already holds the per-stage half in `Route {agent, model, approval, escalate}`; a workflow is a sequence of those with artifact edges, which `pma` does not have yet.

`REVIEW-FIX-TOP-FINDINGS`, project scope, as a worked example:

| Stage | Reads | Writes |
|-|-|-|
| review | the repository | `REVIEW.md` |
| validate | `REVIEW.md`, and the review's handoff message | `TODO.md`, ordered by priority |
| fix | `TODO.md`, critical and high only | a patch |

Three stages, one room, three grants, three containers. Each stage's grant is revoked before the next is minted, so no two stages hold a channel at once, and a stage cannot keep writing past its own handoff.

The validator exists to check the reviewer, so it must not be steerable by it. What it inherits is D19; that it never reads the reviewer's reasoning is D15 and D20 together.

### The three conversations

| Conversation | Frequency | Must work when |
|-|-|-|
| `pma-agent` and a worker | every run | normal |
| the developer and `pma-agent` | routine | normal |
| the developer and a worker, direct | rare | `pma-agent` is down or wrong |

The third is why a worker is a principal on the bus rather than something `pma` relays for. A path that exists for `pma-agent` failing cannot run through `pma-agent`.

Its two cases differ in what `pma-agent` must learn. When `pma-agent` is down it cannot know of the intervention until it returns. When it is up and wrong about one worker it must see the correction at once. Both are answered by putting the developer's message in the workflow room: `pma-agent` is a member, so a live one reads it immediately and a returning one replays it from its cursor.

A developer intervening on most runs means `pma-agent` is not doing its job. Record interventions per run beside the rest of the run row. That rate decides whether the design works.

## 6. Decisions

The review surface. Each states what was chosen, and what was rejected where a reasonable alternative existed.

**D1. Every message runs through minos. Files and container control do not.**

| What moves | Via minos | How |
|-|-|-|
| `pma-agent` instructs a worker | yes | the workflow's room |
| developer instructs `pma-agent` | yes | the workflow's room |
| developer instructs a worker | yes | the workflow's room |
| one instruction to every worker | yes | the `control` channel |
| a worker's report or a file it produced | no | the bind mount `sanduk` already provides |
| killing a run | no | `pma` tells the container engine |

Everyone in a workflow's room reads everything written in it, subject to their grant's window (D19). That is what makes a correction visible to `pma-agent` without a second delivery path, and what lets the developer reach a worker while `pma-agent` is down.

The two `no` rows are deliberate. A file channel through minos would be `/vfs`, which section 8.3 refuses outright. A stop that needs minos to be up cannot stop anything when minos is what failed (section 9).

**D2. `pma` the tool and `pma-agent` are separate principals.** The alternative is a model holding mint, ship and push authority with attacker-influenced text as its input.

**D3. `pma-agent` holds a grant, not an admin session.** It does not found rooms, approve submissions or mint grants. Those are the tool's.

**D4. A worker holds a grant over one workflow room and `control`.** The room grant is windowed by `since` (D19), so the room it may write to is not necessarily the room it may read whole.

**D5. A room is per workflow instance, and the workflow declares its scope.** Rejected: a fixed grain. A project-scoped workflow gets one room per project, a task-scoped one a room per task, and a workflow with no conversation gets no room. Both grains were argued as the single answer in review; neither is. A rework reuses the worktree, and the second run reading what the first was told is why the room is not per run. Room count tracks workflows dispatched, not the backlog, and a completed workflow is the deletion trigger section 11 lacks.

**D6. `control` is a channel, not a room.** Rejected: a room holding every worker. A room is writable by its participants, so any worker could broadcast to the fleet, and a compromised one could stop it.

**D7. A space groups rooms and decides no admission.** Rejected: access inherited down a space to its rooms. chat-concepts 2.3 refuses a second admission rule because "admission would then be decided by two rules that can disagree, and every question about who is in a room would have to ask both".

**D8. Access is a group per project, invited per room.** This is the existing mechanism and it composes with D7 without touching it. The cost is one `invite` at room creation, which `pma` does anyway.

**D9. Workflow rooms are admin-founded.** Only an administrator may invite to an admin-founded room (wire-contract 7), which is what stops a worker inviting a second worker into its own room. Rejected: ad-hoc rooms, where any participant may invite. The cost of D9 is that nothing deletes the room, because chat-concepts open question 1 has not decided who may. Section 11.

**D10. No room holds two concurrent workers.** Sequential stages of one workflow share a room; what each inherits is D19. Concurrency is the thing refused, because the rule in section 3 is transitive and bites hardest when it is a loop: two live workers in one room means each is bounded by the other's inputs and can adapt to the other's replies. A stopped predecessor cannot adapt. Worker-to-worker conversation is not otherwise ruled out; it is one grant away. Stated per room, that is reversible.

**D11. Three author kinds.** `user`, `agent`, and `system` for the server. `pma` the tool speaks as `system`, because its output is server-side fact: a run started, a grant was minted, verify failed. Two readers need this. A person must not take an agent's text for a decision, and a worker must be able to tell the developer's instruction from `pma-agent`'s when they disagree.

**D12. A server-enforced send rate is required, not optional.** Two cheaper guards exist -- never reply to an author whose kind is `agent`, and reply only when addressed -- and both are agent-side policy running on attacker-writable input. The first also fails outright here, where an agent answering an agent is the design.

**D13. Agents do not enter rooms.** Writing needs access, not occupancy: `Messaging.Send` checks `requireAccess` and nothing else (`go/internal/messaging/messaging.go:379`). An agent that entered would churn a `room` push per switch, and would keep a transient room alive forever by never leaving.

**D14. Agents are refused in transient rooms.** Two reasons: a transient room promises to retain nothing and a hosted model breaks that promise undetectably; and retention (section 10) requires the opposite of a space that retains nothing.

**D15. Whole replies, not streamed.** minos has no in-place edit and no typing indicator, both excluded by name (chat-concepts 6). A chunk per message would consume a `seq` each and fill read cursors with fragments.

**D16. Stop has four layers, and two of them are not conversations.** Section 8.

**D17. Grants are non-refreshable by their holder, re-mintable by `pma`, and revocable.** A worker cannot extend its own reach; continuation is always decided outside the container.

**D18. A structured payload passes through the server unread.** The schema is `pma` policy, not a wire shape.

**D19. What a stage inherits from its predecessor is a workflow property, with three settings.** One grant field, `since`, expresses all three: the stage's own start, so artifacts alone; the predecessor's handoff message, so artifacts and one deliberate framing message; or null, the whole room. Default to the first. Which fits depends on the agent and model pair and on the kind of task, which is why it is policy rather than a rule. Rejected: one isolation rule for every workflow.

The framing setting is the interesting one, and it is weaker than a shared room by more than one message. A handoff is open loop: the predecessor is stopped and its grant revoked before the successor starts, so it cannot adapt to what the successor says. It can still lie once, in 64 KiB, under its own name, in a message the developer reads.

**D20. Artifacts are transport; the room is the record.** Stage output moves on the bind mount, which D1 already requires of files. The room carries what the developer watches and intervenes in. The room's audience and a grant's window are therefore different things: one log, one window per stage. That is what lets a stage read nothing of its predecessor's conversation while the developer reads all of it, without splitting the workflow across rooms.

**D21. `user` and `system` messages cross every stage boundary. `since` filters `agent` authors alone.** A workflow that drops the developer's standing instruction at a stage boundary is a bug, not an isolation policy. Two costs: `since` is two rules rather than one, and a grant must be able to resolve an author to a kind, which 8.3 denies today. See 8.5.

## 7. Model changes

Against chat-concepts. Two.

### 7.1 `kind` on the user object

`sync.users` is `{username, online}` (wire-contract 6), so participants cannot tell an agent from a person, and neither can another agent. Add `kind`, per D11. `author: "system"` is already reserved for events and stays the server's alone.

### 7.2 A space

The model has one object that bundles principals and none that bundles places. A group is the only entry in table 2.6 with no message log: it exists to be named where access is decided, and nothing else. A space is the symmetric object.

| | **Space** |
|-|-|
| Created by | chat admin |
| Contains | rooms and channels; not spaces |
| Who may admit | nobody |
| Identity | an opaque id with a name |
| Has a message log | no |
| Decides access | no |
| Carries | archival period, display order |
| Survives being empty | yes |
| A room belongs to | zero or one space |

**Retention inherits, and a room overrides it.** `archive.set` takes a `room` today (wire-contract 11). A space carries the same `{period, searchable}` pair, and a room with no period of its own uses its space's. This changes what a null period means: today "a room with no period keeps everything" (chat-concepts 4), and under a space it means "inherit, and keep everything if the space has none either". One rule read in one order.

**Retention is not deletion.** Archival removes messages from a live room and keeps them admin-readable. The room remains. A space bounds what its rooms hold and not how many there are. Section 11.

**Depth is one.** `projects > cynn > task/31` is two levels of container and this builds one. The top level is a front end's tab bar, not a model level: `system` is a channel and `projects` would be a container, and they are not the same kind of object. A second level would have to name a question that two flat attributes on a room cannot answer; kind-within-project is a second axis, not a deeper tree.

**A room belongs to one space.** `pma dispatch cynn:31` names a task in one project and gives the run a worktree of that project. A task that unblocks another repository still belongs to the one whose worktree it holds.

## 8. Wire changes

Against wire-contract. Every one is a socket operation or an additive field. HTTP routes are frozen (wire-contract 3), and sections 9 to 11 set the precedent that operations extend and routes do not.

### 8.1 A grant

`POST /login` yields a 12-hour cookie refreshed to a 7-day ceiling. That is wrong for a caller inside a box it controls: the credential is reusable and outlives the run by orders of magnitude.

`pma` mints a credential per stage from its admin session, carrying its room, its `since`, no admin group, and no capability it was not given.

Scope and expiry are separate axes, and an earlier draft conflated them. The room tracks the workflow; the expiry tracks the run. A grant expires with its stage's run timeout, bounded above by the workflow's own outer bound. A three-stage workflow mints three grants over one room and revokes each before minting the next (section 5), so a task open for weeks never implies a credential valid for weeks. Revocation closes every websocket on the grant, as `/logout` already does for a session.

The agent presents it as an `Authorization` header on the upgrade and on HTTP. A header is not a route. A bearer token also drops what a cookie carries for nobody's benefit here: `SameSite`, the CSP `connect-src` dance, and a cookie jar on disk inside the container.

The existing rule that a session keeps the rights it was issued with (wire-contract 2: read the role from the session, not the user table) extends to a grant unchanged. That is what makes this safe to build.

### 8.2 Capabilities, denied by default

A grant carries an explicit capability set and anything absent is refused with the existing error shape.

| | worker | `pma-agent` |
|-|-|-|
| read | its workflow room from its `since`; `control` | the workflow rooms it supervises, whole; `control` |
| send | that room | those rooms |
| submit | that room | that room and `control` |
| acknowledge a decision | yes | yes |
| everything else | refused | refused |

`pma-agent` differs from a worker in room scope and window alone. It is not a separate class.

### 8.3 Denials

Three are not capabilities but blanket refusals, because the surface behind each is too large to scope.

- **`/vfs` answers 403 for a grant.** `home:` is writable per user and a `writefile` body may be 100 MiB (wire-contract 3, 4). One session covers chat and all of `/vfs`, so granting chat grants file transfer. Not a size limit: the whole namespace. A worker that must hand over a file writes it to the bind mount, where `pma` reads it and the container cannot reach the reader. Detection exists without prevention today -- a VFS mutation posts an event to `system` (wire-contract 7) -- which makes exfiltration noisy rather than impossible.

- **`/settings` answers 403 for a grant.** `POST /settings` replaces `home:/.osjs/settings.json` wholesale with any JSON object under the 1 MiB cap. A second file channel.

- **`sync` returns no roster.** A grant gets its own rooms and nothing else. The rationale is reconnaissance, not privacy: a worker's output is read by the developer, and a list of real accounts is material for a convincing forgery.

### 8.4 Operations

| Op | Request fields | Reply |
|-|-|-|
| `grant.mint` | `principal`, `rooms`, `capabilities`, `expires` | a grant |
| `grant.revoke` | `grant` | `{ok: true}` |
| `space.create` | `name` | a space |
| `space.set` | `space`, `name?`, `period?`, `searchable?` | a space |
| `space.assign` | `room`, `space` or `null` | `{ok: true, room}` |
| `space.delete` | `space`, `rooms` | `{ok: true}` |
| `submission.answer` | `submission`, `payload` | `{ok: true}` |

`create` and `open` gain an optional `space`. The sync object gains `spaces`, a list of `{id, name, archive}`, limited to spaces holding something the caller may reach.

`space.delete` takes `rooms` as `detach` or `delete`. Build `detach` only, until chat-concepts open question 1 decides who may delete a persisted room.

`grant.mint` and `grant.revoke` are reachable from the host-facing listener alone. See 8.6.

### 8.5 Fields

- `kind` on the user object (7.1).

- `space` on a room and on a channel: an id or null (7.2).

- A deadline on a submission. Past it the server decides it `timed_out` and pushes that to the author, a third terminal state beside approved and rejected. An agent blocked on a decision nobody makes otherwise burns its run timeout and dies with the work half done, and it cannot be trusted to time itself out.

- A structured payload alongside `body`, passed through unread (D18). A worker's request has fields: what permission, the projected cost, the path, the command. An instruction to a worker has a verb: continue, interrupt, stop, abandon. Without it a harness must interrupt on every message, which makes a status note destructive, or on none, which makes correction impossible. This is the case chat-concepts open question 3 in [channels.md](channels.md) anticipates.

- A task id, a run id, a workflow id and a stage on every object. Keys joining four records: `pma`'s task and run rows, `sanduk`'s report and relay log, and the minos archive. Without them, reconstructing what an agent did means matching timestamps across three clocks. minos reads none of them (D20's corollary: it does not know what a workflow is).

- `since` on a grant: a seq, or null. `history` and `sync` refuse `agent`-authored messages before it (D19, D21). One field, four uses: stage isolation, D14's persisted-room case, the `group.assign` disclosure in section 11, and a newly invited agent that must not receive five years of room history.

- Author `kind` readable by a grant holder, either on the message or as the kinds of authors in the grant's own rooms on its `sync`. 7.1 puts `kind` on the user object while 8.3 gives a grant no roster, so the reader D11 exists to serve cannot resolve a name to a kind. D21 needs it as well.

### 8.6 Separable listeners

The agent-facing address and the human-facing address are configured independently, so administrative operations are unreachable from the internal network at any authority level.

This is the one mitigation for `minosd` being dual-homed. Without it, `grant.mint` is reachable by the principals it exists to constrain -- including `pma-agent`, which runs on the host and would otherwise mint itself a wider grant than it was given.

No TLS on the internal network. Say so explicitly, so nobody adds a certificate lifecycle to a link that cannot leave the host. It follows that the agent-facing listener must never be the one bound to a routable interface.

### 8.7 A rate and a quota

Messages per second and messages per grant, enforced by the server (D12). A quota also bounds section 4: 64 KiB per body times a capped message count is a bounded channel, and without the cap it is not.

Past the limit the server answers with an error the agent can read. A readable refusal is already the convention, and an LLM changes course on one. It refuses rather than buffers.

## 9. Stopping a worker

Four layers. Only the first two are conversations.

| Layer | Mechanism | Depends on | Loses |
|-|-|-|-|
| 1. Interrupt | the harness sends the agent's own interrupt | `minosd`, the harness, an interruptible agent | nothing; the turn ends and the session stands |
| 2. Graceful stop | interrupt, a final instruction, then exit | the same | nothing |
| 3. Hard kill | `pma` stops the container | a terminal | the turn in flight, and the session unless it is on the bind mount |
| 4. Revoke | `pma` withdraws the grant; every socket on it closes | a terminal | the channel; the container may still run |

Layers 3 and 4 are `pma` the tool. They need no model, no `minosd`, and no cooperation from the worker. Build them first: they are the only layers that work when `minosd` or the model provider is what failed.

**Interruption is resumable, and it is an agent capability.** Claude Code ends the turn on SIGINT and leaves it unfinished on SIGTERM; a session resumes with `--resume <session-id>` or `--continue`, and the Agent SDK exposes `interrupt()` ([headless](https://code.claude.com/docs/en/headless)). Its `system/init` event carries a `capabilities` array naming values such as `interrupt_receipt_v1`, which is feature detection without comparing version strings. Other agents differ and some cannot be interrupted at all.

`sanduk` already declares each agent's wire protocols on the handler and refuses an unsupported pairing before it builds anything (`sanduk/agent.py:142`). Interruptibility belongs in the same place. `pma` then knows at dispatch whether a task can be corrected or only killed.

**Resumption needs the session to survive the container.** Claude Code stores transcripts as `.jsonl` under a project directory on the machine. A disposable container takes them with it, so the session store must be on the bind mount. `sanduk` does not do this today.

**An interrupt is not prompt.** A worker mid-inference is not draining its socket, and a connection's outbound queue holds 256 frames (`go/internal/socket/socket.go:42`). A broadcast on `control` is read when the turn ends, which may be minutes. Layer 3 is the answer when that is too slow.

## 10. What already fits

Do not rebuild these.

- **Terminal decisions.** A decided submission is refused with `That submission has been decided` (wire-contract 9). Exactly-once answering, already specified.

- **Replay is a correctness property, not a convenience.** `sync` carries the caller's own submissions and `history` with `lastSeq` closes a gap (wire-contract 8). When `pma-agent` loses connectivity while the developer stops three workers, it reconnects and reads those stops in order before deciding anything. A webhook or a relay queue gives this only if the receiver builds a durable inbox.

- **A durable progress marker.** The server-side `read` cursor never moves backwards (wire-contract 6), so "handled up to 41" survives a redeploy.

- **Push on decision.** The `submission` push reaches the author on a decision, so an agent waits rather than polls.

- **A rejection carries a reason.** `submission.reject` takes `comment`, which is `pma review --rework "feedback"`.

- **A channel is read-only to its audience.** Broadcast with no path for a worker to forge one.

- **Submissions gate fleet-wide acts.** `pma-agent` submits, the developer approves, and approval publishes the text unchanged under its author's name. A moderator may not edit (chat-concepts 5), so attribution survives.

- **A body ceiling.** 64 KiB per body under a 1 MiB frame (wire-contract 5, 6).

- **A readable refusal.** `{"error": "<message>"}` on every operation. An LLM reads the string and changes course without a lookup table.

Request and reply is about 80 percent built. Submissions are the right primitive; what is missing is an answer payload, a deadline and a rate limit.

## 11. Known holes

Stated, not solved.

**Nothing deletes a workflow room.** D9 founds rooms as an administrator, and chat-concepts open question 1 leaves unanswered who may delete a persisted room. A space bounds what its rooms hold and not how many there are.

D5 reduces the pressure without closing the hole. Room count tracks workflows dispatched, not the open backlog, and dispatch is a triaged batch the developer sizes against cost, so the count starts at zero and grows at a rate they choose. A completed workflow is also an unambiguous deletion trigger, which an open-ended task is not. What is still missing is anyone entitled to act on it: answer chat-concepts open question 1, or give a space a room lifetime, which inherits the transient room's problem of a deletion promise enforced by a sweep running when nobody is watching.

**Group assignment admits an agent everywhere at once.** A grant tracks the group (chat-concepts 2.3), so assigning an agent to `engineering` admits it to every room that group was invited to and discloses all of their history, in one `group.assign`, with nobody in those rooms asked. D8 uses a group per project, which makes this the mechanism the design relies on. Forbidding agents as group members is one check in `group.assign` and would break D8. Unresolved.

**A blocked reader is disconnected.** A connection's outbound queue holds 256 frames. A client that stops to wait on a model call stops draining, and in a busy room is disconnected. `go/internal/client` separates the reader from the work that blocks; anything written against the raw wire must do the same.

**Two wire implementations.** `go/internal/client` is `internal`, so out-of-tree code cannot import it, and `go/conformance/wire.go` is the 581-line reference to reimplement against. `pma` is Rust and needs a client; the container needs a static binary that is more than a client -- it owns the agent process, feeds room messages to it, sends its interrupt, and posts its output back. Unless that harness is also Rust, the envelope is implemented twice.

## 12. Out of scope

- **Agent discovery.** `pma` starts every agent and knows what each is for. [A2A](https://a2a-protocol.org/latest/specification) AgentCards answer capability discovery across organisations. One host, one dispatcher.

- **Federation.** [AMP](https://github.com/agentmessaging/protocol) addresses agents as `agent@tenant.provider` to route across providers. Every principal here is on one host.

- **Webhooks.** New server behaviour -- retries, delivery state -- and an outbound request from `minosd` to an address an agent influenced crosses the boundary section 8 exists to hold. `history` already gives pull delivery.

- **In-process agents.** Goroutines in `minosd` calling `messaging.Messaging` directly bypass `go/conformance`, need a fake connection for occupancy, and put model dependencies in the server. Moot anyway: the worker is in a container by requirement.

- **Agent-to-agent messaging as a protocol feature.** It is a grant policy (D10).

- **Nested spaces.** Deferred until something names a question two flat attributes cannot answer (7.2).

- **Delegation through MCP.** A person's assistant logged in as that person is a different design: the account is a person's, so `kind` cannot mark it and a per-message `via` would be needed, and the assistant holds the person's full authority including admin. Worth doing, separately, after this.

## 13. To decide before `architecture.md`

1. Does `minosd` run as a container `pma` starts, or as an operator's service `pma` finds? The second is cleaner and means `pma` must degrade to a bind-mount mailbox when it is absent.

2. Is the capability set (8.2) general, or a single `agent` role with a room list? General costs more now and avoids a second special case later.

3. Does `archive.search` span a space? "Search every workflow room in `cynn`" is the obvious want, and it is the pressure that breaks D7: a space-wide search must resolve to the union of rooms the caller may reach, and a careless version resolves to the space's audience, which a space does not have. Answer before building search, not after.

4. Can `pma` recover a workflow room its own database lost? If `pma` founds rooms and its state is the only index, a `pma` failure leaves the developer unable to find the room in exactly the case the room exists for.

5. Does a timed-out submission count against the worker's failures, or against the person who did not answer?

6. Which record is authoritative in a dispute? Recommendation: the relay for what reached a model, the archive for what was decided. Agent rooms then need a retention period at least equal to the relay's log retention, or the two records cover different periods and neither is whole.
