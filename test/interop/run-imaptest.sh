#!/bin/bash
# run-imaptest.sh - run the Dovecot imaptest IMAP conformance suite.
#
# Usage:
#   ./run-imaptest.sh
#   IMAPTEST_SECS=120 ./run-imaptest.sh    # longer soak run
#
# This script is a thin wrapper around the standard run.sh that additionally
# starts the "imaptest" compose profile and restricts pytest to the
# @pytest.mark.imaptest marker.
#
# Environment:
#   IMAPTEST_SECS   Duration passed to imaptest's secs= flag (default 30).
#   COMPOSE_PROJECT_NAME  Compose project name (default: interop).
#
# The standard make interop target is unaffected; the imaptest service is
# gated behind the "imaptest" compose profile and the pytest marker excludes
# it from the default run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
export RUN_ID
export IMAPTEST_SECS="${IMAPTEST_SECS:-30}"

LOGS_DIR="${SCRIPT_DIR}/logs/${RUN_ID}"
mkdir -p "${LOGS_DIR}"

echo "interop-imaptest: run_id=${RUN_ID}"
echo "interop-imaptest: logs at ${LOGS_DIR}"
echo "interop-imaptest: secs=${IMAPTEST_SECS}"

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
        out=$(docker compose --profile imaptest up -d --no-recreate "${svc}" 2>&1)
        rc=$?
        echo "${out}" | tee -a "${LOGS_DIR}/compose.log"
        if [ "${rc}" -eq 0 ] && ! echo "${out}" | grep -q "failed to set up container networking"; then
            return 0
        fi
        echo "interop-imaptest: ${svc} hit bridge race on attempt ${attempt}; sleeping ${SVC_RETRY_GAP}s"
        sleep "${SVC_RETRY_GAP}"
    done
    echo "interop-imaptest: FATAL ${svc} failed to start after ${MAX_SVC_RETRIES} attempts"
    docker compose --profile imaptest ps "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    docker compose --profile imaptest logs --tail 40 "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    return 1
}

# Build images including the imaptest image.
docker compose --profile imaptest build --quiet herold runner imaptest \
    2>&1 | tee "${LOGS_DIR}/build.log"
docker compose --profile imaptest pull --quiet coredns postfix \
    2>&1 | tee -a "${LOGS_DIR}/build.log" || true

# Phase 1: DNS.
compose_up_one coredns

# Phase 2: certgen.
compose_up_one certgen
echo "interop-imaptest: waiting for certgen to complete"
docker compose --profile imaptest wait certgen 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 3: herold first (imaptest depends on it being healthy).
for svc in herold postfix james mutt-client snail-client; do
    compose_up_one "${svc}" || true
    sleep 2
done

# Phase 4: wait for herold health check.
echo "interop-imaptest: waiting for herold health check"
docker compose --profile imaptest up -d --wait herold \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 5: james-setup.
compose_up_one james-setup
echo "interop-imaptest: waiting for james-setup to complete"
docker compose --profile imaptest wait james-setup 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 6: imaptest container (idles until docker exec runs imaptest).
compose_up_one imaptest

# Wait for the imaptest container to finish installing the CA.
# Try both separator conventions (dash and underscore) for the container name,
# matching the same pattern the Python _container_name() helper uses.
wait_imaptest_ready() {
    local project="${COMPOSE_PROJECT_NAME:-interop}"
    local max_attempts="${1:-30}"
    local container=""
    echo "interop-imaptest: waiting for imaptest container to become ready"
    for i in $(seq 1 "${max_attempts}"); do
        for sep in "-" "_"; do
            local candidate="${project}${sep}imaptest${sep}1"
            if docker exec "${candidate}" sh -c "test -x /usr/local/bin/imaptest" >/dev/null 2>&1; then
                container="${candidate}"
                echo "interop-imaptest: imaptest container ready: ${container} (attempt ${i})"
                return 0
            fi
        done
        sleep 2
    done
    echo "interop-imaptest: WARNING imaptest container did not become ready"
    docker ps -a 2>/dev/null | grep imaptest || true
    return 1
}
wait_imaptest_ready 30 || true

NETWORK_NAME="$(docker compose --profile imaptest ps --format '{{.Name}}' | head -1 | \
    xargs -I{} docker inspect {} --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' \
    2>/dev/null || echo interop_interop)"

wait_port() {
    local target_host="$1"
    local target_port="$2"
    local label="$3"
    local max_attempts="${4:-45}"
    echo "interop-imaptest: waiting for ${label} on ${target_host}:${target_port}"
    for i in $(seq 1 "${max_attempts}"); do
        if docker run --rm --network "${NETWORK_NAME}" alpine:3.20 \
                sh -c "nc -z -w1 ${target_host} ${target_port}" >/dev/null 2>&1; then
            echo "interop-imaptest: ${label} ready (attempt ${i})"
            return 0
        fi
        sleep 2
    done
    echo "interop-imaptest: WARNING ${label} not ready"
    return 1
}
wait_port postfix 25 "postfix" 90 || true
wait_port james 25 "james" 90 || true

# Collect logs helper.
collect_log() {
    local svc="$1"
    docker compose --profile imaptest logs "${svc}" 2>/dev/null > "${LOGS_DIR}/${svc}.log" || true
}
collect_all_logs() {
    collect_log coredns
    collect_log certgen
    collect_log herold
    # Save imaptest container stdout (CA install messages) separately from
    # the imaptest binary output (written by _save_log inside the runner).
    docker compose --profile imaptest logs imaptest 2>/dev/null \
        > "${LOGS_DIR}/imaptest-container.log" || true
    collect_log postfix
    collect_log james
    collect_log james-setup
}

# Run pytest with only the imaptest marker.
# Pass IMAPTEST_SECS into the runner container via -e.
PYTEST_EXIT=0
docker compose --profile imaptest \
    run --rm \
    -e RUN_ID="${RUN_ID}" \
    -e IMAPTEST_SECS="${IMAPTEST_SECS}" \
    runner \
    pytest -v --tb=short \
        --junitxml="/artifacts/${RUN_ID}/junit.xml" \
        -m imaptest \
        scenarios/ \
    2>&1 | tee "${LOGS_DIR}/pytest.log" \
    || PYTEST_EXIT=$?

ln -sfn "${RUN_ID}" "${SCRIPT_DIR}/logs/latest"

collect_all_logs

docker compose --profile imaptest down --remove-orphans --volumes \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

echo ""
echo "interop-imaptest: run complete (exit=${PYTEST_EXIT})"
echo "interop-imaptest: logs at ${LOGS_DIR}"
echo "interop-imaptest: imaptest.log at ${LOGS_DIR}/imaptest.log"

exit "${PYTEST_EXIT}"
