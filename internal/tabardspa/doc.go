// Package tabardspa serves the tabard single-page application static
// assets at the root of herold's public HTTP listener
// (REQ-DEPLOY-COLOC-01..05).
//
// The package owns three responsibilities:
//
//  1. An embedded copy of the tabard build artefacts (HTML / JS / CSS /
//     fonts / images) under dist/. The release-build script
//     scripts/embed-tabard.sh copies the upstream tabard dist into this
//     directory before `go build`; the placeholder index.html shipped
//     in source control documents the expected shape so a fresh
//     checkout still compiles.
//
//  2. A runtime override: when [server.tabard].asset_dir is set, the
//     handler serves from that directory instead of the embedded FS.
//     Operators use this to hot-reload during development; production
//     deployments rely on the embedded copy.
//
//  3. The HTTP handler itself, which discriminates cache classes
//     (immutable for content-addressed Vite assets; bounded TTL for
//     non-hashed assets like favicon.ico; no-cache for index.html),
//     emits the strict Content-Security-Policy required by
//     REQ-DEPLOY-COLOC-04, and falls back to index.html for unknown
//     non-asset paths so the SPA's client-side router decides.
//
// Reserved API prefixes (/api, /jmap, /.well-known, /chat, /proxy,
// /hooks, /login, /logout, /auth, /oidc) are explicitly 404ed by the
// SPA handler as a defensive measure: the public mux's longest-prefix
// routing already keeps them from reaching here in production, but a
// future mount-order regression will not silently start serving the
// SPA for an API path.
package tabardspa
