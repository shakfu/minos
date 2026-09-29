#!/usr/bin/env bash
# Probe how Claude Code's stream-json mode answers an interrupt mid-turn.
# Settles the open High item in TODO.md: does a `result` follow the interrupt,
# and what happens to a user frame queued behind the running turn.
#
# Usage: scripts/probe-interrupt.sh [out-dir] [delay-seconds]
# Runs on your Claude account and spends a few turns' worth of tokens.
set -uo pipefail

out=${1:-$(mktemp -d -t minos-interrupt.XXXXXX)}
delay=${2:-3}
mkdir -p "$out"
command -v claude >/dev/null || { echo "claude not on PATH" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq not on PATH" >&2; exit 2; }

user() {
	jq -cn --arg t "$1" '{type:"user",message:{role:"user",content:[{type:"text",text:$t}]}}'
}
interrupt() {
	jq -cn --argjson cancel "$1" \
		'{type:"control_request",request_id:"r1",request:({subtype:"interrupt"} + (if $cancel then {cancel_queued:true} else {} end))}'
}

# probe NAME QUEUED CANCEL: one long turn, optionally a second frame queued
# behind it, then the interrupt after $delay seconds. stdin stays open for 10 s
# afterwards, so an exit is the CLI's own and not end of input.
probe() {
	local name=$1 queued=$2 cancel=$3
	{
		user "Count from 1 to 300, one number per line."
		[[ $queued == true ]] && user "Reply with the single word SECOND and nothing else."
		sleep "$delay"
		interrupt "$cancel"
		sleep 10
	} | claude -p --input-format stream-json --output-format stream-json --verbose \
		>"$out/$name.jsonl" 2>"$out/$name.stderr"
	local code=$?

	echo "== $name (exit $code)"
	jq -c 'select(.type != "stream_event") | {type, subtype,
		request_id: .response.request_id, response: .response.response,
		is_error, result: (if .result then (.result | tostring | .[0:60]) else null end)}
		| with_entries(select(.value != null))' "$out/$name.jsonl"
	if jq -e -s 'any(.[]; (.type == "assistant" or .type == "result") and (tostring | test("SECOND")))' \
		"$out/$name.jsonl" >/dev/null; then
		echo "-- the queued frame ran"
	elif [[ $queued == true ]]; then
		echo "-- the queued frame did not run"
	fi
	[[ -s $out/$name.stderr ]] && echo "-- stderr: $(head -c 300 "$out/$name.stderr")"
	echo
}

probe single false false
probe queued true false
probe cancel-queued true true
echo "raw output in $out"
