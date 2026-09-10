package protoimap_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// wireReproMultipartAlternative mirrors reproMultipartAlternative in
// session_fetch_bodystructure_test.go (kept as an independent copy since
// that file lives in the white-box package protoimap, and this one in the
// black-box protoimap_test package): the MIME shape from herold issue
// #321 (production message id 3366), a multipart/alternative message with
// a text/plain part and a multipart/related HTML part carrying one inline
// image.
const wireReproMultipartAlternative = "From: sender@example.test\r\n" +
	"To: alice@example.test\r\n" +
	"Subject: repro\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"outer\"\r\n" +
	"\r\n" +
	"--outer\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"hello plain\r\n" +
	"--outer\r\n" +
	"Content-Type: multipart/related; boundary=\"inner\"\r\n" +
	"\r\n" +
	"--inner\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: 7bit\r\n" +
	"\r\n" +
	"<p>hello html</p>\r\n" +
	"--inner\r\n" +
	"Content-Type: image/jpeg\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-ID: <img1>\r\n" +
	"Content-Disposition: inline; filename=\"a.jpg\"\r\n" +
	"\r\n" +
	"aGVsbG8=\r\n" +
	"--inner--\r\n" +
	"--outer--\r\n"

// TestFETCH_BODYSTRUCTURE_NestedMultipart is the conformance case pinning
// issue #321 end to end: a real IMAP session FETCHes BODYSTRUCTURE for a
// multipart/alternative message and gets the nested per-part structure
// (not the single-part flattening that left the Gmail Android app unable
// to find a text section), then fetches the HTML subpart by its numbered
// section and gets exactly that subpart's raw bytes.
func TestFETCH_BODYSTRUCTURE_NestedMultipart(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := wireReproMultipartAlternative
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	_, _, err = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  f.pid,
		InternalDate: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		Size:         int64(len(msg)),
		Blob:         blob,
		Envelope:     store.Envelope{Subject: "repro", From: "sender@example.test", To: "alice@example.test"},
	}, []store.MessageMailbox{{MailboxID: f.inbox.ID}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")

	resp := c.send("f1", "FETCH 1 (UID BODYSTRUCTURE)")
	joined := strings.Join(resp, "")
	const wantStructure = `("TEXT" "PLAIN" ("CHARSET" "utf-8") NIL NIL "7BIT" 11 0 NIL NIL NIL NIL)(("TEXT" "HTML" ("CHARSET" "utf-8") NIL NIL "7BIT" 17 0 NIL NIL NIL NIL)("IMAGE" "JPEG" NIL "<img1>" NIL "BASE64" 8 NIL ("INLINE" ("FILENAME" "a.jpg")) NIL NIL) "RELATED" ("BOUNDARY" "inner") NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "outer") NIL NIL NIL`
	if !strings.Contains(joined, wantStructure) {
		t.Fatalf("FETCH BODYSTRUCTURE missing nested structure:\n got:  %v\n want substring: %s", resp, wantStructure)
	}
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("FETCH BODYSTRUCTURE did not complete OK: %v", resp)
	}

	resp = c.send("f2", "FETCH 1 BODY.PEEK[2.1]")
	joined = strings.Join(resp, "\n")
	if !strings.Contains(joined, "BODY[2.1]") || !strings.Contains(joined, "<p>hello html</p>") {
		t.Fatalf("FETCH BODY.PEEK[2.1] did not return the HTML subpart: %v", resp)
	}
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("FETCH BODY.PEEK[2.1] did not complete OK: %v", resp)
	}

	resp = c.send("f3", "FETCH 1 BODY.PEEK[2.2.MIME]")
	joined = strings.Join(resp, "\n")
	if !strings.Contains(joined, "Content-Type: image/jpeg") || !strings.Contains(joined, "Content-ID: <img1>") {
		t.Fatalf("FETCH BODY.PEEK[2.2.MIME] did not return the image part's own MIME header: %v", resp)
	}
}
