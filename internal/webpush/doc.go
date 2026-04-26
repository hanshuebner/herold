// Package webpush implements the outbound Web Push delivery path
// (RFC 8030 transport, RFC 8291 message encryption, RFC 8292 VAPID
// auth). Three concerns live here:
//
//   - encrypt.go   — RFC 8291 aes128gcm content-encoding envelope
//     (P-256 ECDH + HKDF + AES-128-GCM).
//   - payload.go   — REQ-PROTO-125 privacy-capped JSON builder per
//     JMAP entity kind.
//   - ratelimit.go — REQ-PROTO-126 per-subscription token bucket +
//     daily counter + cooldown.
//   - dispatcher.go — REQ-PROTO-123 change-feed-driven outbound loop:
//     per-subscription type filter, rate-limit, encrypt,
//     VAPID-sign, HTTP POST, retry-by-status-code.
//
// REQ-PROTO-124 (coalescing) and REQ-PROTO-127 (notificationRules
// engine) land in Wave 3.8c; integration hooks are in place
// (BuildPayload returns a coalesce-tag the dispatcher will hand to
// 3.8c, and a TODO(3.8c-coord) marker on the rules consult site).
package webpush
