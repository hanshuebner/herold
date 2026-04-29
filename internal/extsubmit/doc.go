// Package extsubmit is the external SMTP submission engine for per-Identity
// outbound mail (REQ-AUTH-EXT-SUBMIT-05..07).
//
// When a JMAP Identity carries an IdentitySubmission row, the server calls
// Submitter.Submit instead of enqueuing the message on the local outbound
// queue. The Submitter performs the full SMTP wire exchange against the
// configured external endpoint and returns an Outcome that the caller maps
// to the JMAP EmailSubmission state.
//
// There is no local retry: the external server's queue is authoritative once
// the message is accepted (REQ-AUTH-EXT-SUBMIT-05). 4xx responses are
// "soft-fail" (caller may surface as transient to the user, but there is no
// re-queue); 5xx responses are "hard-fail" (permanent, user-visible error).
//
// OAuth token refresh is handled by the Refresher. When the in-memory access
// token is within the refresh window, Refresher.Refresh contacts the token
// endpoint, seals the new token, and updates the IdentitySubmission row via
// the store. On a 4xx or invalid_grant response it returns ErrAuthFailed so
// the caller can record the auth-failed state.
//
// Probe runs AUTH-only (EHLO + AUTH + QUIT) without a MAIL FROM / DATA
// exchange, which avoids triggering remote anti-spam. Gmail and M365 reject
// MAIL FROM:<> so probing via a null sender is not an option.
//
// DKIM signing is explicitly skipped on this path (REQ-AUTH-EXT-SUBMIT-06).
// The external server is responsible for signing under its own key; local
// re-signing would fail DMARC alignment.
package extsubmit
