package storetest

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testClientLogAppendAndList verifies that AppendClientLog stores a row and
// ListClientLogByCursor returns it with all fields preserved.
func testClientLogAppendAndList(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := "u1"
	sessionID := "sess1"
	reqID := "req1"
	route := "/inbox"
	stack := "Error: boom\n  at foo (app.js:1)"

	row := store.ClientLogRow{
		Slice:       store.ClientLogSliceAuth,
		ServerTS:    now,
		ClientTS:    now.Add(-50 * time.Millisecond),
		ClockSkewMS: 50,
		App:         "suite",
		Kind:        "error",
		Level:       "error",
		UserID:      &userID,
		SessionID:   &sessionID,
		PageID:      "page-uuid-1",
		RequestID:   &reqID,
		Route:       &route,
		BuildSHA:    "abc123",
		UA:          "Mozilla/5.0",
		Msg:         "Unexpected error",
		Stack:       &stack,
		PayloadJSON: `{"foo":"bar"}`,
	}

	if err := s.Meta().AppendClientLog(ctx, row); err != nil {
		t.Fatalf("AppendClientLog: %v", err)
	}

	rows, next, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListClientLogByCursor: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows; want 1", len(rows))
	}
	if next != "" {
		t.Errorf("nextCursor = %q; want empty", next)
	}

	got := rows[0]
	if got.ID == 0 {
		t.Error("ID must be non-zero after insert")
	}
	if got.Slice != store.ClientLogSliceAuth {
		t.Errorf("Slice = %q; want %q", got.Slice, store.ClientLogSliceAuth)
	}
	if !got.ServerTS.Equal(now) {
		t.Errorf("ServerTS = %v; want %v", got.ServerTS, now)
	}
	if got.Msg != "Unexpected error" {
		t.Errorf("Msg = %q; want %q", got.Msg, "Unexpected error")
	}
	if got.UserID == nil || *got.UserID != userID {
		t.Errorf("UserID = %v; want %q", got.UserID, userID)
	}
	if got.Stack == nil || *got.Stack != stack {
		t.Errorf("Stack = %v; want %q", got.Stack, stack)
	}
	if got.PayloadJSON != `{"foo":"bar"}` {
		t.Errorf("PayloadJSON = %q; want {\"foo\":\"bar\"}", got.PayloadJSON)
	}
}

// testClientLogNullableFieldsPublicSlice verifies that nullable fields (UserID,
// SessionID, RequestID, Route, Stack) round-trip as nil for a public-slice row.
func testClientLogNullableFieldsPublicSlice(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.ClientLogRow{
		Slice:       store.ClientLogSlicePublic,
		ServerTS:    now,
		ClientTS:    now,
		ClockSkewMS: 0,
		App:         "suite",
		Kind:        "log",
		Level:       "info",
		PageID:      "page-pub-1",
		BuildSHA:    "def456",
		UA:          "curl/7.0",
		Msg:         "page loaded",
		PayloadJSON: `{}`,
	}

	if err := s.Meta().AppendClientLog(ctx, row); err != nil {
		t.Fatalf("AppendClientLog: %v", err)
	}

	rows, _, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{Slice: store.ClientLogSlicePublic},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListClientLogByCursor: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows; want 1", len(rows))
	}
	got := rows[0]
	if got.UserID != nil {
		t.Errorf("UserID should be nil; got %q", *got.UserID)
	}
	if got.SessionID != nil {
		t.Errorf("SessionID should be nil; got %q", *got.SessionID)
	}
	if got.RequestID != nil {
		t.Errorf("RequestID should be nil; got %q", *got.RequestID)
	}
	if got.Route != nil {
		t.Errorf("Route should be nil; got %q", *got.Route)
	}
	if got.Stack != nil {
		t.Errorf("Stack should be nil; got %q", *got.Stack)
	}
}

