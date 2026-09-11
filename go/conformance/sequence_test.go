package conformance

// The delivery contract: a per-room sequence number is what makes a dropped,
// duplicated or reordered push recoverable, and these are the properties a
// client's repair relies on.

import (
	"fmt"
	"testing"
)

// The server's backfill cap. A reply this long is a truncation, and lastSeq is
// how the client finds out.
const historyLimit = 200

// fill sends count messages without waiting for each reply in turn, which is
// what the pid is for; a round trip each would make the cap tests the slowest.
func fill(socket *Socket, room any, count int) {
	pids := make([]int, count)
	for n := range count {
		pids[n] = socket.SendOp("send", "room", room, "body", fmt.Sprint(n))
	}
	for _, pid := range pids {
		socket.AwaitReply(pid)
	}
}

func TestASequenceStartsAtOneAndNeverRepeats(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Seq"))
	var seqs []any
	for n := range 5 {
		seqs = append(seqs, alice.Call("send", "room", room["id"], "body", fmt.Sprint(n))["seq"])
	}
	same(t, seqs, []int{1, 2, 3, 4, 5})
}

func TestSequencesArePerRoom(t *testing.T) {
	alice := connect(t, "alice")
	first := alice.Call("open", "invite", []string{}, "title", unique("A"))
	second := alice.Call("open", "invite", []string{}, "title", unique("B"))

	alice.Call("send", "room", first["id"], "body", "one")
	alice.Call("send", "room", first["id"], "body", "two")
	same(t, alice.Call("send", "room", second["id"], "body", "one")["seq"], 1)
}

// A client applies both against one cursor, so both must be numbered.
func TestAnEventConsumesASequenceNumberToo(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Mixed"))
	alice.Call("send", "room", room["id"], "body", "one")
	alice.Call("invite", "room", room["id"], "principal", "bob")
	same(t, alice.Call("send", "room", room["id"], "body", "two")["seq"], 3)

	kinds := pluck(alice.Call("history", "room", room["id"], "since", 0)["messages"], "kind")
	same(t, kinds, []string{"text", "event", "text"})
}

// Which is why discarding a message from a gap is safe.
func TestAMessageIsStoredBeforeItIsPublished(t *testing.T) {
	alice, bob := connect(t, "alice"), connect(t, "bob")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Durable"))
	alice.Call("send", "room", room["id"], "body", "hello")

	pushed := bob.ExpectPush(MessageIn(room["id"]))
	stored := list(alice.Call("history", "room", room["id"], "since", num(pushed["seq"])-1)["messages"])

	// A push is the stored message plus the type that says which push it is.
	withoutType := Obj{}
	for key, value := range pushed {
		if key != "type" {
			withoutType[key] = value
		}
	}
	same(t, stored[0], withoutType)
}

func TestTheRoomReportsWhereItEnds(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("End"))
	for n := range 3 {
		alice.Call("send", "room", room["id"], "body", fmt.Sprint(n))
	}

	reply := alice.Call("history", "room", room["id"], "since", 3)
	same(t, reply["messages"], []any{})
	same(t, reply["lastSeq"], 3)
}

// The repair a client makes when it sees a gap.
func TestACursorReturnsExactlyTheDifference(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Gap"))
	for n := range 6 {
		alice.Call("send", "room", room["id"], "body", fmt.Sprint(n))
	}

	missed := alice.Call("history", "room", room["id"], "since", 2)["messages"]
	same(t, pluck(missed, "seq"), []int{3, 4, 5, 6})
	same(t, pluck(missed, "body"), []string{"2", "3", "4", "5"})
}

// A client a thousand behind wants the recent ones, not the oldest.
func TestABackfillIsCappedAtTheNewest(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Long"))
	total := historyLimit + 10
	fill(alice, room["id"], total)

	messages := list(alice.Call("history", "room", room["id"], "since", 0)["messages"])
	same(t, len(messages), historyLimit)
	same(t, obj(messages[len(messages)-1])["seq"], total)
	same(t, obj(messages[0])["seq"], total-historyLimit+1)
}

// Asking again from the same cursor returns the same slice, so the shortfall
// cannot be looped over; the client must notice it through lastSeq.
func TestATruncatedBackfillIsDetectableRatherThanPageable(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{}, "title", unique("Capped"))
	total := historyLimit + 10
	fill(alice, room["id"], total)

	first := alice.Call("history", "room", room["id"], "since", 0)
	again := alice.Call("history", "room", room["id"], "since", 0)
	same(t, first, again)

	shortfall := num(obj(list(first["messages"])[0])["seq"]) - num(first["since"]) - 1
	same(t, shortfall, 10)
	same(t, first["lastSeq"], total)
}

// The same mechanism as a dropped frame, over a longer absence.
func TestAReconnectingClientRepairsFromItsCursor(t *testing.T) {
	alice := connect(t, "alice")
	room := alice.Call("open", "invite", []string{"bob"}, "title", unique("Away"))
	alice.Call("send", "room", room["id"], "body", "before")

	returning := connect(t, "bob")
	same(t, byID(returning.Call("sync")["rooms"])[str(room["id"])]["lastSeq"], 1)

	alice.Call("send", "room", room["id"], "body", "after")
	caughtUp := returning.Call("history", "room", room["id"], "since", 0)["messages"]
	same(t, pluck(caughtUp, "body"), []string{"before", "after"})
}
