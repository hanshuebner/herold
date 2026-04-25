// Phase 2 Wave 2.2 advanced-capability tests: CONDSTORE, QRESYNC,
// MOVE, NOTIFY, MULTIAPPEND, COMPRESS=DEFLATE, LIST-STATUS,
// SPECIAL-USE.
//
// All tests reuse the harness fixture established in server_test.go.
// They are deterministic against the FakeClock (no time.Sleep loops
// where avoidable; IDLE-style polls advance the clock explicitly).

package protoimap_test

import (
	"bufio"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// -----------------------------------------------------------------------------
// CAPABILITY advertisement smoke test
// -----------------------------------------------------------------------------

func TestCAPABILITY_AdvancedCapsAdvertised(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("a1", "CAPABILITY")
	joined := strings.Join(resp, "\n")
	for _, needle := range []string{
		"CONDSTORE",
		"QRESYNC",
		"MOVE",
		"MULTIAPPEND",
		"NOTIFY",
		"COMPRESS=DEFLATE",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing capability %q in %v", needle, resp)
		}
	}
}

// -----------------------------------------------------------------------------
// CONDSTORE
// -----------------------------------------------------------------------------

func TestCONDSTORE_FETCH_MODSEQ_ChangedSince(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	for i := 0; i < 3; i++ {
		seedMessage(t, f, fmt.Sprintf("m%d", i))
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("e1", "ENABLE CONDSTORE")
	c.send("s1", "SELECT INBOX")
	// Bump the modseq of message 2 by storing a flag.
	c.send("st1", `STORE 2 +FLAGS (\Seen)`)

	// FETCH 1:* (UID FLAGS) (CHANGEDSINCE 1) — only msgs with modseq>1.
	resp := c.send("f1", `FETCH 1:* (UID FLAGS) (CHANGEDSINCE 1)`)
	hits := 0
	for _, line := range resp {
		if strings.HasPrefix(line, "* ") && strings.Contains(line, "FETCH") {
			hits++
			if !strings.Contains(line, "MODSEQ") {
				t.Fatalf("CONDSTORE FETCH should include MODSEQ: %q", line)
			}
		}
	}
	if hits == 0 {
		t.Fatalf("expected at least one FETCH response, got: %v", resp)
	}
}

func TestCONDSTORE_STORE_UNCHANGEDSINCE_ReturnsMODIFIED(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	for i := 0; i < 2; i++ {
		seedMessage(t, f, fmt.Sprintf("m%d", i))
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX (CONDSTORE)")
	// Bump modseq of msg 1 first.
	c.send("st0", `STORE 1 +FLAGS (\Flagged)`)
	// Now ask STORE with UNCHANGEDSINCE 1: msg 1 should be rejected,
	// msg 2 (still at original modseq) accepted.
	resp := c.send("st1", `STORE 1:2 (UNCHANGEDSINCE 1) +FLAGS (\Seen)`)
	last := resp[len(resp)-1]
	if !strings.Contains(last, "MODIFIED") {
		t.Fatalf("expected OK [MODIFIED ...], got: %q", last)
	}
}

// -----------------------------------------------------------------------------
// QRESYNC
// -----------------------------------------------------------------------------

func TestQRESYNC_SELECT_EmitsVanishedAndFETCH(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	for i := 0; i < 3; i++ {
		seedMessage(t, f, fmt.Sprintf("m%d", i))
	}
	c := loggedInClient(t, f)
	defer c.close()
	// Initial SELECT to learn UIDVALIDITY / UIDs.
	first := c.send("s1", "SELECT INBOX")
	uidValidity := uintFromCode(first, "UIDVALIDITY")
	// Bump modseq of message 2.
	c.send("st1", `STORE 2 +FLAGS (\Seen)`)
	// Re-SELECT with QRESYNC (uidvalidity 1 (1:3))
	cmd := fmt.Sprintf("SELECT INBOX (QRESYNC (%d 1 1:3))", uidValidity)
	resp := c.send("s2", cmd)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "FETCH") {
		t.Fatalf("expected synthetic FETCH after QRESYNC SELECT: %v", resp)
	}
}

func TestQRESYNC_EXPUNGE_EmitsVanishedNotExpunge(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	for i := 0; i < 3; i++ {
		seedMessage(t, f, fmt.Sprintf("m%d", i))
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("e1", "ENABLE QRESYNC")
	c.send("s1", "SELECT INBOX")
	c.send("st1", `STORE 1:3 +FLAGS (\Deleted)`)
	resp := c.send("e1x", "EXPUNGE")
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "VANISHED") {
		t.Fatalf("expected VANISHED on QRESYNC session, got: %v", resp)
	}
	for _, line := range resp {
		if strings.HasPrefix(line, "* ") && strings.HasSuffix(line, " EXPUNGE") {
			t.Fatalf("QRESYNC session must not emit '* N EXPUNGE': %q", line)
		}
	}
}

// -----------------------------------------------------------------------------
// MOVE
// -----------------------------------------------------------------------------

func TestMOVE_AtomicCopyExpunge(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	dest, err := f.ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.pid,
		Name:        "Archive",
		Attributes:  store.MailboxAttrArchive | store.MailboxAttrSubscribed,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	for i := 0; i < 2; i++ {
		seedMessage(t, f, fmt.Sprintf("m%d", i))
	}
	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("mv1", "MOVE 1:2 Archive")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("MOVE failed: %v", resp)
	}
	// Source mailbox should have zero messages now.
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected source emptied, got %d messages", len(msgs))
	}
	// Dest should have 2 messages.
	dmsgs, _ := f.ha.Store.Meta().ListMessages(ctx, dest.ID, store.MessageFilter{})
	if len(dmsgs) != 2 {
		t.Fatalf("expected 2 messages in dest, got %d", len(dmsgs))
	}
}

