package conformance

// Every chat operation: what it answers, and what it refuses. A refusal is the
// text a client shows a user, so messages are asserted verbatim.

import (
	"fmt"
	"strings"
	"testing"
)

const systemChannel = "system"

// -- sync ---------------------------------------------------------------------

func TestSyncDescribesTheWholeOfWhatAClientNeeds(t *testing.T) {
	reply := connect(t, admin).Call("sync")
	keySet(t, reply, "me", "isAdmin", "users", "groups", "rooms", "channels", "read", "submissions")
	same(t, reply["me"], "demo")
	same(t, reply["isAdmin"], true)
}

func TestSyncListsEveryAccountSorted(t *testing.T) {
	users := connect(t, "alice").Call("sync")["users"]
	same(t, pluck(users, "username"), []string{"alice", "bob", "demo"})
	for _, user := range list(users) {
		keySet(t, obj(user), "username", "online")
	}
}

func TestSyncReportsTheCallerAsOnline(t *testing.T) {
	online := map[string]any{}
	for _, user := range list(connect(t, "alice").Call("sync")["users"]) {
		online[str(obj(user)["username"])] = obj(user)["online"]
	}
	same(t, online["alice"], true)
}

func TestOnlyAnAdministratorIsToldSo(t *testing.T) {
	same(t, connect(t, "alice").Call("sync")["isAdmin"], false)
}

func TestTheSystemChannelExistsAndEveryoneIsInIt(t *testing.T) {
	system := byID(connect(t, "alice").Call("sync")["channels"])[systemChannel]
	same(t, system["title"], "System")
	same(t, system["kind"], "channel")
	same(t, system["grants"], []any{})
	truth(t, has(system["audience"], "alice"), "alice is not in %v", system["audience"])
}

func TestAnUnknownOperationNamesItself(t *testing.T) {
	same(t, connect(t, admin).Refuse("nonsense"), "No such chat operation: nonsense")
}

// -- raising a room -----------------------------------------------------------

func TestOpenRaisesAnAdHocRoomWithTheCreatorInIt(t *testing.T) {
	room := connect(t, "alice").Call("open", "invite", []string{"bob"})
	same(t, room["kind"], "room")
	same(t, room["authority"], "user")
	same(t, room["retention"], "persisted")
	same(t, room["createdBy"], "alice")
	same(t, room["audience"], []string{"alice", "bob"})
	same(t, room["occupants"], []any{})
	same(t, room["lastSeq"], 0)
	same(t, sorted(pluck(room["grants"], "id")), []string{"alice", "bob"})
}

func TestAnUnnamedRoomIsTitledAfterWhoIsInIt(t *testing.T) {
	same(t, connect(t, "alice").Call("open", "invite", []string{"bob"})["title"], "alice, bob")
}

func TestAGivenTitleIsKept(t *testing.T) {
	title := unique("Chat")
	same(t, connect(t, "alice").Call("open", "invite", []string{"bob"}, "title", title)["title"], title)
}

// Two conversations between the same people are two conversations.
func TestAdHocTitlesNeedNotBeUnique(t *testing.T) {
	alice := connect(t, "alice")
	first := alice.Call("open", "invite", []string{"bob"}, "title", "Planning")
	second := alice.Call("open", "invite", []string{"bob"}, "title", "Planning")
	truth(t, first["id"] != second["id"], "both are %v", first["id"])
}

func TestARoomMayBeRaisedTransient(t *testing.T) {
	room := connect(t, "alice").Call("open", "invite", []string{"bob"}, "retention", "transient")
	same(t, room["retention"], "transient")
}

func TestNoOtherRetentionExists(t *testing.T) {
	same(t, connect(t, "alice").Refuse("open", "retention", "forever"), "No such retention: forever")
}

func TestOpenRefusesAnUnknownInvitee(t *testing.T) {
	same(t, connect(t, "alice").Refuse("open", "invite", []string{"nobody"}), "No such user: nobody")
}

func TestOpenRefusesAnUnknownPrincipalKind(t *testing.T) {
	refusal := connect(t, "alice").Refuse("open", "invite", []any{Obj{"kind": "robot", "id": "hal"}})
	same(t, refusal, "No such principal kind: robot")
}

// -- founding a permanent room ------------------------------------------------

func TestCreateFoundsAnAdminRoom(t *testing.T) {
	room := connect(t, admin).Call("create", "title", unique("Engineering"), "invite", []string{"alice"})
	same(t, room["authority"], "admin")
	same(t, room["retention"], "persisted")
	same(t, room["audience"], []string{"alice", "demo"})
}

