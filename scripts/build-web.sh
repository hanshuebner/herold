#!/usr/bin/env bash
#
# Build the in-tree Svelte workspace under web/ and copy the build
# artefacts into internal/webspa/dist/ so the next `go build` (without
# the `nofrontend` build tag) bakes the SPAs into the herold binary
# via the //go:embed directive in internal/webspa/embed_default.go.
#
# Layout produced inside internal/webspa/dist/:
#   suite/   <- web/apps/suite/dist/* (Svelte consumer SPA)
#   admin/   <- web/apps/admin/dist/* once Phase 2 lands; for now a
#               placeholder index.html shipped in source control.
#
# This script intentionally fails loudly if pnpm is not installed and
# if the suite build does not produce an index.html. The build is
# deterministic given the lockfile: it runs `pnpm install
# --frozen-lockfile` and rejects lockfile drift. There is no fallback
# to npm / yarn.

set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v pnpm >/dev/null 2>&1; then
  echo "build-web.sh: pnpm is required but not installed." >&2
  echo "  see https://pnpm.io/installation, or install via:" >&2
  echo "    npm install -g pnpm@latest-10" >&2
  echo "    corepack enable && corepack use pnpm@latest" >&2
  exit 1
fi

# 1. Install workspace dependencies from the locked manifest. Refuse
#    to drift the lockfile silently: a CI build that needs lockfile
#    edits should be the explicit work of a frontend PR.
echo ">>> pnpm install --frozen-lockfile (in web/)"
pnpm --dir web install --frozen-lockfile

# 2. Build the suite SPA. Vite emits to web/apps/suite/dist/.
echo ">>> pnpm --filter @herold/suite build"
pnpm --dir web --filter @herold/suite build

# 3. Mirror the build artefact into internal/webspa/dist/suite/.
#    Drop the placeholder index.html from source control before the
#    copy so a stale placeholder cannot survive the build. The
#    .gitkeep in dist/ stays untouched.
SUITE_SRC="web/apps/suite/dist"
SUITE_DST="internal/webspa/dist/suite"

if [ ! -f "${SUITE_SRC}/index.html" ]; then
  echo "build-web.sh: ${SUITE_SRC}/index.html missing after build" >&2
  exit 1
fi

echo ">>> copy ${SUITE_SRC}/ -> ${SUITE_DST}/"
rm -rf "${SUITE_DST}"
mkdir -p "${SUITE_DST}"
# Use cp -R so symlinks become real files; the embedded FS does not
# follow symlinks at runtime.
cp -R "${SUITE_SRC}/." "${SUITE_DST}/"

echo "build-web.sh: suite SPA installed at ${SUITE_DST}/"
