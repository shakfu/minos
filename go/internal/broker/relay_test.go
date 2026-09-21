package broker

// The two pipe lanes, against a real server and a scripted agent. No model and
// no container: the agent is whatever writes stream-json on a pipe.

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"minos/internal/config"
	"minos/internal/link"
	"minos/internal/messaging"
)

// agent is a fake worker on the other end of the broker's pipes.
type agent struct {
	stdin  io.Reader      // what the broker pushed
	stdout io.WriteCloser // what the agent says
	relay  *Relay
}

// scripted wires a relay to a fake agent and starts both lanes.
func scripted(t *testing.T, run *run) (*agent, *control) {
	t.Helper()
	pushed, toAgent := io.Pipe()
	fromAgent, said := io.Pipe()
	events := &control{}

	relay := NewRelay(run.worker, claudeAdapter{}, toAgent, events.record)
	go relay.Deliver()
	go func() { _ = relay.Read(fromAgent) }()
	t.Cleanup(func() {
		toAgent.Close()
		said.Close()
	})
	return &agent{stdin: pushed, stdout: said, relay: relay}, events
}

// say is the agent ending a turn with its final message.
func (a *agent) endTurn(t *testing.T, text string) {
	t.Helper()
	record, err := json.Marshal(map[string]any{"type": "result", "result": text})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.stdout.Write(append(record, '\n')); err != nil {
		t.Fatalf("the agent cannot write: %v", err)
	}
}

// read is one frame the broker pushed onto the agent's stdin.
func (a *agent) read(t *testing.T) map[string]any {
	t.Helper()
	line := make([]byte, 0, 512)
	buffer := make([]byte, 1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := a.stdin.Read(buffer)
		if err != nil {
			t.Fatalf("the agent's stdin closed: %v", err)
		}
		if n == 0 {
			continue
		}
		if buffer[0] == '\n' {
			var frame map[string]any
			if err := json.Unmarshal(line, &frame); err != nil {
				t.Fatalf("the broker pushed %q: %v", line, err)
			}
			return frame
		}
		line = append(line, buffer[0])
	}
	t.Fatal("nothing was pushed")
	return nil
}

// text is the message inside a pushed frame.
func text(t *testing.T, frame map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var pushed struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &pushed) != nil || len(pushed.Message.Content) == 0 {
		t.Fatalf("a pushed frame reads %v", frame)
	}
	return pushed.Message.Content[0].Text
}

// control collects what the broker told the dispatcher.
type control struct {
	mutex  sync.Mutex
	events []map[string]any
}

func (c *control) record(event map[string]any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.events = append(c.events, event)
}

func (c *control) seen(name string) int {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	count := 0
	for _, event := range c.events {
		if event["event"] == name {
			count++
		}
	}
	return count
}

// -- the relay ---------------------------------------------------------------

// Without relay an agent can work for twenty minutes and post nothing, and the
// room stops being the record.
func TestTheRoomGetsTheTurnWhetherOrNotTheAgentCooperates(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, _ := scripted(t, run)

	worker.endTurn(t, "the tests pass; the fix is in vfs.go")
	waitFor(t, "the turn in the room", func() bool {
		for _, message := range run.developer.Log(run.room) {
			if message.Body == "the tests pass; the fix is in vfs.go" && message.Author == "bob" {
				return true
			}
		}
		return false
	})
}

func TestATurnTooLongForTheWireIsCutRatherThanDropped(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, _ := scripted(t, run)

	worker.endTurn(t, strings.Repeat("x", messaging.BodyLimit+2048))
	waitFor(t, "the cut turn in the room", func() bool {
		for _, message := range run.developer.Log(run.room) {
			if strings.HasSuffix(message.Body, relayCut) {
				return true
			}
		}
		return false
	})
}

// -- the push lane -----------------------------------------------------------

// The only lane that delivers without the agent's cooperation.
func TestWhatTheRoomSaysReachesTheAgentWithoutItAsking(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, _ := scripted(t, run)
	worker.endTurn(t, "waiting")

	say(t, run.developer, run.room, "stop and run the tests first")
	if got := text(t, worker.read(t)); !strings.Contains(got, "stop and run the tests first") {
		t.Fatalf("the agent was pushed %q", got)
	}
}

