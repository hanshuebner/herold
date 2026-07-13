package maillist_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/dsn"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// Bounce scoring / auto-suspend / reactivation, S2 part 2 (issue #184,
// REQ-MLIST-53/54/55). These tests build on
// TestBounceProcessor_AttributesAndClassifies et al in bounce_test.go,
// which already exercise attribution/classification; the tests here
// exercise the scoring policy, the auto-suspend transition and its
// monitorability, and reactivation, driven through the same
// BounceProcessor.ProcessBounce entry point.

// hardBounceDSN is bounceTestDSN (bounce_test.go) parameterised by
// recipient, so a suite that inserts more than one member can bounce
// each independently.
func hardBounceDSN(recipient string) string {
	return "From: MAILER-DAEMON@mx.example.net\r\n" +
		"To: list+bounce-XXXX@example.test\r\n" +
		"Subject: Undelivered Mail Returned to Sender\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"B\"\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"Delivery failed.\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: message/delivery-status\r\n" +
		"\r\n" +
		"Reporting-MTA: dns; mx.example.net\r\n" +
		"\r\n" +
		"Final-Recipient: rfc822; " + recipient + "\r\n" +
		"Action: failed\r\n" +
		"Status: 5.1.1\r\n" +
		"Diagnostic-Code: smtp; 550 5.1.1 unknown user\r\n" +
		"\r\n" +
		"--B--\r\n"
}

// softBounceDSN is a transient-failure (RFC 3464 Action: delayed, 4.x.x)
// DSN, classifying dsn.ClassificationSoft.
func softBounceDSN(recipient string) string {
	return "From: MAILER-DAEMON@mx.example.net\r\n" +
		"To: list+bounce-XXXX@example.test\r\n" +
		"Subject: Delayed Mail\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"B\"\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=us-ascii\r\n" +
		"\r\n" +
		"Delivery delayed.\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: message/delivery-status\r\n" +
		"\r\n" +
		"Reporting-MTA: dns; mx.example.net\r\n" +
		"\r\n" +
		"Final-Recipient: rfc822; " + recipient + "\r\n" +
		"Action: delayed\r\n" +
		"Status: 4.2.2\r\n" +
		"Diagnostic-Code: smtp; 450 4.2.2 mailbox full\r\n" +
		"\r\n" +
		"--B--\r\n"
}

// bounceViaToken signs a VERP token for (ml, memberID) and drives it
// through ProcessBounce, mirroring what protosmtp's inbound RCPT/DATA
// path does for a real VERP bounce address.
func bounceViaToken(t *testing.T, bp *maillist.BounceProcessor, ts *maillist.TokenSigner, ml store.MailingList, memberID store.MailingListMemberID, raw string) maillist.BounceResult {
	t.Helper()
	token, err := ts.Sign(maillist.TokenPurposeVERP, ml.ID, memberID, time.Time{})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
		List:  ml,
		Token: token,
		Raw:   []byte(raw),
	})
	if err != nil {
		t.Fatalf("ProcessBounce: %v", err)
	}
	return res
}

