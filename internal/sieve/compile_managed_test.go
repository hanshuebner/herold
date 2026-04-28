package sieve_test

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// -- CompileRules unit tests ------------------------------------------

func TestCompileRules_Empty(t *testing.T) {
	script, err := sieve.CompileRules(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if script != "" {
		t.Errorf("expected empty string for nil rules, got %q", script)
	}
}

func TestCompileRules_AllDisabled(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: false,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "spam@x.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "delete"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if script != "" {
		t.Errorf("expected empty string for all-disabled rules, got %q", script)
	}
}

func TestCompileRules_FromEquals_Delete(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "spam@evil.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "delete"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, `address :is :all "From" "spam@evil.com"`) {
		t.Errorf("script missing expected from-equals test; got:\n%s", script)
	}
	if !strings.Contains(script, `fileinto "Trash"`) {
		t.Errorf("script missing fileinto Trash; got:\n%s", script)
	}
}

func TestCompileRules_FromContains(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "newsletter"},
			},
			Actions: []store.RuleAction{
				{Kind: "apply-label", Params: map[string]any{"label": "Newsletters"}},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, `:contains`) {
		t.Errorf("script missing :contains match type; got:\n%s", script)
	}
	if !strings.Contains(script, `fileinto "Newsletters"`) {
		t.Errorf("script missing fileinto Newsletters; got:\n%s", script)
	}
}

func TestCompileRules_ThreadID_SkipInbox_MarkRead(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "thread-id", Op: "equals", Value: "thread-abc-123"},
			},
			Actions: []store.RuleAction{
				{Kind: "skip-inbox"},
				{Kind: "mark-read"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, `header :is "X-Herold-Thread-Id" "thread-abc-123"`) {
		t.Errorf("script missing thread-id header test; got:\n%s", script)
	}
	if !strings.Contains(script, `fileinto "Archive"`) {
		t.Errorf("script missing fileinto Archive (skip-inbox); got:\n%s", script)
	}
	if !strings.Contains(script, `addflag`) {
		t.Errorf("script missing addflag (mark-read); got:\n%s", script)
	}
}

func TestCompileRules_ForwardAction(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "subject", Op: "contains", Value: "invoice"},
			},
			Actions: []store.RuleAction{
				{Kind: "forward", Params: map[string]any{"to": "accounting@corp.example.com"}},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, `redirect "accounting@corp.example.com"`) {
		t.Errorf("script missing redirect; got:\n%s", script)
	}
}

func TestCompileRules_FromDomain(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from-domain", Op: "equals", Value: "acme.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "apply-label", Params: map[string]any{"label": "Acme"}},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, `:domain`) {
		t.Errorf("script missing :domain match; got:\n%s", script)
	}
}

func TestCompileRules_MultipleConditions_AllOf(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "contains", Value: "@corp.com"},
				{Field: "subject", Op: "contains", Value: "urgent"},
			},
			Actions: []store.RuleAction{
				{Kind: "mark-read"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, "allof") {
		t.Errorf("script missing allof for multiple conditions; got:\n%s", script)
	}
}

func TestCompileRules_SortOrder_Deterministic(t *testing.T) {
	// Rules with explicit sort orders: IDs 3, 1, 2 with orders 20, 5, 10.
	// Expected compiled order: rule ID=1 (order 5), rule ID=2 (order 10), rule ID=3 (order 20).
	rules := []store.ManagedRule{
		{
			ID: 3, Enabled: true, SortOrder: 20,
			Conditions: []store.RuleCondition{{Field: "from", Op: "equals", Value: "c@x.com"}},
			Actions:    []store.RuleAction{{Kind: "skip-inbox"}},
		},
		{
			ID: 1, Enabled: true, SortOrder: 5,
			Conditions: []store.RuleCondition{{Field: "from", Op: "equals", Value: "a@x.com"}},
			Actions:    []store.RuleAction{{Kind: "skip-inbox"}},
		},
		{
			ID: 2, Enabled: true, SortOrder: 10,
			Conditions: []store.RuleCondition{{Field: "from", Op: "equals", Value: "b@x.com"}},
			Actions:    []store.RuleAction{{Kind: "skip-inbox"}},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify "a@x.com" comes before "b@x.com", and "b@x.com" before "c@x.com".
	posA := strings.Index(script, "a@x.com")
	posB := strings.Index(script, "b@x.com")
	posC := strings.Index(script, "c@x.com")
	if posA < 0 || posB < 0 || posC < 0 {
		t.Fatalf("script missing expected addresses; got:\n%s", script)
	}
	if !(posA < posB && posB < posC) {
		t.Errorf("rules not in sort-order sequence; posA=%d posB=%d posC=%d\nscript:\n%s",
			posA, posB, posC, script)
	}
}

func TestCompileRules_InvalidActionCombo(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "bad@x.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "delete"},
				{Kind: "apply-label", Params: map[string]any{"label": "Spam"}},
			},
		},
	}
	_, err := sieve.CompileRules(rules)
	if err == nil {
		t.Fatal("expected error for delete+apply-label combo, got nil")
	}
	if !strings.Contains(err.Error(), "delete") {
		t.Errorf("error should mention 'delete'; got: %v", err)
	}
}

