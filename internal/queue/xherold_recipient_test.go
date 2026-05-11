package queue_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// TestSubmit_StripsXHeroldRecipient verifies REQ-FLOW-35: the queue
// strips the herold-internal X-Herold-Recipient header from the user-
// supplied body before persisting the body blob. The signed and
// relayed outbound message must NEVER carry the per-recipient
// annotation that exists only for inbound rendering (REQ-FLOW-34).
//
// The user smuggles the header in (a forged JMAP EmailSubmission, or
// a malicious local APPEND-then-Send path). The strip is unconditional
// and happens before the blob is stored so DKIM signing sees the
// clean body.
func TestSubmit_StripsXHeroldRecipient(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	forged := "From: alice@local.test\r\n" +
		"X-Herold-Recipient: leaked@example.test\r\n" +
		"Subject: hi\r\n" +
		"\r\n" +
		"body bytes\r\n"
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader(forged),
	})
	if envID == "" {
		t.Fatal("expected envelope id")
	}

	rows, err := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d queue rows, want 1", len(rows))
	}
	// Read the persisted body blob — that is the bytes that go to the
	// signer / wire. The strip MUST have removed the forged header.
	rc, err := f.store.Blobs().Get(f.ctx, rows[0].BodyBlobHash)
	if err != nil {
		t.Fatalf("Blobs.Get: %v", err)
	}
	defer rc.Close()
	bodyOut, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Contains(bytes.ToLower(bodyOut), []byte("x-herold-recipient")) {
		t.Fatalf("queued body still carries X-Herold-Recipient:\n%s", string(bodyOut))
	}
	// Other headers and the body must remain.
	if !bytes.Contains(bodyOut, []byte("From: alice@local.test\r\n")) {
		t.Fatalf("From header lost during strip:\n%s", string(bodyOut))
	}
	if !bytes.Contains(bodyOut, []byte("Subject: hi\r\n")) {
		t.Fatalf("Subject header lost during strip:\n%s", string(bodyOut))
	}
	if !bytes.HasSuffix(bodyOut, []byte("body bytes\r\n")) {
		t.Fatalf("body content lost during strip; tail=%q", string(bodyOut[max(0, len(bodyOut)-32):]))
	}
}

// TestSubmit_StripsXHeroldRecipient_CaseInsensitive guards against
// case-variant smuggling.
func TestSubmit_StripsXHeroldRecipient_CaseInsensitive(t *testing.T) {
	f := newFixture(t, fixtureOpts{concurrency: 4, perHost: 2})
	forged := "From: alice@local.test\r\n" +
		"x-HEROLD-recipient: leaked@example.test\r\n" +
		"Subject: hi\r\n" +
		"\r\n" +
		"body\r\n"
	envID := f.submit(t, queue.Submission{
		MailFrom:   "alice@local.test",
		Recipients: []string{"bob@dest.test"},
		Body:       strings.NewReader(forged),
	})
	rows, err := f.store.Meta().ListQueueItems(f.ctx, store.QueueFilter{EnvelopeID: envID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	rc, err := f.store.Blobs().Get(f.ctx, rows[0].BodyBlobHash)
	if err != nil {
		t.Fatalf("Blobs.Get: %v", err)
	}
	defer rc.Close()
	bodyOut, _ := io.ReadAll(rc)
	if bytes.Contains(bytes.ToLower(bodyOut), []byte("x-herold-recipient")) {
		t.Fatalf("upper-case variant not stripped:\n%s", string(bodyOut))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
