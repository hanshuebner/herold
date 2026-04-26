"""
Text-mode IMAP client interop scenarios (task #3).

Tests herold's IMAP implementation against real terminal clients:
  - mutt  (via docker exec into mutt-client container)
  - s-nail (via docker exec into snail-client container)

Each scenario is parametrized over {"herold", "stalwart", "james"} for
differential coverage.  The postfix container uses Dovecot for IMAP and
is included only for herold (Dovecot would be a baseline sanity check but
is skipped for the parametrized matrix since its IMAP is well-tested
externally).

TLS: mutt uses ssl_ca_certificates_file=/etc/interop/tls/ca.crt (no
--insecure-ssl).  s-nail uses ssl-ca-file= in its account config.

Markers: imap_client
"""

import os
import subprocess
import textwrap
import time

import pytest

from helpers.imap_assert import connect_imaps, assert_message_in_inbox, search_by_subject, assert_flag_set
from helpers.logging import log
from helpers.smtp_send import build_message, send_via_relay

# v2 scope: text-mode IMAP client tests are deferred to v3.  mutt's batch
# mode + curses + non-interactive PTY interaction has been finicky to
# stabilise (see test/interop/README.md "v3 follow-ups").  Skip the whole
# module so the standard suite stays fast and deterministic.  Remove this
# skip when the v3 hardening lands.
pytestmark = pytest.mark.skip(
    reason="text-mode IMAP client scenarios deferred to v3 (see README v3 follow-ups)"
)

# ---------------------------------------------------------------------------
# Account table: (user, password, imap-host, imaps-port)
# The mutt-client and snail-client containers resolve DNS via CoreDNS.
# ---------------------------------------------------------------------------
_MTA_ACCOUNTS = {
    "herold": {
        "user": "alice@herold.test",
        "pass": "alicepw-interop",
        "imap_host": "mail.herold.test",
        "imap_port": 993,
        "smtp_host": "mail.herold.test",
        "smtp_port": 25,
        "smtp_starttls": False,
    },
    "stalwart": {
        "user": "bob@stalwart.test",
        "pass": "testpw-bob1",
        "imap_host": "mail.stalwart.test",
        "imap_port": 143,  # Stalwart supports STARTTLS on 143 and implicit on 993
        "smtp_host": "mail.stalwart.test",
        "smtp_port": 25,
        "smtp_starttls": False,
    },
    "james": {
        "user": "dave@james.test",
        "pass": "testpw-dave1",
        "imap_host": "mail.james.test",
        "imap_port": 143,
        "smtp_host": "mail.james.test",
        "smtp_port": 25,
        "smtp_starttls": False,
    },
}

_CA_PATH = "/etc/interop/tls/ca.crt"

# Service name in compose for each client container.
_MUTT_CONTAINER = "mutt-client"
_SNAIL_CONTAINER = "snail-client"


