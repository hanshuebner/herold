package email_test

// notinmailbox_test.go -- the herold `notInMailbox` Email/query filter
// condition (issue #467). A message that carries a Junk membership
// must be hidden from the inbox view even when it also sits in Inbox,
// on both the SQL fast path and the Go-side slow path, and the two
// paths must agree exactly.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// TestEmail_Query_NotInMailbox_FastAndSlowPathAgree pins the wire
// contract: `{inMailbox: <inbox>, notInMailbox: [<junk>]}` excludes a
// message that holds memberships in both Inbox and Junk, and returns a
// message that is in Inbox only. The bare filter (no unpushable
// predicate) reaches the SQL fast path (fastquery.go); adding a `text`
// predicate that matches every candidate forces the Go-side slow path
// (query.go's matchConditionWithAttachments) without changing which
// messages should match, so the two results must be identical.
func TestEmail_Query_NotInMailbox_FastAndSlowPathAgree(t *testing.T) {
	testEmail_Query_NotInMailbox_FastAndSlowPathAgree(t, setupFixture(t))
}

// TestEmail_Query_NotInMailbox_FastAndSlowPathAgree_Postgres is the
// Postgres leg: notInMailbox is implemented independently in
// storesqlite and storepg's QueryEmailFast, so both backends need
// direct coverage. Skips when HEROLD_PG_DSN is not set.
func TestEmail_Query_NotInMailbox_FastAndSlowPathAgree_Postgres(t *testing.T) {
	testEmail_Query_NotInMailbox_FastAndSlowPathAgree(t, setupFixturePostgres(t))
}

func testEmail_Query_NotInMailbox_FastAndSlowPathAgree(t *testing.T, f *fixture) {
	ctx := context.Background()

	junk, err := f.srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.pid,
		Name:        "Junk",
		Attributes:  store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox Junk: %v", err)
	}

	// inboxAndJunk: filed in Inbox (insertMessage's default) and Junk.
	// A classifier verdict adds the Junk membership while the Inbox
	// membership survives -- exactly the shape notInMailbox must hide.
	inboxAndJunk := f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: inboxAndJunk\r\n\r\nbody",
		"inboxAndJunk", "a@example.test", "b@example.test", nil, "quisquam searchterm inboxandjunk")
	if _, _, err := f.srv.Store.Meta().AddMessageToMailbox(ctx, inboxAndJunk.ID, junk.ID); err != nil {
		t.Fatalf("AddMessageToMailbox junk (inboxAndJunk): %v", err)
	}

	// inboxOnly: filed in Inbox only. Must be returned.
	inboxOnly := f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: inboxOnly\r\n\r\nbody",
		"inboxOnly", "a@example.test", "b@example.test", nil, "quisquam searchterm inboxonly")

	inboxJmapID := fmt.Sprintf("%d", f.inbox.ID)
	junkJmapID := fmt.Sprintf("%d", junk.ID)
	wantID := fmt.Sprintf("%d", inboxOnly.ID)

	// Fast path: bare {inMailbox, notInMailbox}, no unpushable predicate.
	start := time.Now()
	_, fastRaw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter": map[string]any{
			"inMailbox":    inboxJmapID,
			"notInMailbox": []string{junkJmapID},
		},
	})
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Fatalf("notInMailbox Email/query took %v, want <1s (fast path not engaged?)", elapsed)
	}
	var fastResp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(fastRaw, &fastResp); err != nil {
		t.Fatalf("unmarshal fast: %v: %s", err, fastRaw)
	}
	if len(fastResp.IDs) != 1 || fastResp.IDs[0] != wantID {
		t.Fatalf("fast-path ids = %v, want [%s] (inboxAndJunk %d must stay excluded) (raw=%s)",
			fastResp.IDs, wantID, inboxAndJunk.ID, fastRaw)
	}

	// Slow path: add a `text` predicate matching both candidate
	// messages, which is not SQL-pushable and forces
	// matchConditionWithAttachments to evaluate notInMailbox in Go.
	_, slowRaw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter": map[string]any{
			"text":         "quisquam searchterm",
			"inMailbox":    inboxJmapID,
			"notInMailbox": []string{junkJmapID},
		},
	})
	var slowResp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(slowRaw, &slowResp); err != nil {
		t.Fatalf("unmarshal slow: %v: %s", err, slowRaw)
	}
	if len(slowResp.IDs) != len(fastResp.IDs) || (len(slowResp.IDs) > 0 && slowResp.IDs[0] != fastResp.IDs[0]) {
		t.Fatalf("fast-path and slow-path ids differ: fast=%v slow=%v", fastResp.IDs, slowResp.IDs)
	}
}

// TestEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments asserts
// that notInMailbox referencing a mailbox id that does not exist is
// rejected with invalidArguments, the same rule other JMAP mailbox
// references apply (e.g. Email/set's mailboxIds), rather than silently
// matching every candidate.
func TestEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments(t *testing.T) {
	testEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments(t, setupFixture(t))
}

// TestEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments_Postgres
// is the Postgres leg: the validation calls store.Metadata.GetMailboxByID,
// so both backends need direct coverage. Skips when HEROLD_PG_DSN is
// not set.
func TestEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments_Postgres(t *testing.T) {
	testEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments(t, setupFixturePostgres(t))
}

func testEmail_Query_NotInMailbox_UnknownMailbox_InvalidArguments(t *testing.T, f *fixture) {
	_ = f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody",
		"s", "a@example.test", "b@example.test", nil, "")

	name, raw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter": map[string]any{
			"notInMailbox": []string{"999999999"},
		},
	})
	if name != "error" {
		t.Fatalf("notInMailbox with unknown mailbox id: expected method-level error, got %q (raw=%s)", name, raw)
	}
	var got struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if got.Type != "invalidArguments" {
		t.Fatalf("type = %q, want invalidArguments (raw=%s)", got.Type, raw)
	}
}

// TestEmail_QueryChanges_NotInMailbox asserts that Email/queryChanges
// honours notInMailbox the same way Email/query does: a message
// created with an Inbox+Junk membership never appears in `added`.
func TestEmail_QueryChanges_NotInMailbox(t *testing.T) {
	testEmail_QueryChanges_NotInMailbox(t, setupFixture(t))
}

// TestEmail_QueryChanges_NotInMailbox_Postgres is the Postgres leg:
// queryChangesHandler shares matchConditionWithAttachments with
// Email/query, but exercises a different code path (gatherCandidates +
// the change-feed walk), so both backends need direct coverage. Skips
// when HEROLD_PG_DSN is not set.
func TestEmail_QueryChanges_NotInMailbox_Postgres(t *testing.T) {
	testEmail_QueryChanges_NotInMailbox(t, setupFixturePostgres(t))
}

func testEmail_QueryChanges_NotInMailbox(t *testing.T, f *fixture) {
	ctx := context.Background()

	junk, err := f.srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.pid,
		Name:        "Junk",
		Attributes:  store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox Junk: %v", err)
	}
	inboxJmapID := fmt.Sprintf("%d", f.inbox.ID)
	junkJmapID := fmt.Sprintf("%d", junk.ID)
	filter := map[string]any{
		"inMailbox":    inboxJmapID,
		"notInMailbox": []string{junkJmapID},
	}

	_, queryRaw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    filter,
	})
	var queryResp struct {
		QueryState string `json:"queryState"`
	}
	if err := json.Unmarshal(queryRaw, &queryResp); err != nil {
		t.Fatalf("unmarshal initial query: %v: %s", err, queryRaw)
	}

	// inboxAndJunk: created after the initial query state, filed in
	// both Inbox and Junk. Must never appear in `added`.
	inboxAndJunk := f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: inboxAndJunk\r\n\r\nbody",
		"inboxAndJunk", "a@example.test", "b@example.test", nil, "")
	if _, _, err := f.srv.Store.Meta().AddMessageToMailbox(ctx, inboxAndJunk.ID, junk.ID); err != nil {
		t.Fatalf("AddMessageToMailbox junk (inboxAndJunk): %v", err)
	}

	// inboxOnly: created after the initial query state, filed in Inbox
	// only. Must appear in `added`.
	inboxOnly := f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: inboxOnly\r\n\r\nbody",
		"inboxOnly", "a@example.test", "b@example.test", nil, "")

	_, changesRaw := f.invoke(t, "Email/queryChanges", map[string]any{
		"accountId":       protojmap.AccountIDForPrincipal(f.pid),
		"filter":          filter,
		"sinceQueryState": queryResp.QueryState,
	})
	var changesResp struct {
		Added []struct {
			ID string `json:"id"`
		} `json:"added"`
	}
	if err := json.Unmarshal(changesRaw, &changesResp); err != nil {
		t.Fatalf("unmarshal queryChanges: %v: %s", err, changesRaw)
	}

	wantID := fmt.Sprintf("%d", inboxOnly.ID)
	excludedID := fmt.Sprintf("%d", inboxAndJunk.ID)
	var gotIDs []string
	for _, a := range changesResp.Added {
		gotIDs = append(gotIDs, a.ID)
		if a.ID == excludedID {
			t.Fatalf("added contains %s (Inbox+Junk message must stay excluded): %v (raw=%s)",
				excludedID, changesResp.Added, changesRaw)
		}
	}
	if len(gotIDs) != 1 || gotIDs[0] != wantID {
		t.Fatalf("added ids = %v, want [%s] (raw=%s)", gotIDs, wantID, changesRaw)
	}
}

