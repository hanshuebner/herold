package email_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// Issue #553. A reminder lives on exactly one membership of a
// multi-mailbox message. Removing that membership must hand the pair to
// a remaining one, and moving the pair between memberships must never be
// observable as "on both" or "on neither" by the wake sweep.

func TestEmailSet_RemoveSnoozeHoldingMembership_KeepsReminder_SQLite(t *testing.T) {
	testRemoveSnoozeHoldingMembership(t, setupFixture(t))
}

func TestEmailSet_RemoveSnoozeHoldingMembership_KeepsReminder_Postgres(t *testing.T) {
	testRemoveSnoozeHoldingMembership(t, setupFixturePostgres(t))
}

func TestEmailSet_ConsolidateSnooze_WakeSweepSeesOneHolder_SQLite(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	st, err := storesqlite.Open(context.Background(), filepath.Join(dir, "h.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testConsolidateWakeSweep(t, st, clk, "alice@example.test")
}

func TestEmailSet_ConsolidateSnooze_WakeSweepSeesOneHolder_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testConsolidateWakeSweep(t, st, clk, fmt.Sprintf("alice-%d@example.test", time.Now().UnixNano()))
}

// dueCount reports how many due reminders a wake sweep at 2031 lists for message id
// (a shared Postgres database may hold other tests' reminders).
func dueCount(t *testing.T, f *fixture, id store.MessageID) int {
	t.Helper()
	due, err := f.srv.Store.Meta().ListDueSnoozedMessages(context.Background(),
		time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), 100)
	if err != nil {
		t.Fatalf("ListDueSnoozedMessages: %v", err)
	}
	n := 0
	for _, m := range due {
		if m.ID == id {
			n++
		}
	}
	return n
}

func testRemoveSnoozeHoldingMembership(t *testing.T, f *fixture) {
	s := newSnoozeScenario(t, f)
	a := s.mailbox("A")
	b := s.mailbox("B")
	s.set(map[string]any{"mailboxIds": map[string]bool{
		fmt.Sprintf("%d", a.ID): true, fmt.Sprintf("%d", b.ID): true,
	}})
	s.set(map[string]any{"snoozedUntil": "2030-06-01T12:00:00Z"})
	s.check("snoozed in A+B")

	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", a.ID): nil})
	gm, err := f.srv.Store.Meta().GetMessage(context.Background(), s.mid)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if len(gm.Mailboxes) != 1 || gm.Mailboxes[0].MailboxID != b.ID {
		t.Fatalf("memberships = %+v, want only B", gm.Mailboxes)
	}
	s.check("after removing A")
	if got := dueCount(t, f, s.mid); got != 1 {
		t.Errorf("wake sweep lists %d due reminders, want 1", got)
	}
}

// snoozeHookStore wraps a store so every SetSnooze call runs a hook right
// after it commits: the point between the two writes of a non-atomic
// consolidation.
type snoozeHookStore struct {
	store.Store
	meta *snoozeHookMeta
}

func (s *snoozeHookStore) Meta() store.Metadata { return s.meta }

type snoozeHookMeta struct {
	store.Metadata
	after func()
}

func (m *snoozeHookMeta) SetSnooze(ctx context.Context, id store.MessageID, mb store.MailboxID, when *time.Time, wake *store.MailboxID) (store.ModSeq, error) {
	ms, err := m.Metadata.SetSnooze(ctx, id, mb, when, wake)
	if m.after != nil {
		m.after()
	}
	return ms, err
}

func testConsolidateWakeSweep(t *testing.T, st store.Store, clk clock.Clock, canonicalEmail string) {
	hs := &snoozeHookStore{Store: st, meta: &snoozeHookMeta{Metadata: st.Meta()}}
	f := setupFixtureWithStore(t, hs, clk, canonicalEmail)
	s := newSnoozeScenario(t, f)
	archive := s.mailbox("Archive")
	s.set(map[string]any{"mailboxIds": map[string]bool{fmt.Sprintf("%d", archive.ID): true}})
	s.set(map[string]any{"snoozedUntil": "2030-06-01T12:00:00Z"})

	wakes := 0
	sweep := func() {
		ctx := context.Background()
		due, err := st.Meta().ListDueSnoozedMessages(ctx, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), 100)
		if err != nil {
			t.Errorf("ListDueSnoozedMessages: %v", err)
			return
		}
		for _, m := range due {
			if m.ID != s.mid {
				continue
			}
			if _, err := st.Meta().ReleaseSnooze(ctx, m.ID, m.MailboxID); err != nil {
				t.Errorf("ReleaseSnooze: %v", err)
				return
			}
			wakes++
		}
	}
	hs.meta.after = sweep

	// Adding Inbox (lower id) makes it the canonical membership; the
	// reminder is consolidated onto it while a sweep fires after every
	// reminder write.
	s.set(map[string]any{fmt.Sprintf("mailboxIds/%d", f.inbox.ID): true})
	hs.meta.after = nil
	sweep()
	if wakes != 1 {
		t.Errorf("message woke %d times across consolidation, want exactly 1", wakes)
	}
}
