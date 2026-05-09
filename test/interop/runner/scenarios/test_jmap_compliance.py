"""
JMAP wire-protocol conformance via jmapio/jmap-test-suite.

The upstream suite is a TypeScript runner (~300 tests across RFC 8620 core +
RFC 8621 mail) that drives a live JMAP server through its HTTP wire surface
using a primary and secondary test account. It writes a JSON report and
exits 0 (all required passed), 1 (failures), or 2 (fatal).

Architecture
------------
The suite lives in the "jmaptest" compose service (profile "jmaptest"),
built from the upstream master branch at a pinned SHA (see
config/jmaptest/Dockerfile). The pytest runner docker-execs the suite
inside that container.

Endpoint
--------
The suite hits herold's admin listener over plain HTTP at
http://mail.herold.test:8080/.well-known/jmap. The interop herold container
runs in dev_mode, which co-mounts the JMAP handler on the same listener as
the admin REST API; production deployments split these into kind="public"
and kind="admin" listeners on separate ports.

Accounts (set up by config/herold/bootstrap.sh):
  primary   alice@herold.test / alicepw-interop
  secondary bob@herold.test   / bobpw-interop

Auth
----
authMethod=basic. Herold's JMAP server (internal/protojmap/auth.go) accepts
both Basic and Bearer; the suite's "basic" mode sends the principal's
password directly, which directory.Authenticate consumes.

Failure model
-------------
The test fails on:
  - Suite exit code != 0 AND any failing test id falls outside the
    KNOWN_INTENTIONAL_FAILURES allowlist (i.e. there is at least one
    *unexpected* failure)
  - The set of failing test ids does NOT exactly cover the allowlist
    (i.e. an entry has been "fixed" upstream-or-locally without us
    removing it from the allowlist — either way the allowlist has
    drifted and we want to know)
  - JSON report missing or unparseable

The full JSON report is saved to /artifacts/${RUN_ID}/jmap-report.json so
post-run inspection can drill into individual test failures.

KNOWN_INTENTIONAL_FAILURES
--------------------------
Test ids whose failures herold deliberately accepts as "won't fix until
the underlying machinery lands". Every entry needs a justification line
referencing the requirement that documents the refusal. When the
underlying capability ships, REMOVE the entry — it's an "expected to
pass now" gate, not a permanent waiver.
"""

import json
import os
import subprocess

import pytest

from helpers.logging import log

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

_JMAPTEST_SERVICE = "jmaptest"
_CONFIG_PATH = "/etc/jmap-test/config.json"

# Test ids the upstream suite reports as failing that herold refuses on
# purpose. See module docstring; remove entries when the underlying
# capability lands.
KNOWN_INTENTIONAL_FAILURES: dict[str, str] = {
    # Filtering an Email/query by someInThreadHaveKeyword /
    # noneInThreadHaveKeyword requires an aggregated thread-flag index
    # that herold has not built yet; the dispatcher refuses with
    # unsupportedFilter per REQ-PERF-IMPL-04 (1s response budget) rather
    # than fall back to a thread-wide scan. When the index ships, drop
    # both entries.
    "email/filter-some-in-thread-have-keyword":
        "REQ-PERF-IMPL-04: thread-flag index not yet built",
    "email/filter-none-in-thread-have-keyword":
        "REQ-PERF-IMPL-04: thread-flag index not yet built",
}

# Optional glob filter (forwarded to the suite's --filter flag) — set
# JMAPTEST_FILTER=core/* to scope a debug run. Default: run everything.
_JMAPTEST_FILTER = os.environ.get("JMAPTEST_FILTER", "")

# Wall-clock cap for the docker exec that runs the suite. Upstream's full
# matrix takes a couple of minutes against a healthy server; allow generous
# headroom for slow CI hosts and the per-test 30 s HTTP timeout.
_JMAPTEST_TIMEOUT = int(os.environ.get("JMAPTEST_TIMEOUT", "900"))


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _container_name(service: str) -> str | None:
    """Return the running container name for the compose service, or None."""
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


