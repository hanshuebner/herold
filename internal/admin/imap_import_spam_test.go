package admin

// imap_import_spam_test.go covers the #300 imapImportSpamAdapter
// (REQ-FILT-02): Classify forwards to the shared spam.Classifier and
// degrades a plugin error to spam.Unclassified exactly like protosmtp's
// classify() helper, carrying a "<class>: <detail>" Reason when an
// attempt was made and failed (re #326); RecordVerdict persists the
// llm_classifications transparency row (REQ-FILT-66) for a genuine
// verdict or an attempted-and-failed Unclassified outcome, and is a
// no-op only for the "no plugin configured, no attempt made" case.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
)

// fakeSpamInvoker is a minimal spam.PluginInvoker: Call returns a scripted
// raw JSON response, or a scripted error, for the configured plugin name.
type fakeSpamInvoker struct {
	plugin string
	raw    json.RawMessage
	err    error
}

func (f *fakeSpamInvoker) Call(_ context.Context, plugin, _ string, _ any, result any) error {
	if plugin != f.plugin {
		return errors.New("fakeSpamInvoker: unexpected plugin " + plugin)
	}
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal(f.raw, result)
}

func buildSpamTestMessage(t *testing.T) mailparse.Message {
	t.Helper()
	raw := "From: sender@example.com\r\nTo: bob@example.com\r\n" +
		"Subject: adapter test\r\nMessage-ID: <spam-adapter-01@example.com>\r\n\r\n" +
		"Hello, this is a test message for spam classification.\r\n"
	msg, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	return msg
}

// TestIMAPImportSpamAdapter_Classify verifies Classify forwards the plugin's
// verdict.
func TestIMAPImportSpamAdapter_Classify(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"spam","score":0.93,"reason":"looks bad","model":"test-model"}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg)
	if got.Verdict != spam.Spam {
		t.Errorf("Verdict = %v, want spam.Spam", got.Verdict)
	}
	if got.Score != 0.93 {
		t.Errorf("Score = %v, want 0.93", got.Score)
	}
}

// TestIMAPImportSpamAdapter_ClassifyDegradesOnPluginError verifies a plugin
// error (simulating a timeout or crash) degrades to
// spam.Classification{Verdict: spam.Unclassified}, matching protosmtp's
// classify() helper -- the caller never sees an error.
func TestIMAPImportSpamAdapter_ClassifyDegradesOnPluginError(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", err: context.DeadlineExceeded}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg)
	if got.Verdict != spam.Unclassified {
		t.Errorf("Verdict = %v, want spam.Unclassified", got.Verdict)
	}
	// re #326: an attempted-and-failed classification carries a
	// "<class>: <detail>" Reason so RecordVerdict can persist it.
	if got.Reason == "" {
		t.Errorf("Reason is empty, want a non-empty timeout reason")
	}
	if !strings.HasPrefix(got.Reason, "timeout: ") {
		t.Errorf("Reason = %q, want a timeout: prefix", got.Reason)
	}
}

// TestIMAPImportSpamAdapter_ClassifyNilClassifier verifies a nil
// spam.Classifier (defensive; the real wiring always constructs one)
// degrades to Unclassified rather than panicking, with an empty Reason
// (re #326: "no plugin configured, no attempt made" is the one
// Unclassified case RecordVerdict must NOT persist a row for).
func TestIMAPImportSpamAdapter_ClassifyNilClassifier(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg)
	if got.Verdict != spam.Unclassified {
		t.Errorf("Verdict = %v, want spam.Unclassified", got.Verdict)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty (no attempt made)", got.Reason)
	}
}