// testClientLogPagination verifies that the cursor-based pagination returns
// rows in id DESC order and that the nextCursor advances correctly.
func testClientLogPagination(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		row := store.ClientLogRow{
			Slice:       store.ClientLogSliceAuth,
			ServerTS:    base.Add(time.Duration(i) * time.Second),
			ClientTS:    base,
			ClockSkewMS: 0,
			App:         "suite",
			Kind:        "log",
			Level:       "info",
			PageID:      "pg",
			BuildSHA:    "sha",
			UA:          "ua",
			Msg:         "msg",
			PayloadJSON: "{}",
		}
		if err := s.Meta().AppendClientLog(ctx, row); err != nil {
			t.Fatalf("AppendClientLog[%d]: %v", i, err)
		}
	}

	// First page of 3.
	page1, cur1, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{Slice: store.ClientLogSliceAuth},
		Limit:  3,
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 3 {
		t.Fatalf("page 1 len = %d; want 3", len(page1))
	}
	if cur1 == "" {
		t.Error("expected non-empty nextCursor after first page")
	}
	// Rows must arrive in descending ID order.
	for i := 1; i < len(page1); i++ {
		if page1[i].ID >= page1[i-1].ID {
			t.Errorf("rows not in desc order: page1[%d].ID=%d >= page1[%d].ID=%d",
				i, page1[i].ID, i-1, page1[i-1].ID)
		}
	}

	// Second page using cursor.
	page2, cur2, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{Slice: store.ClientLogSliceAuth},
		Cursor: cur1,
		Limit:  3,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("page 2 len = %d; want 2", len(page2))
	}
	if cur2 != "" {
		t.Errorf("expected empty nextCursor at end; got %q", cur2)
	}
	// No overlap between pages.
	seenIDs := make(map[int64]bool)
	for _, r := range page1 {
		seenIDs[r.ID] = true
	}
	for _, r := range page2 {
		if seenIDs[r.ID] {
			t.Errorf("duplicate row ID %d across pages", r.ID)
		}
	}
}

// testClientLogListByRequestID verifies that ListClientLogByRequestID returns
// exactly the rows with the given request_id.
func testClientLogListByRequestID(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	reqA := "req-aaa"
	reqB := "req-bbb"

	for _, req := range []string{reqA, reqA, reqB} {
		r := req
		row := store.ClientLogRow{
			Slice:       store.ClientLogSliceAuth,
			ServerTS:    time.Now().UTC(),
			ClientTS:    time.Now().UTC(),
			App:         "suite",
			Kind:        "log",
			Level:       "info",
			PageID:      "pg",
			RequestID:   &r,
			BuildSHA:    "sha",
			UA:          "ua",
			Msg:         "m",
			PayloadJSON: "{}",
		}
		if err := s.Meta().AppendClientLog(ctx, row); err != nil {
			t.Fatalf("AppendClientLog: %v", err)
		}
	}

	got, err := s.Meta().ListClientLogByRequestID(ctx, reqA)
	if err != nil {
		t.Fatalf("ListClientLogByRequestID: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows; want 2", len(got))
	}
	for _, r := range got {
		if r.RequestID == nil || *r.RequestID != reqA {
			t.Errorf("unexpected RequestID: %v", r.RequestID)
		}
	}
}

