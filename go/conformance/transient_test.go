package conformance

// Transient rooms: the promise that a conversation is not kept. The only tests
// that spend real time, each on a server of its own with a short grace.
//
// A test that asserts a room survives does not sleep a fixed interval: it
// empties a second room and waits for that one to go, which proves a sweep ran
// past the grace period.

import (
	"testing"
	"time"
)

// The contract puts the deletion between the grace and one further sweep.
const sweepSeconds = 1

var grace = map[string]string{"MINOS_ROOM_GRACE": "1", "MINOS_ROOM_SWEEP": "1"}

// Ceiling on the grace plus one sweep, with room for a loaded machine.
const deletionTimeout = 15 * time.Second

// emptyARoom raises a transient room, occupies it, and leaves.
func emptyARoom(socket *Socket, title string) any {
	room := socket.Call("open", "invite", []string{}, "retention", "transient", "title", title)
	occupancy := socket.Call("enter", "room", room["id"])["occupancy"]
	socket.Call("exit", "occupancy", occupancy)
	return room["id"]
}

// awaitASweep blocks until a sweep has demonstrably run past the grace, then one
// interval more, so a room deleted in the same pass has been announced too.
func awaitASweep(socket *Socket) {
	doomed := emptyARoom(socket, unique("Doomed"))
	socket.ExpectPushWithin(gone(doomed), deletionTimeout)
	time.Sleep((sweepSeconds + 1) * time.Second)
}

func listed(socket *Socket, room any) bool {
	_, found := byID(socket.Call("sync")["rooms"])[str(room)]
	return found
}

func TestARoomIsDeletedOnceItsLastOccupantHasBeenGone(t *testing.T) {
	server := freshServer(t, grace)
	alice, bob := attach(t, server, "alice"), attach(t, server, "bob")

	room := alice.Call("open", "invite", []string{"bob"}, "retention", "transient", "title", unique("Brief"))
	occupancy := alice.Call("enter", "room", room["id"])["occupancy"]
	alice.Call("send", "room", room["id"], "body", "said and gone")
	alice.Call("exit", "occupancy", occupancy)

	// Its audience is told before it goes, or it would sit in their lists.
	bob.ExpectPushWithin(gone(room["id"]), deletionTimeout)

	// Gone, not merely hidden: nothing about it answers any more.
	same(t, alice.Refuse("history", "room", room["id"], "since", 0), "No such room: "+str(room["id"]))
	truth(t, !listed(bob, room["id"]), "bob still lists the room")
}

// A reload or a dropped connection must not destroy a live conversation.
func TestReturningDuringTheGracePeriodRescuesTheRoom(t *testing.T) {
	alice := attach(t, freshServer(t, grace), "alice")

	room := alice.Call("open", "invite", []string{}, "retention", "transient", "title", unique("Rescued"))
	occupancy := alice.Call("enter", "room", room["id"])["occupancy"]
	alice.Call("exit", "occupancy", occupancy)
	alice.Call("enter", "room", room["id"])

	awaitASweep(alice)
	truth(t, listed(alice, room["id"]), "the room was deleted")
}

// The countdown starts from emptying, and a room never filled never empties.
func TestARoomNobodyEverEnteredDoesNotExpire(t *testing.T) {
	alice := attach(t, freshServer(t, grace), "alice")

	room := alice.Call("open", "invite", []string{}, "retention", "transient", "title", unique("Untouched"))

	awaitASweep(alice)
	truth(t, listed(alice, room["id"]), "the room was deleted")
}

func TestAPersistedRoomSurvivesEmptying(t *testing.T) {
	alice := attach(t, freshServer(t, grace), "alice")

	room := alice.Call("open", "invite", []string{}, "retention", "persisted", "title", unique("Kept"))
	occupancy := alice.Call("enter", "room", room["id"])["occupancy"]
	alice.Call("exit", "occupancy", occupancy)

	awaitASweep(alice)
	truth(t, listed(alice, room["id"]), "the room was deleted")
}

// The occupancy is the connection's, so losing one releases it.
func TestADroppedConnectionStartsTheCountdown(t *testing.T) {
	server := freshServer(t, grace)
	alice, bob := attach(t, server, "alice"), attach(t, server, "bob")

	room := alice.Call("open", "invite", []string{"bob"}, "retention", "transient", "title", unique("Dropped"))
	bob.Call("enter", "room", room["id"])
	bob.Close()

	alice.ExpectPushWithin(gone(room["id"]), deletionTimeout)
}
