package storetest

// storetest_categorylabelmailbox.go -- compliance cases for issue #406:
// SetDerivedCategories ensures a label mailbox exists per derived category
// (ADR-0004 "a category is a label"), defaulting to MailboxDispositionPinned
// with a dense priority in the derived set's order, adopting an existing
// label untouched, and never overwriting a disposition or priority the user
// has since changed.

import (
	"errors"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// testSetDerivedCategoriesCreatesLabelMailboxes verifies that the first
// successful SetDerivedCategories call for a principal creates one pinned,
// densely-prioritised label mailbox per derived category, in order, and
// that the mailbox creations are visible on the principal's change feed
// (so Mailbox/changes reports the new tabs).
func testSetDerivedCategoriesCreatesLabelMailboxes(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-label-create@example.com")

	seed, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	cats := []string{"primary", "social", "promotions", "updates", "forums"}

	feedBefore, err := s.Meta().ReadChangeFeed(ctx, p.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed before: %v", err)
	}
	var cursor store.ChangeSeq
	if len(feedBefore) > 0 {
		cursor = feedBefore[len(feedBefore)-1].Seq
	}

	ok, err := s.Meta().SetDerivedCategories(ctx, p.ID, cats, seed.DerivedCategoriesEpoch)
	if err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}
	if !ok {
		t.Fatalf("SetDerivedCategories: expected hit, got miss")
	}

	created := make(map[string]store.Mailbox, len(cats))
	for i, name := range cats {
		mb, err := s.Meta().GetMailboxByName(ctx, p.ID, name)
		if err != nil {
			t.Fatalf("GetMailboxByName(%q): %v", name, err)
		}
		if mb.Disposition != store.MailboxDispositionPinned {
			t.Errorf("mailbox %q Disposition = %q, want %q", name, mb.Disposition, store.MailboxDispositionPinned)
		}
		if mb.Priority == nil || *mb.Priority != i {
			t.Errorf("mailbox %q Priority = %v, want %d", name, mb.Priority, i)
		}
		created[name] = mb
	}

	feedAfter, err := s.Meta().ReadChangeFeed(ctx, p.ID, cursor, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed after: %v", err)
	}
	for name, mb := range created {
		var sawCreated bool
		for _, ch := range feedAfter {
			if ch.Kind == store.EntityKindMailbox && ch.EntityID == uint64(mb.ID) && ch.Op == store.ChangeOpCreated {
				sawCreated = true
			}
		}
		if !sawCreated {
			t.Errorf("change feed does not report mailbox %q created: %+v", name, feedAfter)
		}
	}

	// A second recompute with the same category set is deduplicated by
	// the categoriser caller before it ever reaches the store, but calling
	// SetDerivedCategories again directly (as this test does, bypassing
	// that dedup) must remain idempotent: no duplicate mailboxes, no
	// disposition/priority churn.
	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (re-read): %v", err)
	}
	if _, err := s.Meta().SetDerivedCategories(ctx, p.ID, cats, cfg.DerivedCategoriesEpoch); err != nil {
		t.Fatalf("SetDerivedCategories (repeat): %v", err)
	}
	for i, name := range cats {
		mb, err := s.Meta().GetMailboxByName(ctx, p.ID, name)
		if err != nil {
			t.Fatalf("GetMailboxByName(%q) after repeat: %v", name, err)
		}
		if mb.ID != created[name].ID {
			t.Errorf("mailbox %q ID changed across repeat recompute: %d -> %d (duplicate?)", name, created[name].ID, mb.ID)
		}
		if mb.Disposition != store.MailboxDispositionPinned {
			t.Errorf("mailbox %q Disposition after repeat = %q, want %q", name, mb.Disposition, store.MailboxDispositionPinned)
		}
		if mb.Priority == nil || *mb.Priority != i {
			t.Errorf("mailbox %q Priority after repeat = %v, want %d", name, mb.Priority, i)
		}
	}
}

