package imapimport

// classify_subprocess_e2e_test.go closes the gap noted in issue #304's
// stage 2-4 landing comment: no test yet exercised the IMAP import path
// against a REAL classifier plugin subprocess (only the in-process
// fakeSpamClassifier in spam_test.go). This file wires a genuine
// internal/plugin.Manager running internal/plugin/testdata/
// classifierfixture as a real child process (STANDARDS section 8, no
// mocks at the process boundary) into accountWorkerOpts.spamClassifier,
// mirroring internal/admin/imap_import_spam.go's adapter logic (which is
// unexported and therefore not importable from this package).
//
// Covers:
//   - a live-arrival import invokes the classifier exactly once and the
//     inserted message carries both a spam verdict (llm_classifications)
//     and a $category-* keyword (#304 acceptance item 1, IMAP leg).
//   - a plugin sleeping past the classify budget is cut off deterministically
//     via the harness's injected FakeClock, and the import proceeds with the
//     message unclassified in INBOX (#304 acceptance item 5, the
//     injected-clock half; the real-time-bounded half lives in
//     internal/admin's classify_acceptance_matrix_e2e_test.go).
//
// Runs on SQLite always and on Postgres when HEROLD_PG_DSN is set.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// runSyncOnceSpamNoDeadline is runSyncOnceSpam without the 15s
// context.WithTimeout runSyncOnceCfgSpam wraps every call in. That
// wrapper is fine for every other imapimport test (it just bounds a
// slow test), but it defeats spam.Classifier's own FakeClock-driven
// budget cutoff: Classify's deadline() helper only arms the injected
// Clock's timer when ctx carries NO deadline of its own
// (internal/spam/classifier.go); with any ancestor deadline present it
// falls back to real wall-clock cancellation instead. This variant
// passes ctx straight through with no deadline, so a caller that wants
// the deterministic FakeClock cutoff (TestClassifySubprocess_
// BudgetCutoff_FakeClock) gets it, bounding worst-case test hang with
// its own real-time select/timeout instead.
func runSyncOnceSpamNoDeadline(t *testing.T, ctx context.Context, ha *testharness.Server, ts *testIMAPServer, acc store.IMAPImportAccount, spamCl SpamClassifier) error {
	t.Helper()
	w := newAccountWorker(accountWorkerOpts{
		account:        acc,
		store:          ha.Store,
		dataKey:        testDataKey(t),
		cfg:            sysconfig.IMAPImportConfig{},
		log:            newTestLogger(t),
		clk:            ha.Clock,
		dialer:         &fakeDialer{ts: ts},
		categoriser:    noopCategoriser{},
		spamClassifier: spamCl,
	})
	credPlaintext, err := w.openCredential(ctx, acc)
	if err != nil {
		return err
	}
	conn, err := w.opts.dialer.Dial(ctx, dialParams{
		AccountID:           acc.ID,
		Host:                acc.Host,
		Port:                acc.Port,
		TLSMode:             string(acc.TLSMode),
		Username:            acc.Username,
		AuthMethod:          string(acc.AuthMethod),
		CredentialPlaintext: credPlaintext,
	})
	if err != nil {
		return err
	}
	defer func() {
		conn.Logout()
		conn.Close()
	}()
	return w.syncAllFolders(ctx, conn)
}

// buildClassifierFixtureBin compiles internal/plugin/testdata/
// classifierfixture into t.TempDir(), mirroring
// internal/admin/smtp_inbound_categorise_e2e_test.go's buildClassifierFixture
// (duplicated here: that helper is unexported in a different package).
func buildClassifierFixtureBin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "classifierfixture")
	cmd := exec.Command("go", "build", "-o", out, "github.com/hanshuebner/herold/internal/plugin/testdata/classifierfixture")
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build classifierfixture: %v\n%s", err, outb)
	}
	return out
}

