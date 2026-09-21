package broker

// The two pipe lanes, and the adapter that knows one agent CLI's stream.
//
// stdout carries the whole turn and is the broker's only view of the work.
// stdin is the only lane that delivers without the agent's cooperation: a
// message pushed there reaches a model that never calls the shim. The socket
// is the third lane and is the agent's own, in broker.go.
//
// A message is pushed at the end of the turn, not into the middle of one. An
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
	// Push encodes one message as a line on the agent's stdin.
	Push(text string) ([]byte, error)
	// Turn reads one line of the agent's stdout. It answers the turn's final
	// assistant message, and whether this line ended a turn.
	Turn(line []byte) (string, bool)
}

// Claude Code's stream-json, in both directions.
//
// Input is a user message in the Messages API's shape. Output is one JSON
// record per line, of which `result` ends a turn and carries its final text.
type claudeAdapter struct{}

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Push(text string) ([]byte, error) {
	frame := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
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

	mutex sync.Mutex
	// A turn is under way until the agent's stream says it ended. The run
	// starts inside one: the dispatcher gave the agent its task.
	inTurn bool
	held   []string
}

// NewRelay wires the broker to one agent's stdin. control reports to the
// dispatcher and must not block.
func NewRelay(b *Broker, adapter Adapter, stdin io.WriteCloser, control func(map[string]any)) *Relay {
	if control == nil {
		control = func(map[string]any) {}
	}
	return &Relay{broker: b, adapter: adapter, stdin: stdin, control: control, inTurn: true}
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
		r.endTurn()
	}
	return scanner.Err()
}

// Deliver pushes what the room says onto the agent's stdin until the broker
// stops. It runs on its own goroutine and never touches the connection.
func (r *Relay) Deliver() {
	for {
		select {
		case <-r.broker.stopped:
			return
		case <-r.broker.changed:
		}
		for _, text := range r.broker.forPush() {
			r.Push(text)
		}
	}
}

// Push delivers one message, or holds it until the turn ends. Held rather than
// interrupted: an interrupt may discard what is queued, and it is the
// dispatcher's act, not this process's.
func (r *Relay) Push(text string) {
	r.mutex.Lock()
	if r.inTurn {
		r.held = append(r.held, text)
		depth := len(r.held)
		r.mutex.Unlock()
		r.control(map[string]any{"event": "held", "waiting": depth})
		return
	}
	r.mutex.Unlock()
	r.write(text)
}

// endTurn writes everything that arrived during the turn, in the order it
// arrived, before the agent is told the turn is over.
func (r *Relay) endTurn() {
	r.mutex.Lock()
	held := r.held
	r.held, r.inTurn = nil, false
	r.mutex.Unlock()

	r.control(map[string]any{"event": "turn", "delivering": len(held)})
	for _, text := range held {
		r.write(text)
	}
}

func (r *Relay) write(text string) {
	frame, err := r.adapter.Push(text)
	if err != nil {
		r.control(map[string]any{"event": "unpushed", "error": err.Error()})
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if _, err := r.stdin.Write(frame); err != nil {
		r.control(map[string]any{"event": "unpushed", "error": err.Error()})
		return
	}
	// A pushed message starts a turn: the agent answers it.
	r.inTurn = true
}
