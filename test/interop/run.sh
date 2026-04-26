#!/bin/bash
# run.sh - run the interop test suite end-to-end.
#
# Usage:
#   ./run.sh              # standard run (no bulk tests)
#   BULK_N=100 ./run.sh   # standard run; ignored since bulk is not in default suite
#   ./run.sh --bulk       # include bulk tests (sets the 'bulk' marker)
#
# The script:
#   1. Generates a RUN_ID for log correlation.
#   2. Creates a per-run log directory.
#   3. Brings up the compose stack (certgen, DNS, MTAs).
#   4. Waits for herold to be healthy.
#   5. Runs the pytest runner container.
#   6. Copies logs out of compose and shuts everything down.
#   7. Prints a summary and exits with the pytest exit code.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
export RUN_ID
export BULK_N="${BULK_N:-500}"

LOGS_DIR="${SCRIPT_DIR}/logs/${RUN_ID}"
mkdir -p "${LOGS_DIR}"

echo "interop: run_id=${RUN_ID}"
echo "interop: logs at ${LOGS_DIR}"

BULK_MARKER=""
if [[ "${1:-}" == "--bulk" ]]; then
    BULK_MARKER="-m bulk"
    echo "interop: bulk mode enabled (BULK_N=${BULK_N})"
fi
# Allow overriding the marker selection via PYTEST_MARKER env var
# (e.g. PYTEST_MARKER='not bulk and not imap_client').  When set, this
# wins over the default '-m "not bulk"' below.
if [[ -n "${PYTEST_MARKER:-}" ]]; then
    BULK_MARKER="-m ${PYTEST_MARKER}"
    echo "interop: pytest marker override: ${PYTEST_MARKER}"
fi

# Clean up previous compose state.  Docker Desktop on macOS sometimes leaves
# the interop bridge in a stale state after compose down, causing
# "Address already in use" when the next run tries to attach a container.
# Recovery requires removing every container that was on the network, then
# the network itself, then giving the daemon a few seconds to garbage-
# collect the bridge interface.
clean_compose_state() {
    docker compose down --remove-orphans --volumes 2>/dev/null || true
    docker ps -a --filter network=interop_interop -q 2>/dev/null \
        | xargs -r docker rm -f >/dev/null 2>&1 || true
    docker network rm interop_interop -f 2>/dev/null || true
    docker network prune -f >/dev/null 2>&1 || true
}
clean_compose_state
sleep 5

# Wrapper around `docker compose up -d <services>` that retries once on
# the "Address already in use" failure mode.
compose_up() {
    local out
    if out=$(docker compose up -d "$@" 2>&1); then
        echo "${out}"
        return 0
    fi
    echo "${out}"
    if echo "${out}" | grep -q "Address already in use"; then
        echo "interop: 'Address already in use' on first attempt; cleaning and retrying"
        clean_compose_state
        sleep 10
        docker compose up -d "$@" 2>&1
        return $?
    fi
    return 1
}

# Build / pull images.
docker compose build --quiet herold runner 2>&1 | tee "${LOGS_DIR}/build.log"
docker compose pull --quiet coredns postfix 2>&1 | tee -a "${LOGS_DIR}/build.log" || true

# Start core services (no runner profile yet).
# Phase 1: start infrastructure + certgen.
compose_up coredns certgen \
    2>&1 | tee "${LOGS_DIR}/compose.log" || true

# Wait for certgen to complete (it's an init container; exits 0 when done).
echo "interop: waiting for certgen to complete"
docker compose wait certgen 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 2: start MTAs sequentially.  Bringing them up in parallel hits a
# Docker Desktop IPAM race on macOS that surfaces as
# "failed to set up container networking: Address already in use" on a
# subset of services.  A 2-second gap between starts is enough to dodge it.
for svc in herold postfix james mutt-client snail-client; do
    compose_up "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    sleep 2
done

# Wait for herold's health check (up to 120s via retries=24 * interval=5s).
echo "interop: waiting for herold health check"
compose_up --wait herold \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 3: run setup services (user/domain creation).
compose_up james-setup \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Wait for setup services to complete.
echo "interop: waiting for setup services to complete"
docker compose wait james-setup 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Wait for each MTA to accept connections on port 25.  Polls up to 90s per
# service via a one-shot alpine container attached to the interop network.
NETWORK_NAME="$(docker compose ps --format '{{.Name}}' | head -1 | xargs -I{} docker inspect {} --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null || echo interop_interop)"
wait_port() {
    local target_host="$1"
    local target_port="$2"
    local label="$3"
    local max_attempts="${4:-45}"
    echo "interop: waiting for ${label} to accept on ${target_host}:${target_port}"
    for i in $(seq 1 "${max_attempts}"); do
        if docker run --rm --network "${NETWORK_NAME}" alpine:3.20 \
                sh -c "nc -z -w1 ${target_host} ${target_port}" >/dev/null 2>&1; then
            echo "interop: ${label} ready (attempt ${i})"
            return 0
        fi
        sleep 2
    done
    echo "interop: WARNING ${label} did not start listening on ${target_host}:${target_port} within $((max_attempts * 2))s"
    return 1
}
wait_port postfix 25 "postfix" 180 || true
wait_port james 25 "james" 90 || true

# Define collector now; we will run it in the trap to catch runtime logs.
collect_log() {
    local svc="$1"
    docker compose logs "${svc}" 2>/dev/null > "${LOGS_DIR}/${svc}.log" || true
}
collect_all_logs() {
    collect_log coredns
    collect_log certgen
    collect_log herold
    collect_log postfix
    collect_log james
    collect_log james-setup
    collect_log mutt-client
    collect_log snail-client
}

# Run the pytest runner in the 'runner' profile.
PYTEST_EXIT=0
docker compose \
    --profile runner \
    run --rm \
    -e RUN_ID="${RUN_ID}" \
    -e BULK_N="${BULK_N}" \
    runner \
    pytest -v --tb=short \
        --junitxml=/artifacts/${RUN_ID}/junit.xml \
        ${BULK_MARKER:--m "not bulk"} \
        scenarios/ \
    2>&1 | tee "${LOGS_DIR}/pytest.log" \
    || PYTEST_EXIT=$?

# The runner mounts ./logs as /artifacts, so junit.xml lands in ${LOGS_DIR}.
ln -sfn "${RUN_ID}" "${SCRIPT_DIR}/logs/latest"

# Capture runtime logs from every container before tear-down.
collect_all_logs

# Tear down.
docker compose --profile runner down --remove-orphans --volumes \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

echo ""
echo "interop: run complete (exit=${PYTEST_EXIT})"
echo "interop: logs at ${LOGS_DIR}"
echo "interop: junit at ${LOGS_DIR}/junit.xml"

exit "${PYTEST_EXIT}"
