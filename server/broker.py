"""Run the message bus proxy on its own: `python -m server.broker`.

The proxy normally lives inside whichever worker starts first, so `make serve`
stays one command. Running it here instead keeps it out of the workers, which is
what you want if they are restarted often -- the survivor otherwise inherits the
proxy at an arbitrary moment.

Configuration is this server's business, so the endpoints come from
`server.config`; `messaging.run_broker` supplies only the mechanism.
"""

import messaging

from . import config


def main():
    messaging.run_broker(config.BUS_XSUB, config.BUS_XPUB, config.RUN_DIR)


if __name__ == "__main__":
    main()
