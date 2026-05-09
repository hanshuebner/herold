// Integration coverage for the > 1000-message pagination fix (re #99).
// The store backends silently cap a single ListMessages read at 1000
// rows; SELECT and FETCH must paginate to see beyond that bound.

package protoimap_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// seedManyMessages bulk-inserts n messages into the fixture's INBOX in a
// single transaction (per InsertMessages), with stable subjects so the
// caller can grep individual rows in the FETCH response if needed.
func seedManyMessages(t *testing.T, f *fixture, n int) {
	t.Helper()
	ctx := context.Background()
	items := make([]store.InsertMessageItem, 0, n)
	for i := 0; i < n; i++ {
		body := buildMessage(fmt.Sprintf("seed-%05d", i), "body")
		blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			t.Fatalf("blob put %d: %v", i, err)
		}
		items = append(items, store.InsertMessageItem{
			Message: store.Message{
				PrincipalID:  f.pid,
				Size:         int64(len(body)),
				Blob:         blob,
				Envelope:     parseStoreEnvelope(body),
				InternalDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			},
			Targets: []store.MessageMailbox{{MailboxID: f.inbox.ID}},
		})
	}
	if _, err := f.ha.Store.Meta().InsertMessages(ctx, items, store.InsertMessagesOptions{SkipThreading: true}); err != nil {
		t.Fatalf("InsertMessages(%d): %v", n, err)
	}
}

// TestSELECT_EXISTS_PaginatesAboveStoreCap confirms SELECT no longer
// truncates at the store's 1000-row internal cap. Before the fix EXISTS
// reported 1000; after the fix it reports the true count.
func TestSELECT_EXISTS_PaginatesAboveStoreCap(t *testing.T) {
	const total = 1100
	f := newFixture(t, fxOpts{implicitTLS: true})
	seedManyMessages(t, f, total)

	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("s1", "SELECT INBOX")

	want := fmt.Sprintf("%d EXISTS", total)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, want) {
		t.Fatalf("expected %q in SELECT response; got: %s", want, joined)
	}
}

// TestFETCH_ReturnsAllMessagesAboveStoreCap confirms FETCH 1:* enumerates
// every row, not just the first 1000. We assert the count of untagged
// FETCH responses matches the seeded total.
func TestFETCH_ReturnsAllMessagesAboveStoreCap(t *testing.T) {
	const total = 1100
	f := newFixture(t, fxOpts{implicitTLS: true})
	seedManyMessages(t, f, total)

	c := loggedInClient(t, f)
	defer c.close()
	if sel := c.send("s1", "SELECT INBOX"); !strings.Contains(sel[len(sel)-1], "OK") {
		t.Fatalf("SELECT failed: %v", sel)
	}
	resp := c.send("f1", "FETCH 1:* (UID FLAGS)")

	fetched := 0
	for _, line := range resp {
		// Untagged FETCH responses look like "* 1234 FETCH (...)".
		if strings.HasPrefix(line, "* ") && strings.Contains(line, " FETCH ") {
			fetched++
		}
	}
	if fetched != total {
		t.Fatalf("expected %d untagged FETCH responses, got %d (last line: %q)", total, fetched, resp[len(resp)-1])
	}
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("FETCH did not complete OK: %v", resp[len(resp)-1])
	}
}
