# ACP agents

`minosb -agent acp -- COMMAND` runs COMMAND as an [Agent Client Protocol](https://agentclientprotocol.com) agent, version 1, for the whole run. It is the third kind beside the stream kind (`-agent claude`) and the one-shot kind ([one-shot-agents.md](one-shot-agents.md)). The relay, `/end`, `/interrupt`, the linger and the events of [minosb-events.md](minosb-events.md) are the same for all three. The command takes no placeholders.

## What minosb does

- At start, it sends `initialize` with protocol version 1 and no client capabilities, then `session/new` in its own working directory with no MCP servers. An agent that answers neither within 30 s, or answers another version, fails the start with exit 2.
- Each turn is one `session/prompt`. The room gets the turn's `agent_message_chunk` text, with a paragraph break where text follows a tool call. A turn that stops for a reason other than `end_turn` or `cancelled` adds `[stopped: REASON]`; a JSON-RPC error adds `[turn failed: ERROR]`.
- An interrupt is `session/cancel`.
- It advertises no file system and no terminal, so the agent uses its own. A call to either is answered `-32601`.
- Each `session/request_permission` is answered by `-permission`: `deny` (the default) picks the agent's `reject_once` option, and `allow` its `allow_once`. Once, never always, so the next request is asked again. A request with no option of that kind is cancelled.
- `/end` and the linger close the agent's stdin. An agent still running 5 s later is killed with its process group.
- With `-events`, stdout carries the agent's ACP messages.

The events only ACP agents emit are listed in [minosb-events.md](minosb-events.md): `result`, `usage` and `permission`.

## Conformance

`go/acpconformance` checks an agent's end of the protocol, as minosb uses it:

```text
make acp-conformance ACP_AGENT='myra --acp'
```

A MUST rule from the spec fails a test; a SHOULD rule is logged, and fails with `ACP_STRICT=1`. Prompts reach a model, so run the agent against its own test mock. `ACP_PROMPT`, `ACP_SLOW_PROMPT` and `ACP_PERMISSION_PROMPT` name the prompts; a test whose prompt is unset is skipped. The usage test sends `ACP_PROMPT` twice and checks each `usage_update`: `used` and `size` present, a cost with `amount` and `currency`, and a cost that never falls. It skips an agent that sends none, unless `ACP_EXPECT_USAGE=1`. A second usage test loads the session in a new process after two prompts, prompts once more, and checks the cost did not fall. Each test gives the agent a new `HOME` and `XDG_*_HOME`, so its sessions and config stay out of yours; an assignment in `ACP_AGENT` overrides them. Without `ACP_AGENT`, the suite runs against the scripted agent in `internal/acp/acpfake`, so `make test` checks the suite itself.

Agents checked on 2026-11-21, each against its own offline mock:

| Agent | Result | Mock | Asks permission for |
|-|-|-|-|
| `myra --acp --context-window 8000` | passed what it could run, usage too; cancel and permission skipped, no `loadSession` | `scratchpad` SSE server | its `--permissions` mode |
| `gilda --acp --context 200000` | 12/12, also under `ACP_STRICT=1` | `gilda/mock/acp-conformance.json` | its `--permissions` mode |
| `hax --acp` | 12/12, also under `ACP_STRICT=1` | `hax/scripts/mock/acp.txt` | `bash`, `edit`, `write` |
| `minima --acp` | 12/12, also under `ACP_STRICT=1` | `minima/mock/acp.json` | every tool but `read` |
| `hilda --acp --context-window 128000` | 12/12, also under `ACP_STRICT=1` | `hilda/test/acp-mock.py` | `write`, `edit`, `bash`; `ask` is its `--acp` default |

With `ACP_EXPECT_USAGE=1`, all five sent a cumulative cost. `gilda`, `hax`, `myra` and `hilda` send `usage_update` only once they know the model's context window, which a mock lacks: pass `--context N` to `gilda`, `HAX_CONTEXT_LIMIT=N` to `hax` and `--context-window N` to `myra` and `hilda`. With a real provider, the price list that gives the cost also gives the window.

`hax`, `minima` and `hilda` ask by default in ACP mode. Under `minosb`'s default `-permission deny`, they can then only read; a run that should write needs `-permission allow`.
