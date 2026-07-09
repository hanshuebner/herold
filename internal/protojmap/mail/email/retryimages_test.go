package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness"
)

// retryFixture mirrors setupFixture but wires extimg.RegisterOptions
// with a Config permissive enough to fetch loopback test servers
// (AllowPrivate), so Email/retryImages can exercise a real retry
// against the in-tree failing-fetch / fake-image-host harness (issue
// #162 grounding requirement).
func setupRetryFixture(t *testing.T, cfg extimg.Config) *fixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.Open(context.Background(), dbPath, nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, _ := testharness.Start(t, testharness.Options{
		Store:     st,
		Clock:     clk,
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})

	ctx := context.Background()
	p, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
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

// closedLoopbackURL reserves a loopback port, closes it immediately,
// and returns a URL pointing at it -- a deterministic connection-
// refused fetch target.
func closedLoopbackURL(t *testing.T) (string, *net.TCPAddr) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	return fmt.Sprintf("http://127.0.0.1:%d/missing.png", addr.Port), addr
}

func pngBytes() []byte {
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

// seedFailedImageMessage runs extimg.Internalize against a message
// whose only image fails to fetch (closed loopback port), inserts the
// resulting (placeholdered) body plus the retained failed-image
// state, and returns the message id + the origin URL.
func seedFailedImageMessage(t *testing.T, f *fixture, cfg extimg.Config, imgURL string) store.MessageID {
	t.Helper()
	html := `<html><body>Hello.<img src="` + imgURL + `"></body></html>`
	raw := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Hi\r\n" +
		"Content-Type: text/html; charset=\"utf-8\"\r\n\r\n" + html

	out, sum, err := extimg.Internalize(context.Background(), []byte(raw), cfg, extimg.DKIMVerdict{})
	if err != nil {
		t.Fatalf("Internalize: %v", err)
	}
	if sum.Failed != 1 || len(sum.FailedURLs) != 1 {
		t.Fatalf("expected exactly one failed URL, got sum=%+v", sum)
	}
	encoded, err := extimg.EncodeRetainedState(extimg.RetainedState{
		URLs:     sum.FailedURLs,
		Template: sum.FailedImageTemplate,
	})
	if err != nil {
		t.Fatalf("EncodeRetainedState: %v", err)
	}

	ref := f.putBlob(t, string(out))
	now := f.srv.Clock.Now()
	msg := store.Message{
		InternalDate:     now,
		ReceivedAt:       now,
		Size:             ref.Size,
		Blob:             ref,
		FailedImageCount: len(sum.FailedURLs),
		FailedImageState: encoded,
		Envelope: store.Envelope{
			Subject: "Hi",
			From:    "sender@example.test",
			To:      "rcpt@example.test",
			Date:    now,
		},
	}
	if _, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg,
		[]store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	return mostRecentMessageID(t, f)
}

// TestEmailRetryImages_NoFailedImages_NoOp covers the common case: a
// message that never had a failed image reports failedImageCount=0
// and retriedCount=0, no-op.
func TestEmailRetryImages_NoFailedImages_NoOp(t *testing.T) {
	f := setupRetryFixture(t, extimg.Config{Mode: extimg.ModePassthrough})
	m := f.insertMessage(t, "From: a@x\r\nTo: b@x\r\n\r\nbody", "hi", "a@x", "b@x", nil, "")

	name, raw := f.invoke(t, "Email/retryImages", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"id":        fmt.Sprintf("%d", m.ID),
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailImageRetry)
	if name != "Email/retryImages" {
		t.Fatalf("got response %q: %s", name, raw)
	}
	var resp struct {
		RetriedCount     int `json:"retriedCount"`
		FailedImageCount int `json:"failedImageCount"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if resp.RetriedCount != 0 || resp.FailedImageCount != 0 {
		t.Fatalf("expected no-op, got %+v", resp)
	}
}

// TestEmailRetryImages_Succeeds covers grounding requirement (b): a
// message with one retained failed image is retried after the origin
// comes back up (the SAME URL now serves a real image via the
// listen-close-relisten harness trick). Asserts retriedCount=1,
// failedImageCount=0, and -- requirement (d) -- that the origin URL
// is absent from the raw Email/get JSON afterward.
func TestEmailRetryImages_Succeeds(t *testing.T) {
	cfg := extimg.Config{
		Mode:                   extimg.ModeInternalize,
		MaxPerImageBytes:       5 << 20,
		MaxPerMessageImages:    100,
		MaxPerMessageBytes:     50 << 20,
		ConcurrentFetches:      4,
		RequireHTTPS:           false,
		AllowPrivate:           true,
		PerImageConnectTimeout: 200 * time.Millisecond,
		PerImageTotalTimeout:   500 * time.Millisecond,
		PerMessageTimeout:      2 * time.Second,
		HostHeader:             "test.local",
	}
	// Reserve the port (and allowlist it) BEFORE registering the JMAP
	// handlers: the retry handler captures its extimg.Config at
	// registration time, so the allowlisted port must be known first.
	imgURL, addr := closedLoopbackURL(t)
	cfg.AllowedPorts = []int{addr.Port}
	f := setupRetryFixture(t, cfg)
	id := seedFailedImageMessage(t, f, cfg, imgURL)

	// Bring the origin back up on the SAME port with a real image.
	l2, err := net.Listen("tcp", addr.String())
	if err != nil {
		t.Fatalf("relisten: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes())
	}))
	srv.Listener.Close()
	srv.Listener = l2
	srv.Start()
	defer srv.Close()

	name, raw := f.invoke(t, "Email/retryImages", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"id":        fmt.Sprintf("%d", id),
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailImageRetry)
	if name != "Email/retryImages" {
		t.Fatalf("got response %q: %s", name, raw)
	}
	var resp struct {
		RetriedCount     int    `json:"retriedCount"`
		FailedImageCount int    `json:"failedImageCount"`
		NewState         string `json:"newState"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if resp.RetriedCount != 1 {
		t.Fatalf("RetriedCount = %d, want 1: %s", resp.RetriedCount, raw)
	}
	if resp.FailedImageCount != 0 {
		t.Fatalf("FailedImageCount = %d, want 0: %s", resp.FailedImageCount, raw)
	}
	if resp.NewState == "" {
		t.Fatalf("NewState must be non-empty")
	}

	// Requirement (d): fetch the full Email/get response (properties
	// + bodyValues) and assert the origin URL is absent everywhere,
	// and the failedImageCount badge cleared.
	_, getRaw := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{fmt.Sprintf("%d", id)},
		"fetchHTMLBodyValues": true,
	})
	if strings.Contains(string(getRaw), imgURL) {
		t.Fatalf("origin URL leaked into Email/get response: %s", getRaw)
	}
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &getResp); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, getRaw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(getResp.List), getRaw)
	}
	if fc, ok := getResp.List[0]["failedImageCount"]; ok && fc != nil {
		t.Fatalf("failedImageCount should be omitted (0) after full resolution, got %v", fc)
	}
	if strings.Contains(string(getRaw), extimg.PlaceholderDataURI) {
		t.Fatalf("placeholder should have been replaced by the real image: %s", getRaw)
	}
	if !strings.Contains(string(getRaw), "cid:") {
		t.Fatalf("expected a cid: reference for the newly-internalized image: %s", getRaw)
	}
}

