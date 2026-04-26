"""
SMTP send helpers for the interop test suite.

All connections use the CA root from TLS_CA_BUNDLE env var for cert
verification.  No CERT_NONE; no check_hostname=False.
"""

import email.message
import os
import smtplib
import ssl

from helpers.logging import log

# Path to the interop CA bundle, injected by docker-compose via TLS_CA_BUNDLE.
_CA_BUNDLE = os.environ.get("TLS_CA_BUNDLE", "/etc/interop/tls/ca.crt")


def _ssl_ctx() -> ssl.SSLContext:
    """Return a default SSL context trusting the interop CA."""
    ctx = ssl.create_default_context(cafile=_CA_BUNDLE)
    return ctx


def build_message(
    from_addr: str,
    to_addr: str,
    subject: str,
    body: str,
    message_id: str,
    extra_headers: dict | None = None,
) -> email.message.EmailMessage:
    """Build a simple text/plain RFC 5322 message."""
    msg = email.message.EmailMessage()
    msg["From"] = from_addr
    msg["To"] = to_addr
    msg["Subject"] = subject
    msg["Message-ID"] = f"<{message_id}@herold.interop>"
    msg.set_content(body)
    if extra_headers:
        for k, v in extra_headers.items():
            msg[k] = v
    return msg


def send_via_relay(
    host: str,
    port: int,
    from_addr: str,
    to_addr: str,
    msg: email.message.EmailMessage,
    use_starttls: bool = True,
) -> None:
    """
    Deliver a message through an MTA's port-25 relay listener.

    When use_starttls is True the connection issues STARTTLS and verifies
    the server cert against the interop CA.  Set use_starttls=False only
    for MTAs that do not advertise STARTTLS on port 25 (noted in README).
    """
    log("smtp_send", "connecting", f"host={host} port={port} starttls={use_starttls}")
    with smtplib.SMTP(host, port, timeout=30) as smtp:
        smtp.ehlo()
        if use_starttls:
            smtp.starttls(context=_ssl_ctx())
            smtp.ehlo()
        smtp.sendmail(from_addr, [to_addr], msg.as_bytes())
        log("smtp_send", "delivered", f"from={from_addr} to={to_addr}")


def send_via_submission(
    host: str,
    port: int,
    username: str,
    password: str,
    from_addr: str,
    to_addr: str,
    msg: email.message.EmailMessage,
) -> None:
    """
    Submit a message through herold's SMTP submission listener (port 587,
    STARTTLS + AUTH).  Cert is verified against the interop CA.
    """
    log(
        "smtp_submission",
        "connecting",
        f"host={host} port={port} user={username}",
    )
    with smtplib.SMTP(host, port, timeout=30) as smtp:
        smtp.ehlo()
        smtp.starttls(context=_ssl_ctx())
        smtp.ehlo()
        smtp.login(username, password)
        smtp.sendmail(from_addr, [to_addr], msg.as_bytes())
        log("smtp_submission", "delivered", f"from={from_addr} to={to_addr}")


def send_bulk(
    host: str,
    port: int,
    from_addr: str,
    to_addr: str,
    messages: list[email.message.EmailMessage],
    use_starttls: bool = True,
) -> int:
    """
    Deliver a list of messages in a single SMTP session (pipeline-friendly).
    Returns the count of successfully delivered messages.
    """
    log(
        "smtp_bulk",
        "starting",
        f"host={host} port={port} count={len(messages)} starttls={use_starttls}",
    )
    delivered = 0
    with smtplib.SMTP(host, port, timeout=60) as smtp:
        smtp.ehlo()
        if use_starttls:
            smtp.starttls(context=_ssl_ctx())
            smtp.ehlo()
        for msg in messages:
            smtp.sendmail(from_addr, [to_addr], msg.as_bytes())
            delivered += 1
    log("smtp_bulk", "done", f"delivered={delivered}")
    return delivered
