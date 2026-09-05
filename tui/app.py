"""The terminal interface.

The model this draws has no windows in it, which is the point: a room is a
place, and a place is a thing you are *in*. So the interface makes that literal
-- the room you have selected is the room you occupy, and switching away leaves
it. For a transient room that is not a metaphor, it is the thing that keeps the
room alive: the grace period starts when its last occupant leaves, and closing
this program is leaving.

Everything the retired design expressed by dragging is a command here, and the
trade is a good one. Membership was edited by dropping one window on another,
which no keyboard could reach and no script could call; `/invite` says what it
does, can be refused with a reason, and reads the same in a log.

Three regions and a line. The sidebar lists what you can enter, the pane shows
one conversation, the composer takes text or a command, and the status line says
whether the socket is up -- because in a terminal there is nowhere else to
notice that it is not.
"""

import curses
import textwrap
import time

from .protocol import ChatError

SIDEBAR = 24
AUTHOR = 10

# Lines of command output kept under the conversation.
NOTICES = 6

HELP = [
    ("/rooms  /people  /groups", "list what there is"),
    ("/open <who>...", "raise an ad-hoc room, kept"),
    ("/meet <who>...", "raise an ad-hoc room, discarded when everyone leaves"),
    ("/create <title>", "found a permanent room (admin)"),
    ("/invite <who>", "admit a user, or @group"),
    ("/uninvite <who>", "withdraw a grant"),
    ("/leave", "give up your place in this room"),
    ("/group new <name> [user]...", "create a group (admin)"),
    ("/group add|rm <group> <user>", "assign or unassign (admin)"),
    ("/subscribe <id>  /unsubscribe", "a channel's audience is your own choice"),
    ("/quit", "leave every room and stop"),
    ("Tab / S-Tab", "next or previous space"),
    ("PgUp / PgDn", "scroll this room"),
]


def run(client, profile):
    """Hand the terminal to curses and run until the user stops."""
    curses.wrapper(lambda screen: Ui(screen, client, profile).loop())


