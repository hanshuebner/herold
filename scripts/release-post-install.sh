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
# Installs as a single all-or-nothing unit rather than file-by-file, so
# an archive with one corrupted plugin binary cannot leave a mixed set
# behind (part of the bug this script fixes: a per-file install left
# already-installed plugins in place even when a later file in the
# same archive failed verification):
#
#   1. Verify every plugins/<name> binary's sha256 against
#      plugins/MANIFEST. Nothing is written yet, so a failure here
#      leaves $HEROLD_PLUGIN_DIR completely untouched, including
#      binaries from a previous, still-valid install.
#   2. Stage every verified binary into a temp directory created next
#      to (a sibling of) $HEROLD_PLUGIN_DIR -- same filesystem, so the
#      rename in step 3 is atomic. A failure here (disk full,
#      permission denied) also leaves $HEROLD_PLUGIN_DIR untouched;
#      the temp directory is removed on any exit.
#   3. Only once every binary is verified and staged, rename each one
#      into place. A reader of $HEROLD_PLUGIN_DIR never observes a
#      partially-written binary (rename is atomic) or a partially
#      upgraded set (every file that needed staging already passed
#      verification before any rename starts).
#
# POSIX sh only -- Debian's /bin/sh is dash, no bash-isms.

set -eu

RELEASE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PLUGIN_DIR=${HEROLD_PLUGIN_DIR:-/usr/local/lib/herold/plugins}
MANIFEST="$RELEASE_DIR/plugins/MANIFEST"
STAGE_DIR=""

cleanup() {
    [ -n "$STAGE_DIR" ] && rm -rf "$STAGE_DIR"
}
trap cleanup EXIT

if [ ! -d "$RELEASE_DIR/plugins" ]; then
    echo "release-post-install: no plugins/ directory in $RELEASE_DIR, nothing to install"
    exit 0
fi

if [ ! -f "$MANIFEST" ]; then
    echo "release-post-install: $MANIFEST is missing" >&2
    exit 1
fi

names=""
for src in "$RELEASE_DIR"/plugins/*; do
    name=$(basename "$src")
    [ "$name" = "MANIFEST" ] && continue
    [ -f "$src" ] || continue
    names="$names $name"
done

if [ -z "$names" ]; then
    echo "release-post-install: plugins/ has no binaries to install"
    exit 0
fi

# Step 1: verify every binary before touching $PLUGIN_DIR at all.
for name in $names; do
    src="$RELEASE_DIR/plugins/$name"
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
done
echo "release-post-install: all plugin binaries verified"

mkdir -p "$PLUGIN_DIR"

# Step 2: stage every verified binary next to the target.
STAGE_DIR=$(mktemp -d "${PLUGIN_DIR%/}.stage.XXXXXX")
for name in $names; do
    install -m 0755 "$RELEASE_DIR/plugins/$name" "$STAGE_DIR/$name"
done

# Step 3: every binary verified and staged -- install as a unit.
for name in $names; do
    mv -f "$STAGE_DIR/$name" "$PLUGIN_DIR/$name"
    echo "release-post-install: installed $name -> $PLUGIN_DIR/$name"
done
