#!/usr/bin/env bash
#
# Runs inside the bake VM. Installs the toolchain that herold's CI needs:
# Docker, Go, Node + pnpm, forgejo-runner, pre-commit + pinned linter
# binaries, and preloads the docker images CI pulls.
#
# Invoked by bake.sh. Required env: ARCH (arm64|amd64), GO_VERSION,
# NODE_MAJOR, PNPM_VERSION, FORGEJO_RUNNER_VERSION.
#
# At completion writes /etc/herold-bake-versions.json with the version
# of every component the orchestrator captures for the snapshot
# metadata.

set -euxo pipefail

: "${ARCH:?ARCH (arm64|amd64) required}"
: "${GO_VERSION:?GO_VERSION required}"
: "${NODE_MAJOR:?NODE_MAJOR required}"
: "${PNPM_VERSION:?PNPM_VERSION required}"
: "${FORGEJO_RUNNER_VERSION:?FORGEJO_RUNNER_VERSION required}"

export DEBIAN_FRONTEND=noninteractive

case "$ARCH" in
  arm64) DPKG_ARCH=arm64 ;;
  amd64) DPKG_ARCH=amd64 ;;
  *) echo "unknown ARCH=$ARCH" >&2; exit 2 ;;
esac

#---- base packages -----------------------------------------------------------
apt-get update -y
apt-get install -y --no-install-recommends \
  ca-certificates curl gnupg lsb-release apt-transport-https \
  git make cmake jq build-essential unzip zip \
  python3 python3-pip python3-venv

#---- Docker (rootful, official repo) -----------------------------------------
install -m 0755 -d /etc/apt/keyrings
curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors https://download.docker.com/linux/ubuntu/gpg \
  | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg

. /etc/os-release
echo "deb [arch=$DPKG_ARCH signed-by=/etc/apt/keyrings/docker.gpg] \
https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" \
  > /etc/apt/sources.list.d/docker.list

apt-get update -y
apt-get install -y --no-install-recommends \
  docker-ce docker-ce-cli containerd.io \
  docker-buildx-plugin docker-compose-plugin
systemctl enable docker

#---- Go (pinned, tarball) ----------------------------------------------------
GO_TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz"
curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o "/tmp/${GO_TARBALL}" "https://go.dev/dl/${GO_TARBALL}"
rm -rf /usr/local/go
tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
rm "/tmp/${GO_TARBALL}"
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
cat >/etc/profile.d/go.sh <<'EOF'
export PATH="$PATH:/usr/local/go/bin:/root/go/bin"
EOF
chmod +x /etc/profile.d/go.sh
export PATH="$PATH:/usr/local/go/bin:/root/go/bin"

#---- Node + pnpm (NodeSource + corepack) -------------------------------------
curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -
apt-get install -y --no-install-recommends nodejs
corepack enable
corepack prepare "pnpm@${PNPM_VERSION}" --activate

#---- forgejo-runner (pinned binary) ------------------------------------------
FJR_URL="https://code.forgejo.org/forgejo/runner/releases/download/${FORGEJO_RUNNER_VERSION}/forgejo-runner-${FORGEJO_RUNNER_VERSION#v}-linux-${ARCH}"
curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o /usr/local/bin/forgejo-runner "$FJR_URL"
chmod +x /usr/local/bin/forgejo-runner
# Do NOT enable or start a service. The autoscaler injects per-VM
# registration via cloud-init at spawn time.

#---- Linter toolchain (pre-commit references these) --------------------------
go install golang.org/x/tools/cmd/goimports@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
ln -sf /root/go/bin/goimports /usr/local/bin/goimports
ln -sf /root/go/bin/staticcheck /usr/local/bin/staticcheck

# gitleaks: release binary (gitleaks calls amd64 "x64" in its asset names)
GITLEAKS_VERSION="8.30.1"
case "$ARCH" in
  amd64) GL_ARCH=x64 ;;
  arm64) GL_ARCH=arm64 ;;
esac
GITLEAKS_TARBALL="gitleaks_${GITLEAKS_VERSION}_linux_${GL_ARCH}.tar.gz"
curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o "/tmp/${GITLEAKS_TARBALL}" \
  "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/${GITLEAKS_TARBALL}"
tar -C /usr/local/bin -xzf "/tmp/${GITLEAKS_TARBALL}" gitleaks
rm "/tmp/${GITLEAKS_TARBALL}"
chmod +x /usr/local/bin/gitleaks

# pre-commit in an isolated venv to avoid PEP 668 / system-python collisions.
python3 -m venv /opt/pre-commit
/opt/pre-commit/bin/pip install --no-cache-dir --upgrade pip
/opt/pre-commit/bin/pip install --no-cache-dir pre-commit
ln -sf /opt/pre-commit/bin/pre-commit /usr/local/bin/pre-commit

