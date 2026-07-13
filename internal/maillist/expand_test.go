package maillist_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// openSQLiteStore opens a fresh temp-file SQLite store for one test.
func openSQLiteStore(t *testing.T) store.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.OpenWithRand(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil, clk, rand.Reader)
	if err != nil {
		t.Fatalf("storesqlite.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// openPostgresStore opens a Postgres store for the parity leg. Skips
// when HEROLD_PG_DSN is not set (see reference_herold_local_postgres in
// the maintainer's operating notes: a local Postgres on 127.0.0.1:5432
// runs the leg when the env var is exported, so do not treat an unset
// var as "no Postgres available" without probing).
func openPostgresStore(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.OpenWithRand(context.Background(), dsn, t.TempDir(), nil, clk, rand.Reader)
	if err != nil {
		t.Skipf("storepg.OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// The Postgres leg runs against a shared, persistent database
	// (unlike SQLite's fresh temp file per test); wipe row state first
	// so two test runs (or two tests in the same run inserting the
	// same fixture addresses) don't collide on unique constraints.
	if tr, ok := st.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	return st
}

// runOnBothBackends runs fn once against a fresh SQLite store and once
// against Postgres (skipped when HEROLD_PG_DSN is unset).
func runOnBothBackends(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	t.Run("SQLite", func(t *testing.T) { fn(t, openSQLiteStore(t)) })
	t.Run("Postgres", func(t *testing.T) { fn(t, openPostgresStore(t)) })
}

// mustInsertList creates the backing Group principal, an owner
// principal, and the mailing_list row.
func mustInsertList(t *testing.T, st store.Store, address string, arcSeal bool) store.MailingList {
	t.Helper()
	ctx := context.Background()
	owner, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "owner-" + address,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(owner): %v", err)
	}
	group, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: address, DisplayName: "Test List",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	ml, err := st.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID:    group.ID,
		PostingAddress: address,
		DisplayName:    "Test List",
		OwnerID:        owner.ID,
		ARCSeal:        arcSeal,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}
	return ml
}

func mustAddExternalMember(t *testing.T, st store.Store, listID store.MailingListID, addr string, state store.MailingListMemberState) store.MailingListMember {
	t.Helper()
	ctx := context.Background()
	m, err := st.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID:          listID,
		ExternalAddress: &addr,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember(%s): %v", addr, err)
	}
	if state != store.MailingListMemberActive {
		if err := st.Meta().UpdateMailingListMemberState(ctx, m.ID, state); err != nil {
			t.Fatalf("UpdateMailingListMemberState(%s): %v", addr, err)
		}
	}
	return m
}

const testMessage = "From: poster@sender.test\r\n" +
	"To: list@example.test\r\n" +
	"Subject: quarterly update\r\n" +
	"Message-ID: <expand-1@sender.test>\r\n" +
	"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
	"\r\n" +
	"Body text.\r\n"

func mustParse(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	return msg
}

// fakeSubmitter records Submit calls in memory (scalars + body bytes)
// for assertions that don't need a real queue/blob store.
type fakeSubmitter struct {
	mu    sync.Mutex
	calls []queue.Submission
	seq   int
}

func (f *fakeSubmitter) Submit(_ context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	var body []byte
	if msg.Body != nil {
		body, _ = io.ReadAll(msg.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	msg.Body = bytes.NewReader(body)
	f.calls = append(f.calls, msg)
	return queue.EnvelopeID(fmt.Sprintf("env-%d", f.seq)), nil
}

func (f *fakeSubmitter) Calls() []queue.Submission {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]queue.Submission, len(f.calls))
	copy(out, f.calls)
	return out
}

func bodyOf(t *testing.T, sub queue.Submission) string {
	t.Helper()
	b, err := io.ReadAll(sub.Body)
	if err != nil {
		t.Fatalf("read submission body: %v", err)
	}
	return string(b)
}

// TestExpand_OnlyActiveEachMembers exercises REQ-MLIST-10/REQ-MLIST-03:
// suspended, unsubscribed, and pending members get no copy; only
// active/each members do.
func TestExpand_OnlyActiveEachMembers(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		mustAddExternalMember(t, st, ml.ID, "active1@example.net", store.MailingListMemberActive)
		mustAddExternalMember(t, st, ml.ID, "active2@example.net", store.MailingListMemberActive)
		mustAddExternalMember(t, st, ml.ID, "suspended@example.net", store.MailingListMemberSuspended)
		mustAddExternalMember(t, st, ml.ID, "unsubscribed@example.net", store.MailingListMemberUnsubscribed)
		mustAddExternalMember(t, st, ml.ID, "pending@example.net", store.MailingListMemberPending)
		// nomail requires a principal member (REQ-MLIST-04).
		nomailPrincipal, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: "nomail@example.test",
		})
		if err != nil {
			t.Fatalf("InsertPrincipal(nomail): %v", err)
		}
		if _, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
			ListID:       ml.ID,
			PrincipalID:  &nomailPrincipal.ID,
			DeliveryMode: store.MailingListDeliveryNoMail,
		}); err != nil {
			t.Fatalf("AddMailingListMember(nomail): %v", err)
		}

		sub := &fakeSubmitter{}
		exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
		res, err := exp.Expand(context.Background(), maillist.ExpandInput{
			List:   ml,
			Parsed: mustParse(t, testMessage),
			Raw:    []byte(testMessage),
		})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}
		if res.Dropped {
			t.Fatalf("Expand dropped: %v", res.DropReason)
		}
		if res.MemberCount != 2 {
			t.Fatalf("MemberCount = %d, want 2", res.MemberCount)
		}
		calls := sub.Calls()
		if len(calls) != 2 {
			t.Fatalf("Submit calls = %d, want 2", len(calls))
		}
		got := map[string]bool{}
		for _, c := range calls {
			if len(c.Recipients) != 1 {
				t.Fatalf("Submission.Recipients = %v, want exactly one address per call", c.Recipients)
			}
			got[c.Recipients[0]] = true
		}
		if !got["active1@example.net"] || !got["active2@example.net"] {
			t.Fatalf("expected fan-out to active1/active2 only, got %v", got)
		}
	})
}

