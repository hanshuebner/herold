package protoimap_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestFETCH_BODY_InjectsXHeroldRecipient verifies REQ-FLOW-34 on the
// IMAP read path: FETCH BODY[] returns the synthetic
// X-Herold-Recipient header prepended to the stored body bytes, with
// the value drawn from the per-(message, mailbox) received_to column.
// The persisted blob does NOT contain the header — the renderer
// injects it at fetch time (REQ-FLOW-30 dedup invariant).
func TestFETCH_BODY_InjectsXHeroldRecipient(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("annotated", "hello world")
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	_, _, err = f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size:     int64(len(msg)),
		Blob:     blob,
		Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{
		MailboxID:  f.inbox.ID,
		ReceivedTo: "alice+work@example.test",
	}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("f1", "FETCH 1 (BODY.PEEK[])")
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "X-Herold-Recipient: alice+work@example.test") {
		t.Fatalf("FETCH BODY[] did not return X-Herold-Recipient header.\nResponse:\n%s", joined)
	}
	// The injected header must precede the original From: line so a
	// client reading headers top-down sees it without continuation
	// walking.
	xPos := strings.Index(joined, "X-Herold-Recipient:")
	fromPos := strings.Index(joined, "From: sender@example.test")
	if xPos < 0 || fromPos < 0 || xPos > fromPos {
		t.Fatalf("X-Herold-Recipient must precede From: in BODY[]; xPos=%d fromPos=%d\n%s",
			xPos, fromPos, joined)
	}
}

// TestFETCH_BODY_LegacyEmptyReceivedTo asserts the legacy-row case
// (REQ-FLOW-34 final sentence): when received_to is empty (pre-feature
// memberships, Email/set drafts, copies, IMAP APPENDs), the renderer
// emits no synthetic header.
func TestFETCH_BODY_LegacyEmptyReceivedTo(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("legacy", "no annotation")
	blob, err := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("f1", "FETCH 1 (BODY.PEEK[])")
	joined := strings.Join(resp, "\n")
	if strings.Contains(strings.ToLower(joined), "x-herold-recipient") {
		t.Fatalf("FETCH BODY[] injected X-Herold-Recipient for an empty received_to row:\n%s", joined)
	}
}

// TestFETCH_BODY_HEADER_InjectsXHeroldRecipient covers REQ-FLOW-34 for
// the HEADER-only section. The synthetic header lives in the header
// block, so BODY[HEADER] must surface it just like BODY[].
func TestFETCH_BODY_HEADER_InjectsXHeroldRecipient(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("hdr", "body")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if _, _, err := f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{
		MailboxID:  f.inbox.ID,
		ReceivedTo: "alice+filter@example.test",
	}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("h1", "FETCH 1 (BODY.PEEK[HEADER])")
	joined := strings.Join(resp, "\n")
	want := "X-Herold-Recipient: alice+filter@example.test"
	if !strings.Contains(joined, want) {
		t.Fatalf("FETCH BODY[HEADER] missing %q.\nResponse:\n%s", want, joined)
	}
}

// TestFETCH_BODY_HEADER_FIELDS_InjectsXHeroldRecipient confirms
// BODY[HEADER.FIELDS (X-Herold-Recipient)] surfaces the synthetic
// header — a client filtering for it must be able to retrieve it.
func TestFETCH_BODY_HEADER_FIELDS_InjectsXHeroldRecipient(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true})
	ctx := context.Background()
	msg := buildMessage("filter", "body")
	blob, _ := f.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	if _, _, err := f.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob, Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{
		MailboxID:  f.inbox.ID,
		ReceivedTo: "alice+team@example.test",
	}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	c := loggedInClient(t, f)
	defer c.close()
	c.send("s1", "SELECT INBOX")
	resp := c.send("hf1", "FETCH 1 (BODY.PEEK[HEADER.FIELDS (X-Herold-Recipient)])")
	joined := strings.Join(resp, "\n")
	want := "alice+team@example.test"
	if !strings.Contains(joined, want) {
		t.Fatalf("FETCH BODY[HEADER.FIELDS (X-Herold-Recipient)] missing recipient.\nResponse:\n%s", joined)
	}
}

// fmtSize is used by some tests via t.Logf; kept here to give the
// linter something to chew on if the build adds future helpers and
// the import-cleanup hook drops "fmt" by mistake.
var _ = fmt.Sprintf