// startClassifierPluginManager spawns a real classifierfixture child
// process under a real internal/plugin.Manager and waits for it to reach
// StateHealthy. Cleanup shuts the manager down.
func startClassifierPluginManager(t *testing.T, ctx context.Context, clk clock.Clock, bin string) *plug.Manager {
	t.Helper()
	mgr := plug.NewManager(plug.ManagerOptions{Clock: clk, ServerVersion: "test"})
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mgr.Shutdown(shutCtx)
	})
	p, err := mgr.Start(ctx, plug.Spec{
		Name:      "classifier",
		Path:      bin,
		Type:      plug.TypeClassifier,
		Lifecycle: plug.LifecycleLongRunning,
	})
	if err != nil {
		t.Fatalf("mgr.Start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p.State() == plug.StateHealthy {
			return mgr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("classifier plugin did not become healthy (state=%s)", p.State())
	return nil
}

// pluginManagerInvoker adapts *plugin.Manager to spam.PluginInvoker +
// spam.PluginTypeResolver, mirroring internal/admin/server.go's
// unexported pluginInvoker.
type pluginManagerInvoker struct{ mgr *plug.Manager }

func (p pluginManagerInvoker) Call(ctx context.Context, pluginName, method string, params any, result any) error {
	pl := p.mgr.Get(pluginName)
	if pl == nil {
		return fmt.Errorf("plugin %q not registered", pluginName)
	}
	return pl.Call(ctx, method, params, result)
}

func (p pluginManagerInvoker) PluginType(name string) (string, bool) {
	pl := p.mgr.Get(name)
	if pl == nil {
		return "", false
	}
	return string(pl.Type()), true
}

// testSpamAdapter implements SpamClassifier against a real spam.Classifier,
// mirroring internal/admin/imap_import_spam.go's imapImportSpamAdapter
// (unexported there, so re-implemented here): the structural fallback
// (ADR-0002) and the spam-verdict category drop (ADR-0004) are applied
// the same way.
type testSpamAdapter struct {
	cls    *spam.Classifier
	plugin string
	st     store.Store
}

func (a *testSpamAdapter) Classify(ctx context.Context, principalID store.PrincipalID, msg mailparse.Message) spam.Classification {
	clsCtx, categorisationEnabled := a.buildClassifyContext(ctx, principalID)
	cls := spam.Classification{Verdict: spam.Unclassified, Score: -1}
	if a.cls != nil {
		var err error
		// re #326: an attempted-and-failed call keeps cls.Reason (the
		// "<class>: <detail>" string spam.Classifier.Classify already
		// set) so RecordVerdict below can persist it, mirroring
		// internal/admin/imap_import_spam.go's real adapter.
		cls, err = a.cls.Classify(ctx, msg, nil, a.plugin, clsCtx)
		if err != nil {
			cls = spam.Classification{Verdict: spam.Unclassified, Score: -1, Reason: cls.Reason}
		}
	}
	switch {
	case cls.Verdict == spam.Spam:
		cls.Category = ""
	case !categorisationEnabled:
		cls.Category = ""
	case cls.Category == "":
		cls.Category = spam.StructuralCategory(msg)
	}
	return cls
}

func (a *testSpamAdapter) buildClassifyContext(ctx context.Context, principalID store.PrincipalID) (spam.ClassifyContext, bool) {
	base := spam.ClassifyContext{Principal: fmt.Sprint(principalID)}
	cfg, err := a.st.Meta().GetCategorisationConfig(ctx, principalID)
	if err != nil || !cfg.Enabled {
		return base, false
	}
	base.Prompt = cfg.Prompt
	if len(cfg.CategorySet) > 0 {
		base.Categories = make([]spam.CategoryOption, len(cfg.CategorySet))
		for i, c := range cfg.CategorySet {
			base.Categories[i] = spam.CategoryOption{Name: c.Name, Description: c.Description}
		}
	}
	return base, true
}

func (a *testSpamAdapter) RecordVerdict(ctx context.Context, principalID store.PrincipalID, messageID store.MessageID, _ mailparse.Message, classification spam.Classification) {
	// re #326: only the "no plugin configured, no attempt made" case
	// (Unclassified with no Reason) stays unrecorded; an
	// attempted-and-failed Unclassified outcome (Reason set) IS
	// persisted, mirroring internal/admin/imap_import_spam.go.
	if (classification.Verdict == spam.Unclassified && classification.Reason == "") || messageID == 0 {
		return
	}
	v := classification.Verdict.String()
	score := classification.Score
	rec := store.LLMClassificationRecord{MessageID: messageID, PrincipalID: principalID, SpamVerdict: &v, SpamConfidence: &score}
	if classification.Reason != "" {
		reason := classification.Reason
		rec.SpamReason = &reason
	}
	_ = a.st.Meta().SetLLMClassification(ctx, rec)
}

// countCallLogLines counts non-empty lines in a classifierfixture
// HEROLD_TEST_CLASSIFY_CALL_LOG file, one per real classify RPC handled.
func countCallLogLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// classifySubprocessBackends returns (name, store.Store, clock.Clock)
// triples: SQLite (fake clock) always, Postgres (fake clock) when
// HEROLD_PG_DSN is set. Mirrors
// internal/protoadmin/identity_submission_test.go's openSubmissionBackends.
type classifySubprocessBackend struct {
	name string
	st   store.Store
	clk  *clock.FakeClock
}

// classifySubprocessBackends anchors each backend's FakeClock at anchor.
// Item 1's scenario (below) always routes Classify through a
// context.WithTimeout-derived ctx that already carries a real deadline,
// so spam.Classifier's deadline() helper never consults the injected
// clock at all -- any fixed anchor works there. Item 5's scenario needs
// the injected clock's budget-cutoff path (deadline() only engages that
// path when ctx carries no deadline of its own), which internally does
// `context.WithDeadline(ctx, clk.Now().Add(timeout))` -- a REAL Go
// context, whose cancellation is scheduled against real wall-clock time
// regardless of which Clock produced the timestamp. An anchor far in the
// past (e.g. a fixed 2025-01-01) makes that computed deadline already
// elapsed relative to actual wall-clock "now", so the context is born
// already-Done and the call fails instantly instead of blocking until
// the test's explicit Advance. Anchoring at time.Now() keeps that
// computed deadline safely in the future in real wall-clock terms, so
// only the explicit Advance (via the timer registered on the same fake
// clock) cuts it off -- matching internal/spam's own
// TestClassify_BudgetCutoff_FakeClock, which anchors the same way.
func classifySubprocessBackends(t *testing.T, anchor time.Time) []classifySubprocessBackend {
	t.Helper()
	clkA := clock.NewFake(anchor)
	out := []classifySubprocessBackend{{name: "sqlite", st: sqlitetest.Open(t, clkA), clk: clkA}}
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		return out
	}
	clkB := clock.NewFake(anchor)
	blobDir := t.TempDir()
	st, err := storepg.Open(context.Background(), dsn, filepath.Join(blobDir, "blobs"), nil, clkB)
	if err != nil {
		t.Fatalf("storepg.Open: %v", err)
	}
	if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = st.Close() })
	return append(out, classifySubprocessBackend{name: "postgres", st: st, clk: clkB})
}

