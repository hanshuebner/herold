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
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg, "")
	if got.Verdict != spam.Spam {
		t.Errorf("Verdict = %v, want spam.Spam", got.Verdict)
	}
	if got.Score != 0.93 {
		t.Errorf("Score = %v, want 0.93", got.Score)
	}
}

// TestIMAPImportSpamAdapter_Classify_DecisiveSignalResolvesHamToSpam is
// the #396 (second round) regression test for the import path: a Ham
// verdict whose spam_signals match a decisive signal must resolve to
// Spam through the real spam.Classifier the adapter wraps, exactly as
// the SMTP delivery path does (internal/protosmtp's
// deliver_llm_decisive_signal_test.go).
func TestIMAPImportSpamAdapter_Classify_DecisiveSignalResolvesHamToSpam(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"ham","score":0.15,"reason":"promo","spam_signals":["unsolicited_bulk_marketing"]}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg, "")
	if got.Verdict != spam.Spam {
		t.Fatalf("Verdict = %v, want spam.Spam (decisive signal on a Ham verdict)", got.Verdict)
	}
	if got.ModelVerdict != spam.Ham {
		t.Fatalf("ModelVerdict = %v, want spam.Ham", got.ModelVerdict)
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
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg, "")
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
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg, "")
	if got.Verdict != spam.Unclassified {
		t.Errorf("Verdict = %v, want spam.Unclassified", got.Verdict)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty (no attempt made)", got.Reason)
	}
}

// TestIMAPImportSpamAdapter_ClassifyEmptyPluginName covers the other
// "not configured" shape (re #326): a non-nil *spam.Classifier (the
// real wiring always constructs one) but an empty plugin name -- what
// internal/admin/server.go's firstPluginOfType returns when no
// [[plugin]] of type spam/classifier is configured. No RPC attempt is
// made (the fake invoker would error if called), Verdict is
// Unclassified with an empty Reason, matching the nil-classifier case
// above.
func TestIMAPImportSpamAdapter_ClassifyEmptyPluginName(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"spam","score":0.9}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "", st, clk, slog.Default())

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg, "")
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
	got := adapter.Classify(context.Background(), store.PrincipalID(1), msg, "")
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
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, classification, "")

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

// TestIMAPImportSpamAdapter_RecordVerdict_SpamModelFallsBackToPluginName
// is the #396 (second round, item 3) regression test: a response
// carrying no "model" key -- the shape every shipped classifier plugin's
// SpamClassifyResult/MailClassifyResult produces on the wire -- must
// still record SpamModel as the adapter's configured plugin name,
// mirroring protosmtp's persistLLMRecord fix from the first round. The
// sibling TestIMAPImportSpamAdapter_RecordVerdict above cannot catch
// this: it always supplies a "model" key in RawResponse, exercising only
// the (never-shipped) defensive override.
func TestIMAPImportSpamAdapter_RecordVerdict_SpamModelFallsBackToPluginName(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	p, msg := seedPrincipalAndMessage(t, ctx, st, "spam-record-model-01@example.com")

	parsedMsg := buildSpamTestMessage(t)
	classification := spam.Classification{
		Verdict:     spam.Spam,
		Score:       0.9,
		RawResponse: map[string]any{"reason": "bulk sender"},
	}
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, classification, "")

	rec, err := st.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamModel == nil || *rec.SpamModel != "spam-plug" {
		t.Fatalf("SpamModel = %v, want %q (the configured plugin name -- the import path must record a model like the SMTP path does)", rec.SpamModel, "spam-plug")
	}
}

// TestIMAPImportSpamAdapter_RecordVerdict_ModelVerdictPreserved is the
// #396 (second round, item 1) regression test for the transparency
// record on the import path: when Classify server-resolved a Ham
// verdict to Spam on a decisive spam signal, RecordVerdict must persist
// the plugin's own original verdict as SpamModelVerdict alongside the
// applied SpamVerdict.
func TestIMAPImportSpamAdapter_RecordVerdict_ModelVerdictPreserved(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(nil, "spam-plug", st, clk, slog.Default())

	p, msg := seedPrincipalAndMessage(t, ctx, st, "spam-record-model-02@example.com")

	parsedMsg := buildSpamTestMessage(t)
	classification := spam.Classification{
		Verdict:      spam.Spam,
		ModelVerdict: spam.Ham,
		Score:        0.15,
		SpamSignals:  []string{"unsolicited_bulk_marketing"},
		Inconsistent: true,
	}
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, classification, "")

	rec, err := st.Meta().GetLLMClassification(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %v, want spam", rec.SpamVerdict)
	}
	if rec.SpamModelVerdict == nil || *rec.SpamModelVerdict != "ham" {
		t.Fatalf("SpamModelVerdict = %v, want ham (the plugin's own original verdict)", rec.SpamModelVerdict)
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
	adapter.RecordVerdict(ctx, p.ID, msg.ID, parsedMsg, spam.Classification{Verdict: spam.Unclassified, Score: -1}, "")

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
	}, "")

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
	if rec.SpamConfidence != nil {
		t.Errorf("SpamConfidence = %v, want nil (no score was ever produced)", *rec.SpamConfidence)
	}
}

