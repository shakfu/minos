package broker

// The ACP kind, against a real server and the scripted agent in acpfake, run
// as this test binary with fakeAgentEnv set.

import (
	"os"
	"strings"
	"testing"
	"time"

	"minos/internal/acp/acpfake"
	"minos/internal/config"
)

// fakeAgentEnv names the directory the fake agent keeps sessions in. Set, the
// test binary is the agent and runs no tests.
const fakeAgentEnv = "MINOS_ACP_FAKE_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeAgentEnv); dir != "" {
		(&acpfake.Agent{Dir: dir}).Serve(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// acpRun wires a relay to the fake agent.
func acpRun(t *testing.T, run *run, allow ...bool) (*ACPAgent, *Relay, *control) {
	t.Helper()
	env := append(os.Environ(), fakeAgentEnv+"="+t.TempDir())
	events := &control{}
	agent, err := NewACP([]string{os.Args[0]}, env, t.TempDir(), os.Stderr, nil, events.record, len(allow) > 0 && allow[0])
	if err != nil {
		t.Fatal(err)
	}
	agent.Start()
	relay := NewRelay(run.worker, agent, agent.Stdin, events.record)
	relay.Untasked()
	go relay.Deliver()
	go func() { _ = relay.Read(agent.Stdout) }()
	t.Cleanup(func() {
		agent.Stdin.Close()
		agent.Kill()
	})
	return agent, relay, events
}

// A turn is a prompt in one session; its reply is the agent's message
// chunks, joined. The session is the agent's, reported once.
func TestAnACPAgentAnswersEachTurnInOneSession(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	agent, _, events := acpRun(t, run)
	if agent.Name() != "fake" {
		t.Fatalf("the agent is named %q", agent.Name())
	}

	say(t, run.developer, run.room, "hello")
	posted(t, run, "you said: demo: hello")
	say(t, run.developer, run.room, "again")
	posted(t, run, "you said: demo: again")
	if events.seen("session") != 1 || !strings.HasPrefix(events.last("session")["id"].(string), "fake-") {
		t.Fatalf("the session was reported %d times, last as %v", events.seen("session"), events.last("session"))
	}
}

// ACP chunks mark no message boundary, so the text after a tool call starts
// a new paragraph rather than running into the text before it.
func TestTextAfterAnACPToolCallStartsAParagraph(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	acpRun(t, run)

	say(t, run.developer, run.room, "tool")
	if got, want := posted(t, run, "you said"), "reading x.txt\n\nyou said: demo: tool"; got != want {
		t.Fatalf("the turn said %q, want %q", got, want)
	}
}

// An interrupt is session/cancel. The turn ends cancelled, with what the
// agent had said, and the session goes on.
func TestAnInterruptCancelsTheACPPrompt(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	agent, relay, _ := acpRun(t, run)

	say(t, run.developer, run.room, "slow")
	// The fake says "working" once it listens for the cancel.
	waitFor(t, "the slow turn", func() bool {
		agent.mutex.Lock()
		defer agent.mutex.Unlock()
		return agent.text.String() == "working"
	})
	relay.Interrupt(Ask{By: "dispatcher", ID: "p1"})
	if got := posted(t, run, "working"); got != "working" {
		t.Fatalf("the cancelled turn said %q", got)
	}
	say(t, run.developer, run.room, "after")
	posted(t, run, "you said: demo: after")
}

// Until a person can answer from the room, the run's policy answers each
// permission request, and each decision is reported.
func TestAnACPPermissionRequestIsAnsweredByThePolicyAndReported(t *testing.T) {
	for policy, want := range map[bool][2]string{false: {"permission: no", "denied"}, true: {"permission: yes", "allowed"}} {
		run := dispatch(t, config.HistoryLimit, false)
		_, _, events := acpRun(t, run, policy)

		say(t, run.developer, run.room, "permission")
		posted(t, run, want[0])
		got := events.last("permission")
		if got == nil || got["outcome"] != want[1] || got["tool"] != "write x.txt" || got["kind"] != "edit" || got["by"] != "policy" {
			t.Fatalf("allow=%v: the permission was reported as %v", policy, got)
		}
	}
}

// Each turn's whole reply and stop reason are an event, so a dispatcher can
// read the run without the room.
func TestEachACPTurnIsReportedAsAResult(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	_, _, events := acpRun(t, run)

	say(t, run.developer, run.room, "hello")
	posted(t, run, "you said: demo: hello")
	got := events.last("result")
	if got == nil || got["text"] != "you said: demo: hello" || got["stop_reason"] != "end_turn" || got["error"] != nil {
		t.Fatalf("the result was reported as %v", got)
	}
	// The agent's context fill and cumulative cost, as it gave them.
	usage := events.last("usage")
	if usage == nil || usage["used"] != int64(100) || usage["size"] != int64(200000) || usage["cost"] != 0.001 || usage["currency"] != "USD" {
		t.Fatalf("the usage was reported as %v", usage)
	}
}

// A usage_update without a cost is still reported, with no cost.
func TestAnACPUsageWithoutACostIsReported(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	_, _, events := acpRun(t, run)

	say(t, run.developer, run.room, "free")
	posted(t, run, "you said: demo: free")
	usage := events.last("usage")
	if usage == nil || usage["used"] != int64(100) || usage["size"] != int64(200000) {
		t.Fatalf("the usage was reported as %v", usage)
	}
	if _, ok := usage["cost"]; ok {
		t.Fatalf("a usage without a cost was reported with one: %v", usage)
	}
}

// Closing the relay's stdin ends the agent, and Wait is its exit status.
func TestClosingTheRelaysStdinEndsTheACPAgent(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	agent, _, _ := acpRun(t, run)

	say(t, run.developer, run.room, "/end")
	done := make(chan int, 1)
	go func() { done <- agent.Wait() }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("the agent exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent outlived its stdin")
	}
}

// An agent that does not speak ACP is refused before the run starts.
func TestAnAgentThatIsNotACPIsRefusedAtStart(t *testing.T) {
	if _, err := NewACP([]string{"/bin/echo", "hello"}, nil, t.TempDir(), os.Stderr, nil, nil, false); err == nil {
		t.Fatal("echo was taken for an ACP agent")
	}
}
