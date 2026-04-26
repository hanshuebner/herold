// Package webpush implements the outbound Web Push delivery path
// (RFC 8030 transport, RFC 8291 message encryption, RFC 8292 VAPID
// auth):
//
//   - encrypt.go   — RFC 8291 aes128gcm content-encoding envelope
//     (P-256 ECDH + HKDF + AES-128-GCM).
//   - payload.go   — REQ-PROTO-125 privacy-capped JSON builder per
//     JMAP entity kind, including the inline blob walk that produces
//     the 80-byte mail preview (Wave 3.8c).
//   - ratelimit.go — REQ-PROTO-126 per-subscription token bucket +
//     daily counter + cooldown.
//   - rules.go     — REQ-PROTO-127 notificationRules parser and
//     evaluator. Closed-enum event-type vocabulary; unknown JSON
//     fields preserved verbatim.
//   - dispatcher.go — REQ-PROTO-123/124 change-feed-driven outbound
//     loop: per-subscription type filter, rules evaluation, VAPID
//     rotation filter, rate-limit, coalescing window, encrypt,
//     VAPID-sign, HTTP POST, retry-by-status-code.
package webpush