// TestIMAPImportSpamAdapter_ClassifyNilClassifier_StructuralFallback is
// the regression test for a defect found while writing the #304
// acceptance matrix: REQ-FILT-214/ADR-0002 requires the structural
// fallback categoriser to run whenever no classifier plugin is
// installed, but Classify used to return Classification{Verdict:
// Unclassified} immediately on a nil Classifier without ever reaching
// the category-resolution switch. A List-Id message imported with no
// spam plugin configured therefore never got $category-forums.
func TestIMAPImportSpamAdapter_ClassifyNilClassifier_StructuralFallback(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	raw := "From: sender@example.com\r\nTo: bob@example.com\r\n" +
		"List-Id: <announce.example.com>\r\n" +
		"Subject: list mail, no plugin configured\r\nMessage-ID: <spam-adapter-fallback@example.com>\r\n\r\n" +
		"Hello.\r\n"
	msg, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	// PrincipalID(1) has no seeded row yet; GetCategorisationConfig
	// auto-seeds the enabled-by-default config on first read (same as
	// production), so categorisation is enabled here without any setup.
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg)
	if got.Verdict != spam.Unclassified {
		t.Errorf("Verdict = %v, want spam.Unclassified", got.Verdict)
	}
	if got.Category != "forums" {
		t.Errorf("Category = %q, want \"forums\" (structural fallback, REQ-FILT-214)", got.Category)
	}
}

// TestIMAPImportSpamAdapter_RecordVerdict verifies RecordVerdict persists
// the llm_classifications transparency row (REQ-FILT-66).
func TestIMAPImportSpamAdapter_RecordVerdict(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	p, msg := seedPrincipalAndMessage(t, ctx, st, "spam-record-01@example.com")

	parsedMsg := buildSpamTestMessage(t)
	classification := spam.Classification{
		Verdict:     spam.Spam,
		Score:       0.87,
		RawResponse: map[string]any{"reason": "bulk sender", "model": "test-model"},
	}
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, classification)

	rec, err := st.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Errorf("SpamVerdict = %v, want \"spam\"", rec.SpamVerdict)
	}
	if rec.SpamConfidence == nil || *rec.SpamConfidence != 0.87 {
		t.Errorf("SpamConfidence = %v, want 0.87", rec.SpamConfidence)
	}
	if rec.SpamReason == nil || *rec.SpamReason != "bulk sender" {
		t.Errorf("SpamReason = %v, want \"bulk sender\"", rec.SpamReason)
	}
	if rec.SpamModel == nil || *rec.SpamModel != "test-model" {
		t.Errorf("SpamModel = %v, want \"test-model\"", rec.SpamModel)
	}
	if rec.SpamClassifiedAt == nil || !rec.SpamClassifiedAt.Equal(clk.Now()) {
		t.Errorf("SpamClassifiedAt = %v, want %v", rec.SpamClassifiedAt, clk.Now())
	}
}

// TestIMAPImportSpamAdapter_RecordVerdictNoopOnUnclassified verifies
// RecordVerdict never writes a row for an Unclassified verdict.
func TestIMAPImportSpamAdapter_RecordVerdictNoopOnUnclassified(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	p, msg := seedPrincipalAndMessage(t, ctx, st, "spam-record-02@example.com")
	parsedMsg := buildSpamTestMessage(t)
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, spam.Classification{Verdict: spam.Unclassified, Score: -1})

	if _, err := st.Meta().GetLLMClassification(ctx, msg.ID); err == nil {
		t.Errorf("GetLLMClassification succeeded for an unclassified verdict; want not-found")
	}
}

// TestIMAPImportSpamAdapter_RecordVerdictPersistsUnclassifiedWithReason
// covers re #326: an Unclassified outcome that DID carry a Reason (a
// classifier invocation was attempted and failed -- timeout, plugin
// error, or unparseable output) IS persisted, verdict "unclassified",
// distinguishable from a genuine ham verdict and from the "no plugin
// configured" no-attempt case (the sibling Noop test above).
func TestIMAPImportSpamAdapter_RecordVerdictPersistsUnclassifiedWithReason(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	p, msg := seedPrincipalAndMessage(t, ctx, st, "spam-record-03@example.com")
	parsedMsg := buildSpamTestMessage(t)
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, spam.Classification{
		Verdict: spam.Unclassified,
		Score:   -1,
		Reason:  "timeout: json-rpc error -32001: rpc deadline exceeded",
	})

	rec, err := st.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "unclassified" {
		t.Errorf("SpamVerdict = %v, want \"unclassified\"", rec.SpamVerdict)
	}
	if rec.SpamReason == nil || !strings.HasPrefix(*rec.SpamReason, "timeout: ") {
		t.Errorf("SpamReason = %v, want a timeout: prefix", rec.SpamReason)
	}
}
