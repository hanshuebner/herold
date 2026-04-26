"""
Outbound relay scenarios: herold submission -> external MTA delivery.

Marker: outbound
"""

import pytest

from helpers.logging import log
from helpers.smtp_send import build_message, send_via_submission
from helpers.imap_assert import connect_starttls, connect_plain, assert_message_in_inbox

ALICE_USER = "alice@herold.test"
ALICE_PASS = "alicepw-interop"


@pytest.mark.outbound
def test_outbound_relay_to_postfix(
    run_id,
    nonce,
    herold_smtp_host,
    herold_submission_port,
    postfix_host,
    postfix_imap_port,
):
    """alice@herold.test submits mail to carol@postfix.test; carol receives it."""
    subject = f"outbound-to-postfix-{nonce}"
    body = f"outbound body run={run_id} nonce={nonce}"
    msg = build_message(
        from_addr=ALICE_USER,
        to_addr="carol@postfix.test",
        subject=subject,
        body=body,
        message_id=f"out-postfix-{nonce}",
    )
    send_via_submission(
        host=herold_smtp_host,
        port=herold_submission_port,
        username=ALICE_USER,
        password=ALICE_PASS,
        from_addr=ALICE_USER,
        to_addr="carol@postfix.test",
        msg=msg,
    )

    conn = connect_starttls(postfix_host, postfix_imap_port, "carol@postfix.test", "testpw-carol1")
    try:
        raw = assert_message_in_inbox(conn, subject, timeout=30)
        assert nonce.encode() in raw
    finally:
        conn.logout()


@pytest.mark.outbound
def test_outbound_relay_to_james(
    run_id,
    nonce,
    herold_smtp_host,
    herold_submission_port,
    james_host,
    james_imap_port,
):
    """alice@herold.test submits mail to dave@james.test; dave receives it."""
    subject = f"outbound-to-james-{nonce}"
    body = f"outbound body run={run_id} nonce={nonce}"
    msg = build_message(
        from_addr=ALICE_USER,
        to_addr="dave@james.test",
        subject=subject,
        body=body,
        message_id=f"out-james-{nonce}",
    )
    send_via_submission(
        host=herold_smtp_host,
        port=herold_submission_port,
        username=ALICE_USER,
        password=ALICE_PASS,
        from_addr=ALICE_USER,
        to_addr="dave@james.test",
        msg=msg,
    )

    # James serves a self-signed cert on its IMAP STARTTLS port; the
    # interop CA does not chain to it (replacing the JKS keystore inside
    # the running JVM is v3 work).  Connect plaintext on the private
    # interop docker network.
    conn = connect_plain(james_host, james_imap_port, "dave@james.test", "testpw-dave1")
    try:
        raw = assert_message_in_inbox(conn, subject, timeout=30)
        assert nonce.encode() in raw
    finally:
        conn.logout()