// TestIMAPImportSpamAdapter_Classify_NeverSpamOverride covers REQ-FILT-02a
// / REQ-FLT-16 (issue #382): a spam verdict on a message matched by a
// never-spam managed rule carries DeliveryOverride naming the rule, since
// Sieve never runs on the import path (REQ-IMAP-IMP-31) and this is the
// only place that decision is made for imported mail.
func TestIMAPImportSpamAdapter_Classify_NeverSpamOverride(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"spam","score":0.93,"reason":"looks bad"}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "neverspam@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if _, err := st.Meta().InsertManagedRule(ctx, store.ManagedRule{
		PrincipalID: p.ID,
		Name:        "Trusted senders",
		Enabled:     true,
		Conditions: []store.RuleCondition{
			{Field: "from", Op: "contains", Value: "sender@example.com"},
		},
		Actions: []store.RuleAction{{Kind: "never-spam"}},
	}); err != nil {
		t.Fatalf("InsertManagedRule: %v", err)
	}

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(ctx, p.ID, msg, "")
	if got.Verdict != spam.Spam {
		t.Fatalf("Verdict = %v, want spam.Spam", got.Verdict)
	}
	if got.DeliveryOverride != "filter:Trusted senders" {
		t.Errorf("DeliveryOverride = %q, want %q", got.DeliveryOverride, "filter:Trusted senders")
	}
}

// TestIMAPImportSpamAdapter_Classify_NeverSpamOverride_FromDomain is the
// from-domain counterpart of TestIMAPImportSpamAdapter_Classify_NeverSpamOverride
// (re #382): a never-spam rule keyed on the sender's domain must also
// override the verdict on the IMAP-import path, which never runs Sieve
// (REQ-IMAP-IMP-31) and instead matches the rule's conditions directly via
// sieve.NeverSpamOverrideLabel.
func TestIMAPImportSpamAdapter_Classify_NeverSpamOverride_FromDomain(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"spam","score":0.93,"reason":"looks bad"}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "neverspamdomain@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	if _, err := st.Meta().InsertManagedRule(ctx, store.ManagedRule{
		PrincipalID: p.ID,
		Name:        "Trusted domains",
		Enabled:     true,
		Conditions: []store.RuleCondition{
			{Field: "from-domain", Op: "equals", Value: "example.com"},
		},
		Actions: []store.RuleAction{{Kind: "never-spam"}},
	}); err != nil {
		t.Fatalf("InsertManagedRule: %v", err)
	}

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(ctx, p.ID, msg, "")
	if got.Verdict != spam.Spam {
		t.Fatalf("Verdict = %v, want spam.Spam", got.Verdict)
	}
	if got.DeliveryOverride != "filter:Trusted domains" {
		t.Errorf("DeliveryOverride = %q, want %q", got.DeliveryOverride, "filter:Trusted domains")
	}
}

// TestIMAPImportSpamAdapter_Classify_NoNeverSpamRule_NoOverride is the
// unmatched-sender control: no managed rule, no override.
func TestIMAPImportSpamAdapter_Classify_NoNeverSpamRule_NoOverride(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"spam","score":0.93,"reason":"looks bad"}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "no-rule@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	msg := buildSpamTestMessage(t)
	got := adapter.Classify(ctx, p.ID, msg, "")
	if got.DeliveryOverride != "" {
		t.Errorf("DeliveryOverride = %q, want empty (no never-spam rule configured)", got.DeliveryOverride)
	}
}

// -- re #396, third round: own_addresses on the import path -----------------
//
// The four tests below reproduce the maintainer's hand-back on the second
// round's fix (comment 5010): own_addresses on the IMAP-import path was
// built only from the principal's identities/aliases, so a shared
// organisational mailbox's info@/vorstand@ alias -- received only through
// one specific imapimport_account -- made recipient_not_own true for every
// message delivered there, and (once recipient_not_own alone became
// decisive in the second round) turned legitimate transactional mail into
// false-positive Junk moves.

