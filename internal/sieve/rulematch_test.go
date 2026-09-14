package sieve_test

import (
	"bytes"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

func parseTestMessage(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	return msg
}

const testMsgFromTrusted = "From: notify@accountprotection.microsoft.com\r\n" +
	"To: alice@example.test\r\n" +
	"Subject: password reset\r\n\r\n" +
	"Body.\r\n"

func TestMatchRuleConditions_From(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected match on From header")
	}
}

func TestMatchRuleConditions_NoMatch(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "from", Op: "contains", Value: "@evil.example"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no match")
	}
}

func TestMatchRuleConditions_Empty(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions(nil, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("an empty condition list should match unconditionally")
	}
}

func TestMatchRuleConditions_Subject(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "subject", Op: "contains", Value: "password reset"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected match on Subject header")
	}
}

// TestMatchRuleConditions_AndCombined mirrors compileConditions' AND
// semantics: every condition must match.
func TestMatchRuleConditions_AndCombined(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	conds := []store.RuleCondition{
		{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
		{Field: "subject", Op: "contains", Value: "nonexistent-subject"},
	}
	ok, err := sieve.MatchRuleConditions(conds, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no match: the subject condition does not hold")
	}
}

// TestMatchRuleConditions_FromDomain covers re #382: the extracted :domain
// address part carries no "@" prefix, so the condition value must compare
// against it directly.
func TestMatchRuleConditions_FromDomain(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "from-domain", Op: "equals", Value: "accountprotection.microsoft.com"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected match on From-domain condition")
	}
}

// TestMatchRuleConditions_FromDomain_Subdomain covers the subdomain-matching
// reading of "domain" (REQ-FLT-01): a from-domain condition on "microsoft.com"
// matches a sender on the subdomain "accountprotection.microsoft.com".
func TestMatchRuleConditions_FromDomain_Subdomain(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "from-domain", Op: "equals", Value: "microsoft.com"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected from-domain to match a subdomain of the condition value")
	}
}

func TestMatchRuleConditions_FromDomain_NoMatch(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "from-domain", Op: "equals", Value: "evil.example"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no match for an unrelated domain")
	}
}

// TestMatchRuleConditions_FromDomain_NoFalseSuffixMatch guards against a
// naive strings.HasSuffix implementation: "notmicrosoft.com" must not match
// a from-domain condition of "microsoft.com" just because it ends with the
// same characters -- only "microsoft.com" itself or a dot-delimited
// subdomain of it should match.
func TestMatchRuleConditions_FromDomain_NoFalseSuffixMatch(t *testing.T) {
	msg := parseTestMessage(t, "From: notify@notmicrosoft.com\r\n"+
		"To: alice@example.test\r\n"+
		"Subject: password reset\r\n\r\n"+
		"Body.\r\n")
	ok, err := sieve.MatchRuleConditions([]store.RuleCondition{
		{Field: "from-domain", Op: "equals", Value: "microsoft.com"},
	}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected no match: notmicrosoft.com is not a subdomain of microsoft.com")
	}
}

func TestNeverSpamOverrideLabel_MatchByName(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	rules := []store.ManagedRule{
		{
			ID:      7,
			Name:    "Trusted senders",
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
			},
			Actions: []store.RuleAction{{Kind: "never-spam"}},
		},
	}
	label, err := sieve.NeverSpamOverrideLabel(rules, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "filter:Trusted senders" {
		t.Errorf("label = %q, want %q", label, "filter:Trusted senders")
	}
}

func TestNeverSpamOverrideLabel_MatchByID(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	rules := []store.ManagedRule{
		{
			ID:      42,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
			},
			Actions: []store.RuleAction{{Kind: "never-spam"}},
		},
	}
	label, err := sieve.NeverSpamOverrideLabel(rules, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "filter:42" {
		t.Errorf("label = %q, want %q", label, "filter:42")
	}
}

// TestNeverSpamOverrideLabel_IgnoresOtherRules covers a rule without a
// never-spam action (no label) and a disabled never-spam rule (skipped).
func TestNeverSpamOverrideLabel_IgnoresOtherRules(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
			},
			Actions: []store.RuleAction{{Kind: "apply-label", Params: map[string]any{"label": "Security"}}},
		},
		{
			ID:      2,
			Enabled: false,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "@accountprotection.microsoft.com"},
			},
			Actions: []store.RuleAction{{Kind: "never-spam"}},
		},
	}
	label, err := sieve.NeverSpamOverrideLabel(rules, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "" {
		t.Errorf("label = %q, want empty", label)
	}
}

// TestNeverSpamOverrideLabel_FromDomain covers re #382: a never-spam rule
// keyed on the sender's domain resolves to the rule's transparency label.
func TestNeverSpamOverrideLabel_FromDomain(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	rules := []store.ManagedRule{
		{
			ID:      7,
			Name:    "Trusted domains",
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from-domain", Op: "equals", Value: "accountprotection.microsoft.com"},
			},
			Actions: []store.RuleAction{{Kind: "never-spam"}},
		},
	}
	label, err := sieve.NeverSpamOverrideLabel(rules, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "filter:Trusted domains" {
		t.Errorf("label = %q, want %q", label, "filter:Trusted domains")
	}
}

func TestNeverSpamOverrideLabel_NoMatch(t *testing.T) {
	msg := parseTestMessage(t, testMsgFromTrusted)
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "@evil.example"},
			},
			Actions: []store.RuleAction{{Kind: "never-spam"}},
		},
	}
	label, err := sieve.NeverSpamOverrideLabel(rules, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "" {
		t.Errorf("label = %q, want empty", label)
	}
}
