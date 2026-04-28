// Package webspa serves herold's bundled single-page web applications
// (REQ-DEPLOY-COLOC-01..05).
//
// Two SPAs share this package:
//
//  1. The end-user suite (mail / calendar / contacts / chat),
//     mounted at "/" on the public listener. Source under
//     web/apps/suite/; built dist copied into dist/suite/ via
//     scripts/build-web.sh.
//  2. The operator admin SPA, mounted at "/admin/" on the admin
//     listener. Source under web/apps/admin/; built dist copied into
//     dist/admin/ via scripts/build-web.sh. The legacy HTMX UI
//     (internal/protoui) was deleted in Phase 3c-iii of the tabard
//     merge plan; /ui/* on the admin listener 308-redirects to
//     /admin/ as a one-release compatibility shim.
//
// Build-tag split (plan section 5):
//
//   - Default builds (no extra tags) embed dist/ via go:embed in
//     embed_default.go. The placeholder index.html files shipped in
//     source control are overwritten when scripts/build-web.sh runs
//     against a freshly-built web/ workspace.
//   - Builds with -tags nofrontend use embed_stub.go which serves a
//     small in-memory placeholder. No pnpm dependency at compile
//     time. Used by Go-only contributors and the "go" CI lane.
//
// Asset-directory overrides (Options.SuiteAssetDir,
// Options.AdminAssetDir) make the handler read every asset from
// disk on every request, for hot-reload during development.
//
// The HTTP handler discriminates cache classes (immutable for
// content-addressed Vite assets; bounded TTL for non-hashed assets
// like favicon.ico; no-cache for index.html), emits the strict
// Content-Security-Policy required by REQ-DEPLOY-COLOC-04, and
// falls back to index.html for unknown non-asset paths so the SPA's
// client-side router decides.
//
// Reserved API prefixes (/api, /jmap, /.well-known, /chat, /proxy,
// /hooks, /login, /logout, /auth, /oidc) are explicitly 404ed by the
// SPA handler as a defensive measure: the public mux's longest-prefix
// routing already keeps them from reaching here in production, but a
// future mount-order regression will not silently start serving the
// SPA for an API path.
package webspa
