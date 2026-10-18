# TODO

Items marked `R<n>` come from `REVIEW.md` (2026-09-27, at `30abc37`). See that file for probes and evidence.

## Critical

## High

## Medium

## Low

- [ ] Sync cost, measured: `RoomsFor` takes about 100 us per room (10 ms at 100 rooms, 72 ms at 1,000), from `describeAll`'s 4-5 queries per room on one connection (`timeline.go:1108`, `:453`). Not worth a change for a local server; it matters after a restart, when every client syncs at once.

- [ ] Gap: no real agent has run in a container. `make container` proves the socket path: the shim, a bind mount, the host uid. Not yet: `sanduk` running the container with the worktree mounted and its uid dropped (implementation-plan item 3), and an agent CLI driving the shim.

- [ ] Gap: `Window` is reported by `status` and enforced by nothing (`broker.go`, `Config.Window`). Grants make it a rule (implementation-plan item 5).

- [ ] Gap: every phase 0 run speaks as the one `worker` account (implementation-plan 2.4). A room cannot tell two runs apart; the broker tells its submissions apart by id alone.

- [ ] Gap: session revocations are kept in memory, so a server restart forgets a logout. The 7-day session limit still applies.
