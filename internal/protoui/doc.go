// Package protoui implements the operator-facing web UI for Herold.
//
// Scope: Phase 2 Wave 2.4. The UI is intentionally minimal — server-rendered
// Go templates, HTMX for partial swaps, and a sliver of vanilla JS / Alpine.js
// where vanilla forms cannot express the interaction. There is no SPA
// framework, no build pipeline, and no client-side routing. Total JS budget
// per docs/design/notes/open-questions.md R35 is < 30 KB; the vendored copies of
// HTMX and Alpine sit under static/ and are documented in static/VERSIONS.md.
//
// Mounting. The UI handler is mounted onto the existing admin HTTP listener
// via composition in internal/admin/server.go: a parent ServeMux routes
// /api/v1 to protoadmin and /ui/ to protoui. Composition keeps protoadmin's
// REST surface unchanged; the UI extends — never modifies — the admin REST
// API. Operators may also expose the UI on a dedicated listener via
// cfg.Server.UI.MountMode.
//
// Auth model. The web UI is browser-driven and does not use API keys. A
// session cookie carries an HMAC-signed `<principalID>:<expiresAt>` token
// signed with a per-process server key. The cookie is Secure + HttpOnly +
// SameSite=Strict (Secure is overridable for development only). All
// state-changing form posts (POST/PUT/DELETE/PATCH under /ui) require a
// per-session CSRF token submitted in a hidden form field; the token is
// also stored in a separate cookie for the double-submit pattern. Sliding
// renewal extends the session at each authenticated request.
//
// The UI handlers are thin: most operations call directly into the
// store.Metadata, internal/directory, and internal/directoryoidc packages,
// reusing the same logic the REST surface uses without going through HTTP.
// A small set of helpers for password verification and TOTP rendering is
// duplicated from protoadmin (see the duplication-justification comment on
// each, mirroring the protosend pattern). Two callers does not earn a
// shared httperr/uihelper package; a third caller (REQ-HOOK Part B) will.
package protoui