// openClassifySubprocessBackend opens a single named backend
// ("sqlite"/"postgres"), anchoring its FakeClock at time.Now() -- see
// classifySubprocessBackends' doc comment for why this must happen right
// before the caller's classify-timing-sensitive body runs, not once
// up front for a whole subtest matrix.
func openClassifySubprocessBackend(t *testing.T, name string) classifySubprocessBackend {
	t.Helper()
	clk := clock.NewFake(time.Now())
	switch name {
	case "sqlite":
		return classifySubprocessBackend{name: name, st: sqlitetest.Open(t, clk), clk: clk}
	case "postgres":
		dsn := os.Getenv("HEROLD_PG_DSN")
		blobDir := t.TempDir()
		st, err := storepg.Open(context.Background(), dsn, filepath.Join(blobDir, "blobs"), nil, clk)
		if err != nil {
			t.Fatalf("storepg.Open: %v", err)
		}
		if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
			if err := tr.TruncateAll(context.Background()); err != nil {
				t.Fatalf("TruncateAll: %v", err)
			}
		}
		t.Cleanup(func() { _ = st.Close() })
		return classifySubprocessBackend{name: name, st: st, clk: clk}
	default:
		t.Fatalf("unknown backend %q", name)
		return classifySubprocessBackend{}
	}
}

