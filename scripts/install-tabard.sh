#!/bin/sh

# Download the latest tabard suite release and extract it into the
# herold data directory so the public listener serves the consumer SPA
# at "/" without needing a tabard checkout or a custom rebuild.
#
# Used by the README quickstart. Pairs with [server.tabard].asset_dir =
# "./data/tabard" in docs/user/examples/system.toml.quickstart.
#
# Usage:
#   scripts/install-tabard.sh [DATADIR]
#
# DATADIR defaults to "data" (relative to the current working
# directory, matching the quickstart system.toml). The script extracts
# into "$DATADIR/tabard/"; on a re-run it wipes the existing
# "$DATADIR/tabard/" first so an older release does not bleed through.
#
# The release URL is fixed to the GitHub "latest" tag of the
# hanshuebner/tabard suite-dist asset; override TABARD_URL to install
# from a different release or a local tarball file:// URL.

set -eu

DATADIR=${1:-data}
DEST="$DATADIR/tabard"
TABARD_URL=${TABARD_URL:-https://github.com/hanshuebner/tabard/releases/download/latest/tabard-suite-dist.tar.gz}

die () {
    echo "install-tabard: $*" 1>&2
    exit 1
}

command -v curl >/dev/null 2>&1 || die "curl not found in PATH"
command -v tar  >/dev/null 2>&1 || die "tar not found in PATH"

mkdir -p "$DATADIR" || die "could not create $DATADIR"

TMPFILE=$(mktemp -t herold-tabard-dist.XXXXXX) || die "mktemp failed"
trap 'rm -f "$TMPFILE"' EXIT INT TERM

echo "install-tabard: downloading $TABARD_URL"
if ! curl -fsSL "$TABARD_URL" -o "$TMPFILE"; then
    die "download failed: $TABARD_URL"
fi

# Stage into a fresh sibling directory and rename atomically so a
# partial extraction never replaces a working install.
STAGE="$DATADIR/.tabard.new"
rm -rf "$STAGE"
mkdir -p "$STAGE" || die "could not create stage dir $STAGE"

if ! tar -xzf "$TMPFILE" -C "$STAGE"; then
    rm -rf "$STAGE"
    die "tar extraction failed"
fi

if [ ! -f "$STAGE/index.html" ]; then
    rm -rf "$STAGE"
    die "extracted tarball is missing index.html (not a tabard suite-dist build?)"
fi

rm -rf "$DEST"
mv "$STAGE" "$DEST" || die "rename $STAGE -> $DEST failed"

VERSION=""
if [ -f "$DEST/VERSION" ]; then
    VERSION=$(cat "$DEST/VERSION" 2>/dev/null || true)
fi

echo "install-tabard: installed tabard suite into $DEST"
if [ -n "$VERSION" ]; then
    echo "install-tabard: VERSION = $VERSION"
fi
echo
echo "The quickstart system.toml already points [server.tabard].asset_dir"
echo "at ./data/tabard, so no further config changes are required."
echo "Start the server (./herold server start --system-config system.toml)"
echo "and open http://localhost:8080/ in a browser."
