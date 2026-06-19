package protosmtp_test

// spill_test.go tests the Phase-1 spill-file streaming introduced by
// REQ-STORE-17/18: the inbound message body is written to a temp file as
// it arrives instead of accumulating in a []byte.
//
// Three properties are verified:
//
//  1. A large message (body > the test's small MaxMessageSize cap) is
//     rejected with 552 without ever assembling the body in RAM.
//
//  2. A normal-sized message round-trips byte-identical through the blob
//     store: the bytes read back from Blobs.Get contain the body text
//     verbatim (header-prefix + body, no corruption from the spill path).
//
//  3. The spill temp file is removed after (a) a successful delivery,
//     (b) a size-limit rejection, and (c) an explicit RSET.
//     Verified by counting files matching the herold-smtp-spill-* pattern
//     in os.TempDir() before and after each operation.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
)

// countSpillFiles returns the number of herold-smtp-spill-* files currently
// present in os.TempDir(). This is the observable footprint of in-flight
// or leaked spill files.
func countSpillFiles(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "herold-smtp-spill-*"))
	if err != nil {
		t.Fatalf("glob spill files: %v", err)
	}
	return len(matches)
}

// newSpillFixture builds a relay-in SMTP server with a small MaxMessageSize
// (spillTestMaxSize) so that over-limit behaviour can be exercised without
// allocating large buffers in the test.
const spillTestMaxSize = 4096

func newSpillFixture(t *testing.T) *fixture {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &fakePluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)
	interp := sieve.NewInterpreter()
	tlsStore, _ := newTestTLSStore(t)

	srv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      dkimV,
		SPF:       spfV,
		DMARC:     dmarcV,
		ARC:       arcV,
		Spam:      spamCls,
		Sieve:     interp,
		TLS:       tlsStore,
		Resolver:  resolver,
		Clock:     ha.Clock,
		Logger:    ha.Logger,
		Options: protosmtp.Options{
			Hostname:                 "mx.example.test",
			AuthservID:               "mx.example.test",
			MaxMessageSize:           spillTestMaxSize,
			ReadTimeout:              5 * time.Second,
			WriteTimeout:             5 * time.Second,
			DataTimeout:              10 * time.Second,
			ShutdownGrace:            2 * time.Second,
			MaxRecipientsPerMessage:  5,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: 32,
			MaxConcurrentPerIP:       16,
		},
	})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)
	return &fixture{
		ha:        ha,
		srv:       srv,
		listener:  "smtp",
		mode:      protosmtp.RelayIn,
		principal: pid,
		password:  "correct-horse-staple-battery",
	}
}

// smtpEnvelope sets up MAIL FROM + RCPT TO and returns without starting DATA.
func smtpEnvelope(t *testing.T, cli *smtpClient) {
	t.Helper()
	cli.send(t, "EHLO spill-test.example")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
}

// TestSpill_BlobRoundTrip verifies that the body written to the spill
// file and then promoted to the blob store is byte-identical to what was
// sent over the wire (property a). The CRLF normalisation and dot-
// unstuffing must both be preserved in the blob.
func TestSpill_BlobRoundTrip(t *testing.T) {
	f := newSpillFixture(t)
	cli, close := f.dial(t)
	defer close()
	mustOK(t, cli, 220)
	smtpEnvelope(t, cli)

	// Body with a dot-stuffed line ("..stuffed" on the wire => ".stuffed" stored)
	// and non-ASCII UTF-8 content to confirm 8BITMIME / SMTPUTF8 safety.
	const bodyText = "hello from the spill test — unicode works\r\n"
	rawBody := "From: bob@sender.test\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: spill round-trip\r\n" +
		"\r\n" +
		bodyText +
		".stuffed line\r\n"

	// Dot-stuff for the wire: lines starting with '.' get an extra '.'.
	// Split without a trailing CRLF so we don't generate a spurious
	// empty line that would add an extra \r\n to the stored body.
	trimmed := strings.TrimRight(rawBody, "\r\n")
	var wire strings.Builder
	for _, line := range strings.Split(trimmed, "\r\n") {
		if strings.HasPrefix(line, ".") {
			wire.WriteString(".")
		}
		wire.WriteString(line)
		wire.WriteString("\r\n")
	}

	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	cli.sendRaw(t, []byte(wire.String()))
	cli.sendRaw(t, []byte(".\r\n")) // terminator
	code, _ := cli.readReply(t)
	if code != 250 {
		t.Fatalf("DATA: expected 250, got %d", code)
	}
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	// Retrieve the stored blob and verify it contains the body text verbatim.
	ctx := context.Background()
	mbs, err := f.ha.Store.Meta().ListMailboxes(ctx, f.principal)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	var mbID store.MailboxID
	for _, mb := range mbs {
		if strings.EqualFold(mb.Name, "INBOX") {
			mbID = mb.ID
			break
		}
	}
	if mbID == 0 {
		t.Fatal("INBOX not found")
	}
	var blobHash string
	for mid := store.MessageID(1); mid < 200; mid++ {
		m, err := f.ha.Store.Meta().GetMessage(ctx, mid)
		if err != nil {
			continue
		}
		if m.MailboxID != mbID {
			continue
		}
		blobHash = m.Blob.Hash
		break
	}
	if blobHash == "" {
		t.Fatal("no message in INBOX")
	}

	br, err := f.ha.Store.Blobs().Get(ctx, blobHash)
	if err != nil {
		t.Fatalf("blobs.Get: %v", err)
	}
	defer br.Close()
	stored, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	// Blob must contain the body text.
	if !bytes.Contains(stored, []byte(bodyText)) {
		t.Errorf("blob does not contain body text\ngot:  %q\nwant: %q", stored, bodyText)
	}
	// Dot-unstuffing: ".stuffed line" must appear without extra dot.
	if !bytes.Contains(stored, []byte(".stuffed line")) {
		t.Errorf("dot-unstuffed line missing in blob")
	}
	if bytes.Contains(stored, []byte("..stuffed line")) {
		t.Errorf("blob still has double-dot (dot-unstuffing failed)")
	}
	// Received: header prepended correctly.
	if !bytes.HasPrefix(stored, []byte("Received:")) {
		t.Errorf("blob does not start with Received: header; got prefix: %q", stored[:min(40, len(stored))])
	}
}

