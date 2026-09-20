package imapimport

// internalize_pending_test.go covers REQ-EXTIMG-90/91/92 for the
// IMAP-mirror import path: a message whose HTML body carries an
// external http(s) image reference is flagged with InternalizePending
// at ingest, honouring the operator's internalize-imports policy, the
// same decision (extimg.ShouldFlagOnDemand / extimg.HasExternalHTMLImage)
// the Gmail Takeout importer applies (internal/import/gmail).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/extimg/internalizeworker"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// buildRFC822HTML returns a minimal RFC 822 message with an HTML body.
// When withRemoteImage is true the body carries an <img src="http://...">
// reference; otherwise the body is plain HTML with no image at all.
func buildRFC822HTML(msgID, subject string, date time.Time, withRemoteImage bool) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "From: sender@example.test\r\n")
	fmt.Fprintf(&b, "To: recipient@example.test\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	if withRemoteImage {
		fmt.Fprintf(&b, "<html><body><p>%s</p><img src=\"https://example.test/tracker.png\"></body></html>\r\n", subject)
	} else {
		fmt.Fprintf(&b, "<html><body><p>%s, no images here</p></body></html>\r\n", subject)
	}
	return b.Bytes()
}

// TestIngestFlagsExternalHTMLImage verifies that an IMAP-mirrored
// message whose HTML body carries an external http(s) image reference
// lands with InternalizePending set (REQ-EXTIMG-91), the gap #446
// reports.
func TestIngestFlagsExternalHTMLImage(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("extimg1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822HTML("extimg-flag@test", "Order confirmation", d, true)
	appendToServer(t, ts, "extimg1", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "extimg1@example.test",
		username:            "extimg1",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "extimg-flag@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if !msg.InternalizePending {
		t.Errorf("InternalizePending = false; want true for a mirrored message with an external HTML image reference")
	}
}

// TestIngestNoExternalImageNotFlagged verifies that an IMAP-mirrored
// message whose HTML body carries no external image reference is not
// flagged: the substring scan must not false-positive on plain HTML.
func TestIngestNoExternalImageNotFlagged(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("extimg2", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822HTML("extimg-noflag@test", "Plain notice", d, false)
	appendToServer(t, ts, "extimg2", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "extimg2@example.test",
		username:            "extimg2",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "extimg-noflag@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if msg.InternalizePending {
		t.Errorf("InternalizePending = true; want false for a mirrored message with no external image reference")
	}
}

// TestIngestPolicyOffSuppressesFlag verifies that internalize_imports =
// "off" (mapped from [external_images] mode = "passthrough" via
// extimg.ImportPolicyFromMode) suppresses the flag even when the body
// carries an external HTML image reference (REQ-EXTIMG-92).
func TestIngestPolicyOffSuppressesFlag(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("extimg3", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	d := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822HTML("extimg-off@test", "Order confirmation", d, true)
	appendToServer(t, ts, "extimg3", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "extimg3@example.test",
		username:            "extimg3",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnceCfgSpamPolicy(t, ha, ts, acc, nil, nil, sysconfig.IMAPImportConfig{}, "off"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	msg, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "extimg-off@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader: %v", err)
	}
	if msg.InternalizePending {
		t.Errorf("InternalizePending = true; want false when internalize_imports policy is \"off\"")
	}
}

// TestFlaggedMirroredMessageInternalizesOnFirstRead is the end-to-end
// leg of REQ-EXTIMG-91/93: an IMAP-mirrored message flagged with
// InternalizePending at ingest is picked up by the on-demand
// internalizeworker, rewritten to embed the external image inline,
// and has its marker cleared -- exactly once.
func TestFlaggedMirroredMessageInternalizesOnFirstRead(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("extimg4", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(tinyPNGForTest())
	}))
	defer imgSrv.Close()

	d := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822HTMLWithImageURL("extimg-e2e@test", "Order confirmation", d, imgSrv.URL+"/tracker.png")
	appendToServer(t, ts, "extimg4", "pw", "INBOX", raw, nil, d)

	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "extimg4@example.test",
		username:            "extimg4",
		credentialPlaintext: "pw",
	}, nil)

	if err := runSyncOnce(t, ha, ts, acc, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	before, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "extimg-e2e@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (before): %v", err)
	}
	if !before.InternalizePending {
		t.Fatalf("precondition failed: InternalizePending = false after ingest")
	}

	u, err := url.Parse(imgSrv.URL)
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	var port int
	fmt.Sscanf(u.Port(), "%d", &port)
	cfg := extimg.Config{
		Mode:                extimg.ModeInternalize,
		MaxPerImageBytes:    5 << 20,
		MaxPerMessageImages: 100,
		MaxPerMessageBytes:  50 << 20,
		ConcurrentFetches:   4,
		RequireHTTPS:        false,
		AllowPrivate:        true,
		AllowedPorts:        []int{port},
		HostHeader:          "test.local",
	}
	w := internalizeworker.New(ha.Store, cfg, nil, ha.Clock, internalizeworker.Options{
		Concurrency: 1,
		BatchSize:   4,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(runCtx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := ha.Store.Meta().CountInternalizePending(context.Background(), acc.PrincipalID)
		if err != nil {
			t.Fatalf("CountInternalizePending: %v", err)
		}
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	after, err := ha.Store.Meta().GetMessageByMessageIDHeader(context.Background(), acc.PrincipalID, "extimg-e2e@test")
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (after): %v", err)
	}
	if after.InternalizePending {
		t.Fatalf("InternalizePending still set after the worker ran: marker was never cleared")
	}

	rc, err := ha.Store.Blobs().Get(context.Background(), after.Blob.Hash)
	if err != nil {
		t.Fatalf("Blobs.Get: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read rewritten blob: %v", err)
	}
	if bytes.Contains(body, []byte(imgSrv.URL)) {
		t.Errorf("rewritten body still references the external image URL %q; want it replaced with a cid: reference", imgSrv.URL)
	}
	if !bytes.Contains(body, []byte("cid:")) {
		t.Errorf("rewritten body has no cid: reference; the image was not inlined")
	}
}

// buildRFC822HTMLWithImageURL is buildRFC822HTML with an explicit
// image URL, used to point at an httptest.Server.
func buildRFC822HTMLWithImageURL(msgID, subject string, date time.Time, imgURL string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", msgID)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "From: sender@example.test\r\n")
	fmt.Fprintf(&b, "To: recipient@example.test\r\n")
	fmt.Fprintf(&b, "Date: %s\r\n", date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "<html><body><p>%s</p><img src=\"%s\"></body></html>\r\n", subject, imgURL)
	return b.Bytes()
}

// tinyPNGForTest returns a hand-assembled, valid 1x1 transparent PNG.
func tinyPNGForTest() []byte {
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
}
