// Package protoimg implements the inbound HTML image proxy
// (REQ-SEND-70..78). Authenticated suite users render received HTML
// mail through this proxy so that:
//
//   - tracking pixels and remote-load beacons hit the server's egress
//     IP, not the user's browser;
//   - upstream sees no Cookie, Referer, or per-user identifier; and
//   - over-large or non-image responses are rejected before they reach
//     the user's renderer.
//
// Scope. The handler is mounted at GET /proxy/image on the public HTTP
// listener. It reuses the public-listener session cookie for
// authentication via a SessionResolver callback the parent
// (internal/admin) wires from authsession.ResolveSession.
// The proxy is in-process for v1; REQ-SEND-78 leaves a future plug-in
// path open.
//
// Egress hardening. Every upstream request runs against a dedicated
// http.Client with bounded connect / total deadlines, no redirect
// chasing past 3 hops, and a strict https-only redirect predicate. The
// fetch is streamed into a fixed byte budget; an oversized response
// trips a 413 mid-stream rather than buffering the upstream body to
// completion.
//
// Caching. A shared in-memory LRU cache keyed by sha256(url) holds
// up-to-24-hour entries. The key is the URL alone, not (URL, principal)
// — the cache hit for user B reveals nothing about user A because the
// URL itself is the secret a sender embeds in every recipient's
// rendered HTML. Cache eviction is dual-budget: count-bounded and
// byte-bounded, whichever trips first.
//
// Rate limits. Per-user (200/min default), per-(user, upstream-origin)
// (10/min default), and per-user concurrent (8 default). Limits are
// operator-configurable; rejection produces 429 + Retry-After.
package protoimg