// TestBounceScoring_HardBounceSuspendsExactlyThatMember is the
// REQ-MLIST-53/54 e2e: a single hard bounce crosses the default
// threshold and suspends exactly the addressed member -- not a sibling
// member on the same list, not a same-address member on a different
// list.
func TestBounceScoring_HardBounceSuspendsExactlyThatMember(t *testing.T) {
	runOnBothBackends(t, func(t *testing.T, st store.Store) {
		observe.RegisterMailingListMetrics()
		ml := mustInsertList(t, st, "hardsuspend@example.test", false)
		other := mustInsertList(t, st, "hardsuspend-other@example.test", false)
		target := mustAddExternalMember(t, st, ml.ID, "target@example.net", store.MailingListMemberActive)
		sibling := mustAddExternalMember(t, st, ml.ID, "sibling@example.net", store.MailingListMemberActive)
		otherListMember := mustAddExternalMember(t, st, other.ID, "target@example.net", store.MailingListMemberActive)

		ts := maillist.NewTokenSigner(testDataKey())
		bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())

		// The suspended-total counter is a process-global Prometheus
		// registry keyed by posting address; runOnBothBackends re-runs
		// this closure (SQLite, then Postgres) against the SAME address,
		// so assert the DELTA this one bounce contributes rather than an
		// absolute value (mirrors expand_test.go's before/after pattern
		// for the same reason).
		suspendedBefore := testutil.ToFloat64(observe.MailingListSuspendedTotal.WithLabelValues(ml.PostingAddress, "hard"))

		res := bounceViaToken(t, bp, ts, ml, target.ID, hardBounceDSN("target@example.net"))
		if res.Classification != dsn.ClassificationHard {
			t.Fatalf("Classification = %v, want Hard", res.Classification)
		}

		got, err := st.Meta().GetMailingListMember(context.Background(), target.ID)
		if err != nil {
			t.Fatalf("GetMailingListMember(target): %v", err)
		}
		if got.State != store.MailingListMemberSuspended {
			t.Fatalf("target State = %q, want suspended", got.State)
		}
		if got.BounceScore < 1.0 {
			t.Errorf("target BounceScore = %v, want >= 1.0 (default threshold)", got.BounceScore)
		}
		if got.LastBounceAt == nil {
			t.Errorf("target LastBounceAt not set")
		}

		siblingGot, err := st.Meta().GetMailingListMember(context.Background(), sibling.ID)
		if err != nil {
			t.Fatalf("GetMailingListMember(sibling): %v", err)
		}
		if siblingGot.State != store.MailingListMemberActive {
			t.Errorf("sibling State = %q, want unaffected active", siblingGot.State)
		}
		if siblingGot.BounceScore != 0 {
			t.Errorf("sibling BounceScore = %v, want 0 (untouched)", siblingGot.BounceScore)
		}

		otherGot, err := st.Meta().GetMailingListMember(context.Background(), otherListMember.ID)
		if err != nil {
			t.Fatalf("GetMailingListMember(other list): %v", err)
		}
		if otherGot.State != store.MailingListMemberActive {
			t.Errorf("same-address member on a DIFFERENT list State = %q, want unaffected active", otherGot.State)
		}

		// Metrics: herold_mlist_suspended_total{list,reason=hard} and the
		// per-list bounce-rate gauge (REQ-MLIST-54 monitorability).
		if n := testutil.ToFloat64(observe.MailingListSuspendedTotal.WithLabelValues(ml.PostingAddress, "hard")); n != suspendedBefore+1 {
			t.Errorf("herold_mlist_suspended_total{list=%s,reason=hard} = %v, want %v (+1)", ml.PostingAddress, n, suspendedBefore+1)
		}
		if n := testutil.ToFloat64(observe.MailingListSuspendedTotal.WithLabelValues(other.PostingAddress, "hard")); n != 0 {
			t.Errorf("herold_mlist_suspended_total for the OTHER list = %v, want 0 (no leak)", n)
		}
		rate := testutil.ToFloat64(observe.MailingListBounceRate.WithLabelValues(ml.PostingAddress))
		if rate != 0.5 {
			t.Errorf("herold_mlist_bounce_rate{list=%s} = %v, want 0.5 (1 suspended of 2)", ml.PostingAddress, rate)
		}

		// Audit record.
		logs, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "maillist.member.autosuspend"})
		if err != nil {
			t.Fatalf("ListAuditLog: %v", err)
		}
		found := false
		for _, l := range logs {
			if l.Subject == "mailing_list:"+itoaListID(ml.ID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no maillist.member.autosuspend audit row for list %d; rows=%+v", ml.ID, logs)
		}

		// Owner notification: a queue row addressed to the list's
		// owner_principal.
		owner, err := st.Meta().GetPrincipalByID(context.Background(), ml.OwnerID)
		if err != nil {
			t.Fatalf("GetPrincipalByID(owner): %v", err)
		}
		items, err := st.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
		if err != nil {
			t.Fatalf("ListQueueItems: %v", err)
		}
		notified := false
		for _, it := range items {
			if it.RcptTo == owner.CanonicalEmail {
				notified = true
			}
		}
		if !notified {
			t.Fatalf("no queue row addressed to owner %s after auto-suspend; items=%+v", owner.CanonicalEmail, items)
		}
	})
}