// TestEmailRetryImages_StillFailing covers grounding requirement (c):
// retrying without the origin coming back up leaves the count
// unchanged and never leaks the URL into the response.
func TestEmailRetryImages_StillFailing(t *testing.T) {
	cfg := extimg.Config{
		Mode:                   extimg.ModeInternalize,
		MaxPerImageBytes:       5 << 20,
		MaxPerMessageImages:    100,
		MaxPerMessageBytes:     50 << 20,
		ConcurrentFetches:      4,
		RequireHTTPS:           false,
		AllowPrivate:           true,
		PerImageConnectTimeout: 200 * time.Millisecond,
		PerImageTotalTimeout:   500 * time.Millisecond,
		PerMessageTimeout:      2 * time.Second,
		HostHeader:             "test.local",
	}
	imgURL, addr := closedLoopbackURL(t)
	cfg.AllowedPorts = []int{addr.Port}
	f := setupRetryFixture(t, cfg)
	id := seedFailedImageMessage(t, f, cfg, imgURL)

	name, raw := f.invoke(t, "Email/retryImages", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"id":        fmt.Sprintf("%d", id),
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailImageRetry)
	if name != "Email/retryImages" {
		t.Fatalf("got response %q: %s", name, raw)
	}
	if strings.Contains(string(raw), imgURL) {
		t.Fatalf("origin URL leaked into Email/retryImages response: %s", raw)
	}
	var resp struct {
		RetriedCount     int `json:"retriedCount"`
		FailedImageCount int `json:"failedImageCount"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if resp.RetriedCount != 0 {
		t.Fatalf("RetriedCount = %d, want 0: %s", resp.RetriedCount, raw)
	}
	if resp.FailedImageCount != 1 {
		t.Fatalf("FailedImageCount = %d, want 1 (unchanged): %s", resp.FailedImageCount, raw)
	}

	got, err := f.srv.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.FailedImageCount != 1 || got.FailedImageState == "" {
		t.Fatalf("retained state should be untouched: count=%d state=%q", got.FailedImageCount, got.FailedImageState)
	}

	// A second Email/get must still show the badge and no leak.
	_, getRaw := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{fmt.Sprintf("%d", id)},
		"fetchHTMLBodyValues": true,
	})
	if strings.Contains(string(getRaw), imgURL) {
		t.Fatalf("origin URL leaked into Email/get response: %s", getRaw)
	}
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw, &getResp); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, getRaw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(getResp.List), getRaw)
	}
	fc, _ := getResp.List[0]["failedImageCount"].(float64)
	if int(fc) != 1 {
		t.Fatalf("failedImageCount = %v, want 1: %s", getResp.List[0]["failedImageCount"], getRaw)
	}
}

// TestEmailRetryImages_CapabilityAdvertised proves the vendor
// capability descriptor is present on the session so clients can
// detect the retry affordance before offering it.
func TestEmailRetryImages_CapabilityAdvertised(t *testing.T) {
	f := setupRetryFixture(t, extimg.Config{Mode: extimg.ModePassthrough})
	req, err := http.NewRequest(http.MethodGet, f.baseURL+"/.well-known/jmap", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var session struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if _, ok := session.Capabilities["https://netzhansa.com/jmap/email-image-retry"]; !ok {
		t.Fatalf("capability not advertised: %v", session.Capabilities)
	}
}
