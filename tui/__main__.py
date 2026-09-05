"""Run the terminal client: `python -m tui`.

Login happens before curses takes the screen, so a refusal is a line on the
terminal rather than something drawn inside an interface that then has nothing
to show.
"""

import argparse
import getpass
import os
import sys

from .protocol import ChatError, connect
from .transport import TransportError

DEFAULT_SERVER = os.environ.get("MINOS_SERVER", "http://127.0.0.1:8000")


def main(argv=None):
    parser = argparse.ArgumentParser(
        prog="tui", description="A terminal client for a minos server."
    )
    parser.add_argument("--server", default=DEFAULT_SERVER, help="base URL")
    parser.add_argument("--user", help="username; prompted for when absent")
    parser.add_argument(
        "--password",
        help="password; prompted for when absent. Prefer the prompt: an "
        "argument is visible in the process list.",
    )
    args = parser.parse_args(argv)

    username = args.user or input("username: ").strip()
    password = args.password or getpass.getpass("password: ")

    try:
        http, client, profile = connect(args.server, username, password)
    except (TransportError, ChatError) as error:
        print(f"Could not connect: {error}", file=sys.stderr)
        return 1

    from .app import run

    try:
        run(client, profile)
    finally:
        client.stop()
        try:
            http.logout()
        except TransportError:
            pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