// TestExpand_LoopGuard exercises REQ-MLIST-30: a post whose List-ID
// already names this list is dropped, audited, and not fanned out.
func TestExpand_LoopGuard(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "active@example.net", store.MailingListMemberActive)

	raw := "From: poster@sender.test\r\n" +
		"To: list@example.test\r\n" +
		"Subject: hi\r\n" +
		"List-ID: \"Test List\" <list.example.test>\r\n" +
		"\r\n" +
		"Body.\r\n"
	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, raw),
		Raw:    []byte(raw),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Dropped || res.DropReason != maillist.DropReasonLoop {
		t.Fatalf("result = %+v, want Dropped with DropReasonLoop", res)
	}
	if len(sub.Calls()) != 0 {
		t.Fatalf("Submit called %d times, want 0 (loop must not fan out)", len(sub.Calls()))
	}

	logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	found := false
	for _, l := range logs {
		if l.Action == "maillist.post.dropped" && strings.Contains(l.Message, "loop") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit record for the dropped loop post; entries: %+v", logs)
	}
}

// TestExpand_AutoSubmittedGuard exercises REQ-MLIST-31.
func TestExpand_AutoSubmittedGuard(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "active@example.net", store.MailingListMemberActive)

	raw := "From: bounce@sender.test\r\n" +
		"To: list@example.test\r\n" +
		"Subject: delivery failure\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"\r\n" +
		"Body.\r\n"
	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, raw),
		Raw:    []byte(raw),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Dropped || res.DropReason != maillist.DropReasonAutoSubmitted {
		t.Fatalf("result = %+v, want Dropped with DropReasonAutoSubmitted", res)
	}
	if len(sub.Calls()) != 0 {
		t.Fatalf("Submit called %d times, want 0", len(sub.Calls()))
	}
}

// TestExpand_OversizeGuard exercises REQ-MLIST-32: the list's own
// MaxMessageSizeBytes overrides the (larger) deployment default and
// rejects the post before any fan-out.
func TestExpand_OversizeGuard(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	ml.MaxMessageSizeBytes = 10 // smaller than testMessage
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	mustAddExternalMember(t, st, ml.ID, "active@example.net", store.MailingListMemberActive)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:                     ml,
		Parsed:                   mustParse(t, testMessage),
		Raw:                      []byte(testMessage),
		DeploymentMaxMessageSize: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Dropped || res.DropReason != maillist.DropReasonOversize {
		t.Fatalf("result = %+v, want Dropped with DropReasonOversize", res)
	}
	if len(sub.Calls()) != 0 {
		t.Fatalf("Submit called %d times, want 0", len(sub.Calls()))
	}
}

