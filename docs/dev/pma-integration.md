# pma integration

2026-10-14. How minos and [pma](https://github.com/shakfu/pma) can work together, read against pma at `a69f3eb`. A proposal for approval. Built so far: `minosb -events` and `minosb -task` (section 4.2).

## 1. Summary

Each project's documents describe an older version of the other. pma's recent async work also takes a position that limits what minos should be for it: progress is observed, not pushed by agents. The cheapest useful join needs no container and no `minosd` change. One `minosb` change blocks every option that puts a pma run in a room: the agent's stream must reach pma's log.

## 2. What changed in pma

Sources: pma `CHANGELOG.md` (Unreleased, 0.6.0), `docs/dev/async_dispatch.md`, `docs/dev/sanduk-integration.md`, `crates/pma-core/src/{agent,worker,jobs}.rs`.

| Change | Relevance to minos |
|-|-|
| `--detach`, a job queue, and a runner that runs one job at a time | A job is the unit a person waits on. |
| `awaiting-approval` jobs, and `pma job approve <id>` | A plan approval is a decision, like a minos submission. |
| `notify`: a command run per finished job with `PMA_JOB_ID`, `PMA_JOB_STATE`, `PMA_JOB_SUMMARY` | A ready hook for posting to minos. |
| The `claude` worker writes stream-json to a log, and `pma watch` reads it | pma reads the agent's stdout. `minosb` keeps it for itself. |
| A working `sanduk` worker, run end to end with sanduk-rs (`sanduk-integration.md` section 5) | The container path exists without minos. |
| pma and sanduk stay separate executables; no library link | The same rule minos chose for `minosb` (implementation-plan 2.2). |
| `pma web` and a ten-tab TUI over one typed API (`pma_core::api`) | A second front end for approvals besides the minos TUI. |
| Agents draft changelog entries in `.pma/changelog.md` | Not relevant. |

Two facts about the code:

- **No supervising model.** `pma-agent` appears in no Rust source. [design.md](design.md) section 2 assumes one.
- **Every human gate is on the host:** plan approval, `pma review --approve/--reject`, `pma pr`, `pma push` (pma `workflows.md` section 15.13).

## 3. Where the two disagree

### 3.1 pma's documents describe a rejected minos design

pma `docs/dev/using-containers.md` and `implementation-plan.md` items 2b.6-2b.8 plan for:

- `minosd` attached to the sealed network;
- a minos client inside the container;
- a Rust wire client in pma (`open`, `send`, `history`, `submission.*`, `grant.*`);
- grants and a refused `/vfs` as blockers.

minos rejected the first two in [recommended-architecture.md](recommended-architecture.md) section 11. [implementation-plan.md](implementation-plan.md) rulings 2.1 and 2.2 replace the third: pma starts `minosb` as a child and writes no wire client. Grants stay phase 1 work, but `minosb`'s six operations already keep a container away from `/vfs`. pma's documents should be updated to match.

### 3.2 Process model

| Topic | pma | minos | Consequence |
|-|-|-|-|
| Worker stdin | `WATCHDOG` runs the worker with stdin from `/dev/null` and keeps the pipe as a kill switch (`agent.rs`) | `minosb` reads `{"op":"interrupt"}` on stdin | pma cannot use `minosb`'s stdin interrupt. A room `/interrupt` from a listed interrupter still works. |
| Prompt | An argument: `claude -p {prompt}` (`worker.rs`, `Worker::claude`) | The first stdin frame, written by a wrapper (README, "A container run, by hand") | A pma worker template cannot express the wrapper. Resolved by `minosb -task`. |
| Agent output | Cost and summary from the last `result` line in the log; `pma watch` reads the log live | `minosb` keeps the agent's stdout and prints its own control events | A run under `minosb` loses its cost, its summary, and its watch view. The parser takes the last JSON line (pma TODO L10), and `minosb` writes `exited` and `stopped` last. This blocks options 4.2 and 4.3. |
| Process group | The watchdog kills the group on exit or timeout | `minosb`'s death ends the run | Compatible. `minosb` is one more process in the group. |
| Parallel agents | `max_parallel` agents per job | One `minosb`, one socket, one room per run | Compatible. Each run gets its own broker. |

### 3.3 Trust

pma `async_dispatch.md`, "Why observe rather than let agents push":

> A push channel is a socket or token inside the agent's environment. `pma` strips that environment of credentials; this would add one back for every worker.

The `minosa` socket is such a channel. It holds no credential, but it can speak in the room and submit. The two positions fit if pma treats anything a worker says in minos as advice and keeps verify as the only evidence. pma already treats an agent's own report that way.

### 3.4 Identity

minos already has every field pma's model needs (wire-contract section 12):

| pma | minos |
|-|-|
| project | project |
| project tags | project tags |
| `task_key` | `task` on a room with `scope: task` |
| run id | not modelled; one room may carry several runs |

pma records no room id today. [pma_feedback.md](pma_feedback.md) 7.4 calls that a pma bug.

## 4. Options

Cheapest first. Each is usable alone.

### 4.1 Approvals and notices through minos

- `notify` posts each job's summary to a channel.
- An `awaiting-approval` job, or a run that reaches `ready`, becomes a submission in a moderated channel.
- An approval runs `pma job approve <id>` or `pma review <id> --approve`. A rejection runs `pma review <id> --reject`.

| Needs | Where |
|-|-|
| A non-interactive client: `minos post` and `minos submit --await`, or one bridge binary. Today minos has the TUI, and `minosa`, which needs a broker. | minos |
| A way to find waiting jobs and ready runs: `pma job --json` (an open question in `async_dispatch.md`) or polling the text output | pma |

- **Gain.** Approval from any device, an archived record of each decision, and pma's verbs on the moderation queue minos already has.
- **Cost.** It partly duplicates `pma web`. It adds a minos dependency to a host-only flow that works today.

### 4.2 Talk to a running dispatch, with the agent on the host

A pma worker template `claude-minos`:

```text
minosb -server ... -user worker -room {room} -interrupters demo -task {prompt} -socket {socket} -- \
  claude -p --input-format stream-json --output-format stream-json --verbose ...
```

| Needs | Where |
|-|-|
| `-task TEXT`: `minosb` writes the first stdin frame itself, so no wrapper is needed. Text, because pma fills `{prompt}` as one argument. Built 2026-10-14 | minos |
| The agent's stream passed through to stdout unchanged, with control events moved to `-events FILE`. Built 2026-10-14 | minos |
| A room filed under project and task. Proposed in [run-rooms.md](run-rooms.md): pma founds it with `minos room ensure` and passes `{room}`; `minosb` is unchanged | minos, pma |
| The template, a socket path outside the worktree, and the room id on the run row | pma |

- **Gain.** The developer reads a pma run as it happens, corrects it, and interrupts it from the minos TUI. `make host-run` already proves the relay, held messages and both interrupt lanes with a real `claude`.
- **Cost.** About 150 lines in `minosb` with tests, and one template in pma. The agent stays unsandboxed, as pma's `claude` worker is today.

### 4.3 The same, in a container

Option 4.2 with `sanduk run` as `minosb`'s run command.

| Needs | Where |
|-|-|
| Everything in 4.2 | minos, pma |
| Phase 0 item 3: stdin passed to the agent with `--input-format stream-json`, and a socket mounted outside the work mount with a mode for the agent's uid | sanduk |

Not verified: sanduk is not installed on this machine. The local sanduk-rs tree holds `sanduk-container`, whose run spec has a `stdin` option, but not the `sanduk` CLI crate. Whether `sanduk run` exposes stdin and an extra mount is unknown.

### 4.4 Shared identity

- pma files each room under the pma project, with the task key as `task`.
- pma mirrors project tags into minos.
- pma stores the room id on the run row, and recovers it from `sync` if the row is lost.

This is a precondition for 4.2 and 4.3, and costs little in either repository.

### 4.5 Not proposed

- **A2A between pma and minos.** pma starts `minosb` itself and needs no protocol in between. `minosg` ([agent2agent.md](agent2agent.md)) is for orchestrators minos does not start.
- **A Rust wire client in pma.** Ruled out by implementation-plan 2.1.
- **Using minos for progress.** pma reads the agent's log, and `async_dispatch.md` explains why. The relay posts whole turns to the room. It is not a progress feed.

## 5. Recommendation

1. **The `minosb` changes in 4.2.** Stream passthrough first, because it blocks 4.2 and 4.3. Then `-task`, then filing the room. The first two are built. Filing needs no `minosb` change; see [run-rooms.md](run-rooms.md). All are testable in Go with the scripted fake agent.
2. **4.4, then the pma template.** A real pma dispatch whose room shows in the minos TUI is the phase 0 exit test of [implementation-plan.md](implementation-plan.md), without the container.
3. **4.1, only if approvals outside `pma web` are wanted.**
4. **4.3, when sanduk has stdin passthrough and the socket mount.**
5. **Update pma's `using-containers.md` and implementation-plan 2b.6-2b.8** to the broker design.

### An alternative framing

pma already has control and approvals on the host. minos could serve it as two things only: a durable conversation record, and a lane for correcting a run while it works. It would not be a control plane. Under that framing, 4.1 is duplication, and 4.2 is the whole integration.

## 6. Open questions

1. **Do you need to correct a run while it works?** If approvals and notices are enough, 4.1 alone covers it, and 4.2 and 4.3 wait.
2. **What grain should a room have?** pma has 1031 open tasks, and minos cannot delete a room ([pma_feedback.md](pma_feedback.md) section 2). The candidates are one room per task, per workflow instance, or per project. A workflow instance bounds the count by work done, not by backlog.
3. **Who is the interrupter?** pma cannot write `minosb`'s stdin. Either the developer's account goes in `-interrupters`, or pma gets an account and posts `/interrupt` itself.
4. **Does pma ever run `minosd`?** [pma_feedback.md](pma_feedback.md) 7.1 says no: it should be an operator's service. Then, when the server is down, does a run fail (implementation-plan section 3, answer 1) or fall back to running without a room?
5. **Does a run's summary come from pma's parser or from the room?** With passthrough, both exist. They should agree, and pma's log should be the authority.
