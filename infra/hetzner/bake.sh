#!/usr/bin/env bash
#
# Bake a Hetzner Cloud snapshot for herold CI runners.
#
# Usage:    infra/hetzner/bake.sh <arm64|amd64>
# Output:   stdout JSON  {"arch","snapshot_id","created_at","versions":{...}}
# Cost:     ~EUR 0.01 per bake (5-10 minutes of a CAX11/CPX21).
#
# Prereqs:
#   - hcloud CLI on PATH, "herold-ci" context configured.
#   - An SSH key registered in the herold-ci Hetzner project.
#   - The local matching private key available to ssh (agent or BAKE_SSH_KEY_PATH).
#
# Env knobs:
#   HCLOUD_CONTEXT         hcloud context name (default: herold-ci)
#   HCLOUD_LOCATION        Hetzner location (default: nbg1)
#   HCLOUD_SSH_KEY         SSH key name in the project (default: first key found)
#   BAKE_SSH_KEY_PATH      local private key path (default: ~/.ssh/id_ed25519)
#   SNAPSHOTS_FILE         if set, JSON file to update with the new snapshot id
#   GO_VERSION             pinned Go version (default: 1.25.0)
#   NODE_MAJOR             Node major version (default: 20)
#   PNPM_VERSION           pnpm major (default: 9)
#   FORGEJO_RUNNER_VERSION forgejo-runner release tag (default: v12.10.1)

set -euo pipefail

ARCH="${1:-}"
case "$ARCH" in
  arm64) SERVER_TYPE="${HCLOUD_SERVER_TYPE_ARM64:-cax11}" ;;
  amd64) SERVER_TYPE="${HCLOUD_SERVER_TYPE_AMD64:-cpx22}" ;;
  *) echo "usage: $0 <arm64|amd64>" >&2; exit 2 ;;
esac

# Defensive: any HCLOUD_TOKEN in the env would override --context and target
# the wrong project. Clear it for this process.
unset HCLOUD_TOKEN

HCLOUD_CONTEXT="${HCLOUD_CONTEXT:-herold-ci}"
HCLOUD_LOCATION="${HCLOUD_LOCATION:-nbg1}"
BASE_IMAGE="${HCLOUD_BASE_IMAGE:-ubuntu-24.04}"
SNAPSHOTS_FILE="${SNAPSHOTS_FILE:-}"

GO_VERSION="${GO_VERSION:-1.25.0}"
NODE_MAJOR="${NODE_MAJOR:-20}"
PNPM_VERSION="${PNPM_VERSION:-9}"
FORGEJO_RUNNER_VERSION="${FORGEJO_RUNNER_VERSION:-v12.10.1}"
BAKE_SSH_KEY_PATH="${BAKE_SSH_KEY_PATH:-$HOME/.ssh/id_ed25519}"

HC=(hcloud --context "$HCLOUD_CONTEXT")

