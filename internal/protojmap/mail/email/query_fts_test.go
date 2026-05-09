package email

import (
	"encoding/json"
	"testing"
)

// TestBuildFTSQuery_FoldsAND verifies that text predicates buried inside
// a top-level AND are merged into the FTS Query envelope. Without the
// walk, the SPA's REQ-SRC-06 wrapper (`text: AND inMailboxOtherThan: …`)
// would degrade FTS to an empty-text query and return an unrelated
// candidate set.
func TestBuildFTSQuery_FoldsAND(t *testing.T) {
	text := "milfsdating"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Text: &text}),
			mustMarshal(t, emailFilter{InMailboxOtherThan: []jmapID{"4", "5"}}),
		},
	}
	got := buildFTSQuery(f)
	if got.Text != text {
		t.Fatalf("Text: want %q, got %q", text, got.Text)
	}
}

func TestBuildFTSQuery_TopLevelText(t *testing.T) {
	text := "foo"
	f := &emailFilter{Text: &text}
	got := buildFTSQuery(f)
	if got.Text != text {
		t.Fatalf("Text: want %q, got %q", text, got.Text)
	}
}

// OR-trees are NOT folded; the FTS query reflects no text and the
// non-FTS path takes over (or the per-message matchCondition
// re-validates).
func TestBuildFTSQuery_DoesNotFoldOR(t *testing.T) {
	text := "foo"
	f := &emailFilter{
		Operator: "OR",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Text: &text}),
			mustMarshal(t, emailFilter{From: ptr("alice@example.test")}),
		},
	}
	got := buildFTSQuery(f)
	if got.Text != "" {
		t.Fatalf("OR should not fold text into FTS query, got %q", got.Text)
	}
}

func TestBuildFTSQuery_FoldsNestedAND(t *testing.T) {
	text := "foo"
	from := "bob@example.test"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{
				Operator: "AND",
				Conditions: []json.RawMessage{
					mustMarshal(t, emailFilter{Text: &text}),
				},
			}),
			mustMarshal(t, emailFilter{From: &from}),
		},
	}
	got := buildFTSQuery(f)
	if got.Text != text {
		t.Fatalf("Text: want %q, got %q", text, got.Text)
	}
	if len(got.From) != 1 || got.From[0] != from {
		t.Fatalf("From: want [%q], got %v", from, got.From)
	}
}

func mustMarshal(t *testing.T, v emailFilter) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func ptr(s string) *string { return &s }
