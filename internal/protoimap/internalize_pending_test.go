package protoimap_test

// internalize_pending_test.go covers REQ-EXTIMG-90/91/92 for the
// live IMAP surfaces that store a message verbatim (APPEND) or
// re-stage an existing row's blob without rewriting it (COPY/MOVE):
// both must honour the same on-demand flagging decision the
// IMAP-mirror and Gmail Takeout importers apply, factored into
// internal/extimg (re #446).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// buildHTMLMessage returns a minimal RFC 5322 message with an HTML
// body. When withRemoteImage is true the body carries an
// <img src="http://..."> reference.
func buildHTMLMessage(subject string, withRemoteImage bool) string {
	headers := "From: sender@example.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Thu, 01 Jan 2026 00:00:00 +0000\r\n" +
		"Message-ID: <" + subject + "@extimg.test>\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n"
	if withRemoteImage {
		return headers + "<html><body><p>" + subject + "</p><img src=\"https://example.test/tracker.png\"></body></html>\r\n"
	}
	return headers + "<html><body><p>" + subject + ", no images here</p></body></html>\r\n"
}

// appendLiteral APPENDs msg into mailbox via a synchronising literal
// and returns the tagged response's final line.
func appendLiteral(t *testing.T, c *client, tag, mailbox, msg string) string {
	t.Helper()
	c.write(fmt.Sprintf("%s APPEND %s {%d}\r\n", tag, mailbox, len(msg)))
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation, got: %q", line)
	}
	c.write(msg + "\r\n")
	resp := c.readUntilTag(tag)
	return resp[len(resp)-1]
}

// TestAPPEND_FlagsExternalHTMLImage verifies that a single-literal
// IMAP APPEND whose HTML body carries an external http(s) image
// reference lands with InternalizePending set (REQ-EXTIMG-91).
func TestAPPEND_FlagsExternalHTMLImage(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	c := loggedInClient(t, f)
	defer c.close()

	msg := buildHTMLMessage("append-flag", true)
	last := appendLiteral(t, c, "a1", "INBOX", msg)
	if !strings.Contains(last, "OK") {
		t.Fatalf("APPEND failed: %v", last)
	}

	msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil || len(msgs) == 0 {
		t.Fatalf("list messages: err=%v, count=%d", err, len(msgs))
	}
	stored := msgs[len(msgs)-1]
	if !stored.InternalizePending {
		t.Errorf("InternalizePending = false; want true for an APPENDed message with an external HTML image reference")
	}
}

// TestAPPEND_NoExternalImageNotFlagged verifies that plain HTML with
// no external image reference is not flagged.
func TestAPPEND_NoExternalImageNotFlagged(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	c := loggedInClient(t, f)
	defer c.close()

	msg := buildHTMLMessage("append-noflag", false)
	last := appendLiteral(t, c, "a1", "INBOX", msg)
	if !strings.Contains(last, "OK") {
		t.Fatalf("APPEND failed: %v", last)
	}

	msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil || len(msgs) == 0 {
		t.Fatalf("list messages: err=%v, count=%d", err, len(msgs))
	}
	stored := msgs[len(msgs)-1]
	if stored.InternalizePending {
		t.Errorf("InternalizePending = true; want false for an APPENDed message with no external image reference")
	}
}

// TestAPPEND_PolicyOffSuppressesFlag verifies that
// InternalizeImportsPolicy = "off" suppresses the flag even when the
// body carries an external HTML image reference (REQ-EXTIMG-92).
func TestAPPEND_PolicyOffSuppressesFlag(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true, internalizeImportsPolicy: "off"})
	ctx := context.Background()
	c := loggedInClient(t, f)
	defer c.close()

	msg := buildHTMLMessage("append-policy-off", true)
	last := appendLiteral(t, c, "a1", "INBOX", msg)
	if !strings.Contains(last, "OK") {
		t.Fatalf("APPEND failed: %v", last)
	}

	msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil || len(msgs) == 0 {
		t.Fatalf("list messages: err=%v, count=%d", err, len(msgs))
	}
	stored := msgs[len(msgs)-1]
	if stored.InternalizePending {
		t.Errorf("InternalizePending = true; want false when InternalizeImportsPolicy is \"off\"")
	}
}

// TestCOPY_PropagatesInternalizePending verifies that IMAP COPY
// carries a still-pending source row's InternalizePending marker
// forward onto the destination row (re #446): the copy shares the
// source's un-rewritten blob, so dropping the flag would leave the
// copy permanently un-internalized even after the source row is
// rewritten and its own flag cleared.
func TestCOPY_PropagatesInternalizePending(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()

	dest, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.pid, "Archive")
	if err != nil {
		t.Fatalf("get Archive: %v", err)
	}

	body := buildHTMLMessage("copy-pending", true)
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	_, _, err = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:        f.pid,
		Size:               int64(len(body)),
		Blob:               blob,
		Envelope:           parseStoreEnvelope(body),
		InternalDate:       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		InternalizePending: true,
	}, []store.MessageMailbox{{MailboxID: f.inbox.ID}})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("cp1", "COPY 1 Archive")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("COPY failed: %v", resp)
	}

	dmsgs, err := f.ha.Store.Meta().ListMessages(ctx, dest.ID, store.MessageFilter{})
	if err != nil || len(dmsgs) != 1 {
		t.Fatalf("list Archive: err=%v, count=%d", err, len(dmsgs))
	}
	if !dmsgs[0].InternalizePending {
		t.Errorf("InternalizePending = false on the COPY destination row; want true (propagated from the source)")
	}
}
