package broker

// The two pipe lanes, and the adapter that knows one agent CLI's stream.
//
// stdout carries the whole turn and is the broker's only view of the work.
// stdin is the only lane that delivers without the agent's cooperation: a
// message pushed there reaches a model that never calls the shim. The socket
// is the third lane and is the agent's own, in broker.go.
//
// A message is pushed at the end of the turn, not into the middle of one, and
// everything held during a turn goes as one frame, so one turn answers it. An
// interrupt is a separate act and is the dispatcher's: it owns the container,
// and this process must not be in the path of stopping a run. What the broker
// owes the dispatcher is the fact and its order, which is the `held` control
// event, emitted when a message arrives mid-turn and before it is delivered.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"minos/internal/messaging"
)

// What a relayed message is cut to. The turn's whole trace goes to the run
// record; the room carries what a person reads.
const relayLimit = messaging.BodyLimit

// Adapter is one agent CLI's stream, in the two directions that matter. The
// tool names live in the container and the wire lives here, so a new agent is
// an adapter and nothing else.
type Adapter interface {
	// Name is the `-agent` value that selects it.
	Name() string
	// Push encodes messages as one line on the agent's stdin: one frame,
	// so one turn, however many messages it carries.
	Push(texts []string) ([]byte, error)
	// Turn reads one line of the agent's stdout. It answers the turn's final
	// assistant message, and whether this line ended a turn.
	Turn(line []byte) (string, bool)
	// Interrupt encodes a request to stop the current turn. id is the
	// caller's, unique among its requests; the agent's reply echoes it.
	Interrupt(id string) ([]byte, error)
}

// Claude Code's stream-json, in both directions.
//
// Input is a user message in the Messages API's shape. Output is one JSON
// record per line, of which `result` ends a turn and carries its final text.
type claudeAdapter struct{}

func (claudeAdapter) Name() string { return "claude" }

// A content block per message keeps each one's bounds.
func (claudeAdapter) Push(texts []string) ([]byte, error) {
	content := make([]any, 0, len(texts))
	for _, text := range texts {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	frame := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func (claudeAdapter) Turn(line []byte) (string, bool) {
	var record struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "result" {
		return "", false
	}
	return record.Result, true
}

// Interrupt is the control request the Agent SDK's `interrupt()` sends. On
// Claude Code 2.1.284 the interrupted turn ends with one `result`, subtype
// `error_during_execution`, and a user frame queued behind it runs as its own
// turn (scripts/probe-interrupt.sh). Nothing sends it yet.
func (claudeAdapter) Interrupt(id string) ([]byte, error) {
	frame := map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]any{"subtype": "interrupt"},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

var adapters = []Adapter{claudeAdapter{}}

// AdapterFor is the agent's stream handler, by name.
func AdapterFor(name string) (Adapter, error) {
	known := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		if adapter.Name() == name {
			return adapter, nil
		}
		known = append(known, adapter.Name())
	}
	return nil, fmt.Errorf("no adapter for %q; there is: %s", name, strings.Join(known, ", "))
}

// Relay owns the agent's pipes: what is pushed into a turn, and what the turn
// said. One owner, because a message pushed into the turn and a message handed
// over the socket are the same message.
type Relay struct {
	broker  *Broker
	adapter Adapter
	stdin   io.WriteCloser
	control func(map[string]any)

	// ended tells Deliver a turn ended; Deliver alone clears inTurn, so the
	// `turn` event precedes the write. Buffered by one: a turn ends only after
	// Deliver has written the frame that started it.
	ended chan struct{}

	mutex sync.Mutex
	// A turn is under way until the agent's stream says it ended. The run
	// starts inside one: the dispatcher gave the agent its task. A boolean
	// suffices because only Deliver writes, one frame per turn, between turns.
	inTurn bool
	held   []string
}

// NewRelay wires the broker to one agent's stdin. control reports to the
// dispatcher and must not block.
func NewRelay(b *Broker, adapter Adapter, stdin io.WriteCloser, control func(map[string]any)) *Relay {
	if control == nil {
		control = func(map[string]any) {}
	}
	return &Relay{
		broker: b, adapter: adapter, stdin: stdin, control: control,
		ended: make(chan struct{}, 1), inTurn: true,
	}
}

// Read consumes the agent's stdout to its end. Each turn's final message is
// posted to the room under the worker's name, whether or not the agent said
// anything itself: without relay a worker can work for twenty minutes and post
// nothing, and the room stops being the record.
func (r *Relay) Read(stdout io.Reader) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		final, ended := r.adapter.Turn(scanner.Bytes())
		if !ended {
			continue
		}
		if text := strings.TrimSpace(final); text != "" {
			if err := r.broker.relay(text); err != nil {
				r.control(map[string]any{"event": "unrelayed", "error": err.Error()})
			}
		}
		select {
		case r.ended <- struct{}{}:
		default:
		}
	}
	return scanner.Err()
}

// Deliver pushes what the room says onto the agent's stdin until the broker
// stops. It is the only writer to stdin, so nothing overtakes a held message.
func (r *Relay) Deliver() {
	for {
		ended := false
		select {
		case <-r.broker.stopped:
			return
		case <-r.broker.changed:
		case <-r.ended:
			ended = true
		}
		r.deliver(ended)
	}
}

// deliver holds what arrived mid-turn, and between turns writes everything
// held as one frame. Held rather than interrupted: an interrupt is the
// dispatcher's act, not this process's.
func (r *Relay) deliver(ended bool) {
	arrived := r.broker.forPush()

	r.mutex.Lock()
	if ended {
		r.inTurn = false
	}
	r.held = append(r.held, arrived...)
	depth, inTurn := len(r.held), r.inTurn
	var batch []string
	if !inTurn && depth > 0 {
		batch, r.held, r.inTurn = r.held, nil, true
	}
	r.mutex.Unlock()

	if inTurn {
		for i := range arrived {
			r.control(map[string]any{"event": "held", "waiting": depth - len(arrived) + i + 1})
		}
		return
	}
	if ended {
		r.control(map[string]any{"event": "turn", "delivering": len(batch)})
	}
	if len(batch) > 0 {
		r.write(batch)
	}
}

func (r *Relay) write(texts []string) {
	frame, err := r.adapter.Push(texts)
	if err == nil {
		_, err = r.stdin.Write(frame)
	}
	if err != nil {
		// Nothing reached the agent, so no turn started.
		r.mutex.Lock()
		r.inTurn = false
		r.mutex.Unlock()
		r.control(map[string]any{"event": "unpushed", "error": err.Error()})
	}
}
