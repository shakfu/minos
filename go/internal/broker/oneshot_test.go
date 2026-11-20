package broker

// The one-shot kind, against a real server and a fake CLI: a shell script
// that answers one prompt per run in JSON lines and resumes by id.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"minos/internal/config"
)

// fakeCLI answers -p TEXT, or stdin without -p, with a start line and a
// result. It keeps a resumed id and makes a new one otherwise. "slow" sleeps
// until interrupted; "crash" prints nothing and exits 3.
const fakeCLI = `#!/bin/sh
session=s-$$
resumed=no
text=
while [ $# -gt 0 ]; do
	case $1 in
	--resume) session=$2; resumed=yes; shift 2 ;;
	-p) text=$2; shift 2 ;;
	*) shift ;;
	esac
done
[ -z "$text" ] && text=$(cat)
case $text in
*crash*) exit 3 ;;
*slow*)
	trap 'printf "{\"type\":\"result\",\"text\":\"\",\"error\":\"cancelled\",\"session_id\":\"%s\"}\n" "$session"; exit 130' INT
	touch "$0.ready"
	sleep 30 & wait ;;
esac
echo '{"type":"start"}'
printf '{"type":"result","text":"heard %s; resumed %s","session_id":"%s"}\n' "$text" "$resumed" "$session"
`

var fakeSpec = Spec{
	Name: "fake", Resume: []string{"--resume", "{session}"}, Type: "result",
	Text: "text", Session: "session_id", Error: "error",
}

