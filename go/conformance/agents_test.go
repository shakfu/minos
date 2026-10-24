package conformance

// The agent limit: how many occupancies the accounts in MINOS_AGENTS may hold
// in one room. Each test runs a server of its own, so the settings are known.

import (
	"strings"
	"testing"
	"time"
)

// agentRoom is a room demo raises with the invited users in it.
func agentRoom(t *testing.T, server *Server, invite ...string) any {
	t.Helper()
	demo := attach(t, server, "demo")
	return demo.Call("open", "invite", invite, "title", unique("Run"))["id"]
}

// By default worker is the one agent, and a room admits one of it.
func TestASecondAgentRunInARoomIsRefused(t *testing.T) {
	server := freshServer(t, nil)
	room := agentRoom(t, server, "worker")
	first, second := attach(t, server, "worker"), attach(t, server, "worker")

	held := first.Call("enter", "room", room)["occupancy"]
	refusal := second.Refuse("enter", "room", room)
	truth(t, strings.Contains(refusal, "limit"), "the refusal does not name the limit: %q", refusal)

	first.Call("exit", "occupancy", held)
	second.Call("enter", "room", room)
}

func TestAPersonIsNotCountedAgainstTheAgentLimit(t *testing.T) {
	server := freshServer(t, nil)
	room := agentRoom(t, server, "worker", "alice")
	attach(t, server, "worker").Call("enter", "room", room)
	attach(t, server, "alice").Call("enter", "room", room)
	attach(t, server, "alice").Call("enter", "room", room)
}

// Runs sharing an account are in different rooms at once.
func TestAnAgentIsInManyRoomsAtOnce(t *testing.T) {
	server := freshServer(t, nil)
	here, there := agentRoom(t, server, "worker"), agentRoom(t, server, "worker")
	first, second := attach(t, server, "worker"), attach(t, server, "worker")
	first.Call("enter", "room", here)
	second.Call("enter", "room", there)

	rooms := byID(first.Call("sync")["rooms"])
	same(t, rooms[str(here)]["occupants"], []string{"worker"})
	same(t, rooms[str(there)]["occupants"], []string{"worker"})
}

func TestTheAgentLimitIsASetting(t *testing.T) {
	server := freshServer(t, map[string]string{"MINOS_AGENTS": "alice,bob,worker", "MINOS_ROOM_AGENTS": "2"})
	room := agentRoom(t, server, "alice", "bob", "worker")
	attach(t, server, "alice").Call("enter", "room", room)
	attach(t, server, "bob").Call("enter", "room", room)
	attach(t, server, "worker").Refuse("enter", "room", room)
}

func TestAnAgentLimitOfZeroIsNoLimit(t *testing.T) {
	server := freshServer(t, map[string]string{"MINOS_ROOM_AGENTS": "0"})
	room := agentRoom(t, server, "worker")
	for range 3 {
		attach(t, server, "worker").Call("enter", "room", room)
	}
}

// A run that dies gives its place up with its connection.
func TestAClosedConnectionGivesTheAgentsPlaceUp(t *testing.T) {
	server := freshServer(t, nil)
	room := agentRoom(t, server, "worker")
	first := attach(t, server, "worker")
	first.Call("enter", "room", room)
	first.Close()

	second := attach(t, server, "worker")
	deadline := time.Now().Add(5 * time.Second)
	for {
		reply, _ := second.AwaitReply(second.SendOp("enter", "room", room)).(Obj)
		if _, refused := reply["error"]; !refused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the closed connection's place was never given up")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