#---- Android SDK + JDK (amd64 only; for the wetteronline APK build) -----------
# Baked in so CI doesn't re-download ~1 GB of SDK every run. The android job
# only runs on amd64, so skip on arm64 (its build-tools binaries are x86 anyway).
if [ "$ARCH" = "amd64" ]; then
  apt-get install -y --no-install-recommends openjdk-17-jdk-headless
  ANDROID_HOME=/opt/android-sdk
  ANDROID_CLT=12266719   # commandlinetools-linux-<n>_latest.zip
  mkdir -p "$ANDROID_HOME/cmdline-tools"
  curl -fsSL --retry 5 --retry-delay 5 --retry-all-errors -o /tmp/clt.zip \
    "https://dl.google.com/android/repository/commandlinetools-linux-${ANDROID_CLT}_latest.zip"
  unzip -q /tmp/clt.zip -d "$ANDROID_HOME/cmdline-tools"
  mv "$ANDROID_HOME/cmdline-tools/cmdline-tools" "$ANDROID_HOME/cmdline-tools/latest"
  rm /tmp/clt.zip
  # sdkmanager accepts a fixed number of license prompts then exits, so
  # `yes` gets SIGPIPE (exit 141). Under `set -o pipefail` that 141 fails
  # the pipeline even though licensing succeeded; drop pipefail for this
  # one line so the pipeline's exit reflects sdkmanager (still caught by
  # `set -e` on a real error).
  set +o pipefail
  yes | "$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" --sdk_root="$ANDROID_HOME" --licenses >/dev/null
  set -o pipefail
  "$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" --sdk_root="$ANDROID_HOME" \
    "platform-tools" "platforms;android-36" "build-tools;36.0.0" >/dev/null
  cat >/etc/profile.d/android.sh <<'EOF'
export ANDROID_HOME=/opt/android-sdk
export ANDROID_SDK_ROOT=/opt/android-sdk
export PATH="$PATH:/opt/android-sdk/platform-tools:/opt/android-sdk/cmdline-tools/latest/bin"
EOF
  chmod +x /etc/profile.d/android.sh
fi

#---- Preload commonly-used docker images -------------------------------------
docker pull "postgres:16"
docker pull "nats:2-alpine"
docker pull "mailhog/mailhog:latest"

#---- Capture versions for snapshot metadata ----------------------------------
docker_version=$(docker --version | awk '{print $3}' | tr -d ',')
go_version_out=$(/usr/local/go/bin/go version | awk '{print $3}' | sed 's/^go//')
node_version=$(node --version | sed 's/^v//')
pnpm_version=$(corepack pnpm --version 2>/dev/null || pnpm --version 2>/dev/null || echo unknown)
fjr_version=$(/usr/local/bin/forgejo-runner --version 2>/dev/null | head -1 | awk '{print $NF}')
gitleaks_version=$(/usr/local/bin/gitleaks version 2>/dev/null | head -1)
goimports_version=$(/usr/local/go/bin/go version -m /root/go/bin/goimports 2>/dev/null | awk '$1=="mod" {print $3; exit}')
goimports_version="${goimports_version:-unknown}"
staticcheck_version=$(/root/go/bin/staticcheck -version 2>/dev/null || echo unknown)
precommit_version=$(/opt/pre-commit/bin/pre-commit --version 2>/dev/null | awk '{print $2}' || echo unknown)
kernel=$(uname -r)
ubuntu=$(. /etc/os-release && echo "$VERSION_ID")

ubuntu="$ubuntu" kernel="$kernel" docker="$docker_version" \
  go_v="$go_version_out" node_v="$node_version" pnpm_v="$pnpm_version" \
  fjr_v="$fjr_version" gitleaks_v="$gitleaks_version" \
  goimports_v="$goimports_version" staticcheck_v="$staticcheck_version" \
  precommit_v="$precommit_version" \
  python3 -c '
import json, os
def clean(s):
    # Strip control chars that would break downstream JSON parsing.
    return "".join(c for c in s if c.isprintable() or c == " ").strip()
print(json.dumps({
    "ubuntu":         clean(os.environ["ubuntu"]),
    "kernel":         clean(os.environ["kernel"]),
    "docker":         clean(os.environ["docker"]),
    "go":             clean(os.environ["go_v"]),
    "node":           clean(os.environ["node_v"]),
    "pnpm":           clean(os.environ["pnpm_v"]),
    "forgejo_runner": clean(os.environ["fjr_v"]),
    "gitleaks":       clean(os.environ["gitleaks_v"]),
    "goimports":      clean(os.environ["goimports_v"]),
    "staticcheck":    clean(os.environ["staticcheck_v"]),
    "pre_commit":     clean(os.environ["precommit_v"]),
}, indent=2))
' > /etc/herold-bake-versions.json

#---- Cleanup for a smaller snapshot ------------------------------------------
apt-get clean
rm -rf /var/lib/apt/lists/*
rm -rf /root/.cache /tmp/* /var/tmp/*
journalctl --vacuum-size=10M || true
sync

echo "BAKE OK"
