package broker

// The run's state, told to its room.
//
// A dispatcher reads the agent's state from the control events
// (docs/minosb-events.md). A person in the room cannot see those, and needs
// the same fact: whether the agent is working, whether their message waits
// for the turn to end, whether the run stopped. Watch derives the state from
// the events as a dispatcher would, and reports it to the server, which hands
// it to the room's audience.
//
// Reporting never holds up the run. Reports go from one goroutine, and a
// report superseded before it was sent is not sent at all.

import (
	"sync"
	"time"

	"minos/internal/client"
)

// States a run reports, as the server names them.
const (
	stateIdle         = "idle"
	stateWorking      = "working"
	stateInterrupting = "interrupting"
	stateClosing      = "closing"
	stateStopped      = "stopped"
)

type watcher struct {
	broker *Broker

	mutex   sync.Mutex
	state   client.AgentReport
	pending bool
	busy    bool
	wake    chan struct{}
}

// Watch reports the run's state to its room as control events pass through
// report, and returns the function to report them with instead.
func (b *Broker) Watch(report func(map[string]any)) func(map[string]any) {
	w := &watcher{broker: b, wake: make(chan struct{}, 1), state: client.AgentReport{State: stateIdle}}
	b.presence = w
	go w.run()
	return func(event map[string]any) {
		report(event)
		w.observe(event)
	}
}

// observe moves the state on one control event.
func (w *watcher) observe(event map[string]any) {
	name, _ := event["event"].(string)
	w.mutex.Lock()
	state := &w.state
	switch name {
	case "ready":
		state.State, state.Detail = stateIdle, ""
	case "pushed":
		state.State, state.Held, state.Detail = stateWorking, 0, ""
		state.Turn++
		state.Through = max(state.Through, number(event["through"]))
	case "held":
		state.Held = number(event["waiting"])
	case "turn":
		state.Held = 0
		if number(event["delivering"]) == 0 {
			state.State, state.Detail = stateIdle, ""
			state.Through = max(state.Through, number(event["through"]))
		}
	case "interrupt":
		if event["outcome"] == "sent" {
			state.State = stateInterrupting
		}
	case "closed":
		state.State = stateClosing
	case "torn", "unseated":
		state.Detail = name
	case "stopped":
		state.State, state.Held = stateStopped, 0
		reason, _ := event["reason"].(string)
		if state.Detail == "" {
			state.Detail = reason
		}
	default:
		w.mutex.Unlock()
		return
	}
	w.pending = true
	w.mutex.Unlock()
	w.signal()
}

// again reports the current state once more, as after the run regained its
// place in the room.
func (w *watcher) again() {
	if w == nil {
		return
	}
	w.mutex.Lock()
	w.pending = true
	w.mutex.Unlock()
	w.signal()
}

func (w *watcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// run sends the latest state until the broker stops.
func (w *watcher) run() {
	for {
		select {
		case <-w.broker.stopped:
			return
		case <-w.wake:
		}
		for {
			w.mutex.Lock()
			if !w.pending {
				w.busy = false
				w.mutex.Unlock()
				break
			}
			report := w.state
			w.pending, w.busy = false, true
			w.mutex.Unlock()

			// What the agent asked for over the socket was handed over too.
			report.Through = max(report.Through, w.broker.deliveredThrough())
			// A refusal is not the run's concern: a server that predates
			// agent state, or a place lost to a reconnect, which again repairs.
			_ = w.broker.chat.ReportAgent(w.broker.config.Room, report)
		}
	}
}

// flush waits, at most for limit, until the latest state has been sent.
func (w *watcher) flush(limit time.Duration) {
	if w == nil {
		return
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		w.mutex.Lock()
		done := !w.pending && !w.busy
		w.mutex.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// number reads an event's count, which is an int when the relay wrote it and
// a float64 when it came through JSON.
func number(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}