def _ensure_artifacts_dir(run_id: str) -> str:
    log_dir = f"/artifacts/{run_id}"
    os.makedirs(log_dir, exist_ok=True)
    return log_dir


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@pytest.mark.jmaptest
def test_jmap_compliance_baseline(run_id):
    """
    Run jmapio/jmap-test-suite against herold's JMAP listener and assert
    the suite exits 0 (all required tests pass).

    The test requires the "jmaptest" compose profile to be active. It is
    skipped (not failed) when the jmaptest container is not running, so
    the standard make interop suite is unaffected.
    """
    container = _container_name(_JMAPTEST_SERVICE)
    if container is None:
        pytest.skip(
            "jmaptest container not running; start with --profile jmaptest "
            "or use: make interop-jmaptest"
        )

    log_dir = _ensure_artifacts_dir(run_id)
    # The jmaptest container does not bind /artifacts (that mount belongs
    # to the runner). Write the JSON report into the container's /tmp and
    # docker cp it out below.
    report_path_in_container = "/tmp/jmap-report.json"
    stdout_path = f"{log_dir}/jmaptest-stdout.log"
    stderr_path = f"{log_dir}/jmaptest-stderr.log"

    cmd = [
        "docker", "exec",
        container,
        "node", "/opt/jmap-test-suite/dist/cli.js",
        "-c", _CONFIG_PATH,
        "-f",  # force-clean any leftover account data from a prior run
        "-o", report_path_in_container,
    ]
    if _JMAPTEST_FILTER:
        cmd += ["--filter", _JMAPTEST_FILTER]

    log(
        "jmaptest", "start",
        f"container={container} filter={_JMAPTEST_FILTER or '<all>'} "
        f"timeout={_JMAPTEST_TIMEOUT}s",
    )

    result = subprocess.run(
        cmd, capture_output=True, timeout=_JMAPTEST_TIMEOUT,
    )

    stdout = result.stdout.decode("utf-8", errors="replace")
    stderr = result.stderr.decode("utf-8", errors="replace")

    with open(stdout_path, "w", encoding="utf-8") as fh:
        fh.write(stdout)
    with open(stderr_path, "w", encoding="utf-8") as fh:
        fh.write(stderr)

    # Pull the JSON report out of the container into the runner's artifacts
    # dir. cp into a stopped container would fail, but the container idles
    # so docker cp works.
    cp = subprocess.run(
        ["docker", "cp", f"{container}:{report_path_in_container}",
         f"{log_dir}/jmap-report.json"],
        capture_output=True, timeout=30,
    )
    report: dict | None = None
    if cp.returncode == 0:
        try:
            with open(f"{log_dir}/jmap-report.json", "r", encoding="utf-8") as fh:
                report = json.load(fh)
        except (OSError, json.JSONDecodeError) as exc:
            log("jmaptest", "report_parse_failed", f"err={exc}")

    log(
        "jmaptest", "done",
        f"rc={result.returncode} stdout_chars={len(stdout)} "
        f"stderr_chars={len(stderr)}",
    )

    # Surface a tail of stdout so a failing run shows context without having
    # to open the artifact files.
    tail = "\n".join(stdout.strip().splitlines()[-60:])
    print(f"\n--- jmaptest stdout (last 60 lines) ---\n{tail}\n--- end ---")

    if report is not None:
        # Common report shapes carry a top-level summary; print whatever we
        # can find without locking the parser to one schema.
        summary = report.get("summary") or {
            k: report.get(k)
            for k in ("total", "passed", "failed", "skipped", "errors")
            if report.get(k) is not None
        }
        if summary:
            print(f"--- jmaptest summary ---\n{json.dumps(summary, indent=2)}\n--- end ---")

    # Reconcile the failure set against the allowlist. The suite exits 1
    # whenever any test fails, but we treat "all failures are documented
    # intentional refusals AND every documented refusal still fails" as a
    # green run.
    failing_ids = _failing_test_ids(report)
    expected = set(KNOWN_INTENTIONAL_FAILURES)
    unexpected = failing_ids - expected
    fixed = expected - failing_ids

    assert not unexpected, (
        f"jmaptest reported failures NOT in KNOWN_INTENTIONAL_FAILURES: "
        f"{sorted(unexpected)}; "
        f"stdout: {stdout_path}, stderr: {stderr_path}, "
        f"report: {log_dir}/jmap-report.json"
    )
    assert not fixed, (
        f"KNOWN_INTENTIONAL_FAILURES contains test ids that now pass: "
        f"{sorted(fixed)} — remove them from the allowlist"
    )

    if failing_ids:
        log(
            "jmaptest", "passed_with_known_failures",
            f"run_id={run_id} count={len(failing_ids)} "
            f"ids={sorted(failing_ids)}",
        )
    else:
        log("jmaptest", "passed", f"run_id={run_id}")


def _failing_test_ids(report: dict | None) -> set[str]:
    """Extract the set of failing test ids from a jmap-test-suite report.

    The upstream report has a top-level "results" array with one record
    per test. Each record carries "testId" and "status" ("pass" / "fail"
    / "skip"). When the report is missing entirely we treat the failure
    set as empty so the assertion below catches the absence via the
    "unexpected failure" branch.
    """
    if report is None:
        return set()
    results = report.get("results") or []
    out: set[str] = set()
    for r in results:
        if r.get("status") == "fail":
            tid = r.get("testId")
            if isinstance(tid, str):
                out.add(tid)
    return out
