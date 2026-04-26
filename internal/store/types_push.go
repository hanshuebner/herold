package store

import "time"

// This file declares the Phase 3 Wave 3.8a entity backing the JMAP
// PushSubscription datatype (REQ-PROTO-120..122 / REQ-PROTO-48
// promoted to phase 1). The schema-side commentary lives in
// internal/storesqlite/migrations/0017_push_subscription.sql; this
// file is the Go-side companion.
//
// Storage strategy. RFC 8620 §7.2 specifies the standard PushSubscription
// shape. Tabard (the consumer suite) layers three extension properties
// on top per REQ-PROTO-121: notificationRules (a JSON blob with an
// open future-extensible schema), quietHours (a small struct), and
// vapidKeyAtRegistration (the VAPID public key the client used at
// subscribe time so herold knows which key pair the client encrypts
// against; rotation invalidates older subscriptions implicitly).
// The notificationRules blob is opaque to herold for storage —
// REQ-PROTO-127's evaluation engine lands in 3.8c; this wave only
// persists and round-trips the bytes.

// PushSubscriptionID identifies one row in the push_subscription
// table. Server-allocated integer; clients see it as a JMAP id (the
// stringified value).
type PushSubscriptionID uint64

// PushSubscription is one push-endpoint registration owned by a
// principal. Fields map 1:1 to the columns in
// migrations/0017_push_subscription.sql. The wire-form (RFC 8620 §7.2)
// keys.p256dh / keys.auth pair lives here as raw byte slices; the JMAP
// serialiser base64url-encodes on output and decodes on input.
type PushSubscription struct {
	// ID is the assigned primary key.
	ID PushSubscriptionID
	// PrincipalID is the owning principal. CASCADE on DELETE.
	PrincipalID PrincipalID
	// DeviceClientID is the RFC 8620 §7.2 client-supplied identifier
	// the client uses to detect duplicate subscriptions across page
	// reloads. Opaque to the server.
	DeviceClientID string
	// URL is the push endpoint the dispatcher POSTs to (e.g.
	// "https://fcm.googleapis.com/fcm/send/...").
	URL string
	// P256DH is the RFC 8291 P-256 ECDH client public key (raw bytes;
	// length 65 for the uncompressed SEC1 form). Combined with Auth it
	// forms the encryption key material the dispatcher uses in 3.8b.
	P256DH []byte
	// Auth is the RFC 8291 16-byte auth secret (raw bytes).
	Auth []byte
	// Expires is the optional advisory cap from RFC 8620 §7.2; nil
	// when the client did not supply one. The dispatcher refuses to
	// push past this instant; the row stays in place until a client
	// destroys it or operator GC removes expired rows.
	Expires *time.Time
	// Types is the list of JMAP datatype names the subscription is
	// interested in (e.g. ["Mailbox", "Email"]). Empty / nil means
	// "all types".
	Types []string
	// VerificationCode is the server-generated 24-byte base64url-
	// encoded nonce minted on create. Per RFC 8620 §7.2 the server
	// sends a verification ping carrying this string; the client must
	// echo it back via /set update before Verified flips to true. The
	// outbound ping itself is 3.8b work — TODO marker in
	// internal/protojmap/push/methods.go.
	VerificationCode string
	// Verified is true once the verification handshake has completed
	// (the client called /set update with the matching
	// verificationCode). The dispatcher refuses to push to
	// unverified subscriptions.
	Verified bool
	// VAPIDKeyAtRegistration is the base64url-encoded VAPID public key
	// the client sent at subscribe time (REQ-PROTO-121). Empty when
	// the client did not supply one (older tabard builds).
	VAPIDKeyAtRegistration string
	// NotificationRulesJSON carries tabard's per-event-type
	// preference set verbatim (REQ-PROTO-121 / tabard
	// 25-push-notifications.md). nil when the client did not supply
	// rules; the dispatcher falls back to the implicit "deliver
	// minimal state-change envelope" path. The 3.8c engine parses
	// this lazily; 3.8a only round-trips the bytes.
	NotificationRulesJSON []byte
	// QuietHoursStartLocal / QuietHoursEndLocal are 0..23 wall-clock
	// hour-of-day in QuietHoursTZ. nil/nil means "no quiet hours
	// configured". REQ-PROTO-121 / tabard REQ-PUSH-82.
	QuietHoursStartLocal *int
	QuietHoursEndLocal   *int
	// QuietHoursTZ is the IANA timezone name (e.g. "Europe/Berlin").
	// Empty when QuietHoursStartLocal is nil. Validation is the JMAP
	// serializer's job — the store treats this as opaque.
	QuietHoursTZ string
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}