// TestBounceScoring_UnverifiableToken_NoScoreChange extends
// TestBounceProcessor_UnverifiableToken_NoAttribution (bounce_test.go)
// with the REQ-MLIST-52 fail-closed guarantee this ticket adds: a forged
// token must not alter ANY member's bounce_score, not just avoid a wrong
// MemberID.
func TestBounceScoring_UnverifiableToken_NoScoreChange(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "forged@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())
	res, err := bp.ProcessBounce(context.Background(), maillist.BounceInput{
		List:  ml,
		Token: "forged-not-a-real-token",
		Raw:   []byte(hardBounceDSN("member@example.net")),
	})
	if err != nil {
		t.Fatalf("ProcessBounce: %v", err)
	}
	if res.Attributed {
		t.Fatalf("Attributed = true for a forged token, want false")
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.BounceScore != 0 || got.State != store.MailingListMemberActive {
		t.Fatalf("member mutated by a forged token: %+v", got)
	}
}

// TestBounceScoring_UnknownClassification_DoesNotScoreOrSuspend
// exercises the conservatism REQ-MLIST-53 inherits from the DSN parser:
// an attributed but ClassificationUnknown bounce (a malformed body)
// leaves bounce_score/state untouched.
func TestBounceScoring_UnknownClassification_DoesNotScoreOrSuspend(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "unknown@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, clock.NewFake(time.Now()), discardLogger())
	res := bounceViaToken(t, bp, ts, ml, member.ID, "")
	if res.Classification != dsn.ClassificationUnknown {
		t.Fatalf("Classification = %v, want Unknown", res.Classification)
	}

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.BounceScore != 0 {
		t.Errorf("BounceScore = %v, want 0 (Unknown must not score)", got.BounceScore)
	}
	if got.State != store.MailingListMemberActive {
		t.Errorf("State = %q, want active (Unknown must not suspend)", got.State)
	}
}

// TestBounceScoring_SoftBurstCrossesThresholdOnlyAfterEnough exercises
// the "burst of soft bounces within the window does not suspend until
// the threshold is crossed" half of REQ-MLIST-53's default policy: at
// DefaultSoftBounceWeight=0.25 and DefaultSuspendThreshold=1.0, the
// first three soft bounces (score 0.25/0.5/0.75) leave the member
// active; the fourth (score 1.0) suspends it.
func TestBounceScoring_SoftBurstCrossesThresholdOnlyAfterEnough(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "softburst@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	fake := clock.NewFake(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, fake, discardLogger())

	for i := 0; i < 3; i++ {
		res := bounceViaToken(t, bp, ts, ml, member.ID, softBounceDSN("member@example.net"))
		if res.Classification != dsn.ClassificationSoft {
			t.Fatalf("bounce %d Classification = %v, want Soft", i, res.Classification)
		}
		fake.Advance(time.Hour)
		got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
		if err != nil {
			t.Fatalf("GetMailingListMember: %v", err)
		}
		if got.State != store.MailingListMemberActive {
			t.Fatalf("after soft bounce %d: State = %q, want still active (score should be %v < threshold)", i+1, got.State, got.BounceScore)
		}
	}

	// Fourth soft bounce crosses the default threshold (1.0).
	bounceViaToken(t, bp, ts, ml, member.ID, softBounceDSN("member@example.net"))
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberSuspended {
		t.Fatalf("after 4th soft bounce: State = %q (score %v), want suspended", got.State, got.BounceScore)
	}
}

// TestBounceScoring_DecayWindow_OldSoftBouncesAgeOut is the explicit
// decay-window regression REQ-MLIST-53 calls for: two soft bounces
// separated by MORE than the configured decay window must not
// accumulate -- the second bounce's score is exactly its own weight, not
// the sum -- so the member stays active indefinitely under sporadic,
// isolated soft bounces. Uses a fake clock advanced deterministically;
// no wall-clock sleep.
func TestBounceScoring_DecayWindow_OldSoftBouncesAgeOut(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "decay@example.test", false)
	// A short, explicit decay window (1 hour) configured on the list, so
	// the test does not depend on the (much larger) production default.
	ml.BouncePolicyJSON = `{"decay_window_seconds":3600}`
	if err := st.Meta().UpdateMailingList(context.Background(), ml); err != nil {
		t.Fatalf("UpdateMailingList: %v", err)
	}
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	fake := clock.NewFake(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, fake, discardLogger())

	bounceViaToken(t, bp, ts, ml, member.ID, softBounceDSN("member@example.net"))
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.BounceScore != 0.25 {
		t.Fatalf("score after 1st soft bounce = %v, want 0.25", got.BounceScore)
	}

	// Advance well past the 1-hour decay window (three months, "a soft
	// bounce from months ago must not accumulate forever").
	fake.Advance(90 * 24 * time.Hour)
	bounceViaToken(t, bp, ts, ml, member.ID, softBounceDSN("member@example.net"))
	got, err = st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.BounceScore != 0.25 {
		t.Fatalf("score after decay-window gap = %v, want 0.25 (aged out, not 0.5)", got.BounceScore)
	}
	if got.State != store.MailingListMemberActive {
		t.Fatalf("State after aged-out soft bounces = %q, want still active", got.State)
	}
}

func itoaListID(id store.MailingListID) string {
	return strconv.FormatUint(uint64(id), 10)
}

// TestBounceScoring_SuspensionSurvivesRestart is the REQ-MLIST-54 "store
// transition, not an in-memory flag" regression: suspend a member,
// close the SQLite store (simulating a process restart), reopen the
// same on-disk file, and confirm the member is still suspended.
func TestBounceScoring_SuspensionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restart.db")
	fake := clock.NewFake(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))

	st, err := storesqlite.OpenWithRand(context.Background(), path, nil, fake, nil)
	if err != nil {
		t.Fatalf("OpenWithRand: %v", err)
	}
	ml := mustInsertList(t, st, "restart@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, fake, discardLogger())
	bounceViaToken(t, bp, ts, ml, member.ID, hardBounceDSN("member@example.net"))

	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember before restart: %v", err)
	}
	if got.State != store.MailingListMemberSuspended {
		t.Fatalf("State before restart = %q, want suspended", got.State)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := storesqlite.OpenWithRand(context.Background(), path, nil, fake, nil)
	if err != nil {
		t.Fatalf("re-OpenWithRand: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after, err := reopened.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember after restart: %v", err)
	}
	if after.State != store.MailingListMemberSuspended {
		t.Fatalf("State after restart = %q, want still suspended (a store transition, not an in-memory flag)", after.State)
	}
}

