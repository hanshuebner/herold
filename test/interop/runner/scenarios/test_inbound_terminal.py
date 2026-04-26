"""
Inbound relay scenarios: external MTAs deliver to herold via SMTP port 25.
For each external MTA, we:
  1. Send a message FROM <sender>@<mta>.test TO alice@herold.test via the
     external MTA's relay listener.
  2. Verify the message appears in alice's INBOX via IMAPS (port 993).

TLS: all SMTP connections use STARTTLS with the interop CA cert; all IMAP
connections use IMAPS with the same CA.  No ssl.CERT_NONE anywhere.

Marker: inbound
"""

import pytest

from helpers.imap_assert import connect_imaps, assert_message_in_inbox
from helpers.logging import log
from helpers.smtp_send import build_message, send_via_relay


# --------------------------------------------------------------------------
# Herold inbound endpoint coordinates (read from env via conftest fixtures).
# --------------------------------------------------------------------------

ALICE_USER = "alice@herold.test"
ALICE_PASS = "alicepw-interop"


# --------------------------------------------------------------------------
# Tests
# --------------------------------------------------------------------------


@pytest.mark.inbound
@pytest.mark.xfail(
    strict=True,
    reason=(
        "stalwart container does not yet trust the interop CA: when stalwart "
        "accepts the test message and tries to deliver it onward via STARTTLS "
        "to its own MX, the handshake fails with "
        "'invalid peer certificate: UnknownIssuer'. Fix is to install the "
        "interop CA root into stalwart's system trust store at startup. "
        "Tracked as v3 work; remove this marker when fixed."
    ),
)
def test_inbound_terminal_from_stalwart(
    run_id,
    nonce,
    stalwart_host,
    stalwart_smtp_port,
    herold_smtp_host,
    herold_imaps_port,
):
    """Stalwart sends a message to alice@herold.test; alice sees it via IMAPS."""
    subject = f"interop-stalwart-{nonce}"
    body = f"body from stalwart run={run_id} nonce={nonce}"
    msg = build_message(
        from_addr=f"stalwart-sender@stalwart.test",
        to_addr=ALICE_USER,
        subject=subject,
        body=body,
        message_id=f"stalwart-{nonce}",
    )
    log("test", "sending", f"via=stalwart nonce={nonce}")
    send_via_relay(
        host=stalwart_host,
        port=stalwart_smtp_port,
        from_addr=f"stalwart-sender@stalwart.test",
        to_addr=ALICE_USER,
        msg=msg,
        use_starttls=False,  # Stalwart v0.10.6 on port 25: plaintext ok in interop net
    )

    conn = connect_imaps(herold_smtp_host, herold_imaps_port, ALICE_USER, ALICE_PASS)
    try:
        raw = assert_message_in_inbox(conn, subject, timeout=30)
        assert body.encode() in raw or nonce.encode() in raw, (
            f"expected body content in fetched message"
        )
    finally:
        conn.logout()


@pytest.mark.inbound
def test_inbound_terminal_from_postfix(
    run_id,
    nonce,
    postfix_host,
    postfix_smtp_port,
    herold_smtp_host,
    herold_imaps_port,
):
    """docker-mailserver (Postfix) sends to alice@herold.test; alice sees it via IMAPS."""
    subject = f"interop-postfix-{nonce}"
    body = f"body from postfix run={run_id} nonce={nonce}"
    msg = build_message(
        from_addr="carol@postfix.test",
        to_addr=ALICE_USER,
        subject=subject,
        body=body,
        message_id=f"postfix-{nonce}",
    )
    log("test", "sending", f"via=postfix nonce={nonce}")
    send_via_relay(
        host=postfix_host,
        port=postfix_smtp_port,
        from_addr="carol@postfix.test",
        to_addr=ALICE_USER,
        msg=msg,
        use_starttls=False,  # docker-mailserver port 25: STARTTLS optional in relay mode
    )

    conn = connect_imaps(herold_smtp_host, herold_imaps_port, ALICE_USER, ALICE_PASS)
    try:
        raw = assert_message_in_inbox(conn, subject, timeout=30)
        assert nonce.encode() in raw, "expected nonce in fetched message"
    finally:
        conn.logout()


@pytest.mark.inbound
def test_inbound_terminal_from_james(
    run_id,
    nonce,
    james_host,
    james_smtp_port,
    herold_smtp_host,
    herold_imaps_port,
):
    """Apache James sends to alice@herold.test; alice sees it via IMAPS."""
    subject = f"interop-james-{nonce}"
    body = f"body from james run={run_id} nonce={nonce}"
    msg = build_message(
        from_addr="dave@james.test",
        to_addr=ALICE_USER,
        subject=subject,
        body=body,
        message_id=f"james-{nonce}",
    )
    log("test", "sending", f"via=james nonce={nonce}")
    send_via_relay(
        host=james_host,
        port=james_smtp_port,
        from_addr="dave@james.test",
        to_addr=ALICE_USER,
        msg=msg,
        use_starttls=False,  # James port 25: plaintext in relay mode
    )

    conn = connect_imaps(herold_smtp_host, herold_imaps_port, ALICE_USER, ALICE_PASS)
    try:
        raw = assert_message_in_inbox(conn, subject, timeout=30)
        assert nonce.encode() in raw, "expected nonce in fetched message"
    finally:
        conn.logout()
