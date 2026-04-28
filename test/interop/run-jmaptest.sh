#!/bin/bash
# run-jmaptest.sh - run the jmapio/jmap-test-suite JMAP conformance suite.
#
# Usage:
#   ./run-jmaptest.sh
#   JMAPTEST_FILTER='core/*' ./run-jmaptest.sh    # restrict to one category
#   JMAPTEST_TIMEOUT=1800 ./run-jmaptest.sh       # longer overall timeout
#
# This script is a thin wrapper around the standard run.sh that additionally
# starts the "jmaptest" compose profile and restricts pytest to the
# @pytest.mark.jmaptest marker.
#
# Environment:
#   JMAPTEST_FILTER  Glob forwarded to the suite's --filter (default: empty,
#                    runs everything).
#   JMAPTEST_TIMEOUT Overall wall-clock cap for the suite, in seconds
#                    (default: 900).
#   COMPOSE_PROJECT_NAME  Compose project name (default: interop).
#
# The standard make interop target is unaffected; the jmaptest service is
# gated behind the "jmaptest" compose profile and the pytest marker excludes
# it from the default run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
export RUN_ID
export JMAPTEST_FILTER="${JMAPTEST_FILTER:-}"
export JMAPTEST_TIMEOUT="${JMAPTEST_TIMEOUT:-900}"

LOGS_DIR="${SCRIPT_DIR}/logs/${RUN_ID}"
mkdir -p "${LOGS_DIR}"

echo "interop-jmaptest: run_id=${RUN_ID}"
echo "interop-jmaptest: logs at ${LOGS_DIR}"
echo "interop-jmaptest: filter=${JMAPTEST_FILTER:-<all>} timeout=${JMAPTEST_TIMEOUT}s"

# Clean up any stale compose state from previous runs.
clean_compose_state() {
    docker compose down --remove-orphans --volumes 2>/dev/null || true
    docker ps -a --filter network=interop_interop -q 2>/dev/null \
        | xargs -r docker rm -f >/dev/null 2>&1 || true
    docker network rm interop_interop -f 2>/dev/null || true
    docker network prune -f >/dev/null 2>&1 || true
}
clean_compose_state
sleep 5

MAX_SVC_RETRIES=6
SVC_RETRY_GAP=4

compose_up_one() {
    local svc="$1"
    local attempt out rc
    for attempt in $(seq 1 "${MAX_SVC_RETRIES}"); do
        out=$(docker compose --profile jmaptest up -d --no-recreate "${svc}" 2>&1)
        rc=$?
        echo "${out}" | tee -a "${LOGS_DIR}/compose.log"
        if [ "${rc}" -eq 0 ] && ! echo "${out}" | grep -q "failed to set up container networking"; then
            return 0
        fi
        echo "interop-jmaptest: ${svc} hit bridge race on attempt ${attempt}; sleeping ${SVC_RETRY_GAP}s"
        sleep "${SVC_RETRY_GAP}"
    done
    echo "interop-jmaptest: FATAL ${svc} failed to start after ${MAX_SVC_RETRIES} attempts"
    docker compose --profile jmaptest ps "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    docker compose --profile jmaptest logs --tail 40 "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    return 1
}

# Build the herold + runner + jmaptest images.
docker compose --profile jmaptest build --quiet herold runner jmaptest \
    2>&1 | tee "${LOGS_DIR}/build.log"
docker compose --profile jmaptest pull --quiet coredns \
    2>&1 | tee -a "${LOGS_DIR}/build.log" || true

# Phase 1: DNS.
compose_up_one coredns

# Phase 2: certgen.
compose_up_one certgen
echo "interop-jmaptest: waiting for certgen to complete"
docker compose --profile jmaptest wait certgen 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 3: herold (the only MTA jmaptest needs).
compose_up_one herold

# Phase 4: wait for herold health check.
echo "interop-jmaptest: waiting for herold health check"
docker compose --profile jmaptest up -d --wait herold \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 5: jmaptest container (idles until docker exec runs the suite).
compose_up_one jmaptest

# Wait for the jmaptest container to finish installing the CA.
wait_jmaptest_ready() {
    local project="${COMPOSE_PROJECT_NAME:-interop}"
    local max_attempts="${1:-30}"
    local container=""
    echo "interop-jmaptest: waiting for jmaptest container to become ready"
    for i in $(seq 1 "${max_attempts}"); do
        for sep in "-" "_"; do
            local candidate="${project}${sep}jmaptest${sep}1"
            if docker exec "${candidate}" sh -c "test -f /opt/jmap-test-suite/dist/cli.js" >/dev/null 2>&1; then
                container="${candidate}"
                echo "interop-jmaptest: jmaptest container ready: ${container} (attempt ${i})"
                return 0
            fi
        done
        sleep 2
    done
    echo "interop-jmaptest: WARNING jmaptest container did not become ready"
    docker ps -a 2>/dev/null | grep jmaptest || true
    return 1
}
wait_jmaptest_ready 30 || true

# Collect logs helper.
collect_log() {
    local svc="$1"
    docker compose --profile jmaptest logs "${svc}" 2>/dev/null > "${LOGS_DIR}/${svc}.log" || true
}
collect_all_logs() {
    collect_log coredns
    collect_log certgen
    collect_log herold
    docker compose --profile jmaptest logs jmaptest 2>/dev/null \
        > "${LOGS_DIR}/jmaptest-container.log" || true
}

# Run pytest with only the jmaptest marker. Forward JMAPTEST_FILTER /
# JMAPTEST_TIMEOUT into the runner container.
PYTEST_EXIT=0
docker compose --profile jmaptest \
    run --rm \
    -e RUN_ID="${RUN_ID}" \
    -e JMAPTEST_FILTER="${JMAPTEST_FILTER}" \
    -e JMAPTEST_TIMEOUT="${JMAPTEST_TIMEOUT}" \
    runner \
    pytest -v --tb=short \
        --junitxml="/artifacts/${RUN_ID}/junit.xml" \
        -m jmaptest \
        scenarios/ \
    2>&1 | tee "${LOGS_DIR}/pytest.log" \
    || PYTEST_EXIT=$?

ln -sfn "${RUN_ID}" "${SCRIPT_DIR}/logs/latest"

collect_all_logs

docker compose --profile jmaptest down --remove-orphans --volumes \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

echo ""
echo "interop-jmaptest: run complete (exit=${PYTEST_EXIT})"
echo "interop-jmaptest: logs at ${LOGS_DIR}"
echo "interop-jmaptest: jmap-report.json at ${LOGS_DIR}/jmap-report.json"

exit "${PYTEST_EXIT}"
