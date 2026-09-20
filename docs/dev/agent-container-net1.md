# Review: the container-channel direction, against contagent

2026-09-21. A review of [design.md](design.md) and [agent-container-net.md](agent-container-net.md), read against [contagent](https://github.com/kanaka/contagent) at `53e4a99`, the `sanduk` tree at v0.3.1, and [sanduk_feedback.md](sanduk_feedback.md) and [pma_feedback.md](pma_feedback.md).

Claims about contagent were checked against its tree and are cited by file. Claims about the minos Go tree are taken from the two documents and from the two prior reviews; they were not re-verified here. Everything else is argument.

## Verdict

The direction holds. The unix socket (agent-container-net 2), the seven-operation client, the reader/queue split and the four stopping layers are all correct, and contagent is a working instance of the option you rejected, which supports the rejection.

Five problems, ordered by consequence:

1. The build order ships a file channel out of a sealed container five steps before the thing that closes it. Two of the three fixes do not need grants and belong at step 1.

2. Nothing accretes on the authority axis. Knowledge accretion is covered many ways; decision accretion is covered none, and the submission deadline converts the shortfall into failed runs.

3. The knowledge write-path breaks section 3's rule. A wiki or a lessons channel written by workers re-couples the stages D19 isolates, and it defeats D20's validator specifically.

4. agent-container-net 8 lists eight decisions and all eight are transport. The policy question is not in the list.

5. The substrate choice is right and the documents under-argue it. A reader who counts the refused chat features will ask why not a queue.

Six things worth adopting from contagent are in section 10, with costs.

## 1. The build order reduces containment before it raises it

agent-container-net 9 orders: unix listener (1), envelope (2), client (3), MCP (4), process ownership (5), grants and the two denials (6), adapter (7). It states the consequence itself: steps 1 to 4 ship "with today's credential", and design.md 11 says that until 6 lands "a worker agent in a container is an ordinary account with an ordinary session".

Read that against what the container has today. A sealed run reaches the relay and nothing else, and the relay's allowlist is three exact paths (`sanduk/proxy.py:38`). Step 1 adds a second reachable service. Under a 12-hour cookie that service answers `/vfs`, where `writefile` takes 100 MiB into a per-user writable `home:` (wire-contract 3, 4), and `/settings`, which replaces a JSON object under 1 MiB. Both land in host-visible storage the container does not have to reach itself.

So step 1 as ordered opens a file channel out of a sealed container, and step 6 closes it. Five steps of exposure, in a tool whose reason to exist is that the container chooses no destination.

The fix is cheap and the documents already contain it. 8.6 makes the agent-facing address separately configured, and step 1 is the step that creates it. Gate `/vfs` and `/settings` on the listener rather than on the grant. That is a check in the mux, not the capability system, and it needs neither grants nor `kind` nor `since`. *Inference: I have not read `go/internal/httpapi`, so the cost is asserted, not measured.*

Two further observations on the same point:

- **8.6 and agent-container-net 2 are one decision, not two.** If the agent-facing listener is a unix socket, separable listeners are achieved by construction, and per-listener op gating is the natural place to put the denials. Pick the socket and 8.6 costs nothing. The documents price them separately.

- **Order the denials before the capabilities.** 8.2's capability set is a design question with an open alternative (13.2, general set versus a single `agent` role). 8.3's two denials are blanket and have no open question. Shipping a decided thing first is free.

Recommendation: insert step 1.5 -- `/vfs` and `/settings` answer 403 on the agent-facing listener -- and say in 9 that step 1 is not to be deployed against a real repository without it.

## 2. Knowledge accretes; authority does not

The point that knowledge accumulates is granted, and the mechanisms are real: `AGENTS.md` and `CLAUDE.md`, per-purpose templates, skills, markdown knowledge bases, lessons captured in channels. `sanduk` already carries two of these -- a recipe's `instructions` writes standing guidance to the agent's user-level `AGENTS.md` read-only, and a kit's skills land where each agent reads skills, root-owned.

They are all the same axis: what the agent knows before it asks. None of them changes what happens when it asks. That is a second axis, and it is empty.

| Axis | Mechanism | Effect of accumulation |
|-|-|-|
| knowledge | AGENTS.md, templates, skills, wikis, lessons | the agent asks better questions, and fewer of them |
| authority | `route.rs`, approval mode, escalation | the number of questions a human must answer |

Knowledge reduces the question rate by making the agent competent. It cannot drive the rate to zero for any question that is genuinely a decision, because a decision is not a knowledge gap. "May I force-push this branch" is answered by policy, not by a better `AGENTS.md`.

Today every such question is a `channel.submit` with an opaque body, decided once, standing for nothing. Two costs follow:

- **The rate scales with the fleet.** 95 repositories, a workflow per task, three stages per workflow. The developer is in the loop at dispatch rate for any class of question that recurs.

- **8.5's deadline converts the shortfall into failures.** A submission nobody answers becomes `timed_out`, and the worker dies with the work half done. 13.5 asks whose failure that is. The better question is why the rate is high enough for the deadline to fire. A standing decision removes the question before it is asked; a deadline only bounds how long it hurts.

contagent's hostbridge answers this in one file. A prompted command produces a decision on three axes, and the decision is written back (`hostbridge.md`, "Access Control"):

| Axis | Values |
|-|-|
| action | allow, deny |
| scope | once, this session (tagged with the hostbridge pid, expires with it), always |
| specificity | this exact argument list, or any arguments |

Two implementation details are worth taking verbatim:

- **Config and state are one file.** Hand-authored rules and recorded decisions coexist in `.hostbridge.yaml`. The developer edits or deletes to reset, with no second store to reconcile.

- **It is re-read on every access check**, so an edit takes effect without a restart, and specific-args rules beat any-args rules for the same command.

`pma` already holds the right home. `route.rs` commits to policy as an artifact: "one decision per revision, applied deterministically to every dispatch". Approvals are the same shape and are not in it.

**Proposed D22. A decision carries a scope, and `always` writes a rule into the routing artifact.** `submission.answer` (8.4) gains `scope: once | run | always` and, for `always`, a match specificity. `once` is today's behaviour and stays the default. `run` expires with the grant, which is the session analogue and needs no new lifetime. `always` appends a rule to `pma`'s routing artifact, where the next occurrence is answered by `route.rs` with no model and no human in the path.

The match key exists already. D18's structured payload carries the verb and its arguments; 8.5 names the fields. The server stores it unread, so the matching is `pma`'s, which is where it belongs.

**The objection, and it is real.** A rule written in response to a worker's request is a rule an attacker-influenced principal shaped. Section 3's transitivity applies to the artifact. Three mitigations, and the design should state which it takes: the developer authors the rule text rather than accepting the worker's phrasing; the artifact is reviewed per revision, as `route.rs` already is; and `always` is refused for any verb whose blast radius is unbounded, which is section 9's argument-narrowing question.

## 3. The knowledge write-path breaks section 3's rule

This is the critique of the accretion list rather than an endorsement of it.

Section 3: "An agent's authority must not exceed that of the least-trusted principal who can write to it." D19 then windows what a stage reads of its predecessor, defaulting to the stage's own start, so the fix stage reads artifacts and not the reviewer's reasoning. D20 separates the room's audience from a grant's window for exactly this purpose.

Now classify the accretion mechanisms by who may write them:

| Mechanism | Writer | Reaches |
|-|-|-|
| `AGENTS.md`, `CLAUDE.md` | developer | every agent that reads it |
| per-purpose templates | developer | the workflows that select them |
| skills | developer, or a pinned kit | every agent in the image |
| markdown knowledge base or wiki | **whoever may write it** | every agent that reads it |
| lessons captured in channels | **workers, via their own output** | every agent that reads the channel |

The last two are write paths from untrusted output into the context of future agents. They are outside the room, so `since` does not window them, and outside the grant, so capabilities do not gate them.

Three consequences:

- **D19 is bypassed out of band.** A reviewer writes a lesson; the validator reads the wiki; the validator is now steerable by the reviewer. D20 states the validator's purpose plainly -- "The validator exists to check the reviewer, so it must not be steerable by it" -- and a shared knowledge base that either may write returns exactly that steering, on a longer delay and with no cursor to audit it.

- **The transitivity is unbounded across projects.** A lesson learned in `cynn`'s worktree, written to a fleet-wide wiki, reaches the worker holding `py`. That is pma_feedback 1's objection to `pma-agent` as a hub, reached by a second route, and this one has no rate limit and no archive.

- **Retention and the window disagree.** Section 10 makes the archive the record of what was decided. A wiki is a second record with no period, no `since`, and no author kind.

**Proposed D23. Every source read into an agent's prompt is classified by its least-trusted writer, and agent-written sources are gated.** Three settings cover the list: developer-written (`AGENTS.md`, templates, skills) is unrestricted; agent-written-and-approved passes a submission before it is readable, which reuses the mechanism that already exists; agent-written-and-raw is readable only inside the room and window that produced it, which is D19 unchanged.

A practical form of the middle setting, because it is the one that earns its cost: a lesson is submitted, the developer approves, and approval is what writes it to the knowledge base under its author's name. Approval publishes text unchanged and a moderator may not edit it (chat-concepts 5), so attribution survives into the artifact.

**The cheap half, available now.** A validator stage should read no agent-written source at all. That is one flag on the stage in the workflow definition, it needs no new server behaviour, and without it D20's argument for the validator does not survive contact with the wiki.

## 4. The substrate choice is right and under-argued

The reason given -- a multiplicity of agent workflows running concurrently in containers, needing a means of communication -- is sound, and two properties are hard to get any other way: one ordering and one archive across the fleet, and fan-in for `pma-agent` without a per-worker receiver. Replay, the durable cursor and the submission flow are then free, and section 10 is right that they are already built.

The strongest reason is the one the documents leave implicit. **The human end of the bus is a chat client that already exists.** A bespoke queue gives the developer nothing to read. Section 5's third conversation -- the developer reaching a worker directly while `pma-agent` is down -- is a chat client requirement before it is a protocol requirement.

What weakens the case as written is the number of chat affordances the design then refuses:

| Refused | Where |
|-|-|
| occupancy; agents do not enter rooms | D13 |
| transient rooms | D14 |
| streaming and typing indicators | D15 |
| the roster | 8.3 |
| presence, unread counts, the whole TUI | agent-container-net 3 |
| 31 of 38 client operations | agent-container-net 3 |

Concurrency inside a room is refused as well: D10 allows no two concurrent workers, and the D19 default gives each stage a window starting at its own start. So one room holds one live agent, a supervisor and the developer, and the multiplicity is across rooms.

That is a defensible object, but it is not a chat room in use. It is an append-only ordered log per workflow, with a per-principal read window, a durable cursor, replay, and a submission flow -- plus a human client for free. Name it in section 1. A reader who counts the refusals will otherwise conclude the chat model was assumed rather than chosen, and the answer to "why not N mailboxes" is one paragraph the document does not spend.

`sanduk`'s assistant mailbox is the N-mailboxes design, and sanduk_feedback 1 is right that it already does store-and-forward with approval gating. State what it becomes: the degraded path when `minosd` is absent, which 13.1 needs anyway.

## 5. The push trigger and the standing decision are one question

agent-container-net 8.4 is open: what triggers a push into the turn rather than waiting for the next `messages` call. It names a payload verb as the obvious rule and an idle timer as the fallback, and it is right that getting it wrong makes every status note an interruption.

Section 2's `always` rule needs the same taxonomy from the other side: which verb, with which arguments, may be answered without a human.

One verb set answers both. Define it once, in `pma`, as the payload schema D18 keeps off the wire. Each verb then carries two properties: whether it interrupts, and whether it is eligible for a standing decision. Two open questions close on one artifact.

## 6. Interface: PATH shims beside MCP

contagent's `hostbridge-client.js` is symlinked into `/usr/local/bin` under each tool name. The caller runs `paplay` and never learns it is remote (`hostbridge.md`, architecture).

agent-container-net 7 frames the interface choice as MCP-in-the-container versus no-client-at-all, and rejects option 4 because "the agent cannot call a tool". A shim on `PATH` is a third position: the client still owns the socket, the queue and the cursor, and the agent's interface is argv and stdout. The five tools of 8.4 become five executables against the same local socket.

The case, against the constraints already stated:

- MCP over stdio is spoken by several agents. `PATH` is spoken by all nine `sanduk` handlers, including hax, which is a static C binary, and prime-agent, whose only tool is a Python REPL.

- The preamble already states the bounds in the system prompt. Naming five commands there costs one paragraph.

- Refusals pass through unchanged; stdout carries the server's string, which is what 8.4 relies on.

- It composes rather than replaces. One client, two front ends, chosen at dispatch by agent.

The costs: no schema advertisement, no typed replies, and `await` becomes a blocking process rather than a blocking tool call. A shim is also runnable by anything in the container, but under option 3 the socket is already reachable by anything in the container, so that is unchanged.

Recommendation: build the socket client and the seven operations first, then put MCP and shims both on it. It narrows 8.3's open half, because the per-agent adapter that builds argv exists for nine agents in `sanduk` and need not be rebuilt.

## 7. A refusal should carry the rule that would permit it

contagent, on a headless host with no dialog available, denies the prompted command and emits the YAML snippet that would allow it permanently (`README.md`, Hostbridge).

agent-container-net 4 already commits to refusals a model can act on. One step further: the refusal names the rule. The worker then submits that rule instead of guessing what it may ask for, and the developer answers a concrete policy line rather than prose. This also gives a `timed_out` submission a residue -- the rule text, unapplied -- which 13.5 currently leaves as nothing.

## 8. contagent is option 1, built

agent-container-net 2 rejects a second TCP listener on the internal network. contagent is that design running, and three of its properties are the rejection's evidence.

- **No principal at the transport.** The shim dials `host.docker.internal` on a port; the server authenticates nothing. Every rule is therefore about the verb and never about the caller. This is what "reachable by every container on it" costs in practice. It also argues for the per-container socket variant that 2 defers: identity riding the grant alone is the contagent position.

- **The rendezvous file sits inside the project directory.** `build-contagent.yaml` gives the hostbridge feature `volumes: [{path: ./.hostbridge-tmp}]` and `TMPDIR: ${CONTAGENT_CWD}/.hostbridge-tmp`. Control-channel state where the agent writes. That is the failure `sanduk` 0.3.1 fixed for `--log-dir` and that sanduk_feedback 5 flags for the session store. agent-container-net 5 already puts the grant on a mount that is not `/work`; contagent is the counter-example, not the confirmation. Answer 8.5 the same way for the socket path.

- **Connection lifetime equals process lifetime.** One websocket per invocation, and a close kills the process with SIGTERM then SIGKILL after 2s. The 256-frame queue problem does not arise because nothing is long-lived. Not transferable to a room, which is a stream. It does point out that only the push lane and `await` need persistence: `say`, `submit` and `progress` are request and reply.

contagent's trust model is stated plainly and is the opposite of this one: "Contagent reduces exposure; it is not a hard security sandbox." It mounts the docker socket, `~/.config/gh`, `~/.aws` and the SSH agent socket on purpose. Nothing in its access control transfers as a security argument. The decision ledger transfers as a workflow argument.

## 9. Narrow the operation, do not only gate it

Each hostbridge registry entry carries a `transform(args)` run before spawn (`hostbridge.md`, Argument validation): audio takes exactly one path and strips all flags; `xdg-open` requires exactly one `http(s)` URL; clipboard blocks read flags. The broker narrows what it forwards.

`sanduk`'s relay does the same thing in the other direction: `--max-tokens-cap` rewrites the field server-side rather than refusing the call (`sanduk/proxy.py:330`), which sanduk_feedback 6 already lines up against 8.2.

The capability set in 8.2 is coarse by design, and D18 keeps the payload opaque, so the server cannot narrow anything. That split is right. The point is only about where the validator lives: beside `pma`'s decision, not in the worker's prompt. It is also the precondition for D22 -- a verb eligible for a standing decision must be one whose arguments can be constrained, or `always` grants more than it was asked.

## 10. What to adopt, with costs

| # | Adopt | From | Cost | Blocks |
|-|-|-|-|-|
| 1 | `/vfs` and `/settings` 403 on the agent-facing listener, at step 1 | own 8.6 | one check in the mux | nothing |
| 2 | Decision scope `once/run/always`, `always` writes a `route.rs` rule | `.hostbridge.yaml` | a field on `submission.answer`, a rule file in `pma` | the verb taxonomy (5) |
| 3 | Classify knowledge sources by least-trusted writer; validators read none | section 3's own rule | one flag on a stage, now; a submission gate later | nothing for the cheap half |
| 4 | One verb taxonomy for the push trigger and for standing eligibility | -- | one artifact in `pma` | nothing |
| 5 | PATH shims beside MCP on the same client | `hostbridge-client.js` | five small executables | the client (step 3) |
| 6 | Refusals carry the rule that would permit them | contagent headless path | a string in the error | the verb taxonomy |
| 7 | Config and decisions in one file, re-read per check | `.hostbridge.yaml` | none beyond (2) | (2) |
| 8 | Socket path on a mount that is not `/work` | contagent's mistake | none | nothing |

One item outside this document's scope, for `sanduk` rather than minos. contagent maps the host uid, gid, username and home at runtime in `entrypoint.sh` -- `groupadd`, `usermod` or `useradd`, then `runuser` -- so one image serves every user. `sanduk` bakes `AGENT_UID` as a build arg (`runtime.py:485`) and refuses a run whose workdir owner differs (`cli.py:1094`), which is a rebuild per uid. It matters here for one reason: agent-container-net 2 notes that the socket's file mode "is not a substitute: a container running as root ignores it". Under runtime uid mapping the agent is not root, and the mode becomes a real secondary control. Not a replacement for the grant.

## Minor

- agent-container-net 8 lists eight decisions and every one is transport or code layout. Sections 2 and 5 above are policy decisions the build order depends on. Add them, or say explicitly that policy is `pma`'s document and not this one.

- `pending` in the hostbridge protocol tells the caller it is waiting for a human. `await` blocks and the `submission` push arrives on decision, so a worker cannot distinguish queued from lost until the deadline. One push on receipt would cost one frame.

- contagent's `AGENTS.md` states a no-reverse-dependency rule for hostbridge, because it may be split out later. agent-container-net 8.8 asks which package the envelope moves to before a third implementation, and 8.3 asks whether the client carries nine parsers. Both are seams that erode without a marker. One line per seam in `AGENTS.md` is cheaper than noticing later.

- contagent has nothing to say about rate limits, quotas, retention, multi-principal trust, cursors, replay, gap repair or author kind. It is one user and one container. Its silence on these is not evidence.
