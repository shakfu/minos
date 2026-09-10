"""Submissions and moderation: `chat-concepts.md` section 5.

Beyond the core, so these run only against a server that claims the full
contract (`MINOS_CONFORMANCE_SCOPE=full`); `server/` is frozen at the core.

The rule the assertions are built around: a submission is outside its channel's
sequence. Submitting and rejecting issue no number and approval issues the next
one, so what subscribers read stays contiguous whatever the moderators decide.
"""

import pytest

pytestmark = pytest.mark.beyond_core

NO_SUBMISSIONS = "That channel accepts no submissions"
MODERATORS_ONLY = "Only a moderator may do that"
DECIDED = "That submission has been decided"


def submission_push(state, submission_id):
    def matches(event):
        return (
            isinstance(event, dict)
            and event.get("type") == "submission"
            and event["submission"]["id"] == submission_id
            and event["submission"]["state"] == state
        )

    return matches


def found(demo, unique, *subscribers):
    """A channel alice moderates, with each socket given subscribed to it."""
    channel = demo.call("channel.create", title=unique("Curated"), groups=[])
    demo.call("channel.appoint", channel=channel["id"], username="alice")
    for socket in subscribers:
        socket.call("subscribe", channel=channel["id"])
    return channel["id"]


def mine(socket):
    """The caller's own open submissions, by id, as `sync` reports them."""
    return {s["id"]: s for s in socket.call("sync")["submissions"]}


def queue_of(socket, channel_id):
    return [s["id"] for s in socket.call("channel.queue", channel=channel_id)["submissions"]]


# -- which channels take submissions -----------------------------------------


def test_a_channel_with_no_moderator_accepts_no_submissions(demo, bob, unique):
    """A price feed: a producer publishes, an audience reads, nothing to curate."""
    channel = demo.call("channel.create", title=unique("Feed"), groups=[])
    bob.call("subscribe", channel=channel["id"])

    assert channel["moderators"] == []
    assert bob.refuse("channel.submit", channel=channel["id"], body="buy") == NO_SUBMISSIONS


def test_appointing_is_the_administrator_s_and_is_announced(demo, alice, bob, unique):
    channel = demo.call("channel.create", title=unique("Curated"), groups=[])
    bob.call("subscribe", channel=channel["id"])

    reply = demo.call("channel.appoint", channel=channel["id"], username="alice")
    assert reply["ok"] is True
    assert reply["channel"]["moderators"] == ["alice"]
    # Subscribers are told the channel now takes submissions.
    bob.expect_push(
        lambda e: e.get("type") == "room"
        and e["room"]["id"] == channel["id"]
        and e["room"]["moderators"] == ["alice"]
    )

    refusal = "Only an administrator may do that"
    assert alice.refuse("channel.appoint", channel=channel["id"], username="bob") == refusal
    assert alice.refuse("channel.dismiss", channel=channel["id"], username="alice") == refusal
    assert demo.refuse("channel.appoint", channel=channel["id"], username="nobody") == (
        "No such user: nobody"
    )


def test_a_room_has_no_moderators(demo, unique):
    room = demo.call("open", invite=[], title=unique("Room"))
    assert room["moderators"] == []
    assert demo.refuse("channel.appoint", channel=room["id"], username="alice") == (
        f"No such room: {room['id']}"
    )


def test_a_moderator_publishes_directly(demo, alice, bob, unique):
    """Moderators widen the set of producers; they do not replace the admin."""
    channel_id = found(demo, unique, bob)

    assert alice.call("channel.publish", channel=channel_id, body="from the desk")["seq"] == 1
    assert demo.call("channel.publish", channel=channel_id, body="and the admin")["seq"] == 2
    assert bob.refuse("channel.publish", channel=channel_id, body="me too") == (
        "Only an administrator or a moderator may do that"
    )


# -- the life of a submission -------------------------------------------------


def test_a_submission_waits_for_a_moderator_and_takes_no_sequence(demo, alice, bob, unique):
    channel_id = found(demo, unique, bob)

    submission = bob.call("channel.submit", channel=channel_id, body="  a tip  ")
    assert set(submission) == {"id", "channel", "author", "body", "at", "state", "comment"}
    assert (submission["channel"], submission["author"]) == (channel_id, "bob")
    assert submission["body"] == "a tip"
    assert submission["state"] == "pending"
    assert submission["comment"] is None

    alice.expect_push(submission_push("pending", submission["id"]))
    assert queue_of(alice, channel_id) == [submission["id"]]
    assert mine(bob)[submission["id"]]["state"] == "pending"
    # Nothing reached the channel: its sequence has not moved.
    assert bob.call("history", room=channel_id, since=0)["lastSeq"] == 0


def test_approval_publishes_it_as_its_author_wrote_it(demo, alice, bob, unique):
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="a tip")

    assert alice.call("submission.approve", submission=submission["id"]) == {"ok": True, "seq": 1}

    bob.expect_push(submission_push("approved", submission["id"]))
    published = bob.call("history", room=channel_id, since=0)["messages"][-1]
    assert (published["seq"], published["author"], published["body"], published["kind"]) == (
        1, "bob", "a tip", "text",
    )
    assert queue_of(alice, channel_id) == []
    assert submission["id"] not in mine(bob)


