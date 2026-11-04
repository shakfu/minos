# TODO

Items marked `R<n>` come from `REVIEW.md` (2026-09-27, at `30abc37`). See that file for probes and evidence.

## Critical

## High

## Medium

- [ ] Per-turn usage in the `turn` event. `claudeAdapter.Turn` (`go/internal/broker/relay.go`) reads only `type` and `result`. The `result` record also carries `total_cost_usd`, cumulative for the session on Claude Code 2.1.299 (CHANGELOG, `make host-run`), so a turn's cost is the difference from the previous record. `usage`, `duration_ms` and `num_turns` are probably there too; check whether each is per turn or cumulative in `agent.jsonl` from `make host-run`. This gives quotas a measured number to enforce. Use the CLI's reported figures. [agentpipe](https://github.com/kevinelliott/agentpipe) estimates tokens from text length instead (`pkg/utils/tokens.go`).

- [ ] Decide whether `Adapter` supports CLIs other than Claude. `Adapter` assumes one long-lived process that takes stdin frames and interrupts on the same pipe. Most CLIs are one-shot (`codex exec`, `opencode run`, `gemini -p`). A one-shot CLI fits only if the broker restarts it each turn and resumes its session by id; `Interrupt` would then kill the process. agentpipe's `pkg/adapters/` records the flags, stdin handling and resume options for 15 CLIs; `amp.go` shows session resumption.

## Low

- [ ] Agent version in the `ready` event. The broker reads the agent CLI's version inside the image and reports it, so a run that breaks after an image rebuild shows what changed. Wait until a real agent runs in a container (see the gap below).

- [ ] Export a room's archive as Markdown, for a PR description or a review. Check first whether archival search already covers it.

- [ ] Sync cost, measured: `RoomsFor` takes about 100 us per room (10 ms at 100 rooms, 72 ms at 1,000), from `describeAll`'s 4-5 queries per room on one connection (`timeline.go:1108`, `:453`). Not worth a change for a local server; it matters after a restart, when every client syncs at once.

- [ ] Gap: no real agent has run in a container. `make container` proves the socket path: the shim, a bind mount, the host uid. Not yet: `sanduk` running the container with the worktree mounted and its uid dropped (implementation-plan item 3), and an agent CLI driving the shim.

- [ ] Gap: `Window` is reported by `status` and enforced by nothing (`broker.go`, `Config.Window`). Grants make it a rule (implementation-plan item 5).

- [ ] Gap: every phase 0 run speaks as the one `worker` account (implementation-plan 2.4). A room cannot tell two runs apart; the broker tells its submissions apart by id alone.

- [ ] Gap: session revocations are kept in memory, so a server restart forgets a logout. The 7-day session limit still applies.
