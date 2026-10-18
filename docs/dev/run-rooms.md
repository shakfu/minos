# Who founds a run's room

2026-10-14. Status: proposed, for approval. Follows [pma-integration.md](pma-integration.md) section 4.2, whose last `minosb` item asked for "room creation filed under project and task".

## Decision

`minosb` keeps taking `-room <id>` and founds nothing. pma founds the room, as an administrator, through a new one-shot client command:

```text
minos room ensure -project pma -task 23 [-invite worker]...
```

The command prints the id of the open room filed under that project and task. It creates the room first if none exists. pma calls it before it starts the worker, stores the id on the run row, and passes it to the worker template as `{room}`.

When phase 1 grants exist, the same step mints the run's grant beside the room.

## The question

A run's room should be filed under the pma project, with the pma task as `task` (wire-contract section 12). Who creates it, and how does `minosb` learn its id?

## Facts that decide it

| Fact | Source |
|-|-|
| Founding a filed room (`create` with `project`) and filing one (`project.file`) are administrator-only. | wire-contract sections 5 and 12, `messaging.go:489` |
| `open`, the ad-hoc room anyone may raise, cannot be filed by its founder. | wire-contract section 12 |
| A permanent room's title is unique within its project. The check runs under a lock. | `messaging.go:504-508` |
| The founder is always granted the room. | `grantsFor`, `messaging.go:524` |
| The broker founds nothing: "Both ids come from the dispatcher; the broker founds neither." | `internal/broker/broker.go`, package doc |
| pma founds rooms, as administrator. | [design.md](design.md) D5; the "pma (trusted) -- admin" line in [recommended-architecture.md](recommended-architecture.md) section 2 |
| A pma worker template fills `{prompt}`, `{dir}`, `{model}`, `{budget}`, `{timeout}` and `{extra}`. It has no `{project}`, `{task}` or `{room}`. | pma `crates/pma-core/src/worker.rs:187` |
| pma passes no project or task to a worker through the environment. | pma `crates/pma-core/src/*.rs` |
| pma strips credentials from a worker's environment. | pma `async_dispatch.md`; pma `design.md` |

## Options

### A. `minosb` founds and files the room

`minosb -project P -task K` creates or finds the room itself.

- **For.** One step. pma passes two values and needs no minos client.

- **Against.**
  - The worker account must be an administrator. Every run then holds an admin session: file writes, settings, founding and inviting. The six-operation bound still keeps the container out, but the broker becomes the only barrier between a run and every admin operation.

  - It reverses the broker's recorded rule that it founds nothing.

  - Messages relayed under an admin account read as the administrator's in the room. The phase 0 shared account already blurs authorship (implementation-plan 2.4); this makes it worse.

### B. `minosb` looks the room up by project and task

`minosb -project P -task K` finds the one open room filed there that the worker can read. Something else founds it.

- **For.** pma passes values it already has, not a room id it must store.

- **Against.**
  - Founding is still needed, so this adds a lookup without removing a step.

  - Task labels are not unique on the server. Several rooms may name the same task, so the lookup needs a tie rule (open, newest) or a refusal.

  - The lookup is done twice, once by the founder and once by `minosb`.

### C. pma founds the room through a minos command, and passes its id (proposed)

pma runs `minos room ensure` with its own administrator credential, then fills `{room}`.

- **For.**
  - The worker account stays ordinary, and the broker founds nothing.

  - The admin credential stays in pma's process and never reaches a worker's environment.

  - pma stores the room id on the run row, which [pma_feedback.md](pma_feedback.md) 7.4 asks for anyway.

  - The command also serves [pma-integration.md](pma-integration.md) 4.1, notices and approvals, which needs a non-interactive client regardless.

- **Against.**
  - A change in pma: a step before the worker starts, a `{room}` placeholder, and a column on the run row.

  - A new subcommand in `cmd/minos`, which today is only the TUI.

  - pma holds an admin minos credential. That is the stated design, but it is new for pma.

### D. `minosb` takes a second, admin credential for setup

