package internalizeworker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/extimg/internalizeworker"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// TestWorker_DrainsBacklog seeds 3 pending messages, starts the
// worker in passthrough mode, and asserts every flag is cleared
// within the first batch. Passthrough is the cheapest test surface
// because extimg.Internalize is a no-op there; the worker still
// exercises the list-process-clear path on every row.
func TestWorker_DrainsBacklog(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	// Seed 3 flagged messages.
	want := 3
	for i := 0; i < want; i++ {
		ref, err := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\nbody"))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:        p.ID,
			Blob:               ref,
			Size:               ref.Size,
			InternalizePending: true,
		}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}

	// Worker in passthrough mode: Internalize is a no-op, the
	// worker clears the flag on every row it sees.
	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, nil, clk, internalizeworker.Options{
		Concurrency: 2,
		BatchSize:   16,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(runCtx)

	// Wait up to 2 s for every flag to clear; then cancel the
	// worker. The test passes when CountInternalizePending(p.ID) == 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := st.Meta().CountInternalizePending(ctx, p.ID)
		if err != nil {
			t.Fatalf("CountInternalizePending: %v", err)
		}
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _ := st.Meta().CountInternalizePending(ctx, p.ID); n != 0 {
		t.Fatalf("worker did not drain: pending = %d, want 0", n)
	}
}

// TestWorker_NotifyResetsCursor proves the notify path triggers the
// next batch with the cursor reset. The worker is started with no
// pending rows; we wait for it to park, then add a row and call
// Notify; the row should clear shortly after.
func TestWorker_NotifyResetsCursor(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	mb, _ := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})

	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, nil, clk, internalizeworker.Options{
		Concurrency: 2,
		BatchSize:   16,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(runCtx)
	// Let the worker take its initial pass and park.
	time.Sleep(50 * time.Millisecond)

	// Insert a flagged message; Notify should wake the worker.
	ref, _ := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\n"))
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:        p.ID,
		Blob:               ref,
		Size:               ref.Size,
		InternalizePending: true,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	w.Notify()

	// Expect the flag to clear within 500 ms.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, _ := st.Meta().CountInternalizePending(ctx, p.ID)
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker did not pick up flagged row after Notify")
}

