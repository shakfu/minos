package conformance

// A channel's audience rule: open, or restricted to named groups. A test that
// restricts `system` launches its own server, or every other test would see it.
//
// Eligibility is read at delivery, not frozen when the subscription was stored.
// A subscription is the subscriber's own act and outlives the membership.

import (
	"fmt"
	"strings"
	"testing"
)

// channelOf is the system channel as this client sees it, or nil.
func channelOf(socket *Socket) Obj {
	return byID(socket.Call("sync")["channels"])[systemChannel]
}

// Restriction is retroactive: a rule about the audience, not the act.
func TestRestrictingAChannelDropsEveryoneOutsideTheGroup(t *testing.T) {
	server := freshServer(t, nil)
	demo, alice, bob := attach(t, server, "demo"), attach(t, server, "alice"), attach(t, server, "bob")
	ops := demo.Call("group.create", "name", unique("Ops"), "members", []string{"alice"})

	reply := demo.Call("channel.admit", "channel", systemChannel, "group", ops["id"])

	same(t, reply["ok"], true)
	same(t, obj(reply["channel"])["restrictedTo"], []any{ops["id"]})
	same(t, obj(reply["channel"])["audience"], []string{"alice"})

	// bob was subscribed at start-up and is in no admitted group. He is told once,
	// here: the next message on the channel is not addressed to him.
	bob.ExpectPush(gone(systemChannel))
	truth(t, channelOf(bob) == nil, "bob still sees the channel")
	same(t, bob.Refuse("history", "room", systemChannel, "since", 0), "Not invited to that room")
	same(t, bob.Refuse("subscribe", "channel", systemChannel), "That channel is restricted")

	truth(t, channelOf(alice) != nil, "alice lost the channel")
}

// The subscription survives losing the group, and applies again on return.
func TestEligibilityIsReReadRatherThanSnapshotted(t *testing.T) {
	server := freshServer(t, nil)
	demo, bob := attach(t, server, "demo"), attach(t, server, "bob")
	ops := demo.Call("group.create", "name", unique("Ops"), "members", []string{})

	demo.Call("channel.admit", "channel", systemChannel, "group", ops["id"])
	bob.ExpectPush(gone(systemChannel))

	// bob never subscribes again: the subscription he made at start-up was left
	// alone, so admitting a group he is in is enough.
	demo.Call("group.assign", "group", ops["id"], "username", "bob")
	truth(t, channelOf(bob) != nil, "bob does not see the channel")
	same(t, bob.Call("history", "room", systemChannel, "since", 0)["messages"], []any{})

	demo.Call("group.unassign", "group", ops["id"], "username", "bob")
	bob.ExpectPush(gone(systemChannel))
	truth(t, channelOf(bob) == nil, "bob still sees the channel")

	// No groups is an open channel, not a closed one.
	reply := demo.Call("channel.revoke", "channel", systemChannel, "group", ops["id"])
	same(t, obj(reply["channel"])["restrictedTo"], []any{})
	same(t, sorted(obj(reply["channel"])["audience"]), []string{"alice", "bob", "demo"})
	truth(t, channelOf(bob) != nil, "bob does not see the reopened channel")
}

func TestOnlyAnAdministratorSetsTheRule(t *testing.T) {
	alice, demo := connect(t, "alice"), connect(t, admin)
	ops := demo.Call("group.create", "name", unique("Ops"), "members", []string{})
	refusal := "Only an administrator may do that"

	same(t, alice.Refuse("channel.admit", "channel", systemChannel, "group", ops["id"]), refusal)
	same(t, alice.Refuse("channel.revoke", "channel", systemChannel, "group", ops["id"]), refusal)
}

func TestTheRuleNamesAGroupThatExists(t *testing.T) {
	refusal := connect(t, admin).Refuse("channel.admit", "channel", systemChannel, "group", "absent")
	same(t, refusal, "No such group: absent")
}

func TestARoomHasNoAudienceRule(t *testing.T) {
	demo := connect(t, admin)
	room := demo.Call("open", "invite", []string{}, "title", unique("NotAChannel"))
	same(t, room["restrictedTo"], []any{})
	same(t, demo.Refuse("channel.admit", "channel", room["id"], "group", "any"),
		fmt.Sprintf("No such room: %v", room["id"]))
}

// -- founding one, and writing to it ------------------------------------------

func TestAnAdminFoundsAChannelAndPublishesToIt(t *testing.T) {
	demo, alice := connect(t, admin), connect(t, "alice")
	channel := demo.Call("channel.create", "title", unique("Announcements"), "groups", []string{})

	same(t, channel["kind"], "channel")
	same(t, channel["authority"], "admin")
	same(t, channel["createdBy"], "demo")
	same(t, channel["audience"], []any{})
	same(t, channel["restrictedTo"], []any{})

	// Nobody is subscribed to a channel that did not exist a moment ago, so the
	// only way anyone hears of it is the machine channel everyone is in.
	alice.ExpectPush(func(e Obj) bool {
		return e["type"] == "message" && e["room"] == systemChannel &&
			strings.Contains(str(e["body"]), str(channel["id"]))
	})

	alice.Call("subscribe", "channel", channel["id"])
	same(t, demo.Call("channel.publish", "channel", channel["id"], "body", "the first")["seq"], 1)
	same(t, alice.ExpectPush(MessageIn(channel["id"]))["body"], "the first")

	messages := list(alice.Call("history", "room", channel["id"], "since", 0)["messages"])
	published := obj(messages[len(messages)-1])
	same(t, published["author"], "demo")
	same(t, published["kind"], "text")
}

func TestAChannelIsFoundedRestrictedWhenItNamesGroups(t *testing.T) {
	demo, bob := connect(t, admin), connect(t, "bob")
	ops := demo.Call("group.create", "name", unique("Ops"), "members", []string{"alice"})
	channel := demo.Call("channel.create", "title", unique("Restricted"), "groups", []any{ops["id"]})

	same(t, channel["restrictedTo"], []any{ops["id"]})
	same(t, bob.Refuse("subscribe", "channel", channel["id"]), "That channel is restricted")
}

func TestAChannelNameIsUniqueAmongChannels(t *testing.T) {
	demo := connect(t, admin)
	title := unique("Twice")
	demo.Call("channel.create", "title", title, "groups", []string{})
	same(t, demo.Refuse("channel.create", "title", title, "groups", []string{}),
		fmt.Sprintf("A channel called '%s' already exists", title))
}

func TestFoundingAndPublishingAreTheAdministrators(t *testing.T) {
	alice := connect(t, "alice")
	refusal := "Only an administrator may do that"
	same(t, alice.Refuse("channel.create", "title", unique("Mine"), "groups", []string{}), refusal)
	same(t, alice.Refuse("channel.publish", "channel", systemChannel, "body", "hi"), refusal)
}

// send stays refused on a channel, whoever asks. Subscribed first, because
// access is checked before what the room is.
func TestPublishingIsNotSending(t *testing.T) {
	demo := connect(t, admin)
	channel := demo.Call("channel.create", "title", unique("ReadOnly"), "groups", []string{})
	demo.Call("subscribe", "channel", channel["id"])
	same(t, demo.Refuse("send", "room", channel["id"], "body", "hi"), "A channel is read-only")
	same(t, demo.Refuse("channel.publish", "channel", channel["id"], "body", "  "), "Empty message")
}

func TestAChannelCannotBeFoundedOverAGroupThatIsNotThere(t *testing.T) {
	refusal := connect(t, admin).Refuse("channel.create", "title", unique("Ghost"), "groups", []string{"absent"})
	same(t, refusal, "No such group: absent")
}
