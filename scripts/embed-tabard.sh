#!/usr/bin/env bash
set -euo pipefail

# Copies the tabard SPA build artefacts into the herold module's
# embedded dist directory. Run this before `go build ./cmd/herold`
# to bake the current tabard build into the binary
# (REQ-DEPLOY-COLOC-01..03).
#
# Default expects /Users/hans/tabard/apps/suite/dist; override via:
#   TABARD_DIST=/path/to/tabard/dist scripts/embed-tabard.sh

TABARD_DIST="${TABARD_DIST:-/Users/hans/tabard/apps/suite/dist}"
HEROLD_EMBED="$(cd "$(dirname "$0")"/.. && pwd)/internal/tabardspa/dist"

if [ ! -d "$TABARD_DIST" ]; then
    echo "tabard dist not found at $TABARD_DIST" >&2
    exit 1
fi
if [ ! -f "$TABARD_DIST/index.html" ]; then
    echo "$TABARD_DIST/index.html missing -- did the tabard build complete?" >&2
    exit 1
fi
if [ ! -d "$HEROLD_EMBED" ]; then
    echo "herold embed dir not found at $HEROLD_EMBED" >&2
    exit 1
fi

# Wipe existing embedded dist except the .gitkeep marker so the dir
# survives in git history.
find "$HEROLD_EMBED" -mindepth 1 ! -name '.gitkeep' -exec rm -rf {} + 2>/dev/null || true

# Copy the new dist contents.
cp -R "$TABARD_DIST"/. "$HEROLD_EMBED"/

echo "tabard SPA embedded from $TABARD_DIST into $HEROLD_EMBED"
