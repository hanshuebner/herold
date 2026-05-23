#!/bin/sh
#
# Install / re-install the self-hosted Forgejo instance that fronts
# the herold code host at code.netzhansa.com.
#
# Run as root from the herold repo root:
#
#     # cd ~/src/herold
#     # sh infra/freebsd/forgejo/install.sh
#
# What it does (each step a no-op if already in the right state):
#
#   1. pkg install forgejo (sets up the git user, rc.d script, defaults).
#   2. Ensure /var/db/forgejo and /var/log/forgejo exist with the right
#      ownership.
#   3. On a fresh install: generate SECRET_KEY / INTERNAL_TOKEN /
#      JWT_SECRET via `forgejo generate secret`, substitute them into
#      app.ini.template, and write /usr/local/etc/forgejo/app.ini.
#      If app.ini already exists it's left untouched (its secrets stay).
#   4. sysrc forgejo_enable=YES.
#   5. service forgejo start (or restart if already running).
#   6. Bootstrap the admin user 'hanshuebner' with a random password
#      if it doesn't exist yet. Password is printed to stdout - save it.
#
# Apache, DNS, and TLS for code.netzhansa.com are out of scope; the
# operator wires those up separately. Forgejo listens on
# 127.0.0.1:3001 (HTTP) and 0.0.0.0:2222 (Forgejo's built-in SSH).

set -eu

REPO_ROOT="$(pwd)"
TEMPLATE="$REPO_ROOT/infra/freebsd/forgejo/app.ini.template"
APP_INI="/usr/local/etc/forgejo/app.ini"
DATA_DIR="/var/db/forgejo"
LOG_DIR="/var/log/forgejo"
ADMIN_USER="hanshuebner"
ADMIN_EMAIL="hans@huebner.org"
FORGEJO_BIN="/usr/local/bin/forgejo"

log() { printf '[forgejo-install %s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root (try: sudo sh $0)"
[ -r "$TEMPLATE" ] || die "$TEMPLATE not found - run from herold repo root"

# --- 1. pkg install ----------------------------------------------------------

if pkg info -e forgejo >/dev/null 2>&1; then
    log "forgejo already installed: $($FORGEJO_BIN --version 2>/dev/null | head -1)"
else
    log "installing forgejo via pkg"
    ASSUME_ALWAYS_YES=yes pkg install -y forgejo
fi

# Need sudo(8) to run forgejo CLI commands as the git user further down.
if ! command -v sudo >/dev/null 2>&1; then
    log "installing sudo (needed for forgejo CLI as git user)"
    ASSUME_ALWAYS_YES=yes pkg install -y sudo
fi

# --- 2. data + log dirs -------------------------------------------------------

if ! id -u git >/dev/null 2>&1; then
    die "the 'git' user was not created by the forgejo pkg; check pkg install output"
fi

log "ensuring $DATA_DIR and $LOG_DIR exist with git ownership"
mkdir -p "$DATA_DIR/data" "$DATA_DIR/indexers" "$LOG_DIR"
chown -R git:git "$DATA_DIR" "$LOG_DIR"
chmod 750 "$DATA_DIR" "$LOG_DIR"

# --- 3. app.ini ---------------------------------------------------------------

if [ -f "$APP_INI" ]; then
    log "app.ini already exists; leaving its secrets in place"
else
    log "generating fresh secrets and rendering app.ini"
    mkdir -p "$(dirname "$APP_INI")"
    SECRET_KEY=$("$FORGEJO_BIN" generate secret SECRET_KEY)
    INTERNAL_TOKEN=$("$FORGEJO_BIN" generate secret INTERNAL_TOKEN)
    JWT_SECRET=$("$FORGEJO_BIN" generate secret JWT_SECRET)
    sed -e "s|__SECRET_KEY__|$SECRET_KEY|" \
        -e "s|__INTERNAL_TOKEN__|$INTERNAL_TOKEN|" \
        -e "s|__JWT_SECRET__|$JWT_SECRET|" \
        "$TEMPLATE" > "$APP_INI"
    chown root:git "$APP_INI"
    chmod 640 "$APP_INI"
fi

# --- 4. enable + start --------------------------------------------------------

if [ "$(sysrc -n forgejo_enable 2>/dev/null || echo NO)" = "YES" ]; then
    log "rc.conf already has forgejo_enable=YES"
else
    log "enabling forgejo in rc.conf"
    sysrc forgejo_enable=YES >/dev/null
fi

if service forgejo status >/dev/null 2>&1; then
    log "forgejo running; restarting to pick up config changes"
    service forgejo restart
else
    log "starting forgejo"
    service forgejo start
fi

# --- 5. bootstrap admin user --------------------------------------------------

# Give the daemon a moment to open its SQLite db before invoking the CLI.
sleep 3

if sudo -u git "$FORGEJO_BIN" --config "$APP_INI" admin user list 2>/dev/null \
   | awk 'NR > 1 { print $2 }' \
   | grep -qx "$ADMIN_USER"; then
    log "admin user '$ADMIN_USER' already exists"
else
    log "creating admin user '$ADMIN_USER'; the random password is printed below"
    sudo -u git "$FORGEJO_BIN" --config "$APP_INI" admin user create \
        --admin --username "$ADMIN_USER" --email "$ADMIN_EMAIL" --random-password
fi

# --- 6. summary ---------------------------------------------------------------

log "done."
log "Forgejo HTTP:   127.0.0.1:3001 (front via Apache from outside)"
log "Forgejo SSH:    0.0.0.0:2222 (open at the firewall if you want git+ssh)"
log "Next steps:"
log "  - point Apache vhost code.netzhansa.com -> http://127.0.0.1:3001/"
log "  - browse https://code.netzhansa.com/ and sign in as $ADMIN_USER"
log "  - mint a personal access token for the orchestrator under"
log "    Settings -> Applications, drop it into"
log "    /usr/local/etc/herold-runner-orchestrator.env as"
log "    ORCHESTRATOR_CODEBERG_TOKEN (var name unchanged for now)"
