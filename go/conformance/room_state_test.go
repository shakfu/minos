package conformance

// Open and closed: wire-contract 13. Closing says the work in a room is done.
// It is not deleting and not archiving, so nothing about who reads it changes.

import "testing"

func TestARoomIsOpenUntilItIsClosed(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	room := demo.Call("create", "title", unique("task"), "invite", []string{"alice"})
	same(t, room["state"], "open")

	alice.ExpectPush(PushOf("room"))
	alice.Drain()

	closed := demo.Call("room.close", "room", room["id"])
	same(t, closed["ok"], true)
	same(t, obj(closed["room"])["state"], "closed")

	// The audience is told, and still reaches the room: closing is not a grant.
	pushed := obj(alice.ExpectPush(PushOf("room"))["room"])
	same(t, pushed["state"], "closed")
	same(t, sorted(pushed["audience"]), []string{"alice", "demo"})
	same(t, roomOf(alice, str(room["id"]))["state"], "closed")
	same(t, alice.Call("send", "room", room["id"], "body", "still here")["ok"], true)

	same(t, obj(demo.Call("room.reopen", "room", room["id"])["room"])["state"], "open")
}

// A change is on the record in the room it changed, and only a change is.
func TestClosingPostsAnEventAndIsIdempotent(t *testing.T) {
	demo := connect(t, admin)
	room := demo.Call("create", "title", unique("task"), "invite", []any{})

	demo.Call("room.close", "room", room["id"])
	first := list(demo.Call("history", "room", room["id"], "since", 0)["messages"])
	if len(first) != 1 || obj(first[0])["body"] != "demo closed this room" {
		t.Fatalf("closing wrote %v", first)
	}

	same(t, obj(demo.Call("room.close", "room", room["id"])["room"])["state"], "closed")
	after := list(demo.Call("history", "room", room["id"], "since", 0)["messages"])
	if len(after) != 1 {
		t.Fatalf("closing a closed room wrote %v", after)
	}

	demo.Call("room.reopen", "room", room["id"])
	reopened := list(demo.Call("history", "room", room["id"], "since", 0)["messages"])
	if len(reopened) != 2 || obj(reopened[1])["body"] != "demo reopened this room" {
		t.Fatalf("reopening wrote %v", reopened)
	}
}

// Its grace period already decides when a transient room ends; closing would
// name a second, contradictory end.
func TestATransientRoomIsNotClosed(t *testing.T) {
	demo := connect(t, admin)
	meeting := demo.Call("open", "invite", []string{"alice"}, "title", "", "retention", "transient")
	refusal := "A transient room is not closed; it ends when everyone leaves"

	same(t, demo.Refuse("room.close", "room", meeting["id"]), refusal)
	same(t, demo.Refuse("room.reopen", "room", meeting["id"]), refusal)
}

// Who may close is who may invite: institutional for an admin-founded room,
// permissive for a user-founded one.
func TestWhoMayCloseIsWhoMayInvite(t *testing.T) {
	demo, alice, bob := connect(t, admin), connect(t, "alice"), connect(t, "bob")

	founded := demo.Call("create", "title", unique("task"), "invite", []string{"alice"})
	same(t, alice.Refuse("room.close", "room", founded["id"]),
		"Only an administrator may invite to this room")

	raised := alice.Call("open", "invite", []string{"bob"}, "title", unique("ours"))
	same(t, bob.Call("room.close", "room", raised["id"])["ok"], true)

	// Not a participant: no more standing to close than to invite.
	outsider := alice.Call("open", "invite", []any{}, "title", unique("mine"))
	same(t, bob.Refuse("room.close", "room", outsider["id"]), "Not invited to that room")

	same(t, demo.Refuse("room.close", "room", "absent"), "No such room: absent")
	same(t, demo.Refuse("room.close", "room", ""), "No such room: ")
}

// A channel closes too: a client counts active places, not active rooms. Its
// audience keeps it and keeps reading it.
func TestAChannelIsClosedAndStaysReadable(t *testing.T) {
	demo := connect(t, admin)
	channel := demo.Call("channel.create", "title", unique("Releases"), "groups", []any{})
	demo.Call("subscribe", "channel", channel["id"])

	same(t, obj(demo.Call("room.close", "room", channel["id"])["room"])["state"], "closed")
	listed := byID(demo.Call("sync")["channels"])[str(channel["id"])]
	truth(t, listed != nil, "closing took the channel off the list")
	same(t, listed["state"], "closed")
	same(t, demo.Call("channel.publish", "channel", channel["id"],
		"subject", "", "body", "still published")["ok"], true)

	// A channel is admin-founded, so only an administrator closes it.
	same(t, connect(t, "alice").Refuse("room.close", "room", channel["id"]),
		"Only an administrator may invite to this room")
}