// TestSpill_SizeRejectCleansUp verifies that sending a message body larger
// than MaxMessageSize yields a 552 reply AND leaves no lingering spill file
// (property b — rejection path).
func TestSpill_SizeRejectCleansUp(t *testing.T) {
	f := newSpillFixture(t)
	before := countSpillFiles(t)

	cli, close := f.dial(t)
	defer close()
	mustOK(t, cli, 220)
	smtpEnvelope(t, cli)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)

	// Send slightly more than spillTestMaxSize bytes + terminator.
	// The extra header means just a few hundred bytes of overhead,
	// so spillTestMaxSize+1024 is well over the cap.
	bigBody := fmt.Sprintf("Subject: too-big\r\n\r\n%s\r\n.\r\n",
		strings.Repeat("x", spillTestMaxSize+1024))
	cli.sendRaw(t, []byte(bigBody))
	code, _ := cli.readReply(t)
	if code != 552 {
		t.Fatalf("expected 552 (message too large), got %d", code)
	}

	// Give the server a moment to run resetEnvelope (it's synchronous in
	// the command loop, so after we got the reply it's already done).
	after := countSpillFiles(t)
	if after > before {
		t.Errorf("spill file leak on size rejection: before=%d after=%d", before, after)
	}
}

// TestSpill_SuccessDeliveryCleansUp verifies that after a successful 250,
// no spill file remains in os.TempDir() (property b — success path).
func TestSpill_SuccessDeliveryCleansUp(t *testing.T) {
	f := newSpillFixture(t)
	before := countSpillFiles(t)

	cli, close := f.dial(t)
	defer close()
	mustOK(t, cli, 220)
	smtpEnvelope(t, cli)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: bob@sender.test\r\nTo: alice@example.test\r\nSubject: spill-cleanup\r\n\r\nhello\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	code, _ := cli.readReply(t)
	if code != 250 {
		t.Fatalf("expected 250, got %d", code)
	}

	after := countSpillFiles(t)
	if after > before {
		t.Errorf("spill file leak on successful delivery: before=%d after=%d", before, after)
	}
}

// TestSpill_RSETCleansUp verifies that an RSET mid-transaction (after DATA
// started but before LAST) removes the spill file (property b — RSET path).
// We use the BDAT multi-chunk path to accumulate partial data before RSET.
func TestSpill_RSETCleansUp(t *testing.T) {
	f := newSpillFixture(t)
	before := countSpillFiles(t)

	cli, close := f.dial(t)
	defer close()
	mustOK(t, cli, 220)
	smtpEnvelope(t, cli)

	// Send a partial BDAT chunk (not LAST) to create a spill file.
	chunk := "From: bob@sender.test\r\nTo: alice@example.test\r\n"
	cli.send(t, fmt.Sprintf("BDAT %d", len(chunk)))
	cli.sendRaw(t, []byte(chunk))
	mustOK(t, cli, 250) // non-LAST chunk reply

	// Now RSET: should discard the in-flight body and clean up spill.
	cli.send(t, "RSET")
	mustOK(t, cli, 250)

	after := countSpillFiles(t)
	if after > before {
		t.Errorf("spill file leak on RSET: before=%d after=%d", before, after)
	}
}

