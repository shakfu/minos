package broker

// The run's state, as the room sees it, and the kinds of message a run posts.

import (
	"io"
	"strings"
	"testing"

	"minos/internal/client"
	"minos/internal/link"
)

// watched is a run as worker, the default agent, in a room demo is in,
// with a scripted agent behind a relay whose events pass through Watch.
func watched(t *testing.T) (*client.Client, *Broker, *agent, *control) {
	t.Helper()
	server, config := agentRun(t)
	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(developer.Stop)
	run, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Stop)

	events := &control{}
	report := run.Watch(events.record)
	pushed, toAgent := io.Pipe()
	fromAgent, said := io.Pipe()
	relay := NewRelay(run, claudeAdapter{}, toAgent, report)
	go relay.Deliver()
	go func() { _ = relay.Read(fromAgent) }()
	t.Cleanup(func() {
		toAgent.Close()
		said.Close()
	})
	// As minosb reports the task it starts the agent on.
	report(map[string]any{"event": "ready"})
	report(map[string]any{"event": "pushed", "messages": 1})
	return developer, run, &agent{stdin: pushed, stdout: said, relay: relay}, events
}

// stateOf is what the developer's client was last told of worker in room.
func stateOf(c *client.Client, room string) client.AgentState {
	for _, state := range c.Agents(room) {
		if state.Username == "worker" {
			return state
		}
	}
	return client.AgentState{}
}

func TestTheRoomSeesTheRunWorkHoldAndHandOver(t *testing.T) {
	developer, run, worker, _ := watched(t)
	room := run.config.Room
	waitFor(t, "the run to be reported", func() bool { return stateOf(developer, room).State == stateWorking })

	say(t, developer, room, "also check the docs")
	waitFor(t, "the message to be held", func() bool { return stateOf(developer, room).Held == 1 })
	held := developer.Log(room)[len(developer.Log(room))-1].Seq
	if through := stateOf(developer, room).Through; through >= held {
		t.Fatalf("a held message was reported handed over: through %d, held %d", through, held)
	}

	worker.endTurn(t, "done with the task")
	worker.read(t)
	waitFor(t, "the second turn", func() bool {
		state := stateOf(developer, room)
		return state.Turn == 2 && state.Held == 0 && state.Through >= held
	})

	worker.endTurn(t, "done with the docs")
	waitFor(t, "the run to idle", func() bool { return stateOf(developer, room).State == stateIdle })
}

func TestARunThatStopsSaysSoBeforeItLeaves(t *testing.T) {
	developer, run, _, _ := watched(t)
	room := run.config.Room
	waitFor(t, "the run to be reported", func() bool { return stateOf(developer, room).State != "" })

	run.presence.observe(map[string]any{"event": "stopped", "reason": "signal"})
	run.Stop()
	waitFor(t, "the stop", func() bool {
		state := stateOf(developer, room)
		return state.State == stateStopped && state.Detail == "signal"
	})
}

func TestARunPostsProgressAndHandoffsAndTheRoomTellsThemApart(t *testing.T) {
	developer, run, _, _ := watched(t)
	room := run.config.Room

	for _, kind := range []string{"progress", "handoff", ""} {
		if reply := run.Handle(link.Request{Op: link.OpSay, Body: "said as " + kind, Kind: kind}); reply.Code != link.CodeOK {
			t.Fatalf("say -kind %q answered %+v", kind, reply)
		}
	}
	if reply := run.Handle(link.Request{Op: link.OpSay, Body: "x", Kind: "event"}); reply.Code != link.CodeRefused ||
		!strings.Contains(reply.Error, "text, progress, handoff") {
		t.Fatalf("an unknown kind answered %+v", reply)
	}

	var kinds []string
	waitFor(t, "the three", func() bool {
		kinds = nil
		for _, message := range developer.Log(room) {
			if message.Author == "worker" && message.AuthorKind == "agent" {
				kinds = append(kinds, message.Kind)
			}
		}
		return len(kinds) == 3
	})
	if strings.Join(kinds, ",") != "progress,handoff,text" {
		t.Fatalf("the room holds %v", kinds)
	}
}

// What another run said as progress is not news to this one; a handoff is.
func TestAHandoffIsPushedAndProgressIsNot(t *testing.T) {
	for kind, want := range map[string]bool{"text": true, "handoff": true, "progress": false, "event": false} {
		if got := pushable(client.Message{Author: "predecessor", Kind: kind}, "worker"); got != want {
			t.Errorf("a %s is pushed: %v", kind, got)
		}
	}
	if pushable(client.Message{Author: "worker", Kind: "text"}, "worker") {
		t.Error("the run is pushed its own words")
	}
}
