# TODO

Items marked `R<n>` come from `REVIEW.md` (2026-09-27, at `30abc37`). See that file for probes and evidence.

Items marked `pma backend` make minos the place pma's agents run, in place of herdr. pma decided this on 2026-11-20: `pma integrate minos` becomes the only backend while it is on, with no herdr pane beside it. Today pma asks herdr to open a terminal per run, close it, bring it to the front, report the agent's state, and run person-driven (`I`) and take-over (`T`) sessions. pma's side is `docs/dev/pma-minos-integration.md` in pma.

## Critical

## High

- [ ] pma backend: person-driven runs, for pma's `I`. `minosb` runs the agent with or without `-task`, with no linger and no timeout. The person ends the run from the room: `/end` closes stdin, the agent exits, and `stopped` reports reason `ended`. Done when a scripted fake agent takes a person's message, then `/end`, and exits 0.

- [ ] pma backend: take over a finished run, for pma's `T`. This is a person-driven run in the earlier run's room, whose command resumes the agent's session (`claude --resume ID`). minos reports each run's session id in a `session` event from the adapter: claude's `system` `init` record carries `session_id`. pma then does not parse it from each agent's stream. The room shows the earlier run's messages above the take-over. Done when a run and its take-over share a room and a session.

- [ ] pma backend: agent state. pma's TUI and web show each run's state and say when an agent needs a person; herdr reports `idle`, `working`, `blocked` and `done`. `minosb` writes a `state` event to `-events` on each change: `working` when a frame is pushed, `waiting` when a person-driven turn ends, `done` on exit. The client lists a `waiting` room under what waits on you in `OVERVIEW`. Check first how much the `idle` and `turn` events already give.

- [ ] pma backend: adapters for pma's workers (decided 2026-11-20: minos runs agents other than Claude). Replaces the Medium item that asked whether to. There are two kinds:
  - stream: one long-lived process that takes stdin frames, as `claudeAdapter`;
  - one-shot: the broker runs the CLI once per turn and resumes its session by id. `Interrupt` kills the process.

  pma ships `claude`, `codex` (`exec`, `exec resume`), `opencode` (`run --session`), `pi`, `omp`, `gilda`, `minima`, `hax`, `myra` and `hilda`. Each adapter states how a turn's text is passed, what ends a turn, the session id, the resume flag, and the cost where reported. [agentpipe](https://github.com/kevinelliott/agentpipe) `pkg/adapters/` records these for 15 CLIs. A worker without an adapter keeps today's side broker and `minosa`.

- [ ] pma backend: open a room for its person, for pma's `H` and an `I` started in front. `minos -room ID` starts the terminal client in that room. `-exit-on-close` returns when the run stops, so pma can run the client in the foreground as it runs an editor. When a client is already open elsewhere, a server request moves it into the room, so `H` in pma's web needs no new terminal.

- [ ] pma backend: the server as a service, and a check. With `minosd` down, every pma run starts without a room or fails. Add a launchd plist and a systemd unit (`make install-service`). Add `minos ping`, whose exit status tells pma one of: no server, refused credentials, or a server too old for the flags pma passes.

## Medium

- [ ] Per-turn usage in the `turn` event. `claudeAdapter.Turn` (`go/internal/broker/relay.go`) reads only `type` and `result`. The `result` record also carries `total_cost_usd`, cumulative for the session on Claude Code 2.1.299 (CHANGELOG, `make host-run`), so a turn's cost is the difference from the previous record. `usage`, `duration_ms` and `num_turns` are probably there too; check whether each is per turn or cumulative in `agent.jsonl` from `make host-run`. This gives quotas a measured number to enforce. Use the CLI's reported figures. [agentpipe](https://github.com/kevinelliott/agentpipe) estimates tokens from text length instead (`pkg/utils/tokens.go`).

- [ ] pma backend: progress in the room. A herdr pane shows tool calls, edits and test output; a room shows only each turn's reply. `minosb -progress` posts one line per tool use (`Edit src/x.rs`, `Bash cargo test`), rate-limited. They are posted as a kind that clients show muted and that notifies nobody, so it needs author or message kinds.

- [ ] pma backend: permission requests in the room. In a person-driven run, `claude` asks before a tool its permission mode does not allow, and stream-json has no terminal to ask on. `minosb` serves the `--permission-prompt-tool` MCP tool and posts each request to the room; the person allows or denies it. Until then pma runs `I` only under `auto` or `acceptEdits`.

- [ ] pma backend: a credential per run. Every pma run now goes through minos, so the shared `worker` password matters for all of them: an agent with the same uid can read it from `minosb`'s environment. pma records this as issues 5 and 6. `room ensure` returns a token for that one room, which `minosb` uses in place of `MINOS_PASSWORD`. It also tells runs apart (the gap "every phase 0 run speaks as the one `worker` account").

- [ ] pma backend: close a room. pma closes a herdr tab when a run is reviewed. minos cannot delete a room, so with one room per task pma's store can found up to 1031. `minos room close ID` takes a room out of the lists, keeps it searchable, and lets pma close it on review.

- [ ] pma backend: a person-driven run outlives a server restart. `unseated` stops the agent after 45 s. In a person-driven run, keep the agent, reconnect, and hold the person's messages on the server until it does. Headless runs keep the 45 s rule.

## Low

- [ ] pma backend: the contract pma reads, written down. Document each `stopped` reason (`signal`, `exited`, `torn`, `unseated`, `ended`) and each `minosb` exit code, so pma maps each to a run outcome. Today pma assumes `unseated` means failed, untested.

- [ ] pma backend: a check of each mode pma uses. Drive headless, person-driven and take-over runs through `minosb`, as pma does, with the scripted fake agent: `make pma-backend`. pma's `make minos-check` covers headless only.

- [ ] Agent version in the `ready` event. The broker reads the agent CLI's version inside the image and reports it, so a run that breaks after an image rebuild shows what changed. Wait until a real agent runs in a container (see the gap below).

- [ ] Export a room's archive as Markdown, for a PR description or a review. Check first whether archival search already covers it.

- [ ] Sync cost, measured: `RoomsFor` takes about 100 us per room (10 ms at 100 rooms, 72 ms at 1,000), from `describeAll`'s 4-5 queries per room on one connection (`timeline.go:1108`, `:453`). Not worth a change for a local server; it matters after a restart, when every client syncs at once.

- [ ] Gap: no real agent has run in a container. `make container` proves the socket path: the shim, a bind mount, the host uid. Not yet: `sanduk` running the container with the worktree mounted and its uid dropped (implementation-plan item 3), and an agent CLI driving the shim.

- [ ] Gap: `Window` is reported by `status` and enforced by nothing (`broker.go`, `Config.Window`). Grants make it a rule (implementation-plan item 5).

- [ ] Gap: every phase 0 run speaks as the one `worker` account (implementation-plan 2.4). A room cannot tell two runs apart; the broker tells its submissions apart by id alone.

- [ ] Gap: session revocations are kept in memory, so a server restart forgets a logout. The 7-day session limit still applies.
