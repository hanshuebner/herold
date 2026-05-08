// Package extimg implements the external-image internalization
// pipeline specified in docs/design/server/requirements/17-external-
// images.md (REQ-EXTIMG-01..82). At inbound delivery time, the
// rewriter walks every HTML part of the message, finds external
// image references (img src, srcset, picture/source, CSS url(...),
// SVG image href), fetches each one through an SSRF-aware HTTP
// client with strict per-image and per-message limits, attaches the
// fetched bytes as inline MIME parts, and rewrites the references
// to cid: form. The resulting message has no external image
// references and renders identically across IMAP, JMAP, and the
// suite without any user-side fetch.
//
// Operator policy is one config knob (REQ-EXTIMG-01):
//
//   - mode = "internalize" (default) — rewrite at delivery
//   - mode = "passthrough"            — leave unchanged
//
// In passthrough mode this package does nothing; the suite-side
// "show images" gating remains the user's privacy fence (REQ-EXTIMG-03).
//
// The SSRF guard (REQ-EXTIMG-30..37) refuses every IP in IANA's
// non-routable / locally-significant special-purpose registries,
// resolves hostnames once and dials the resolved IP literal (defeating
// DNS rebinding), and re-validates redirect targets at every hop.
//
// DKIM (REQ-EXTIMG-40..47): the wire-original body's DKIM signature
// is verified by the caller before the rewriter runs; the verdict is
// persisted on the message. After rewriting the body, the inbound
// DKIM-Signature and Authentication-Results headers are stripped
// because the new body no longer matches the signed body hash. A
// server-stamped Authentication-Results is emitted recording the
// pre-rewrite verdict and an X-Herold-Body-Modified marker is added.
// ARC sealing (REQ-EXTIMG-47) is reserved for a future iteration.
package extimg