func TestCompileRules_UnknownField(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "banana", Op: "equals", Value: "yellow"},
			},
			Actions: []store.RuleAction{
				{Kind: "delete"},
			},
		},
	}
	_, err := sieve.CompileRules(rules)
	if err == nil {
		t.Fatal("expected error for unknown condition field")
	}
}

func TestCompileRules_UnknownAction(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "x@y.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "explode"},
			},
		},
	}
	_, err := sieve.CompileRules(rules)
	if err == nil {
		t.Fatal("expected error for unknown action kind")
	}
}

func TestCompileRules_Quoting_SpecialChars(t *testing.T) {
	// User value contains a double-quote and backslash: must be escaped.
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "subject", Op: "equals", Value: `He said "hello\world"`},
			},
			Actions: []store.RuleAction{
				{Kind: "skip-inbox"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must contain escaped quote and escaped backslash.
	if !strings.Contains(script, `\"`) {
		t.Errorf("expected escaped quote in script; got:\n%s", script)
	}
	if !strings.Contains(script, `\\`) {
		t.Errorf("expected escaped backslash in script; got:\n%s", script)
	}
}

func TestCompileRules_RequireExtensions(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "x@y.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "mark-read"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(script, `require`) {
		t.Errorf("script missing require statement; got:\n%s", script)
	}
	if !strings.Contains(script, `"imap4flags"`) {
		t.Errorf("script missing imap4flags in require; got:\n%s", script)
	}
}

func TestCompileRules_ForwardAddress_Invalid(t *testing.T) {
	// Forward with newline in address must fail.
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "x@y.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "forward", Params: map[string]any{"to": "a@b.com\nredirect \"evil@z.com\""}},
			},
		},
	}
	_, err := sieve.CompileRules(rules)
	if err == nil {
		t.Fatal("expected error for forward address with newline")
	}
}

func TestCompileRules_ForwardAddress_MissingAt(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:      1,
			Enabled: true,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "x@y.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "forward", Params: map[string]any{"to": "notanemail"}},
			},
		},
	}
	_, err := sieve.CompileRules(rules)
	if err == nil {
		t.Fatal("expected error for forward address without @")
	}
}

// -- EffectiveScript --------------------------------------------------

func TestEffectiveScript_PreambleOnly(t *testing.T) {
	result := sieve.EffectiveScript("preamble content", "")
	if result != "preamble content" {
		t.Errorf("expected preamble only, got %q", result)
	}
}

func TestEffectiveScript_UserOnly(t *testing.T) {
	result := sieve.EffectiveScript("", "user content")
	if result != "user content" {
		t.Errorf("expected user content only, got %q", result)
	}
}

func TestEffectiveScript_Both(t *testing.T) {
	result := sieve.EffectiveScript("preamble", "user")
	if !strings.Contains(result, "# --- user-managed ---") {
		t.Errorf("expected delimiter in combined script; got %q", result)
	}
	if !strings.HasPrefix(result, "preamble") {
		t.Errorf("preamble should come first; got %q", result)
	}
	if !strings.HasSuffix(result, "user") {
		t.Errorf("user script should come last; got %q", result)
	}
}

func TestEffectiveScript_EmptyBoth(t *testing.T) {
	result := sieve.EffectiveScript("", "")
	if result != "" {
		t.Errorf("expected empty string for both empty, got %q", result)
	}
}

// -- Snapshot test for a known rule set ------------------------------
// The snapshot captures the exact Sieve output for a representative rule
// set. If the compiler output changes, the snapshot must be updated and
// the reviewer must confirm the new output is correct.

const snapshotRuleSet = `require ["fileinto", "imap4flags"];
if address :is :all "From" "block@evil.com" {
  fileinto "Trash";
}
if header :is "X-Herold-Thread-Id" "thread-42" {
  fileinto "Archive";
  addflag "\\Seen";
}
`

func TestCompileRules_Snapshot(t *testing.T) {
	rules := []store.ManagedRule{
		{
			ID:        1,
			Enabled:   true,
			SortOrder: 0,
			Conditions: []store.RuleCondition{
				{Field: "from", Op: "equals", Value: "block@evil.com"},
			},
			Actions: []store.RuleAction{
				{Kind: "delete"},
			},
		},
		{
			ID:        2,
			Enabled:   true,
			SortOrder: 1,
			Conditions: []store.RuleCondition{
				{Field: "thread-id", Op: "equals", Value: "thread-42"},
			},
			Actions: []store.RuleAction{
				{Kind: "skip-inbox"},
				{Kind: "mark-read"},
			},
		},
	}
	script, err := sieve.CompileRules(rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if script != snapshotRuleSet {
		t.Errorf("snapshot mismatch.\nwant:\n%s\ngot:\n%s", snapshotRuleSet, script)
	}
}
