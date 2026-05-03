#!/usr/bin/env bash
#
# Build the in-tree Svelte workspace under web/ and copy the build
# artefacts into internal/webspa/dist/ so the next `go build` (without
# the `nofrontend` build tag) bakes the SPAs into the herold binary
# via the //go:embed directive in internal/webspa/embed_default.go.
#
# Layout produced inside internal/webspa/dist/:
#   suite/   <- web/apps/suite/dist/* (Svelte consumer SPA)
#   admin/   <- web/apps/admin/dist/* (Svelte operator admin SPA)
#
# This script intentionally fails loudly if pnpm is not installed and
# if either build does not produce an index.html. The build is
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

# 2. Run the manual bundler to produce user.json and admin.json.
#    Output goes to web/apps/suite/public/manual/ (user.json) and is
#    served verbatim by Vite / embedded in the suite dist. The admin
#    bundle is also produced here so the admin SPA build can consume it.
MANUAL_MANIFEST="docs/manual/manifest.toml"
MANUAL_CONTENT="docs/manual"
MANUAL_OUT="web/apps/suite/public/manual"

echo ">>> bundle manual JSONs -> ${MANUAL_OUT}/"
mkdir -p "${MANUAL_OUT}"
node web/packages/manual/scripts/bundle.mjs \
  --manifest "${MANUAL_MANIFEST}" \
  --content-root "${MANUAL_CONTENT}" \
  --out-json "${MANUAL_OUT}"

if [ ! -f "${MANUAL_OUT}/user.json" ]; then
  echo "build-web.sh: ${MANUAL_OUT}/user.json missing after bundle" >&2
  exit 1
fi

# 3. Build the suite SPA. Vite emits to web/apps/suite/dist/.
echo ">>> pnpm --filter @herold/suite build"
pnpm --dir web --filter @herold/suite build

# 4. Mirror the suite build artefact into internal/webspa/dist/suite/.
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

# 5. Build the admin SPA. Vite emits to web/apps/admin/dist/.
echo ">>> pnpm --filter @herold/admin build"
pnpm --dir web --filter @herold/admin build

# 6. Mirror the admin build artefact into internal/webspa/dist/admin/.
ADMIN_SRC="web/apps/admin/dist"
ADMIN_DST="internal/webspa/dist/admin"

if [ ! -f "${ADMIN_SRC}/index.html" ]; then
  echo "build-web.sh: ${ADMIN_SRC}/index.html missing after build" >&2
  exit 1
fi

echo ">>> copy ${ADMIN_SRC}/ -> ${ADMIN_DST}/"
rm -rf "${ADMIN_DST}"
mkdir -p "${ADMIN_DST}"
cp -R "${ADMIN_SRC}/." "${ADMIN_DST}/"

echo "build-web.sh: admin SPA installed at ${ADMIN_DST}/"

# 6. Run the manual bundler in SSR mode. This uses the Markdoc bundler at
#    web/packages/manual/scripts/bundle.mjs with --ssr to produce per-chapter
#    static HTML pages. No Svelte runtime or Vite build is required for SSR;
#    the bundler uses Markdoc's renderers.html() directly.
echo ">>> node web/packages/manual/scripts/bundle.mjs --ssr"
MANUAL_SRC_MANIFEST="docs/manual/manifest.toml"
MANUAL_CONTENT_ROOT="docs/manual"
MANUAL_TMP_JSON="/tmp/herold-manual-build-json-$$"
MANUAL_TMP_SSR="/tmp/herold-manual-build-ssr-$$"
MANUAL_DST="internal/webspa/dist/manual"

mkdir -p "${MANUAL_TMP_JSON}" "${MANUAL_TMP_SSR}"

node web/packages/manual/scripts/bundle.mjs \
  --manifest "${MANUAL_SRC_MANIFEST}" \
  --content-root "${MANUAL_CONTENT_ROOT}" \
  --out-json "${MANUAL_TMP_JSON}" \
  --out-ssr "${MANUAL_TMP_SSR}" \
  --ssr

# Verify that at least the user and admin audience index redirects were produced.
if [ ! -f "${MANUAL_TMP_SSR}/user/index.html" ]; then
  echo "build-web.sh: ${MANUAL_TMP_SSR}/user/index.html missing after manual SSR build" >&2
  exit 1
fi
if [ ! -f "${MANUAL_TMP_SSR}/admin/index.html" ]; then
  echo "build-web.sh: ${MANUAL_TMP_SSR}/admin/index.html missing after manual SSR build" >&2
  exit 1
fi

# 7. Mirror the manual SSR output into internal/webspa/dist/manual/.
#    We place the user/ and admin/ chapter trees directly, plus the shared
#    manual.css and manual.js. The manual/ redirect index goes at the root.
echo ">>> copy manual SSR output -> ${MANUAL_DST}/"
rm -rf "${MANUAL_DST}"
mkdir -p "${MANUAL_DST}"
cp -R "${MANUAL_TMP_SSR}/." "${MANUAL_DST}/"

# The bundler emits manual/index.html as a redirect from the top-level;
# move it to the dist/manual/ root so it is served at /manual/.
if [ -d "${MANUAL_DST}/manual" ]; then
  if [ -f "${MANUAL_DST}/manual/index.html" ]; then
    cp "${MANUAL_DST}/manual/index.html" "${MANUAL_DST}/index.html"
  fi
  rm -rf "${MANUAL_DST}/manual"
fi

# Clean up temp dirs.
rm -rf "${MANUAL_TMP_JSON}" "${MANUAL_TMP_SSR}"

echo "build-web.sh: manual SSR installed at ${MANUAL_DST}/"