// insertForeignMailbox creates a second principal owning a private
// mailbox unrelated to f.pid's account, and returns its id. When grant
// is true, the mailbox carries an ACLRightLookup grant to f.pid (the
// load_visibility_test.go shared-mailbox fixture pattern); when false,
// f.pid has no access to it at all.
func insertForeignMailbox(t *testing.T, f *fixture, grant bool) store.MailboxID {
	t.Helper()
	ctx := context.Background()
	ownerEmail := fmt.Sprintf("foreign-owner-%d@example.test", time.Now().UnixNano())
	owner, err := f.srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: ownerEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal foreign owner: %v", err)
	}
	mb, err := f.srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: owner.ID,
		Name:        "ForeignPrivate",
	})
	if err != nil {
		t.Fatalf("InsertMailbox foreign: %v", err)
	}
	if grant {
		if err := f.srv.Store.Meta().SetMailboxACL(ctx, mb.ID, &f.pid, store.ACLRightLookup, owner.ID); err != nil {
			t.Fatalf("SetMailboxACL: %v", err)
		}
	}
	return mb.ID
}

// assertInvalidArgumentsMatchingUnknownID invokes method with a
// notInMailbox filter naming foreignMailboxID and asserts the response
// is invalidArguments with the exact description the unknown-mailbox-id
// case produces: a caller must not be able to tell "this mailbox exists
// but I cannot see it" apart from "this id does not exist" (issue #467
// follow-up -- GetMailboxByID alone is a cross-tenant existence oracle).
func assertInvalidArgumentsMatchingUnknownID(t *testing.T, f *fixture, method string, extraArgs map[string]any, foreignMailboxID store.MailboxID) {
	t.Helper()
	args := map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter": map[string]any{
			"notInMailbox": []string{fmt.Sprintf("%d", foreignMailboxID)},
		},
	}
	for k, v := range extraArgs {
		args[k] = v
	}
	name, raw := f.invoke(t, method, args)
	if name != "error" {
		t.Fatalf("%s notInMailbox on a non-visible foreign mailbox: expected method-level error, got %q (raw=%s)", method, name, raw)
	}
	var got struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if got.Type != "invalidArguments" {
		t.Fatalf("type = %q, want invalidArguments (raw=%s)", got.Type, raw)
	}

	unknownArgs := map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter": map[string]any{
			"notInMailbox": []string{"999999999"},
		},
	}
	for k, v := range extraArgs {
		unknownArgs[k] = v
	}
	unknownName, unknownRaw := f.invoke(t, method, unknownArgs)
	if unknownName != "error" {
		t.Fatalf("%s notInMailbox on an unknown mailbox id: expected method-level error, got %q (raw=%s)", method, unknownName, unknownRaw)
	}
	var unknownGot struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(unknownRaw, &unknownGot); err != nil {
		t.Fatalf("unmarshal unknown: %v: %s", err, unknownRaw)
	}
	if unknownGot.Description != got.Description {
		t.Fatalf("foreign-mailbox and unknown-id responses differ (%q vs %q) -- distinguishable responses let a caller probe mailbox existence across tenants",
			got.Description, unknownGot.Description)
	}
}

// TestEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments asserts
// that a notInMailbox id naming a mailbox that exists but belongs to a
// different account, with no ACL grant to the caller, is rejected with
// invalidArguments -- indistinguishable from a nonexistent id. Without
// this check, GetMailboxByID's unscoped existence lookup lets a caller
// binary-search mailbox ids system-wide by reading "200 with results"
// vs "invalidArguments" as a cross-tenant existence oracle.
func TestEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments(t *testing.T) {
	testEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments(t, setupFixture(t))
}

// TestEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments_Postgres
// is the Postgres leg: the visibility check calls
// store.Metadata.GetMailboxACL / HasOwnerAccess, so both backends need
// direct coverage. Skips when HEROLD_PG_DSN is not set.
func TestEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments_Postgres(t *testing.T) {
	testEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments(t, setupFixturePostgres(t))
}

func testEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments(t *testing.T, f *fixture) {
	_ = f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody",
		"s", "a@example.test", "b@example.test", nil, "")
	foreignID := insertForeignMailbox(t, f, false)
	assertInvalidArgumentsMatchingUnknownID(t, f, "Email/query", nil, foreignID)
}

