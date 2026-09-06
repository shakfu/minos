"""A narrated run of the channel audience rule against the compiled server.

Launches `go/minosd` on a database of its own, connects three real websockets,
and prints what each client sees at every step. Nothing is mocked: this is the
wire `tui/` speaks. Run it with `make demo`.

The launch and the socket come from `tests/conformance`, so the demo speaks
whatever the contract currently says rather than a second copy of it that could
drift.
"""

import os
import pathlib
import sys
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

# The harness launches whatever this names, and `make demo` has just built it.
os.environ.setdefault("MINOS_CONFORMANCE_CMD", str(ROOT / "go" / "minosd"))

from tests.conformance import harness  # noqa: E402
from tests.conformance.wire import Http, Socket  # noqa: E402

SYSTEM = "system"
PASSWORDS = {"demo": "demo", "alice": "alice", "bob": "bob"}

# Long enough for a push to have been written and read back.
SETTLE = 0.5


def say(line=""):
    print(line, flush=True)


def step(number, title):
    say()
    say(f"-- {number}. {title} ".ljust(72, "-"))


def attach(server, username):
    """One logged-in client: the HTTP session and the socket over it."""
    http = Http(server.base)
    http.login(username, PASSWORDS[username])
    socket = Socket(server.base, http.cookie_header()).connect()
    socket.handshake()
    socket.call("sync")
    return http, socket


def channel_seen_by(socket):
    channels = {c["id"]: c for c in socket.call("sync")["channels"]}
    return channels.get(SYSTEM)


def report(name, socket):
    channel = channel_seen_by(socket)
    if channel is None:
        say(f"   {name:<6} does not see the channel at all")
    else:
        say(f"   {name:<6} sees it, audience {channel['audience']}")


def latest_in(socket, room):
    messages = socket.call("history", room=room, since=0)["messages"]
    return messages[-1]["body"] if messages else None


def latest(socket):
    return latest_in(socket, SYSTEM)


def run(state):
    server = harness.launch(state)
    say(f"go/minosd on {server.base}, its own database under {state}")
    try:
        demo_http, demo = attach(server, "demo")
        _, alice = attach(server, "alice")
        _, bob = attach(server, "bob")

        step(1, "every account is subscribed to `system` at start-up")
        for name, socket in (("demo", demo), ("alice", alice), ("bob", bob)):
            report(name, socket)

        step(2, "the admin makes a group and restricts the channel to it")
        group = demo.call("group.create", name="Ops", members=["alice"])
        say(f"   /group new Ops alice        ->  @Ops {group['id'][:8]} {group['members']}")
        reply = demo.call("channel.admit", channel=SYSTEM, group=group["id"])
        say("   /channel admit system Ops")
        say(f"   restrictedTo {reply['channel']['restrictedTo'][0][:8]}"
            f"   audience {reply['channel']['audience']}")

        step(3, "the people it excludes are told once, and refused after")
        bob.expect_push(lambda e: e.get("type") == "roomGone" and e.get("room") == SYSTEM)
        say("   bob's client received: roomGone system")
        report("alice", alice)
        report("bob", bob)
        say(f"   bob subscribing again  ->  {bob.refuse('subscribe', channel=SYSTEM)!r}")
        say(f"   bob reading history    ->  {bob.refuse('history', room=SYSTEM, since=0)!r}")

        step(4, "delivery follows the rule, not the subscription")
        demo_http.upload("home:/report.txt", b"x")
        say("   demo writes home:/report.txt, which the server announces on `system`")
        time.sleep(SETTLE)
        say(f"   alice  reads {latest(alice)!r}")
        say("   bob    is refused the history he could read ten seconds ago")

        step(5, "re-admission restores it, and bob does nothing")
        demo.call("group.assign", group=group["id"], username="bob")
        say("   /group add Ops bob")
        report("bob", bob)
        say(f"   bob    reads {latest(bob)!r}")
        say("   his subscription was never deleted, so there was nothing to redo")

        step(6, "a channel can be founded restricted, and written to")
        channel = demo.call("channel.create", title="Ops notices", groups=[group["id"]])
        say(f"   /channel new 'Ops notices' @Ops   ->  {channel['id'][:8]}")
        say("   announced on `system`, since nobody is subscribed to it yet")
        alice.call("subscribe", channel=channel["id"])
        demo.call("channel.publish", channel=channel["id"], body="deploy at four")
        time.sleep(SETTLE)
        say(f"   alice  subscribes and reads {latest_in(alice, channel['id'])!r}")
        # demo founded it, publishes to it, and is in no group it admits.
        say(f"   demo   subscribing        ->  "
            f"{demo.refuse('subscribe', channel=channel['id'])!r}")
        say("   founding a channel and being in its audience are different things")

        step(7, "the rule can be withdrawn: no groups is open, not closed")
        reply = demo.call("channel.revoke", channel=SYSTEM, group=group["id"])
        say("   /channel revoke system Ops")
        say(f"   restrictedTo {reply['channel']['restrictedTo']}"
            f"   audience {sorted(reply['channel']['audience'])}")
        report("bob", bob)
    finally:
        server.stop()


def main():
    with tempfile.TemporaryDirectory(prefix="minos-demo-") as state:
        run(pathlib.Path(state))


if __name__ == "__main__":
    main()
