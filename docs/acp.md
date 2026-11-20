# ACP agents

`minosb -agent acp -- COMMAND` runs COMMAND as an [Agent Client Protocol](https://agentclientprotocol.com) agent, version 1, for the whole run. It is the third kind beside the stream kind (`-agent claude`) and the one-shot kind ([one-shot-agents.md](one-shot-agents.md)). The relay, `/end`, `/interrupt`, the linger and the events of [minosb-events.md](minosb-events.md) are the same for all three. The command takes no placeholders.

## What minosb does

- At start, it sends `initialize` with protocol version 1 and no client capabilities, then `session/new` in its own working directory with no MCP servers. An agent that answers neither within 30 s, or answers another version, fails the start with exit 2.
- Each turn is one `session/prompt`. The room gets the turn's `agent_message_chunk` text. A turn that stops for a reason other than `end_turn` or `cancelled` adds `[stopped: REASON]`; a JSON-RPC error adds `[turn failed: ERROR]`.
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

A MUST rule from the spec fails a test; a SHOULD rule is logged, and fails with `ACP_STRICT=1`. Prompts reach a model, so run the agent against its own test mock. `ACP_PROMPT`, `ACP_SLOW_PROMPT` and `ACP_PERMISSION_PROMPT` name the prompts; a test whose prompt is unset is skipped. Without `ACP_AGENT`, the suite runs against the scripted agent in `internal/acp/acpfake`, so `make test` checks the suite itself.

On 2026-11-20 `myra --acp`, against a mock provider, passed every test it could run. The cancel and permission tests were skipped for want of a slow and a permission-asking mock reply, and session/load because myra does not advertise `loadSession`.
