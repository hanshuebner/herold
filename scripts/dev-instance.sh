#!/usr/bin/env bash
# scripts/dev-instance.sh
#
# Spin up a per-agent ephemeral herold instance on kernel-picked
# ports so subagent puppeteer flows do not collide with the user's
# manual session on 8080 / 5173.
#
# The instance lives in a fresh tempdir under $HEROLD_INSTANCES_DIR
# (default /tmp/herold-instances/), is seeded by re-running the
# bootstrap CLI and a small set of admin REST calls, and is torn
# down by `dev-instance.sh stop <id>` (or by the EXIT trap when the
# `start` invocation is killed).
#
# Subcommands:
#   start [--detach]
#       Provision a fresh instance. Foreground mode (default) blocks
#       until SIGINT/SIGTERM, then tears down. --detach forks the
#       supervisor and prints a state file path before exiting.
#
#       On success, the foreground caller sees these lines on stdout
#       (one key=value per line, in this order, before any tail-style
#       log forwarding):
#
#           INSTANCE=<id>
#           STATE_DIR=<absolute path>
#           BACKEND_URL=http://127.0.0.1:<port>
#           ADMIN_URL=http://127.0.0.1:<port>
#           SUITE_URL=http://127.0.0.1:<port>
#           IMAP_ADDR=127.0.0.1:<port>
#           SMTP_ADDR=127.0.0.1:<port>
#           SMTP_SUBMISSION_ADDR=127.0.0.1:<port>
#
#       After that line block the script keeps running (so the EXIT
#       trap can clean up); kill the script to tear down.
#
#   stop <id>
#       Read the state file, SIGTERM both pids (vite, herold), wait
#       up to 5 s, then SIGKILL stragglers and rm -rf the tempdir.
#
#   list
#       One line per instance: <id>  <suite_url>  <backend_url>  <age>
#
#   gc
#       Sweep $HEROLD_INSTANCES_DIR for tempdirs whose pids are dead
#       and remove them.
#
# Reserved ports: 8080 and 5173 are reserved for the user's manual
# testing session. This script never binds them — it relies on
# herold's port=0 + port_report_file feature and Vite's --port 0
# kernel-picked allocation.
#
# Requires: bash 4+, jq, lsof, curl. Pure POSIX-friendly tooling
# otherwise. Assumes the herold binary at $HEROLD_BIN (default
# ./bin/herold relative to the repo root) is freshly built.

set -euo pipefail

# ── configuration knobs ──────────────────────────────────────────────
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTANCES_DIR="${HEROLD_INSTANCES_DIR:-/tmp/herold-instances}"
HEROLD_BIN="${HEROLD_BIN:-$REPO_ROOT/bin/herold}"
SEED_PASSWORD="${HEROLD_SEED_PASSWORD:-testpass123...}"
SEED_DOMAIN="${HEROLD_SEED_DOMAIN:-example.local}"
# Principals to provision: "<localpart>:<flag>" where flag is "admin"
# or "user". The first listed admin is created via `bootstrap`; the
# rest go through `principal create`.
SEED_PRINCIPALS=(
    "admin:admin"
    "alice:user"
    "filip:user"
    "bob:user"
)
READY_TIMEOUT="${HEROLD_READY_TIMEOUT:-30}"