// testClientLogEvictByAge verifies that EvictClientLog removes rows older than
// MaxAge and returns the count deleted.
//
// We deliberately insert "old" rows using the Unix epoch (1970-01-01) so the
// test does not depend on the backend's wall-clock — any reasonable clock-Now
// minus any positive MaxAge will be after the epoch.  The "fresh" row uses a
// far-future timestamp so it is never evicted by the age check.
func testClientLogEvictByAge(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	// Unix epoch: definitely old relative to any realistic "now".
	old := time.UnixMicro(1000).UTC()
	// Year 2099: definitely not old.
	fresh := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, ts := range []time.Time{old, old, fresh} {
		row := store.ClientLogRow{
			Slice:       store.ClientLogSliceAuth,
			ServerTS:    ts,
			ClientTS:    ts,
			App:         "suite",
			Kind:        "log",
			Level:       "info",
			PageID:      "pg",
			BuildSHA:    "sha",
			UA:          "ua",
			Msg:         "m",
			PayloadJSON: "{}",
		}
		if err := s.Meta().AppendClientLog(ctx, row); err != nil {
			t.Fatalf("AppendClientLog: %v", err)
		}
	}

	// MaxAge=1 second: the epoch rows are billions of seconds old; the
	// 2099 row is decades in the future.
	deleted, err := s.Meta().EvictClientLog(ctx, store.ClientLogEvictOptions{
		Slice:     store.ClientLogSliceAuth,
		CapRows:   1_000_000, // cap huge — won't trigger
		MaxAge:    time.Second,
		BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("EvictClientLog: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d; want 2", deleted)
	}

	rows, _, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{Slice: store.ClientLogSliceAuth},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListClientLogByCursor after evict: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("remaining rows = %d; want 1", len(rows))
	}
}

// testClientLogEvictByCap verifies that EvictClientLog removes rows exceeding
// the CapRows limit (oldest first by ID).
func testClientLogEvictByCap(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		row := store.ClientLogRow{
			Slice:       store.ClientLogSliceAuth,
			ServerTS:    now,
			ClientTS:    now,
			App:         "suite",
			Kind:        "log",
			Level:       "info",
			PageID:      "pg",
			BuildSHA:    "sha",
			UA:          "ua",
			Msg:         "m",
			PayloadJSON: "{}",
		}
		if err := s.Meta().AppendClientLog(ctx, row); err != nil {
			t.Fatalf("AppendClientLog[%d]: %v", i, err)
		}
	}

	deleted, err := s.Meta().EvictClientLog(ctx, store.ClientLogEvictOptions{
		Slice:     store.ClientLogSliceAuth,
		CapRows:   3,
		MaxAge:    7 * 24 * time.Hour, // far future — won't trigger
		BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("EvictClientLog: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d; want 2", deleted)
	}

	rows, _, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{Slice: store.ClientLogSliceAuth},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListClientLogByCursor after cap evict: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("remaining rows = %d; want 3", len(rows))
	}
}

// testClientLogEvictDoesNotCrossSlice verifies that evicting the auth slice
// leaves public-slice rows untouched.
func testClientLogEvictDoesNotCrossSlice(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	// Use Unix epoch so the rows are definitely "old" regardless of the
	// backend clock (see testClientLogEvictByAge for rationale).
	old := time.UnixMicro(1000).UTC()

	for _, sl := range []store.ClientLogSlice{store.ClientLogSliceAuth, store.ClientLogSlicePublic} {
		row := store.ClientLogRow{
			Slice:       sl,
			ServerTS:    old,
			ClientTS:    old,
			App:         "suite",
			Kind:        "log",
			Level:       "info",
			PageID:      "pg",
			BuildSHA:    "sha",
			UA:          "ua",
			Msg:         "m",
			PayloadJSON: "{}",
		}
		if err := s.Meta().AppendClientLog(ctx, row); err != nil {
			t.Fatalf("AppendClientLog(%s): %v", sl, err)
		}
	}

	deleted, err := s.Meta().EvictClientLog(ctx, store.ClientLogEvictOptions{
		Slice:     store.ClientLogSliceAuth,
		CapRows:   1_000_000,
		MaxAge:    time.Second, // epoch rows are aeons old
		BatchSize: 100,
	})
	if err != nil {
		t.Fatalf("EvictClientLog: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d; want 1 (auth only)", deleted)
	}

	// Public row must still be present.
	rows, _, err := s.Meta().ListClientLogByCursor(ctx, store.ClientLogCursorOptions{
		Filter: store.ClientLogFilter{Slice: store.ClientLogSlicePublic},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("ListClientLogByCursor(public): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("public rows remaining = %d; want 1", len(rows))
	}
}