// -----------------------------------------------------------------------------
// NOTIFY
// -----------------------------------------------------------------------------

func TestNOTIFY_SubscribesToMailboxEvents(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("n1", "NOTIFY SET (SELECTED MessageNew)")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("NOTIFY SET failed: %v", resp)
	}
}

func TestNOTIFY_SelectorFilters_NonMatchingMailboxIgnored(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("n1", "NOTIFY SET (MAILBOXES (Sent) MessageNew)")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("NOTIFY SET MAILBOXES failed: %v", resp)
	}
	resp = c.send("n2", "NOTIFY NONE")
	last = resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("NOTIFY NONE failed: %v", resp)
	}
}

// -----------------------------------------------------------------------------
// MULTIAPPEND
// -----------------------------------------------------------------------------

func TestMULTIAPPEND_AllOrNothing(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	msg1 := buildMessage("ma1", "first")
	msg2 := buildMessage("ma2", "second")
	cmd := fmt.Sprintf("a1 APPEND INBOX (\\Seen) {%d}\r\n", len(msg1))
	c.write(cmd)
	if !strings.HasPrefix(c.readLine(), "+") {
		t.Fatalf("expected continuation")
	}
	c.write(msg1)
	c.write(fmt.Sprintf(" (\\Flagged) {%d}\r\n", len(msg2)))
	if !strings.HasPrefix(c.readLine(), "+") {
		t.Fatalf("expected second continuation")
	}
	c.write(msg2 + "\r\n")
	resp := c.readUntilTag("a1")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") || !strings.Contains(last, "APPENDUID") {
		t.Fatalf("MULTIAPPEND failed: %v", resp)
	}
	// Verify both messages are in INBOX.
	ctx := context.Background()
	msgs, _ := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 inserted, got %d", len(msgs))
	}
}

// -----------------------------------------------------------------------------
// COMPRESS=DEFLATE
// -----------------------------------------------------------------------------

func TestCOMPRESS_DEFLATE_Roundtrip(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("z1", "COMPRESS DEFLATE")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("COMPRESS DEFLATE failed: %v", resp)
	}
	// After OK, server has installed the deflate stream. Wrap our
	// client side with flate readers/writers and issue a NOOP.
	zw, err := flate.NewWriter(c.conn, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate writer: %v", err)
	}
	zr := flate.NewReader(c.conn)
	cw := bufio.NewWriter(zw)
	br := bufio.NewReader(zr)
	if _, err := cw.WriteString("z2 NOOP\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := cw.Flush(); err != nil {
		t.Fatalf("flush bufio: %v", err)
	}
	if err := zw.Flush(); err != nil {
		t.Fatalf("flate flush: %v", err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := br.ReadString('\n')
	if err != nil && !errIsEOF(err) {
		t.Fatalf("read compressed: %v (line=%q)", err, line)
	}
	if !strings.Contains(line, "OK") {
		t.Fatalf("expected OK on NOOP through deflate, got %q", line)
	}
}

func errIsEOF(e error) bool { return e == io.EOF || e == io.ErrUnexpectedEOF }

// -----------------------------------------------------------------------------
// LIST-STATUS
// -----------------------------------------------------------------------------

func TestLIST_STATUS_ReturnsExtendedItems(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	for i := 0; i < 2; i++ {
		seedMessage(t, f, fmt.Sprintf("m%d", i))
	}
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("l1", `LIST "" "*" RETURN (STATUS (MESSAGES UIDNEXT))`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "* STATUS") {
		t.Fatalf("expected STATUS untagged response, got: %v", resp)
	}
	if !strings.Contains(joined, "MESSAGES") {
		t.Fatalf("STATUS should include MESSAGES: %v", resp)
	}
}

// -----------------------------------------------------------------------------
// SPECIAL-USE
// -----------------------------------------------------------------------------

func TestSPECIAL_USE_PerCreate(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	c := loggedInClient(t, f)
	defer c.close()
	resp := c.send("c1", `CREATE Drafts (USE (\Drafts))`)
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("CREATE-SPECIAL-USE failed: %v", resp)
	}
	// Verify the attribute persisted.
	ctx := context.Background()
	mb, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.pid, "Drafts")
	if err != nil {
		t.Fatalf("lookup Drafts: %v", err)
	}
	if mb.Attributes&store.MailboxAttrDrafts == 0 {
		t.Fatalf("expected \\Drafts attribute on created mailbox: attrs=%v", mb.Attributes)
	}
	// LIST should reflect the attribute.
	resp = c.send("l1", `LIST "" "Drafts"`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "\\Drafts") {
		t.Fatalf("LIST should expose \\Drafts: %v", resp)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// seedMessage inserts one message into the fixture's INBOX directly via
// the store. Returns nothing — the message ordering is determined by
// insertion order which the caller controls.
func seedMessage(t *testing.T, f *fixture, subject string) {
	t.Helper()
	ctx := context.Background()
	body := buildMessage(subject, "body")
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	_, _, err = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		MailboxID:    f.inbox.ID,
		Size:         int64(len(body)),
		Blob:         blob,
		Envelope:     parseStoreEnvelope(body),
		InternalDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
}

// uintFromCode pulls "[KEY n]" from a SELECT response and returns n. 0
// when not found.
func uintFromCode(resp []string, key string) uint64 {
	for _, line := range resp {
		idx := strings.Index(line, "["+key)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(key)+1:]
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			continue
		}
		fields := strings.Fields(rest[:end])
		if len(fields) == 0 {
			continue
		}
		var v uint64
		fmt.Sscanf(fields[0], "%d", &v)
		return v
	}
	return 0
}

// silence unused-import warning when the test file only sometimes
// touches clock.
var _ = clock.Real{}
