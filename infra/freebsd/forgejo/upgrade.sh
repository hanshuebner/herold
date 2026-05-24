#!/bin/sh
#
# Upgrade self-hosted Forgejo from FreeBSD pkg's version (currently
# v14.0.5) to a source-built upstream release (default v15.0.2). The
# pkg lags upstream and lacks a v15 build at the time of writing.
# Run as root from the herold repo root:
#
#     # cd ~/src/herold
#     # sh infra/freebsd/forgejo/upgrade.sh
#
# What it does, each step idempotent:
#
#   1. Verify build prerequisites (go, gmake, git, curl, npm).
#   2. pkg lock the forgejo pkg so a later `pkg upgrade` can't
#      clobber the binary we install.
#   3. Backup the SQLite DB to /var/db/forgejo/data/forgejo.db.<ts>.bak.
#   4. Clone the forgejo source to /var/tmp/forgejo-src (kept across
#      runs; subsequent runs just fetch + checkout the new tag).
#   5. Compare the installed binary's version against the target;
#      exit early if already up-to-date.
#   6. `gmake build TAGS="bindata sqlite sqlite_unlock_notify pam"`
#      with the same tags FreeBSD's pkg used.
#   7. Stop forgejo, archive the previous binary, install the new
#      one to /usr/local/sbin/forgejo, restart.
#   8. Wait for HTTP /api/v1/version, print the daemon's view.
#   9. Print the rollback recipe in case anything went wrong.
#
# Forgejo runs schema migrations automatically on first start of the
# new binary against an older DB. The DB backup in step 3 is the
# safety net.
#
# Override via env:
#   FORGEJO_VERSION   target tag (default v15.0.2)
#   FORGEJO_SRC       working source dir (default /var/tmp/forgejo-src)

set -eu

FORGEJO_VERSION="${FORGEJO_VERSION:-v15.0.2}"
FORGEJO_REPO="${FORGEJO_REPO:-https://codeberg.org/forgejo/forgejo}"
FORGEJO_SRC="${FORGEJO_SRC:-/var/tmp/forgejo-src}"
FORGEJO_BIN="/usr/local/sbin/forgejo"
FORGEJO_DB="/var/db/forgejo/data/forgejo.db"
BUILD_TAGS="bindata sqlite sqlite_unlock_notify pam"
TS=$(date +%Y%m%d-%H%M%S)

log() { printf '[forgejo-upgrade %s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo sh $0)"

# --- 1. prereqs ---------------------------------------------------------------
# Forgejo's Makefile is GNU-make. On FreeBSD use gmake (`pkg install gmake`).
# The build also pulls Node deps for the frontend before going-binary'ing it,
# so node + npm have to be present.
missing=""
for cmd in go gmake git curl node npm; do
    command -v "$cmd" >/dev/null 2>&1 || missing="$missing $cmd"
done
if [ -n "$missing" ]; then
    die "missing build tools:$missing - pkg install go gmake git curl node npm"
fi
log "tools OK: go $(go version | awk '{print $3}'); node $(node --version); gmake $(gmake --version | head -1 | awk '{print $NF}')"

# --- 2. pkg lock --------------------------------------------------------------
if pkg query '%n %?k' forgejo 2>/dev/null | grep -q '^forgejo 1$'; then
    log "forgejo pkg already locked"
else
    log "locking forgejo pkg so pkg upgrade does not overwrite the source build"
    pkg lock -y forgejo
fi

# --- 3. backup the DB ---------------------------------------------------------
if [ -f "$FORGEJO_DB" ]; then
    DB_BAK="${FORGEJO_DB}.${TS}.bak"
    log "backing up SQLite DB to $DB_BAK"
    install -m 0640 -o git -g git "$FORGEJO_DB" "$DB_BAK"
fi

# --- 4. clone or fetch the forgejo source -------------------------------------
if [ ! -d "$FORGEJO_SRC/.git" ]; then
    log "cloning forgejo source to $FORGEJO_SRC (this is a few hundred MB)"
    rm -rf "$FORGEJO_SRC"
    git clone --depth 1 --branch "$FORGEJO_VERSION" "$FORGEJO_REPO" "$FORGEJO_SRC"
else
    log "fetching $FORGEJO_VERSION into existing checkout"
    cd "$FORGEJO_SRC"
    git fetch --tags --depth 1 origin "$FORGEJO_VERSION" 2>/dev/null \
        || git fetch --tags origin
    git checkout -q "$FORGEJO_VERSION"
fi
cd "$FORGEJO_SRC"

# --- 5. early exit if already at target ---------------------------------------
target=${FORGEJO_VERSION#v}
if [ -x "$FORGEJO_BIN" ]; then
    current=$("$FORGEJO_BIN" --version 2>&1 | head -1 | awk '{print $3}')
    if [ "$current" = "$target" ]; then
        log "forgejo binary already at $target; skipping rebuild + swap"
        log "(remove $FORGEJO_SRC and re-run if you want to force a rebuild)"
        exit 0
    fi
    log "current installed: $current; target: $target"
fi

# --- 6. build -----------------------------------------------------------------
log "building forgejo $FORGEJO_VERSION (typically 5-15 min on a small VM)"
log "  source: $FORGEJO_SRC"
log "  tags:   $BUILD_TAGS"
gmake clean >/dev/null 2>&1 || true
TAGS="$BUILD_TAGS" gmake build

[ -x "./forgejo" ] || die "build produced no ./forgejo binary"
NEW_VERSION=$(./forgejo --version 2>&1 | head -1 | awk '{print $3}')
log "built forgejo $NEW_VERSION"

# --- 7. swap + restart --------------------------------------------------------
log "stopping forgejo service"
service forgejo stop 2>&1 || true
sleep 2

if [ -e "$FORGEJO_BIN" ]; then
    BIN_BAK="${FORGEJO_BIN}.bak.${TS}"
    log "archiving previous binary as $BIN_BAK"
    cp -p "$FORGEJO_BIN" "$BIN_BAK"
fi
install -m 0755 -o root -g wheel ./forgejo "$FORGEJO_BIN"
log "installed $FORGEJO_BIN ($($FORGEJO_BIN --version | head -1 | awk '{print $3}'))"

log "starting forgejo (will run schema migrations automatically)"
service forgejo start

# --- 8. wait for daemon to be ready -------------------------------------------
log "waiting for HTTP /api/v1/version"
ready=0
for i in $(seq 1 90); do
    if curl -sf -o /dev/null -m 2 http://127.0.0.1:3001/api/v1/version; then
        log "forgejo HTTP responsive after ${i}s"
        ready=1
        break
    fi
    sleep 1
done
[ "$ready" = 1 ] || die "forgejo did not come up within 90s; check /var/log/daemon.log"

log "version reported by daemon: $(curl -sS http://127.0.0.1:3001/api/v1/version)"

# --- 9. rollback recipe -------------------------------------------------------
cat >&2 <<EOF

[forgejo-upgrade] done.

Rollback (if migrations or new build misbehave):
    sudo service forgejo stop
    sudo cp '${BIN_BAK:-/usr/local/sbin/forgejo.bak.<ts>}' '$FORGEJO_BIN'
    sudo cp '${DB_BAK:-$FORGEJO_DB.<ts>.bak}' '$FORGEJO_DB'
    sudo service forgejo start

To later return to the pkg-managed forgejo:
    sudo pkg unlock forgejo
    sudo pkg install -f forgejo

EOF