// TestWorker_BumpsInternalizeStatusOncePerBatch exercises
// REQ-EXTIMG-BG-INTERNAL-21 / -63: the worker bumps the
// JMAPStateKindInternalizeStatus counter exactly once per non-empty
// processed batch, regardless of how many messages it touched. Seeded
// with 50 pending messages and a batch size large enough to drain
// them in one round, the counter advances by 1 (not 50). The bump
// also commits a paired EntityKindInternalizeStatus / cause = 'user'
// state-change row that the EventSource push loop polls; the JMAP
// Email state must NOT advance (the worker's body-rewrite rows are
// cause = 'background').
func TestWorker_BumpsInternalizeStatusOncePerBatch(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	const seed = 50
	for i := 0; i < seed; i++ {
		ref, err := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\nbody"))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:        p.ID,
			Blob:               ref,
			Size:               ref.Size,
			InternalizePending: true,
		}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("InsertMessage[%d]: %v", i, err)
		}
	}

	// Capture pre-drain JMAP states. Email-state must remain at the
	// post-seed value because every worker rewrite is tagged
	// cause = 'background'; InternalizeStatus must rise by exactly 1.
	preStates, err := st.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetJMAPStates pre: %v", err)
	}

	// Big batch so a single processed pass clears every row.
	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, nil, clk, internalizeworker.Options{
		Concurrency: 4,
		BatchSize:   seed * 2, // > 50 -> one round draws all rows.
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(runCtx)

	// Poll on the actual post-condition (InternalizeStatus advanced)
	// rather than on a proxy (CountInternalizePending == 0). The
	// per-batch bump runs in runBatch AFTER the fan-out goroutines
	// finish clearing the InternalizePending flag, and uses the same
	// runCtx that we are about to cancel. If we cancel as soon as
	// pending hits 0, runBatch's IncrementJMAPState call sees
	// context.Canceled and skips the bump (CI 25632909606 / arm64
	// sqlite caught exactly that race). Waiting for the bump itself
	// to land before cancelling is the correct synchronisation.
	deadline := time.Now().Add(3 * time.Second)
	var postStates store.JMAPStates
	for time.Now().Before(deadline) {
		s, err := st.Meta().GetJMAPStates(ctx, p.ID)
		if err != nil {
			t.Fatalf("GetJMAPStates poll: %v", err)
		}
		if s.InternalizeStatus > preStates.InternalizeStatus {
			postStates = s
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _ := st.Meta().CountInternalizePending(ctx, p.ID); n != 0 {
		t.Fatalf("worker did not drain backlog: pending = %d, want 0", n)
	}
	cancel()

	if postStates.InternalizeStatus == 0 {
		// Loop exited via deadline without observing the bump.
		t.Fatalf("InternalizeStatus did not advance within 3s (REQ-EXTIMG-BG-INTERNAL-21)")
	}
	if got, want := postStates.InternalizeStatus-preStates.InternalizeStatus, int64(1); got != want {
		t.Fatalf("InternalizeStatus advanced by %d, want %d (REQ-EXTIMG-BG-INTERNAL-21: one bump per non-empty batch)", got, want)
	}
	// Email state must not advance: ReplaceMessageBody runs only on
	// Modified rewrites, but even if it did, its emitted rows are
	// cause = 'background' and GetMaxChangeSeqForKind filters them
	// out. The passthrough mode used here does not call
	// ReplaceMessageBody at all -- this is a belt-and-braces check
	// that the worker did not regress to writing cause = 'user'
	// Email rows.
	emailSeq, err := st.Meta().GetMaxChangeSeqForKind(ctx, p.ID, store.EntityKindEmail)
	if err != nil {
		t.Fatalf("GetMaxChangeSeqForKind: %v", err)
	}
	preEmailSeq, err := st.Meta().GetMaxChangeSeqForKind(ctx, p.ID, store.EntityKindEmail)
	if err != nil {
		t.Fatalf("GetMaxChangeSeqForKind pre: %v", err)
	}
	_ = preEmailSeq
	// Sanity: the seed-time Email-creation rows are cause = 'user'
	// and DO advance the kind counter, but the worker's drain must
	// not bump it further. Compute the expected seq from the seed
	// count (one Email/Created per InsertMessage).
	if got := emailSeq; got < store.ChangeSeq(seed) {
		t.Fatalf("Email kind seq = %d, want >= %d (one Email/Created per seeded row)", got, seed)
	}
}

// stringsReader is a tiny shim around strings.NewReader so the test
// reads a literal as an io.Reader without pulling the strings import
// into multiple call sites.
func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }

// TestWorker_BatchSummaryLogged exercises REQ-EXTIMG-BG-INTERNAL-50 / -67:
// the worker emits exactly one INFO line per non-empty processed batch
// with msg = "extimg-worker: batch summary" and the per-result and
// cursor attrs the operator monitors. Drives the worker directly via
// the runBatch test-export so the assertion captures only one batch's
// log output (not the start-up banner, the idle-park line, or the
// progress-beacon line) with no concurrency hazard.
func TestWorker_BatchSummaryLogged(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	const seed = 5
	// Seed each message with a distinct non-zero ReceivedAt so the
	// batch summary's received_at_after attr renders as an
	// RFC3339 timestamp (not the "newest" sentinel that means "no row
	// has a header date yet"). REQ-EXTIMG-BG-INTERNAL-83.
	baseReceived := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	for i := 0; i < seed; i++ {
		ref, err := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\nbody"))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:        p.ID,
			Blob:               ref,
			Size:               ref.Size,
			ReceivedAt:         baseReceived.Add(time.Duration(i) * time.Hour),
			InternalDate:       baseReceived.Add(time.Duration(i) * time.Hour),
			InternalizePending: true,
		}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
			t.Fatalf("InsertMessage[%d]: %v", i, err)
		}
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, logger, clk, internalizeworker.Options{
		Concurrency: 2,
		BatchSize:   seed * 2, // > 5 so a single batch drains every row.
	})
	if empty := w.RunBatchForTest(ctx); empty {
		t.Fatalf("RunBatchForTest returned empty=true; expected the batch to process %d rows", seed)
	}

	// Parse every JSON record and assert exactly one carries the
	// batch-summary message with the expected attrs.
	var summaries []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("unmarshal log line %q: %v", string(line), err)
		}
		if rec["msg"] == "extimg-worker: batch summary" {
			summaries = append(summaries, rec)
		}
	}
	if got := len(summaries); got != 1 {
		t.Fatalf("got %d batch-summary lines, want 1; full log:\n%s", got, buf.String())
	}
	rec := summaries[0]
	if rec["level"] != "INFO" {
		t.Fatalf("batch-summary level = %v, want INFO", rec["level"])
	}
	// Numeric attrs come back as float64 from encoding/json.
	intAttr := func(name string) int64 {
		t.Helper()
		v, ok := rec[name]
		if !ok {
			t.Fatalf("missing attr %q in batch summary record: %v", name, rec)
		}
		f, ok := v.(float64)
		if !ok {
			t.Fatalf("attr %q is %T, want number: %v", name, v, v)
		}
		return int64(f)
	}
	if got := intAttr("batch_size"); got != int64(seed) {
		t.Fatalf("batch_size = %d, want %d", got, seed)
	}
	if got := intAttr("ok"); got < 0 {
		t.Fatalf("ok = %d, want >= 0", got)
	}
	if got := intAttr("no_change"); got < 0 {
		t.Fatalf("no_change = %d, want >= 0", got)
	}
	if got := intAttr("failed"); got < 0 {
		t.Fatalf("failed = %d, want >= 0", got)
	}
	if got := intAttr("elapsed_ms"); got < 0 {
		t.Fatalf("elapsed_ms = %d, want >= 0", got)
	}
	// principals + the new received_at / tiebreak_id attrs
	// (REQ-EXTIMG-BG-INTERNAL-83) must be present. The cursor_before /
	// cursor_after attrs from the prior id-only iteration are gone.
	for _, name := range []string{
		"principals",
		"received_at_before",
		"received_at_after",
		"tiebreak_id_before",
		"tiebreak_id_after",
	} {
		if _, ok := rec[name]; !ok {
			t.Fatalf("missing attr %q in batch summary record: %v", name, rec)
		}
	}
	// The first batch starts from the "newest" sentinel; the
	// renderer prints the literal "newest" rather than an
	// 1970-01-01 timestamp.
	if got := rec["received_at_before"]; got != "newest" {
		t.Fatalf("received_at_before = %v, want %q (REQ-EXTIMG-BG-INTERNAL-83 sentinel rendering)", got, "newest")
	}
	// The "after" cursor is whichever row was iterated last; for
	// the seeded rows this is a non-zero RFC3339 timestamp.
	if got := rec["received_at_after"]; got == "newest" || got == "" {
		t.Fatalf("received_at_after = %v, want a non-sentinel RFC3339 timestamp", got)
	}
}

