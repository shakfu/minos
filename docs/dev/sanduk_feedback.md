# Review: docs/dev/design.md

Read 2026-09-19 against the minos tree, the `sanduk` tree at v0.3.1, and the documents design.md cites. Verdict first, then what does not hold, ordered by consequence. Nits last.

## Verdict

The trust argument is sound and is the part worth keeping. Section 3's rule, its transitivity, D2, D6, D9 and the ordering in section 9 (build layers 3 and 4 first) all survive scrutiny. Section 10 is accurate about what minos already gives.

The weak parts are all on the boundary with the other two tools: section 1's account of `sanduk` is a version out of date, section 4 overstates what the sealed container protects, and sections 8 and 9 assume a control path into a running container that does not exist and is not on anyone's list.

Every citation I spot-checked is correct: `go/internal/messaging/messaging.go:379` is `Messaging.Send`, `go/internal/socket/socket.go:42` is `outboundDepth = 256`, `config.Admins` is `map[string]bool` (`go/internal/config/config.go:61`), wire-contract 2 is the 12-hour/7-day session, wire-contract 4 is the 100 MiB `writefile` body, chat-concepts open question 1 is who-may-delete, and `sanduk/agent.py:142` is the `protocols` declaration. This is unusually good for a design document and it makes the rest of the review cheap.

## 1. The `sanduk` row in section 1 is stale

"no control channel: a task goes in at the start and a report comes out at the end" describes `sanduk run`. It does not describe `sanduk assistants`, which shipped before this document was written.

An assistant is an identity, a schedule, a persistent `workspace/`, and a SQLite mailbox: `inbox` and `outbox` tables (`src/sanduk/assistants.py:86,93`), `sanduk tell` to enqueue, `decide` for approve/reject (`src/sanduk/assistants.py:392`), and `deliver` to pipe approved entries to a command (`src/sanduk/assistants.py:697`). Pending inbox items are consumed at wakeup and outbox entries are held until approved.

That is store-and-forward messaging with human approval gating. It is the design's submission flow at wakeup granularity. The document must say what happens to it -- superseded, or kept as the degraded path -- because a reader who knows `sanduk` will otherwise stop trusting section 1.

It also changes the argument. The question the document should answer is not "how do agents talk", which is answered, but "what does mid-turn correction buy over an inbox consumed at the next wakeup". The answer is section 5's third conversation, and that is the only capability the existing mailbox cannot provide. Lead with it.

## 2. Section 4 overstates what sealing protects

"A worker holds a repository that must not leave" is not true under any `sanduk` mode. The agent reads the repository and sends it to a hosted model on every turn; that is the run. `sealed` does not stop repository content leaving. It stops the container choosing *where* it goes: the network is `--internal` (`runtime.py:228`), the relay is the only reachable address, and the relay's allowlist is three exact paths (`proxy.py:38`, exact match rather than prefix, deliberately).

The real threat is narrower and worth stating precisely: exfiltration to a third party of the worker's choosing, and disclosure to *other principals* on the bus. Both are real, and both are what sections 8.3 and 8.7 actually address. As written, section 4 claims a property the deployment never had, and D12's justification inherits the overreach.

## 3. Layers 1 and 2 need `sanduk` changes nobody has listed

Section 9 treats interrupt and graceful stop as agent-capability questions. They are also plumbing questions, and the plumbing is absent.

- `launch` starts the container with `stdin=subprocess.DEVNULL` (`src/sanduk/agent.py:262`).

- `run_argv` passes no `-i` (`src/sanduk/runtime.py:297`), so the container has no stdin regardless.

- The Claude handler passes the task positionally: `["-p", task, "--output-format", "stream-json", "--verbose"]` (`src/sanduk/agents/claude.py:72`). One-shot headless. There is no `--input-format stream-json`, so there is no control-request path and nothing to carry an `interrupt()`.

A SIGINT to that process ends the process. It does not end a turn and leave a session standing. Layers 1 and 2 need: `-i` on the container run, `stdin=PIPE`, streaming input mode, and a writer that is not the read loop. That is a different process model, not a flag.

The document's one `sanduk` ask -- the session store on the bind mount -- is the smaller half of the work.

## 4. The harness displaces `sanduk`'s reader, and section 11 undercounts it

Section 11 says the container needs "a static binary that is more than a client -- it owns the agent process". Today the host owns the agent process, and owning it is what produces every artefact a run has:

- `Agent.argv` builds each CLI's flags. Nine handlers exist in `src/sanduk/agents/`.

- `Agent.reader` parses each CLI's JSON stream into `Outcome(ok, text, error, stats)`, including the per-agent token and cost arithmetic (`src/sanduk/agents/claude.py:34-57`).

- The `--timeout` watchdog is a host `threading.Timer` that kills the process (`src/sanduk/agent.py:274`).

- The live trace the operator watches is printed by `Reader.event`.

Move process ownership into the container and all four move with it, in the harness's language. Section 11 costs this as "the envelope is implemented twice". It is the envelope plus nine argv builders plus nine stream parsers plus the timeout plus the trace. Either the harness carries them, or it shells out to a per-agent adapter and `sanduk` keeps them -- which is the cheaper design and should be named as an option.

## 5. The session store cannot live in `-w`

Correct that `sanduk` does not persist `.jsonl` transcripts and that resumption needs them outside the container. But the bind mount the document points at is `-w`, which lands at `/work` (`src/sanduk/cli.py:98`) and is the repository worktree the agent edits.

