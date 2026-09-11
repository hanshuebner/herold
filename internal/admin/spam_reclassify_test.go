package admin

// spam_reclassify_test.go exercises `herold spam reclassify` (issue
// #318) against a real classifierfixture child process (STANDARDS
// section 8, no mocks at the process boundary), on both store backends.
// classifierfixture's scripted verdict is fixed for the lifetime of the
// process (env vars read once per call, but HEROLD_TEST_CLASSIFY_VERDICT
// is the same value every call), so each scenario that needs a
// different verdict starts its own plugin instance.

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// spamReclassifyFixture seeds one principal with INBOX and Junk, plus
// one message per name in subjects, all in INBOX and all unclassified.
// Returns the principal id and a name -> MessageID map.
func spamReclassifyFixture(t *testing.T, st store.Store, subjects ...string) (store.PrincipalID, map[string]store.MessageID) {
	t.Helper()
	ctx := context.Background()

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "reclassify@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
	if err != nil {
		t.Fatalf("InsertMailbox INBOX: %v", err)
	}
	if _, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Junk", Attributes: store.MailboxAttrJunk}); err != nil {
		t.Fatalf("InsertMailbox Junk: %v", err)
	}

	for _, subject := range subjects {
		body := "From: sender@example.test\r\nTo: reclassify@example.test\r\nSubject: " + subject + "\r\n\r\nbody\r\n"
		blob, err := st.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			t.Fatalf("Blobs.Put %s: %v", subject, err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID:  p.ID,
			InternalDate: time.Now(),
			ReceivedAt:   time.Now(),
			Size:         blob.Size,
			Blob:         blob,
			Envelope:     store.Envelope{Subject: subject},
		}, []store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
			t.Fatalf("InsertMessage %s: %v", subject, err)
		}
	}

	byName := make(map[string]store.MessageID, len(subjects))
	for _, subject := range subjects {
		byName[subject] = findSpamVerdictMessageBySubject(t, st, []store.PrincipalID{p.ID}, subject)
	}
	return p.ID, byName
}