// oneShot wires a relay to the fake CLI, run as argv with {fake} for its path.
func oneShot(t *testing.T, run *run, argv ...string) (*OneShot, *Relay, *control, *strings.Builder) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(script, []byte(fakeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, arg := range argv {
		argv[i] = strings.ReplaceAll(arg, "{fake}", script)
	}
	passthrough := &strings.Builder{}
	agent, err := NewOneShot(fakeSpec, argv, os.Environ(), os.Stderr, &lockedWriter{b: passthrough})
	if err != nil {
		t.Fatal(err)
	}
	agent.Start()
	events := &control{}
	relay := NewRelay(run.worker, agent, agent.Stdin, events.record)
	relay.Untasked()
	go relay.Deliver()
	go func() { _ = relay.Read(agent.Stdout) }()
	t.Cleanup(func() {
		agent.Kill()
		agent.Stdin.Close()
	})
	return agent, relay, events, passthrough
}

// lockedWriter is a passthrough a test may read while turns write it.
type lockedWriter struct {
	mutex sync.Mutex
	b     *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.b.Write(p)
}

// posted waits for a body from the worker that contains want.
func posted(t *testing.T, run *run, want string) string {
	t.Helper()
	var found string
	waitFor(t, "a reply holding "+want, func() bool {
		for _, message := range run.developer.Log(run.room) {
			if message.Author == "bob" && strings.Contains(message.Body, want) {
				found = message.Body
				return true
			}
		}
		return false
	})
	return found
}

// Each turn is one process. The second resumes the session the first
// reported, and the dispatcher hears that session once.
func TestAOneShotAgentResumesItsSessionOnTheNextTurn(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	_, _, events, passthrough := oneShot(t, run, "{fake}", "{resume}", "-p", "{text}")

	say(t, run.developer, run.room, "hello")
	first := posted(t, run, "heard demo: hello")
	if !strings.Contains(first, "resumed no") {
		t.Fatalf("the first turn said %q", first)
	}
	say(t, run.developer, run.room, "again")
	if second := posted(t, run, "heard demo: again"); !strings.Contains(second, "resumed yes") {
		t.Fatalf("the second turn said %q", second)
	}
	if events.seen("session") != 1 || events.last("session")["agent"] != "fake" {
		t.Fatalf("the session was reported %d times, last as %v", events.seen("session"), events.last("session"))
	}
	if events.seen("pushed") != 2 {
		t.Fatalf("pushed was reported %d times", events.seen("pushed"))
	}
	waitFor(t, "the CLI's own lines passed through", func() bool {
		return strings.Count(passthrough.String(), `"type":"start"`) == 2
	})
	if strings.Contains(passthrough.String(), turnType) {
		t.Fatalf("the relay's own line reached the passthrough: %s", passthrough.String())
	}
}

// With no {text} in the command, the text reaches the CLI on stdin, so the
// process list does not show it.
func TestAOneShotCommandWithoutTextGetsItOnStdin(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	oneShot(t, run, "{fake}", "{resume}")

	say(t, run.developer, run.room, "by stdin")
	posted(t, run, "heard demo: by stdin")
}

// An interrupt signals the running process. Its turn ends with what the CLI
// reported, and the next turn still resumes the session.
func TestAnInterruptStopsTheOneShotProcess(t *testing.T) {
	defer func(was time.Duration) { interruptGrace = was }(interruptGrace)
	interruptGrace = 2 * time.Second
	defer func(was time.Duration) { drainGrace = was }(drainGrace)
	drainGrace = 200 * time.Millisecond
	run := dispatch(t, config.HistoryLimit, false)
	agent, relay, events, _ := oneShot(t, run, "{fake}", "{resume}", "-p", "{text}")

	say(t, run.developer, run.room, "slow")
	// Ready once its trap is set; a signal before then kills it outright.
	waitFor(t, "the slow process", func() bool {
		agent.mutex.Lock()
		argv := agent.argv
		agent.mutex.Unlock()
		_, err := os.Stat(argv[0] + ".ready")
		return err == nil
	})
	relay.Interrupt(Ask{By: "dispatcher", ID: "p1"})
	if got := posted(t, run, "[turn failed"); got != "[turn failed: cancelled]" {
		t.Fatalf("the interrupted turn said %q", got)
	}
	if got := events.outcomes(); len(got) != 1 || got[0] != "sent" {
		t.Fatalf("the interrupt was reported as %v", got)
	}
	say(t, run.developer, run.room, "after")
	if got := posted(t, run, "heard demo: after"); !strings.Contains(got, "resumed yes") {
		t.Fatalf("the turn after the interrupt said %q", got)
	}
}

// A CLI that dies without a result still ends its turn, and the room hears
// why. The run's status is that process's.
func TestAOneShotCrashEndsTheTurnAndIsTheRunsStatus(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	agent, _, _, _ := oneShot(t, run, "{fake}", "{resume}", "-p", "{text}")

	say(t, run.developer, run.room, "crash")
	if got := posted(t, run, "[turn failed"); got != "[turn failed: fake printed no result and exited 3]" {
		t.Fatalf("the crashed turn said %q", got)
	}
	say(t, run.developer, run.room, "/end")
	done := make(chan int, 1)
	go func() { done <- agent.Wait() }()
	select {
	case code := <-done:
		if code != 3 {
			t.Fatalf("the run's status is %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end after /end")
	}
}

func TestASpecAndItsCommandAreCheckedBeforeTheRun(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unnamed":     `{"text":"text"}`,
		"textless":    `{"name":"x"}`,
		"sessionless": `{"name":"x","text":"text","resume":["--resume","{session}"]}`,
		"unknown":     `{"name":"x","text":"text","cost":"usage.cost"}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSpec(path); err == nil {
			t.Errorf("%s: the spec was accepted", name)
		}
	}
	if _, err := NewOneShot(fakeSpec, []string{"fake", "-p", "{text}"}, nil, nil, nil); err == nil {
		t.Error("a command without {resume} was accepted for a spec that resumes")
	}
}

// The specs minos ships load.
func TestTheShippedSpecsLoad(t *testing.T) {
	paths, err := filepath.Glob("../../../agents/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no specs found: %v", err)
	}
	for _, path := range paths {
		if _, err := LoadSpec(path); err != nil {
			t.Error(err)
		}
	}
}
