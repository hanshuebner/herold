package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// An Email/query without an inMailbox condition lists each email once,
// however many mailboxes it belongs to (RFC 8621 section 4.4: the
// query result is a list of Email ids). The SQL fast path serves the
// unfiltered query, the inMailboxOtherThan query, the keyword queries
// and the total; each is checked here.
func TestEmail_Query_WithoutInMailbox_ListsMultiMembershipEmailOnce(t *testing.T) {
	testEmail_Query_WithoutInMailbox_ListsMultiMembershipEmailOnce(t, setupFixture(t))
}

func TestEmail_Query_WithoutInMailbox_ListsMultiMembershipEmailOnce_Postgres(t *testing.T) {
	testEmail_Query_WithoutInMailbox_ListsMultiMembershipEmailOnce(t, setupFixturePostgres(t))
}

func testEmail_Query_WithoutInMailbox_ListsMultiMembershipEmailOnce(t *testing.T, f *fixture) {
	ctx := context.Background()
	label, err := f.srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: f.pid, Name: "Label"})
	if err != nil {
		t.Fatalf("InsertMailbox Label: %v", err)
	}
	archive, err := f.srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: f.pid, Name: "Archive"})
	if err != nil {
		t.Fatalf("InsertMailbox Archive: %v", err)
	}

	// multi sits in INBOX, Label and Archive; $seen on its canonical
	// (lowest mailbox id, INBOX) membership only. single sits in INBOX.
	multi := f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: multi\r\n\r\nbody",
		"multi", "a@example.test", "b@example.test", nil, "")
	for _, mb := range []store.Mailbox{label, archive} {
		if _, _, err := f.srv.Store.Meta().AddMessageToMailbox(ctx, multi.ID, mb.ID); err != nil {
			t.Fatalf("AddMessageToMailbox %s: %v", mb.Name, err)
		}
	}
	if _, err := f.srv.Store.Meta().UpdateMessageFlags(ctx, multi.ID, f.inbox.ID, store.MessageFlagSeen, 0, nil, nil, 0); err != nil {
		t.Fatalf("UpdateMessageFlags: %v", err)
	}
	single := f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: single\r\n\r\nbody",
		"single", "a@example.test", "b@example.test", nil, "")
	multiID := fmt.Sprintf("%d", multi.ID)
	singleID := fmt.Sprintf("%d", single.ID)
	account := protojmap.AccountIDForPrincipal(f.pid)

	type queryResp struct {
		IDs   []string `json:"ids"`
		Total *int     `json:"total"`
	}
	query := func(args map[string]any) queryResp {
		t.Helper()
		args["accountId"] = account
		_, raw := f.invoke(t, "Email/query", args)
		var r queryResp
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("unmarshal Email/query: %v: %s", err, raw)
		}
		return r
	}
	sameSet := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: ids = %v, want %v", name, got, want)
		}
		seen := map[string]bool{}
		for _, id := range got {
			if seen[id] {
				t.Fatalf("%s: duplicate id %s in %v", name, id, got)
			}
			seen[id] = true
		}
		for _, id := range want {
			if !seen[id] {
				t.Fatalf("%s: ids = %v, want %v", name, got, want)
			}
		}
	}

	r := query(map[string]any{"calculateTotal": true})
	sameSet("no filter", r.IDs, multiID, singleID)
	if r.Total == nil || *r.Total != 2 {
		t.Fatalf("no filter: total = %v, want 2", r.Total)
	}

	r = query(map[string]any{"limit": 1})
	sameSet("no filter, limit 1", r.IDs, singleID)
	r = query(map[string]any{"position": 1, "limit": 1})
	sameSet("no filter, position 1", r.IDs, multiID)

	r = query(map[string]any{"filter": map[string]any{"inMailboxOtherThan": []string{fmt.Sprintf("%d", label.ID)}}})
	sameSet("inMailboxOtherThan label", r.IDs, singleID)

	r = query(map[string]any{"filter": map[string]any{"hasKeyword": "$seen"}})
	sameSet("hasKeyword $seen", r.IDs, multiID)
	r = query(map[string]any{"filter": map[string]any{"notKeyword": "$seen"}})
	sameSet("notKeyword $seen", r.IDs, singleID)

	r = query(map[string]any{"collapseThreads": true})
	sameSet("collapseThreads", r.IDs, multiID, singleID)
}
