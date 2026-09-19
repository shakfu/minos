# Feedback on design.md

From `pma`'s side, 2026-09-19. Read against [design.md](design.md), [chat-concepts.md](chat-concepts.md), [wire-contract.md](../wire-contract.md), the Go tree, and `pma`'s own [using-containers.md](https://github.com/shakfu/pma/blob/main/docs/dev/using-containers.md). Claims below marked *verified* were checked against the cited file; the rest are argument.

## Verdict

The trust reasoning is the strongest part and it holds. Section 3's rule, its transitivity, and D2 are correct and worth keeping unchanged.

Three problems:

1. `pma-agent` reading every task room makes it a channel between workers, which is what D10 forbids and section 3 rules out. The document does not acknowledge it.

2. A room per task is 1031 rooms today, against a model with no deletion. Section 11 states the hole; the room grain is what creates it.

3. Four internal inconsistencies, all cheap to fix, listed in section 3 below.

## Citations checked

| Claim | Status |
|-|-|
| `Messaging.Send` gates on `requireAccess` | verified, with a correction: it also refuses a channel (`messaging.go:383`). "and nothing else" in D13 is wrong as written. The point about occupancy stands |
| outbound queue holds 256 frames | verified, `socket/socket.go:42` (`outboundDepth`) |
| admin is binary | verified, `config.Admins map[string]bool` (`config/config.go:61`) |
| 100 MiB `writefile`, 1 MiB other bodies, 64 KiB body | verified, wire-contract 85, 340 |
| role read from the session, not the user table | verified, wire-contract 55 |
| `That submission has been decided` | verified, wire-contract 483 |
| chat-concepts open question 1 leaves deletion unanswered | verified, chat-concepts 223 |
| channels.md open question 3 anticipates a pass-through payload | verified, channels 181 |
| `go/conformance/wire.go` is 581 lines; `go/internal/client` is not importable | verified |

## 1. `pma-agent` is the hub, and 8.2 reinstates what D10 forbids

D10 keeps two workers out of one room, and gives the reason: "two workers in one room means each is bounded by the other repository's content". Section 3 makes that transitive.

8.2 then gives `pma-agent` read on every task room and send on every task room. That is a channel between any two workers, with a model in the middle. Repository content from `cynn` enters `pma-agent`'s context and leaves in an instruction to the worker holding `py`. The hop is slower and lossier than a shared room; it is not narrower. By section 3's own rule every worker is bounded by all 96 repositories, which is the state D10 exists to prevent, and the document does not say so anywhere.

This is the design's central question and it turns on one fact the document leaves unstated: **is `pma-agent` one context across the fleet, or one instance per task?**

| | one fleet context | one instance per task |
|-|-|-|
| Grant | "a grant over every task room", as section 2 and 8.2 have it | one task room plus `control`, the same shape as a worker |
| Cross-repository contamination | by construction | none; D10's property holds end to end |
| Fleet-wide view | yes, and it is the reason to want this shape | none. Ranking across projects goes back to `pma`, which already does it: `rank.rs`, `route.rs`, `class.rs`, `complexity.rs` |
| Cost | one long context, growing | one context per dispatched task |
| What `control` is for | reaching the fleet the agent already sees | the only fleet-wide surface, gated by a submission |

Per task is the smaller claim and it costs little, because the cross-project judgement `pma-agent` would otherwise supply is the part `pma` already computes from stored scores rather than from prose. Recommendation: per task, and 8.2's room scope for `pma-agent` becomes one room, not every room.

If the fleet context is wanted anyway, say what it decides that `rank.rs` cannot, and state the consequence plainly in section 3: the rule is accepted as violated across projects, with `pma`'s execution monopoly (D2, D3) as the only barrier. That is a defensible position. It is not the position the document currently claims to hold.

Two smaller consequences either way:

- `pma-agent` submits to `control` (5, 8.2). Under a fleet context, repository text from one project can shape a fleet-wide instruction, and the developer approving it is the only filter. Section 10 already secures the part that matters -- a moderator may not edit, so the text is approved under its author's name unchanged.

- The rate limit (D12) is correctly mandatory under either shape. An agent answering an agent is the design, so the two cheaper agent-side guards do not apply. No change asked.

**One caveat on measurement.** Section 5 makes the intervention rate the test of whether the design works. `pma`'s `runs` table holds 0 rows today, against 96 projects and 1031 open tasks. Nothing yet measures it. That is expected -- the pipeline is not built -- but it means the fleet-context question above cannot be settled by evidence yet, and the reversible choice is the per-task one.

## 2. Numbers: 1031 rooms, none deletable

`~/.config/pma/projects.db`: 96 projects, 1031 open tasks. D5 founds a room per task. So the design starts at about 1031 rooms and grows with every new TODO entry, against chat-concepts open question 1, which does not say who may delete one. Section 11 names this and calls both exits larger than the design. The room grain is the cheaper lever.

**Alternative framing: a room per project.** 96 rooms, fixed by the portfolio, not by the backlog. 8.5 already adds a task id to every object, so the task grain survives as a payload field.

| | room per task | room per project |
|-|-|-|
| rooms today | 1031 | 96 |
| deletion hole | load-bearing | mostly moot; the set is bounded and stable |
| spaces (7.2) | needed to group 1031 rooms | nothing left to group. `space.create`, `space.set`, `space.assign`, `space.delete` -- 4 of the 7 new ops in 8.4 -- disappear |
| D5 rework reads run 1 | by room scope | by task id filter |
| D10 | holds | breaks: concurrent dispatch puts several workers in one room |

D10's own argument does not carry over. It rejects two workers per room because "each is bounded by the other repository's content". Within one project there is one repository, so the transitive rule does not bite. What remains is real but smaller: repository content that steers one worker can steer its siblings in the same repo, and read cursors carry unrelated traffic.

Retention inheritance is the one part of 7.2 worth keeping. It does not need a space object. A server default period, overridden per room, is the same rule in one attribute.

## 3. Internal inconsistencies

**3.1 8.6 does not constrain `pma-agent`.** The section claims listener separation makes `grant.mint` unreachable "by the principals it exists to constrain -- including `pma-agent`, which runs on the host". But `pma-agent` runs on the host, so it reaches the human-facing listener, which is where `grant.mint` lives. Listener separation constrains workers only. As written the mitigation does not do what the sentence says.

Fix: bind the agent-facing listener to the sealed network and to loopback, and put `pma-agent` on it. The claim then holds, and 8.6's closing rule -- never bind the agent-facing listener to a routable interface -- is unaffected, because loopback is not routable.

**3.2 Grant expiry contradicts D17 and pma's item 8.** 8.1 says "Expiry tracks the task with an outer bound, not the run", then says "each run gets a fresh grant over it". If every run mints a fresh grant, the expiry should be the run's timeout. `pma`'s item 8 asks for exactly that: "expiring with the run's timeout". A task spanning days means a credential valid for days inside a container that `sanduk` deletes at the end of each run.

The room lifetime tracks the task. The grant lifetime should be `min(run timeout, task outer bound)`. `pma` already stores `timeout_minutes` per run.

**3.3 D11 makes `system` mean two things.** 7.1 says `author: "system"` "is already reserved for events and stays the server's alone". D11 says `pma` the tool speaks as `system`. Both cannot hold. `system` is the one label a participant cannot forge, and D11 spends it on a host process holding an admin session.

Either `pma` gets an account with `kind` distinguishing it from `agent` and `user`, or it emits server events through an admin operation and the server authors them. The second is closer to D11's stated reason ("its output is server-side fact"), and it keeps the unforgeable label unforgeable.

**3.4 The quota is in the wrong unit.** 8.7 sets "messages per second and messages per grant". Section 4 states the threat in bytes: a repository leaving 64 KiB at a time. A message count times 64 KiB is a byte budget stated indirectly and generously -- 200 messages per run is 12.8 MiB, which exceeds most source trees in the portfolio. Put the quota in bytes of body per grant, name the number, and derive it from the largest worktree you are willing to see leave. A message rate is still worth having, for queue pressure, not for containment.

**3.5 D13.** See the citation table. `Send` also refuses channels.

## 4. State the security claim in bytes

8.3 refuses `/vfs` with the right argument: "Not a size limit: the whole namespace." Then 8.7 leaves the chat channel bounded by a number nobody has picked. A worker with `send` on one room writes repository content into bodies, and those bodies cross the boundary by design -- that is what the room is for. `archive` then stores them host-side and makes them searchable.

So the property `sanduk` sells changes, and section 4 half says it. Finish the sentence with a number:

> A sealed run may emit at most N bytes, to one room, rate limited, attributed, and archived.

That is defensible and better than the relay-only story in one way: it is readable while it happens. It is not "nothing leaves". `pma`'s note reached the same point from the other side ("`sealed` stops meaning 'nothing leaves'"), and neither document has yet named N.

## 5. Holes that bite pma specifically

**No degraded mode.** D1 routes every message through minos. Section 13 question 1 asks whether `minosd` is `pma`'s container or an operator's service, but no decision gives `pma` a dispatch path when `minosd` is down. `pma` dispatches today with no server at all. Requirement from this side: dispatch must not depend on `minosd`. `pma`'s item 7 keeps a bind-mount mailbox for exactly this, and it is also the fallback for a single fire-and-forget run that needs no room. State in D1 that minos carries conversation, not dispatch.

**The group hole has a cheaper fix than section 11 suggests.** Section 11 says forbidding agents as group members would break D8. It would not, if the rule is placed on the grant rather than on `group.assign`: a grant's room set is explicit, and group membership confers nothing to a principal whose `kind` is `agent`. D8's group stays what it is for -- admitting colleagues -- and the one-`group.assign`-discloses-everything path closes. This needs 7.1's `kind` to exist, which is another reason to keep 7.1 while dropping 7.2.

**Revoke without kill is the dangerous state.** Layer 4 leaves a container running with the worktree and no channel: an unsupervised agent still writing to a bind mount `pma` will later diff. Layer 4 should imply layer 3 unless the operator asks otherwise. `pma` already owns the kill -- children run in their own process group under a timeout (`src/agent.rs:38`).

**Two wire implementations is three.** Section 11 counts the Rust client and the container harness. `pma` is Rust; the harness must own an agent process, so it is whatever language that agent ships with. Concrete proposal: promote `go/internal/client` to `go/client` and write the harness in Go against it, leaving `pma` one minimal Rust subset -- `open`, `send`, `history`, `submission.*`, `grant.*`. That is one new implementation, not two, and it is the smallest change to the Go tree in this document.

## 6. Ordering

`pma` is blocked on two things and nothing else:

1. A grant, minted by `pma`, scoped to rooms, expiring with the run (8.1, corrected per 3.2).

2. Deny by default, with `/vfs` and `/settings` refused (8.3).

Add one that this document is right to raise and `pma`'s note missed: separable listeners (8.6, corrected per 3.1). Those three make a sealed run with a channel possible. Everything else is quality of life:

| Deferrable | Why |
|-|-|
| spaces (7.2, 4 ops) | unnecessary under a room per project; a retention default replaces the only load-bearing part |
| `kind` (7.1) | keep. It is one additive field and the group fix depends on it |
| submission deadline | keep. An agent cannot time itself out and a blocked run burns its budget |
| structured payload (8.5) | keep, and coordinate with channels.md question 3 rather than specifying it twice |
| task and run ids (8.5) | keep. `pma`'s `runs` table already has `id`, `project` and `task_key` to join against |
| `submission.answer` | keep |
| rate and quota (8.7) | keep, in bytes |

Seven new operations against a frozen contract with a 581-line conformance reference is the real cost of this document, and it is not stated anywhere in it. Dropping `space.*` removes four of them.

## 7. Answers to section 13, from this side

1. **Operator's service.** `pma` should not own `minosd`'s lifetime. `sanduk`'s `runs.py` sweep does not own it either, so a `pma`-started `minosd` is an orphan nobody collects. With the mailbox fallback, absence is degradation rather than failure.

2. **Single role plus a room list**, not a general capability set. Under a per-task `pma-agent` (section 1) the two principals differ only in a room list and a submit target, so the general set is a hedge against a second special case that has not appeared.

3. **Do not build space-wide search.** It is the pressure that breaks D7, by this document's own reading, and under a room per project the want ("search every task room in `cynn`") is one room.

4. **No, and that is a `pma` bug to fix on this side.** `pma` should record the room id on the task row, and recover by listing rooms from its admin session when the row is lost. Raised as a `pma` work item; no minos change.

5. **Neither.** A timed-out submission is a failure of the escalation policy, not of the worker or the person. Record it on the run row as an unanswered escalation and let it drive the policy. `pma` owns this; minos only needs the `timed_out` state pushed to the author.

6. **Agreed with the recommendation.** The relay for what reached a model, the archive for what was decided. Add: the archive is authoritative for nothing a model said inside the container, because `minosd` never saw it.

## 8. Questions back

1. Is `pma-agent` one context across the fleet, or one per task? 8.2's room scope and D10's property both depend on the answer.

2. What is N in section 4? Bytes per grant, per run.

3. Does a worker get a read cursor, given D13? An agent that does not enter a room still needs a durable marker, and section 10 counts the cursor as already fitting.

4. Is `space` worth its four operations if the room count is 96?