// testSetDerivedCategoriesAdoptsExistingLabel verifies that a label the
// user already created (or that a prior recompute created) by the same
// name is adopted rather than duplicated, and that a disposition/priority
// the user set is preserved verbatim rather than reset to the pinned
// default.
func testSetDerivedCategoriesAdoptsExistingLabel(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-label-adopt@example.com")

	// The user hand-creates a "social" label and files it (disposition
	// filed, unranked) before the categoriser ever runs.
	existing, err := s.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "social",
		Disposition: store.MailboxDispositionFiled,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(social): %v", err)
	}

	seed, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	cats := []string{"primary", "social", "promotions"}
	ok, err := s.Meta().SetDerivedCategories(ctx, p.ID, cats, seed.DerivedCategoriesEpoch)
	if err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}
	if !ok {
		t.Fatalf("SetDerivedCategories: expected hit, got miss")
	}

	// "social" is adopted: same ID, disposition and priority untouched.
	got, err := s.Meta().GetMailboxByID(ctx, existing.ID)
	if err != nil {
		t.Fatalf("GetMailboxByID(social): %v", err)
	}
	if got.Disposition != store.MailboxDispositionFiled {
		t.Fatalf("adopted mailbox Disposition = %q, want unchanged %q", got.Disposition, store.MailboxDispositionFiled)
	}
	if got.Priority != nil {
		t.Fatalf("adopted mailbox Priority = %v, want unchanged nil", got.Priority)
	}
	byName, err := s.Meta().GetMailboxByName(ctx, p.ID, "social")
	if err != nil {
		t.Fatalf("GetMailboxByName(social): %v", err)
	}
	if byName.ID != existing.ID {
		t.Fatalf("GetMailboxByName(social) = mailbox %d, want the adopted mailbox %d (duplicate created)", byName.ID, existing.ID)
	}

	// "primary" and "promotions" are newly created and pinned, ranked
	// after the (unranked) adopted "social" -- i.e. starting at 0, since
	// "social" carries no priority to skip past.
	primary, err := s.Meta().GetMailboxByName(ctx, p.ID, "primary")
	if err != nil {
		t.Fatalf("GetMailboxByName(primary): %v", err)
	}
	if primary.Disposition != store.MailboxDispositionPinned || primary.Priority == nil || *primary.Priority != 0 {
		t.Errorf("primary = disposition %q priority %v, want pinned/0", primary.Disposition, primary.Priority)
	}
	promotions, err := s.Meta().GetMailboxByName(ctx, p.ID, "promotions")
	if err != nil {
		t.Fatalf("GetMailboxByName(promotions): %v", err)
	}
	if promotions.Disposition != store.MailboxDispositionPinned || promotions.Priority == nil || *promotions.Priority != 1 {
		t.Errorf("promotions = disposition %q priority %v, want pinned/1", promotions.Disposition, promotions.Priority)
	}
}

// testSetLLMClassificationCreatesLabelMailboxes verifies the live
// delivery hook (issue #406): SetLLMClassification -- the write path
// used by internal/protosmtp, the IMAP-import adapter, and the admin
// spam-reclassify/apply-verdicts tools, none of which call
// SetDerivedCategories directly -- ensures a label mailbox exists for
// the principal's configured category vocabulary the first time a
// message carries a category assignment.
func testSetLLMClassificationCreatesLabelMailboxes(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-label-llm@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "cat-label-llm@host")

	// Reading the config once seeds the default 5-category vocabulary
	// (primary/social/promotions/updates/forums), matching what the live
	// mail.classify delivery path does via buildClassifyContext before
	// the classify call.
	if _, err := s.Meta().GetCategorisationConfig(ctx, p.ID); err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}

	cat := "promotions"
	rec := store.LLMClassificationRecord{
		MessageID:        msg.ID,
		PrincipalID:      p.ID,
		CategoryAssigned: &cat,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (after): %v", err)
	}
	wantNames := []string{"primary", "social", "promotions", "updates", "forums"}
	if len(cfg.DerivedCategories) != len(wantNames) {
		t.Fatalf("DerivedCategories = %v, want %v", cfg.DerivedCategories, wantNames)
	}
	for i, name := range wantNames {
		mb, err := s.Meta().GetMailboxByName(ctx, p.ID, name)
		if err != nil {
			t.Fatalf("GetMailboxByName(%q): %v", name, err)
		}
		if mb.Disposition != store.MailboxDispositionPinned {
			t.Errorf("mailbox %q Disposition = %q, want pinned", name, mb.Disposition)
		}
		if mb.Priority == nil || *mb.Priority != i {
			t.Errorf("mailbox %q Priority = %v, want %d", name, mb.Priority, i)
		}
	}
}

