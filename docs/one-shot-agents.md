# One-shot agents

`minosb -agent-spec FILE` runs a CLI once per turn, in place of one long-lived stream process. The CLI must answer one prompt and exit, print JSON, and resume a session by id. The events are those in [minosb-events.md](minosb-events.md).

## The run command

Everything after `--` is run for each turn, with two placeholders:

| Placeholder | Becomes |
|-|-|
| `{text}` | the turn's text: the task, or the room's messages joined by blank lines. With no `{text}` in the command, the text goes to the CLI's stdin, which keeps it out of the process list. |
| `{resume}` | an argument on its own. It becomes the spec's `resume` arguments once a turn has reported a session id, and nothing before that. A spec with `resume` needs it exactly once. |

```text
minosb ... -agent-spec agents/gilda.json -task "fix the test" -- gilda {resume} -p {text} --json
```

A placeholder rather than a fixed position, so the command can wrap the CLI, as `sanduk run` does.

## A turn

- A frame from the relay starts the process. Its stdin is the text, or empty.
- Its stdout reaches the dispatcher byte for byte, as with a stream agent.
- The turn ends when the process exits. A child that still holds stdout gets 1 s, then its process group is killed.
- The room gets the final record's text. Without text, it gets `[turn failed: ERROR]`, or `[turn failed: NAME printed no result and exited N]`.
- An interrupt sends SIGINT to the process group, then SIGKILL after 5 s.
- A turn that reports no session id keeps the last one, so one failed turn does not lose the conversation.
- Each shipped CLI's result reports the cost of that process alone. A run's cost is the sum over its turns. A stream agent differs: `claude`'s `total_cost_usd` is cumulative for the session.
- `minosb`'s exit status is the last process's: 0 when it exited 0, and 1 when a signal ended it.

## The spec

A JSON object. Unknown keys are refused.

| Key | Required | Is |
|-|-|-|
| `name` | yes | the `agent` in the `session` event |
| `text` | yes | the dotted path to the final text |
| `session` | with `resume` | the dotted path to the session id |
| `resume` | no | the arguments that resume a session; `{session}` is the id |
| `type` | no | the `type` of the final record; without it, the last JSON object printed |
| `error` | no | the dotted path to the error, posted when there is no text |

## Shipped specs

`agents/` holds these, each written from the CLI's source. `hilda` and `myra` have run three resumed turns through the relay against a mock provider. None has run against a real model under minos.

| Spec | Run command | Source |
|-|-|-|
| `gilda.json` | `gilda {resume} -p {text} --json`, or `-p -` for stdin | `gilda/cmd/gilda/headless.go` |
| `hax.json` | `hax {resume} --json`, text on stdin | `hax/docs/sessions.md` |
| `minima.json` | `minima {resume} -p {text} --json` | `minima/src/frontend/json.rs` |
| `hilda.json` | `hilda {resume} -p - --json`, text on stdin | `hilda/src/Hilda/App.hs` |
| `myra.json` | `myra {resume} --json -p {text}` | `myra/src/main.c` |

The installed `minima` and `hilda` predate `--resume`, and `myra` is not installed. The first two refuse the resume arguments; rebuild them from source.