// newIMAPImportTestAccount inserts a principal and an imapimport_account
// bound to it (no owning Identity -- accountEmail becomes the Username
// fallback own_addresses.go resolves), returning both. ownAddresses, when
// non-empty, is the account's configured own-address list (store.
// IMAPImportAccount.OwnAddresses); pass nil for none.
func newIMAPImportTestAccount(t *testing.T, ctx context.Context, st store.Store, principalEmail, accountEmail string, ownAddresses []string) (store.Principal, store.IMAPImportAccount) {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: principalEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	acc, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Test Account",
		Host:         "imap.example.test",
		Port:         993,
		TLSMode:      store.IMAPImportTLSModeImplicit,
		Username:     accountEmail,
		AuthMethod:   store.IMAPImportAuthMethodPassword,
		CredentialCT: []byte("v1:test"),
		State:        store.IMAPImportAccountStateEnabled,
		OwnAddresses: ownAddresses,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	return p, acc
}

// parseImapImportSpamTestMessage parses raw as an RFC822 message for the
// tests below (buildSpamTestMessage's body is fixed; these tests need
// varying To/List-Id headers).
func parseImapImportSpamTestMessage(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	return msg
}

// TestIMAPImportSpamAdapter_Classify_RecipientNotOwnAloneNeverDecisive is
// the #396 third-round regression test for required outcome 2:
// recipient_not_own alone -- even on an account whose own-address set is
// known complete -- must never resolve a Ham verdict to Spam. Plain
// transactional content, no mailing-list headers, so bulk_list_relay never
// enters the picture.
func TestIMAPImportSpamAdapter_Classify_RecipientNotOwnAloneNeverDecisive(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"order confirmation","spam_signals":["recipient_not_own"],"ham_signals":["known_business_sender"]}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	_, acc := newIMAPImportTestAccount(t, ctx, st, "recipient-not-own-alone@example.test", "recipient-not-own-alone@example.test", nil)
	// Mark the account's own-address set complete with nothing extra
	// learned, mirroring what runOwnAddressBackfill does for an account
	// whose headers never revealed another address.
	if err := st.Meta().SetIMAPImportLearnedAddresses(ctx, acc.ID, nil); err != nil {
		t.Fatalf("SetIMAPImportLearnedAddresses: %v", err)
	}

	msg := parseImapImportSpamTestMessage(t, "From: wir-machen-druck.de <order@wir-machen-druck.de>\r\n"+
		"To: not-this-account@elsewhere.test\r\n"+
		"Subject: Your order status\r\nMessage-ID: <recipient-not-own-alone@example.com>\r\n\r\n"+
		"Your order has shipped.\r\n")
	got := adapter.Classify(ctx, acc.PrincipalID, msg, acc.ID)
	if got.Verdict != spam.Ham {
		t.Fatalf("Verdict = %v, want spam.Ham (recipient_not_own alone must never be decisive)", got.Verdict)
	}
	if got.ModelVerdict != spam.Unclassified {
		t.Fatalf("ModelVerdict = %v, want spam.Unclassified (no resolution should have happened)", got.ModelVerdict)
	}
}

// TestIMAPImportSpamAdapter_Classify_IncompleteAccountNeverDecisiveForRecipientNotOwn
// is the #396 third-round regression test for the other half of required
// outcome 2: even recipient_not_own combined with bulk_list_relay must
// never be decisive when the account's own-address set is known
// incomplete (no configured list and no completed learning pass) --
// SetIMAPImportLearnedAddresses is deliberately never called here.
func TestIMAPImportSpamAdapter_Classify_IncompleteAccountNeverDecisiveForRecipientNotOwn(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"ham","score":0.08,"reason":"automated acknowledgment","spam_signals":["recipient_not_own"],"ham_signals":[]}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	_, acc := newIMAPImportTestAccount(t, ctx, st, "incomplete-account@example.test", "incomplete-account@example.test", nil)

	msg := parseImapImportSpamTestMessage(t, "From: no-reply@jamestown.example\r\n"+
		"To: 3rc@xxdz88.com\r\nList-Id: <3rc.xxdz88.com>\r\nPrecedence: list\r\n"+
		"Subject: Ihr Anliegen\r\nMessage-ID: <incomplete-account@example.com>\r\n\r\n"+
		"Vielen Dank fuer Ihre Anfrage.\r\n")
	got := adapter.Classify(ctx, acc.PrincipalID, msg, acc.ID)
	if got.Verdict != spam.Ham {
		t.Fatalf("Verdict = %v, want spam.Ham (an incomplete own-address set must never let recipient_not_own be decisive, even combined with bulk_list_relay)", got.Verdict)
	}
}

