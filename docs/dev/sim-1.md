# sim-1: three agents in one room

Run on 2026-10-24, on `f44baf9` with `go/cmd/sim` uncommitted.

## The simulation

`make sim` runs three scripted agents through the real stack. No model runs.

- One `minosd`, with a database of its own.
- Per agent, one `minosb`, its socket, and `minosa`.
- The agent is `go/cmd/sim` again, selected by `SIM_AGENT`. It speaks Claude Code's stream-json, so the broker's `claude` adapter reads it unchanged.
- `demo` founds an ad-hoc room and a channel, and moderates the channel.

The agents review a fictional PR #42, a session cache:

| Agent | Role | Script |
|-|-|-|
| `alice` | security | 3 progress messages, finding `cache-key`, a proposal to block the merge |
| `bob` | performance | 2 progress messages, waits for `cache-key`, 1 more, finding `cache-cost`, a proposal to raise the cache TTL |
| `worker` | tests | 2 progress messages, waits for `cache-key` and `cache-cost`, 1 more, a proposal to add an isolation test |

Each operation maps to one shim call:

- Progress is `minosa say`.
- A finding is `minosa say -payload`, a JSON object in the broker's `minos-payload` fence.
- A wait polls `minosa messages -json` until the topics it needs appear.
- A proposal is `minosa submit`, then `minosa await`. The agent then says the outcome.

The task turn ends with a summary, which the relay posts. Every later stdin frame ends a turn with an empty result, which the relay drops. `-linger 2s` closes each agent's input once it is idle, and the agent exits.

The scripted moderator approves `alice` and `worker` and rejects `bob`. `-manual` leaves the decisions to a person in the terminal client. `-server URL` runs against a running server; `-pace F` scales every delay.

The agents run as `alice`, `bob` and `worker`. A broker drops its own account's messages (`internal/broker/broker.go`, `forPush` and `messages`). Three runs as `worker` would not see each other.

The run breaks D10 of [design.md](design.md): no room holds two concurrent workers. The layout was chosen before D10 was checked.

`go/cmd/sim/main_test.go` runs the whole simulation under `make test`, in about 9 s.

## Results

All 3 agents exited 0, all 3 proposals were decided, and the room held 23 messages. The run took 5.8 s at `-pace 1`.

Frames pushed onto each agent's stdin after its task turn:

| Agent | Frames | Messages in the first | Single-message frames | Findings read over the socket |
|-|-|-|-|-|
| `alice` | 6 | 8 | 5 | 0 |
| `bob` | 3 | 11 | 2 | 1 |
| `worker` | 1 | 14 | 0 | 2 |

20 messages were said by agents, and each reached the other 2 agents: 40 deliveries in 10 frames.

## Findings

1. **D10 holds only because the fake stays silent.** The run avoided a reply loop only because every pushed frame ends a turn with an empty result. A model replies to what it reads, so loop prevention would depend on its choice. D10 refuses the configuration instead. D10 is enforced nowhere: it lives in a document only.

2. **The push lane sends every message to every agent.** Batching applies only while the recipient is in a turn. An idle agent takes one turn per message. `alice` finished first and took 5 single-message turns. `worker` finished last and took 1. With a real agent, each of those 10 turns is paid. The agent that finishes first pays the most.

3. **A message read over the socket is pushed again.** `bob` read `cache-key` with `messages`, then got it in his first stdin frame. `worker` got 2 findings twice. The two lanes keep separate cursors, `delivered` and `pushed`. A model would read the finding twice.

4. **`await` holds the turn open, so the agent receives nothing on stdin while it waits.** Everything sent to it is held until the decision arrives. A person may take up to 3600 s, the `await` ceiling.

5. **Agents cannot share through a channel.** A broker reads its room alone. An agent may submit to a channel but not publish to it. Findings therefore went into room text, in a fence the terminal client shows raw.

6. **Messages are filtered by author; submissions are matched by id.** `decided` matches a submission by the id `submit` returned, so it works under a shared account. `forPush` and `messages` drop by author, so they do not. Any room with more than one run needs one account per run.

7. **Channel titles are unique.** A second run against one server failed until each run named its channel with the time.

8. **A signal skipped the cleanup.** SIGTERM left the state directory and the server's run directory in `/tmp`. Fixed: `simulate` stops the brokers on a signal and returns.

The fake shows the plumbing works. It says nothing about a model's behaviour, for example whether a model polls `messages` or waits for the push. `make host-run` is the test for that.

## Recommended changes

1. **Make D10 a server setting.** It needs three things the server lacks:

   - **Which accounts are agents.** There is no author kind. Until grants land, a setting such as `MINOS_AGENTS=worker` lists them.
   - **What makes an agent live in a room.** Brokers never call `enter`. A broker would enter its room in `Open` and exit in `Stop`.
   - **A count that a shared account cannot evade.** A person occupies one room, across connections, so `worker` entering a second room releases its first. That gives a third run a free place in the first room. Agent accounts would hold one occupancy per connection, and the server would count occupancies, not users.

   The limit is then `MINOS_ROOM_AGENTS`, default 1, with 0 for no limit. `enter` refuses an agent past it. D10 says "stated per room, that is reversible", so a per-room override is a later step. This changes the wire contract: one setting, one refusal, and a new rule for agent occupancy. The simulation would then set the limit to 3 on its own server.

   Built on 2026-10-24, as described here. `make sim` sets `MINOS_AGENTS=alice,bob,worker` and `MINOS_ROOM_AGENTS=3` on its own server.

2. **Mark socket deliveries as pushed.** After `messages` hands a message over, it should not be pushed onto stdin as well. The alternative is a push that says which messages the agent has already read. Either way a model reads each message once.

3. **Address pushes before rooms hold more than one agent.** Mentions or topics would let a broker push only what concerns its agent. Without that, turns grow as agents times messages. A supervisor that forwards selectively is the topology [design.md](design.md) section 5 already describes, and needs no change to the server.

4. **Let an agent wait for a decision without holding its turn.** `submit` would return at once, and the decision would arrive as a pushed message. The agent stays reachable while a person decides, at the cost of one more turn. `await` stays for agents that want to block.

5. **Fold payload fences in the terminal client.** The fence is the only structured channel between agents, and the room shows it as raw JSON. [channels.md](channels.md) explores typed channels as the longer-term answer.

6. **Add the D10-conformant scenario to the simulation.** One room per agent, all as `worker`, with a scripted supervisor relaying findings. Run both scenarios on one script and compare frames, turns and duplicate deliveries.
