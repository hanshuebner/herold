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
