# TODO

Items marked `R<n>` come from `REVIEW.md` (2026-09-27, at `30abc37`). See that file for probes and evidence.

## Critical

## High

- [ ] Decide who sends an interrupt. `claudeAdapter.Interrupt` encodes it; nothing sends it. Measured on Claude Code 2.1.284, one run per case (`scripts/probe-interrupt.sh`): the interrupted turn ends with one `result` (`error_during_execution`, `result: null`), so the relay's turn count holds. A queued user frame then runs as its own turn; with `cancel_queued` it does not, yet `cancelled` came back empty. The CLI exits 1 when its last turn was interrupted, which `minosb` reports as `exited` with code 1. `minosb` has no control input from the dispatcher, so a dispatcher-sent interrupt needs one (a signal, or a line on stdin). Until decided the broker holds and reports `held`.

## Medium

## Low

- [ ] The terminal client does not redraw while a key's request is outstanding (up to 10 s). ^C works (R17), but the handlers still block: moving the requests off the handlers, with results applied back on the loop, is the remaining refactor.

- [ ] If the agent stops reading stdin, the relay's write blocks `Deliver`, and `held` events stop. A write deadline is not the fix: a frame can exceed the 64 KiB pipe buffer, so a timed-out write leaves half a JSON line and every later frame is malformed; the only safe response is closing stdin, which ends the run. `minosb` exits without waiting on `Deliver`, so today the cost is the missing `held` events alone.

- [ ] Sync cost, measured: `RoomsFor` takes about 100 us per room (10 ms at 100 rooms, 72 ms at 1,000), from `describeAll`'s 4-5 queries per room on one connection (`timeline.go:1108`, `:453`). Not worth a change for a local server; it matters after a restart, when every client syncs at once.

- [ ] Gap: no real agent has run in a container. `make container` proves the socket path: the shim, a bind mount, the host uid. Not yet: `sanduk` running the container with the worktree mounted and its uid dropped (implementation-plan item 3), and an agent CLI driving the shim.

- [ ] Gap: `Window` is reported by `status` and enforced by nothing (`broker.go`, `Config.Window`). Grants make it a rule (implementation-plan item 5).

- [ ] Gap: every phase 0 run speaks as the one `worker` account (implementation-plan 2.4). A room cannot tell two runs apart; the broker tells its submissions apart by id alone.

- [ ] Gap: session revocations are kept in memory, so a server restart forgets a logout. The 7-day session limit still applies.