`minosb` logs in as an administrator to ensure the room, then logs out and runs as the worker.

- **For.** One step, no pma code beyond placeholders.

- **Against.** The admin credential sits in the worker's environment, which pma strips. `minosb`'s `childEnv` removes `MINOS_PASSWORD` and `MINOS_USER`; a second credential would need the same care, forever. Two credentials in one process, when the design gives each process one.

### E. Wait for phase 1 grants

A grant mint founds the room and returns a credential scoped to it.

- **For.** The final shape. The mint is the step C runs today.

- **Against.** Phase 1 is a wire change with conformance work (implementation-plan section 5). It blocks the integration for no gain over C, which reaches the same shape.

### 0. No founding, for a pilot

The developer creates one room by hand, and a pma worker template names it with a fixed `-room`.

- **For.** No code in either repository. Enough to watch the first real runs.

- **Against.** Every run of that template shares one room, across projects and tasks. That is the cross-repository mixing D10 forbids. Acceptable for a supervised pilot on one project only.

## Why C

C is the only option that keeps each recorded rule:

- the worker account stays ordinary;

- the broker founds nothing;

- no credential enters a worker's environment;

- pma founds rooms, as D5 says.

Its cost is a pma change that pma needs anyway, for 4.4.

Option 0 is worth taking first, while C is built. It answers whether a running pma dispatch is worth reading in a room before either repository pays for founding.

## What `minos room ensure` does

1. Log in with `MINOS_USER` and `MINOS_PASSWORD` from the environment, as `minosb` does. Refuse a non-administrator with the server's own sentence.

2. Create the project if it does not exist. Optionally apply `-tag` values, for 4.4's tag mirroring.

3. `create` a permanent room titled `task/<task>`, filed under the project with `scope: task` and `task: <task>`, inviting each `-invite` principal (`worker`, or `@group` for D8's project group).

4. If the server answers that the title is taken, find that room in `sync` by project and title, and invite any missing principal.

5. If the found room is closed, reopen it. A rework reads what the last run was told (D5).

6. Print the room id on stdout and nothing else. Exit 2 on a refusal, with the sentence on stderr.

A title may hold any character. `checkName` limits only its length (`messaging.go:72`), and a slash in a title is allowed. A pma key such as `pma:23` gives the title `task/23` under project `pma`.

Title uniqueness makes step 4 safe when two dispatches race, because the server checks the title under a lock. A task with several rooms cannot arise through this command, though a person can still create one by hand.

## What pma changes

| Change | Where |
|-|-|
| A `{room}` placeholder, filled from the run row | `worker.rs`, `build` |
| Before starting a worker whose template names `{room}`: run `minos room ensure` with pma's credential, and store the id on the run row | `dispatch.rs`, schema migration |
| A minos credential in pma's settings, passed to `minos` through the environment of that one subprocess | `settings.toml` |
| A run-row column for the room id, shown by `pma review` and the front ends | `store.rs`, `screen.rs` |

The worker template from [pma-integration.md](pma-integration.md) 4.2 then reads:

```text
minosb -user worker -room {room} -interrupters demo -task {prompt} -socket {socket} -events {events} -- \
  claude -p --input-format stream-json --output-format stream-json --verbose ...
```

`{socket}` and `{events}` are two more placeholders this needs: a socket path outside the worktree and under 103 bytes, and an events file beside the run's log.

## Grain

This fixes one room per (project, task). [design.md](design.md) D5 prefers one room per workflow instance, with the workflow declaring its scope. `ensure` takes the scope as data, so a workflow run would pass `-scope project` or a workflow-instance label as the task. The command does not need to change when D5's grain arrives.

## Open questions

1. **Does pma hold an admin minos account, or a narrower dispatcher role?** minos has only admin and user today. A dispatcher role is phase 1 work.

2. **Who closes a room?** A run that ends `approved` or `rejected` leaves its task open in TODO.md. Closing on task completion fits D5's deletion trigger, but it belongs to pma's settle step, not to `ensure`.

3. **Which credential runs `ensure` on a machine shared by several developers?** Phase 0 assumes one.
