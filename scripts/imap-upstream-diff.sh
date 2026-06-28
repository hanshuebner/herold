#!/usr/bin/env bash
# imap-upstream-diff.sh -- show upstream go-imap commits and diffs since the
# fork base so a maintainer can decide which parser or robustness fixes to
# cherry-pick into third_party/go-imap.
#
# Reads the base commit from third_party/go-imap/UPSTREAM.
# Clones upstream into a temp dir (or reuses a cached clone in IMAP_CACHE_DIR).
# Prints a one-line log of every commit on the upstream default branch that
# post-dates the base, then the full diff restricted to the paths we keep
# (everything except cmd/, which we deleted from the fork).
#
# The script is read-only: it never modifies the working tree.
# If the network is unavailable the clone step fails with a clear message.
#
# Usage:
#   scripts/imap-upstream-diff.sh
#   IMAP_CACHE_DIR=/tmp/go-imap-cache scripts/imap-upstream-diff.sh
#
# Pipe through `less -R` for pagination.

set -euo pipefail

UPSTREAM_URL="https://github.com/emersion/go-imap"

HEROLD_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
UPSTREAM_FILE="$HEROLD_ROOT/third_party/go-imap/UPSTREAM"

if [[ ! -f "$UPSTREAM_FILE" ]]; then
    echo "imap-upstream-diff: $UPSTREAM_FILE not found" >&2
    exit 1
fi

BASE_COMMIT="$(grep '^commit:' "$UPSTREAM_FILE" | awk '{print $2}')"
if [[ -z "$BASE_COMMIT" ]]; then
    echo "imap-upstream-diff: could not parse commit from $UPSTREAM_FILE" >&2
    exit 1
fi

# Use a persistent cache directory to avoid re-cloning on every run.
CACHE_DIR="${IMAP_CACHE_DIR:-/tmp/go-imap-upstream-cache}"

if [[ -d "$CACHE_DIR/.git" ]]; then
    echo ">> fetching upstream into cached clone at $CACHE_DIR ..."
    if ! git -C "$CACHE_DIR" fetch -q origin 2>/dev/null; then
        echo ""
        echo "imap-upstream-diff: fetch failed -- network unavailable?" >&2
        echo "Cached clone exists; showing commits reachable from the cache." >&2
        echo ""
    fi
else
    echo ">> cloning $UPSTREAM_URL into $CACHE_DIR ..."
    if ! git clone -q "$UPSTREAM_URL" "$CACHE_DIR" 2>/dev/null; then
        echo ""
        echo "imap-upstream-diff: clone failed -- network unavailable?" >&2
        echo "Run with network access, or set IMAP_CACHE_DIR to an existing clone." >&2
        exit 1
    fi
fi

DEFAULT_BRANCH="$(git -C "$CACHE_DIR" symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's|origin/||' || echo master)"

echo ""
echo "=== Upstream commits since base ($BASE_COMMIT) on origin/$DEFAULT_BRANCH ==="
echo ""
git -C "$CACHE_DIR" log --oneline "${BASE_COMMIT}..origin/${DEFAULT_BRANCH}"

echo ""
echo "=== Diff since base (paths: everything except cmd/) ==="
echo ""
# Exclude cmd/ because we deleted it from the fork; it is the only path that
# diverges structurally rather than as a content change.
git -C "$CACHE_DIR" diff "${BASE_COMMIT}..origin/${DEFAULT_BRANCH}" \
    -- ':!cmd/'