Putting the session store there gives the agent write access to its own transcript. That is the same failure `sanduk` 0.3.1 fixed for `--log-dir`, where the default put request bodies inside `/work` and let the agent edit its own audit trail. The session store needs a separate mount that is not `/work` and not under it.

## 6. `minosd` is the second dual-homed enforcement point; the first one is built

8.6 calls dual-homing "the one mitigation". Read section 8 as a list of requirements and compare it with `proxy.py`:

| design.md | `sanduk.proxy` today |
|-|-|
| 8.1 a per-run credential, not a cookie | per-run bearer token, `compare_digest` (`proxy.py:299`) |
| 8.3 blanket route denials | exact-match path allowlist (`proxy.py:38`) |
| 8.7 a quota | `--budget`, serialized across a call (`proxy.py:448-465`) |
| 8.7 a readable refusal | refusal kinds by status, "an agent branches on these strings" (`proxy.py:79`) |
| 8.2 a limit the container cannot edit | `max_tokens` cap rewritten server-side (`proxy.py:330`) |

The alternative framing the document does not consider: put the coarse boundary in a relay in front of `minosd` rather than inside it. `minosd` then binds loopback only and 8.6 costs nothing, because the agent-facing listener is the relay. 8.3's two blanket 403s become two lines in an allowlist that is already exact-match and already tested.

The honest objection is that a relay cannot enforce per-room scoping without reimplementing the socket envelope, which is section 11's second hole again. So the split is probably: routes, rate and quota at the relay; rooms and capabilities in `minosd`. The document puts all of it in `minosd` without arguing why, and that argument is load-bearing for 8.1, 8.2 and 8.6 together.

One blocker either way: the relay is HTTP/1.1 via `http.client` and `BaseHTTPRequestHandler`, with no upgrade handling. Websocket relaying is new code.

## 7. 7.1 puts `kind` where 8.3's principals cannot read it

D11 states the reader need per message: "a worker must be able to tell the developer's instruction from `pma-agent`'s when they disagree". 7.1 puts `kind` on the user object in `sync.users`.

8.3 then says a grant gets no roster. A worker reading `history` therefore has author names and no way to resolve any of them to a kind. The principal D11 exists to serve is the one 8.3 blinds.

Two ways out, and the document should pick one: author kind on the message, or a grant's `sync` carries the kinds of authors in its own rooms and nothing else. The second preserves 8.3's reconnaissance argument at the cost of one more field.

## 8. D14 does not close the threat section 4 opens with

Section 4's outbound-through-the-provider case is stated generally: inviting an agent to a five-year-old room sends five years of it to the provider. D14 answers only the transient case. A five-year-old *persisted* room is the example given, and nothing in the design constrains it. Section 11's first group hole is the same gap from the other side.

The model already has one lever and the design adds a second without using it. A grant could carry a `since`, with `history` and `sync` refusing anything before it. One field, and it closes D14's case, the persisted-room case, and the `group.assign` disclosure in section 11 at once. The cost is an agent that cannot read the room's past, which for a per-task room founded by `pma` is no cost at all. Worth a decision entry rather than silence.

## 9. Room count is what breaks first, and D5 is not costed against it

95 repositories, a room per task, no deletion path, and D9 makes the rooms admin-founded so only chat-concepts open question 1 can remove them. Section 11 names this and offers two ways out, both larger than the design.

There is a third the document does not list: a room per project, with the task id in the structured payload from 8.5 and per-task views reconstructed by filter. It costs D10 -- every worker in a project's room is then bounded by every other worker's inputs under section 3's transitivity -- which is a real reason to reject it. But D5 currently reads as a free choice justified by rework, and it is not free. State the trade.

## 10. 8.1 contradicts itself on expiry

"Expiry tracks the task with an outer bound, not the run" and "each run gets a fresh grant over it" cannot both hold. A task open for three weeks with a task-scoped expiry is exactly the long-lived reusable credential the paragraph opens by rejecting.

Recommendation: expiry tracks the run plus a margin; room scope tracks the task. `sanduk` already sizes a lifetime to a run this way for the network holder (`HOLDER_MARGIN`, `src/sanduk/cli.py:114`).

## 11. Decision 13.1 is cheaper than the document assumes

"`pma` must degrade to a bind-mount mailbox when it is absent" is costed as the price of the operator-service option. That mailbox is built: see point 1. Option 2 is cheaper than the document thinks, which strengthens the recommendation it already leans toward.

## Minor

- 8.5 cites "chat-concepts open question 3 in channels.md". It is channels.md open question 3. chat-concepts open question 3 is room-name uniqueness. A reader will follow the wrong pointer.

- Section 8's opener cites "sections 9 to 11" for the operations-extend-and- routes-do-not precedent. Sections 9 to 11 of this document are stopping, what fits, and known holes. Presumably wire-contract 9 to 11.

- D16 says "Section 8". Stopping is section 9.

- `Agent.protocols` (`src/sanduk/agent.py:143`) is the set of *provider completion* protocols a handler speaks, and `Agent.check` (`src/sanduk/agent.py:145`) refuses on `completion_protocols(provider) & self.protocols` alone. Interruptibility belongs beside it, not in it: a second attribute and a second refusal. As section 9 words it, a reader could take it for an entry in the same frozenset.

- D15 rules out streaming to minos. The operator's live trace comes off the agent's stdout today, not off the bus, so nothing is lost -- provided point 4 keeps a route for it. Worth one clause, because the two decisions look like they conflict and do not.