// TestIMAPImportSpamAdapter_Classify_RelayedAutoReplyToNonOwnedAddressStillJunk
// is the #396 third-round test for required outcome 4's second evaluation
// case: on an account whose own-address set IS known complete, the
// comment-4833 relayed-auto-reply shape (a throwaway group address,
// List-Id, Precedence: list) must still resolve to Spam -- carried by the
// combination of recipient_not_own and the server-computed bulk_list_relay
// signal.
func TestIMAPImportSpamAdapter_Classify_RelayedAutoReplyToNonOwnedAddressStillJunk(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	invoker := &fakeSpamInvoker{plugin: "spam-plug", raw: json.RawMessage(`{"verdict":"ham","score":0.08,"reason":"automated acknowledgment from a legitimate business","spam_signals":["recipient_not_own"],"ham_signals":[]}`)}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	_, acc := newIMAPImportTestAccount(t, ctx, st, "relayed-autoreply@example.test", "relayed-autoreply@example.test", nil)
	if err := st.Meta().SetIMAPImportLearnedAddresses(ctx, acc.ID, nil); err != nil {
		t.Fatalf("SetIMAPImportLearnedAddresses: %v", err)
	}

	msg := parseImapImportSpamTestMessage(t, "From: no-reply@jamestown.example\r\n"+
		"To: 3rc@xxdz88.com\r\nList-Id: <3rc.xxdz88.com>\r\nPrecedence: list\r\n"+
		"Subject: Ihr Anliegen\r\nMessage-ID: <relayed-autoreply@example.com>\r\n\r\n"+
		"Vielen Dank fuer Ihre Anfrage.\r\n")
	got := adapter.Classify(ctx, acc.PrincipalID, msg, acc.ID)
	if got.Verdict != spam.Spam {
		t.Fatalf("Verdict = %v, want spam.Spam (recipient_not_own + bulk_list_relay is decisive on a complete account)", got.Verdict)
	}
	if got.ModelVerdict != spam.Ham {
		t.Fatalf("ModelVerdict = %v, want spam.Ham", got.ModelVerdict)
	}
}

// TestIMAPImportSpamAdapter_Classify_ConfiguredOwnAddressAvoidsFalsePositive
// is the #396 third-round test for required outcome 1 and the first three
// false-positive rows in the hand-back's table (messages 3684/3691/3692,
// print-shop order-status mail to info@classic-computing.de): once the
// account's own-address list is configured with the upstream alias,
// recipient_not_own is never even asserted for mail to it, so the message
// stays Ham regardless of the decisive-signal rules.
func TestIMAPImportSpamAdapter_Classify_ConfiguredOwnAddressAvoidsFalsePositive(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var gotReq spam.Request
	invoker := &fakeSpamInvokerFunc{plugin: "spam-plug", fn: func(raw json.RawMessage) (json.RawMessage, error) {
		if err := json.Unmarshal(raw, &gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return json.RawMessage(`{"verdict":"ham","score":0.05,"reason":"order status notification, DKIM pass, known business sender"}`), nil
	}}
	cls := spam.New(invoker, slog.Default(), clk)
	st := sqlitetest.Open(t, clk)
	adapter := newIMAPImportSpamAdapter(cls, "spam-plug", st, clk, slog.Default())

	_, acc := newIMAPImportTestAccount(t, ctx, st, "vorsitz@classic-computing.de", "vorsitz@classic-computing.de",
		[]string{"info@classic-computing.de", "vorstand@classic-computing.de"})

	msg := parseImapImportSpamTestMessage(t, "From: order-status@wir-machen-druck.de\r\n"+
		"To: info@classic-computing.de\r\n"+
		"Subject: Your print order has shipped\r\nMessage-ID: <configured-own-addr@example.com>\r\n\r\n"+
		"Your print order status has been updated.\r\n")
	got := adapter.Classify(ctx, acc.PrincipalID, msg, acc.ID)
	if got.Verdict != spam.Ham {
		t.Fatalf("Verdict = %v, want spam.Ham (info@classic-computing.de is a configured own address)", got.Verdict)
	}
	if gotReq.RecipientNotOwn {
		t.Fatalf("request recipient_not_own = true, want false: info@classic-computing.de is configured in own_addresses")
	}
	if !gotReq.OwnAddressesComplete {
		t.Fatalf("request own_addresses_complete = false, want true: the account has a configured own-address list")
	}
}

// fakeSpamInvokerFunc is a spam.PluginInvoker whose Call decodes params
// into a json.RawMessage and hands it to fn, letting a test both inspect
// the exact wire request (params is a spam.Request/MailClassifyRequest
// value, not raw bytes, so it is re-marshalled here) and script the
// response, unlike fakeSpamInvoker's fixed raw response.
type fakeSpamInvokerFunc struct {
	plugin string
	fn     func(json.RawMessage) (json.RawMessage, error)
}

func (f *fakeSpamInvokerFunc) Call(_ context.Context, plugin, _ string, params any, result any) error {
	if plugin != f.plugin {
		return errors.New("fakeSpamInvokerFunc: unexpected plugin " + plugin)
	}
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := f.fn(b)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}
