package storetest

// storetest_senderrules.go -- compliance cases for per-sender category
// rules (issue #555): the rules persist with the categorisation config
// and survive derived-category writes, and ListMessagesFromCandidates
// pages through one principal's messages by From substring.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

func testSenderRulesRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "rules-roundtrip@example.com")

	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	if len(cfg.SenderRules) != 0 {
		t.Fatalf("fresh config SenderRules = %v, want none", cfg.SenderRules)
	}
	want := []store.SenderCategoryRule{
		{Sender: "news@shop.example", Category: "promotions"},
		{Sender: "bob@sender.example", Category: "social"},
	}
	cfg.SenderRules = want
	if err := s.Meta().UpdateCategorisationConfig(ctx, cfg); err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}
	got, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig after write: %v", err)
	}
	if fmt.Sprint(got.SenderRules) != fmt.Sprint(want) {
		t.Fatalf("SenderRules = %v, want %v", got.SenderRules, want)
	}

	// SetDerivedCategories touches only the derived list.
	if _, err := s.Meta().SetDerivedCategories(ctx, p.ID, []string{"primary", "social"}, got.DerivedCategoriesEpoch); err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}
	got, err = s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SenderRules) != 2 {
		t.Fatalf("SenderRules after SetDerivedCategories = %v, want 2 rules", got.SenderRules)
	}

	// Clearing the list persists as empty.
	got.SenderRules = nil
	if err := s.Meta().UpdateCategorisationConfig(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, err = s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SenderRules) != 0 {
		t.Fatalf("SenderRules after clear = %v, want none", got.SenderRules)
	}
}

func testListMessagesFromCandidates(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "from-cand@example.com")
	other := mustInsertPrincipal(t, s, "from-cand-other@example.com")
	inbox := mustInsertMailboxWithAttrs(t, s, p.ID, "INBOX", store.MailboxAttrInbox)
	archive := mustInsertMailboxWithAttrs(t, s, p.ID, "Archive", store.MailboxAttrArchive)
	trash := mustInsertMailboxWithAttrs(t, s, p.ID, "Trash", store.MailboxAttrTrash)
	junk := mustInsertMailboxWithAttrs(t, s, p.ID, "Junk", store.MailboxAttrJunk)
	otherInbox := mustInsertMailboxWithAttrs(t, s, other.ID, "INBOX", store.MailboxAttrInbox)

	insert := func(pid store.PrincipalID, from string, mbs ...store.MailboxID) {
		t.Helper()
		body := "From: " + from + "\r\nSubject: s\r\n\r\nb\r\n"
		ref := putBlob(t, s, body)
		mm := make([]store.MessageMailbox, len(mbs))
		for i, id := range mbs {
			mm[i] = store.MessageMailbox{MailboxID: id}
		}
		if _, _, err := s.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:  pid,
			Size:         ref.Size,
			Blob:         ref,
			ReceivedAt:   time.Now().UTC(),
			InternalDate: time.Now().UTC(),
			Envelope:     store.Envelope{From: from, Subject: "s"},
		}, mm); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}
	insert(p.ID, "Bob <Bob@Sender.Example>", inbox.ID, archive.ID) // two memberships
	insert(p.ID, "bob@sender.example", inbox.ID)
	insert(p.ID, "bob@sender.example", trash.ID)   // excluded: Trash only
	insert(p.ID, "bob@sender.example", junk.ID)    // excluded: Junk only
	insert(p.ID, "carol@sender.example", inbox.ID) // different sender
	insert(other.ID, "bob@sender.example", otherInbox.ID)

	// One page holds everything: 3 memberships over 2 messages.
	rows, err := s.Meta().ListMessagesFromCandidates(ctx, p.ID, "BOB@sender.example", 0, 100)
	if err != nil {
		t.Fatalf("ListMessagesFromCandidates: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (two memberships + one)", len(rows))
	}
	for _, r := range rows {
		if r.PrincipalID != p.ID {
			t.Errorf("row for message %d belongs to principal %d, want %d", r.ID, r.PrincipalID, p.ID)
		}
		if !strings.Contains(strings.ToLower(r.Envelope.From), "bob@sender.example") {
			t.Errorf("row From = %q does not match", r.Envelope.From)
		}
		if r.MailboxID == trash.ID || r.MailboxID == junk.ID {
			t.Errorf("row in excluded mailbox %d", r.MailboxID)
		}
	}

	// Paging by message id: limit 1 returns the first message's
	// memberships, then the second's, then nothing.
	page1, err := s.Meta().ListMessagesFromCandidates(ctx, p.ID, "bob@sender.example", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].ID != page1[1].ID {
		t.Fatalf("page 1 = %d rows, want both memberships of one message", len(page1))
	}
	page2, err := s.Meta().ListMessagesFromCandidates(ctx, p.ID, "bob@sender.example", page1[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].ID <= page1[0].ID {
		t.Fatalf("page 2 = %v, want one later message", page2)
	}
	page3, err := s.Meta().ListMessagesFromCandidates(ctx, p.ID, "bob@sender.example", page2[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page3) != 0 {
		t.Fatalf("page 3 = %d rows, want none", len(page3))
	}

	// SQL wildcards in the needle are literal.
	rows, err = s.Meta().ListMessagesFromCandidates(ctx, p.ID, "%", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("needle %% matched %d rows, want 0", len(rows))
	}
}
