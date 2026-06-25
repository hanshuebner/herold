// Package identityverify composes and dispatches the per-Identity
// verification email (REQ-IDENT-30..49).
//
// Wave-3 task #14 owns this package: it bridges the
// jmapidentity.VerificationTrigger hook (fired on Identity/set { create }
// commit) and the outbound queue. The hook:
//
//  1. Generates a CSPRNG 32-byte raw verification token (base64url-
//     encoded, 43 ASCII chars, no padding) and a 6-digit decimal code.
//  2. Stores their sha256 hashes plus a 24h expiry on the identity row
//     via store.Meta().IssueIdentityVerificationToken (REQ-IDENT-31/32/34).
//  3. Renders a multipart/alternative RFC 5322 message containing the
//     callback URL "<public_base_url>/verify-identity?token=<token>" and
//     the bare code on its own line (REQ-IDENT-33). The URL uses
//     [server].public_base_url (the SPA origin), not the MTA hostname.
//  4. Submits the message to the outbound queue, signed under the
//     canonical hosted domain's DKIM key (REQ-IDENT-30, REQ-DKIM-*).
//
// The raw token leaves the package only through the email body. The
// store sees the sha256 hashes only. The whole flow is best-effort: a
// failure to compose or enqueue is logged but does not block the JMAP
// /set response — the user can request a resend from the SPA's "have a
// code" affordance (REQ-IDENT-41) once the wizard ships (task #21).
//
// REST verification handlers (REQ-IDENT-40..43) live in
// internal/protoadmin/identity_verify.go; this package is concerned
// solely with the outbound-send half of the round-trip.
package identityverify
