# What contagent teaches, and what changes

2026-09-21

[contagent](https://github.com/kanaka/contagent) solves a neighbouring problem and ships. This reads it at `53e4a99` (2026-08-27) against [agent-container-net.md](agent-container-net.md) and [design.md](design.md), takes what transfers, names what does not, and proposes a fifth option those documents do not score.

Citations of the form `file:line` are contagent's tree. Citations of the form "design.md 8.3" are ours.

## 1. What it is

A Docker runtime for coding agents on a developer's machine. One operator, one project, an interactive session or a one-shot command. Five agent CLIs are supported: Claude Code, OpenCode, Pi, Codex, Copilot.

| | contagent | minos |
|-|-|-|
| Who runs the agent | the developer, at a prompt | `pma`, unattended, many at once |
| Isolation unit | a container per invocation or shell session | a container per stage of a workflow |
| What is isolated | the filesystem and the credential set | the network, the filesystem, and the authority |
| Network | default bridge; ordinary outbound internet | `--internal`; one reachable address |
| Control channel | none; the developer is at the terminal | a room on `minosd` |
| Human in the loop | a native dialog, synchronous | a submission, asynchronous |
| Threat model | accident and blast radius | an attacker-influenced agent |

The last row decides how much of the rest transfers. The README states it: "Contagent reduces exposure; it is not a hard security sandbox" (`README.md:270`). design.md 3 assumes the opposite about the worker. **Their mechanisms transfer; their trust conclusions do not.**

## 2. The comparable part

Hostbridge. A host-side server that runs a fixed list of commands on the container's behalf: audio, notifications, clipboard, `xdg-open`, text-to-speech, and a GUI dialog binary. Two pieces, 443 and 175 lines.

| Fact | Where |
|-|-|
| The server binds `127.0.0.1` | `hostbridge.js:28` |
| The container dials `host.docker.internal` over websocket | `hostbridge-client.js:20` |
| One websocket is one command; connection lifetime is process lifetime | `hostbridge.js:8` |
| The container-side shim is symlinked under each tool name, so the agent calls `paplay` | `hostbridge.md`, Architecture |
| Commands are a fixed registry, each with an argument `transform` | `hostbridge.js:78`, `40-76` |
| Access is `allow` / `deny` / `prompt`, defaulting to `prompt` | `hostbridge.md`, Access Control |
| A decision carries a scope: once, session, always; and any-args or these-args | same |
| Rules are re-read on every invocation | same |
| Disconnect kills the process group: SIGTERM, then SIGKILL after 2s | `hostbridge.js:35` |
| The bridge is started by the launcher and shut down when the container exits | `contagent:241`, `contagent:252` |

In the terms of agent-container-net.md 2, hostbridge is option 2 -- a relay. Our objection to option 2 was that "a relay that cannot read the envelope cannot scope a room". That is true of `sanduk`'s HTTP relay and false of hostbridge, which parses every frame it carries and is the policy point. **The objection was to a generic proxy, not to a broker.** Section 5 below takes that distinction somewhere.

### 2.1 What the shim is, and is not

The shape matters more than the size, and two things about it are easy to get backwards.

**It ships in the image; nothing is symlinked from the host.** The links are made at build time and point at a file that is also in the image:

```dockerfile
COPY hostbridge-client.js /usr/local/bin/hostbridge-client.js
RUN chmod +x /usr/local/bin/hostbridge-client.js && \
    for t in paplay aplay play notify-send xdg-open pbcopy pbpaste wl-copy \
             wl-paste xclip xsel say glimpse; do \
      ln -s hostbridge-client.js /usr/local/bin/$t; \
    done
```

Thirteen names, one file, no mount. Dispatch is on the invoked name -- `path.basename(process.argv[1])`, `hostbridge-client.js:25` -- which is busybox's multicall pattern.

**It is transport, not a toolset.** On invocation it opens the websocket, sends `{type:"exec", cmd, args}` naming itself and its argv, pumps stdin up and stdout and stderr down as base64, forwards signals, and takes its exit code from the `exit` frame. It is `ssh host cmd` with a fixed destination. What it presents to its caller is ordinary process semantics -- argv, three streams, signals, an exit code -- which is why a caller needs no library, no config and no protocol knowledge.

**It carries no policy and is not trusted to.** Every constraint is in the host process the container cannot reach:

| Constraint | Where it lives |
|-|-|
| Which commands exist at all | `REGISTRY`, `hostbridge.js:78` |
| Which arguments are permitted | the per-command `transform`, `hostbridge.js:40-76` |
| Whether this invocation runs | `.hostbridge.yaml`: allow, deny or prompt |
| How long it may run | the per-command `timeout` |
| What happens on disconnect | process-group kill, SIGTERM then SIGKILL |

The thirteen names are a convenience for the caller, not a restriction on it. Any process in the container can open the websocket itself and send an `exec` frame naming any command, and it meets the same registry check. Replacing the shim with a hostile program changes nothing, because it never held authority.

**The tool names live in the container; the tool definitions live on the host; only the host decides.** That is the transferable idea, and it is what makes the front end interchangeable: an MCP server, a shim, or both over one policy point.


## 3. What transfers

### 3.1 The shim reaches further than the tool

**First, the lane this governs is the smallest one.** `docs/media/architecture.d2` draws three edges between the agent and its client, and the front-end question touches one of them:

| Edge | Direction | Carries | Share of bytes |
|-|-|-|-|
| `agent -> client.pipes` | out | the whole turn: text, tool trace, cost | dominant |
| `client.pipes -> agent` | in | a pushed message, queued to the turn's end; an interrupt, which is not | small |
| `agent <-> client.mcp` | request and reply | the asks: `messages`, `say`, `submit`, `await`, `progress` | small |

The pipes are the main channel by volume and the harder requirement by necessity: stdin is the only lane that delivers without the agent's cooperation, and stdout is how the harness learns that anything happened at all. MCP-or-shim is the pull lane alone.

That does not make it optional. Three things have no expression on the pipes: an `await` that blocks rather than ending the turn, `progress` and `messages` on the agent's own schedule, and a refusal the agent reads without its turn ending. agent-container-net.md 7 says it in one clause -- "what survives is the tool call" -- and the pull lane is what justifies a socket into the container at all. Under option 5 the pipes are host-side, so **the socket carries only the pull lane**; remove that lane and option 5 is option 4 with an unused mount.

With the scope set, the reach argument.

agent-container-net.md 7 rejects option 4 partly on reach: "Every agent CLI differs. MCP over stdio is the one interface several already speak." contagent drives five agent CLIs and uses no MCP at all. The interface all five speak is `exec`.

Nothing in the agent knows a bridge exists; it runs `paplay f.mp3` and gets an exit code (2.1).

**Registration, not protocol, is what differs per CLI.** MCP needs four things from the agent CLI: a client implementation, a config format declaring the server, a way to supply that config at launch, and process lifecycle. All four differ per CLI -- Claude Code takes `.mcp.json` or `--mcp-config`, Codex an `[mcp_servers]` table in `config.toml`, OpenCode its own JSON key. (Verify each before relying on it; these formats move, and this is the detail most likely to be stale.) So MCP does not remove the per-agent code sanduk_feedback 4 priced at nine argv builders. It relocates it from argv to config writers. A shim registers once, in the image, for every CLI at once, including ones that do not exist yet.

| Per agent CLI | shim | MCP |
|-|-|-|
| Needs a client implementation in the CLI | no | yes |
| Registration | none; `PATH` | a config file, per CLI |
| Permission configuration | the bash gate the run already needs | a tool gate, which often defaults to prompting |
| Works with a CLI that has no plugin system | yes | no |
| Reachable without a model in the loop | yes | no |

**Four things `exec` gives that MCP does not.**

- **A startup failure is visible to the agent.** An MCP server that fails to start is an absent tool: the model is never told it lost one, and it proceeds without. A missing or failing binary is a non-zero exit and a line on stderr, which is the readable-refusal convention design.md 10 already relies on.

- **A payload need not pass through the model.** `minos say --file REVIEW.md` posts 64 KiB the model never re-emits. Under MCP the body is an argument, so it is generated token by token, at token cost, with the transcription errors that implies.

- **The transport is testable without a model.** `docker exec ... minos say hello` exercises build-order steps 1 to 3 end to end. Under MCP the first end-to-end test needs an agent CLI, a working config and an inference, so the transport cannot be signed off before the harness is.

- **Under option 5, the container holds no persistent minos process.** Each call is connect, one operation, exit -- contagent's rule (section 2). The push lane is the broker writing to the agent's stdin, which `sanduk` holds on the host. "The in-container client died" stops being a failure mode, because between calls there is nothing to die.

**Three costs.**

- **The caller set, which the shim does not widen.** A binary on `PATH` looks like it widens the caller set from the agent to the container. It does not: the mounted socket does. Anything the container runs can dial that socket whether a shim exists or not -- including repository code, which the worker executes every time it runs `make test`, and which design.md 3 already treats as attacker-influenced. The shim makes that path trivial, not possible, and an MCP server on the agent's stdio does not close it. What moves the boundary is which option is taken, and section 5 scores it. The mitigation is the same either way: a non-root agent uid and a socket mode that admits only it (3.6), plus the local rate limit below.

- **Structured payload against shell quoting.** D18's payload is JSON, and a model writing JSON inside a shell command quotes it wrongly sooner or later. Rule: **the shim must never require the model to quote JSON on a command line.** Take `--payload-file`, or read the payload from stdin.

- **Schema advertisement.** A tool list is injected by the CLI and stays for the turn; a preamble is prompt text competing for attention across a long one. Two mitigations: one multicall binary, `minos`, with subcommands, so there is one name to remember rather than five; and a `--help` worth reading. This narrows the gap and does not close it, and it is the reason to build MCP second rather than not at all.

**A rate limit becomes load-bearing sooner.** A model calls a tool once per turn, deliberately. A shell loop calls a binary a thousand times a second. design.md 8.7's server-side rate is already required (D12); add a local one in the broker, so a runaway loop meets a fast refusal instead of spending the grant's quota on it.

Recommendation, refined: **one multicall binary as the first front end, MCP as the second, both over one client, one queue and one cursor.** What would flip it: every agent CLI we actually dispatch to serving MCP, and the preamble proving unreliable in practice. We have neither observation yet, and the shim is what lets us get them without first committing to a CLI.

### 3.2 One connection per call

"No job IDs, no separate cancel endpoint, no race conditions" (`hostbridge.md`, Signal handling). Cancelling is closing. Cleanup is the socket's `close` handler.

This applies to our tool lane and not to our room lane. The room socket is one connection with one cursor (agent-container-net.md 3) and must stay that way. But `await`, which blocks, is cheaper as its own connection than as a correlation id multiplexed onto the room socket, because then the timeout, the cancel and the teardown are one mechanism. The server-side deadline of design.md 8.5 is still required: a closed client connection does not decide a submission.

### 3.3 Lifetime binding is the whole stop mechanism

contagent binds three lifetimes in a chain: bridge to run, connection to process, process group to connection. Kill any link and everything below it dies, with no bookkeeping.

design.md 9 says revocation "loses the channel; the container may still run". agent-container-net.md 6 says the client "exits non-zero". Those are consistent only if the harness treats a revocation-closed socket as fatal rather than as a reconnect trigger -- and 3 requires it to treat an ordinary drop as a reconnect trigger. The two cases must be distinguishable on the wire: **a revocation close needs a reason code the client can read, or layer 4 is not a stop and reconnect-with-backfill fights it.** Add it to design.md 8.1.

### 3.4 An approval has a scope axis; our submission does not

A hostbridge decision is three-dimensional: `{allow|deny}` x `{once, session, always}` x `{these args, any args}`. Specific-args rules beat any-args rules. The state is the config file, so a decision made in a dialog and a rule written by hand are the same object.

Ours is one-dimensional. Every submission is one decision on one request. An agent needing thirty similar permissions asks thirty times, and design.md 8.7's rate limit then throttles the mechanism the design depends on.

This is `pma` policy, not a wire change: D18 keeps the payload opaque to the server, so the server cannot key a cache on it, and `pma` can. `pma` answers a submission from a cached decision keyed by `(grant, verb, subject)`, and the submission is still recorded and still visible in the room. What changes is who answers it.

Copy the axis, not the ceiling. `always` plus `any args` is a permanent blanket grant produced by one keystroke, which suits an operator at a terminal and not an unattended fleet. **Our top scope is the run**, because a grant expires with the run (design.md 8.1) and a decision that outlives its grant is a decision about a principal that no longer exists.

### 3.5 Validate arguments where the attacker cannot reach

Each registry entry sanitises before spawning: audio accepts exactly one path and strips all flags, `xdg-open` requires exactly one `http(s)` URL, clipboard blocks read flags (`hostbridge.js:40-76`). The allowlist names the command; the transform bounds what the command is asked to do.

Our analogue is missing at the one place it matters. D18 passes `payload` through unread, so `minosd` does not validate it, and design.md does not say who does. `pma` parses agent-authored JSON to read a permission request, its cost, a path and a command -- attacker-authored structure, consumed by the trusted tool that holds mint, ship and push. Name the validator: `pma`, at the boundary, before the payload reaches a decision, rejecting with a readable refusal. One sentence in design.md 8.5 closes it.

### 3.6 Identical paths, host identity

The project mounts at the same absolute path inside and out (`contagent:194`), and the entrypoint creates a user with the host UID and GID and drops to it with `runuser` (`entrypoint.sh:80`, `entrypoint.sh:27`).

Two consequences for `sanduk`, both cheap:

- Absolute paths in agent output -- `REVIEW.md`, a patch, a stack trace -- resolve on the host. Under D20 artifacts are transport and `pma` reads them, so a path the developer cannot open is a defect in the handoff.

- Files the agent writes to the worktree are owned by the developer, not by root.

It also repairs a line in agent-container-net.md 2: "the socket's file mode is not a substitute: a container running as root ignores it". A container whose agent process is not root does not ignore it. The mode is still not the boundary -- the grant is -- but it stops being a nullity, and getting there costs an entrypoint that drops privilege.

### 3.7 `pending` is a third reply

The client is told when a command is waiting on a human, so it can show that state (`hostbridge.md`, Protocol). Our `await` returns `approved`, `rejected` or `timed_out`, so an agent cannot tell "nobody has looked" from "this is slow".

Weak recommendation: one push when a submission is first read by a moderator. It only pays when the agent has other work, which a single-room worker often does not.

## 4. What fails there, and what it confirms here

**Policy state sits on the writable mount.** `.hostbridge.yaml` defaults to a relative path resolved against the host launch directory (`hostbridge.js:33`), which `contagent` bind-mounts read-write into the container at the identical path (`contagent:194`). Rules are re-read on every access check and matched on command and args alone.

Inference, not tested: a process in the container can append `{cmd: xdg-open, access: allow, args: any, scope: always}` to that file and never be prompted again. The same write deletes any `deny`. For contagent's threat model this is not a bug -- the container is trusted. For ours it is the failure agent-container-net.md 5 already rules out for the credential.

Extend that rule from the credential to the policy: **no file the agent can write may decide what the agent may do.** It covers the grant, the capability set, the decision cache of 3.4, and the Claude Code session store that design.md 9 requires to survive the container.

**There is no caller identity.** A grep for token, auth, secret and origin across both hostbridge files returns the two host constants and nothing else. Access is decided by command name, never by who asked. One container and one operator, so there is nobody to distinguish. We have three principals with different authority reading one room and cannot borrow this.

**The container is not sealed.** The `docker run` assembly at `contagent:194` sets no `--network`, and the shim reaches the host by `host.docker.internal`. The container has ordinary outbound internet. Hostbridge is not an escape hatch from a sealed box; it is a way to reach host *commands*, which is a different problem from reaching the host *network*. `sanduk`'s `--internal` starts from a stronger place, and nothing in contagent tests the part of our design that `--internal` creates.

## 5. Option 5: the broker on the host

agent-container-net.md 2 scores four ways in and recommends 3: a unix socket bind-mounted into the container, with the 7-operation minos client running inside it. Hostbridge's topology is a fifth, which the table does not hold.

**A per-run broker, on the host, owned by `pma`. It holds the grant and the minos connection. The container holds a shim that speaks to a mounted unix socket and knows nothing else.**

| | Container holds | `minosd` change | Sealing | Grant inside |
|-|-|-|-|-|
| 3 | socket file, the 7-op client, the grant | a unix listener | held | yes |
| 5 | socket file, a shim | none | held | no |

What it buys:

1. **The grant never enters the container.** agent-container-net.md 8's open question 5 -- where the grant is stored inside -- disappears. The mounted socket is the credential, and its holder is whoever `pma` mounted it into. Revocation is closing the socket, which needs neither `minosd` nor the agent's cooperation.

2. **`minosd` needs no unix listener.** Build-order step 1 goes away; the broker dials the ordinary TCP listener from the host.

3. **The envelope is implemented once.** The broker is the client `pma` already needs (design.md 11: "`pma` is Rust and needs a client"). agent-container-net.md 8 item 8 and design.md 11's "two wire implementations" are answered by construction rather than by a refactor of `go/internal/client`.

4. **The cursor and the pipes end up on the same side.** agent-container-net.md 4's argument is "one holder, or no guarantee" -- the process that pushes messages into the agent's stdin must be the process that holds the room cursor. Option 5 satisfies it with both on the host, so process ownership need not move into the container, and `sanduk`'s nine argv builders, nine stream parsers, watchdog and trace never move at all. Build-order steps 5 and 7 collapse into one.

5. **The container-side code becomes uninteresting, and need not persist.** 175 lines with no dependencies is contagent's measurement of the same job. Paired with the shim front end (3.1), each call is a process that connects, does one thing and exits, so no minos process runs in the container between calls and none can die there.

What it costs, stated fairly:

1. **A second protocol**, where option 3 reuses the minos wire end to end. Mitigation: make the broker-to-shim protocol MCP over the socket, and there is no second protocol -- the shim is a pipe, and MCP is the container-side interface agent-container-net.md 4 wants anyway.

2. **A host process per run that must not die.** A dead broker kills the channel exactly as a dead in-container client does, but it is one more thing for `pma` to supervise. `pma` already supervises the container.

3. **It does not remove grants.** Capabilities and the two denials still bound what the broker may do, because the broker's input is the agent's requests and the rule in design.md 3 is transitive. What changes is the order of the locks: an attacker must subvert the agent and then get past a fixed list of seven operations that is not a session's rights and cannot be widened by anything the agent says. That fixed list is contagent's `REGISTRY`, and it is worth copying even though their trust model is not.

4. **With no token, the socket's mode is the only gate.** The best property -- the grant never enters the container -- removes the second factor along with it.

| | Who can reach minos from inside | Gate |
|-|-|-|
| Option 4, no socket | the agent process alone | the process boundary |
| Option 3, socket plus a grant file | anything that can open the socket and read the grant | file mode on the grant file |
| Option 5, the socket is the credential | anything that can open the socket | file mode on the socket |

So 3.6 -- a non-root agent uid, and a socket whose mode admits only it -- is a prerequisite of option 5 rather than an improvement to it. Under option 3 it is an improvement, because the grant file is a second gate.

5. **It makes the container's interface `pma`-specific.** Option 3 leaves the container a first-class minos client, which is what a second consumer -- a person's assistant, a different harness -- would also want to be. If the goal is that anything in a box can join a room, option 3 is the general answer and option 5 is the specific one.

**Recommendation: option 5, with option 3 as the fallback** if broker supervision proves more expensive than a unix listener in `minosd`. The decisive point is the fourth of what it buys: it is the only option that keeps the cursor and the pipes together without moving process ownership across the container boundary, and moving that ownership was the largest unpriced item in the plan.

## 6. The question the lanes raise: does `say` exist?

Not a contagent lesson. It falls out of 3.1's lane table, and it is a larger decision than the front-end choice is.

`say` is in the tool list, and the client already reads every turn on stdout. So the agent's speech into the room has two possible sources.

| | The agent calls `say` | The client relays the turn's final message |
|-|-|-|
| The agent must know minos exists | yes | no |
| Works with a CLI that serves no MCP | no | yes |
| Room record against turn record | can diverge; the agent may say nothing | identical by construction |
| Messages per turn | zero to n, the agent's choice | exactly one |
| The rate and quota (D12, 8.7) | load-bearing | trivially satisfied |
| Carries a `payload` (D18) | yes | no |
| Speech before the turn ends | yes | no |
| Verbosity | the agent chooses what to post | needs a filter: the final assistant message, not the trace |

Relay fits D15 ("whole replies, not streamed") and D20 ("the room is the record") more closely than `say` does, and it removes the failure where an agent works for twenty minutes and posts nothing. What it cannot do is speak before the turn ends, which is the case the developer watching a long run most wants served.

Recommendation: **both, with relay as the floor.** The client relays each turn's final assistant message, so the room has a record whether or not the agent cooperates; `say` remains the way an agent speaks before its turn ends, and the way it attaches a payload. Neither subsumes the other. The cost of keeping both is that the rate limit stays load-bearing, which D12 already requires.

Decide it before the preamble is written (agent-container-net.md 4), because it changes what the agent is told it must do.

## 7. Changes to make

| Doc | Section | Change |
|-|-|-|
| agent-container-net.md | 2 | Add option 5 to the table and score it. Correct "why not 2" to say the objection is to a proxy that cannot parse, not to a broker that can |
| agent-container-net.md | 2 | Drop or qualify "the socket's file mode is not a substitute": true of a root container, false of one that drops privilege (3.6) |
| agent-container-net.md | 4 | State the lane weighting: the pipes carry the bulk, the tool lane carries only what has no other expression (3.1) |
| agent-container-net.md | 4 | Add the shim as a third front end beside MCP and the push lane; `exec` is the one interface every agent CLI speaks (3.1) |
| agent-container-net.md | 4 | Decide `say` against relaying the turn's final message; recommendation is both, relay as the floor (section 6) |
| agent-container-net.md | 7 | Weaken the case against option 4: "every agent CLI differs" is answered by the shim, not by MCP |
| agent-container-net.md | 9 | If option 5 is taken, steps 1, 5 and 7 change or disappear |
| design.md | 8.1 | A revocation close must carry a reason the client can tell from an ordinary drop (3.3) |
| design.md | 8.5 | Name `pma` as the validator of `payload`, at the boundary, with a readable refusal (3.5) |
| design.md | 6, D18 | Note that opacity to the server makes validation `pma`'s obligation, not nobody's |
| design.md | 10 | Add the decision scope: `pma` may answer a submission from a cached decision, ceiling of one run (3.4) |
| design.md | 11 | Add the rule: no file the agent can write may decide what the agent may do (section 4) |
| agent-container-net.md | 3 | A front end is not a policy point. The tool names may live in the container; the operation list and the capability set may not (2.1) |
| `sanduk` | -- | Mount the worktree at its host path; drop to a non-root uid in the entrypoint (3.6) |

## 8. To decide

Additions to agent-container-net.md 8.

9. Option 5 or option 3. This subsumes items 1, 3 and 5 of that list: under option 5 there is no socket in `minosd`, the client does not own the agent process, and no grant is stored in the container.

10. If option 5: is the broker-to-shim protocol MCP, or a narrower line protocol? MCP costs nothing extra if the shim is a pipe.

11. Does a cached approval decision (3.4) exist at all, and is its ceiling the run or the workflow? A workflow-scoped decision outlives three grants and needs a principal to be keyed to that is not the grant.

12. Does the container run as root? The answer changes whether the socket's file mode means anything, and it is a `sanduk` change either way.

13. May anything in the container reach the socket, or only the agent's uid? This is a property of the socket, not of the front end (3.1): repository code the worker executes can dial it either way. Under option 5 it is not a question but a prerequisite, because the socket is the credential (section 5).

14. Does `say` survive, or does the client relay the turn's final assistant message into the room (section 6)? Recommendation: both, relay as the floor. It decides the tool count and the preamble.