func TestOnlyAnAdministratorMayFoundOne(t *testing.T) {
	same(t, connect(t, "alice").Refuse("create", "title", unique("Ops")), "Only an administrator may do that")
}

func TestAPermanentRoomNeedsAName(t *testing.T) {
	same(t, connect(t, admin).Refuse("create", "title", "   "), "A permanent room needs a name")
}

// No room is both admin-founded and transient, and asking is not an error.
// See chat-concepts.md, core question 2.
func TestAPermanentRoomCannotBeTransient(t *testing.T) {
	room := connect(t, admin).Call("create", "title", unique("Standing"), "retention", "transient")
	same(t, room["retention"], "persisted")
	same(t, room["authority"], "admin")
}

// "Post it in Engineering" only works if that resolves to one room.
func TestAPermanentNameIsUniqueWithoutCase(t *testing.T) {
	demo := connect(t, admin)
	title := unique("Engineering")
	demo.Call("create", "title", title)
	refusal := demo.Refuse("create", "title", strings.ToUpper(title))
	same(t, refusal, fmt.Sprintf("A permanent room called '%s' already exists", strings.ToUpper(title)))
}

// -- invitation ---------------------------------------------------------------

func TestAnyParticipantMayInviteToAUserRoom(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Ours"))
	reply := alice.Call("invite", "room", room["id"], "principal", "bob")
	same(t, reply["ok"], true)
	same(t, obj(reply["room"])["audience"], []string{"alice", "bob"})
}

func TestAParticipantMayNotInviteToAnAdminRoom(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	room := demo.Call("create", "title", unique("Board"), "invite", []string{"alice"})
	refusal := alice.Refuse("invite", "room", room["id"], "principal", "bob")
	same(t, refusal, "Only an administrator may invite to this room")
}

func TestAStrangerMayNotInviteToAUserRoom(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Private"))
	same(t, bob.Refuse("invite", "room", room["id"], "principal", "bob"), "Not invited to that room")
}

func TestInvitingTwiceChangesNothing(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Twice"))
	again := alice.Call("invite", "room", room["id"], "principal", "bob")
	same(t, obj(again["room"])["audience"], []string{"alice", "bob"})
}

func TestUninviteWithdrawsAGrant(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Brief"))
	reply := alice.Call("uninvite", "room", room["id"], "principal", "bob")
	same(t, obj(reply["room"])["audience"], []string{"alice"})
}

func TestAChannelIsNotARoomToInviteTo(t *testing.T) {
	refusal := connect(t, admin).Refuse("invite", "room", systemChannel, "principal", "alice")
	same(t, refusal, "No such room: "+systemChannel)
}

// -- groups as principals -----------------------------------------------------

func TestAGroupGrantAdmitsItsMembers(t *testing.T) {
	demo := connect(t, admin)
	team := demo.Call("group.create", "name", unique("Team"), "members", []string{"alice"})
	room := demo.Call("create", "title", unique("AllHands"), "invite", []any{group(team["id"])})
	same(t, room["audience"], []string{"alice", "demo"})
}

// Assigning someone admits them everywhere the group was invited.
func TestAGroupGrantFollowsTheGroup(t *testing.T) {
	demo := connect(t, admin)
	team := demo.Call("group.create", "name", unique("Team"), "members", []string{})
	room := demo.Call("create", "title", unique("Later"), "invite", []any{group(team["id"])})
	demo.Call("group.assign", "group", team["id"], "username", "bob")

	same(t, demo.Call("history", "room", room["id"], "since", 0)["room"], room["id"])
	audience := obj(demo.Call("invite", "room", room["id"], "principal", "bob")["room"])["audience"]
	truth(t, has(audience, "bob"), "bob is not in %v", audience)
}

func TestAnUnknownGroupCannotBeInvited(t *testing.T) {
	refusal := connect(t, admin).Refuse("create", "title", unique("Nope"), "invite", []any{group("absent")})
	same(t, refusal, "No such group: absent")
}

// -- leaving ------------------------------------------------------------------

func TestAParticipantMayGiveUpTheirOwnGrant(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Leaving"))
	same(t, bob.Call("leave", "room", room["id"]), Obj{"ok": true})
	_, listed := byID(bob.Call("sync")["rooms"])[str(room["id"])]
	truth(t, !listed, "bob still lists the room")
}

// It would be restored the moment grants were re-evaluated.
func TestAccessFromAGroupCannotBeLeft(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	team := demo.Call("group.create", "name", unique("Team"), "members", []string{"alice"})
	room := demo.Call("create", "title", unique("Inherited"), "invite", []any{group(team["id"])})
	same(t, alice.Refuse("leave", "room", room["id"]),
		"Access to this room comes from a group, so it cannot be left")
}

