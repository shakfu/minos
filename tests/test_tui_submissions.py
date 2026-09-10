"""The terminal client's moderation commands, against the compiled server.

Section 5 exists in `go/` alone -- `server/` is frozen at the core -- so these
launch `go/minosd` rather than the Flask app `test_tui.py` drives. The launch is
the conformance harness's, as `make demo`'s is.
"""

import subprocess
import uuid

import pytest

from test_tui import FakeUi, wait_for
from tests.conformance import harness
from tui.protocol import connect


@pytest.fixture(scope="module")
def go_server(tmp_path_factory):
    # make decides whether the binary is stale, so this always tests the source.
    subprocess.run(["make", "-s", "go"], cwd=harness.ROOT, check=True)
    server = harness.launch(
        tmp_path_factory.mktemp("tui-go"), argv=[str(harness.ROOT / "go" / "minosd")]
    )
    yield server.base
    server.stop()


@pytest.fixture
def login(go_server):
    opened = []

    def open_client(username):
        _, client, _ = connect(go_server, username, username)
        opened.append(client)
        return client

    yield open_client
    for client in opened:
        client.stop()


def found(demo, *subscribers):
    channel = demo.create_channel(f"Curated {uuid.uuid4().hex[:8]}")
    for client in subscribers:
        client.subscribe(channel["id"])
    return channel["id"]


def moderated(demo, *subscribers):
    """A channel alice moderates, once every subscriber's client knows it."""
    channel_id = found(demo, *subscribers)
    demo.appoint(channel_id, "alice")
    for client in subscribers:
        assert wait_for(lambda: (client.space(channel_id) or {}).get("moderators") == ["alice"])
    return channel_id


def test_an_appointed_moderator_publishes_from_the_composer(login):
    demo, alice, bob = login("demo"), login("alice"), login("bob")
    channel_id = found(demo, alice, bob)

    FakeUi(demo).command(f"/channel appoint {channel_id} alice")
    assert wait_for(lambda: alice.moderates(channel_id))

    ui = FakeUi(alice)
    ui.select(channel_id)
    assert ui.submitting(channel_id) is False
    ui.input = "from the desk"
    ui.submit()

    assert wait_for(lambda: bob.log.get(channel_id))
    assert bob.log[channel_id][-1]["author"] == "alice"


def test_a_subscriber_s_composer_submits_and_a_moderator_approves(login):
    demo, alice, bob = login("demo"), login("alice"), login("bob")
    channel_id = moderated(demo, alice, bob)

    writer = FakeUi(bob)
    writer.select(channel_id)
    assert writer.submitting(channel_id) is True
    writer.input = "a tip"
    writer.submit()
    assert any("Submitted" in notice for notice in writer.notices)

    moderator = FakeUi(alice)
    moderator.select(channel_id)
    moderator.command("/queue")
    listed = next(notice for notice in moderator.notices if "a tip" in notice)
    moderator.command(f"/approve {listed[1:9]}")

    assert wait_for(lambda: bob.log.get(channel_id))
    assert (bob.log[channel_id][-1]["author"], bob.log[channel_id][-1]["body"]) == ("bob", "a tip")
    assert wait_for(lambda: not bob.submissions)


def test_a_rejection_reaches_its_author_and_ack_closes_it(login):
    demo, alice, bob = login("demo"), login("alice"), login("bob")
    channel_id = moderated(demo, alice, bob)
    told = []
    bob.on_notice = told.append

    submission = bob.submit(channel_id, "nearly")
    assert wait_for(lambda: submission["id"] in alice.queued)
    FakeUi(alice).command(f"/reject {submission['id'][:8]} too long")

    assert wait_for(lambda: bob.submissions[submission["id"]]["state"] == "rejected")
    assert bob.submissions[submission["id"]]["comment"] == "too long"
    assert any("too long" in notice for notice in told)

    writer = FakeUi(bob)
    writer.command("/submissions")
    assert any("rejected" in notice and "nearly" in notice for notice in writer.notices)
    writer.command(f"/ack {submission['id'][:8]}")
    assert submission["id"] not in bob.submissions
    assert bob.request("sync")["submissions"] == []


def test_a_returning_author_finds_the_rejection_waiting(login):
    demo, alice, bob = login("demo"), login("alice"), login("bob")
    channel_id = moderated(demo, alice, bob)
    submission = bob.submit(channel_id, "while away")
    bob.stop()

    alice.reject(submission["id"])
    returned = login("bob")
    assert returned.submissions[submission["id"]]["state"] == "rejected"


def test_dismissing_the_last_moderator_is_said_and_rejects_the_queue(login):
    demo, alice, bob = login("demo"), login("alice"), login("bob")
    channel_id = moderated(demo, alice, bob)
    submission = bob.submit(channel_id, "orphaned")

    ui = FakeUi(demo)
    ui.command(f"/channel dismiss {channel_id} alice")
    assert any("takes no submissions" in notice for notice in ui.notices)
    assert wait_for(lambda: bob.submissions[submission["id"]]["state"] == "rejected")
    assert wait_for(lambda: FakeUi(bob).submitting(channel_id) is False)


def test_an_unmoderated_channel_publishes_and_is_refused_as_before(login):
    """No moderators is the core's channel, and the composer treats it so."""
    demo, bob = login("demo"), login("bob")
    channel_id = found(demo, bob)

    ui = FakeUi(bob)
    ui.select(channel_id)
    ui.input = "hello"
    ui.submit()
    assert any("Only an administrator may do that" in notice for notice in ui.notices)