// TestExpand_AutoSubmittedOnCopies verifies every fanned-out copy
// carries Auto-Submitted: auto-forwarded (REQ-MLIST-24), regardless of
// the number of members.
func TestExpand_AutoSubmittedOnCopies(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)
	mustAddExternalMember(t, st, ml.ID, "b@example.net", store.MailingListMemberActive)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	if _, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	}); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	calls := sub.Calls()
	if len(calls) != 2 {
		t.Fatalf("Submit calls = %d, want 2", len(calls))
	}
	for _, c := range calls {
		if !strings.Contains(bodyOf(t, c), "Auto-Submitted: auto-forwarded\r\n") {
			t.Errorf("copy to %v missing Auto-Submitted: auto-forwarded", c.Recipients)
		}
	}
}

// TestExpand_ARCSeal_PreservesFrom exercises REQ-MLIST-21: a list with
// ARCSeal=true and a wired Sealer produces a copy carrying an ARC set,
// with the original From: header intact.
func TestExpand_ARCSeal_PreservesFrom(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", true)
	mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)

	clk := clock.NewFake(time.Now())
	km := keymgmt.NewManager(st.Meta(), discardLogger(), clk, rand.Reader)
	if _, err := km.GenerateKey(context.Background(), ml.Domain, store.DKIMAlgorithmEd25519SHA256); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sealer := mailarc.NewSealer(km, nil, discardLogger())

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, sealer, clk, discardLogger())
	prior := mailauth.AuthResults{ARC: mailauth.ARCResult{Status: mailauth.AuthNone}}
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
		Auth:   prior,
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.MemberCount != 1 {
		t.Fatalf("result = %+v, want one delivered copy", res)
	}
	if res.Unsealed {
		t.Fatalf("result.Unsealed = true on the happy sealed path; want false")
	}
	calls := sub.Calls()
	if len(calls) != 1 {
		t.Fatalf("Submit calls = %d, want 1", len(calls))
	}
	body := bodyOf(t, calls[0])
	if !strings.Contains(body, "ARC-Seal:") || !strings.Contains(body, "ARC-Message-Signature:") || !strings.Contains(body, "ARC-Authentication-Results:") {
		t.Fatalf("ARC set not present in sealed copy:\n%s", body)
	}
	if !strings.Contains(body, "From: poster@sender.test\r\n") {
		t.Fatalf("original From: header not preserved in sealed copy:\n%s", body)
	}
}

