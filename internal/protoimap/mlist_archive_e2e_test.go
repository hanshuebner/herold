package protoimap_test

// mlist_archive_e2e_test.go is the REQ-MLIST-70..74 full-pipeline proof
// requested for Stage 4 (epic #187): one real Expand() call fans a post
// out to a mix of `each` and `nomail` members and archives it once, then
// a `nomail` member reads the archived copy over a REAL IMAP connection
// (SELECT/FETCH) and is refused every mutation (APPEND/STORE/EXPUNGE) --
// the same protocol-level session_mailbox.go / acl.go gating
// mlist_archive_test.go already exercises, driven this time from the
// actual Expander rather than a hand-seeded message.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// e2eFakeSubmitter records Submit calls (recipient + body) without a real
// outbound queue -- this test only needs to prove WHO got a copy, not
// deliver it; TestExpand_ArchiveFilesOnce_SharesBlobWithFanout
// (internal/maillist) already proves the blob-dedup guarantee through
// the real queue.
type e2eFakeSubmitter struct {
	mu    sync.Mutex
	calls []string
}

func (s *e2eFakeSubmitter) Submit(_ context.Context, msg queue.Submission) (queue.EnvelopeID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, msg.Recipients[0])
	return queue.EnvelopeID(fmt.Sprintf("env-%d", len(s.calls))), nil
}

func (s *e2eFakeSubmitter) recipients() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

const e2ePostBody = "From: poster@sender.test\r\n" +
	"To: list@example.test\r\n" +
	"Subject: quarterly update\r\n" +
	"Message-ID: <e2e-post-1@sender.test>\r\n" +
	"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
	"\r\n" +
	"Body text.\r\n"

