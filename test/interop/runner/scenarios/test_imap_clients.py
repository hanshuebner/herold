"""
Text-mode IMAP client interop scenarios.

Drives real terminal mail clients (mutt, s-nail) against herold's IMAP
listener and asserts the wire round-trip succeeds. The intent is
end-to-end coverage of how a non-Python client negotiates TLS,
authenticates with PLAIN over SASL-IR (RFC 4959), opens a mailbox,
fetches headers, searches by Subject, and stores flags.

The clients run in dedicated compose containers (mutt-client,
snail-client). Both have the interop CA installed in their system trust
store; both reach herold by its hostname (mail.herold.test) over the
private interop docker network.

Mutt is curses-only and refuses to start without a usable terminfo
entry, so we wrap it in script(1) inside an xterm-typed exec. The wrap
captures the curses output (with ANSI escapes) which we strip before
asserting on the visible characters. s-nail has a true non-interactive
batch mode (-#) and is driven via -Y commands.

These scenarios are parametrised over MTAs in principle, but in v2 only
herold is exercised — the third-party MTAs in the suite (docker-
mailserver, James) already have decades of coverage from their own
upstream test suites; what we want here is signal on herold's IMAP.
"""

import os
import re
import subprocess
import time

import pytest

from helpers.imap_assert import connect_imaps, search_by_subject, assert_flag_set
from helpers.logging import log
from helpers.smtp_send import build_message, send_via_relay

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

ALICE_USER = "alice@herold.test"
ALICE_PASS = "alicepw-interop"
HEROLD_HOST = "mail.herold.test"
HEROLD_IMAPS_PORT = 993
HEROLD_SMTP_PORT = 25

# CA bundle path inside the client containers (mounted from the tls-data
# volume populated by the certgen init service).
_CA_PATH = "/etc/interop/tls/ca.crt"

_MUTT_CONTAINER = "mutt-client"
_SNAIL_CONTAINER = "snail-client"

# Strip CSI / SS2 / SS3 / charset designators / single-shift escapes that
# mutt emits as part of its curses output. We do NOT collapse spaces or
# normalise whitespace — the assertions use substring containment so
# residual whitespace is harmless.
_ANSI_RE = re.compile(rb"\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][AB012]|\x1b[78=>]|\x0f|\r")


def _strip_ansi(b: bytes) -> str:
    return _ANSI_RE.sub(b"", b).decode("utf-8", errors="replace")


# ---------------------------------------------------------------------------
# docker exec helper
# ---------------------------------------------------------------------------