def test_a_rejection_leaves_no_gap(demo, alice, bob, unique):
    """What a rejection must not do is cost subscribers a sequence number."""
    channel_id = found(demo, unique, bob)
    first = bob.call("channel.submit", channel=channel_id, body="one")
    second = bob.call("channel.submit", channel=channel_id, body="two")

    alice.call("submission.reject", submission=first["id"])
    alice.call("submission.approve", submission=second["id"])

    messages = bob.call("history", room=channel_id, since=0)["messages"]
    assert [(m["seq"], m["body"]) for m in messages] == [(1, "two")]


def test_a_rejection_is_reported_and_kept_until_acknowledged(demo, alice, bob, unique):
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="nearly")

    reply = alice.call("submission.reject", submission=submission["id"], comment=" too long ")
    assert reply == {"ok": True}

    pushed = bob.expect_push(submission_push("rejected", submission["id"]))["submission"]
    assert pushed["comment"] == "too long"
    # The text comes back with it, so the author can revise and resubmit.
    assert pushed["body"] == "nearly"
    assert queue_of(alice, channel_id) == []

    assert mine(bob)[submission["id"]]["state"] == "rejected"
    assert bob.call("submission.acknowledge", submission=submission["id"]) == {"ok": True}
    assert submission["id"] not in mine(bob)
    assert bob.refuse("submission.acknowledge", submission=submission["id"]) == (
        f"No such submission: {submission['id']}"
    )


def test_an_author_who_was_away_learns_the_outcome_on_return(demo, alice, connect, unique):
    """The reason a rejection is kept rather than deleted when it is made."""
    bob = connect("bob")
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="while away")
    bob.close()

    alice.call("submission.reject", submission=submission["id"])

    rejection = mine(connect("bob"))[submission["id"]]
    assert rejection["state"] == "rejected"
    assert rejection["comment"] is None


# -- who decides ---------------------------------------------------------------


def test_only_a_moderator_decides_and_an_administrator_is_not_one(demo, alice, bob, unique):
    """The moderator set is what makes a channel take submissions, so it is explicit."""
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="mine")

    for socket in (demo, bob):
        assert socket.refuse("channel.queue", channel=channel_id) == MODERATORS_ONLY
        assert socket.refuse("submission.approve", submission=submission["id"]) == MODERATORS_ONLY
        assert socket.refuse("submission.reject", submission=submission["id"]) == MODERATORS_ONLY


def test_a_decision_is_final(demo, alice, bob, unique):
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="once")
    alice.call("submission.reject", submission=submission["id"])

    assert alice.refuse("submission.approve", submission=submission["id"]) == DECIDED
    assert alice.refuse("submission.reject", submission=submission["id"]) == DECIDED


def test_only_its_author_acknowledges_and_only_once_decided(demo, alice, bob, unique):
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="waiting")

    assert bob.refuse("submission.acknowledge", submission=submission["id"]) == (
        "That submission is still pending"
    )
    alice.call("submission.reject", submission=submission["id"])
    assert alice.refuse("submission.acknowledge", submission=submission["id"]) == (
        f"No such submission: {submission['id']}"
    )


def test_dismissing_the_last_moderator_rejects_the_queue(demo, bob, unique):
    channel_id = found(demo, unique, bob)
    submission = bob.call("channel.submit", channel=channel_id, body="orphaned")

    reply = demo.call("channel.dismiss", channel=channel_id, username="alice")
    assert reply["channel"]["moderators"] == []

    pushed = bob.expect_push(submission_push("rejected", submission["id"]))["submission"]
    assert pushed["comment"] == "That channel no longer accepts submissions"
    assert bob.refuse("channel.submit", channel=channel_id, body="again") == NO_SUBMISSIONS


def test_dismissing_one_of_two_moderators_keeps_the_queue(demo, bob, unique):
    channel_id = found(demo, unique, bob)
    demo.call("channel.appoint", channel=channel_id, username="demo")
    submission = bob.call("channel.submit", channel=channel_id, body="still here")

    demo.call("channel.dismiss", channel=channel_id, username="alice")
    assert queue_of(demo, channel_id) == [submission["id"]]


def test_submitting_needs_the_channel_and_something_to_say(demo, alice, bob, unique):
    channel_id = found(demo, unique)
    assert bob.refuse("channel.submit", channel=channel_id, body="hi") == (
        "Not invited to that room"
    )

    bob.call("subscribe", channel=channel_id)
    assert bob.refuse("channel.submit", channel=channel_id, body="   ") == "Empty message"

    room = bob.call("open", invite=[], title=unique("Room"))
    assert bob.refuse("channel.submit", channel=room["id"], body="hi") == (
        f"No such room: {room['id']}"
    )
    assert alice.refuse("submission.approve", submission="absent") == (
        "No such submission: absent"
    )