// A message that arrives mid-turn waits for the turn to end. Interrupting is a
// separate act and the dispatcher's, so what the broker owes is the fact.
func TestAMessageArrivingMidTurnIsHeldAndReportedBeforeItIsDelivered(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, events := scripted(t, run)

	say(t, run.developer, run.room, "first")
	waitFor(t, "the held report", func() bool { return events.seen("held") == 1 })

	worker.endTurn(t, "done with what I was doing")
	if got := text(t, worker.read(t)); !strings.Contains(got, "first") {
		t.Fatalf("the held message arrived as %q", got)
	}
}

func TestHeldMessagesKeepTheOrderTheyArrivedIn(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, events := scripted(t, run)

	say(t, run.developer, run.room, "one", "two", "three")
	waitFor(t, "three held", func() bool { return events.seen("held") == 3 })

	worker.endTurn(t, "done")
	for _, want := range []string{"one", "two", "three"} {
		if got := text(t, worker.read(t)); !strings.Contains(got, want) {
			t.Fatalf("expected %q, the agent was pushed %q", want, got)
		}
	}
}

// The worker's own voice is not news to it, and a room event is the server's
// bookkeeping rather than an instruction.
func TestTheAgentIsNotPushedItsOwnMessagesOrTheRoomsEvents(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, _ := scripted(t, run)
	worker.endTurn(t, "waiting")

	if reply := run.worker.Handle(link.Request{Op: link.OpSay, Body: "from the shim"}); reply.Code != link.CodeOK {
		t.Fatalf("say was refused: %s", reply.Error)
	}
	if _, err := run.developer.Invite(run.room, clientPrincipal("alice")); err != nil {
		t.Fatalf("cannot invite: %v", err)
	}
	say(t, run.developer, run.room, "the only thing to push")

	if got := text(t, worker.read(t)); !strings.Contains(got, "the only thing to push") {
		t.Fatalf("the agent was pushed %q first", got)
	}
}

// Both lanes carry every message: the socket is what the agent asks for, and
// stdin is what reaches it whether it asks or not.
func TestTheSocketAndTheStdinLaneEachCarryTheWholeRoom(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, _ := scripted(t, run)
	worker.endTurn(t, "waiting")

	say(t, run.developer, run.room, "read the contract")
	if got := text(t, worker.read(t)); !strings.Contains(got, "read the contract") {
		t.Fatalf("the agent was pushed %q", got)
	}

	records := run.drain(t, 1)
	if records[len(records)-1].Body != "read the contract" {
		t.Fatalf("the socket delivered %+v", records)
	}
}

// A torn run stops being fed, as it stops being drained.
func TestATornRunPushesNothingMore(t *testing.T) {
	run := dispatch(t, config.HistoryLimit, false)
	worker, _ := scripted(t, run)
	worker.endTurn(t, "waiting")
	run.worker.tear(run.room, 3)

	say(t, run.developer, run.room, "this must not arrive")
	if pushed := run.worker.forPush(); pushed != nil {
		t.Fatalf("a torn run offered %v", pushed)
	}
}

// -- the adapter -------------------------------------------------------------

func TestTheAdapterIsNamedAndAnUnknownOneIsRefused(t *testing.T) {
	adapter, err := AdapterFor("claude")
	if err != nil || adapter.Name() != "claude" {
		t.Fatalf("claude resolved to %v, %v", adapter, err)
	}
	if _, err := AdapterFor("nothing"); err == nil || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("an unknown adapter answered %v", err)
	}
}

func TestOnlyATerminalRecordEndsATurn(t *testing.T) {
	adapter := claudeAdapter{}
	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking"}]}}`,
		`{"type":"system","subtype":"init"}`,
		`not json at all`,
	} {
		if _, ended := adapter.Turn([]byte(line)); ended {
			t.Fatalf("%s ended a turn", line)
		}
	}
	final, ended := adapter.Turn([]byte(`{"type":"result","result":"done"}`))
	if !ended || final != "done" {
		t.Fatalf("a result answered %q, %v", final, ended)
	}
}
