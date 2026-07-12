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

// TestBuildFTSQuery_FoldsOR pins the re #198 fix: an OR-tree folds into
// store.Query.Or (one branch per condition) rather than being dropped.
// Before the fix, mergeFTSQuery only handled AND and left an OR node's
// text-bearing predicates entirely out of the FTS query, which silently
// widened a multi-address "all mail with this person" search into an
// unscoped scan combined with a matchCondition fast path that trusted
// the (non-existent) narrowing.
func TestBuildFTSQuery_FoldsOR(t *testing.T) {
	text := "foo"
	from := "alice@example.test"
	f := &emailFilter{
		Operator: "OR",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Text: &text}),
			mustMarshal(t, emailFilter{From: &from}),
		},
	}
	got := buildFTSQuery(f)
	if got.Text != "" || len(got.From) != 0 {
		t.Fatalf("OR must not fold branches into the top-level fields, got Text=%q From=%v", got.Text, got.From)
	}
	if len(got.Or) != 2 {
		t.Fatalf("want 2 OR branches, got %d: %+v", len(got.Or), got.Or)
	}
	if got.Or[0].Text != text {
		t.Errorf("branch 0: want Text=%q, got %+v", text, got.Or[0])
	}
	if len(got.Or[1].From) != 1 || got.Or[1].From[0] != from {
		t.Errorf("branch 1: want From=[%q], got %+v", from, got.Or[1])
	}
}

// TestBuildFTSQuery_FoldsMultiValueOR is the direct regression test for
// the contact "all mail with this person" scenario: three addresses
// joined by " OR " must all appear as branches, not just the first.
func TestBuildFTSQuery_FoldsMultiValueOR(t *testing.T) {
	addrs := []string{
		"andreasrosenthal9@gmail.com",
		"andreas.rosenthal@lamberti.at",
		"rosenthal@deepick.eu",
	}
	conditions := make([]json.RawMessage, len(addrs))
	for i, a := range addrs {
		a := a
		conditions[i] = mustMarshal(t, emailFilter{Text: &a})
	}
	f := &emailFilter{Operator: "OR", Conditions: conditions}
	got := buildFTSQuery(f)
	if len(got.Or) != len(addrs) {
		t.Fatalf("want %d OR branches, got %d: %+v", len(addrs), len(got.Or), got.Or)
	}
	for i, a := range addrs {
		if got.Or[i].Text != a {
			t.Errorf("branch %d: want Text=%q, got %q", i, a, got.Or[i].Text)
		}
	}
}

// TestBuildFTSQuery_FoldsMultipleTextConditions pins the AND-side twin
// of the OR under-matching fix: the suite's own whitespace tokenizer
// splits a multi-word bareword search (e.g. "invoice friday") into
// separate top-level `{text: ...}` conditions ANDed together. Before the
// fix, mergeFTSQuery's `q.Text == ""` guard kept only the first word and
// silently dropped the rest from the FTS query, with no other
// validation path to catch the loss (re #198).
func TestBuildFTSQuery_FoldsMultipleTextConditions(t *testing.T) {
	first := "invoice"
	second := "friday"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Text: &first}),
			mustMarshal(t, emailFilter{Text: &second}),
		},
	}
	got := buildFTSQuery(f)
	if got.Text != "invoice friday" {
		t.Fatalf("want joined Text %q, got %q", "invoice friday", got.Text)
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
