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

# Clean up previous compose state. Docker Desktop on macOS sometimes
# leaves the interop bridge in a stale state after compose down, which
# causes "Address already in use" when the next run tries to attach a
# container. Recovery requires:
#   1. compose down (volumes + orphans)
#   2. forcibly removing any container still attached to the bridge
#   3. removing the network itself
#   4. waiting long enough for the daemon to release the bridge veth
clean_compose_state() {
    docker compose down --remove-orphans --volumes 2>/dev/null || true
    docker ps -a --filter network=interop_interop -q 2>/dev/null \
        | xargs -r docker rm -f >/dev/null 2>&1 || true
    docker network rm interop_interop -f 2>/dev/null || true
    docker network prune -f >/dev/null 2>&1 || true
}
clean_compose_state
sleep 5

# Bring services up one at a time. Docker Desktop on macOS has a
# documented IPAM race where parallel container starts hit
# "failed to set up container networking: Address already in use".
# Sequential starts with a settling gap dodge it; on transient
# failures we retry the SAME service without tearing the whole stack
# down (the partial state is fine — compose will start whatever is
# still in Created).
MAX_SVC_RETRIES=6
SVC_RETRY_GAP=4

compose_up_one() {
    local svc="$1"
    local attempt out rc
    for attempt in $(seq 1 "${MAX_SVC_RETRIES}"); do
        out=$(docker compose up -d --no-recreate "${svc}" 2>&1)
        rc=$?
        echo "${out}" | tee -a "${LOGS_DIR}/compose.log"
        # docker compose up returns 0 even when one container fails to
        # start, so we have to inspect both rc and the output for the
        # bridge race. A clean run has no error lines.
        if [ "${rc}" -eq 0 ] && ! echo "${out}" | grep -q "failed to set up container networking"; then
            return 0
        fi
        echo "interop: ${svc} hit bridge race on attempt ${attempt}; sleeping ${SVC_RETRY_GAP}s"
        sleep "${SVC_RETRY_GAP}"
    done
    echo "interop: FATAL ${svc} failed to start after ${MAX_SVC_RETRIES} attempts"
    docker compose ps "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    docker compose logs --tail 40 "${svc}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    return 1
}

# Build / pull images.
docker compose build --quiet herold runner 2>&1 | tee "${LOGS_DIR}/build.log"
docker compose pull --quiet coredns postfix 2>&1 | tee -a "${LOGS_DIR}/build.log" || true

# Phase 1: DNS first. Everything else needs it.
compose_up_one coredns

# Phase 2: certgen (one-shot, exits 0). The MTAs gate on its successful
# exit via `condition: service_completed_successfully` so we have to
# wait for it explicitly before we expect any of them to actually start.
compose_up_one certgen
echo "interop: waiting for certgen to complete"
docker compose wait certgen 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 3: long-running services. Sequential to dodge the IPAM race.
# herold first so the wait_port checks below have something to talk to;
# postfix/james are slow to bring up (postfix's docker-mailserver image
# does its own setup before opening port 25, James's JVM start is
# multi-second).
for svc in herold postfix james mutt-client snail-client; do
    compose_up_one "${svc}" || true
    sleep 2
done

# Phase 4: wait for herold's health check (max ~120s, retries=24 * 5s).
echo "interop: waiting for herold health check"
docker compose up -d --wait herold \
    2>&1 | tee -a "${LOGS_DIR}/compose.log" || true

# Phase 5: james-setup (one-shot user/domain creation).
compose_up_one james-setup
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
    # Dump a snapshot of the container's recent logs and ps state so
    # whoever reads the run.log can see why the service refused to come
    # up — ports unbound usually mean a config error or a startup
    # crash, both of which are visible in the container's stderr.
    docker compose ps "${label}" 2>&1 | tee -a "${LOGS_DIR}/compose.log" || true
    docker compose logs --tail 80 "${label}" 2>&1 \
        | tee -a "${LOGS_DIR}/${label}-startup.log" \
        | sed -n '1,40p' || true
    return 1
}
wait_port postfix 25 "postfix" 180 || true
wait_port james 25 "james" 90 || true

# Wait for the text-mode IMAP client containers to finish their apt-get
# install of mutt / s-nail. We poll for the binary inside each container.
wait_client_ready() {
    local svc="$1"
    local bin="$2"
    local max_attempts="${3:-90}"
    local container="interop-${svc}-1"
    echo "interop: waiting for ${svc} to install ${bin}"
    for i in $(seq 1 "${max_attempts}"); do
        if docker exec "${container}" sh -c "command -v ${bin}" >/dev/null 2>&1; then
            echo "interop: ${svc} ready (attempt ${i})"
            return 0
        fi
        sleep 2
    done
    echo "interop: WARNING ${svc} did not install ${bin} within $((max_attempts * 2))s"
    docker logs "${container}" 2>&1 | tail -20 || true
    return 1
}
wait_client_ready mutt-client mutt 90 || true
wait_client_ready snail-client s-nail 90 || true

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