// captureLogger returns a logger recording every emitted entry (as
// logfmt text, level included) into buf, so a test can assert on the log
// level and message content without depending on stdlib slog's exact
// wire format beyond "level=X" and the message substring.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// TestExpand_ARCSeal_NoActiveKey_FansOutUnsealed_WithDistinctOutcome is the
// issue #183 regression test: a list with ARCSeal=true whose domain has NO
// active DKIM key (the normal state for a freshly onboarded domain, per
// the live-verifier repro in the ticket) must still deliver the post --
// dropping mail is the worse failure -- but the degradation must be
// impossible to miss:
//   - the fanout_total metric records a DISTINCT "unsealed" outcome, not
//     the same "delivered" bucket a normal sealed copy gets;
//   - the log line is ERROR, not WARN;
//   - a distinct OutcomeFailure audit row is written;
//   - the delivered copy itself carries no ARC-Seal/ARC-Message-Signature
//     headers (it really did go out unsealed, not silently re-sealed by
//     some other path).
//
// Exercises the real mailarc.Sealer against a keymgmt.Manager with no
// generated key, reproducing "mailarc: no active DKIM key for <domain>"
// verbatim rather than a stand-in fake error.
func TestExpand_ARCSeal_NoActiveKey_FansOutUnsealed_WithDistinctOutcome(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", true)
		mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)

		clk := clock.NewFake(time.Now())
		km := keymgmt.NewManager(st.Meta(), discardLogger(), clk, rand.Reader)
		// Deliberately no km.GenerateKey call: ml.Domain has no active
		// DKIM key, the "fresh domain" state the ticket calls out.
		sealer := mailarc.NewSealer(km, nil, discardLogger())

		observe.RegisterMailingListMetrics()
		listLabel := ml.PostingAddress
		beforeUnsealed := testutil.ToFloat64(observe.MailingListFanoutTotal.WithLabelValues(listLabel, "unsealed"))
		beforeDelivered := testutil.ToFloat64(observe.MailingListFanoutTotal.WithLabelValues(listLabel, "delivered"))

		logger, logbuf := captureLogger()
		sub := &fakeSubmitter{}
		exp := maillist.NewExpander(st.Meta(), sub, sealer, clk, logger)
		res, err := exp.Expand(context.Background(), maillist.ExpandInput{
			List:   ml,
			Parsed: mustParse(t, testMessage),
			Raw:    []byte(testMessage),
			Auth:   mailauth.AuthResults{ARC: mailauth.ARCResult{Status: mailauth.AuthNone}},
		})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}

		// The post is still delivered -- dropping mail is the worse
		// failure -- but flagged as unsealed.
		if res.Dropped {
			t.Fatalf("result.Dropped = true; a sealing failure must not drop the post: %+v", res)
		}
		if res.MemberCount != 1 {
			t.Fatalf("MemberCount = %d, want 1 (still delivered despite the seal failure)", res.MemberCount)
		}
		if !res.Unsealed {
			t.Fatalf("result.Unsealed = false; want true (sealing failed)")
		}
		calls := sub.Calls()
		if len(calls) != 1 {
			t.Fatalf("Submit calls = %d, want 1", len(calls))
		}
		body := bodyOf(t, calls[0])
		if strings.Contains(body, "ARC-Seal:") || strings.Contains(body, "ARC-Message-Signature:") {
			t.Fatalf("delivered copy carries an ARC set despite the seal failure:\n%s", body)
		}

		// Distinct metric outcome: "unsealed" incremented, "delivered" NOT.
		afterUnsealed := testutil.ToFloat64(observe.MailingListFanoutTotal.WithLabelValues(listLabel, "unsealed"))
		afterDelivered := testutil.ToFloat64(observe.MailingListFanoutTotal.WithLabelValues(listLabel, "delivered"))
		if afterUnsealed != beforeUnsealed+1 {
			t.Errorf("fanout_total{outcome=unsealed} = %v, want %v", afterUnsealed, beforeUnsealed+1)
		}
		if afterDelivered != beforeDelivered {
			t.Errorf("fanout_total{outcome=delivered} = %v, want unchanged at %v (must not double-count as a normal delivery)", afterDelivered, beforeDelivered)
		}

		// ERROR log, not WARN, and it names the actual failure.
		logged := logbuf.String()
		if !strings.Contains(logged, "level=ERROR") {
			t.Errorf("no ERROR-level log line for the seal failure; got:\n%s", logged)
		}
		if strings.Contains(logged, "level=WARN") && strings.Contains(logged, "arc seal failed") {
			t.Errorf("seal failure logged at WARN, want ERROR only; got:\n%s", logged)
		}
		if !strings.Contains(logged, "no active DKIM key") {
			t.Errorf("log line missing the underlying mailarc error; got:\n%s", logged)
		}

		// Distinct OutcomeFailure audit row, separate from the ordinary
		// "delivered" success path (which writes no per-post audit row at
		// all in S1 -- only guard drops and this degradation do).
		logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
			Action: "maillist.fanout.unsealed",
		})
		if err != nil {
			t.Fatalf("ListAuditLog: %v", err)
		}
		found := false
		for _, row := range logs {
			if row.Subject == fmt.Sprintf("mailing_list:%d", ml.ID) {
				found = true
				if row.Outcome != store.OutcomeFailure {
					t.Errorf("audit row Outcome = %v, want OutcomeFailure", row.Outcome)
				}
				if !strings.Contains(row.Message, "no active DKIM key") {
					t.Errorf("audit row Message = %q, want it to name the seal failure", row.Message)
				}
			}
		}
		if !found {
			t.Fatalf("no maillist.fanout.unsealed audit row for mailing_list:%d; rows=%+v", ml.ID, logs)
		}
	})
}