// startReclassifyPlugin builds (once per call) and starts classifierfixture
// scripted to always return verdict, waits for it to reach StateHealthy,
// and returns a spam.Classifier backed by it plus a shutdown func.
func startReclassifyPlugin(t *testing.T, clk clock.Clock, verdict string) (*spam.Classifier, string, func()) {
	t.Helper()
	fixturePath := buildClassifierFixture(t)
	t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", verdict)

	mgr := plugin.NewManager(plugin.ManagerOptions{Clock: clk, ServerVersion: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	pl, err := mgr.Start(ctx, plugin.Spec{
		Name:      "classifierfixture",
		Path:      fixturePath,
		Type:      plugin.TypeClassifier,
		Lifecycle: plugin.LifecycleLongRunning,
	})
	if err != nil {
		cancel()
		t.Fatalf("plugin Start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && pl.State() != plugin.StateHealthy {
		time.Sleep(20 * time.Millisecond)
	}
	if pl.State() != plugin.StateHealthy {
		t.Fatalf("plugin did not reach healthy state (current=%s)", pl.State())
	}
	cls := spam.New(pluginInvoker{mgr: mgr}, nil, clk)
	shutdown := func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = mgr.Shutdown(sctx)
		cancel()
	}
	return cls, "classifierfixture", shutdown
}

func TestSpamReclassify_SQLite(t *testing.T) {
	testSpamReclassify(t, func() store.Store { return sqlitetest.Open(t, clock.NewReal()) })
}

func TestSpamReclassify_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	testSpamReclassify(t, func() store.Store {
		st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
		if err != nil {
			t.Skipf("storepg.Open: %v", err)
		}
		if tr, ok := st.(interface {
			TruncateAll(ctx context.Context) error
		}); ok {
			if err := tr.TruncateAll(context.Background()); err != nil {
				_ = st.Close()
				t.Fatalf("TruncateAll: %v", err)
			}
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	})
}

// testSpamReclassify is the backend-agnostic body: each scenario opens
// its own store (via newStore) since each needs a differently-scripted
// classifier plugin process.
func testSpamReclassify(t *testing.T, newStore func() store.Store) {
	if testing.Short() {
		t.Skip("builds and runs a real plugin child process")
	}
	ctx := context.Background()
	clk := clock.NewReal()

	t.Run("unclassified spam moved to Junk", func(t *testing.T) {
		st := newStore()
		pid, msg := spamReclassifyFixture(t, st, "new-spam")
		cls, plugName, shutdown := startReclassifyPlugin(t, clk, "spam")
		defer shutdown()

		sum, err := reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: true})
		if err != nil {
			t.Fatalf("reclassifySpam: %v", err)
		}
		want := SpamReclassifySummary{Selected: 1, Classified: 1, Spam: 1, Moved: 1}
		if sum != want {
			t.Fatalf("summary = %+v, want %+v", sum, want)
		}
		if got := mailboxSet(t, st, msg["new-spam"]); len(got) != 1 || !got["Junk"] {
			t.Errorf("new-spam mailboxes = %v, want {Junk}", got)
		}
		rec, err := st.Meta().GetLLMClassification(ctx, msg["new-spam"])
		if err != nil {
			t.Fatalf("GetLLMClassification: %v", err)
		}
		if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
			t.Errorf("SpamVerdict = %v, want spam", rec.SpamVerdict)
		}
		if rec.SpamModel == nil || *rec.SpamModel != plugName {
			t.Errorf("SpamModel = %v, want %q", rec.SpamModel, plugName)
		}
	})

	t.Run("ham left in place", func(t *testing.T) {
		st := newStore()
		pid, msg := spamReclassifyFixture(t, st, "new-ham")
		cls, plugName, shutdown := startReclassifyPlugin(t, clk, "ham")
		defer shutdown()

		sum, err := reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: true})
		if err != nil {
			t.Fatalf("reclassifySpam: %v", err)
		}
		want := SpamReclassifySummary{Selected: 1, Classified: 1, Ham: 1}
		if sum != want {
			t.Fatalf("summary = %+v, want %+v", sum, want)
		}
		if got := mailboxSet(t, st, msg["new-ham"]); len(got) != 1 || !got["INBOX"] {
			t.Errorf("new-ham mailboxes = %v, want {INBOX}", got)
		}
	})

	t.Run("already-classified skipped by default, reclassified with flag", func(t *testing.T) {
		st := newStore()
		pid, msg := spamReclassifyFixture(t, st, "stale-verdict")
		mid := msg["stale-verdict"]
		staleVerdict := "ham"
		staleScore := 0.05
		if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
			MessageID:      mid,
			PrincipalID:    pid,
			SpamVerdict:    &staleVerdict,
			SpamConfidence: &staleScore,
		}); err != nil {
			t.Fatalf("seed stale SetLLMClassification: %v", err)
		}

		cls, plugName, shutdown := startReclassifyPlugin(t, clk, "spam")
		defer shutdown()

		sum, err := reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: true})
		if err != nil {
			t.Fatalf("reclassifySpam (default): %v", err)
		}
		want := SpamReclassifySummary{Selected: 1, Skipped: 1}
		if sum != want {
			t.Fatalf("summary (default) = %+v, want %+v", sum, want)
		}
		rec, err := st.Meta().GetLLMClassification(ctx, mid)
		if err != nil {
			t.Fatalf("GetLLMClassification: %v", err)
		}
		if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
			t.Errorf("SpamVerdict after skip = %v, want unchanged ham", rec.SpamVerdict)
		}
		if got := mailboxSet(t, st, mid); len(got) != 1 || !got["INBOX"] {
			t.Errorf("mailboxes after skip = %v, want {INBOX} (not moved)", got)
		}

		sum, err = reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: false})
		if err != nil {
			t.Fatalf("reclassifySpam (unclassified-only=false): %v", err)
		}
		want = SpamReclassifySummary{Selected: 1, Classified: 1, Spam: 1, Moved: 1}
		if sum != want {
			t.Fatalf("summary (forced) = %+v, want %+v", sum, want)
		}
		rec, err = st.Meta().GetLLMClassification(ctx, mid)
		if err != nil {
			t.Fatalf("GetLLMClassification: %v", err)
		}
		if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
			t.Errorf("SpamVerdict after forced reclassify = %v, want spam", rec.SpamVerdict)
		}
		if got := mailboxSet(t, st, mid); len(got) != 1 || !got["Junk"] {
			t.Errorf("mailboxes after forced reclassify = %v, want {Junk}", got)
		}
	})

	t.Run("recorded unclassified verdict still selected (re #326)", func(t *testing.T) {
		st := newStore()
		pid, msg := spamReclassifyFixture(t, st, "prior-unclassified")
		mid := msg["prior-unclassified"]
		// A prior classifier attempt timed out and (re #326) IS now
		// recorded, verdict "unclassified" with a reason -- unlike a
		// genuine ham/spam/suspect verdict, this must still count as
		// missed classification for --unclassified-only, or a message
		// that hit a transient plugin outage would never get a second
		// chance.
		priorVerdict := "unclassified"
		priorReason := "timeout: json-rpc error -32001: rpc deadline exceeded"
		// No SpamConfidence (re #326): a real Unclassified outcome
		// never produced a score.
		if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
			MessageID:   mid,
			PrincipalID: pid,
			SpamVerdict: &priorVerdict,
			SpamReason:  &priorReason,
		}); err != nil {
			t.Fatalf("seed prior unclassified SetLLMClassification: %v", err)
		}

		cls, plugName, shutdown := startReclassifyPlugin(t, clk, "spam")
		defer shutdown()

		sum, err := reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: true})
		if err != nil {
			t.Fatalf("reclassifySpam: %v", err)
		}
		want := SpamReclassifySummary{Selected: 1, Classified: 1, Spam: 1, Moved: 1}
		if sum != want {
			t.Fatalf("summary = %+v, want %+v (a recorded \"unclassified\" verdict must not be skipped)", sum, want)
		}
		rec, err := st.Meta().GetLLMClassification(ctx, mid)
		if err != nil {
			t.Fatalf("GetLLMClassification: %v", err)
		}
		if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
			t.Errorf("SpamVerdict after reclassify = %v, want spam", rec.SpamVerdict)
		}
	})

	t.Run("dry-run writes nothing", func(t *testing.T) {
		st := newStore()
		pid, msg := spamReclassifyFixture(t, st, "dry-run-msg")
		mid := msg["dry-run-msg"]
		cls, plugName, shutdown := startReclassifyPlugin(t, clk, "spam")
		defer shutdown()

		sum, err := reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: true, DryRun: true})
		if err != nil {
			t.Fatalf("reclassifySpam: %v", err)
		}
		want := SpamReclassifySummary{Selected: 1, Classified: 1, Spam: 1, Moved: 1}
		if sum != want {
			t.Fatalf("summary = %+v, want %+v", sum, want)
		}
		if _, err := st.Meta().GetLLMClassification(ctx, mid); err == nil {
			t.Error("GetLLMClassification: want ErrNotFound after dry-run, got a record")
		}
		if got := mailboxSet(t, st, mid); len(got) != 1 || !got["INBOX"] {
			t.Errorf("mailboxes after dry-run = %v, want {INBOX} (nothing moved)", got)
		}
	})

	t.Run("undo restores", func(t *testing.T) {
		st := newStore()
		pid, msg := spamReclassifyFixture(t, st, "undo-msg")
		mid := msg["undo-msg"]
		cls, plugName, shutdown := startReclassifyPlugin(t, clk, "spam")
		defer shutdown()

		var undoBuf bytes.Buffer
		undoWriter := csv.NewWriter(&undoBuf)
		if err := undoWriter.Write([]string{"message_id", "previous_mailbox_ids"}); err != nil {
			t.Fatalf("write undo header: %v", err)
		}

		sum, err := reclassifySpam(ctx, st, clk, cls, plugName, pid, spamReclassifyOptions{UnclassifiedOnly: true, UndoLog: undoWriter})
		if err != nil {
			t.Fatalf("reclassifySpam: %v", err)
		}
		undoWriter.Flush()
		if err := undoWriter.Error(); err != nil {
			t.Fatalf("flush undo log: %v", err)
		}
		want := SpamReclassifySummary{Selected: 1, Classified: 1, Spam: 1, Moved: 1}
		if sum != want {
			t.Fatalf("summary = %+v, want %+v", sum, want)
		}
		if got := mailboxSet(t, st, mid); len(got) != 1 || !got["Junk"] {
			t.Fatalf("mailboxes before undo = %v, want {Junk}", got)
		}

		rows, err := parseSpamUndoCSV(strings.NewReader(undoBuf.String()))
		if err != nil {
			t.Fatalf("parseSpamUndoCSV: %v", err)
		}
		undoSum, err := undoSpamVerdicts(ctx, st, pid, rows, false)
		if err != nil {
			t.Fatalf("undoSpamVerdicts: %v", err)
		}
		if undoSum.Restored != 1 {
			t.Fatalf("undo restored = %d, want 1", undoSum.Restored)
		}
		if got := mailboxSet(t, st, mid); len(got) != 1 || !got["INBOX"] {
			t.Errorf("mailboxes after undo = %v, want {INBOX}", got)
		}
	})
}

func TestParseSpamReclassifySince(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	if got, err := parseSpamReclassifySince("", now); err != nil || got != nil {
		t.Errorf("empty --since = (%v, %v), want (nil, nil)", got, err)
	}

	ts, err := parseSpamReclassifySince("2026-09-01T00:00:00Z", now)
	if err != nil {
		t.Fatalf("RFC3339 --since: %v", err)
	}
	if ts == nil || !ts.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC3339 --since = %v, want 2026-09-01T00:00:00Z", ts)
	}

	dur, err := parseSpamReclassifySince("24h", now)
	if err != nil {
		t.Fatalf("duration --since: %v", err)
	}
	if dur == nil || !dur.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("duration --since = %v, want %v", dur, now.Add(-24*time.Hour))
	}

	if _, err := parseSpamReclassifySince("not-a-time", now); err == nil {
		t.Error("invalid --since: want error, got nil")
	}
	if _, err := parseSpamReclassifySince("-1h", now); err == nil {
		t.Error("negative duration --since: want error, got nil")
	}
}