// TestBounceScoring_ReactivationResetsScoreAndRestoresFanout is the
// REQ-MLIST-55 e2e: a hard bounce suspends a member, who then genuinely
// receives no fan-out copy (asserted against the actual Expander path,
// not just the roster filter in isolation); an operator reactivation
// resets bounce_score and the SAME Expander.Expand call fans out to the
// member again on the very next post.
func TestBounceScoring_ReactivationResetsScoreAndRestoresFanout(t *testing.T) {
	st := openSQLiteStore(t)
	ml := mustInsertList(t, st, "reactivate-fanout@example.test", false)
	member := mustAddExternalMember(t, st, ml.ID, "member@example.net", store.MailingListMemberActive)
	_ = mustAddExternalMember(t, st, ml.ID, "sibling@example.net", store.MailingListMemberActive)

	ts := maillist.NewTokenSigner(testDataKey())
	fake := clock.NewFake(time.Now())
	bp := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), ts, fake, discardLogger())
	sub := &fakeSubmitter{}
	exp := maillist.NewExpander(st.Meta(), sub, nil, fake, discardLogger())
	exp.TokenSigner = ts

	// Baseline: both members get a copy.
	_, err := exp.Expand(context.Background(), maillist.ExpandInput{
		List: ml, Parsed: mustParse(t, testMessage), Raw: []byte(testMessage),
	})
	if err != nil {
		t.Fatalf("Expand (baseline): %v", err)
	}
	if got := recipientsOf(sub.Calls()); len(got) != 2 || !got["member@example.net"] || !got["sibling@example.net"] {
		t.Fatalf("baseline fan-out recipients = %v, want both members", got)
	}

	// Hard-bounce the member: suspends it.
	bounceViaToken(t, bp, ts, ml, member.ID, hardBounceDSN("member@example.net"))
	got, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember: %v", err)
	}
	if got.State != store.MailingListMemberSuspended {
		t.Fatalf("State after hard bounce = %q, want suspended", got.State)
	}

	// Next post: the suspended member gets NO copy; the sibling still does.
	sub2 := &fakeSubmitter{}
	exp2 := maillist.NewExpander(st.Meta(), sub2, nil, fake, discardLogger())
	exp2.TokenSigner = ts
	if _, err := exp2.Expand(context.Background(), maillist.ExpandInput{
		List: ml, Parsed: mustParse(t, testMessage), Raw: []byte(testMessage),
	}); err != nil {
		t.Fatalf("Expand (post-suspend): %v", err)
	}
	if got := recipientsOf(sub2.Calls()); got["member@example.net"] {
		t.Fatalf("suspended member received a fan-out copy: recipients=%v", got)
	} else if !got["sibling@example.net"] {
		t.Fatalf("sibling did not receive its fan-out copy: recipients=%v", got)
	}

	// Operator reactivation: resets bounce_score and restores delivery.
	if err := st.Meta().ReactivateMailingListMember(context.Background(), member.ID); err != nil {
		t.Fatalf("ReactivateMailingListMember: %v", err)
	}
	reactivated, err := st.Meta().GetMailingListMember(context.Background(), member.ID)
	if err != nil {
		t.Fatalf("GetMailingListMember after reactivate: %v", err)
	}
	if reactivated.State != store.MailingListMemberActive || reactivated.BounceScore != 0 {
		t.Fatalf("member after reactivate = %+v, want active with BounceScore 0", reactivated)
	}

	sub3 := &fakeSubmitter{}
	exp3 := maillist.NewExpander(st.Meta(), sub3, nil, fake, discardLogger())
	exp3.TokenSigner = ts
	if _, err := exp3.Expand(context.Background(), maillist.ExpandInput{
		List: ml, Parsed: mustParse(t, testMessage), Raw: []byte(testMessage),
	}); err != nil {
		t.Fatalf("Expand (post-reactivate): %v", err)
	}
	if got := recipientsOf(sub3.Calls()); !got["member@example.net"] {
		t.Fatalf("reactivated member did not receive the next fan-out copy: recipients=%v", got)
	}
}

func recipientsOf(calls []queue.Submission) map[string]bool {
	out := map[string]bool{}
	for _, c := range calls {
		for _, r := range c.Recipients {
			out[r] = true
		}
	}
	return out
}