// TestMlistArchive_EndToEnd_FanoutThenIMAPRead is the acceptance scenario
// from issue #187: post to a list with a mix of `each` and `nomail`
// members -> `each` members get email (a Submit call each), `nomail`
// members get none, the archive holds exactly one copy, and a `nomail`
// member can READ it over IMAP but cannot mutate it.
func TestMlistArchive_EndToEnd_FanoutThenIMAPRead(t *testing.T) {
	af := newACLFixture(t) // gives us a real store + IMAP-attached harness
	ctx := context.Background()

	// The list, its Group principal, and its archive mailbox.
	group, err := af.ha.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: "list@example.test", DisplayName: "E2E List",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	ml, err := af.ha.Store.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID: group.ID, PostingAddress: "list@example.test", DisplayName: "E2E List",
		OwnerID: af.aliceID,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}
	archiveMB, err := af.ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: group.ID, Name: maillist.ArchiveMailboxName(ml), Attributes: store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(archive): %v", err)
	}
	ml.ArchiveMailboxID = &archiveMB.ID
	if err := af.ha.Store.Meta().UpdateMailingList(ctx, ml); err != nil {
		t.Fatalf("UpdateMailingList(set archive): %v", err)
	}
	ml, err = af.ha.Store.Meta().GetMailingList(ctx, ml.ID)
	if err != nil {
		t.Fatalf("GetMailingList: %v", err)
	}

	// Roster: two `each` external members, one `nomail` internal member.
	if _, err := af.ha.Store.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: ml.ID, ExternalAddress: strPtrE2E("each1@example.net"),
	}); err != nil {
		t.Fatalf("add each1: %v", err)
	}
	if _, err := af.ha.Store.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: ml.ID, ExternalAddress: strPtrE2E("each2@example.net"),
	}); err != nil {
		t.Fatalf("add each2: %v", err)
	}
	dir := directory.New(af.ha.Store.Meta(), af.ha.Logger, af.ha.Clock, nil)
	nomailPass := "nomail-correct-horse"
	nomailPID, err := dir.CreatePrincipal(ctx, "nomail-member@example.test", nomailPass)
	if err != nil {
		t.Fatalf("create nomail member: %v", err)
	}
	nomailMember, err := af.ha.Store.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: ml.ID, PrincipalID: &nomailPID, DeliveryMode: store.MailingListDeliveryNoMail,
	})
	if err != nil {
		t.Fatalf("add nomail member: %v", err)
	}
	// The exact call the admin REST roster handlers make -- REQ-MLIST-72.
	if err := maillist.SyncMemberArchiveGrant(ctx, af.ha.Store.Meta(), ml, nomailMember); err != nil {
		t.Fatalf("SyncMemberArchiveGrant: %v", err)
	}

	// Post to the list: one real Expand() call.
	sub := &e2eFakeSubmitter{}
	exp := maillist.NewExpander(af.ha.Store.Meta(), sub, nil, af.ha.Clock, af.ha.Logger)
	exp.Blobs = af.ha.Store.Blobs()
	parsed, err := mailparse.Parse(strings.NewReader(e2ePostBody), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("mailparse.Parse: %v", err)
	}
	res, err := exp.Expand(ctx, maillist.ExpandInput{
		List:   ml,
		Parsed: parsed,
		Raw:    []byte(e2ePostBody),
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	// `each` members got email; `nomail` got none.
	if res.MemberCount != 2 {
		t.Fatalf("MemberCount = %d, want 2 (each1 + each2 only)", res.MemberCount)
	}
	got := map[string]bool{}
	for _, r := range sub.recipients() {
		got[r] = true
	}
	if !got["each1@example.net"] || !got["each2@example.net"] {
		t.Fatalf("expected email to each1/each2, got recipients=%v", sub.recipients())
	}
	if len(got) != 2 {
		t.Fatalf("unexpected extra recipients (nomail member must get no copy): %v", sub.recipients())
	}

	// The archive holds exactly one copy.
	if !res.Archived {
		t.Fatalf("result.Archived = false; want true")
	}
	archived, err := af.ha.Store.Meta().ListMessages(ctx, archiveMB.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(archive): %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archive holds %d messages, want exactly 1", len(archived))
	}

	// The nomail member reads it over a REAL IMAP connection.
	c := loginAsACL(t, af, "nomail-member@example.test", nomailPass)
	defer c.close()
	archiveName := maillist.ArchiveMailboxName(ml)
	selResp := c.send("s1", fmt.Sprintf(`SELECT %q`, archiveName))
	if !strings.Contains(selResp[len(selResp)-1], "OK") {
		t.Fatalf("nomail member SELECT the archive failed: %v", selResp)
	}
	fetchResp := c.send("f1", `FETCH 1 (UID FLAGS ENVELOPE)`)
	if !strings.Contains(strings.Join(fetchResp, "\n"), "quarterly update") {
		t.Fatalf("nomail member FETCH did not return the fanned-out post: %v", fetchResp)
	}

	// ...but cannot mutate it: APPEND, STORE \Deleted, EXPUNGE all NO.
	appendMsg := "From: x@example.net\r\n\r\nshould not append\r\n"
	c.write(fmt.Sprintf("a1 APPEND %q {%d}\r\n", archiveName, len(appendMsg)))
	var last string
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "+") {
			c.write(appendMsg + "\r\n")
			continue
		}
		if strings.HasPrefix(line, "a1 ") {
			last = line
			break
		}
	}
	if !strings.Contains(last, "NO") {
		t.Fatalf("nomail member APPEND into the archive must be refused: %v", last)
	}
	storeResp := c.send("st1", `STORE 1 +FLAGS (\Deleted)`)
	if !strings.Contains(storeResp[len(storeResp)-1], "NO") {
		t.Fatalf("nomail member STORE +FLAGS (\\Deleted) must be refused: %v", storeResp)
	}
	expungeResp := c.send("e1", "EXPUNGE")
	if !strings.Contains(expungeResp[len(expungeResp)-1], "NO") {
		t.Fatalf("nomail member EXPUNGE must be refused: %v", expungeResp)
	}

	// The archived post survived every denied mutation.
	archivedAfter, err := af.ha.Store.Meta().ListMessages(ctx, archiveMB.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(archive) after mutation attempts: %v", err)
	}
	if len(archivedAfter) != 1 {
		t.Fatalf("archive holds %d messages after denied mutations, want still exactly 1", len(archivedAfter))
	}
}

func strPtrE2E(s string) *string { return &s }
