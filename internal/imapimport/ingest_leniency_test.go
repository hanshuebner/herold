package imapimport

// ingest_leniency_test.go covers re #279: ingest must parse leniently
// (missing closing multipart boundary marker, unrecognised charset) to
// match the render/index/search-snippet/push paths, and a message that
// still fails to ingest (a genuine structural limit, e.g. MaxDepth) must
// not be silently skipped past the folder's high-water mark.

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/testharness"
)

// buildTruncatedBoundaryRFC822 builds a multipart message whose final part
// is not followed by a closing "--boundary--" marker. The boundary is the
// literal RFC 2046 example string "simple boundary" (matching the
// production report's upstream form-mailer, re #279).
func buildTruncatedBoundaryRFC822(msgID, subject string, date time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "From: sender@example.test\r\n")
	fmt.Fprintf(&b, "To: recipient@example.test\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"simple boundary\"\r\n")
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "This is the preamble.\r\n")
	fmt.Fprintf(&b, "--simple boundary\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain\r\n")
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "This is the body of %s.\r\n", subject)
	// Deliberately no closing "--simple boundary--" marker.
	return b.Bytes()
}

// buildUnknownCharsetRFC822 builds a single-part text/plain message whose
// declared charset is not in the charset registry, with a raw 8-bit body
// byte that is not valid UTF-8/ASCII (so the parser's post-decode UTF-8
// validation, not just the charset lookup, is exercised).
func buildUnknownCharsetRFC822(msgID, subject string, date time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "From: sender@example.test\r\n")
	fmt.Fprintf(&b, "To: recipient@example.test\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=\"totally-bogus-charset-xyz\"\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString("body with a raw byte: ")
	b.WriteByte(0xFC) // not valid UTF-8 on its own
	b.WriteString("\r\n")
	return b.Bytes()
}

// buildDeeplyNestedRFC822 builds a message nested well beyond
// mailparse.DefaultMaxDepth so parsing fails with ReasonDepthExceeded
// regardless of StrictBoundary/StrictCharset settings -- a genuine
// structural limit, not a leniency knob, exercising the non-lossy
// high-water behaviour independent of the parser's strictness.
func buildDeeplyNestedRFC822(msgID, subject string, date time.Time, depth int) []byte {
	current := "Content-Type: text/plain\r\n\r\nleaf\r\n"
	for i := 0; i < depth; i++ {
		boundary := fmt.Sprintf("d%d", i)
		current = fmt.Sprintf(
			"Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n--%s\r\n%s\r\n--%s--\r\n",
			boundary, boundary, current, boundary,
		)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "From: sender@example.test\r\n")
	fmt.Fprintf(&b, "To: recipient@example.test\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	b.WriteString(current)
	return b.Bytes()
}

// TestIngestTruncatedBoundaryLenient reproduces the production defect: a
// message whose final MIME part has no closing boundary marker must still
// ingest (re #279 scope item 1).
func TestIngestTruncatedBoundaryLenient(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("trunc1", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	raw := buildTruncatedBoundaryRFC822("trunc-boundary@test", "Membership form", d)
	appendToServer(t, ts, "trunc1", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "trunc1@example.test",
		username:            "trunc1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Fatalf("INBOX has %d messages; want 1 (truncated-boundary message must still ingest)", got)
	}

	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "trunc-boundary@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if msg.Envelope.Subject != "Membership form" {
		t.Errorf("Subject = %q; want %q", msg.Envelope.Subject, "Membership form")
	}
}

// TestIngestUnknownCharsetLenient reproduces the second recorded failure
// class: a declared charset the parser cannot resolve must not reject the
// whole message (re #279 scope item 1).
func TestIngestUnknownCharsetLenient(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("charset1", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	raw := buildUnknownCharsetRFC822("unknown-charset@test", "Bogus charset", d)
	appendToServer(t, ts, "charset1", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "charset1@example.test",
		username:            "charset1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Fatalf("INBOX has %d messages; want 1 (unknown-charset message must still ingest)", got)
	}
}

// TestIngestFailureDoesNotAdvanceHighWater reproduces the durable half of
// the defect: a message that still fails to ingest (a genuine structural
// limit, not a leniency knob) must not be skipped past the folder's
// high-water mark. Before the fix, persistCursor advanced high_water to
// UIDNEXT-1 unconditionally, so the failed UID (and anything the same pass
// fetched above it) became permanently unreachable -- forward sync only
// ever looks strictly above high_water (re #279 scope item 2).
func TestIngestFailureDoesNotAdvanceHighWater(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("deep1", "pw")
	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	badRaw := buildDeeplyNestedRFC822("deep-fail@test", "Too deep", d, 40)
	badUID := appendToServer(t, ts, "deep1", "pw", "INBOX", badRaw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "deep1@example.test",
		username:            "deep1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// The bad message must not have ingested.
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 0 {
		t.Fatalf("INBOX has %d messages after first sync; want 0 (depth-exceeded message must not ingest)", got)
	}

	// The cursor's high-water mark must stop below the failed UID, not
	// advance past it -- otherwise forward sync (which only fetches UIDs
	// strictly above high_water) would never look at it again.
	cursor, found, err := ha.Store.Meta().GetIMAPImportFolderCursor(context.Background(), acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor: %v", err)
	}
	if !found {
		t.Fatal("cursor not persisted after sync")
	}
	if cursor.HighWaterUID >= uint64(badUID) {
		t.Fatalf("HighWaterUID = %d; want < %d (failed UID must remain above high_water so it stays refetchable)",
			cursor.HighWaterUID, uint64(badUID))
	}

	// A second sync pass must attempt the same UID again (it remains
	// strictly above high_water) and still fail the same way -- proving it
	// is genuinely refetchable, not silently gone.
	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 0 {
		t.Fatalf("INBOX has %d messages after second sync; want 0", got)
	}
	cursor2, _, err := ha.Store.Meta().GetIMAPImportFolderCursor(context.Background(), acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor (second): %v", err)
	}
	if cursor2.HighWaterUID >= uint64(badUID) {
		t.Fatalf("HighWaterUID after second sync = %d; want < %d (still stuck below the failed UID)",
			cursor2.HighWaterUID, uint64(badUID))
	}

	// Now append a good message above the bad one. It must still be
	// ingested by a later sync pass even though the bad UID keeps failing
	// and the water mark stays capped below it -- the failure of one
	// message must not block newer arrivals from being mirrored.
	d2 := d.Add(time.Hour)
	goodRaw := buildRFC822("deep-follow-up@test", "Follow up", d2)
	appendToServer(t, ts, "deep1", "pw", "INBOX", goodRaw, nil, d2)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if got := countMailboxMessages(t, ha.Store, acc.PrincipalID, "INBOX"); got != 1 {
		t.Fatalf("INBOX has %d messages after third sync; want 1 (the good follow-up message)", got)
	}
	if _, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "deep-follow-up@test"); err != nil {
		t.Errorf("follow-up message not ingested: %v", err)
	}
}