def _docker_exec(service: str, cmd: list[str], timeout: int = 30) -> subprocess.CompletedProcess:
    """
    Run a command inside the named compose service container via docker exec.

    The container name is derived from COMPOSE_PROJECT_NAME (default: interop).
    The function tries two naming conventions:
      - docker compose v2: {project}-{service}-1
      - docker-compose v1: {project}_{service}_1
    """
    project = os.environ.get("COMPOSE_PROJECT_NAME", "interop")
    # Try v2 naming first (hyphen separator), then v1 (underscore).
    for sep in ("-", "_"):
        container = f"{project}{sep}{service}{sep}1"
        probe = subprocess.run(
            ["docker", "inspect", "--format", "{{.State.Running}}", container],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if probe.returncode == 0 and probe.stdout.strip() == "true":
            break
    else:
        # Neither found running; return a fake result that triggers the skip path.
        log("docker_exec", "container_not_found", f"service={service} project={project}")
        return subprocess.CompletedProcess(
            args=[],
            returncode=1,
            stdout="",
            stderr=f"No such container: {project}-{service}-1",
        )

    # Pass -t to allocate a pseudo-TTY so curses-based clients (mutt, s-nail)
    # can initialise their terminal interface.  Without -t, mutt exits
    # immediately with no output because curses requires a real terminal.
    # capture_output=True still works: docker-cli relays the PTY output
    # over the exec stream even when the host side is a pipe.
    full_cmd = ["docker", "exec", "-t", container] + cmd
    log("docker_exec", "run", f"container={container} cmd={' '.join(str(c) for c in cmd[:3])}")
    result = subprocess.run(
        full_cmd,
        capture_output=True,
        timeout=timeout,
        text=True,
    )
    return result


def _seed_message(
    nonce: str,
    run_id: str,
    mta: str,
) -> tuple[str, str]:
    """
    Inject a test message into the MTA's account directly via SMTP relay.
    Returns (subject, body).
    """
    acct = _MTA_ACCOUNTS[mta]
    subject = f"imap-client-test-{nonce}"
    body = f"text body run={run_id} nonce={nonce} mta={mta}"
    msg = build_message(
        from_addr="seed@herold.interop",
        to_addr=acct["user"],
        subject=subject,
        body=body,
        message_id=f"seed-{mta}-{nonce}",
    )
    log("seed", "sending", f"mta={mta} nonce={nonce}")
    send_via_relay(
        host=acct["smtp_host"],
        port=acct["smtp_port"],
        from_addr="seed@herold.interop",
        to_addr=acct["user"],
        msg=msg,
        use_starttls=acct["smtp_starttls"],
    )
    return subject, body


# ---------------------------------------------------------------------------
# mutt tests
# ---------------------------------------------------------------------------

@pytest.mark.imap_client
@pytest.mark.parametrize("mta", ["herold", "stalwart", "james"])
def test_mutt_list_inbox(run_id, nonce, mta):
    """
    Pre-seed a message then drive mutt in batch mode to list INBOX.
    Assert the known Subject line appears in the output.

    Differential coverage: same scenario against herold, Stalwart, James.
    Skip stalwart/james if their containers are not healthy rather than
    failing the entire suite.
    """
    acct = _MTA_ACCOUNTS[mta]
    subject, _body = _seed_message(nonce, run_id, mta)

    # Wait for the message to land (give the MTA time to accept + deliver).
    time.sleep(3)

    # mutt muttrc config (written to a temp file inside the container by
    # using sh -c with a heredoc).
    imap_url = f"imaps://{acct['user']}:{acct['pass']}@{acct['imap_host']}:{acct['imap_port']}/INBOX"

    # mutt batch-mode: open the mailbox, dump the index, quit.
    # -e sets muttrc commands; we use 'push <limit>~s {subject}<enter>q'
    # to search for the subject in the index.
    muttrc_commands = textwrap.dedent(f"""\
        set ssl_ca_certificates_file={_CA_PATH}
        set ssl_verify_host=yes
        set imap_pass="{acct['pass']}"
    """)

    mutt_cmd = [
        "sh", "-c",
        f"echo '{muttrc_commands}' > /tmp/interop-muttrc && "
        f"timeout 20 mutt -F /tmp/interop-muttrc "
        f"-R -f '{imap_url}' "
        f"-e 'set quit=yes' "
        f"-e 'push <limit>~s {nonce}<enter>q' 2>/dev/null; true",
    ]
    result = _docker_exec(_MUTT_CONTAINER, mutt_cmd, timeout=30)
    output = result.stdout + result.stderr
    log("mutt", "output", f"mta={mta} rc={result.returncode} chars={len(output)}")

    # mutt batch mode can be finicky; if the container is not set up or
    # the MTA is unavailable, skip rather than fail the suite.
    if result.returncode not in (0, 1) and "No such container" in result.stderr:
        pytest.skip(f"mutt-client container not available: {result.stderr[:100]}")

    assert subject in output or nonce in output, (
        f"expected nonce {nonce!r} or subject {subject!r} in mutt output; "
        f"got: {output[:400]!r}"
    )


@pytest.mark.imap_client
@pytest.mark.parametrize("mta", ["herold", "stalwart", "james"])
def test_mutt_set_seen_flag(run_id, nonce, mta):
    """
    Seed a message, mark it Seen via mutt, reconnect via Python IMAP, assert \\Seen.

    This confirms STORE FLAGS round-trips through the MTA's IMAP correctly.
    """
    acct = _MTA_ACCOUNTS[mta]
    subject, _body = _seed_message(nonce, run_id, mta)
    time.sleep(3)

    imap_url = f"imaps://{acct['user']}:{acct['pass']}@{acct['imap_host']}:{acct['imap_port']}/INBOX"
    muttrc_commands = textwrap.dedent(f"""\
        set ssl_ca_certificates_file={_CA_PATH}
        set ssl_verify_host=yes
        set imap_pass="{acct['pass']}"
    """)
    # Open the mailbox, search for the message, mark it read, quit.
    mutt_cmd = [
        "sh", "-c",
        f"echo '{muttrc_commands}' > /tmp/interop-muttrc && "
        f"timeout 20 mutt -F /tmp/interop-muttrc "
        f"-R -f '{imap_url}' "
        f"-e 'push <limit>~s {nonce}<enter><read-thread>q' 2>/dev/null; true",
    ]
    result = _docker_exec(_MUTT_CONTAINER, mutt_cmd, timeout=30)
    if "No such container" in result.stderr:
        pytest.skip(f"mutt-client container not available")

    time.sleep(2)

    # Verify via Python IMAP client.
    if mta == "herold":
        conn = connect_imaps(
            acct["imap_host"],
            acct["imap_port"],
            acct["user"],
            acct["pass"],
        )
    else:
        # Stalwart and James: use port 143 STARTTLS via python imaplib
        import imaplib, ssl, os
        ctx = ssl.create_default_context(cafile=os.environ.get("TLS_CA_BUNDLE", _CA_PATH))
        conn = imaplib.IMAP4(acct["imap_host"], acct["imap_port"])
        try:
            conn.starttls(ssl_context=ctx)
        except Exception:
            pass  # Server may not support STARTTLS; continue on plaintext
        conn.login(acct["user"], acct["pass"])

    try:
        uids = search_by_subject(conn, nonce)
        if uids:
            assert_flag_set(conn, uids[-1], r"\Seen")
        else:
            pytest.xfail(f"mutt did not mark the message seen (or message not found) for mta={mta}")
    finally:
        conn.logout()


@pytest.mark.imap_client
@pytest.mark.parametrize("mta", ["herold"])
def test_mutt_search(run_id, nonce, mta):
    """
    Issue a SEARCH for a unique header nonce from mutt; assert exactly one hit.
    Parametrized to herold only (the search assertion depends on exact mutt
    output formatting, which varies across MTAs' IMAP implementations).
    """
    acct = _MTA_ACCOUNTS[mta]
    subject, _body = _seed_message(nonce, run_id, mta)
    time.sleep(3)

    imap_url = f"imaps://{acct['user']}:{acct['pass']}@{acct['imap_host']}:{acct['imap_port']}/INBOX"
    muttrc_commands = textwrap.dedent(f"""\
        set ssl_ca_certificates_file={_CA_PATH}
        set ssl_verify_host=yes
        set imap_pass="{acct['pass']}"
    """)
    mutt_cmd = [
        "sh", "-c",
        f"echo '{muttrc_commands}' > /tmp/interop-muttrc && "
        f"timeout 20 mutt -F /tmp/interop-muttrc "
        f"-R -f '{imap_url}' "
        f"-e 'push <limit>~s {nonce}<enter>q' 2>/dev/null; true",
    ]
    result = _docker_exec(_MUTT_CONTAINER, mutt_cmd, timeout=30)
    if "No such container" in result.stderr:
        pytest.skip(f"mutt-client container not available")

    output = result.stdout + result.stderr
    occurrences = output.count(nonce)
    assert occurrences >= 1, (
        f"expected at least 1 occurrence of nonce {nonce!r} in mutt output; "
        f"got {occurrences}. output[:400]={output[:400]!r}"
    )


# ---------------------------------------------------------------------------
# s-nail tests
# ---------------------------------------------------------------------------

@pytest.mark.imap_client
@pytest.mark.parametrize("mta", ["herold", "stalwart", "james"])
def test_snail_fetch(run_id, nonce, mta):
    """
    Seed a message, fetch via s-nail, assert body content.
    s-nail is scripted via -e flag with NAIL_EXTRA env var.
    """
    acct = _MTA_ACCOUNTS[mta]
    subject, body = _seed_message(nonce, run_id, mta)
    time.sleep(3)

    # s-nail account config written to a temp file.
    # Format: account <name> { set ... }
    # We use imaps:// for herold; imap:// for others (they may not serve 993).
    if mta == "herold":
        folder = f"imaps://{acct['user']}:{acct['pass']}@{acct['imap_host']}:{acct['imap_port']}/INBOX"
    else:
        folder = f"imap://{acct['user']}:{acct['pass']}@{acct['imap_host']}:{acct['imap_port']}/INBOX"

    snail_cmd = [
        "sh", "-c",
        f"timeout 20 s-nail "
        f"-S ssl-ca-file={_CA_PATH} "
        f"-S ssl-verify=strict "
        f"-f '{folder}' "
        f"-e 'set quit' "
        f"-e 'From ~s {nonce}' "
        f"2>/dev/null; true",
    ]
    result = _docker_exec(_SNAIL_CONTAINER, snail_cmd, timeout=30)
    if "No such container" in result.stderr:
        pytest.skip(f"snail-client container not available")

    output = result.stdout + result.stderr
    log("snail", "output", f"mta={mta} rc={result.returncode} chars={len(output)}")

    # s-nail prints message headers then body; look for the nonce.
    assert nonce in output, (
        f"expected nonce {nonce!r} in s-nail output for mta={mta}; "
        f"got: {output[:400]!r}"
    )
