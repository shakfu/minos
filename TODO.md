# TODO

Items marked `R<n>` come from `REVIEW.md` (2026-09-27, at `30abc37`). See that file for probes and evidence.

## Critical

## High

- [ ] Decide who sends an interrupt. `claudeAdapter.Interrupt` encodes it; nothing sends it. Measured on Claude Code 2.1.284, one run per case (`scripts/probe-interrupt.sh`): the interrupted turn ends with one `result` (`error_during_execution`, `result: null`), so the relay's turn count holds. A queued user frame then runs as its own turn; with `cancel_queued` it does not, yet `cancelled` came back empty. The CLI exits 1 when its last turn was interrupted, which `minosb` reports as `exited` with code 1. `minosb` has no control input from the dispatcher, so a dispatcher-sent interrupt needs one (a signal, or a line on stdin). Until decided the broker holds and reports `held`.

## Medium

- [ ] R8. `messages -since N` raises `delivered` to `N` past the room's end; `progress` follows (`broker.go:289-291`, `:343-347`). Refuse a `since` above `delivered`.

- [ ] R9. `MarkRead` stores any sequence (`timeline.go:1374`). Clamp to `high_seq` or refuse.

- [ ] R10. A moderator outside a restricted channel's audience can read its queue, approve and publish (`messaging.go:1465-1472`, `:1610`, `:1686-1692`, `:1780-1790`). Decide the rule: refuse ineligible appointments and dismiss on lost eligibility, or state that appointment grants access. Write it in `chat-concepts.md` section 5; add a conformance test.

- [ ] R14. `markRead` sends `space.LastSeq`, not the last sequence in the local log (`app.go:2471-2473`). Send the last displayed sequence.

- [ ] R15. No bracketed paste: a pasted newline submits, a pasted `/` line runs, a pasted character answers a pending confirmation. Call `screen.EnablePaste()`; treat text between paste events as input only.

- [ ] R16. List cursors are row indexes (`app.go:160-170`); a push above the cursor moves the selection. Hold the selection by id.

- [ ] R17. `Enter`, `Exit`, `Send`, `Open` and commands block the key loop for up to 10 s (`app.go:532`, `:542`, `:837`, `:924`; `protocol.go:36`). `^C` is not processed meanwhile.

- [ ] Tests: add `-race` to `make test`. Add a server-restart-under-broker test (`implementation-plan.md:150`). Find why `GOCOVERDIR` coverage of the conformance binary writes no data (candidates: env handling, `SIGTERM` then `Kill` in `go/conformance/harness.go:154-158`).

## Low

- [ ] R18.1. `room.close`, `project.file`, `archive.set`, `channel.dismiss` on `system` are unguarded in `chat` (`chat.go:275-340`); `messaging` has no guard of its own.

- [ ] R18.2. `SetState` posts its event with a nil audience (`messaging.go:1333`); each client pays one `history` request per close.

- [ ] R18.3. `chat` keeps occupancy ids after `enter` releases them in `timeline` (`chat.go:371-378`). Comment at `chat.go:43` is stale.

- [ ] R18.4. `DeleteProject` runs 3 statements outside a transaction (`timeline.go:762-773`). `CreateRoom` checks then inserts with no unique index (`messaging.go:495-505`).

- [ ] R18.5. `Sweep` stops at the first failed delete and skips the archive pass (`messaging.go:857-860`).

- [ ] R18.6. `minosa say "- done"` exits 2: `split` passes the body to the flag parser (`go/cmd/minosa/main.go:162`).

- [ ] R18.7. `submit` stores the pending submission after the request returns (`broker.go:378-380`); an earlier decision push is overwritten.

- [ ] R18.8. `decided` adopts any same-author submission in the same channel (`broker.go:239-243`); two runs sharing a channel can `await` each other's.

- [ ] R18.9. `/leave`, `/unsubscribe` keep `viewSpace` with no selection; `/subscribe` from `OVERVIEW` targets an off-screen channel (`app.go:1212`, `:1652`, `:1665`).

- [ ] R18.10. `Listen` sets the socket mode after binding (`link.go:185-189`); the comment says before.

- [ ] R18.11. 13 of 14 `link.Refuse` calls pass an empty rule; `Status.Expiry` is never set. `implementation-plan.md:147`, `:161` promise both.

- [ ] R18.12. Terminal client: `-password` has no env or file alternative (`go/cmd/minos/main.go:29`); `http://` accepted without warning; raw HTTP error body printed unsanitized (`transport.go:89`, `main.go:47`).

- [ ] R18.13. `lastAt` clones up to 1,000 messages per room several times per frame (`app.go:674`). Measure.

- [ ] `describeAll` runs 4-5 queries per room on one connection during sync (`timeline.go:1108`, `:453`). Measure.

- [ ] `staticcheck` S1011 at `go/cmd/demo/main.go:178`.

- [ ] Docs: README omits `minosb`, `minosa`, `broker`, `link`, `testserver`, `cmd/demo`; `make go` builds 4 binaries, not 2; Tab contradicts itself (lines 53, 157); "Five things" has 6 bullets.

- [ ] Docs: cheatsheet lacks `/projects`, `/project new|tag|untag|file|rm`, `/close`, `/reopen`, ^A, the `[project/]` prefix of `/create`.

- [ ] Docs: diagrams missing. Commit the SVGs or correct `Makefile:53-55`; `agent-container-net.md:17`, `:47` embed absent images.

- [ ] Docs: document `MINOS_TIMELINE_DB`, `MINOS_USER`, `MINOS_PASSWORD`, `MINOS_SOCKET`, `MINOS_SERVER`.

- [ ] Docs: `wire-contract.md` request table omits `project`, `scope`, `task` on `create`, and `project` on `channel.create`.

- [ ] Docs: `chat-concepts.md` has no project, scope or state; permanent-name uniqueness is per project, not global (`:90`); period change applies at the next sweep (`:251`, cf. `wire-contract.md:586`).

- [ ] Docs: add a status line to each `docs/dev/` file. `agent-container-net.md` recommends the design `recommended-architecture.md:160` rejects; `design.md:3` contradicts its line 198; the 13 edits in `agent-container-net2.md` section 7 are unapplied; pick one name for a submission's final state (`timed_out`, `expired`, `timeout`).

- [ ] Docs: mark `implementation-plan.md` status. Built: 1, 2, 4. Not built: 5 (grants), 6 (author kind), 7 (deadline), 8 (rate and quota), 13 (`minosa mcp`).

- [ ] Docs: move admitted gaps here: no container run yet (`CHANGELOG.md:47`); read `Window` enforced by nothing (`broker.go:62`); workers share one account; in-memory revocations; no `worker` account.

- [ ] CHANGELOG: *Unreleased* describes Python paths, removed make targets and a removed sidebar as current; it has two `### Fixed` headings.
