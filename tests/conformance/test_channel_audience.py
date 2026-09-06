"""A channel's audience rule: open, or restricted to named groups.

Every test here that restricts a channel launches its own server. `system` is
the only channel the contract declares, so restricting it on the shared one
would change what every other test is looking at.

The rule the assertions are built around: eligibility is read at delivery, not
frozen when the subscription was stored. A subscription is the subscriber's own
act and outlives the group membership that allowed it.
"""

SYSTEM_CHANNEL = "system"


def gone(room_id):
    return lambda event: event.get("type") == "roomGone" and event.get("room") == room_id


def channel_of(socket):
    """The system channel as this client currently sees it, or None."""
    channels = {c["id"]: c for c in socket.call("sync")["channels"]}
    return channels.get(SYSTEM_CHANNEL)


def test_restricting_a_channel_drops_everyone_outside_the_group(
    fresh_server, attach, unique
):
    """Restriction is retroactive: it is a rule about the audience, not the act."""
    server = fresh_server()
    demo, alice, bob = (attach(server, name) for name in ("demo", "alice", "bob"))
    group = demo.call("group.create", name=unique("Ops"), members=["alice"])

    reply = demo.call("channel.admit", channel=SYSTEM_CHANNEL, group=group["id"])

    assert reply["ok"] is True
    assert reply["channel"]["restrictedTo"] == [group["id"]]
    assert reply["channel"]["audience"] == ["alice"]

    # Bob was subscribed at start-up and is in no admitted group. He is told
    # once, here; nothing else would tell him, because the next message on the
    # channel is not addressed to him.
    bob.expect_push(gone(SYSTEM_CHANNEL))
    assert channel_of(bob) is None
    assert bob.refuse("history", room=SYSTEM_CHANNEL, since=0) == "Not invited to that room"
    assert bob.refuse("subscribe", channel=SYSTEM_CHANNEL) == "That channel is restricted"

    assert channel_of(alice) is not None


def test_eligibility_is_re_read_rather_than_snapshotted(fresh_server, attach, unique):
    """The subscription survives losing the group, and applies again on return."""
    server = fresh_server()
    demo, bob = attach(server, "demo"), attach(server, "bob")
    group = demo.call("group.create", name=unique("Ops"), members=[])

    demo.call("channel.admit", channel=SYSTEM_CHANNEL, group=group["id"])
    bob.expect_push(gone(SYSTEM_CHANNEL))

    # Bob never subscribes again. Admitting a group he is in is enough, which is
    # only true because the subscription he made at start-up was left alone.
    demo.call("group.assign", group=group["id"], username="bob")
    assert channel_of(bob) is not None
    assert bob.call("history", room=SYSTEM_CHANNEL, since=0)["messages"] == []

    demo.call("group.unassign", group=group["id"], username="bob")
    bob.expect_push(gone(SYSTEM_CHANNEL))
    assert channel_of(bob) is None

    # And the rule itself can be withdrawn: no groups is an open channel, not a
    # closed one.
    reply = demo.call("channel.revoke", channel=SYSTEM_CHANNEL, group=group["id"])
    assert reply["channel"]["restrictedTo"] == []
    assert set(reply["channel"]["audience"]) == {"alice", "bob", "demo"}
    assert channel_of(bob) is not None


def test_only_an_administrator_sets_the_rule(alice, demo, unique):
    group = demo.call("group.create", name=unique("Ops"), members=[])
    refusal = "Only an administrator may do that"

    assert alice.refuse("channel.admit", channel=SYSTEM_CHANNEL, group=group["id"]) == refusal
    assert alice.refuse("channel.revoke", channel=SYSTEM_CHANNEL, group=group["id"]) == refusal


def test_the_rule_names_a_group_that_exists(demo):
    assert demo.refuse("channel.admit", channel=SYSTEM_CHANNEL, group="absent") == (
        "No such group: absent"
    )


def test_a_room_has_no_audience_rule(demo, unique):
    room = demo.call("open", invite=[], title=unique("NotAChannel"))
    assert room["restrictedTo"] == []
    assert demo.refuse("channel.admit", channel=room["id"], group="any") == (
        f"No such room: {room['id']}"
    )


# -- founding one, and writing to it -----------------------------------------


def test_an_admin_founds_a_channel_and_publishes_to_it(demo, alice, unique):
    title = unique("Announcements")
    channel = demo.call("channel.create", title=title, groups=[])

    assert channel["kind"] == "channel"
    assert channel["authority"] == "admin"
    assert channel["createdBy"] == "demo"
    assert channel["audience"] == []
    assert channel["restrictedTo"] == []

    # Nobody is subscribed to a channel that did not exist a moment ago, so the
    # only way anyone hears of it is the machine channel everyone is in.
    assert alice.expect_push(
        lambda e: e.get("type") == "message"
        and e.get("room") == SYSTEM_CHANNEL
        and channel["id"] in e.get("body", "")
    )

    alice.call("subscribe", channel=channel["id"])
    assert demo.call("channel.publish", channel=channel["id"], body="the first")["seq"] == 1
    assert alice.expect_push(
        lambda e: e.get("type") == "message" and e.get("room") == channel["id"]
    )["body"] == "the first"

    published = alice.call("history", room=channel["id"], since=0)["messages"][-1]
    assert published["author"] == "demo"
    assert published["kind"] == "text"


def test_a_channel_is_founded_restricted_when_it_names_groups(demo, bob, unique):
    group = demo.call("group.create", name=unique("Ops"), members=["alice"])
    channel = demo.call(
        "channel.create", title=unique("Restricted"), groups=[group["id"]]
    )

    assert channel["restrictedTo"] == [group["id"]]
    assert bob.refuse("subscribe", channel=channel["id"]) == "That channel is restricted"


def test_a_channel_name_is_unique_among_channels(demo, unique):
    title = unique("Twice")
    demo.call("channel.create", title=title, groups=[])
    assert demo.refuse("channel.create", title=title, groups=[]) == (
        f"A channel called '{title}' already exists"
    )


def test_founding_and_publishing_are_the_administrator_s(alice, demo, unique):
    refusal = "Only an administrator may do that"
    assert alice.refuse("channel.create", title=unique("Mine"), groups=[]) == refusal
    assert alice.refuse("channel.publish", channel=SYSTEM_CHANNEL, body="hi") == refusal


def test_publishing_is_not_sending(demo, unique):
    """`send` stays refused on a channel, whoever is asking.

    Subscribed first, because access is checked before what the room is: a
    channel nobody is in refuses the way any unreachable room does.
    """
    channel = demo.call("channel.create", title=unique("ReadOnly"), groups=[])
    demo.call("subscribe", channel=channel["id"])
    assert demo.refuse("send", room=channel["id"], body="hi") == "A channel is read-only"
    assert demo.refuse("channel.publish", channel=channel["id"], body="  ") == "Empty message"


def test_a_channel_cannot_be_founded_over_a_group_that_is_not_there(demo, unique):
    assert demo.refuse(
        "channel.create", title=unique("Ghost"), groups=["absent"]
    ) == "No such group: absent"
