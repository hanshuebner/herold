"""
Structured test-runner logging.

All log lines are plain ASCII key=value pairs so they are trivially grep-able
in CI and in the archived compose.log.  No emojis; no color codes.
"""

import datetime
import sys


def log(context: str, event: str, detail: str = "") -> None:
    """
    Emit a single structured log line to stderr.

    Format: timestamp context=<ctx> event=<event> [<detail>]

    Example:
        log("smtp_send", "connected", "host=postfix port=25")
    """
    ts = datetime.datetime.now(datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    line = f"{ts} context={context} event={event}"
    if detail:
        line = f"{line} {detail}"
    print(line, file=sys.stderr, flush=True)
