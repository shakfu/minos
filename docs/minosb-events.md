# minosb control events

What `minosb` tells its dispatcher, and how a run ends. A dispatcher such as pma maps these to a run's state and outcome.

Events are JSON objects, one per line. They go to stdout, or to the file `-events` names; with `-events`, stdout carries the agent's stream unchanged. `stopped` is always the last line, and nothing follows it.

## Events

| Event | Fields | When |
|-|-|-|
| `ready` | `room`, `channel`, `socket` | The socket answers. The agent has not started yet. |
| `pushed` | `messages` | A frame was written to the agent's stdin: the task, or the room's messages. A turn starts. |
| `session` | `id`, `agent` | The agent reported its session id, which resumes the session (`claude --resume ID`). Once per id; from claude's `system` `init` record. |
| `held` | `waiting` | A message arrived mid-turn. `waiting` is how many are held now. |
| `turn` | `delivering`, `read` (optional) | A turn ended. `delivering` is how many held messages go in the next frame. `read` counts those left out because the agent read them over the socket. |
| `interrupt` | `by`, `id`, `outcome`, `error` (optional) | The outcome of a request to stop a turn: `sent`, `merged` (into one already sent), `idle` (no turn to stop), `closed` (stdin is closed), or `failed`. |
| `closed` | `linger`, or `by` and `id` | The agent's stdin is closing: after `-linger` idle time, or after an `/end` from `by` (message `id`). |
| `undelivered` | `messages` | Messages that will never be pushed: they came after `closed` or after an `/end`. |
| `result` | `text`, `stop_reason`, `error` (optional) | ACP only: a turn ended. `text` is the whole reply, which the room may cut. `stop_reason` is ACP's (`end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`); a turn the agent failed has `error` in its place. Precedes the `turn` it ends. |
| `usage` | `cost`, `currency` | ACP only: the agent's `usage_update` carried a cost. It is cumulative for the session ([prompt-turn](https://agentclientprotocol.com/protocol/prompt-turn)), so the last is the total. `currency` is the agent's: an ISO 4217 code such as `USD`, or another unit such as `credits`. Absent when the agent reports no cost. |
| `permission` | `tool`, `kind`, `input`, `outcome`, `by` | ACP only: the agent asked permission. `tool` is the tool call's title, `kind` its ACP kind, `input` its raw input cut to 200 bytes. `outcome` is `allowed` or `denied`; `by` is `policy`, from `-permission`. |
| `refused` | `error` | A control-lane line was not a request. |
| `torn` | `missing` | The broker fell further behind than the server can backfill. |
| `unseated` | `room` | The run lost its place in the room and did not regain it within 45 s. |
| `exited` | `code` | The run command ended, with this exit status. |
| `stopped` | `reason` | The last line. |
| `unrelayed`, `unpushed`, `unclosed`, `unread` | `error` | A post to the room, a push, closing stdin, or reading the agent's stdout failed. |

## Agent state

A dispatcher reads the agent's state from the event stream alone:

| State | Entered on |
|-|-|
| working | `pushed` |
| waiting | `turn` with `delivering: 0`: the agent is between turns and nothing is queued for it |
| done | `stopped` |

A `turn` with `delivering` above 0 is followed by `pushed`, so the agent stays working.

`ready` means neither state. With `-task`, `pushed` follows at once. Without it, the agent waits for the room, and its first `turn` comes only after the first message.

## How a run ends

| `stopped` reason | Cause | minosb exit status |
|-|-|-|
| `exited` | The run command ended by itself, or after a linger close | the run command's |
| `ended` | The run command ended after an `/end` from the room | the run command's |
| `signal` | minosb got SIGINT or SIGTERM and killed the run command | 0 |
| `torn` | `torn` above; the run command is killed | 1 |
| `unseated` | `unseated` above; the run command is killed | 1 |

Two exits emit no `stopped`:

- Exit 2: minosb refused its arguments, or could not log in, open its socket or start the run command. The refusal is on stderr. If it got as far as `ready`, `ready` is the last event.
- SIGKILL: nothing is written.

## `/end` and `/interrupt`

Both are room messages that start with the command.

- `/interrupt [correction]` stops the running turn. The correction, if any, is pushed when that turn ends. Only `-interrupters` may send it; anyone else's is pushed as text.
- `/end`, alone apart from surrounding whitespace, closes stdin once the running turn ends, or at once between turns. `-interrupters` may send it. With no interrupters, anyone in the room may. `/end` followed by other words is pushed as text.
