package messaging

// Agent state: what a run in a room is doing, as its broker reports it.
//
// A dispatcher reads a run's state from the broker's control events, which no
// client sees. A person in the room needs the same fact -- is it working, is my
// message held until its turn ends, did it stop -- so the broker reports it
// here and the server hands it to the room's audience. It is not a message: it
// takes no sequence number and is not kept across a restart. The latest
// report replaces the one before it.
//
// A run reports only from inside its room, and a run that leaves it is
// reported stopped by the server, so a crashed broker cannot leave a room
// showing an agent at work.

import (
	"slices"
	"sort"
	"strings"
	"time"

	"minos/internal/timeline"
)

// AgentPush carries one run's state to its room's audience.
const AgentPush = "agent"

// Agent states.
const (
	AgentIdle         = "idle"
	AgentWorking      = "working"
	AgentInterrupting = "interrupting"
	AgentClosing      = "closing"
	AgentStopped      = "stopped"
)

var agentStates = []string{AgentIdle, AgentWorking, AgentInterrupting, AgentClosing, AgentStopped}

// DetailLimit is the most characters an agent state's detail may have.
const DetailLimit = 200

// AgentState is one run's state in one room.
type AgentState struct {
	Room     string `json:"room"`
	Username string `json:"username"`
	// idle, working, interrupting, closing or stopped.
	State string `json:"state"`
	// Turns started, messages held for the next one, and the highest sequence
	// number the run has been handed by either lane.
	Turn    int64 `json:"turn"`
	Held    int64 `json:"held"`
	Through int64 `json:"through"`
	// A short note: why it stopped, or what it is doing.
	Detail string `json:"detail"`
	// When the state began, and when it was last reported. The server's
	// clock, so a client compares them with nothing else.
	Since float64 `json:"since"`
	At    float64 `json:"at"`
}

// AgentReport is what a run says of itself.
type AgentReport struct {
	State   string
	Turn    int64
	Held    int64
	Through int64
	Detail  string
}

type agentEvent struct {
	Type  string      `json:"type"`
	Agent *AgentState `json:"agent"`
}

type agentKey struct{ room, username string }

// AgentReply answers agent.state.
type AgentReply struct {
	Ok    bool        `json:"ok"`
	Agent *AgentState `json:"agent"`
}

func clock() float64 { return float64(time.Now().UnixNano()) / float64(time.Second) }

// ReportAgent records what a run in a room is doing, and tells the room.
func (m *Messaging) ReportAgent(username, roomID string, report AgentReport) (*AgentReply, error) {
	if !m.isAgent(username) {
		return nil, refuse("Only an agent run reports its state")
	}
	room, err := m.requireAccess(roomID, username, timeline.RoomKind)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(m.store.OccupantsOf(roomID), username) {
		return nil, refuse("Enter the room before reporting a state in it")
	}
	if !slices.Contains(agentStates, report.State) {
		return nil, refuse("No such agent state: %s; there is: %s", report.State, strings.Join(agentStates, ", "))
	}
	report.Detail = strings.TrimSpace(report.Detail)
	if len([]rune(report.Detail)) > DetailLimit {
		return nil, refuse("A detail is at most %d characters", DetailLimit)
	}

	at := clock()
	state := &AgentState{
		Room: roomID, Username: username, State: report.State,
		Turn: max(0, report.Turn), Held: max(0, report.Held),
		Through: min(max(0, report.Through), room.LastSeq),
		Detail:  report.Detail, Since: at, At: at,
	}
	key := agentKey{roomID, username}
	m.agentMutex.Lock()
	if previous, ok := m.agents[key]; ok && previous.State == state.State {
		state.Since = previous.Since
	}
	m.agents[key] = state
	copied := *state
	m.agentMutex.Unlock()

	m.deliver(room.Audience, agentEvent{Type: AgentPush, Agent: &copied})
	return &AgentReply{Ok: true, Agent: &copied}, nil
}

// vacated marks stopped each run that no longer holds a place in a room. A
// broker that crashed or lost its connection reports nothing more, and the
// room must not go on showing it at work.
func (m *Messaging) vacated(roomID string) {
	occupants := m.store.OccupantsOf(roomID)
	var stopped []AgentState
	m.agentMutex.Lock()
	for key, state := range m.agents {
		if key.room != roomID || state.State == AgentStopped || slices.Contains(occupants, key.username) {
			continue
		}
		at := clock()
		state.State, state.Held, state.Detail, state.Since, state.At = AgentStopped, 0, "left the room", at, at
		stopped = append(stopped, *state)
	}
	m.agentMutex.Unlock()
	if len(stopped) == 0 {
		return
	}
	room, err := m.store.Room(roomID)
	if err != nil || room == nil {
		return
	}
	for index := range stopped {
		m.deliver(room.Audience, agentEvent{Type: AgentPush, Agent: &stopped[index]})
	}
}

// agentsIn is the runs reported in the given rooms, by room and then name.
func (m *Messaging) agentsIn(rooms []timeline.Room) []AgentState {
	wanted := map[string]bool{}
	for _, room := range rooms {
		wanted[room.ID] = true
	}
	found := []AgentState{}
	m.agentMutex.Lock()
	for key, state := range m.agents {
		if wanted[key.room] {
			found = append(found, *state)
		}
	}
	m.agentMutex.Unlock()
	sort.Slice(found, func(i, j int) bool {
		if found[i].Room != found[j].Room {
			return found[i].Room < found[j].Room
		}
		return found[i].Username < found[j].Username
	})
	return found
}

// forgetAgents drops what was reported in a room that no longer exists.
func (m *Messaging) forgetAgents(roomID string) {
	m.agentMutex.Lock()
	defer m.agentMutex.Unlock()
	for key := range m.agents {
		if key.room == roomID {
			delete(m.agents, key)
		}
	}
}
