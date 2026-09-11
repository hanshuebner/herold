#!/usr/bin/env bash
# test-release-post-install.sh -- exercises scripts/release-post-install.sh
# against synthetic release archives (re #317).
#
# Covers the three scenarios the atomic-install rewrite is for:
#   1. good set: every plugin binary verifies -- all land, executable,
#      with the expected content.
#   2. one-bad-checksum set: one binary's content does not match
#      plugins/MANIFEST -- the script exits non-zero and the target
#      plugin directory is left completely untouched (no partial
#      install, and a pre-existing install from an earlier good run is
#      not disturbed either).
#   3. re-run idempotence: running the good set twice in a row succeeds
#      both times and leaves the same binaries in place.
#
# Invoked from the deploy job's "package release assets" step so a
# regression in release-post-install.sh's atomicity fails CI before an
# archive reaches outpost.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
POST_INSTALL="$SCRIPT_DIR/release-post-install.sh"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# build_release <dir> -- populates <dir>/plugins/{a,b,c} and a matching
# plugins/MANIFEST for a well-formed archive.
build_release() {
    local dir=$1
    mkdir -p "$dir/plugins"
    printf 'binary-a-content\n' > "$dir/plugins/plugin-a"
    printf 'binary-b-content\n' > "$dir/plugins/plugin-b"
    printf 'binary-c-content\n' > "$dir/plugins/plugin-c"
    ( cd "$dir/plugins" && for f in plugin-a plugin-b plugin-c; do
        sha256sum "$f"
      done ) > "$dir/plugins/MANIFEST"
    cp "$POST_INSTALL" "$dir/post-install.sh"
    chmod +x "$dir/post-install.sh"
}

echo "=== scenario 1: good set ==="
good_release="$WORK/good-release"
good_target="$WORK/good-target"
build_release "$good_release"

HEROLD_PLUGIN_DIR="$good_target" "$good_release/post-install.sh"

for name in plugin-a plugin-b plugin-c; do
    [ -f "$good_target/$name" ] || fail "good set: $name missing from $good_target"
    [ -x "$good_target/$name" ] || fail "good set: $name not executable"
    diff -q "$good_release/plugins/$name" "$good_target/$name" >/dev/null \
        || fail "good set: $name content differs from the release archive"
done
# No leftover staging directories.
stray=$(find "$(dirname "$good_target")" -maxdepth 1 -name "$(basename "$good_target").stage.*" 2>/dev/null || true)
[ -z "$stray" ] || fail "good set: leftover staging directory $stray"
echo "ok: good set installs all plugin binaries"

echo "=== scenario 2: one bad checksum ==="
bad_release="$WORK/bad-release"
bad_target="$WORK/bad-target"
build_release "$bad_release"
# Corrupt plugin-b's content after the manifest was computed, so its
# sha256 no longer matches plugins/MANIFEST.
printf 'corrupted-content\n' > "$bad_release/plugins/plugin-b"

if HEROLD_PLUGIN_DIR="$bad_target" "$bad_release/post-install.sh" 2>"$WORK/bad.log"; then
    fail "one-bad-checksum set: post-install.sh exited 0, expected non-zero"
fi
grep -q "plugin-b sha256 mismatch" "$WORK/bad.log" \
    || fail "one-bad-checksum set: expected sha256 mismatch message, got: $(cat "$WORK/bad.log")"
[ ! -e "$bad_target" ] || fail "one-bad-checksum set: $bad_target was created; target must stay untouched"
echo "ok: one-bad-checksum set leaves the target untouched"

echo "=== scenario 2b: one bad checksum against an existing good install ==="
existing_release="$WORK/existing-release"
existing_target="$WORK/existing-target"
build_release "$existing_release"
HEROLD_PLUGIN_DIR="$existing_target" "$existing_release/post-install.sh"
before_a=$(sha256sum "$existing_target/plugin-a" | cut -d ' ' -f1)

# A subsequent, corrupted release must not disturb the already-installed set.
printf 'corrupted-content\n' > "$existing_release/plugins/plugin-c"
if HEROLD_PLUGIN_DIR="$existing_target" "$existing_release/post-install.sh" 2>"$WORK/existing.log"; then
    fail "existing install: post-install.sh exited 0 on a corrupted re-run, expected non-zero"
fi
for name in plugin-a plugin-b plugin-c; do
    [ -f "$existing_target/$name" ] || fail "existing install: $name disappeared after a failed re-run"
done
after_a=$(sha256sum "$existing_target/plugin-a" | cut -d ' ' -f1)
[ "$before_a" = "$after_a" ] || fail "existing install: plugin-a changed after a failed re-run"
echo "ok: a corrupted archive never disturbs a previously installed set"

echo "=== scenario 3: re-run idempotence ==="
idem_release="$WORK/idem-release"
idem_target="$WORK/idem-target"
build_release "$idem_release"
HEROLD_PLUGIN_DIR="$idem_target" "$idem_release/post-install.sh"
first_sums=$(cd "$idem_target" && sha256sum plugin-a plugin-b plugin-c)
HEROLD_PLUGIN_DIR="$idem_target" "$idem_release/post-install.sh"
second_sums=$(cd "$idem_target" && sha256sum plugin-a plugin-b plugin-c)
[ "$first_sums" = "$second_sums" ] || fail "idempotence: binaries changed across identical re-runs"
echo "ok: re-running against an unchanged archive is idempotent"

echo "all release-post-install.sh scenarios passed"
