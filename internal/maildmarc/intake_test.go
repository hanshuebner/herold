package maildmarc_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// seedDMARCReportMessage inserts one principal + INBOX, persists the
// supplied raw bytes as a blob, and inserts a Message row whose
// envelope mirrors the raw headers. Returns the resulting Message.
func seedDMARCReportMessage(t *testing.T, fs *fakestore.Store, recipient string, raw []byte) store.Message {
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
		MailboxID: mb.ID,
		Size:      ref.Size,
		Blob:      ref,
		Envelope: store.Envelope{
			To:      recipient,
			Subject: "Report Domain: example.test",
			From:    "noreply-dmarc-support@google.com",
		},
	}
	uid, modseq, err := fs.Meta().InsertMessage(ctx, msg)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	return msg
}

func TestIntake_PollsChangeFeed_FiltersByRecipientPattern(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
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

func TestIntake_AdvancesCursor(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
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
