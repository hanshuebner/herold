package email

import (
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// REQ-PERF-INDEX-10: the fast-pushability gate must accept a
// single-level `{operator: 'AND', conditions: [...]}` whose every
// conjunct is itself flat-pushable. The reported regression
// (2026-05-10) was that the SPA's `applyTrashJunkExclusion` produced
// this shape for any default-scoped search, and the gate's
// pre-fix behaviour was "any non-empty Operator → slow path",
// which dropped trivially-indexable predicates such as
// `before:2009-01-01` to the Go-side scan.

// TestMergeFilterIntoOpts_FlatBefore is the original baseline:
// a flat FilterCondition with `before` is pushable.
func TestMergeFilterIntoOpts_FlatBefore(t *testing.T) {
	before := "2009-01-01T00:00:00Z"
	f := &emailFilter{Before: &before}

	var opts store.EmailQueryFastOpts
	if !mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("flat before-filter must be pushable")
	}
	if opts.Before == nil {
		t.Fatalf("Before not merged: %+v", opts)
	}
	if opts.Before.Format("2006-01-02") != "2009-01-01" {
		t.Fatalf("Before = %v, want 2009-01-01", opts.Before)
	}
}

// TestMergeFilterIntoOpts_AndOfBeforeAndInMailboxOtherThan covers the
// exact wire shape `applyTrashJunkExclusion` used to emit unconditionally.
// After the fix it must merge cleanly into a single opts accumulator.
func TestMergeFilterIntoOpts_AndOfBeforeAndInMailboxOtherThan(t *testing.T) {
	before := "2009-01-01T00:00:00Z"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Before: &before}),
			mustMarshal(t, emailFilter{InMailboxOtherThan: []jmapID{"7", "8"}}),
		},
	}

	var opts store.EmailQueryFastOpts
	if !mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("AND of two flat-pushable conjuncts must be pushable")
	}
	if opts.Before == nil {
		t.Fatalf("Before not merged from AND conjunct: %+v", opts)
	}
	if got := opts.Before.Format("2006-01-02"); got != "2009-01-01" {
		t.Fatalf("Before = %v, want 2009-01-01", got)
	}
	if len(opts.InMailboxOtherThan) != 2 {
		t.Fatalf("InMailboxOtherThan len = %d, want 2 (%+v)", len(opts.InMailboxOtherThan), opts)
	}
	if opts.InMailboxOtherThan[0] != 7 || opts.InMailboxOtherThan[1] != 8 {
		t.Fatalf("InMailboxOtherThan = %v, want [7 8]", opts.InMailboxOtherThan)
	}
}

// TestMergeFilterIntoOpts_AndWithUnpushableConjunct: a free-text
// search inside an AND must drop to the slow path. The SQL planner
// has no FTS hook on this code path; folding into FTS happens in
// query_fts.go for the slow path.
func TestMergeFilterIntoOpts_AndWithUnpushableConjunct(t *testing.T) {
	text := "invoice"
	before := "2009-01-01T00:00:00Z"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Before: &before}),
			mustMarshal(t, emailFilter{Text: &text}),
		},
	}

	var opts store.EmailQueryFastOpts
	if mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("AND containing a text-conjunct must NOT be pushable; got opts=%+v", opts)
	}
}

// TestMergeFilterIntoOpts_OrRefused: only AND folds. OR/NOT must
// drop to the slow path even when every conjunct is itself flat-pushable.
func TestMergeFilterIntoOpts_OrRefused(t *testing.T) {
	a := "2009-01-01T00:00:00Z"
	b := "2010-01-01T00:00:00Z"
	f := &emailFilter{
		Operator: "OR",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Before: &a}),
			mustMarshal(t, emailFilter{Before: &b}),
		},
	}

	var opts store.EmailQueryFastOpts
	if mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("OR must not be pushable")
	}
}