func TestLeavingARoomOneIsNotInIsRefused(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Solo"))
	same(t, bob.Refuse("leave", "room", room["id"]), "Not invited to that room")
}

// -- occupancy ----------------------------------------------------------------

func TestEnteringTakesAPlaceThatShowsInTheRoom(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Sit"))
	reply := alice.Call("enter", "room", room["id"])
	same(t, reply["ok"], true)
	same(t, reply["room"], room["id"])
	_, isText := reply["occupancy"].(string)
	truth(t, isText, "occupancy is %v", reply["occupancy"])

	same(t, byID(alice.Call("sync")["rooms"])[str(room["id"])]["occupants"], []string{"alice"})
}

func TestExitingGivesThePlaceUp(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Stand"))
	occupancy := alice.Call("enter", "room", room["id"])["occupancy"]
	same(t, alice.Call("exit", "occupancy", occupancy), Obj{"ok": true})

	same(t, byID(alice.Call("sync")["rooms"])[str(room["id"])]["occupants"], []any{})
}

// Otherwise a client could end a room somebody else is sitting in.
func TestOneConnectionMayNotReleaseAnothersPlace(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Shared"))
	occupancy := alice.Call("enter", "room", room["id"])["occupancy"]
	same(t, bob.Refuse("exit", "occupancy", occupancy), "Not in that room")
}

func TestEnteringARoomOneMayNotSeeIsRefused(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Closed"))
	same(t, bob.Refuse("enter", "room", room["id"]), "Not invited to that room")
}

// -- speaking -----------------------------------------------------------------

func TestAMessageIsAnsweredWithItsSequence(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Talk"))
	same(t, alice.Call("send", "room", room["id"], "body", "one"), Obj{"ok": true, "seq": 1})
	same(t, alice.Call("send", "room", room["id"], "body", "two"), Obj{"ok": true, "seq": 2})
}

func TestAnEmptyMessageIsRefused(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Quiet"))
	same(t, alice.Refuse("send", "room", room["id"], "body", "   "), "Empty message")
}

func TestAChannelIsReadOnlyToItsAudience(t *testing.T) {
	same(t, connect(t, "alice").Refuse("send", "room", systemChannel, "body", "hi"), "A channel is read-only")
}

func TestSpeakingInARoomOneMayNotSeeIsRefused(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Sealed"))
	same(t, bob.Refuse("send", "room", room["id"], "body", "hi"), "Not invited to that room")
}

func TestAnUnknownRoomSaysSo(t *testing.T) {
	same(t, connect(t, "alice").Refuse("send", "room", "absent", "body", "hi"), "No such room: absent")
}

// Not a type error: an id that is not a string is not a room that exists.
func TestARoomIdOfTheWrongTypeIsSimplyMissing(t *testing.T) {
	same(t, connect(t, "alice").Refuse("send", "room", 5, "body", "hi"), "No such room: 5")
}

// -- history ------------------------------------------------------------------

func TestHistoryReturnsEverythingAfterACursor(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Log"))
	for _, text := range []string{"one", "two", "three"} {
		alice.Call("send", "room", room["id"], "body", text)
	}

	reply := alice.Call("history", "room", room["id"], "since", 1)
	same(t, reply["room"], room["id"])
	same(t, reply["since"], 1)
	same(t, reply["lastSeq"], 3)
	same(t, pluck(reply["messages"], "seq"), []int{2, 3})
}

func TestAMessageCarriesTheWholeShape(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Shape"))
	alice.Call("send", "room", room["id"], "body", "hello")

	message := obj(list(alice.Call("history", "room", room["id"], "since", 0)["messages"])[0])
	keySet(t, message, "room", "seq", "author", "kind", "subject", "body", "at")
	null(t, message, "subject")
	same(t, message["author"], "alice")
	same(t, message["kind"], "text")
	same(t, message["body"], "hello")
	truth(t, num(message["at"]) > 0, "at is %v", message["at"])
}

func TestAnUnparseableCursorMeansTheBeginning(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Cursor"))
	alice.Call("send", "room", room["id"], "body", "one")

	reply := alice.Call("history", "room", room["id"], "since", "nonsense")
	same(t, reply["since"], 0)
	same(t, pluck(reply["messages"], "seq"), []int{1})
}

func TestAnInvitationIsRecordedAsAnEventInTheRoom(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Events"))
	alice.Call("invite", "room", room["id"], "principal", "bob")

	messages := list(alice.Call("history", "room", room["id"], "since", 0)["messages"])
	message := obj(messages[len(messages)-1])
	same(t, message["kind"], "event")
	same(t, message["author"], "system")
	same(t, message["body"], "alice invited bob")
}