// TestSpill_DATA_BDAT_SameParser verifies REQ-PROTO-08: the DATA and BDAT
// receive paths produce byte-identical stored blobs for the same input body.
func TestSpill_DATA_BDAT_SameParser(t *testing.T) {
	// We need two distinct principals so the two deliveries don't
	// fight over the same INBOX.
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "smtp", Protocol: "smtp"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid1, err := dir.CreatePrincipal(ctx, "alice@example.test", "correct-horse-staple-battery")
	if err != nil {
		t.Fatalf("CreatePrincipal alice: %v", err)
	}
	pid2, err := dir.CreatePrincipal(ctx, "bob@example.test", "correct-horse-staple-battery-2")
	if err != nil {
		t.Fatalf("CreatePrincipal bob: %v", err)
	}

	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &fakePluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := newResolverAdapter(ha.DNS)
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)
	interp := sieve.NewInterpreter()
	tlsStore, _ := newTestTLSStore(t)

	srv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      dkimV,
		SPF:       spfV,
		DMARC:     dmarcV,
		ARC:       arcV,
		Spam:      spamCls,
		Sieve:     interp,
		TLS:       tlsStore,
		Resolver:  resolver,
		Clock:     ha.Clock,
		Logger:    ha.Logger,
		Options: protosmtp.Options{
			Hostname:                 "mx.example.test",
			AuthservID:               "mx.example.test",
			MaxMessageSize:           65536,
			ReadTimeout:              5 * time.Second,
			WriteTimeout:             5 * time.Second,
			DataTimeout:              10 * time.Second,
			ShutdownGrace:            2 * time.Second,
			MaxRecipientsPerMessage:  5,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: 32,
			MaxConcurrentPerIP:       16,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	ha.AttachSMTP("smtp", srv, protosmtp.RelayIn)

	f := &fixture{ha: ha, srv: srv, listener: "smtp", mode: protosmtp.RelayIn, principal: pid1}

	// Fixed body. Same bytes will be sent via DATA and BDAT.
	rawBody := "From: sender@sender.test\r\nTo: alice@example.test\r\nSubject: parser-equiv\r\n\r\nBody text.\r\n"

	// --- DATA path (deliver to alice) ---
	cli1, close1 := f.dial(t)
	defer close1()
	mustOK(t, cli1, 220)
	cli1.send(t, "EHLO client.example.test")
	mustOK(t, cli1, 250)
	cli1.send(t, "MAIL FROM:<sender@sender.test>")
	mustOK(t, cli1, 250)
	cli1.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli1, 250)
	cli1.send(t, "DATA")
	mustOK(t, cli1, 354)
	// Dot-stuff and send. Strip the trailing CRLF before splitting so we
	// don't produce an extra empty line at the end (which would add a
	// spurious \r\n to the stored body).
	trimmedBody := strings.TrimRight(rawBody, "\r\n")
	var dotStuffed strings.Builder
	for _, line := range strings.Split(trimmedBody, "\r\n") {
		if strings.HasPrefix(line, ".") {
			dotStuffed.WriteString(".")
		}
		dotStuffed.WriteString(line)
		dotStuffed.WriteString("\r\n")
	}
	cli1.sendRaw(t, []byte(dotStuffed.String()))
	cli1.sendRaw(t, []byte(".\r\n"))
	if code, text := cli1.readReply(t); code != 250 {
		t.Fatalf("DATA 250: %d %s", code, text)
	}

	// --- BDAT path (deliver to bob, SAME body bytes) ---
	cli2, close2 := f.dial(t)
	defer close2()
	mustOK(t, cli2, 220)
	cli2.send(t, "EHLO client.example.test")
	mustOK(t, cli2, 250)
	cli2.send(t, "MAIL FROM:<sender@sender.test>")
	mustOK(t, cli2, 250)
	cli2.send(t, "RCPT TO:<bob@example.test>")
	mustOK(t, cli2, 250)
	cli2.send(t, fmt.Sprintf("BDAT %d LAST", len(rawBody)))
	cli2.sendRaw(t, []byte(rawBody))
	if code, text := cli2.readReply(t); code != 250 {
		t.Fatalf("BDAT LAST 250: %d %s", code, text)
	}

	// Extract stored blobs for both. Strip the Received: + Authentication-Results
	// prefix (which differs per delivery) and compare the body suffix.
	extractBody := func(pid store.PrincipalID) []byte {
		t.Helper()
		mbs, _ := ha.Store.Meta().ListMailboxes(ctx, pid)
		for _, mb := range mbs {
			if !strings.EqualFold(mb.Name, "INBOX") {
				continue
			}
			for mid := store.MessageID(1); mid < 500; mid++ {
				m, err := ha.Store.Meta().GetMessage(ctx, mid)
				if err != nil || m.MailboxID != mb.ID {
					continue
				}
				br, err := ha.Store.Blobs().Get(ctx, m.Blob.Hash)
				if err != nil {
					t.Fatalf("blobs.Get: %v", err)
				}
				full, _ := io.ReadAll(br)
				_ = br.Close()
				// Find double-CRLF (end of headers) and return only body.
				if i := bytes.Index(full, []byte("\r\n\r\n")); i >= 0 {
					return full[i+4:]
				}
				return full
			}
		}
		t.Fatalf("no message in INBOX for principal %d", pid)
		return nil
	}

	aliceBody := extractBody(pid1)
	bobBody := extractBody(pid2)

	if !bytes.Equal(aliceBody, bobBody) {
		t.Errorf("DATA and BDAT produced different body bytes\nDATA: %q\nBDAT: %q",
			aliceBody, bobBody)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
