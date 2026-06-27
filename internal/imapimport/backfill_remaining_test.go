package imapimport

// backfill_remaining_test.go covers D6: the herold_imapimport_backfill_remaining
// gauge must reflect the real count of in-horizon UIDs below a folder's
// low-water mark that are not yet mirrored, and drain to zero once the backfill
// completes. REQ-IMAP-IMP-63.

import (
	"context"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

func TestCountUIDsBelow(t *testing.T) {
	uids := []imap.UID{1, 2, 3, 4, 5}
	cases := []struct {
		mark uint64
		want int
	}{
		{0, 0},   // zero mark -> 0 by contract
		{1, 0},   // nothing strictly below 1
		{3, 2},   // 1,2
		{6, 5},   // all
		{100, 5}, // all
	}
	for _, c := range cases {
		if got := countUIDsBelow(uids, c.mark); got != c.want {
			t.Errorf("countUIDsBelow(uids, %d) = %d; want %d", c.mark, got, c.want)
		}
	}
	if got := countUIDsBelow(nil, 10); got != 0 {
		t.Errorf("countUIDsBelow(nil, 10) = %d; want 0", got)
	}
}

// dropLowFetchConn wraps a Conn but drops fetched messages whose UID is below
// minUID, simulating a backfill that fetched only part of the in-horizon set
// (e.g. an interrupted run). The low-water mark then sits above the unfetched
// UIDs, so backfill_remaining is genuinely non-zero.
type dropLowFetchConn struct {
	Conn
	minUID imap.UID
}

func (c *dropLowFetchConn) UIDFetch(ctx context.Context, uids []imap.UID) ([]fetchedMessage, error) {
	msgs, err := c.Conn.UIDFetch(ctx, uids)
	if err != nil {
		return nil, err
	}
	var out []fetchedMessage
	for _, m := range msgs {
		if m.UID >= c.minUID {
			out = append(out, m)
		}
	}
	return out, nil
}

// TestBackfillRemainingGaugeRealAndDrains verifies the gauge is the real
// remaining count (not a hardcoded 0) and drains to zero once the backfill
// completes. REQ-IMAP-IMP-63 / D6.
func TestBackfillRemainingGaugeRealAndDrains(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("bfr1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	// Five INBOX messages (UIDs 1..5), all within a generous horizon floor.
	base := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		d := base.AddDate(0, i, 0)
		raw := buildRFC822("bfr-msg-"+time.Month(i+1).String()+"@test", "BFR", d)
		appendToServer(t, ts, "bfr1", "pw", "INBOX", raw, nil, d)
	}
	floor := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "bfr1@example.test",
		username:            "bfr1",
		credentialPlaintext: "pw",
	}, &floor)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: noopCategoriser{},
	})

	credPlaintext, err := w.openCredential(ctx, acc)
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	dp := dialParams{
		AccountID:           acc.ID,
		Host:                acc.Host,
		Port:                acc.Port,
		TLSMode:             string(acc.TLSMode),
		Username:            acc.Username,
		AuthMethod:          string(acc.AuthMethod),
		CredentialPlaintext: credPlaintext,
	}

	// Pass 1: a partial fetch that only ingests UIDs >= 3, leaving 1 and 2
	// below the low-water mark. backfill_remaining must report 2.
	partialConn, err := w.opts.dialer.Dial(ctx, dp)
	if err != nil {
		t.Fatalf("dial partial: %v", err)
	}
	if err := w.syncAllFolders(ctx, &dropLowFetchConn{Conn: partialConn, minUID: 3}); err != nil {
		t.Fatalf("partial syncAllFolders: %v", err)
	}
	partialConn.Logout()
	partialConn.Close()

	if got := testutil.ToFloat64(observe.IMAPImportBackfillRemaining.WithLabelValues(acc.ID)); got != 2 {
		t.Errorf("backfill_remaining after partial backfill = %v; want 2", got)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 3 {
		t.Fatalf("INBOX message count after partial backfill = %d; want 3", got)
	}

	// Pass 2: a full fetch. The lowered-horizon backfill extension pulls UIDs
	// 1 and 2, lowering the low-water mark, so backfill_remaining drains to 0.
	fullConn, err := w.opts.dialer.Dial(ctx, dp)
	if err != nil {
		t.Fatalf("dial full: %v", err)
	}
	if err := w.syncAllFolders(ctx, fullConn); err != nil {
		t.Fatalf("full syncAllFolders: %v", err)
	}
	fullConn.Logout()
	fullConn.Close()

	if got := testutil.ToFloat64(observe.IMAPImportBackfillRemaining.WithLabelValues(acc.ID)); got != 0 {
		t.Errorf("backfill_remaining after complete backfill = %v; want 0", got)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 5 {
		t.Errorf("INBOX message count after complete backfill = %d; want 5", got)
	}
}