// testSetLLMClassificationDoesNotCreateLabelWithNoCategory verifies that
// a spam-only classification record (no category assigned) never
// triggers the label-mailbox-ensure hook.
func testSetLLMClassificationDoesNotCreateLabelWithNoCategory(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-label-llm-nocat@example.com")
	mb := mustInsertMailbox(t, s, p.ID, "INBOX")
	msg := mustInsertMessage(t, s, mb.ID, "cat-label-llm-nocat@host")

	verdict := "spam"
	rec := store.LLMClassificationRecord{
		MessageID:   msg.ID,
		PrincipalID: p.ID,
		SpamVerdict: &verdict,
	}
	if err := s.Meta().SetLLMClassification(ctx, rec); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	if len(cfg.DerivedCategories) != 0 {
		t.Fatalf("DerivedCategories = %v, want empty (no category assigned)", cfg.DerivedCategories)
	}
	if _, err := s.Meta().GetMailboxByName(ctx, p.ID, "primary"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMailboxByName(primary) = %v, want ErrNotFound (no label should have been created)", err)
	}
}

// testSetDerivedCategoriesUserDispositionSurvivesRecompute verifies that
// once a category label exists, a user-set disposition change on it is
// never reverted by a later recompute that still lists the category (a
// rename/removal in the derived set leaving the label and its disposition
// intact is the same code path: SetDerivedCategories never touches a
// mailbox that already exists by that name).
func testSetDerivedCategoriesUserDispositionSurvivesRecompute(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "cat-label-survive@example.com")

	cfg0, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	cats := []string{"primary", "updates"}
	if _, err := s.Meta().SetDerivedCategories(ctx, p.ID, cats, cfg0.DerivedCategoriesEpoch); err != nil {
		t.Fatalf("SetDerivedCategories (first recompute): %v", err)
	}
	updates, err := s.Meta().GetMailboxByName(ctx, p.ID, "updates")
	if err != nil {
		t.Fatalf("GetMailboxByName(updates): %v", err)
	}
	if updates.Disposition != store.MailboxDispositionPinned {
		t.Fatalf("updates Disposition = %q, want pinned default", updates.Disposition)
	}

	// The user demotes "updates" to bundled.
	if err := s.Meta().SetMailboxDisposition(ctx, updates.ID, store.MailboxDispositionBundled); err != nil {
		t.Fatalf("SetMailboxDisposition(updates, bundled): %v", err)
	}

	// A later recompute that still lists "updates" (rename or removal of
	// some other category in the set does not touch it) must not revert
	// the user's choice, whether or not "updates" itself is still present.
	for _, nextCats := range [][]string{
		{"primary", "updates", "social"}, // updates still present
		{"primary", "social"},            // updates removed from the set
	} {
		cfg, err := s.Meta().GetCategorisationConfig(ctx, p.ID)
		if err != nil {
			t.Fatalf("GetCategorisationConfig: %v", err)
		}
		if _, err := s.Meta().SetDerivedCategories(ctx, p.ID, nextCats, cfg.DerivedCategoriesEpoch); err != nil {
			t.Fatalf("SetDerivedCategories(%v): %v", nextCats, err)
		}
		got, err := s.Meta().GetMailboxByID(ctx, updates.ID)
		if err != nil {
			t.Fatalf("GetMailboxByID(updates) after %v: %v", nextCats, err)
		}
		if got.Disposition != store.MailboxDispositionBundled {
			t.Fatalf("updates Disposition after recompute %v = %q, want the user-set %q",
				nextCats, got.Disposition, store.MailboxDispositionBundled)
		}
	}
}