def _container_name(service: str) -> str | None:
    """
    Return the running container name for the given compose service, or
    None if no matching container is up.
    """
    project = os.environ.get("COMPOSE_PROJECT_NAME", "interop")
    for sep in ("-", "_"):
        name = f"{project}{sep}{service}{sep}1"
        probe = subprocess.run(
            ["docker", "inspect", "--format", "{{.State.Running}}", name],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if probe.returncode == 0 and probe.stdout.strip() == "true":
            return name
    return None


def _docker_exec(
    service: str,
    cmd: list[str],
    env: dict[str, str] | None = None,
    timeout: int = 20,
) -> subprocess.CompletedProcess:
    """
    Run cmd inside the named compose service container. env entries are
    passed via -e flags. Returns the CompletedProcess (stdout/stderr in
    bytes).
    """
    name = _container_name(service)
    if name is None:
        return subprocess.CompletedProcess(
            args=[], returncode=127, stdout=b"", stderr=b"container-not-running"
        )
    full = ["docker", "exec"]
    for k, v in (env or {}).items():
        full += ["-e", f"{k}={v}"]
    full += [name, *cmd]
    log("docker_exec", "run", f"container={name} cmd={' '.join(cmd[:2])}")
    return subprocess.run(full, capture_output=True, timeout=timeout)


# ---------------------------------------------------------------------------
# Seeding
# ---------------------------------------------------------------------------


def _seed_message(nonce: str, run_id: str) -> tuple[str, str]:
    """
    Inject a test message via SMTP into alice@herold.test.
    Returns (subject, body).
    """
    subject = f"imap-client-test-{nonce}"
    body = f"text body run={run_id} nonce={nonce}"
    msg = build_message(
        from_addr="seed@external.test",
        to_addr=ALICE_USER,
        subject=subject,
        body=body,
        message_id=f"seed-{nonce}",
    )
    log("seed", "sending", f"nonce={nonce}")
    send_via_relay(
        host=HEROLD_HOST,
        port=HEROLD_SMTP_PORT,
        from_addr="seed@external.test",
        to_addr=ALICE_USER,
        msg=msg,
        use_starttls=False,
    )
    # Give herold a moment to deliver the message into alice's mailbox.
    time.sleep(2)
    return subject, body


# ---------------------------------------------------------------------------
# mutt invocations
# ---------------------------------------------------------------------------


_MUTTRC = f"""\
set ssl_ca_certificates_file={_CA_PATH}
set ssl_verify_host=yes
set ssl_force_tls=yes
set imap_user="{ALICE_USER}"
set imap_pass="{ALICE_PASS}"
set imap_authenticators="plain"
set quit=yes
"""


def _ensure_muttrc() -> None:
    """
    Write the muttrc into the mutt-client container. Idempotent;
    rewrites every test so a stale file from a previous run cannot
    affect us.
    """
    cmd = ["sh", "-c", f"cat > /tmp/interop-muttrc <<'EOF'\n{_MUTTRC}EOF"]
    r = _docker_exec(_MUTT_CONTAINER, cmd, timeout=10)
    if r.returncode != 0:
        pytest.skip(f"mutt-client not available (rc={r.returncode}): {r.stderr!r}")


def _run_mutt(push: str, *, read_only: bool, timeout: int = 20) -> str:
    """
    Run mutt batch-mode against alice's INBOX over IMAPS, pushing the
    given keystroke macro. Returns the ANSI-stripped curses output.

    push is the mutt key-macro string (e.g. "<limit>~s NONCE<enter>q").
    read_only=True opens with -R (no flag updates). read_only=False
    opens normally so flag changes are written back.
    """
    flags = "-R" if read_only else ""
    folder = f"imaps://{HEROLD_HOST}:{HEROLD_IMAPS_PORT}/INBOX"
    # The push string is wrapped in escaped double-quotes inside a
    # shell-double-quoted -e argument, which is itself inside a sh -c
    # argument. The triple-escaping here is intentional and brittle, but
    # keeps the control surface in this one helper.
    inner = (
        f'mutt -F /tmp/interop-muttrc {flags} -f {folder} '
        f'-e "push \\"{push}\\""'
    )
    sh_cmd = f"timeout {timeout - 2} script -qc '{inner}' /tmp/mutt.log >/dev/null 2>&1; cat /tmp/mutt.log"
    r = _docker_exec(
        _MUTT_CONTAINER,
        ["sh", "-c", sh_cmd],
        env={"TERM": "xterm", "LINES": "200", "COLUMNS": "200"},
        timeout=timeout,
    )
    out = _strip_ansi(r.stdout) + _strip_ansi(r.stderr)
    log("mutt", "output", f"chars={len(out)}")
    return out


# ---------------------------------------------------------------------------
# s-nail invocations
# ---------------------------------------------------------------------------


def _run_snail(commands: list[str], *, timeout: int = 15) -> str:
    """
    Run s-nail in batch mode (-#) with the rcfile suppressed (-:/) and
    a series of -Y commands. The first command must open the IMAP
    folder. Returns combined stdout+stderr.

    s-nail is much friendlier than mutt to non-interactive use: no
    curses, no terminfo dependency, and -# is a documented batch flag.
    """
    folder = (
        f"imaps://{ALICE_USER.replace('@', '%40')}:{ALICE_PASS}"
        f"@{HEROLD_HOST}:{HEROLD_IMAPS_PORT}/INBOX"
    )
    args = [
        "s-nail", "-:/", "-#",
        "-S", "v15-compat",
        "-S", f"tls-ca-file={_CA_PATH}",
        "-Y", f"File {folder}",
    ]
    for c in commands:
        args += ["-Y", c]
    args += ["-Y", "quit"]
    r = _docker_exec(_SNAIL_CONTAINER, args, timeout=timeout)
    out = r.stdout.decode("utf-8", errors="replace")
    err = r.stderr.decode("utf-8", errors="replace")
    log("snail", "output", f"rc={r.returncode} chars={len(out) + len(err)}")
    # s-nail emits routine TLS warnings on stderr that aren't failures.
    # Concatenate so callers can grep either stream uniformly.
    return out + err


# ---------------------------------------------------------------------------
# Tests: mutt
# ---------------------------------------------------------------------------


@pytest.mark.imap_client
def test_mutt_index_shows_seeded_subject(run_id, nonce):
    """
    Open INBOX in mutt batch mode and assert the seeded Subject line
    appears in the rendered index.
    """
    _ensure_muttrc()
    subject, _ = _seed_message(nonce, run_id)
    out = _run_mutt(push="<enter>q", read_only=True)
    assert nonce in out, (
        f"expected nonce {nonce!r} in mutt index output; got: {out[:600]!r}"
    )


@pytest.mark.imap_client
def test_mutt_search_by_subject(run_id, nonce):
    """
    Use mutt's <limit> command (interactive SEARCH) with a unique
    subject and assert the visible header row contains the nonce.
    """
    _ensure_muttrc()
    subject, _ = _seed_message(nonce, run_id)
    out = _run_mutt(push=f"<limit>~s {nonce}<enter>q", read_only=True)
    assert nonce in out, (
        f"expected nonce {nonce!r} in mutt limited index; got: {out[:600]!r}"
    )
    # The status line should report a filtered count of 1.
    assert "Msgs:1/" in out, (
        f"expected mutt status to show Msgs:1/N (filtered to one match); got: {out[-300:]!r}"
    )


@pytest.mark.imap_client
def test_mutt_clears_new_flag(run_id, nonce):
    """
    Open INBOX read-write, limit to the seeded message, clear the
    \"new\" flag (which translates to \\Seen on IMAP), sync the mailbox,
    and verify via Python imaplib that \\Seen is now set.
    """
    _ensure_muttrc()
    _, _ = _seed_message(nonce, run_id)
    push = f"<limit>~s {nonce}<enter><clear-flag>N<sync-mailbox>q"
    _run_mutt(push=push, read_only=False)
    # Round-trip via Python.
    conn = connect_imaps(HEROLD_HOST, HEROLD_IMAPS_PORT, ALICE_USER, ALICE_PASS)
    try:
        uids = search_by_subject(conn, nonce)
        assert uids, f"no message with nonce {nonce!r} found"
        assert_flag_set(conn, uids[-1], r"\Seen")
    finally:
        conn.logout()


# ---------------------------------------------------------------------------
# Tests: s-nail
# ---------------------------------------------------------------------------


@pytest.mark.imap_client
def test_snail_print_matches_seeded_subject(run_id, nonce):
    """
    Use s-nail's print command with an IMAP search criterion to fetch
    the message body. Assert the nonce appears in the printed output
    (which contains both headers and body).
    """
    subject, body = _seed_message(nonce, run_id)
    out = _run_snail([f'print "(subject {nonce})"'])
    assert nonce in out, (
        f"expected nonce {nonce!r} in s-nail print output; got: {out[:600]!r}"
    )
    assert subject in out, (
        f"expected subject {subject!r} in s-nail print output; got: {out[:600]!r}"
    )


@pytest.mark.imap_client
def test_snail_seen_after_print(run_id, nonce):
    """
    s-nail's print command implicitly marks messages as seen (since the
    user has read them). Verify via Python imaplib.
    """
    _, _ = _seed_message(nonce, run_id)
    _run_snail([f'print "(subject {nonce})"'])
    conn = connect_imaps(HEROLD_HOST, HEROLD_IMAPS_PORT, ALICE_USER, ALICE_PASS)
    try:
        uids = search_by_subject(conn, nonce)
        assert uids, f"no message with nonce {nonce!r} found"
        assert_flag_set(conn, uids[-1], r"\Seen")
    finally:
        conn.logout()
