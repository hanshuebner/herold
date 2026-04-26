// Package sesinbound implements the AWS SES inbound path (REQ-HOOK-SES-01..07).
//
// When an operator runs SES as the public-facing MX, SES delivers raw RFC 5322
// bytes to an S3 bucket and fires an SNS notification. This package exposes an
// HTTP handler (POST /hooks/ses/inbound) that:
//
//   - Verifies the SNS message signature against the X.509 cert at
//     SigningCertURL (REQ-HOOK-SES-02).
//   - Confirms SubscriptionConfirmation only for allowlisted topic ARNs
//     (REQ-HOOK-SES-03).
//   - Fetches the S3 object and passes the raw bytes to the same inbound
//     pipeline SMTP-delivered mail uses (REQ-HOOK-SES-04).
//   - Deduplicates by SNS MessageId for at least 24 h (REQ-HOOK-SES-02).
//   - Guards both the SigningCertURL and the S3 endpoint via the netguard
//     SSRF predicate (REQ-HOOK-SES-06).
//
// Configuration lives in [hooks.ses_inbound] in system.toml (REQ-HOOK-SES-03).
// Mount Handler() on the public HTTP listener when enabled.
package sesinbound
