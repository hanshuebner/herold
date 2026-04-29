// Package linkpreview fetches and parses HTML metadata for outbound URL
// references found in chat message bodies. The output is a small struct
// (title, description, image, site name, canonical URL) that the chat
// surface attaches to a Message so downstream clients can render a
// Google-Chat-style preview card without re-fetching the page.
//
// Trust posture
//
// Inputs come from end-user message text. Every fetch must therefore be
// hardened against the standard server-side request forgery (SSRF) and
// resource-exhaustion attacks:
//
//   - Only http and https schemes are accepted.
//   - The DNS resolution is gated by netguard.CheckHost, and the dialer
//     itself installs netguard.ControlContext so a redirect can't slip
//     through to a private address.
//   - Response body is capped at MaxBodyBytes; reads beyond the cap are
//     truncated and the partial body is parsed.
//   - Total fetch time (connect + headers + body) is bounded by a context
//     deadline derived from FetchTimeout.
//   - Only text/html content types are parsed; everything else is
//     dropped without parsing the body.
//   - Up to MaxRedirects redirects; each redirect re-runs the netguard
//     check on the new target.
//
// Off-host caching is intentionally NOT implemented in this package; the
// chat dispatcher stores the parsed Preview alongside the Message in
// metadata_json so a redelivered or reloaded message renders the cached
// card without hitting the network again.
package linkpreview
