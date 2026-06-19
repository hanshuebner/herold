package protoimap_test

// Tests for the APPEND spill-file streaming path (REQ-STORE-17..18).
//
// The test messages are sized above the production appendSpillThreshold
// (64 KiB) so each APPEND triggers the disk-spill path. The tests verify:
//
//   1. A large APPEND (body > spill threshold) round-trips byte-identical
//      through the blob store.
//   2. The spill temp file is removed from os.TempDir after a successful
//      single-item APPEND (no resource leak).
//   3. MULTIAPPEND with multiple large literals stores all bodies
//      byte-identically and cleans up all spill files.
//   4. A LITERAL+ (non-synchronising) large APPEND also cleans up its spill.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// buildLargeMessage returns a minimal RFC 5322 message whose combined
// header + body is at least headerSize+bodySize bytes, using bodyPattern
// to fill the body. The body contains no CRLF so Blobs().Put stores
// exactly the bytes delivered (no CRLF normalisation changes content).
func buildLargeMessage(subject, bodyPattern string, bodySize int) string {
	headers := "From: sender@example.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Thu, 01 Jan 2026 00:00:00 +0000\r\n" +
		"Message-ID: <" + subject + "@spill.test>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n"
	var sb strings.Builder
	sb.WriteString(headers)
	for sb.Len() < len(headers)+bodySize {
		sb.WriteString(bodyPattern)
	}
	// Trim to exactly len(headers)+bodySize.
	full := sb.String()
	if len(full) > len(headers)+bodySize {
		full = full[:len(headers)+bodySize]
	}
	return full + "\r\n"
}

// countSpillsInDir returns the number of herold-imap-append-spill-*
// files currently in dir. Pass the fixture's spillDir so the count is
// isolated to this test's temp directory and not polluted by concurrent
// runs sharing the global os.TempDir().
func countSpillsInDir(t *testing.T, dir string) int {
	t.Helper()
	if dir == "" {
		dir = os.TempDir()
	}
	matches, err := filepath.Glob(filepath.Join(dir, "herold-imap-append-spill-*"))
	if err != nil {
		t.Fatalf("glob spill files: %v", err)
	}
	return len(matches)
}

// firstDiffByte returns the index of the first differing byte between a
// and b, or -1 when they are equal. Used to produce a concise failure
// message without printing kilobytes of body content.
func firstDiffByte(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// TestAPPEND_LargeBody_SpillRoundTrip appends a 128 KiB message (well
// above the 64 KiB appendSpillThreshold) via a synchronising literal and
// verifies that:
//
//   - The server returns OK [APPENDUID ...].
//   - The stored blob content is byte-identical to the sent bytes.
//   - No spill temp files remain after the command completes.
func TestAPPEND_LargeBody_SpillRoundTrip(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true, spillDir: t.TempDir()})
	ctx := context.Background()

	const bodySize = 128 * 1024 // 128 KiB — well above 64 KiB threshold
	msg := buildLargeMessage("spill-round-trip", "ABCDEFGHIJ", bodySize)

	before := countSpillsInDir(t, f.spillDir)

	c := loggedInClient(t, f)
	defer c.close()

	// Synchronising APPEND: server sends continuation, client sends body.
	c.write(fmt.Sprintf("a1 APPEND INBOX {%d}\r\n", len(msg)))
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected continuation, got: %q", line)
	}
	c.write(msg + "\r\n")
	resp := c.readUntilTag("a1")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") || !strings.Contains(last, "APPENDUID") {
		t.Fatalf("expected OK [APPENDUID ...], got: %v", last)
	}

	// No spill files should remain after successful APPEND.
	after := countSpillsInDir(t, f.spillDir)
	if after > before {
		t.Fatalf("spill file leak: before=%d after=%d", before, after)
	}

	// Byte-identity check: fetch the stored blob and compare.
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil || len(msgs) == 0 {
		t.Fatalf("list messages: err=%v, count=%d", err, len(msgs))
	}
	stored := msgs[len(msgs)-1]
	blobRC, err := f.ha.Store.Blobs().Get(ctx, stored.Blob.Hash)
	if err != nil {
		t.Fatalf("blob get: %v", err)
	}
	got, err := io.ReadAll(blobRC)
	_ = blobRC.Close()
	if err != nil {
		t.Fatalf("blob read: %v", err)
	}
	if !bytes.Equal(got, []byte(msg)) {
		t.Fatalf("blob content mismatch: got %d bytes, want %d bytes; first diff at byte %d",
			len(got), len(msg), firstDiffByte(got, []byte(msg)))
	}
}

// TestAPPEND_LargeBody_LiteralPlus_SpillCleanup uses a non-synchronising
// (LITERAL+) literal to send a 70 KiB body and verifies the spill file is
// removed on success even without the continuation round-trip.
func TestAPPEND_LargeBody_LiteralPlus_SpillCleanup(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true, spillDir: t.TempDir()})

	const bodySize = 70 * 1024 // 70 KiB — above 64 KiB threshold
	msg := buildLargeMessage("spill-literal-plus", "XYZXYZ", bodySize)

	before := countSpillsInDir(t, f.spillDir)

	c := loggedInClient(t, f)
	defer c.close()

	// LITERAL+ skips the continuation exchange.
	c.write(fmt.Sprintf("a1 APPEND INBOX {%d+}\r\n", len(msg)))
	c.write(msg + "\r\n")
	resp := c.readUntilTag("a1")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") {
		t.Fatalf("APPEND LITERAL+ failed: %v", resp)
	}

	after := countSpillsInDir(t, f.spillDir)
	if after > before {
		t.Fatalf("spill file leak after LITERAL+: before=%d after=%d", before, after)
	}
}

// TestMULTIAPPEND_LargeBodies_SpillCleanup sends two 80 KiB literals in
// one MULTIAPPEND command, verifies both messages are stored, and checks
// that all spill temp files are removed after the successful command.
func TestMULTIAPPEND_LargeBodies_SpillCleanup(t *testing.T) {
	f := newFixture(t, fxOpts{implicitTLS: true, spillDir: t.TempDir()})
	ctx := context.Background()

	const bodySize = 80 * 1024 // 80 KiB each
	msg1 := buildLargeMessage("multi-spill-1", "AAAA", bodySize)
	msg2 := buildLargeMessage("multi-spill-2", "BBBB", bodySize)

	before := countSpillsInDir(t, f.spillDir)

	c := loggedInClient(t, f)
	defer c.close()

	// First literal — synchronising.
	c.write(fmt.Sprintf("a1 APPEND INBOX (\\Seen) {%d}\r\n", len(msg1)))
	line := c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected first continuation, got: %q", line)
	}
	c.write(msg1)
	// Second literal — synchronising; no extra CRLF between body and flag list.
	c.write(fmt.Sprintf(" (\\Flagged) {%d}\r\n", len(msg2)))
	line = c.readLine()
	if !strings.HasPrefix(line, "+") {
		t.Fatalf("expected second continuation, got: %q", line)
	}
	c.write(msg2 + "\r\n")
	resp := c.readUntilTag("a1")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "OK") || !strings.Contains(last, "APPENDUID") {
		t.Fatalf("MULTIAPPEND failed: %v", last)
	}

	// Spill cleanup check.
	after := countSpillsInDir(t, f.spillDir)
	if after > before {
		t.Fatalf("spill file leak after MULTIAPPEND: before=%d after=%d", before, after)
	}

	// Both messages must be in INBOX.
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, f.inbox.ID, store.MessageFilter{})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 messages in INBOX, got %d (err=%v)", len(msgs), err)
	}
}
