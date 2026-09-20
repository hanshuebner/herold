package extimg

import "bytes"

// ShouldFlagOnDemand reports whether an importer that stores a message
// verbatim (rather than internalizing live) should set the message's
// InternalizePending marker, given the operator's internalize-imports
// policy string (REQ-EXTIMG-91/92). The policy mirrors the operator's
// [external_images] mode: "" / "on_demand" (default) flags eligible
// messages; "off" (mapped from mode = "passthrough") never flags.
// Both the Gmail Takeout importer and the IMAP-mirror importer share
// this decision so the policy cannot drift between the two callers.
func ShouldFlagOnDemand(policy string) bool {
	switch policy {
	case "", "on_demand":
		return true
	case "off":
		return false
	}
	return true
}

// ShouldFlagOnDemandForMode is ShouldFlagOnDemand for a caller that
// already carries a live extimg.Config (e.g. a JMAP handler wired with
// the same extImg dependency the delivery-time internalize path
// uses): mode = ModePassthrough suppresses flagging; every other
// mode, including the empty default, enables it.
func ShouldFlagOnDemandForMode(mode Mode) bool {
	return mode != ModePassthrough
}

// HasExternalHTMLImage is the cheap heuristic that gates the pending
// flag: a substring scan for an "<img" tag and an "http://" or
// "https://" reference anywhere in the raw message body (REQ-EXTIMG-91).
// Precise URL extraction is deferred to the read-time internalize pass;
// a false positive here costs one no-op rewrite at first read, and a
// false negative leaves the message unrewritten (no privacy harm).
func HasExternalHTMLImage(body []byte) bool {
	return bytes.Contains(body, []byte("<img")) &&
		(bytes.Contains(body, []byte("http://")) || bytes.Contains(body, []byte("https://")))
}