// TestMergeFilterIntoOpts_NestedAndRefused: the gate refuses
// double recursion. AND-inside-AND is the Phase 2 work; for now
// keep it strict.
func TestMergeFilterIntoOpts_NestedAndRefused(t *testing.T) {
	before := "2009-01-01T00:00:00Z"
	inner := emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Before: &before}),
		},
	}
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, inner),
			mustMarshal(t, emailFilter{InMailboxOtherThan: []jmapID{"7"}}),
		},
	}

	var opts store.EmailQueryFastOpts
	if mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("nested AND must not be pushable")
	}
}

// TestMergeFilterIntoOpts_ConflictingBefore: two conjuncts both
// setting Before to different values is a conflict. First-writer-wins
// → reject the merge. Correctness over cleverness.
func TestMergeFilterIntoOpts_ConflictingBefore(t *testing.T) {
	a := "2009-01-01T00:00:00Z"
	b := "2010-01-01T00:00:00Z"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Before: &a}),
			mustMarshal(t, emailFilter{Before: &b}),
		},
	}

	var opts store.EmailQueryFastOpts
	if mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("conflicting Before predicates must refuse the merge")
	}
}

// TestMergeFilterIntoOpts_DuplicateBeforeIsFine: two conjuncts both
// setting Before to the same value is harmless and must remain
// pushable. RFC 8621 §5.5 makes it equivalent to a single Before
// predicate.
func TestMergeFilterIntoOpts_DuplicateBeforeIsFine(t *testing.T) {
	a := "2009-01-01T00:00:00Z"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{Before: &a}),
			mustMarshal(t, emailFilter{Before: &a}),
		},
	}

	var opts store.EmailQueryFastOpts
	if !mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("duplicate identical Before predicates must remain pushable")
	}
}

// TestMergeFilterIntoOpts_EmptyAnd: a degenerate AND-of-nothing is
// "match all"; pushable as a no-op.
func TestMergeFilterIntoOpts_EmptyAnd(t *testing.T) {
	f := &emailFilter{Operator: "AND"}

	var opts store.EmailQueryFastOpts
	if !mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("empty AND must be pushable as a no-op")
	}
	if opts.Before != nil || opts.After != nil ||
		opts.InMailbox != nil || len(opts.InMailboxOtherThan) > 0 {
		t.Fatalf("empty AND must not populate opts; got %+v", opts)
	}
}

// TestMergeFilterIntoOpts_AndOfHasKeywordAndInMailboxOtherThan: the
// suite's "Important + exclude trash/junk" path. Both conjuncts are
// flat-pushable, so the gate must accept the AND.
func TestMergeFilterIntoOpts_AndOfHasKeywordAndInMailboxOtherThan(t *testing.T) {
	important := "$important"
	f := &emailFilter{
		Operator: "AND",
		Conditions: []json.RawMessage{
			mustMarshal(t, emailFilter{HasKeyword: &important}),
			mustMarshal(t, emailFilter{InMailboxOtherThan: []jmapID{"7", "8"}}),
		},
	}

	var opts store.EmailQueryFastOpts
	if !mergeFilterIntoOpts(f, &opts) {
		t.Fatalf("AND of hasKeyword + inMailboxOtherThan must be pushable")
	}
	if opts.HasKeyword != "$important" {
		t.Fatalf("HasKeyword = %q, want $important", opts.HasKeyword)
	}
	if len(opts.InMailboxOtherThan) != 2 {
		t.Fatalf("InMailboxOtherThan len = %d, want 2", len(opts.InMailboxOtherThan))
	}
}

// TestMergeFilterIntoOpts_NilFilterIsNoOp: a nil filter (no filter
// supplied at all) is the inbox's default page; pushable as a no-op.
func TestMergeFilterIntoOpts_NilFilterIsNoOp(t *testing.T) {
	var opts store.EmailQueryFastOpts
	if !mergeFilterIntoOpts(nil, &opts) {
		t.Fatalf("nil filter must be pushable")
	}
}
