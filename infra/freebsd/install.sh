#!/bin/sh
#
# Install / re-install the herold-runner-orchestrator service on a
# FreeBSD host.
#
# Run as root from the herold repo root:
#
#     # cd ~/src/herold
#     # sh infra/freebsd/install.sh
#
# What it does (each step is a no-op if already in the right state):
#
#   1. Ensure `go` is installed (pkg install go if missing).
#   2. Build cmd/herold-runner-orchestrator/ in /tmp.
#   3. Install the binary to /usr/local/bin/.
#   4. Create the _herold-orch service user.
#   5. Create /usr/local/etc/herold-runner-orchestrator.env if missing
#      (template with empty token fields). The script EXITS here on a
#      fresh install so the operator can fill in the tokens, then
#      re-run.
#   6. Install the rc.d script to /usr/local/etc/rc.d/.
#   7. sysrc herold_runner_orchestrator_enable=YES.
#   8. service ... start, or service ... restart if already running.
#
# Re-running after a code change rebuilds and restarts; no state is
# lost. The env file is never overwritten once it has tokens.

set -eu

REPO_ROOT="$(pwd)"
BIN_SRC_DIR="$REPO_ROOT/cmd/herold-runner-orchestrator"
RC_SRC="$REPO_ROOT/infra/freebsd/rc.d/herold_runner_orchestrator"
BIN_DST="/usr/local/bin/herold-runner-orchestrator"
RC_DST="/usr/local/etc/rc.d/herold_runner_orchestrator"
ENV_FILE="/usr/local/etc/herold-runner-orchestrator.env"
SVC_USER="_herold-orch"
LOG_FILE="/var/log/herold-runner-orchestrator.log"

log() { printf '[install %s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# --- preflight ----------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo sh $0)"
[ -d "$BIN_SRC_DIR" ] || die "$BIN_SRC_DIR not found - run from the herold repo root"
[ -r "$RC_SRC" ] || die "$RC_SRC not found - run from the herold repo root"

# --- 1. ensure go ------------------------------------------------------------

if ! command -v go >/dev/null 2>&1; then
    log "go not found; installing via pkg"
    ASSUME_ALWAYS_YES=yes pkg install -y go
fi
log "go: $(go version)"

# --- 2-3. build + install binary ---------------------------------------------

BUILD_TMP="$(mktemp -d -t herold-orch-build.XXXXXX)"
trap 'rm -rf "$BUILD_TMP"' EXIT

log "building (may take a minute on a cold module cache)"
( cd "$BIN_SRC_DIR" && go build -trimpath -o "$BUILD_TMP/herold-runner-orchestrator" . )

log "installing binary to $BIN_DST"
install -m 0755 -o root -g wheel "$BUILD_TMP/herold-runner-orchestrator" "$BIN_DST"

# --- 4. service user ----------------------------------------------------------

if pw user show "$SVC_USER" >/dev/null 2>&1; then
    log "service user $SVC_USER already exists"
else
    log "creating service user $SVC_USER"
    pw user add "$SVC_USER" \
        -d /nonexistent \
        -s /usr/sbin/nologin \
        -c "herold runner orchestrator"
fi

# --- 5. env file --------------------------------------------------------------

if [ ! -e "$ENV_FILE" ]; then
    log "creating env file template at $ENV_FILE"
    umask 077
    cat >"$ENV_FILE" <<'EOF'
# herold-runner-orchestrator credentials. Mode 0600, owned by _herold-orch.
# Edit and add real values, then re-run install.sh.

# Hetzner Cloud Read+Write API token for the herold-ci project.
HCLOUD_TOKEN=

# Forgejo personal access token (e.g. issued by the self-hosted
# instance at code.netzhansa.com). Needs at least read+write
# repository and read+write actions / runner scopes.
ORCHESTRATOR_FORGEJO_TOKEN=
EOF
    chown "$SVC_USER:$SVC_USER" "$ENV_FILE"
    chmod 0600 "$ENV_FILE"
    log "env file template written; populate the two tokens and re-run this script"
    exit 0
fi

# Both tokens must have non-empty values before we try to start.
if ! grep -qE '^HCLOUD_TOKEN=[^[:space:]]+' "$ENV_FILE"; then
    die "$ENV_FILE: HCLOUD_TOKEN is empty - fill it in"
fi
if ! grep -qE '^ORCHESTRATOR_CODEBERG_TOKEN=[^[:space:]]+' "$ENV_FILE"; then
    die "$ENV_FILE: ORCHESTRATOR_CODEBERG_TOKEN is empty - fill it in"
fi

# Tighten ownership/mode in case prior runs left it loose.
chown "$SVC_USER:$SVC_USER" "$ENV_FILE"
chmod 0600 "$ENV_FILE"

# --- 6. rc.d script -----------------------------------------------------------

log "installing rc.d script to $RC_DST"
install -m 0755 -o root -g wheel "$RC_SRC" "$RC_DST"

# --- 7. enable in rc.conf -----------------------------------------------------

if [ "$(sysrc -n herold_runner_orchestrator_enable 2>/dev/null || echo NO)" = "YES" ]; then
    log "rc.conf already has herold_runner_orchestrator_enable=YES"
else
    log "enabling in rc.conf via sysrc"
    sysrc herold_runner_orchestrator_enable=YES >/dev/null
fi

# --- 8. start or restart ------------------------------------------------------

if service herold_runner_orchestrator status >/dev/null 2>&1; then
    log "service running; restarting to pick up new binary"
    service herold_runner_orchestrator restart
else
    log "starting service"
    service herold_runner_orchestrator start
fi

# Brief tail so the operator sees the orchestrator come up before the
# script exits.
sleep 2
if [ -e "$LOG_FILE" ]; then
    log "recent log (tail -20 $LOG_FILE):"
    tail -20 "$LOG_FILE" 2>/dev/null || true
fi

log "done. follow with: tail -F $LOG_FILE"