# ── small utilities ──────────────────────────────────────────────────
die() { echo "dev-instance: $*" >&2; exit 1; }
log() { echo "dev-instance: $*" >&2; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

new_instance_id() {
    # 12 hex chars of /dev/urandom — collision-resistant per host
    head -c 6 /dev/urandom | xxd -p
}

state_file_for() {
    printf '%s/%s/.state\n' "$INSTANCES_DIR" "$1"
}

# wait_for_file PATH TIMEOUT_SECONDS — polls until the file exists.
wait_for_file() {
    local path="$1" deadline=$(( $(date +%s) + ${2:-30} ))
    while [ ! -s "$path" ]; do
        [ "$(date +%s)" -ge "$deadline" ] && return 1
        sleep 0.1
    done
    return 0
}

# wait_for_http URL TIMEOUT_SECONDS — polls until URL responds with
# any HTTP status (i.e., the listener is accepting connections).
wait_for_http() {
    local url="$1" deadline=$(( $(date +%s) + ${2:-30} ))
    while ! curl -s -o /dev/null -w '%{http_code}' "$url" 2>/dev/null | grep -q '^[0-9][0-9][0-9]$'; do
        [ "$(date +%s)" -ge "$deadline" ] && return 1
        sleep 0.2
    done
    return 0
}

# read_port_from_report STATE_DIR LISTENER_NAME — extract a single
# `address` field from the herold port_report_file by listener name.
read_port_from_report() {
    local report="$1/ports.toml" name="$2"
    [ -s "$report" ] || die "port report file missing: $report"
    awk -v name="$name" '
        /^\[\[listener\]\]/ { in_block=1; cur_name=""; cur_addr=""; next }
        in_block && /^name *= */ { sub(/^name *= */, ""); gsub(/"/, ""); cur_name=$0 }
        in_block && /^address *= */ {
            sub(/^address *= */, ""); gsub(/"/, ""); cur_addr=$0
            if (cur_name == name) { print cur_addr; exit 0 }
        }
    ' "$report"
}

addr_to_url() {
    # Convert "127.0.0.1:34521" → "http://127.0.0.1:34521"
    printf 'http://%s\n' "$1"
}

# ── seeding ──────────────────────────────────────────────────────────

# Render system.toml into TARGET_DIR, with all listener ports = 0 and
# port_report_file pointing at TARGET_DIR/ports.toml.
write_system_toml() {
    local dir="$1"
    local cert="$dir/data/admin.crt"
    local key="$dir/data/admin.key"
    cat > "$dir/system.toml" <<EOF
# Generated by scripts/dev-instance.sh — DO NOT EDIT BY HAND.
# Per-instance ephemeral config; tempdir is rm -rf'd on teardown.

[server]
hostname = "mail.example.local"
data_dir = "$dir/data"
shutdown_grace = "1s"
port_report_file = "$dir/ports.toml"

[server.admin_tls]
source = "file"
cert_file = "$cert"
key_file  = "$key"

[server.storage]
backend = "sqlite"

[server.storage.sqlite]
path = "$dir/data/herold.sqlite"

[[listener]]
name = "smtp-relay"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "none"

[[listener]]
name = "smtp-submission"
address = "127.0.0.1:0"
protocol = "smtp-submission"
tls = "starttls"
auth_required = true
cert_file = "$cert"
key_file  = "$key"

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = "$cert"
key_file  = "$key"

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

# Herold serves only API surfaces in dev-instance mode; the SPA is
# served by the Vite dev server on \$SUITE_URL. We leave asset_dir
# unset so herold falls back to the binary's embedded SPA — agents
# never load BACKEND_URL/ in the browser, so the placeholder is fine.
[server.suite]
enabled = true

[observability]
log_format = "json"
log_level = "info"
metrics_bind = "127.0.0.1:0"
EOF
}

# Seed the domain + non-admin principals via the admin REST surface.
# Caller bootstrapped the admin principal beforehand and passes the
# resulting API key.
seed_instance() {
    local dir="$1" backend_url="$2" api_key="$3"

    # `herold bootstrap` already created admin@$SEED_DOMAIN, which
    # implicitly registered $SEED_DOMAIN itself. Calling `domain add`
    # afterwards races against the auto-create and surfaces a leaky
    # 409 with the raw SQLite "UNIQUE constraint failed: domains.name
    # (1555)" string, so we just skip it.

    for spec in "${SEED_PRINCIPALS[@]}"; do
        local local_part="${spec%%:*}" flag="${spec##*:}"
        # admin was already created via bootstrap.
        [ "$local_part" = "admin" ] && continue
        log "creating principal $local_part@$SEED_DOMAIN ($flag)"
        HEROLD_API_KEY="$api_key" "$HEROLD_BIN" principal create \
            "$local_part@$SEED_DOMAIN" \
            --password "$SEED_PASSWORD" \
            --server-url "$admin_url" \
            --system-config "$dir/system.toml" \
            --quiet \
            >/dev/null 2>"$dir/logs/principal-$local_part.err" \
            || { cat "$dir/logs/principal-$local_part.err" >&2; die "principal create $local_part failed"; }
    done
}

# ── start subcommand ─────────────────────────────────────────────────

cmd_start() {
    local detach=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --detach) detach=1 ;;
            -h|--help) sed -n '/^# Subcommands:/,/^# Reserved ports:/p' "$0" >&2; exit 0 ;;
            *) die "start: unknown flag: $1" ;;
        esac
        shift
    done

    require_cmd jq
    require_cmd curl
    require_cmd lsof
    [ -x "$HEROLD_BIN" ] || die "herold binary not found at $HEROLD_BIN — run 'make build-server' first"

    mkdir -p "$INSTANCES_DIR"
    local id; id=$(new_instance_id)
    local dir="$INSTANCES_DIR/$id"
    mkdir -p "$dir/data" "$dir/logs"

    # Self-signed cert for the listeners that demand one.
    "$REPO_ROOT/scripts/make-self-signed-cert.sh" "$dir/data" "mail.example.local" \
        >>"$dir/logs/cert.log" 2>&1 \
        || die "make-self-signed-cert.sh failed; see $dir/logs/cert.log"

    write_system_toml "$dir"

    # Bootstrap the admin BEFORE starting the server, since
    # `herold bootstrap` opens the store directly and a running
    # server would race on the SQLite write lock. bootstrap also
    # runs migrations on first open, so the data dir is fully
    # initialised by the time we start the server.
    log "bootstrapping admin@$SEED_DOMAIN"
    "$HEROLD_BIN" bootstrap \
        --system-config "$dir/system.toml" \
        --email "admin@$SEED_DOMAIN" \
        --password "$SEED_PASSWORD" \
        --save-credentials=false \
        >"$dir/bootstrap.out" 2>"$dir/bootstrap.err" \
        || die "bootstrap failed; see $dir/bootstrap.err"
    # Bootstrap prints a human-readable block; the api_key line looks
    # like "  api_key: hk_<token>" (two-space indent, single colon).
    local api_key
    api_key=$(awk -F': ' '/^[[:space:]]*api_key:/ { print $2; exit }' "$dir/bootstrap.out")
    [ -n "$api_key" ] || die "bootstrap output did not include api_key (see $dir/bootstrap.out)"
    printf '%s\n' "$api_key" > "$dir/api-key.txt"
    chmod 600 "$dir/api-key.txt"

    # Start the herold backend in the background. Its port_report_file
    # tells us which ports the kernel picked.
    log "starting herold backend (instance $id)"
    "$HEROLD_BIN" server start \
        --system-config "$dir/system.toml" \
        >"$dir/logs/herold.log" 2>&1 &
    local herold_pid=$!
    echo "$herold_pid" > "$dir/.herold.pid"

    if ! wait_for_file "$dir/ports.toml" "$READY_TIMEOUT"; then
        kill "$herold_pid" 2>/dev/null || true
        die "herold did not produce ports.toml within ${READY_TIMEOUT}s; see $dir/logs/herold.log"
    fi

    local public_addr admin_addr imap_addr smtp_addr smtp_sub_addr
    public_addr=$(read_port_from_report "$dir" "public")
    admin_addr=$(read_port_from_report "$dir" "admin")
    imap_addr=$(read_port_from_report "$dir" "imap")
    smtp_addr=$(read_port_from_report "$dir" "smtp-relay")
    smtp_sub_addr=$(read_port_from_report "$dir" "smtp-submission")
    [ -n "$public_addr" ] || die "ports.toml missing public listener"
    [ -n "$admin_addr" ]  || die "ports.toml missing admin listener"

    local backend_url admin_url
    backend_url=$(addr_to_url "$public_addr")
    admin_url=$(addr_to_url "$admin_addr")

    # Server is up — wait for the public listener to actually answer.
    if ! wait_for_http "$backend_url/" 5; then
        kill "$herold_pid" 2>/dev/null || true
        die "public listener at $backend_url did not respond"
    fi

    # Seed: domain + principals (admin already created via bootstrap).
    seed_instance "$dir" "$backend_url" "$api_key"

    # Make sure the workspace has node_modules installed (a no-op
    # for the user's main checkout; mandatory for fresh agent
    # worktrees).
    if [ ! -d "$REPO_ROOT/web/node_modules" ]; then
        log "running pnpm install (no node_modules found in $REPO_ROOT/web)"
        ( cd "$REPO_ROOT/web" && pnpm install --frozen-lockfile ) \
            >"$dir/logs/pnpm-install.log" 2>&1 \
            || die "pnpm install failed; see $dir/logs/pnpm-install.log"
    fi

    # Pick a free port for Vite by asking the kernel for one (small
    # TOCTOU window; vite --strictPort makes the bind fail fast if
    # somebody snipes it before we connect).
    local vite_port
    vite_port=$(python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()") \
        || die "failed to pick a free port for vite"

    # Start Vite for the suite SPA, pointed at the per-instance backend.
    # Bypass the `dev` script wrapper (which prepends `pnpm run
    # bundle-manual` and swallows trailing args) and invoke the vite
    # binary directly via `pnpm --dir ... exec`. We exec in the
    # background without a subshell so $! captures the actual node
    # pid, and we have a single process to SIGTERM later.
    log "starting vite on port $vite_port (HEROLD_URL=$backend_url)"
    HEROLD_URL="$backend_url" \
        pnpm --dir "$REPO_ROOT/web/apps/suite" exec vite --port "$vite_port" --strictPort \
        >"$dir/logs/vite.log" 2>&1 &
    local vite_pid=$!
    echo "$vite_pid" > "$dir/.vite.pid"

    local vite_url="http://localhost:$vite_port"
    if ! wait_for_http "$vite_url/" "$READY_TIMEOUT"; then
        kill "$vite_pid" "$herold_pid" 2>/dev/null || true
        die "vite did not respond on $vite_url within ${READY_TIMEOUT}s; see $dir/logs/vite.log"
    fi

    # Persist state for stop/list/gc.
    cat > "$dir/.state" <<EOF
INSTANCE_ID=$id
HEROLD_PID=$herold_pid
VITE_PID=$vite_pid
STATE_DIR=$dir
BACKEND_URL=$backend_url
ADMIN_URL=$admin_url
SUITE_URL=$vite_url
IMAP_ADDR=$imap_addr
SMTP_ADDR=$smtp_addr
SMTP_SUBMISSION_ADDR=$smtp_sub_addr
STARTED_AT=$(date +%s)
EOF

    # Print machine-readable URLs to stdout (the contract the
    # subagent reads).
    cat <<EOF
INSTANCE=$id
STATE_DIR=$dir
BACKEND_URL=$backend_url
ADMIN_URL=$admin_url
SUITE_URL=$vite_url
IMAP_ADDR=$imap_addr
SMTP_ADDR=$smtp_addr
SMTP_SUBMISSION_ADDR=$smtp_sub_addr
EOF

    if [ "$detach" = "1" ]; then
        log "instance $id detached; supervisor pid $$ exiting"
        # Disown the children so they survive after this script exits.
        disown "$herold_pid" "$vite_pid" 2>/dev/null || true
        exit 0
    fi

    # Foreground mode: register cleanup, then block.
    cleanup() {
        log "tearing down instance $id"
        kill_tree "$vite_pid" TERM
        kill_tree "$herold_pid" TERM
        sleep 1
        kill_tree "$vite_pid" KILL
        kill_tree "$herold_pid" KILL
        rm -rf "$dir"
    }
    trap cleanup EXIT INT TERM

    log "instance $id ready; SIGINT/SIGTERM to tear down"
    # Keep the script alive without busy-waiting.
    wait "$herold_pid" "$vite_pid"
}

# ── stop subcommand ──────────────────────────────────────────────────

# kill_tree PID SIG — send SIG to PID and every transitive descendant.
# pnpm execs node which forks Vite which forks esbuild — without a
# tree walk, SIGTERM to the root just orphans the children.
kill_tree() {
    local pid="$1" sig="${2:-TERM}"
    [ -n "$pid" ] || return 0
    local children
    children=$(pgrep -P "$pid" 2>/dev/null || true)
    for child in $children; do
        kill_tree "$child" "$sig"
    done
    kill -"$sig" "$pid" 2>/dev/null || true
}

cmd_stop() {
    local id="${1:-}"
    [ -n "$id" ] || die "stop: missing instance id"
    local dir="$INSTANCES_DIR/$id"
    local state="$dir/.state"
    [ -s "$state" ] || die "stop: no state file at $state"
    # shellcheck disable=SC1090
    source "$state"

    log "stopping instance $INSTANCE_ID"
    kill_tree "$VITE_PID" TERM
    kill_tree "$HEROLD_PID" TERM
    local deadline=$(( $(date +%s) + 5 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        if ! kill -0 "$VITE_PID" 2>/dev/null && ! kill -0 "$HEROLD_PID" 2>/dev/null; then
            break
        fi
        sleep 0.2
    done
    kill_tree "$VITE_PID" KILL
    kill_tree "$HEROLD_PID" KILL
    rm -rf "$dir"
}

# ── list subcommand ──────────────────────────────────────────────────

cmd_list() {
    [ -d "$INSTANCES_DIR" ] || return 0
    local d age now=$(date +%s)
    for d in "$INSTANCES_DIR"/*/; do
        [ -d "$d" ] || continue
        local state="$d.state"
        [ -s "$state" ] || continue
        # shellcheck disable=SC1090
        ( source "$state"
          age=$(( now - STARTED_AT ))
          local alive="dead"
          if kill -0 "$HEROLD_PID" 2>/dev/null && kill -0 "$VITE_PID" 2>/dev/null; then
              alive="up"
          fi
          printf '%s\t%s\t%s\t%s\t%ss\n' "$INSTANCE_ID" "$alive" "$SUITE_URL" "$BACKEND_URL" "$age"
        )
    done
}

# ── gc subcommand ────────────────────────────────────────────────────

cmd_gc() {
    [ -d "$INSTANCES_DIR" ] || return 0
    local d
    for d in "$INSTANCES_DIR"/*/; do
        [ -d "$d" ] || continue
        local state="$d.state"
        if [ ! -s "$state" ]; then
            log "gc: removing $d (no state file)"
            rm -rf "$d"
            continue
        fi
        # shellcheck disable=SC1090
        ( source "$state"
          if ! kill -0 "$HEROLD_PID" 2>/dev/null && ! kill -0 "$VITE_PID" 2>/dev/null; then
              log "gc: removing dead instance $INSTANCE_ID ($d)"
              rm -rf "$d"
          fi
        )
    done
}

# ── dispatch ─────────────────────────────────────────────────────────

case "${1:-}" in
    start) shift; cmd_start "$@" ;;
    stop)  shift; cmd_stop "$@" ;;
    list)  shift; cmd_list "$@" ;;
    gc)    shift; cmd_gc "$@" ;;
    -h|--help|"")
        sed -n '2,/^set -euo/p' "$0" | sed -e 's/^# \{0,1\}//' -e '$d' >&2
        exit 0
        ;;
    *) die "unknown subcommand: $1 (try start|stop|list|gc)" ;;
esac
