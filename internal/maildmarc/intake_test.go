package maildmarc_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// seedDMARCReportMessage inserts one principal + INBOX, persists the
// supplied raw bytes as a blob, and inserts a Message row whose
// envelope mirrors the raw headers. Returns the resulting Message.
func seedDMARCReportMessage(t *testing.T, fs store.Store, recipient string, raw []byte) store.Message {
	t.Helper()
	ctx := t.Context()
	p, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: recipient,
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	mb, err := fs.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert mailbox: %v", err)
	}
	ref, err := fs.Blobs().Put(ctx, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	msg := store.Message{
		Size: ref.Size,
		Blob: ref,
		Envelope: store.Envelope{
			To:      recipient,
			Subject: "Report Domain: example.test",
			From:    "noreply-dmarc-support@google.com",
		},
	}
	uid, modseq, err := fs.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mb.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	return msg
}

func TestIntake_PollsChangeFeed_FiltersByRecipientPattern(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	// Seed one DMARC report and one ordinary mail.
	att := gzReportBytes(t, sampleReportXML)
	report := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	_ = seedDMARCReportMessage(t, fs, "dmarc-reports@example.test", report)

	other := []byte(strings.Join([]string{
		"From: alice@example.test",
		"To: bob@example.test",
		"Subject: hello",
		"",
		"normal mail body",
		"",
	}, "\r\n"))
	_ = seedDMARCReportMessage(t, fs, "bob@example.test", other)

	ing := maildmarc.NewIngestor(fs.Meta(), logger, clk)
	intake := maildmarc.NewIntake(fs, ing, logger, clk, maildmarc.IntakeOptions{
		PollInterval:     50 * time.Millisecond,
		RecipientPattern: "dmarc-reports@",
		CursorKey:        "dmarc-intake-test",
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()

	// Drive the loop: advance the clock so the empty-feed sleep wakes,
	// then poll the store until the report row appears or we time out.
	deadline := time.Now().Add(3 * time.Second)
	var seen bool
	for time.Now().Before(deadline) {
		clk.Advance(50 * time.Millisecond)
		reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
		if len(reports) == 1 {
			seen = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if !seen {
		t.Fatalf("intake did not ingest the DMARC report")
	}
	reports, _ := fs.Meta().ListDMARCReports(t.Context(), store.DMARCReportFilter{})
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1 (only the DMARC mail should ingest)", len(reports))
	}
}

// shutdownFlushStore wraps a real store so the in-loop SetFTSCursor
// path returns context.Canceled while the shutdown-path SetFTSCursor
// (which uses a fresh background context) is allowed through. With
// the fix, the worker writes the advanced cursor on its way out via a
// fresh ctx; without the fix, every SetFTSCursor would carry the
// cancelled run ctx and the persisted cursor would lag the in-memory
// advance, causing a restart to re-process the report.
type shutdownFlushStore struct {
	store.Store
	failingMeta *shutdownFlushMeta
}

func (s shutdownFlushStore) Meta() store.Metadata { return s.failingMeta }

type shutdownFlushMeta struct {
	store.Metadata
	parentCtx context.Context
}

func (m *shutdownFlushMeta) SetFTSCursor(ctx context.Context, key string, seq uint64) error {
	if ctx == m.parentCtx {
		return context.Canceled
	}
	return m.Metadata.SetFTSCursor(ctx, key, seq)
}

// TestIntake_PersistsCursorOnShutdown asserts that the worker's
// shutdown defer flushes the in-memory cursor when the in-loop
// SetFTSCursor lost its race with ctx cancellation. Restart with the
// same cursor key must NOT re-ingest the seeded report.
func TestIntake_PersistsCursorOnShutdown(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	att := gzReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	_ = seedDMARCReportMessage(t, fs, "dmarc-reports@example.test", raw)

	const cursorKey = "dmarc-intake-shutdown-test"
	runCtx, cancel := context.WithCancel(context.Background())
	wrapped := shutdownFlushStore{
		Store:       fs,
		failingMeta: &shutdownFlushMeta{Metadata: fs.Meta(), parentCtx: runCtx},
	}

	ing := maildmarc.NewIngestor(fs.Meta(), logger, clk)
	intake := maildmarc.NewIntake(wrapped, ing, logger, clk, maildmarc.IntakeOptions{
		PollInterval:     20 * time.Millisecond,
		RecipientPattern: "dmarc-reports@",
		CursorKey:        cursorKey,
	})

	done := make(chan error, 1)
	go func() { done <- intake.Run(runCtx) }()

	// Drive the worker until it has ingested the report (the in-loop
	// cursor write fails, but the in-memory cursor advances and the
	// report row lands).
	deadline := time.Now().Add(3 * time.Second)
	var ingested bool
	for time.Now().Before(deadline) {
		clk.Advance(20 * time.Millisecond)
		reports, _ := fs.Meta().ListDMARCReports(context.Background(), store.DMARCReportFilter{})
		if len(reports) == 1 {
			ingested = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ingested {
		cancel()
		<-done
		t.Fatalf("first intake did not ingest the report")
	}

	memCursor := intake.Cursor()
	if memCursor == 0 {
		cancel()
		<-done
		t.Fatalf("first intake's in-memory cursor did not advance")
	}

	// Cancel and wait for Run to return; the shutdown defer must
	// persist the cursor via a fresh ctx.
	cancel()
	<-done

	persisted, err := fs.Meta().GetFTSCursor(context.Background(), cursorKey)
	if err != nil {
		t.Fatalf("GetFTSCursor post-shutdown: %v", err)
	}
	if persisted != memCursor {
		t.Fatalf("post-shutdown persisted cursor = %d, in-memory cursor = %d (shutdown flush did not run)", persisted, memCursor)
	}

	// Restart with the same cursor key on the unwrapped store. The
	// new worker must NOT re-ingest the report (no second
	// ListDMARCReports row appears).
	intake2 := maildmarc.NewIntake(fs, ing, logger, clk, maildmarc.IntakeOptions{
		PollInterval:     20 * time.Millisecond,
		RecipientPattern: "dmarc-reports@",
		CursorKey:        cursorKey,
	})
	runCtx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- intake2.Run(runCtx2) }()
	// Give the second worker a few poll cycles so it would re-ingest
	// if the cursor were not honoured.
	for j := 0; j < 5; j++ {
		clk.Advance(20 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}
	cancel2()
	<-done2

	reports, _ := fs.Meta().ListDMARCReports(context.Background(), store.DMARCReportFilter{})
	if len(reports) != 1 {
		t.Fatalf("after restart reports = %d, want 1 (no double ingest)", len(reports))
	}
}

func TestIntake_AdvancesCursor(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	att := gzReportBytes(t, sampleReportXML)
	raw := buildReportEmail(
		"google.com!example.test!1700000000!1700086400.xml.gz",
		"application/gzip",
		att,
	)
	_ = seedDMARCReportMessage(t, fs, "dmarc-reports@example.test", raw)

	ing := maildmarc.NewIngestor(fs.Meta(), logger, clk)
	intake := maildmarc.NewIntake(fs, ing, logger, clk, maildmarc.IntakeOptions{
		PollInterval:     50 * time.Millisecond,
		RecipientPattern: "dmarc-reports@",
		CursorKey:        "dmarc-intake-cursor",
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	var advanced bool
	for time.Now().Before(deadline) {
		clk.Advance(50 * time.Millisecond)
		seq, _ := fs.Meta().GetFTSCursor(t.Context(), "dmarc-intake-cursor")
		if seq > 0 {
			advanced = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if !advanced {
		t.Fatalf("cursor did not advance")
	}
}