// TestClassifySubprocess_OneCallVerdictAndCategory is the IMAP-import leg
// of #304 acceptance item 1: a live-arrival import invokes a real
// classifierfixture subprocess exactly once and lands with both a spam
// verdict and a $category-* keyword.
func TestClassifySubprocess_OneCallVerdictAndCategory(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test: spawns a real plugin subprocess")
	}
	bin := buildClassifierFixtureBin(t)
	for _, be := range classifySubprocessBackends(t, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Run(be.name, func(t *testing.T) {
			callLog := filepath.Join(t.TempDir(), "calls.log")
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")
			t.Setenv("HEROLD_TEST_CLASSIFY_CATEGORY", "promotions")
			t.Setenv("HEROLD_TEST_CLASSIFY_CALL_LOG", callLog)

			ts := startTestIMAPServer(t)
			user := "classify-" + be.name
			ts.addUser(user, "pw")
			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})

			pctx, pcancel := context.WithCancel(context.Background())
			t.Cleanup(pcancel)
			mgr := startClassifierPluginManager(t, pctx, be.clk, bin)
			cls := spam.New(pluginManagerInvoker{mgr: mgr}, slog.Default(), be.clk)
			adapter := &testSpamAdapter{cls: cls, plugin: "classifier", st: ha.Store}

			base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			// One old message so the first sync pass is a genuine initial
			// backfill (never classified, D1) and the second is a genuine
			// live arrival.
			oldRaw := buildRFC822("classify-old-"+be.name+"@test", "Old", base)
			appendToServer(t, ts, user, "pw", "INBOX", oldRaw, nil, base)

			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               user + "@example.test",
				username:            user,
				credentialPlaintext: "pw",
			}, nil)

			if err := runSyncOnceSpam(t, ha, ts, acc, nil, adapter); err != nil {
				t.Fatalf("first (backfill) sync: %v", err)
			}

			d := base.AddDate(0, 0, 5)
			msgIDHeader := "classify-new-" + be.name + "@test"
			raw := buildRFC822(msgIDHeader, "New", d)
			appendToServer(t, ts, user, "pw", "INBOX", raw, nil, d)

			if err := runSyncOnceSpam(t, ha, ts, acc, nil, adapter); err != nil {
				t.Fatalf("second (live) sync: %v", err)
			}

			ctx := context.Background()
			msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgIDHeader)
			if err != nil {
				t.Fatalf("GetMessageByMessageIDHeader: %v", err)
			}
			inboxMB := mustGetMailboxByName(t, ha.Store, acc.PrincipalID, "INBOX")
			kws := keywordsForMailbox(msg, inboxMB.ID)
			if !hasKeyword(kws, "$category-promotions") {
				t.Fatalf("keywords = %v, want $category-promotions present", kws)
			}

			rec, err := ha.Store.Meta().GetLLMClassification(ctx, msg.ID)
			if err != nil {
				t.Fatalf("GetLLMClassification: %v", err)
			}
			if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
				t.Fatalf("SpamVerdict = %v, want \"ham\"", rec.SpamVerdict)
			}

			if got := countCallLogLines(t, callLog); got != 1 {
				t.Fatalf("classifier invoked %d times for the live arrival, want exactly 1", got)
			}
		})
	}
}