class Ui:
    def __init__(self, screen, client, profile):
        self.screen = screen
        self.client = client
        self.profile = profile
        self.input = ""
        self.notices = []
        self.scroll = 0
        self.selected = None
        self.occupancy = None
        self.dirty = True
        self.running = True

        client.on_change = self._changed
        client.on_notice = self.notice

        curses.curs_set(1)
        screen.nodelay(True)
        screen.timeout(120)
        self._init_colours()

    def _init_colours(self):
        self.colour = {}
        if not curses.has_colors():
            return
        curses.start_color()
        curses.use_default_colors()
        for index, (name, fg) in enumerate(
            (
                ("dim", curses.COLOR_BLUE),
                ("me", curses.COLOR_CYAN),
                ("event", curses.COLOR_YELLOW),
                ("bad", curses.COLOR_RED),
                ("good", curses.COLOR_GREEN),
            ),
            start=1,
        ):
            curses.init_pair(index, fg, -1)
            self.colour[name] = curses.color_pair(index)

    def _changed(self):
        self.dirty = True

    def notice(self, text):
        self.notices.append((time.time(), str(text)))
        del self.notices[:-200]
        self.dirty = True

    # -- the spaces the sidebar lists -----------------------------------------

    def spaces(self):
        """Rooms then channels, each most recently active first.

        One flat order, because Tab has to walk something and a terminal has no
        second axis to arrange them on.
        """
        # Title, then age. Ad-hoc titles are descriptions rather than names and
        # several rooms may share one, so the second key is what keeps their
        # order from shuffling between draws -- and it matches the timestamp
        # shown beside them when they collide.
        def order(space):
            return (space["title"].lower(), space.get("createdAt", 0))

        return (
            sorted(self.client.rooms.values(), key=order)
            + sorted(self.client.channels.values(), key=order)
        )

    def ensure_selection(self):
        spaces = self.spaces()
        ids = [space["id"] for space in spaces]
        if self.selected not in ids:
            self.select(ids[0] if ids else None)

    def select(self, space_id):
        """Move between spaces, taking and giving up occupancy as we go.

        Occupancy is what a transient room's life is measured by, so it has to
        follow the selection rather than the login: a user with a room open is
        in it, and a user who moved away is not.
        """
        if space_id == self.selected:
            return
        self._release()

        self.selected = space_id
        self.scroll = 0
        space = self.client.space(space_id) if space_id else None
        if space is not None and space["kind"] == "room":
            try:
                self.occupancy = self.client.enter(space_id)
            except ChatError as error:
                self.notice(error)
        self.dirty = True

    def _release(self):
        if self.occupancy is not None:
            try:
                self.client.exit(self.occupancy)
            except ChatError:
                pass
            self.occupancy = None

    def cycle(self, step):
        spaces = [space["id"] for space in self.spaces()]
        if not spaces:
            return
        try:
            index = spaces.index(self.selected)
        except ValueError:
            index = 0
        self.select(spaces[(index + step) % len(spaces)])

    # -- the loop -------------------------------------------------------------

    def loop(self):
        while self.running:
            self.ensure_selection()
            if self.dirty:
                self.draw()
                self.dirty = False
            self.read_key()
        self._release()

    def read_key(self):
        try:
            key = self.screen.get_wch()
        except curses.error:
            return  # timeout: nothing typed, which is most of the time
        except KeyboardInterrupt:
            self.running = False
            return

        if key == curses.KEY_RESIZE:
            self.dirty = True
        elif key in ("\n", "\r", curses.KEY_ENTER):
            self.submit()
        elif key in ("\x7f", "\b", curses.KEY_BACKSPACE):
            self.input = self.input[:-1]
            self.dirty = True
        elif key == "\t":
            self.cycle(1)
        elif key == curses.KEY_BTAB:
            self.cycle(-1)
        elif key == "\x0e":  # ^N
            self.cycle(1)
        elif key == "\x10":  # ^P
            self.cycle(-1)
        elif key == curses.KEY_PPAGE:
            self.scroll += 5
            self.dirty = True
        elif key == curses.KEY_NPAGE:
            self.scroll = max(0, self.scroll - 5)
            self.dirty = True
        elif key == "\x15":  # ^U
            self.input = ""
            self.dirty = True
        elif key == "\x04" and not self.input:  # ^D on an empty line
            self.running = False
        elif key == "\x03":  # ^C
            self.running = False
        elif isinstance(key, str) and key.isprintable():
            self.input += key
            self.dirty = True

    def submit(self):
        text = self.input.strip()
        self.input = ""
        self.dirty = True
        if not text:
            return

        if text.startswith("/"):
            self.command(text)
            return

        if self.selected is None:
            self.notice("Nowhere to send that; open a room first")
            return
        try:
            self.client.send(self.selected, text)
        except ChatError as error:
            self.notice(error)

    # -- commands -------------------------------------------------------------

    def command(self, text):
        parts = text[1:].split()
        if not parts:
            return
        name, args = parts[0].lower(), parts[1:]
        handler = getattr(self, f"cmd_{name}", None)
        if handler is None:
            self.notice(f"No such command: /{name} -- try /help")
            return
        try:
            handler(args)
        except ChatError as error:
            self.notice(error)

    def principal(self, token):
        """A user, or a group when it is written `@name`.

        The prefix is the whole syntax of inviting a group, which is the one
        thing the retired interface could not express at all.
        """
        if token.startswith("@"):
            wanted = token[1:]
            for group in self.client.groups:
                if group["name"].lower() == wanted.lower() or group["id"] == wanted:
                    return {"kind": "group", "id": group["id"]}
            raise ChatError(f"No such group: {wanted}")
        return {"kind": "user", "id": token}

    def cmd_help(self, args):
        for form, what in HELP:
            self.notice(f"{form:<30} {what}")

    def cmd_quit(self, args):
        self.running = False

    def cmd_rooms(self, args):
        for space in self.spaces():
            shape = space["kind"] if space["kind"] == "channel" else space["retention"]
            self.notice(
                f"{space['title']} -- {shape}, {len(space['audience'])} invited"
                f" [{space['id'][:8]}]"
            )

    def cmd_people(self, args):
        for user in self.client.users:
            self.notice(f"{'online ' if user['online'] else 'offline'} {user['username']}")

    def cmd_groups(self, args):
        if not self.client.groups:
            self.notice("No groups yet")
        for group in self.client.groups:
            self.notice(f"@{group['name']} -- {', '.join(group['members']) or 'nobody'}")

    def cmd_open(self, args):
        room = self.client.open_room([self.principal(a) for a in args])
        self.select(room["id"])

    def cmd_meet(self, args):
        room = self.client.open_room(
            [self.principal(a) for a in args], retention="transient"
        )
        self.notice(f"{room['title']} is transient: it is discarded when everyone leaves")
        self.select(room["id"])

    def cmd_create(self, args):
        if not args:
            raise ChatError("A permanent room needs a name: /create <title>")
        room = self.client.create_room(" ".join(args))
        self.select(room["id"])

    def cmd_invite(self, args):
        if not self.selected or not args:
            raise ChatError("Usage: /invite <user|@group>")
        for token in args:
            room = self.client.invite(self.selected, self.principal(token))
        self.notice(f"{room['title']}: {', '.join(room['audience'])}")

    def cmd_uninvite(self, args):
        if not self.selected or not args:
            raise ChatError("Usage: /uninvite <user|@group>")
        room = self.client.uninvite(self.selected, self.principal(args[0]))
        self.notice(f"{room['title']}: {', '.join(room['audience'])}")

    def cmd_leave(self, args):
        if not self.selected:
            return
        leaving = self.selected
        self._release()
        self.client.leave(leaving)
        self.selected = None

    def cmd_group(self, args):
        if not args:
            raise ChatError("Usage: /group new|add|rm ...")
        action = args[0].lower()
        if action == "new" and len(args) >= 2:
            group = self.client.create_group(args[1], args[2:])
            self.notice(f"Created @{group['name']}")
        elif action == "add" and len(args) == 3:
            self.client.assign_group(self.principal("@" + args[1])["id"], args[2])
            self.notice(f"{args[2]} joined @{args[1]}, and every room it was invited to")
        elif action == "rm" and len(args) == 3:
            self.client.unassign_group(self.principal("@" + args[1])["id"], args[2])
            self.notice(f"{args[2]} left @{args[1]}, and every room it carried")
        else:
            raise ChatError("Usage: /group new <name> [user]... | add|rm <group> <user>")

    def cmd_subscribe(self, args):
        if not args:
            raise ChatError("Usage: /subscribe <channel-id>")
        channel = self.client.subscribe(args[0])
        self.select(channel["id"])

    def cmd_unsubscribe(self, args):
        if not self.selected:
            return
        leaving = self.selected
        self._release()
        self.client.unsubscribe(leaving)
        self.selected = None

    # -- drawing --------------------------------------------------------------

    def draw(self):
        self.screen.erase()
        height, width = self.screen.getmaxyx()
        if height < 6 or width < 40:
            self._put(0, 0, "Terminal too small", width)
            self.screen.refresh()
            return

        self.draw_status(width)
        self.draw_sidebar(height, width)
        self.draw_pane(height, width)
        self.draw_composer(height, width)
        self.screen.refresh()

    def _put(self, y, x, text, width, attr=0, pad=False):
        """Draw text into a field, optionally clearing the rest of it.

        `pad` matters more than it looks: a shorter string drawn over a longer
        one leaves the tail behind unless the field is filled, and the sidebar
        redraws rows whose contents change length every time a room is added.
        """
        if y < 0 or x < 0 or width <= 0:
            return
        text = str(text)
        if pad:
            text = text[:width].ljust(width)
        try:
            self.screen.addnstr(y, x, text, max(0, width), attr)
        except curses.error:
            # The bottom-right cell cannot be written without scrolling.
            pass

    def draw_status(self, width):
        who = self.profile.get("username", "?")
        if self.client.is_admin:
            who += " (admin)"
        left = f" minos  {who}"
        state = "connected" if self.client.socket.connected else "disconnected"
        attr = self.colour.get("good" if self.client.socket.connected else "bad", 0)

        self._put(0, 0, left.ljust(width), width, curses.A_REVERSE)
        self._put(0, max(0, width - len(state) - 2), state, len(state) + 1,
                  curses.A_REVERSE | attr)

    def draw_sidebar(self, height, width):
        bottom = height - 3
        row = 1
        ambiguous = self._ambiguous()
        rooms = [s for s in self.spaces() if s["kind"] == "room"]
        channels = [s for s in self.spaces() if s["kind"] == "channel"]

        for heading, spaces in (("ROOMS", rooms), ("CHANNELS", channels)):
            if row >= bottom:
                break
            self._put(row, 1, heading, SIDEBAR - 2, curses.A_BOLD | self.colour.get("dim", 0), pad=True)
            row += 1
            if not spaces:
                self._put(row, 2, "(none)", SIDEBAR - 3, self.colour.get("dim", 0), pad=True)
                row += 1
            for space in spaces:
                if row >= bottom:
                    break
                row = self._draw_space(row, space, ambiguous)
            row += 1

        if row < bottom:
            self._put(row, 1, "PEOPLE", SIDEBAR - 2,
                      curses.A_BOLD | self.colour.get("dim", 0), pad=True)
            row += 1
            for user in self.client.users:
                if row >= bottom or user["username"] == self.client.me:
                    continue
                mark = "*" if user["online"] else " "
                attr = self.colour.get("good", 0) if user["online"] else self.colour.get("dim", 0)
                self._put(row, 2, f"{mark} {user['username']}", SIDEBAR - 3, attr, pad=True)
                row += 1

        for line in range(row, bottom):
            self._put(line, 1, "", SIDEBAR - 2, pad=True)
        for line in range(1, height - 3):
            self._put(line, SIDEBAR, "|", 1, self.colour.get("dim", 0))

    def _ambiguous(self):
        """Titles held by more than one space.

        Only ad-hoc rooms can collide: a permanent room's title is a name and
        the server keeps those unique, because "post it in Engineering" has to
        resolve to one room. An ad-hoc room's title only describes who is in it,
        so two conversations with the same people share one -- and the way to
        tell those apart is when they started, which is how anybody would say
        it out loud.
        """
        seen, twice = set(), set()
        for space in self.spaces():
            title = space["title"]
            if title in seen:
                twice.add(title)
            seen.add(title)
        return twice

    def _draw_space(self, row, space, ambiguous=()):
        selected = space["id"] == self.selected
        unread = self.client.unread(space["id"])
        marker = ">" if selected else " "
        label = space["title"]
        if space["title"] in ambiguous:
            label += time.strftime(" %H:%M:%S", time.localtime(space.get("createdAt", 0)))
        if space["kind"] == "room" and space["retention"] == "transient":
            label += " ~"

        attr = curses.A_BOLD if selected else 0
        if unread and not selected:
            attr |= self.colour.get("me", 0)

        text = f"{marker} {label}"
        if unread and not selected:
            text = f"{text} ({unread})"
        self._put(row, 1, text, SIDEBAR - 2, attr, pad=True)
        return row + 1

    def draw_pane(self, height, width):
        left = SIDEBAR + 2
        pane = width - left
        bottom = height - 3
        space = self.client.space(self.selected) if self.selected else None

        if space is None:
            self._put(1, left, "No room selected. /open <user> to raise one,", pane)
            self._put(2, left, "or /help for what else there is.", pane)
            return

        header = space["title"]
        if space["kind"] == "channel":
            header += "  (channel: read-only)"
        elif space["retention"] == "transient":
            header += "  (transient: discarded when everyone leaves)"
        self._put(1, left, header, pane, curses.A_BOLD, pad=True)
        occupants = space.get("occupants") or []
        self._put(
            2, left,
            f"{len(space['audience'])} invited"
            + (f", here now: {', '.join(occupants)}" if occupants else ""),
            pane, self.colour.get("dim", 0), pad=True,
        )

        lines = self._pane_lines(space, pane)
        visible = bottom - 4
        self.scroll = max(0, min(self.scroll, max(0, len(lines) - visible)))
        end = len(lines) - self.scroll
        window = lines[max(0, end - visible) : end]

        for offset, (text, attr) in enumerate(window):
            self._put(4 + offset, left, text, pane, attr, pad=True)

        if self.scroll == 0:
            self._mark_read(space)

    def _pane_lines(self, space, pane):
        """Every message in this space, wrapped, plus the notice log."""
        lines = []
        for message in self.client.log.get(space["id"], []):
            stamp = time.strftime("%H:%M", time.localtime(message.get("at", 0)))
            author = message.get("author", "?")
            attr = 0
            if message.get("kind") == "event":
                attr = self.colour.get("event", 0)
            elif author == self.client.me:
                attr = self.colour.get("me", 0)

            body = message.get("body", "")
            prefix = f"{stamp} {author[:AUTHOR]:<{AUTHOR}} "
            wrapped = textwrap.wrap(body, max(10, pane - len(prefix))) or [""]
            lines.append((prefix + wrapped[0], attr))
            for extra in wrapped[1:]:
                lines.append((" " * len(prefix) + extra, attr))

        # Command output belongs to the session rather than to this room, and
        # it would otherwise read as something somebody said here. Kept to the
        # last few, below a rule, and never confusable with a message.
        recent = self.notices[-NOTICES:]
        if recent:
            lines.append(("-" * max(4, min(pane, 40)), self.colour.get("dim", 0)))
            for _, text in recent:
                for wrapped in textwrap.wrap(text, max(10, pane - 2)) or [""]:
                    lines.append(("  " + wrapped, self.colour.get("dim", 0)))
        return lines

    def _mark_read(self, space):
        """Seen, as opposed to received.

        The delivery cursor moved when the message arrived; this one moves when
        a person is looking at the room with the newest message on screen. They
        are different facts, and the server keeps this one because it is the
        same fact from every device.
        """
        if space["lastSeq"] > self.client.read.get(space["id"], 0):
            try:
                self.client.mark_read(space["id"], space["lastSeq"])
            except ChatError:
                pass

    def draw_composer(self, height, width):
        space = self.client.space(self.selected) if self.selected else None
        read_only = space is not None and space["kind"] == "channel"

        self._put(height - 3, 0, "-" * width, width, self.colour.get("dim", 0))
        prompt = "  (channel) " if read_only else "> "
        text = prompt + self.input
        self._put(height - 2, 0, text, width - 1, pad=True)

        hint = " /help  Tab: next  PgUp/PgDn: scroll  ^C: quit"
        self._put(height - 1, 0, hint.ljust(width), width,
                  curses.A_REVERSE | self.colour.get("dim", 0))

        try:
            self.screen.move(height - 2, min(len(text), width - 1))
        except curses.error:
            pass
