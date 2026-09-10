package admin

// imap_import_spam_test.go covers the #300 imapImportSpamAdapter
// (REQ-FILT-02): Classify forwards to the shared spam.Classifier and
// degrades a plugin error to spam.Unclassified exactly like protosmtp's
// classify() helper; RecordVerdict persists the llm_classifications
// transparency row (REQ-FILT-66) and is a no-op for an Unclassified
// verdict.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
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
	got := adapter.Classify(context.Background(), msg)
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
	got := adapter.Classify(context.Background(), msg)
	if got.Verdict != spam.Unclassified {
		t.Errorf("Verdict = %v, want spam.Unclassified", got.Verdict)
	}
}

// TestIMAPImportSpamAdapter_ClassifyNilClassifier verifies a nil
// spam.Classifier (defensive; the real wiring always constructs one)
// degrades to Unclassified rather than panicking.
func TestIMAPImportSpamAdapter_ClassifyNilClassifier(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(context.Background(), msg)
	if got.Verdict != spam.Unclassified {
		t.Errorf("Verdict = %v, want spam.Unclassified", got.Verdict)
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