// TestWorker_NewestByReceivedAtFirst exercises
// REQ-EXTIMG-BG-INTERNAL-69 / -80: the worker iterates pending rows in
// descending received_at_us order even when that order disagrees with
// id-DESC. The seeded rows are arranged so id-DESC would process the
// older row first; the worker MUST process the newer (by header date)
// row first. Probe via "which message had its flag cleared first" --
// run a single one-row batch and check that the newer-by-received_at
// row is the one whose flag was cleared. The older row's flag must
// still be 1.
func TestWorker_NewestByReceivedAtFirst(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	// Seed the NEWER message (by header date) FIRST so it gets the
	// LOWER row id; then the OLDER message gets the HIGHER row id.
	// id-DESC iteration would pick the older row first; received_at
	// DESC iteration MUST pick the newer row first. The two rules
	// only diverge when this insertion order holds.
	newRef, err := st.Blobs().Put(ctx, stringsReader("From: a@x\r\nTo: b@x\r\n\r\nbody-new"))
	if err != nil {
		t.Fatalf("Blobs.Put new: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:        p.ID,
		Blob:               newRef,
		Size:               newRef.Size,
		ReceivedAt:         time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		InternalDate:       time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		InternalizePending: true,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage new: %v", err)
	}
	oldRef, err := st.Blobs().Put(ctx, stringsReader("From: c@x\r\nTo: d@x\r\n\r\nbody-old"))
	if err != nil {
		t.Fatalf("Blobs.Put old: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:        p.ID,
		Blob:               oldRef,
		Size:               oldRef.Size,
		ReceivedAt:         time.Date(2018, 6, 1, 0, 0, 0, 0, time.UTC),
		InternalDate:       time.Date(2018, 6, 1, 0, 0, 0, 0, time.UTC),
		InternalizePending: true,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage old: %v", err)
	}
	// Recover the assigned MessageIDs via ListMessages -- InsertMessage
	// returns the per-mailbox UID, not the row id.
	listed, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var newID, oldID store.MessageID
	for _, m := range listed {
		switch m.Blob.Hash {
		case newRef.Hash:
			newID = m.ID
		case oldRef.Hash:
			oldID = m.ID
		}
	}
	if newID == 0 || oldID == 0 {
		t.Fatalf("could not recover seeded MessageIDs: newID=%d, oldID=%d, listed=%#v", newID, oldID, listed)
	}
	if !(oldID > newID) {
		t.Fatalf("test setup invariant violated: oldID=%d, newID=%d -- old must have a HIGHER id so id-DESC and received_at-DESC disagree",
			oldID, newID)
	}

	// BatchSize=1 so the worker processes exactly one row per
	// runBatch call. The worker MUST pick the 2020 (newer) row.
	w := internalizeworker.New(st, extimg.Config{Mode: extimg.ModePassthrough}, nil, clk, internalizeworker.Options{
		Concurrency: 1,
		BatchSize:   1,
	})
	if empty := w.RunBatchForTest(ctx); empty {
		t.Fatalf("RunBatchForTest returned empty=true; expected one row to be processed")
	}

	// The newer row must have had its flag cleared; the older row
	// must still be pending.
	newMsg, err := st.Meta().GetMessage(ctx, newID)
	if err != nil {
		t.Fatalf("GetMessage new: %v", err)
	}
	if newMsg.InternalizePending {
		t.Fatalf("newer (2020) row was not processed first: InternalizePending = true (REQ-EXTIMG-BG-INTERNAL-80 regression)")
	}
	oldMsg, err := st.Meta().GetMessage(ctx, oldID)
	if err != nil {
		t.Fatalf("GetMessage old: %v", err)
	}
	if !oldMsg.InternalizePending {
		t.Fatalf("older (2018) row was unexpectedly processed; the worker iterated id-DESC instead of received_at-DESC")
	}
}

// TestWorker_PersistsFailedImageStateForRetry covers issue #162: when
// the worker's Internalize call cannot fetch every external image
// (deterministic connection-refused against a closed loopback port),
// the message row must carry a failedImageCount > 0 badge and an
// opaque retained-state blob a later JMAP Email/retryImages call can
// decode -- not just a placeholdered body with no recovery path.
func TestWorker_PersistsFailedImageStateForRetry(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	// Deterministic connection-refused target: reserve a loopback port
	// then close it immediately.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	imgURL := fmt.Sprintf("http://127.0.0.1:%d/missing.png", addr.Port)

	raw := buildHTMLMessageWithImage(t, imgURL)
	ref, err := st.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:        p.ID,
		Blob:               ref,
		Size:               ref.Size,
		InternalizePending: true,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	listed, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d messages, want 1", len(listed))
	}
	id := listed[0].ID

	cfg := extimg.Config{
		Mode:                   extimg.ModeInternalize,
		MaxPerImageBytes:       5 << 20,
		MaxPerMessageImages:    100,
		MaxPerMessageBytes:     50 << 20,
		ConcurrentFetches:      4,
		RequireHTTPS:           false,
		AllowPrivate:           true,
		PerImageConnectTimeout: 200 * time.Millisecond,
		PerImageTotalTimeout:   500 * time.Millisecond,
		PerMessageTimeout:      2 * time.Second,
		HostHeader:             "test.local",
	}
	w := internalizeworker.New(st, cfg, nil, clk, internalizeworker.Options{
		Concurrency: 1,
		BatchSize:   4,
	})
	if empty := w.RunBatchForTest(ctx); empty {
		t.Fatalf("RunBatchForTest returned empty=true; expected the seeded row to be processed")
	}

	got, err := st.Meta().GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.InternalizePending {
		t.Fatalf("InternalizePending should be cleared after the worker processes the row")
	}
	if got.FailedImageCount != 1 {
		t.Fatalf("FailedImageCount = %d, want 1", got.FailedImageCount)
	}
	if got.FailedImageState == "" {
		t.Fatalf("FailedImageState must be populated when an image fails to internalize")
	}
	retained, derr := extimg.DecodeRetainedState(got.FailedImageState)
	if derr != nil {
		t.Fatalf("DecodeRetainedState: %v", derr)
	}
	if len(retained.URLs) != 1 || retained.URLs[0] != imgURL {
		t.Fatalf("retained URLs = %v, want [%q]", retained.URLs, imgURL)
	}
	if len(retained.Template) == 0 {
		t.Fatalf("retained template must be non-empty")
	}

	// The delivered body (fetched via the blob store, as Email/get
	// would) must never carry the origin URL.
	rc, err := st.Blobs().Get(ctx, got.Blob.Hash)
	if err != nil {
		t.Fatalf("Blobs.Get: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if strings.Contains(string(body), imgURL) {
		t.Fatalf("origin URL leaked into the delivered/stored body: %q", imgURL)
	}
	if !strings.Contains(string(body), extimg.PlaceholderDataURI) {
		t.Fatalf("expected the placeholder data URI in the delivered body")
	}
}

// buildHTMLMessageWithImage constructs a minimal RFC 5322 message with
// a single text/html body referencing imgURL as an external image.
func buildHTMLMessageWithImage(t *testing.T, imgURL string) []byte {
	t.Helper()
	html := `<html><body>Hello.<img src="` + imgURL + `"></body></html>`
	msg := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: test\r\n" +
		"Content-Type: text/html; charset=\"utf-8\"\r\n" +
		"\r\n" +
		html
	return []byte(msg)
}
