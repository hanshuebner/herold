"""
Shared pytest fixtures for the interop test suite.

All MTA endpoint coordinates come from environment variables injected by
docker-compose.yml. No hardcoded addresses in test code.

TLS: the CA root path comes from TLS_CA_BUNDLE env var (default:
/etc/interop/tls/ca.crt). All helpers load this value; no CERT_NONE.
"""

import os
import uuid

import pytest

from helpers.logging import log


def pytest_configure(config):
    """Register custom markers to satisfy --strict-markers."""
    config.addinivalue_line("markers", "inbound: tests verifying herold receives mail")
    config.addinivalue_line("markers", "outbound: tests verifying herold delivers mail")
    config.addinivalue_line("markers", "imap_client: text-mode IMAP client interop scenarios")
    config.addinivalue_line(
        "markers",
        "bulk: load / bulk-send scenarios; NOT collected by default",
    )


@pytest.fixture(scope="session")
def run_id() -> str:
    """Session-scoped run ID: from env (set by run.sh) or a fresh UUID."""
    rid = os.environ.get("RUN_ID", f"local-{uuid.uuid4().hex[:8]}")
    log("session", "run_id", f"run_id={rid}")
    return rid


# ---------- Herold endpoint fixtures ----------

@pytest.fixture(scope="session")
def herold_smtp_host() -> str:
    return os.environ.get("HEROLD_HOST", "mail.herold.test")


@pytest.fixture(scope="session")
def herold_smtp_port() -> int:
    return int(os.environ.get("HEROLD_SMTP_PORT", "25"))


@pytest.fixture(scope="session")
def herold_submission_port() -> int:
    return int(os.environ.get("HEROLD_SUBMISSION_PORT", "587"))


@pytest.fixture(scope="session")
def herold_imap_port() -> int:
    return int(os.environ.get("HEROLD_IMAP_PORT", "143"))


@pytest.fixture(scope="session")
def herold_imaps_port() -> int:
    return int(os.environ.get("HEROLD_IMAPS_PORT", "993"))


@pytest.fixture(scope="session")
def herold_metrics_url() -> str:
    return os.environ.get("HEROLD_METRICS_URL", "http://herold:9090/metrics")


# ---------- docker-mailserver (Postfix) endpoint fixtures ----------

@pytest.fixture(scope="session")
def postfix_host() -> str:
    return os.environ.get("POSTFIX_HOST", "mail.postfix.test")


@pytest.fixture(scope="session")
def postfix_smtp_port() -> int:
    return int(os.environ.get("POSTFIX_SMTP_PORT", "25"))


@pytest.fixture(scope="session")
def postfix_imap_port() -> int:
    return int(os.environ.get("POSTFIX_IMAP_PORT", "143"))


# ---------- Apache James endpoint fixtures ----------

@pytest.fixture(scope="session")
def james_host() -> str:
    return os.environ.get("JAMES_HOST", "mail.james.test")


@pytest.fixture(scope="session")
def james_smtp_port() -> int:
    return int(os.environ.get("JAMES_SMTP_PORT", "25"))


@pytest.fixture(scope="session")
def james_imap_port() -> int:
    return int(os.environ.get("JAMES_IMAP_PORT", "143"))


# ---------- Per-test unique nonce ----------

@pytest.fixture
def nonce() -> str:
    """Per-test unique nonce for Message-ID and Subject generation."""
    return uuid.uuid4().hex[:12]
