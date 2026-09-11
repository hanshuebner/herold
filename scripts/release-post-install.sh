#!/bin/sh
# release-post-install.sh -- installs the first-party plugin binaries
# shipped inside a herold release archive into the plugin directory,
# atomically and alongside the server binary (re #317).
#
# The outpost activate script (netzhansa-infra, roles/deploy) runs this as
# `sh "<release-dir>/post-install.sh"` as root, with the release directory
# as cwd, before it swaps the `current` symlink and restarts the herold
# systemd unit. A non-zero exit here aborts the deploy, so the server
# binary and its plugins always land from the same release.
#
# For every file under plugins/ (except MANIFEST) this verifies the
# file's sha256 against the entry recorded in plugins/MANIFEST, then
# installs it to $HEROLD_PLUGIN_DIR (default
# /usr/local/lib/herold/plugins) via a temp file + rename so a reader
# never observes a partially-written binary.
#
# POSIX sh only -- Debian's /bin/sh is dash, no bash-isms.

set -eu

RELEASE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PLUGIN_DIR=${HEROLD_PLUGIN_DIR:-/usr/local/lib/herold/plugins}
MANIFEST="$RELEASE_DIR/plugins/MANIFEST"

if [ ! -d "$RELEASE_DIR/plugins" ]; then
    echo "release-post-install: no plugins/ directory in $RELEASE_DIR, nothing to install"
    exit 0
fi

if [ ! -f "$MANIFEST" ]; then
    echo "release-post-install: $MANIFEST is missing" >&2
    exit 1
fi

mkdir -p "$PLUGIN_DIR"

for src in "$RELEASE_DIR"/plugins/*; do
    name=$(basename "$src")
    [ "$name" = "MANIFEST" ] && continue
    [ -f "$src" ] || continue

    expected=$(awk -v f="$name" '$2 == f { print $1 }' "$MANIFEST")
    if [ -z "$expected" ]; then
        echo "release-post-install: $name has no entry in plugins/MANIFEST" >&2
        exit 1
    fi

    actual=$(sha256sum "$src" | cut -d ' ' -f1)
    if [ "$actual" != "$expected" ]; then
        echo "release-post-install: $name sha256 mismatch: expected $expected, got $actual" >&2
        exit 1
    fi

    dst="$PLUGIN_DIR/$name"
    tmp="$dst.tmp"
    install -m 0755 "$src" "$tmp"
    mv -f "$tmp" "$dst"
    echo "release-post-install: installed $name -> $dst"
done
