package store

// InboundAttachmentPolicy enumerates the inbound MIME-attachment policy
// options (REQ-FLOW-ATTPOL-01). Default at the Server is
// AttPolicyAccept; per-recipient and per-domain rows in the store
// override the default.
type InboundAttachmentPolicy uint8

const (
	// AttPolicyUnset is the zero value; rows persisted with this value
	// are ignored by the lookup so a typo in a manually-edited config
	// never silently refuses every message.
	AttPolicyUnset InboundAttachmentPolicy = iota
	// AttPolicyAccept admits the message regardless of MIME shape. This
	// is the default when no row is on file.
	AttPolicyAccept
	// AttPolicyRejectAtData refuses the message with 552 5.3.4 in the
	// SMTP DATA phase when the parsed top-level MIME structure carries
	// an attachment (REQ-FLOW-ATTPOL-01) and additionally rejects
	// post-acceptance when the deep MIME walker finds an attachment
	// hiding inside multipart/alternative (REQ-FLOW-ATTPOL-02).
	AttPolicyRejectAtData
)

// String returns the wire-form token used in TOML / CLI / audit logs.
func (p InboundAttachmentPolicy) String() string {
	switch p {
	case AttPolicyAccept:
		return "accept"
	case AttPolicyRejectAtData:
		return "reject_at_data"
	default:
		return ""
	}
}

// ParseInboundAttachmentPolicy parses the wire-form token. Empty input
// returns AttPolicyUnset; unknown tokens return AttPolicyUnset. Callers
// distinguish "absent" from "explicit accept" via the IsZero check.
func ParseInboundAttachmentPolicy(s string) InboundAttachmentPolicy {
	switch s {
	case "accept":
		return AttPolicyAccept
	case "reject_at_data":
		return AttPolicyRejectAtData
	default:
		return AttPolicyUnset
	}
}

// InboundAttachmentPolicyRow carries one resolved policy entry: the
// effective policy and the operator-overridable reject text.
type InboundAttachmentPolicyRow struct {
	// Policy is the effective policy after recipient / domain
	// inheritance has been applied. AttPolicyAccept when no row is on
	// file (the default).
	Policy InboundAttachmentPolicy
	// RejectText is the operator-overridable text appended after the
	// "552 5.3.4 " prefix on a refusal. Empty falls back to the
	// documented default "attachments not accepted on this address".
	RejectText string
}