// TestEmail_Query_NotInMailbox_SharedMailbox_Accepted asserts that a
// notInMailbox id naming a mailbox shared to the caller via an
// ACLRightLookup grant is accepted -- the caller can legitimately see
// that mailbox, so it is not treated as unknown/foreign.
func TestEmail_Query_NotInMailbox_SharedMailbox_Accepted(t *testing.T) {
	testEmail_Query_NotInMailbox_SharedMailbox_Accepted(t, setupFixture(t))
}

// TestEmail_Query_NotInMailbox_SharedMailbox_Accepted_Postgres is the
// Postgres leg. Skips when HEROLD_PG_DSN is not set.
func TestEmail_Query_NotInMailbox_SharedMailbox_Accepted_Postgres(t *testing.T) {
	testEmail_Query_NotInMailbox_SharedMailbox_Accepted(t, setupFixturePostgres(t))
}

func testEmail_Query_NotInMailbox_SharedMailbox_Accepted(t *testing.T, f *fixture) {
	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody",
		"s", "a@example.test", "b@example.test", nil, "")
	sharedID := insertForeignMailbox(t, f, true)

	name, raw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter": map[string]any{
			"notInMailbox": []string{fmt.Sprintf("%d", sharedID)},
		},
	})
	if name == "error" {
		t.Fatalf("notInMailbox on a Lookup-shared mailbox must be accepted, got error: %s", raw)
	}
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	wantID := fmt.Sprintf("%d", m.ID)
	if len(resp.IDs) != 1 || resp.IDs[0] != wantID {
		t.Fatalf("ids = %v, want [%s] (raw=%s)", resp.IDs, wantID, raw)
	}
}

// TestEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments
// is the Email/queryChanges leg of
// TestEmail_Query_NotInMailbox_ForeignMailbox_InvalidArguments: the
// same validation runs on both methods, so both need the same
// cross-tenant-oracle check pinned.
func TestEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments(t *testing.T) {
	testEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments(t, setupFixture(t))
}

// TestEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments_Postgres
// is the Postgres leg. Skips when HEROLD_PG_DSN is not set.
func TestEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments_Postgres(t *testing.T) {
	testEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments(t, setupFixturePostgres(t))
}

func testEmail_QueryChanges_NotInMailbox_ForeignMailbox_InvalidArguments(t *testing.T, f *fixture) {
	_ = f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody",
		"s", "a@example.test", "b@example.test", nil, "")
	foreignID := insertForeignMailbox(t, f, false)
	assertInvalidArgumentsMatchingUnknownID(t, f, "Email/queryChanges",
		map[string]any{"sinceQueryState": "0"}, foreignID)
}

// TestEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted is the
// Email/queryChanges leg of
// TestEmail_Query_NotInMailbox_SharedMailbox_Accepted.
func TestEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted(t *testing.T) {
	testEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted(t, setupFixture(t))
}

// TestEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted_Postgres
// is the Postgres leg. Skips when HEROLD_PG_DSN is not set.
func TestEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted_Postgres(t *testing.T) {
	testEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted(t, setupFixturePostgres(t))
}

func testEmail_QueryChanges_NotInMailbox_SharedMailbox_Accepted(t *testing.T, f *fixture) {
	sharedID := insertForeignMailbox(t, f, true)
	filter := map[string]any{
		"notInMailbox": []string{fmt.Sprintf("%d", sharedID)},
	}

	_, queryRaw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    filter,
	})
	var queryResp struct {
		QueryState string `json:"queryState"`
	}
	if err := json.Unmarshal(queryRaw, &queryResp); err != nil {
		t.Fatalf("unmarshal initial query: %v: %s", err, queryRaw)
	}

	m := f.insertMessage(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: s\r\n\r\nbody",
		"s", "a@example.test", "b@example.test", nil, "")

	name, changesRaw := f.invoke(t, "Email/queryChanges", map[string]any{
		"accountId":       protojmap.AccountIDForPrincipal(f.pid),
		"filter":          filter,
		"sinceQueryState": queryResp.QueryState,
	})
	if name == "error" {
		t.Fatalf("notInMailbox on a Lookup-shared mailbox must be accepted, got error: %s", changesRaw)
	}
	var changesResp struct {
		Added []struct {
			ID string `json:"id"`
		} `json:"added"`
	}
	if err := json.Unmarshal(changesRaw, &changesResp); err != nil {
		t.Fatalf("unmarshal queryChanges: %v: %s", err, changesRaw)
	}
	wantID := fmt.Sprintf("%d", m.ID)
	if len(changesResp.Added) != 1 || changesResp.Added[0].ID != wantID {
		t.Fatalf("added ids = %v, want [%s] (raw=%s)", changesResp.Added, wantID, changesRaw)
	}
}