func TestHistoryOfARoomOneMayNotSeeIsRefused(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Hidden"))
	same(t, bob.Refuse("history", "room", room["id"], "since", 0), "Not invited to that room")
}

// -- read cursors -------------------------------------------------------------

func TestAReadCursorIsRecorded(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Read"))
	alice.Call("send", "room", room["id"], "body", "one")
	same(t, alice.Call("read", "room", room["id"], "seq", 1), Obj{"ok": true, "room": room["id"], "seq": 1})
	same(t, obj(alice.Call("sync")["read"])[str(room["id"])], 1)
}

func TestAReadCursorNeverMovesBackwards(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Rewind"))
	for _, text := range []string{"one", "two", "three"} {
		alice.Call("send", "room", room["id"], "body", text)
	}

	alice.Call("read", "room", room["id"], "seq", 3)
	alice.Call("read", "room", room["id"], "seq", 1)
	same(t, obj(alice.Call("sync")["read"])[str(room["id"])], 3)
}

func TestAReadCursorIsTheSameFactFromEveryConnection(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Devices"))
	alice.Call("send", "room", room["id"], "body", "one")
	alice.Call("read", "room", room["id"], "seq", 1)

	second := connect(t, "alice")
	same(t, obj(second.Call("sync")["read"])[str(room["id"])], 1)
}

// -- groups -------------------------------------------------------------------

func TestAGroupIsCreatedWithItsMembers(t *testing.T) {
	name := unique("Team")
	team := connect(t, admin).Call("group.create", "name", name, "members", []string{"alice", "bob"})
	keySet(t, team, "id", "name", "members")
	same(t, team["name"], name)
	same(t, team["members"], []string{"alice", "bob"})
}

func TestUnknownMembersAreDroppedRatherThanRefused(t *testing.T) {
	team := connect(t, admin).Call("group.create", "name", unique("Team"), "members", []string{"alice", "ghost"})
	same(t, team["members"], []string{"alice"})
}

func TestOnlyAnAdministratorManagesGroups(t *testing.T) {
	refusal := connect(t, "alice").Refuse("group.create", "name", unique("Rogue"))
	same(t, refusal, "Only an administrator may do that")
}

func TestAGroupNeedsAName(t *testing.T) {
	same(t, connect(t, admin).Refuse("group.create", "name", "  "), "A group needs a name")
}

func TestAssignmentAddsAndUnassignmentRemoves(t *testing.T) {
	demo := connect(t, admin)
	team := demo.Call("group.create", "name", unique("Team"), "members", []string{})
	same(t, demo.Call("group.assign", "group", team["id"], "username", "bob")["members"], []string{"bob"})
	same(t, demo.Call("group.unassign", "group", team["id"], "username", "bob")["members"], []string{})
}

func TestAssigningToAnUnknownGroupSaysSo(t *testing.T) {
	refusal := connect(t, admin).Refuse("group.assign", "group", "absent", "username", "bob")
	same(t, refusal, "No such group: absent")
}

func TestAssigningAnUnknownUserSaysSo(t *testing.T) {
	demo := connect(t, admin)
	team := demo.Call("group.create", "name", unique("Team"))
	same(t, demo.Refuse("group.assign", "group", team["id"], "username", "ghost"), "No such user: ghost")
}

// -- presence and occupancy ---------------------------------------------------

// Two facts, not one. Presence says whether someone could reply.
func TestPresenceIsGlobalAndNamesNoRoom(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{}, "title", unique("Alone"))
	alice.Call("enter", "room", room["id"])

	// bob shares no room with alice and still sees her online.
	online := map[string]any{}
	for _, user := range list(bob.Call("sync")["users"]) {
		online[str(obj(user)["username"])] = obj(user)["online"]
	}
	same(t, online["alice"], true)
}

// And it is the fact a transient room's lifetime is measured from.
func TestOccupancyIsPerRoom(t *testing.T) {
	alice := connect(t, "alice")
	here := alice.Call("open", "invite", []string{}, "title", unique("Here"))
	elsewhere := alice.Call("open", "invite", []string{}, "title", unique("Elsewhere"))
	alice.Call("enter", "room", here["id"])

	rooms := byID(alice.Call("sync")["rooms"])
	same(t, rooms[str(here["id"])]["occupants"], []string{"alice"})
	same(t, rooms[str(elsewhere["id"])]["occupants"], []any{})
}

func occupying(room any, occupants string) Pred {
	return func(e Obj) bool {
		pushed := obj(e["room"])
		return e["type"] == "room" && pushed["id"] == room && encode(pushed["occupants"]) == occupants
	}
}

