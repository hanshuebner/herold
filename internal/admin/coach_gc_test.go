package admin

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	jmapcoach "github.com/hanshuebner/herold/internal/protojmap/coach"
	"github.com/hanshuebner/herold/internal/store"
)

// TestCoachGCWindow_ConstValue verifies that the exported GCWindow constant
// matches the 90-day value documented in REQ-PROTO-110. This guards against
// accidental modification; the window is load-bearing for the store's
// 90-day counter derivation in GetCoachStat.
func TestCoachGCWindow_ConstValue(t *testing.T) {
	want := 90 * 24 * time.Hour
	if jmapcoach.GCWindow != want {
		t.Errorf("GCWindow = %v; want %v", jmapcoach.GCWindow, want)
	}
}

// TestCoachGC_GCCoachEvents_DeletesOldRows verifies that the store's
// GCCoachEvents method deletes rows older than the cutoff. This is the
// underlying operation the admin server GC tick calls.
func TestCoachGC_GCCoachEvents_DeletesOldRows(t *testing.T) {
	ctx := context.Background()
	_, cfg := minimalConfigFixture(t)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	st, err := openStore(ctx, cfg, discardLogger(), fakeClock)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer st.Close()

	// Insert a principal so we can create coach events.
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@gc.test",
		DisplayName:    "Alice",
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	pid := p.ID

	// Append a coach event in the distant past (older than 90 days).
	oldTime := now.Add(-100 * 24 * time.Hour)
	if err := st.Meta().AppendCoachEvents(ctx, []store.CoachEvent{{
		PrincipalID: pid,
		Action:      "archive",
		Method:      store.CoachInputMethodKeyboard,
		Count:       1,
		OccurredAt:  oldTime,
		RecordedAt:  oldTime,
	}}); err != nil {
		t.Fatalf("AppendCoachEvents (old): %v", err)
	}

	// Append a recent coach event (within 90 days).
	recentTime := now.Add(-10 * 24 * time.Hour)
	if err := st.Meta().AppendCoachEvents(ctx, []store.CoachEvent{{
		PrincipalID: pid,
		Action:      "archive",
		Method:      store.CoachInputMethodKeyboard,
		Count:       1,
		OccurredAt:  recentTime,
		RecordedAt:  recentTime,
	}}); err != nil {
		t.Fatalf("AppendCoachEvents (recent): %v", err)
	}

	// GC with a cutoff of now - GCWindow (90 days).
	cutoff := now.Add(-jmapcoach.GCWindow)
	n, err := st.Meta().GCCoachEvents(ctx, cutoff)
	if err != nil {
		t.Fatalf("GCCoachEvents: %v", err)
	}
	if n != 1 {
		t.Errorf("GCCoachEvents deleted %d rows; want 1 (old event)", n)
	}

	// Verify the recent event was kept: ListCoachStats should return non-zero
	// counts because the recent event falls inside the 14d or 90d window.
	stats, err := st.Meta().ListCoachStats(ctx, pid, now)
	if err != nil {
		t.Fatalf("ListCoachStats: %v", err)
	}
	if len(stats) == 0 {
		t.Error("ListCoachStats returned 0 stats; want the recent event to be kept")
	}
}
