"""
Bulk send/receive scenarios (task #4).

These tests are marked 'bulk' and are NOT collected by default (see pytest.ini).
Run with:  pytest -m bulk
Or via:    make interop-bulk

BULK_N controls the message count (default 500; set via env var).

Inbound bulk: the runner sends BULK_N messages directly to herold:25 in
a single SMTP session (postfix-as-sender's default concurrent delivery
exceeds herold's MaxConcurrentPerIP cap).  We then poll IMAP STATUS and
SEARCH to verify all messages arrived.  Prometheus metrics are collected
before and after to assert sane invariants.

Bulk mixed: two threads, one inbound and one outbound (v3 nice-to-have).
"""

import os
import re
import time
import uuid

import pytest
import requests

from helpers.imap_assert import connect_imaps, wait_for_inbox_count, search_by_subject
from helpers.logging import log
from helpers.smtp_send import build_message, send_bulk

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

BULK_N = int(os.environ.get("BULK_N", "500"))

ALICE_USER = "alice@herold.test"
ALICE_PASS = "alicepw-interop"

HEROLD_METRICS_URL = os.environ.get("HEROLD_METRICS_URL", "http://herold:9090/metrics")

# ---------------------------------------------------------------------------
# Metrics helpers
# ---------------------------------------------------------------------------


def _scrape_metrics() -> dict[str, float]:
    """
    Fetch /metrics from herold and parse Prometheus text format into a dict.
    Returns a mapping of metric_name -> float value (last seen per name).
    """
    try:
        r = requests.get(HEROLD_METRICS_URL, timeout=10)
        r.raise_for_status()
    except Exception as exc:
        log("metrics", "scrape_failed", f"url={HEROLD_METRICS_URL} err={exc}")
        return {}

    result: dict[str, float] = {}
    for line in r.text.splitlines():
        if line.startswith("#") or not line.strip():
            continue
        # Each data line: metric_name{labels} value [timestamp]
        m = re.match(r'^([a-zA-Z_][a-zA-Z0-9_:]*(?:\{[^}]*\})?) +([0-9.eE+\-]+)', line)
        if m:
            result[m.group(1)] = float(m.group(2))
    return result


def _metric_sum(metrics: dict[str, float], prefix: str) -> float:
    """Sum all metric values whose key starts with prefix."""
    return sum(v for k, v in metrics.items() if k.startswith(prefix))


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@pytest.mark.bulk
def test_bulk_inbound(run_id, herold_smtp_host, herold_imaps_port, herold_smtp_port):
    """
    Send BULK_N messages addressed to alice@herold.test in a single SMTP
    session directly to herold's port-25 listener.  Verify all arrive;
    check Prometheus deltas.

    Note: we do NOT route through Postfix here.  Postfix's default
    smtp_destination_concurrency_limit (20) opens parallel deliveries to
    herold, which exceeds herold's per-IP concurrency cap (16, see
    internal/protosmtp/server.go MaxConcurrentPerIP), so a chunk of the
    batch gets refused with "smtp connection refused (per-IP cap)".
    A single sequential SMTP session models the "bulk receive" path
    cleanly without dragging postfix's concurrency policy into the
    measurement.
    """
    bulk_id = uuid.uuid4().hex[:8]
    log("bulk_inbound", "start", f"n={BULK_N} bulk_id={bulk_id}")

    # --- Scrape before -------------------------------------------------------
    metrics_before = _scrape_metrics()
    accepted_before = _metric_sum(
        metrics_before, "herold_smtp_messages_accepted_total"
    )
    log("bulk_inbound", "metrics_before", f"accepted={accepted_before}")

    # --- Get current message count so we can track the delta -----------------
    conn = connect_imaps(herold_smtp_host, herold_imaps_port, ALICE_USER, ALICE_PASS)
    try:
        from helpers.imap_assert import get_inbox_count
        count_before = get_inbox_count(conn)
    finally:
        conn.logout()

    # --- Build and send BULK_N messages in a single SMTP session -------------
    messages = []
    for i in range(BULK_N):
        subject = f"bulk-inbound-{bulk_id}-{i:05d}"
        msg = build_message(
            from_addr="bulk-sender@external.test",
            to_addr=ALICE_USER,
            subject=subject,
            body=f"bulk body run={run_id} bulk_id={bulk_id} seq={i}",
            message_id=f"bulk-{bulk_id}-{i:05d}",
        )
        messages.append(msg)

    t_start = time.monotonic()
    delivered = send_bulk(
        host=herold_smtp_host,
        port=herold_smtp_port,
        from_addr="bulk-sender@external.test",
        to_addr=ALICE_USER,
        messages=messages,
        use_starttls=False,
    )
    t_send = time.monotonic() - t_start
    assert delivered == BULK_N, f"expected {BULK_N} delivered, got {delivered}"

    # --- Poll IMAP until all messages arrive (deadline: 120s) ----------------
    conn = connect_imaps(herold_smtp_host, herold_imaps_port, ALICE_USER, ALICE_PASS)
    try:
        t_poll_start = time.monotonic()
        final_count = wait_for_inbox_count(conn, count_before + BULK_N, timeout=120)
        t_poll = time.monotonic() - t_poll_start

        # SEARCH by bulk_id substring to count exact arrivals.
        matching = search_by_subject(conn, bulk_id)
        arrived = len(matching)
    finally:
        conn.logout()

    t_wall = time.monotonic() - t_start
    log(
        "bulk_inbound",
        "done",
        f"n={BULK_N} delivered={delivered} arrived={arrived} "
        f"t_send={t_send:.1f}s t_poll={t_poll:.1f}s t_wall={t_wall:.1f}s",
    )

    # --- Scrape after --------------------------------------------------------
    metrics_after = _scrape_metrics()
    accepted_after = _metric_sum(
        metrics_after, "herold_smtp_messages_accepted_total"
    )
    accepted_delta = accepted_after - accepted_before
    log(
        "bulk_inbound",
        "metrics_after",
        f"accepted_before={accepted_before} accepted_after={accepted_after} delta={accepted_delta}",
    )

    # --- Assertions ----------------------------------------------------------
    assert arrived == BULK_N, (
        f"expected {BULK_N} messages with bulk_id={bulk_id!r} in INBOX, got {arrived}"
    )

    # Prometheus invariants: accepted counter must increment by exactly BULK_N.
    # Tolerance of 0: we sent to one recipient in a clean session.
    if metrics_before and metrics_after:
        assert accepted_delta == BULK_N, (
            f"herold_smtp_messages_accepted_total delta={accepted_delta} expected={BULK_N}"
        )
        # No error counters should have increased.
        error_before = _metric_sum(metrics_before, "herold_smtp_messages_rejected_total")
        error_after = _metric_sum(metrics_after, "herold_smtp_messages_rejected_total")
        assert error_after - error_before == 0, (
            f"rejection counter increased by {error_after - error_before} during bulk run"
        )
    else:
        log("bulk_inbound", "metrics_skipped", "could not scrape /metrics; invariant check skipped")


@pytest.mark.bulk
@pytest.mark.skip(reason="bulk-mixed (two threads: inbound + outbound) is v3 scope; skipped")
def test_bulk_mixed(run_id):
    """Two concurrent threads: inbound feed + outbound submission. v3 scope."""
    pass