// A person is in one place at a time: entering a room leaves the one they were in.
func TestEnteringARoomLeavesTheRoomYouWereIn(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	first := alice.Call("open", "invite", []string{"bob"}, "title", unique("First"))
	second := alice.Call("open", "invite", []string{}, "title", unique("Second"))
	held := alice.Call("enter", "room", first["id"])["occupancy"]
	bob.ExpectPush(occupying(first["id"], `["alice"]`))

	alice.Call("enter", "room", second["id"])
	same(t, alice.ExpectPush(PushOf("exited")), Obj{"type": "exited", "room": first["id"], "occupancy": held})
	bob.ExpectPush(occupying(first["id"], `[]`))

	rooms := byID(alice.Call("sync")["rooms"])
	same(t, rooms[str(first["id"])]["occupants"], []any{})
	same(t, rooms[str(second["id"])]["occupants"], []string{"alice"})
}

// On any device: entering on one takes the person out on the other.
func TestEnteringOnAnotherConnectionReleasesThisOne(t *testing.T) {
	laptop, phone := connect(t, "alice"), connect(t, "alice")
	here := laptop.Call("open", "invite", []string{}, "title", unique("Laptop"))
	there := laptop.Call("open", "invite", []string{}, "title", unique("Phone"))
	held := laptop.Call("enter", "room", here["id"])["occupancy"]

	phone.Call("enter", "room", there["id"])
	same(t, laptop.ExpectPush(PushOf("exited")), Obj{"type": "exited", "room": here["id"], "occupancy": held})
	// Released rather than refused: the laptop may still say it has left.
	same(t, laptop.Call("exit", "occupancy", held), Obj{"ok": true})
}

// Two devices in the same room are still one person in one place.
func TestTwoConnectionsMayShareOneRoom(t *testing.T) {
	laptop, phone := connect(t, "alice"), connect(t, "alice")
	room := laptop.Call("open", "invite", []string{}, "title", unique("Shared"))
	laptop.Call("enter", "room", room["id"])
	phone.Call("enter", "room", room["id"])

	// Both entries are announced; a release would have come before the second.
	entries := 0
	_, before := laptop.CollectPush(func(e Obj) bool {
		if occupying(room["id"], `["alice"]`)(e) {
			entries++
		}
		return entries == 2
	})
	for _, event := range before {
		truth(t, obj(event)["type"] != "exited", "a second device in the same room released the first: %s", encode(event))
	}
}

// -- channels -----------------------------------------------------------------

func TestASubscriptionCanBeDroppedAndRetaken(t *testing.T) {
	bob := connect(t, "bob")
	same(t, bob.Call("unsubscribe", "channel", systemChannel), Obj{"ok": true})
	_, listed := byID(bob.Call("sync")["channels"])[systemChannel]
	truth(t, !listed, "bob still lists %s", systemChannel)

	channel := bob.Call("subscribe", "channel", systemChannel)
	truth(t, has(channel["audience"], "bob"), "bob is not in %v", channel["audience"])
}

func TestARoomIsNotAChannelToSubscribeTo(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("NotAChannel"))
	same(t, alice.Refuse("subscribe", "channel", room["id"]), fmt.Sprintf("No such room: %v", room["id"]))
}

func TestAnUnknownChannelSaysSo(t *testing.T) {
	for _, operation := range []string{"subscribe", "unsubscribe"} {
		t.Run(operation, func(t *testing.T) {
			same(t, connect(t, "alice").Refuse(operation, "channel", "absent"), "No such room: absent")
		})
	}
}

// -- the system channel -------------------------------------------------------

func TestAFilesystemChangeIsAnnouncedOnTheSystemChannel(t *testing.T) {
	alice := connect(t, "alice")
	name := unique("announced")
	before := alice.Call("history", "room", systemChannel, "since", 0)["lastSeq"]
	session(t, "alice").Upload("home:/"+name+".txt", []byte("x"))

	messages := alice.Call("history", "room", systemChannel, "since", before)["messages"]
	want := "alice wrote home:/" + name + ".txt"
	truth(t, has(pluck(messages, "body"), want), "no %q in %s", want, encode(messages))
}

func TestAFailedMutationAnnouncesNothing(t *testing.T) {
	alice := connect(t, "alice")
	before := alice.Call("history", "room", systemChannel, "since", 0)["lastSeq"]
	session(t, "alice").Vfs("unlink", Obj{"path": "home:/" + unique("absent")})

	after := alice.Call("history", "room", systemChannel, "since", before)
	same(t, after["messages"], []any{})
}
