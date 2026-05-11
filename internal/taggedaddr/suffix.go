// Package taggedaddr implements RFC 5233 §3.1 sub-address suffix
// extraction and the inbound-delivery filter-evaluation glue for the
// tagged-addresses feature (REQ-TAG-01..03, REQ-TAG-20..32).
//
// The package is intentionally narrow:
//
//   - Split extracts (base_local, suffix, base_email) from an envelope
//     address using the `+` separator (REQ-TAG-01).
//   - The Suffix is normalised to lower-case (REQ-TAG-02).
//   - Empty suffixes (`alice+@example.test`) are treated as "no suffix"
//     (REQ-TAG-03).
//
// The inbound pipeline imports this package to derive (baseEmail,
// suffix) from `rc.addr` before consulting tagged_address_filters.
package taggedaddr

import "strings"

// Split returns the base local-part and the lower-cased suffix derived
// from a recipient envelope address per REQ-TAG-01..03.
//
//   - addr is the envelope RCPT TO, in any case. The caller must have
//     already canonicalised aliases; we operate on the literal address
//     bytes.
//   - On a malformed address (no `@`, empty local-part) the caller gets
//     ("", "", "") so the inbound pipeline can fall through.
//   - When the local-part contains no `+`, suffix is "" and the caller
//     skips tag-filter evaluation (REQ-TAG-01 second sentence).
//   - When the local-part is `alice+`, the suffix is empty per
//     REQ-TAG-03 — same "no suffix" path. Bytes after the first `+`
//     are taken verbatim; further `+` characters are part of the
//     suffix string (REQ-TAG-01 last sentence).
//
// baseLocal is returned in its original case so the caller can use
// downstream-correct casing if it needs to (the store's canonical-
// email lookup lower-cases on its own). suffix is canonicalised to
// lower-case per REQ-TAG-02. baseEmail is `baseLocal + "@" + domain`
// with the local-part and domain lower-cased — the form that matches
// the stored JMAPIdentity.Email column (which the JMAP layer
// canonicalises on insert).
func Split(addr string) (baseLocal, suffix, baseEmail string) {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		// Malformed: no `@`, leading `@`, or trailing `@`. The
		// inbound pipeline never reaches here for valid envelope
		// recipients; the guard is defence-in-depth so a stray
		// caller doesn't panic.
		return "", "", ""
	}
	local := addr[:at]
	domain := strings.ToLower(addr[at+1:])
	plus := strings.IndexByte(local, '+')
	if plus < 0 {
		// No `+` in the local-part: REQ-TAG-01 says the message has
		// no suffix; the tagged-address subsystem is a no-op.
		return local, "", strings.ToLower(local) + "@" + domain
	}
	baseLocal = local[:plus]
	rawSuffix := local[plus+1:]
	if rawSuffix == "" {
		// REQ-TAG-03: empty suffix is treated as "no suffix".
		return baseLocal, "", strings.ToLower(baseLocal) + "@" + domain
	}
	suffix = strings.ToLower(rawSuffix)
	baseEmail = strings.ToLower(baseLocal) + "@" + domain
	return baseLocal, suffix, baseEmail
}
