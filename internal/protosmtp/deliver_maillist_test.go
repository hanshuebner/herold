package protosmtp_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// capturingSubmissionQueue is like fakeSubmissionQueue (submission_queue_test.go)
// but also retains the submitted body bytes, so mailing-list tests can
// assert on the shaped headers each fan-out copy carries.
type capturingSubmissionQueue struct {
	mu    sync.Mutex
	calls []capturedMailingListSubmission
	seq   int
}

type capturedMailingListSubmission struct {
	queue.Submission
	Body []byte
}

func (c *capturingSubmissionQueue) Submit(_ context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	var body []byte
	if msg.Body != nil {
		body, _ = io.ReadAll(msg.Body)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.calls = append(c.calls, capturedMailingListSubmission{Submission: msg, Body: body})
	return queue.EnvelopeID(fmt.Sprintf("env-mlist-%d", c.seq)), nil
}

func (c *capturingSubmissionQueue) Calls() []capturedMailingListSubmission {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capturedMailingListSubmission, len(c.calls))
	copy(out, c.calls)
	return out
}

// mustInsertTestMailingList creates the backing Group principal, an
// owner principal, and the mailing_list row on the fixture's store.
func mustInsertTestMailingList(t *testing.T, st store.Store, address string) store.MailingList {
	t.Helper()
	ctx := context.Background()
	owner, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "owner-" + address,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(owner): %v", err)
	}
	group, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: address, DisplayName: "Announce",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	ml, err := st.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID:    group.ID,
		PostingAddress: address,
		DisplayName:    "Announce",
		OwnerID:        owner.ID,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}
	return ml
}

func mustAddTestListMember(t *testing.T, st store.Store, listID store.MailingListID, addr string) {
	t.Helper()
	if _, err := st.Meta().AddMailingListMember(context.Background(), store.MailingListMember{
		ListID:          listID,
		ExternalAddress: &addr,
	}); err != nil {
		t.Fatalf("AddMailingListMember(%s): %v", addr, err)
	}
}

// TestMailingList_RCPT_ExpandsToActiveMembers is the REQ-MLIST-10..12/
// 20/24 end-to-end acceptance test: mail accepted on the relay-in
// listener, addressed to a hosted list's posting_address, reaches
// every active/each member with the correct List-ID / List-Post /
// Auto-Submitted headers -- driven through the real SMTP session state
// machine (RCPT TO / DATA), not by calling internal/maillist directly.
func TestMailingList_RCPT_ExpandsToActiveMembers(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ml := mustInsertTestMailingList(t, f.ha.Store, "announce@example.test")
	mustAddTestListMember(t, f.ha.Store, ml.ID, "member1@example.net")
	mustAddTestListMember(t, f.ha.Store, ml.ID, "member2@example.net")

	sub := &capturingSubmissionQueue{}
	exp := maillist.NewExpander(f.ha.Store.Meta(), sub, nil, f.ha.Clock, f.ha.Logger)
	f.srv.SetMailingListExpander(exp)

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<poster@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<announce@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: poster@sender.test\r\n" +
		"To: announce@example.test\r\n" +
		"Subject: quarterly update\r\n" +
		"Message-ID: <mlist-e2e-1@sender.test>\r\n" +
		"\r\n" +
		"Body text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	calls := sub.Calls()
	if len(calls) != 2 {
		t.Fatalf("Submit calls = %d, want 2 (one per active member)", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		if len(c.Recipients) != 1 {
			t.Fatalf("Recipients = %v, want exactly one address per call", c.Recipients)
		}
		seen[c.Recipients[0]] = true
		bodyStr := string(c.Body)
		if !strings.Contains(bodyStr, `List-ID: "Announce" <announce.example.test>`) {
			t.Errorf("copy to %s missing correct List-ID:\n%s", c.Recipients[0], bodyStr)
		}
		if !strings.Contains(bodyStr, "List-Post: <mailto:announce@example.test>") {
			t.Errorf("copy to %s missing correct List-Post:\n%s", c.Recipients[0], bodyStr)
		}
		if !strings.Contains(bodyStr, "Auto-Submitted: auto-forwarded") {
			t.Errorf("copy to %s missing Auto-Submitted: auto-forwarded:\n%s", c.Recipients[0], bodyStr)
		}
		if !strings.Contains(bodyStr, "From: poster@sender.test") {
			t.Errorf("copy to %s lost the original From: header:\n%s", c.Recipients[0], bodyStr)
		}
		if c.SigningDomain != "example.test" || !c.Sign {
			t.Errorf("copy to %s: Sign=%v SigningDomain=%q, want Sign=true SigningDomain=example.test", c.Recipients[0], c.Sign, c.SigningDomain)
		}
	}
	if !seen["member1@example.net"] || !seen["member2@example.net"] {
		t.Fatalf("expected copies to both members, got %v", seen)
	}

	// No local mailbox delivery: the posting address is a list, not a
	// principal mailbox.
	mbs, _ := f.ha.Store.Meta().ListMailboxes(context.Background(), f.principal)
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "INBOX") {
			msgs, _ := f.ha.Store.Meta().ListMessages(context.Background(), mb.ID, store.MessageFilter{Limit: 5})
			if len(msgs) != 0 {
				t.Errorf("mailing-list post must not deliver locally to alice's INBOX, found %d messages", len(msgs))
			}
		}
	}
}

// TestMailingList_RCPT_LoopGuardDropsWithoutFanout verifies a message
// that already carries this list's own List-ID is accepted (250, no
// SMTP-level error surfaced to the sending peer) but produces zero
// queue submissions (REQ-MLIST-30).
func TestMailingList_RCPT_LoopGuardDropsWithoutFanout(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	ml := mustInsertTestMailingList(t, f.ha.Store, "announce@example.test")
	mustAddTestListMember(t, f.ha.Store, ml.ID, "member1@example.net")

	sub := &capturingSubmissionQueue{}
	exp := maillist.NewExpander(f.ha.Store.Meta(), sub, nil, f.ha.Clock, f.ha.Logger)
	f.srv.SetMailingListExpander(exp)

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<other-list-bounce@elsewhere.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<announce@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: poster@sender.test\r\n" +
		"To: announce@example.test\r\n" +
		"Subject: re-looped update\r\n" +
		"List-ID: \"Announce\" <announce.example.test>\r\n" +
		"Message-ID: <mlist-e2e-loop-1@sender.test>\r\n" +
		"\r\n" +
		"Body text.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	if calls := sub.Calls(); len(calls) != 0 {
		t.Fatalf("Submit calls = %d, want 0 (loop guard must drop before fan-out)", len(calls))
	}
}
