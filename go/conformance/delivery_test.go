package conformance

// Who receives a push, and who does not: the frames the server sends to people
// who did not ask for anything.

import (
	"testing"
)

func TestAMessageReachesTheRestOfTheRoom(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Heard"))
	bob.Drain()
	alice.Call("send", "room", room["id"], "body", "hello")

	event := bob.ExpectPush(MessageIn(room["id"]))
	same(t, event["author"], "alice")
	same(t, event["body"], "hello")
	same(t, event["seq"], 1)
}

func TestASpeakerHearsTheirOwnMessage(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Echo"))
	alice.Drain()
	alice.Call("send", "room", room["id"], "body", "hello")
	same(t, alice.ExpectPush(MessageIn(room["id"]))["body"], "hello")
}

// Proved by a message that must arrive, and what overtook it.
func TestAMessageDoesNotReachOutsideItsRoom(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	shared := alice.Call("open", "invite", []string{"bob"}, "title", unique("Shared"))
	private := alice.Call("open", "invite", []string{}, "title", unique("Private"))
	bob.Drain()

	alice.Call("send", "room", private["id"], "body", "secret")
	alice.Call("send", "room", shared["id"], "body", "public")

	heard, before := bob.CollectPush(MessageIn(shared["id"]))
	same(t, heard["body"], "public")
	for _, event := range before {
		truth(t, obj(event)["room"] != private["id"], "bob heard %s", encode(event))
	}
}

// The push that tells a client a room exists at all.
func TestAnInvitationAnnouncesTheRoomToTheInvited(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Later"))
	bob.Drain()
	alice.Call("invite", "room", room["id"], "principal", "bob")

	event := bob.ExpectPush(func(e Obj) bool { return e["type"] == "room" && obj(e["room"])["id"] == room["id"] })
	same(t, obj(event["room"])["audience"], []string{"alice", "bob"})
}

// A delta would leave a client that missed one permanently behind.
func TestARoomPushCarriesTheWholeRoom(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Whole"))
	bob.Drain()
	alice.Call("invite", "room", room["id"], "principal", "bob")

	pushed := obj(bob.ExpectPush(PushOf("room"))["room"])
	keySet(t, pushed, "id", "title", "kind", "authority", "retention", "createdBy", "createdAt",
		"grants", "restrictedTo", "audience", "occupants", "lastSeq", "moderators", "archive",
		"project", "scope", "task", "state")
}

// The audience is read before the change, or nobody would tell them.
func TestRemovalIsAnnouncedToThePersonRemoved(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Removed"))
	alice.Call("uninvite", "room", room["id"], "principal", "bob")

	// Matched on the shape: the room's creation is still in flight and would
	// otherwise be taken for this.
	event := bob.ExpectPush(func(e Obj) bool {
		pushed := obj(e["room"])
		return e["type"] == "room" && pushed["id"] == room["id"] && !has(pushed["audience"], "bob")
	})
	same(t, obj(event["room"])["audience"], []string{"alice"})
}

// The room push shows who is left; roomGone is what tells the removed to drop it.
func TestRemovalTellsThePersonRemovedToDropTheRoom(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Dropped"))
	bob.Drain()
	alice.Call("uninvite", "room", room["id"], "principal", "bob")
	bob.ExpectPush(gone(room["id"]))
}

func TestLeavingIsAnnouncedAsTheRoomGoingAway(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Left"))
	bob.Drain()
	bob.Call("leave", "room", room["id"])
	bob.ExpectPush(gone(room["id"]))
}

// Or someone removed could keep a transient room alive by sitting in it.
func TestRemovalReleasesThePlaceTheRemovedHeld(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Evicted"), "retention", "transient")
	bob.Call("enter", "room", room["id"])
	reply := alice.Call("uninvite", "room", room["id"], "principal", "bob")
	same(t, obj(reply["room"])["occupants"], []any{})
	same(t, byID(alice.Call("sync")["rooms"])[str(room["id"])]["occupants"], []any{})
}

func TestLosingAGroupReleasesThePlaceItGave(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	team := demo.Call("group.create", "name", unique("Team"), "members", []string{"bob"})
	room := demo.Call("create", "title", unique("Seated"), "invite", []any{group(team["id"])})
	bob.Call("enter", "room", room["id"])
	demo.Call("group.unassign", "group", team["id"], "username", "bob")

	bob.ExpectPush(gone(room["id"]))
	same(t, byID(demo.Call("sync")["rooms"])[str(room["id"])]["occupants"], []any{})
}

func TestEnteringARoomIsAnnouncedToItsAudience(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Arrived"))
	alice.Call("enter", "room", room["id"])

	event := bob.ExpectPush(func(e Obj) bool {
		pushed := obj(e["room"])
		return e["type"] == "room" && pushed["id"] == room["id"] && len(list(pushed["occupants"])) > 0
	})
	same(t, obj(event["room"])["occupants"], []string{"alice"})
}

func TestUnsubscribingIsAnnouncedAsTheChannelGoingAway(t *testing.T) {
	bob := connect(t, "bob")
	channel := connect(t, admin).Call("channel.create", "title", unique("Leaving"), "groups", []string{})
	bob.Call("subscribe", "channel", channel["id"])
	bob.Drain()
	bob.Call("unsubscribe", "channel", channel["id"])

	same(t, bob.ExpectPush(PushOf("roomGone"))["room"], channel["id"])
}

