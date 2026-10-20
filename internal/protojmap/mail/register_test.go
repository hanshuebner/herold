package mail

import (
	"encoding/json"
	"testing"
)

// TestMailAccountCapability_MaxMailboxesPerEmailUnlimited: an email may
// belong to any number of mailboxes (REQ-PROTO-128), so the account
// capability advertises maxMailboxesPerEmail as null, which RFC 8621
// section 1.3.1 defines as no limit.
func TestMailAccountCapability_MaxMailboxesPerEmailUnlimited(t *testing.T) {
	raw, err := json.Marshal(mailAccountCapability{}.AccountCapability())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := body["maxMailboxesPerEmail"]
	if !ok || string(v) != "null" {
		t.Fatalf("maxMailboxesPerEmail = %s (present %v), want null: %s", v, ok, raw)
	}
}
