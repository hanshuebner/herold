package protoadmin

// auth_apikey_scope_test.go tests the parseAPIKeyScope function, which
// decodes scope_json from a stored APIKey row. It lives in package
// protoadmin (not protoadmin_test) so it can reach the unexported function.
//
// The security-critical cases (B-3):
//   - raw == "":   legacy unset column → ScopeAdmin (backward compat)
//   - raw == "[]": empty JSON array   → ScopeMailSend (NOT ScopeAdmin)
//   - malformed:   bad JSON           → ScopeMailSend (NOT ScopeAdmin)
//   - valid:       correct scope set  → that scope set, unchanged

import (
	"testing"

	"github.com/hanshuebner/herold/internal/auth"
)

func TestParseAPIKeyScope_EmptyRaw_LegacyGrantsAdmin(t *testing.T) {
	// Empty string is the pre-migration column state: treat as admin
	// for backward compatibility with rows that predate the scope column.
	got := parseAPIKeyScope("")
	if !got.Has(auth.ScopeAdmin) {
		t.Errorf("parseAPIKeyScope(%q) = %v; want ScopeAdmin", "", got.Slice())
	}
}

func TestParseAPIKeyScope_EmptyArray_GrantsMailSendNotAdmin(t *testing.T) {
	// "[]" is a stored empty-array scope_json — degrade to ScopeMailSend,
	// never ScopeAdmin (B-3 privilege-escalation guard).
	got := parseAPIKeyScope("[]")
	if got.Has(auth.ScopeAdmin) {
		t.Errorf("parseAPIKeyScope(%q) = %v; must NOT contain ScopeAdmin", "[]", got.Slice())
	}
	if !got.Has(auth.ScopeMailSend) {
		t.Errorf("parseAPIKeyScope(%q) = %v; want ScopeMailSend as fallback", "[]", got.Slice())
	}
}

func TestParseAPIKeyScope_MalformedJSON_GrantsMailSendNotAdmin(t *testing.T) {
	// Malformed JSON: degrade to least-privilege, not ScopeAdmin (B-3).
	for _, bad := range []string{"not-json", "{}", `"admin"`, "null"} {
		got := parseAPIKeyScope(bad)
		if got.Has(auth.ScopeAdmin) {
			t.Errorf("parseAPIKeyScope(%q) = %v; must NOT contain ScopeAdmin", bad, got.Slice())
		}
		if !got.Has(auth.ScopeMailSend) {
			t.Errorf("parseAPIKeyScope(%q) = %v; want ScopeMailSend as fallback", bad, got.Slice())
		}
	}
}

func TestParseAPIKeyScope_ValidScope_ReturnsThatScope(t *testing.T) {
	// A well-formed scope array must be returned verbatim.
	cases := []struct {
		raw  string
		want []auth.Scope
	}{
		{`["mail.send"]`, []auth.Scope{auth.ScopeMailSend}},
		{`["admin"]`, []auth.Scope{auth.ScopeAdmin}},
		{`["mail.send","mail.receive"]`, []auth.Scope{auth.ScopeMailSend, auth.ScopeMailReceive}},
	}
	for _, tc := range cases {
		got := parseAPIKeyScope(tc.raw)
		for _, sc := range tc.want {
			if !got.Has(sc) {
				t.Errorf("parseAPIKeyScope(%q): missing %q in %v", tc.raw, sc, got.Slice())
			}
		}
	}
}
