"""
IMAP assertion helpers for the interop test suite.

All connections use the CA root from TLS_CA_BUNDLE env var for cert
verification.  No CERT_NONE; no check_hostname=False.
"""

import imaplib
import os
import ssl
import time

from helpers.logging import log

_CA_BUNDLE = os.environ.get("TLS_CA_BUNDLE", "/etc/interop/tls/ca.crt")


def _ssl_ctx() -> ssl.SSLContext:
    """Return a default SSL context trusting the interop CA."""
    ctx = ssl.create_default_context(cafile=_CA_BUNDLE)
    return ctx


_IMAP_CONNECT_TIMEOUT = 30  # seconds


def connect_imaps(host: str, port: int, username: str, password: str) -> imaplib.IMAP4_SSL:
    """Open an IMAPS (implicit TLS) connection and return the authenticated client."""
    log("imap", "connecting", f"host={host} port={port} user={username} tls=implicit")
    conn = imaplib.IMAP4_SSL(host, port, ssl_context=_ssl_ctx(), timeout=_IMAP_CONNECT_TIMEOUT)
    conn.login(username, password)
    log("imap", "authenticated", f"host={host} user={username}")
    return conn


def connect_starttls(host: str, port: int, username: str, password: str) -> imaplib.IMAP4:
    """Open an IMAP + STARTTLS connection and return the authenticated client."""
    log("imap", "connecting", f"host={host} port={port} user={username} tls=starttls")
    conn = imaplib.IMAP4(host, port, timeout=_IMAP_CONNECT_TIMEOUT)
    conn.starttls(ssl_context=_ssl_ctx())
    conn.login(username, password)
    log("imap", "authenticated", f"host={host} user={username}")
    return conn


def connect_plain(host: str, port: int, username: str, password: str) -> imaplib.IMAP4:
    """
    Open a plaintext IMAP connection and return the authenticated client.

    Used only for third-party MTAs that serve self-signed certs we have
    not (yet) wired into the interop CA chain (e.g., Apache James in v2:
    its JKS keystore is the default self-signed and replacing it requires
    keytool against the running JVM, deferred to v3).  Acceptable inside
    the private interop docker network; never use in production.
    """
    log("imap", "connecting", f"host={host} port={port} user={username} tls=none")
    conn = imaplib.IMAP4(host, port, timeout=_IMAP_CONNECT_TIMEOUT)
    conn.login(username, password)
    log("imap", "authenticated", f"host={host} user={username}")
    return conn


def assert_message_in_inbox(
    conn: imaplib.IMAP4 | imaplib.IMAP4_SSL,
    subject_substring: str,
    timeout: float = 30.0,
    poll_interval: float = 1.0,
) -> bytes:
    """
    Poll IMAP INBOX until a message whose Subject contains subject_substring
    appears or timeout elapses.  Returns the raw RFC 5322 bytes of the first
    matching message.  Raises AssertionError on timeout.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        # Re-SELECT on each iteration so the server flushes its EXISTS
        # count and we see messages that arrived after the first SELECT.
        # imaplib does not process unsolicited EXISTS notifications in
        # the polling loop, so a single SELECT-then-loop misses new mail.
        # Tolerate SELECT returning NO: a brand-new account may not have
        # the INBOX folder created until first delivery (lazy mailbox
        # creation).  Retry on the next poll.
        try:
            typ, _ = conn.select("INBOX")
        except imaplib.IMAP4.error:
            time.sleep(poll_interval)
            continue
        if typ != "OK":
            time.sleep(poll_interval)
            continue
        typ, data = conn.search(None, f'SUBJECT "{subject_substring}"')
        if typ != "OK":
            time.sleep(poll_interval)
            continue
        msg_ids = data[0].split()
        if msg_ids:
            uid = msg_ids[-1]
            typ, fetch_data = conn.fetch(uid, "(RFC822)")
            assert typ == "OK", f"IMAP FETCH failed: {typ}"
            raw = fetch_data[0][1]
            log("imap", "found_message", f"subject_substring={subject_substring!r} uid={uid!r}")
            return raw
        time.sleep(poll_interval)
    raise AssertionError(
        f"message with subject containing {subject_substring!r} "
        f"did not appear in INBOX within {timeout}s"
    )


def assert_flag_set(
    conn: imaplib.IMAP4 | imaplib.IMAP4_SSL,
    uid: bytes,
    flag: str,
) -> None:
    """
    Assert that the message identified by uid has the given flag set.
    flag should be e.g. r'\\Seen'.
    """
    typ, data = conn.fetch(uid, "(FLAGS)")
    assert typ == "OK", f"IMAP FETCH FLAGS failed: {typ} {data}"
    flags_str = data[0].decode()
    assert flag in flags_str, f"expected flag {flag!r} in {flags_str!r}"
    log("imap", "flag_verified", f"uid={uid!r} flag={flag!r}")


def get_inbox_count(
    conn: imaplib.IMAP4 | imaplib.IMAP4_SSL,
) -> int:
    """Return the current message count in INBOX via STATUS.

    A brand-new account with no delivered messages may not yet have an
    INBOX folder (lazy mailbox creation).  In that case STATUS returns
    NO [NONEXISTENT]; treat as count=0.
    """
    try:
        typ, data = conn.status("INBOX", "(MESSAGES)")
    except imaplib.IMAP4.error as exc:
        if b"NONEXISTENT" in str(exc).encode() or b"mailbox not found" in str(exc).encode():
            log("imap", "inbox_count", "count=0 (mailbox not yet created)")
            return 0
        raise
    if typ != "OK":
        # Tolerate NONEXISTENT (lazy INBOX creation) as count=0.
        joined = b" ".join(d if isinstance(d, bytes) else str(d).encode() for d in data)
        if b"NONEXISTENT" in joined or b"mailbox not found" in joined:
            log("imap", "inbox_count", "count=0 (mailbox not yet created)")
            return 0
        raise AssertionError(f"IMAP STATUS failed: {typ} {data}")
    # data[0] is like b'"INBOX" (MESSAGES 42)'
    import re
    m = re.search(rb"MESSAGES (\d+)", data[0])
    assert m, f"could not parse MESSAGES count from: {data[0]!r}"
    count = int(m.group(1))
    log("imap", "inbox_count", f"count={count}")
    return count


def wait_for_inbox_count(
    conn: imaplib.IMAP4 | imaplib.IMAP4_SSL,
    expected_min: int,
    timeout: float = 120.0,
    poll_interval: float = 2.0,
) -> int:
    """
    Poll STATUS until INBOX MESSAGES >= expected_min or timeout elapses.
    Returns the final count.  Raises AssertionError on timeout.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        count = get_inbox_count(conn)
        if count >= expected_min:
            return count
        time.sleep(poll_interval)
    count = get_inbox_count(conn)
    raise AssertionError(
        f"INBOX message count {count} did not reach {expected_min} within {timeout}s"
    )


def search_by_subject(
    conn: imaplib.IMAP4 | imaplib.IMAP4_SSL,
    subject_substring: str,
) -> list[bytes]:
    """Return a list of UIDs matching the given subject substring."""
    # Re-SELECT to flush the mailbox snapshot before searching.
    conn.select("INBOX")
    typ, data = conn.search(None, f'SUBJECT "{subject_substring}"')
    assert typ == "OK", f"IMAP SEARCH failed: {typ} {data}"
    ids = data[0].split()
    log("imap", "search_result", f"subject_substring={subject_substring!r} count={len(ids)}")
    return ids
