package email_test

// internalize_pending_test.go covers REQ-EXTIMG-90/91/92 for the JMAP
// Email/import create path: an uploaded blob lands verbatim, exactly
// like the IMAP-mirror and Gmail Takeout importers, so it shares
// their on-demand flagging decision, factored into internal/extimg
// (re #446).
//
// Each test has a sqlite leg (always) and a postgres leg (skipped
// unless HEROLD_PG_DSN is set), following the setupFixture /
// setupFixturePostgres idiom already used by this package's other
// backend-agnostic tests (e.g. TestEmail_Query_AndOfTwoInMailbox_...).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/protojmap/mail/thread"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/testharness"
)

// importAndGetMessage performs an Email/import create with body and
// returns the resulting store.Message row.
func importAndGetMessage(t *testing.T, f *fixture, body string) store.Message {
	t.Helper()
	ref := f.putBlob(t, body)
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"new1": map[string]any{
				"blobId":     ref.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	mid, _ := resp.Created["new1"]["id"].(string)
	if mid == "" {
		t.Fatalf("created id missing: %v", resp.Created)
	}
	midU, err := strconv.ParseUint(mid, 10, 64)
	if err != nil {
		t.Fatalf("parse email id %q: %v", mid, err)
	}
	stored, err := f.srv.Store.Meta().GetMessage(context.Background(), store.MessageID(midU))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	return stored
}

const htmlBodyWithRemoteImage = "From: a@example.test\r\n" +
	"To: b@example.test\r\n" +
	"Subject: import-flag\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>hi</p><img src=\"https://example.test/tracker.png\"></body></html>\r\n"

const htmlBodyNoRemoteImage = "From: a@example.test\r\n" +
	"To: b@example.test\r\n" +
	"Subject: import-noflag\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<html><body><p>hi, no images here</p></body></html>\r\n"

// testEmail_Import_FlagsExternalHTMLImage is the shared body: verifies
// that Email/import flags an uploaded message whose HTML body carries
// an external http(s) image reference (REQ-EXTIMG-91).
func testEmail_Import_FlagsExternalHTMLImage(t *testing.T, f *fixture) {
	stored := importAndGetMessage(t, f, htmlBodyWithRemoteImage)
	if !stored.InternalizePending {
		t.Errorf("InternalizePending = false; want true for an imported message with an external HTML image reference")
	}
}

func TestEmail_Import_FlagsExternalHTMLImage(t *testing.T) {
	testEmail_Import_FlagsExternalHTMLImage(t, setupFixture(t))
}

func TestEmail_Import_FlagsExternalHTMLImage_Postgres(t *testing.T) {
	testEmail_Import_FlagsExternalHTMLImage(t, setupFixturePostgres(t))
}

// testEmail_Import_NoExternalImageNotFlagged is the shared body:
// verifies that plain HTML with no external image reference is not
// flagged.
func testEmail_Import_NoExternalImageNotFlagged(t *testing.T, f *fixture) {
	stored := importAndGetMessage(t, f, htmlBodyNoRemoteImage)
	if stored.InternalizePending {
		t.Errorf("InternalizePending = true; want false for an imported message with no external image reference")
	}
}

func TestEmail_Import_NoExternalImageNotFlagged(t *testing.T) {
	testEmail_Import_NoExternalImageNotFlagged(t, setupFixture(t))
}

func TestEmail_Import_NoExternalImageNotFlagged_Postgres(t *testing.T) {
	testEmail_Import_NoExternalImageNotFlagged(t, setupFixturePostgres(t))
}

// testEmail_Import_PassthroughModeSuppressesFlag is the shared body:
// verifies that [external_images] mode = "passthrough" suppresses the
// flag even when the body carries an external HTML image reference
// (REQ-EXTIMG-92).
func testEmail_Import_PassthroughModeSuppressesFlag(t *testing.T, f *fixture) {
	stored := importAndGetMessage(t, f, htmlBodyWithRemoteImage)
	if stored.InternalizePending {
		t.Errorf("InternalizePending = true; want false when [external_images] mode is \"passthrough\"")
	}
}

func TestEmail_Import_PassthroughModeSuppressesFlag(t *testing.T) {
	testEmail_Import_PassthroughModeSuppressesFlag(t, setupRetryFixture(t, extimg.Config{Mode: extimg.ModePassthrough}))
}

func TestEmail_Import_PassthroughModeSuppressesFlag_Postgres(t *testing.T) {
	testEmail_Import_PassthroughModeSuppressesFlag(t, setupRetryFixturePostgres(t, extimg.Config{Mode: extimg.ModePassthrough}))
}

// setupRetryFixturePostgres is setupRetryFixture's Postgres counterpart
// (retryimages_test.go), mirroring setupFixturePostgres's DSN-skip and
// unique-canonical-email handling: opened against HEROLD_PG_DSN instead
// of an on-disk SQLite file, wired with the given extimg.Config the way
// setupRetryFixture wires it for SQLite. Skips when HEROLD_PG_DSN is
// unset or the connection cannot be established.
func setupRetryFixturePostgres(t *testing.T, cfg extimg.Config) *fixture {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, _ := testharness.Start(t, testharness.Options{
		Store:     st,
		Clock:     clk,
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})

	ctx := context.Background()
	canonicalEmail := fmt.Sprintf("alice-retry-%d@example.test", time.Now().UnixNano())
	p, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: canonicalEmail,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plaintext := "hk_test_alice_" + fmt.Sprintf("%d", p.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	inbox, err := srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	mailbox.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)
	email.RegisterWithOptions(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock, email.RegisterOptions{
		ExtImg:   cfg,
		Hostname: "test.local",
	})
	thread.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)
	t.Cleanup(func() { email.WaitBackgroundWrites(jmapServ.Registry()) })

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{srv: srv, pid: p.ID, inbox: inbox, client: client, baseURL: base, apiKey: plaintext}
}