func TestLosingAGroupTakesItsRoomsAway(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	team := demo.Call("group.create", "name", unique("Team"), "members", []string{"bob"})
	room := demo.Call("create", "title", unique("Grouped"), "invite", []any{group(team["id"])})
	bob.ExpectPush(func(e Obj) bool { return e["type"] == "room" && obj(e["room"])["id"] == room["id"] })
	bob.Drain()

	demo.Call("group.unassign", "group", team["id"], "username", "bob")
	same(t, bob.ExpectPush(PushOf("roomGone"))["room"], room["id"])
}

// Anyone may be invited through a group, so everyone hears about one.
func TestAGroupChangeIsAnnouncedToEveryone(t *testing.T) {
	alice, demo := connect(t, "alice"), connect(t, admin)
	alice.Drain()
	name := unique("Team")
	demo.Call("group.create", "name", name, "members", []string{})

	same(t, obj(alice.ExpectPush(PushOf("group"))["group"])["name"], name)
}

// Presence depends on every connection a user has, so these tests take a server
// of their own: another test's socket for bob, still closing, would hide his arrival.
func TestArrivingAndLeavingAreAnnouncedAsPresence(t *testing.T) {
	server := freshServer(t, nil)
	alice := attach(t, server, "alice")
	alice.Drain()
	socket := dial(t, server.Base, login(t, server, "bob").CookieHeader())
	socket.Handshake()

	arrival := alice.ExpectPush(func(e Obj) bool { return e["type"] == "presence" && e["username"] == "bob" })
	same(t, arrival["online"], true)

	socket.Close()
	departure := alice.ExpectPush(func(e Obj) bool {
		return e["type"] == "presence" && e["username"] == "bob" && e["online"] == false
	})
	same(t, departure["online"], false)
}

// It is a fact about a person, and this one already knows it. Proved by bob's
// arrival, which must reach alice: a frame about alice would be queued ahead.
func TestPresenceDoesNotComeBackToItsSubject(t *testing.T) {
	server := freshServer(t, nil)
	alice := attach(t, server, "alice")
	socket := dial(t, server.Base, login(t, server, "bob").CookieHeader())
	socket.Handshake()
	_, before := alice.CollectPush(func(e Obj) bool { return e["type"] == "presence" && e["username"] == "bob" })
	socket.Close()

	for _, event := range before {
		truth(t, obj(event)["username"] != "alice", "alice was told about herself: %s", encode(event))
	}
}

// Closing one of two connections leaves the person online. The laptop holds a
// room, so its release proves the server has handled the close; demo's arrival
// then marks the point by which a wrong departure would have reached alice.
func TestPresenceIsAboutThePersonNotTheConnection(t *testing.T) {
	server := freshServer(t, nil)
	alice := attach(t, server, "alice")
	laptop := attach(t, server, "bob")
	phone := attach(t, server, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Devices"))
	laptop.Call("enter", "room", room["id"])
	alice.ExpectPush(func(e Obj) bool {
		return e["type"] == "room" && obj(e["room"])["id"] == room["id"] && len(list(obj(e["room"])["occupants"])) > 0
	})

	laptop.Close()
	alice.ExpectPush(func(e Obj) bool {
		return e["type"] == "room" && obj(e["room"])["id"] == room["id"] && len(list(obj(e["room"])["occupants"])) == 0
	})
	attach(t, server, admin)
	_, before := alice.CollectPush(func(e Obj) bool { return e["type"] == "presence" && e["username"] == admin })
	for _, event := range before {
		truth(t, obj(event)["type"] != "presence", "presence changed while bob was still connected: %s", encode(event))
	}

	phone.Close()
	alice.ExpectPush(func(e Obj) bool {
		return e["type"] == "presence" && e["username"] == "bob" && e["online"] == false
	})
}

// Or a transient room nobody is in would stay alive forever.
func TestADroppedConnectionReleasesThePlacesItHeld(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Held"))

	socket := dial(t, shared.Base, session(t, "bob").CookieHeader())
	socket.Handshake()
	socket.Call("sync")
	socket.Call("enter", "room", room["id"])

	same(t, byID(alice.Call("sync")["rooms"])[str(room["id"])]["occupants"], []string{"bob"})

	socket.Close()
	alice.ExpectPush(func(e Obj) bool {
		pushed := obj(e["room"])
		occupants, isList := pushed["occupants"].([]any)
		return e["type"] == "room" && pushed["id"] == room["id"] && isList && len(occupants) == 0
	})
}

func TestAFilesystemChangeReachesTheSystemChannelLive(t *testing.T) {
	alice := connect(t, "alice")
	alice.Drain()
	name := unique("live")
	session(t, "alice").Upload("home:/"+name+".txt", []byte("x"))

	event := alice.ExpectPush(MessageIn(systemChannel))
	same(t, event["body"], "alice wrote home:/"+name+".txt")
	same(t, event["kind"], "event")
	same(t, event["author"], "system")
}