// TestClassifySubprocess_BudgetCutoff_FakeClock is the injected-clock half
// of #304 acceptance item 5: a real classifierfixture subprocess sleeping
// past the classify budget is cut off deterministically once the
// harness's FakeClock is advanced past the timeout, and the import
// proceeds with the message unclassified in INBOX rather than blocking
// forever or failing the sync.
func TestClassifySubprocess_BudgetCutoff_FakeClock(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test: spawns a real plugin subprocess")
	}
	bin := buildClassifierFixtureBin(t)
	// Backend stores/clocks are opened lazily, right before each subtest's
	// classify-timing-sensitive body runs (not shared from a pre-built
	// slice): the SQLite subtest alone takes 10+ real seconds (it waits
	// out the still-"sleeping" plugin's graceful-shutdown RPC timeout in
	// t.Cleanup), and an anchor captured before the loop started would
	// already be stale by the time the Postgres subtest's classify call
	// runs -- see classifySubprocessBackends' doc comment for why a stale
	// anchor makes the real context.WithDeadline it feeds already-expired.
	names := []string{"sqlite"}
	if os.Getenv("HEROLD_PG_DSN") != "" {
		names = append(names, "postgres")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			be := openClassifySubprocessBackend(t, name)
			t.Setenv("HEROLD_TEST_CLASSIFY_SLEEP_MS", "600000") // far past any budget; cut off by the fake clock, not by elapsing
			t.Setenv("HEROLD_TEST_CLASSIFY_VERDICT", "ham")

			ts := startTestIMAPServer(t)
			user := "timeout-" + be.name
			ts.addUser(user, "pw")
			ha, _ := testharness.Start(t, testharness.Options{Store: be.st, Clock: be.clk})

			pctx, pcancel := context.WithCancel(context.Background())
			t.Cleanup(pcancel)
			mgr := startClassifierPluginManager(t, pctx, be.clk, bin)
			const budget = 5 * time.Second
			cls := spam.New(pluginManagerInvoker{mgr: mgr}, slog.Default(), be.clk).WithTimeout(budget)
			adapter := &testSpamAdapter{cls: cls, plugin: "classifier", st: ha.Store}

			base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
			oldRaw := buildRFC822("timeout-old-"+be.name+"@test", "Old", base)
			appendToServer(t, ts, user, "pw", "INBOX", oldRaw, nil, base)

			acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
				email:               user + "@example.test",
				username:            user,
				credentialPlaintext: "pw",
			}, nil)

			if err := runSyncOnceSpam(t, ha, ts, acc, nil, adapter); err != nil {
				t.Fatalf("first (backfill) sync: %v", err)
			}

			d := base.AddDate(0, 0, 5)
			msgIDHeader := "timeout-new-" + be.name + "@test"
			raw := buildRFC822(msgIDHeader, "New", d)
			appendToServer(t, ts, user, "pw", "INBOX", raw, nil, d)

			syncErrCh := make(chan error, 1)
			go func() {
				syncErrCh <- runSyncOnceSpamNoDeadline(t, context.Background(), ha, ts, acc, adapter)
			}()

			// Advance the fake clock past the classify budget once the
			// classifier's deadline timer is armed (NumWaiters reaches 1);
			// this is what cuts the plugin's real (600s) sleep off
			// deterministically rather than by waiting it out.
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) && be.clk.NumWaiters() < 1 {
				time.Sleep(5 * time.Millisecond)
			}
			if be.clk.NumWaiters() < 1 {
				t.Fatalf("classify deadline timer never armed (NumWaiters=%d)", be.clk.NumWaiters())
			}
			be.clk.Advance(budget)

			select {
			case err := <-syncErrCh:
				if err != nil {
					t.Fatalf("second (live) sync: %v", err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("sync did not return after the fake clock advanced past the classify budget")
			}

			ctx := context.Background()
			msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(ctx, acc.PrincipalID, msgIDHeader)
			if err != nil {
				t.Fatalf("GetMessageByMessageIDHeader: %v", err)
			}
			inboxMB := mustGetMailboxByName(t, ha.Store, acc.PrincipalID, "INBOX")
			if !msgIsMemberOf(msg, inboxMB.ID) {
				t.Errorf("timed-out classify message not in INBOX (mailboxes=%v)", msg.Mailboxes)
			}
			// re #326: a classify call that was attempted and cut off by
			// the budget IS now recorded, verdict "unclassified" with a
			// timeout reason -- distinguishable from a genuine ham
			// verdict and from the "no plugin configured" case, which
			// stays unrecorded (TestIMAPImportSpamAdapter_ClassifyNilClassifier
			// et al in internal/admin).
			rec, err := ha.Store.Meta().GetLLMClassification(ctx, msg.ID)
			if err != nil {
				t.Fatalf("GetLLMClassification: %v", err)
			}
			if rec.SpamVerdict == nil || *rec.SpamVerdict != "unclassified" {
				t.Errorf("SpamVerdict = %v, want \"unclassified\"", rec.SpamVerdict)
			}
			if rec.SpamReason == nil || !strings.HasPrefix(*rec.SpamReason, "timeout: ") {
				t.Errorf("SpamReason = %v, want a timeout: prefix", rec.SpamReason)
			}
		})
	}
}