log() { printf '[bake %s %s] %s\n' "$ARCH" "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

ssh_keys=$("${HC[@]}" ssh-key list -o columns=name -o noheader)
if [ -z "$ssh_keys" ]; then
  die "no SSH keys in project '$HCLOUD_CONTEXT'. Upload one with:
    hcloud --context $HCLOUD_CONTEXT ssh-key create --name hans-laptop \\
      --public-key-from-file ~/.ssh/id_ed25519.pub"
fi
SSH_KEY_NAME="${HCLOUD_SSH_KEY:-$(echo "$ssh_keys" | head -1)}"
[ -f "$BAKE_SSH_KEY_PATH" ] || die "private key not found at $BAKE_SSH_KEY_PATH (set BAKE_SSH_KEY_PATH)"

TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
SERVER_NAME="herold-bake-${ARCH}-${TIMESTAMP}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SERVER_ID=""
cleanup() {
  local rc=$?
  set +e
  if [ -n "$SERVER_ID" ]; then
    log "cleanup: deleting server $SERVER_ID"
    "${HC[@]}" server delete "$SERVER_ID" >/dev/null 2>&1
  fi
  exit "$rc"
}
trap cleanup EXIT

log "creating $SERVER_TYPE in $HCLOUD_LOCATION from $BASE_IMAGE (ssh-key: $SSH_KEY_NAME)"
SERVER_JSON=$("${HC[@]}" server create \
  --name "$SERVER_NAME" \
  --type "$SERVER_TYPE" \
  --location "$HCLOUD_LOCATION" \
  --image "$BASE_IMAGE" \
  --ssh-key "$SSH_KEY_NAME" \
  --label herold-ci=bake \
  --label arch="$ARCH" \
  -o json)

SERVER_ID=$(printf '%s' "$SERVER_JSON" | python3 -c 'import sys,json; print(json.load(sys.stdin)["server"]["id"])')
SERVER_IP=$(printf '%s' "$SERVER_JSON" | python3 -c 'import sys,json; print(json.load(sys.stdin)["server"]["public_net"]["ipv4"]["ip"])')
log "server $SERVER_ID up at $SERVER_IP"

SSH=(ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
     -o LogLevel=ERROR -o ConnectTimeout=10 -i "$BAKE_SSH_KEY_PATH" "root@$SERVER_IP")

log "waiting for SSH"
for _ in $(seq 1 30); do
  if "${SSH[@]}" true 2>/dev/null; then break; fi
  sleep 5
done
"${SSH[@]}" true || die "SSH never became ready"

log "waiting for cloud-init to finish"
"${SSH[@]}" 'cloud-init status --wait' >/dev/null || die "cloud-init failed"

log "copying provisioner"
scp -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -i "$BAKE_SSH_KEY_PATH" \
    "$SCRIPT_DIR/provision.sh" "root@$SERVER_IP:/root/provision.sh" >/dev/null

log "provisioning (output streamed)"
"${SSH[@]}" \
  "GO_VERSION='$GO_VERSION' NODE_MAJOR='$NODE_MAJOR' PNPM_VERSION='$PNPM_VERSION' \
   FORGEJO_RUNNER_VERSION='$FORGEJO_RUNNER_VERSION' ARCH='$ARCH' \
   bash /root/provision.sh"

log "collecting installed versions"
VERSIONS=$("${SSH[@]}" 'cat /etc/herold-bake-versions.json')

log "shutdown"
"${HC[@]}" server shutdown "$SERVER_ID" >/dev/null
for _ in $(seq 1 30); do
  status=$("${HC[@]}" server describe "$SERVER_ID" -o json \
           | python3 -c 'import sys,json; print(json.load(sys.stdin)["status"])')
  [ "$status" = "off" ] && break
  sleep 3
done
[ "$status" = "off" ] || die "server did not power off"

log "creating snapshot"
SNAPSHOT_DESC="herold-ci runner $ARCH bake $TIMESTAMP go${GO_VERSION} node${NODE_MAJOR} fjr${FORGEJO_RUNNER_VERSION}"
"${HC[@]}" server create-image "$SERVER_ID" \
  --type snapshot \
  --description "$SNAPSHOT_DESC" \
  --label herold-ci=runner \
  --label arch="$ARCH"

# create-image has no --output flag, so look the new snapshot up by label.
# Pick the newest matching image, which is necessarily the one we just made.
SNAPSHOT_ID=$(ARCH="$ARCH" "${HC[@]}" image list --type snapshot \
  --selector "herold-ci=runner,arch=$ARCH" -o json \
  | python3 -c '
import json, os, sys
imgs = json.load(sys.stdin)
if not imgs:
    sys.exit("no snapshot with labels herold-ci=runner arch=" + os.environ["ARCH"])
imgs.sort(key=lambda i: i["created"], reverse=True)
print(imgs[0]["id"])
')
log "snapshot $SNAPSHOT_ID created"

CREATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
SUMMARY=$(ARCH="$ARCH" SNAPSHOT_ID="$SNAPSHOT_ID" CREATED_AT="$CREATED_AT" \
          SERVER_TYPE="$SERVER_TYPE" SNAPSHOT_DESC="$SNAPSHOT_DESC" \
          VERSIONS="$VERSIONS" \
  python3 -c '
import json, os, sys
try:
    versions = json.loads(os.environ["VERSIONS"])
except json.JSONDecodeError as e:
    sys.stderr.write(f"WARNING: versions JSON malformed ({e}); summary will mark _parse_error\n")
    versions = {"_parse_error": str(e), "_raw": os.environ["VERSIONS"][:200]}
print(json.dumps({
  "arch": os.environ["ARCH"],
  "snapshot_id": int(os.environ["SNAPSHOT_ID"]),
  "created_at": os.environ["CREATED_AT"],
  "server_type_baked_on": os.environ["SERVER_TYPE"],
  "description": os.environ["SNAPSHOT_DESC"],
  "versions": versions,
}, indent=2))')
printf '%s\n' "$SUMMARY"

if [ -n "$SNAPSHOTS_FILE" ]; then
  log "updating $SNAPSHOTS_FILE"
  SNAPSHOTS_FILE="$SNAPSHOTS_FILE" ARCH="$ARCH" SUMMARY="$SUMMARY" python3 -c '
import json, os
path = os.environ["SNAPSHOTS_FILE"]
arch = os.environ["ARCH"]
data = {}
if os.path.exists(path):
    try:
        with open(path) as f:
            data = json.load(f)
    except json.JSONDecodeError:
        data = {}
data[arch] = json.loads(os.environ["SUMMARY"])
os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
with open(path, "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
'
fi

log "done"