// TestExpand_ARCSeal_NoSealerWired_FansOutUnsealed covers the other
// unsealed-degradation path: ARCSeal=true but no Sealer at all is wired
// into the Expander (a deployment/wiring gap rather than a missing key).
// Must produce the same distinct signals as the missing-key case.
func TestExpand_ARCSeal_NoSealerWired_FansOutUnsealed(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", true)
	mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)

	logger, logbuf := captureLogger()
	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), logger)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.MemberCount != 1 || !res.Unsealed {
		t.Fatalf("result = %+v, want delivered+unsealed", res)
	}
	if !strings.Contains(logbuf.String(), "level=ERROR") {
		t.Errorf("no ERROR-level log line for the missing Sealer; got:\n%s", logbuf.String())
	}
	logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.fanout.unsealed"})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	found := false
	for _, row := range logs {
		if row.Subject == fmt.Sprintf("mailing_list:%d", ml.ID) && row.Outcome == store.OutcomeFailure {
			found = true
		}
	}
	if !found {
		t.Fatalf("no maillist.fanout.unsealed audit row for mailing_list:%d", ml.ID)
	}
}

// TestExpand_BlobSharedAcrossCopies exercises REQ-MLIST-11: every
// fan-out copy is submitted through the real outbound queue, and its
// content-addressed blob store dedups the identical shaped bytes down
// to one stored blob shared by every resulting queue row -- the blob is
// never rewritten per member.
func TestExpand_BlobSharedAcrossCopies(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	for i := 0; i < 5; i++ {
		mustAddExternalMember(t, st, ml.ID, fmt.Sprintf("m%d@example.net", i), store.MailingListMemberActive)
	}

	clk := clock.NewFake(time.Now())
	q := queue.New(queue.Options{
		Store:  st,
		Logger: discardLogger(),
		Clock:  clk,
		// Deliverer is never invoked: Submit only persists rows; no
		// Run() call happens in this test.
	})
	exp := maillist.NewExpander(st.Meta(), q, nil, clk, discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.MemberCount != 5 {
		t.Fatalf("MemberCount = %d, want 5", res.MemberCount)
	}

	items, err := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("queue items = %d, want 5", len(items))
	}
	firstHash := items[0].BodyBlobHash
	if firstHash == "" {
		t.Fatalf("BodyBlobHash empty")
	}
	for _, it := range items {
		if it.BodyBlobHash != firstHash {
			t.Errorf("queue item %d BodyBlobHash = %q, want %q (all copies must share one blob)", it.ID, it.BodyBlobHash, firstHash)
		}
	}
}

// TestExpand_LargeRoster_StreamsIncrementally exercises REQ-MLIST-12: a
// roster larger than the store's internal 500-row page size is fanned
// out via exactly one Submit call per member, in roster order, without
// the caller ever holding more than the roster's addresses (never full
// per-member copies) at once. store.StreamActiveEachMembers is already
// proven to page correctly in internal/store/storetest; this test
// proves Expand rides that same incremental path at a scale spanning
// multiple pages rather than loading the roster whole.
func TestExpand_LargeRoster_StreamsIncrementally(t *testing.T) {
	if testing.Short() {
		t.Skip("large-roster fan-out; skipped in -short")
	}
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	const n = 1200 // > 2x the store's 500-row internal page size
	for i := 0; i < n; i++ {
		mustAddExternalMember(t, st, ml.ID, fmt.Sprintf("m%04d@example.net", i), store.MailingListMemberActive)
	}

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.MemberCount != n {
		t.Fatalf("MemberCount = %d, want %d", res.MemberCount, n)
	}
	calls := sub.Calls()
	if len(calls) != n {
		t.Fatalf("Submit calls = %d, want %d", len(calls), n)
	}
	for i, c := range calls {
		if len(c.Recipients) != 1 {
			t.Fatalf("call %d Recipients = %v, want exactly one address (never batched, keeping the per-member VERP seam clean)", i, c.Recipients)
		}
		want := fmt.Sprintf("m%04d@example.net", i)
		if c.Recipients[0] != want {
			t.Fatalf("call %d recipient = %q, want %q (roster order)", i, c.Recipients[0], want)
		}
	}
}

// TestExpand_EmptyRoster verifies a list with no active/each members
// fans out to nobody without error (not a guard drop -- Dropped stays
// false, MemberCount is simply zero).
func TestExpand_EmptyRoster(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)

	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped {
		t.Fatalf("empty roster must not be reported as a guard drop: %+v", res)
	}
	if res.MemberCount != 0 || len(sub.Calls()) != 0 {
		t.Fatalf("empty roster should fan out to nobody without error: %+v", res)
	}
}

// TestExpand_VERPMailFrom exercises REQ-MLIST-50: with a TokenSigner
// wired, every fan-out copy's envelope MAIL FROM is a per-member VERP
// token address, and each token verifies back to exactly the member it
// was sent to (never to any other member on the roster).
func TestExpand_VERPMailFrom(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		ml := mustInsertList(t, st, "list@example.test", false)
		m1 := mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)
		m2 := mustAddExternalMember(t, st, ml.ID, "b@example.net", store.MailingListMemberActive)

		ts := maillist.NewTokenSigner([]byte("0123456789abcdef0123456789abcdef"))
		sub := &fakeSubmitter{}
		exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), discardLogger())
		exp.TokenSigner = ts
		res, err := exp.Expand(context.Background(), maillist.ExpandInput{
			List:   ml,
			Parsed: mustParse(t, testMessage),
			Raw:    []byte(testMessage),
		})
		if err != nil {
			t.Fatalf("Expand: %v", err)
		}
		if res.MemberCount != 2 {
			t.Fatalf("MemberCount = %d, want 2", res.MemberCount)
		}

		byRecipient := map[string]store.MailingListMemberID{
			"a@example.net": m1.ID,
			"b@example.net": m2.ID,
		}
		calls := sub.Calls()
		if len(calls) != 2 {
			t.Fatalf("Submit calls = %d, want 2", len(calls))
		}
		seen := map[store.MailingListMemberID]bool{}
		for _, c := range calls {
			if len(c.Recipients) != 1 {
				t.Fatalf("Recipients = %v, want exactly one", c.Recipients)
			}
			wantMember, ok := byRecipient[c.Recipients[0]]
			if !ok {
				t.Fatalf("unexpected recipient %q", c.Recipients[0])
			}
			if !strings.HasPrefix(c.MailFrom, "list+bounce-") || !strings.HasSuffix(c.MailFrom, "@example.test") {
				t.Fatalf("MailFrom for %s = %q, want the REQ-MLIST-50 VERP shape", c.Recipients[0], c.MailFrom)
			}
			local := strings.TrimSuffix(c.MailFrom, "@example.test")
			_, token, ok := maillist.ParseVERPBounceLocalPart(local)
			if !ok {
				t.Fatalf("MailFrom %q for %s does not parse as a VERP bounce local-part", c.MailFrom, c.Recipients[0])
			}
			gotMember, verr := ts.Verify(maillist.TokenPurposeVERP, ml.ID, token, time.Now())
			if verr != nil {
				t.Fatalf("Verify token for %s: %v", c.Recipients[0], verr)
			}
			if gotMember != wantMember {
				t.Fatalf("MailFrom for %s verified to member %d, want %d", c.Recipients[0], gotMember, wantMember)
			}
			seen[gotMember] = true
		}
		if len(seen) != 2 {
			t.Fatalf("expected two distinct member attributions, got %v", seen)
		}
	})
}

// TestExpand_NoTokenSigner_FallsBackToS1BounceAddress verifies that
// without a TokenSigner wired, Expand still fans out (never drops the
// post) using the S1 list-wide bounce address, and logs the
// degradation loudly rather than silently.
func TestExpand_NoTokenSigner_FallsBackToS1BounceAddress(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "list@example.test", false)
	mustAddExternalMember(t, st, ml.ID, "a@example.net", store.MailingListMemberActive)

	logger, logbuf := captureLogger()
	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, clock.NewFake(time.Now()), logger)
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List:   ml,
		Parsed: mustParse(t, testMessage),
		Raw:    []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.Dropped || res.MemberCount != 1 {
		t.Fatalf("result = %+v, want one delivered copy", res)
	}
	calls := sub.Calls()
	if len(calls) != 1 || calls[0].MailFrom != "list+bounce@example.test" {
		t.Fatalf("MailFrom = %+v, want the S1 list-wide bounce address", calls)
	}
	if !strings.Contains(logbuf.String(), "level=ERROR") || !strings.Contains(logbuf.String(), "no TokenSigner wired") {
		t.Errorf("no loud ERROR log for the missing TokenSigner; got:\n%s", logbuf.String())
	}
}
