package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/apps"
	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/appaction"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testfakes/fakeapp"
	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

var appsE2EDataKey = []byte("apps-e2e-test-data-key-32-bytes!")

// appsFlow is the wiring of one TestApps_EndToEnd backend leg: the admin
// REST surface, a JMAP server with the apps capability, and the fake app.
type appsFlow struct {
	t      *testing.T
	st     store.Store
	clk    *clock.FakeClock
	fake   *fakeapp.Server
	admin  *httptest.Server
	key    string
	alice  store.Principal
	thread string
	raws   [][]byte
}

func (f *appsFlow) adminDo(method, path string, body any) (int, []byte) {
	f.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.admin.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if f.key != "" {
		req.Header.Set("Authorization", "Bearer "+f.key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// newJMAP starts a JMAP server whose apps capability allows
// maxPayload bytes per invocation. Requests carrying the
// "e2e-session" cookie authenticate as that principal id through the
// cookie path, with the end-user scopes a Suite login carries.
func (f *appsFlow) newJMAP(maxPayload int64, timeout time.Duration) *httptest.Server {
	f.t.Helper()
	dir := directory.New(f.st.Meta(), nil, f.clk, nil)
	srv := protojmap.NewServer(f.st, dir, nil, nil, f.clk, protojmap.Options{
		DownloadRatePerSec: -1,
		SessionResolver: func(r *http.Request) (store.PrincipalID, auth.ScopeSet, bool) {
			c, err := r.Cookie("e2e-session")
			if err != nil {
				return 0, nil, false
			}
			n, err := strconv.ParseUint(c.Value, 10, 64)
			if err != nil {
				return 0, nil, false
			}
			return store.PrincipalID(n), auth.NewScopeSet(auth.AllEndUserScopes...), true
		},
	})
	appaction.Register(srv.Registry(), appaction.Options{
		Store: f.st,
		Client: apps.NewClient(apps.ClientOptions{
			Timeout:                timeout,
			AllowedInsecureOrigins: []string{f.fake.Origin()},
			UserAgent:              "herold/e2e",
		}),
		DataKey:         appsE2EDataKey,
		MaxPayloadBytes: maxPayload,
		InvokeTimeout:   timeout,
		Clock:           f.clk,
	})
	h := httptest.NewServer(srv.Handler())
	f.t.Cleanup(h.Close)
	return h
}

func (f *appsFlow) jmapGet(h *httptest.Server, path string) (*http.Response, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: "e2e-session", Value: strconv.FormatUint(uint64(f.alice.ID), 10)})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

// invoke calls AppAction/invoke for ext on the seeded thread and
// returns the method response name and arguments.
func (f *appsFlow) invoke(h *httptest.Server, appID, ext string) (string, map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"using": []string{"urn:ietf:params:jmap:core", string(appaction.Capability)},
		"methodCalls": []any{[]any{"AppAction/invoke", map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(f.alice.ID), "appId": appID,
			"extensionId": ext, "threadId": f.thread}, "c0"}},
	})
	req, _ := http.NewRequest(http.MethodPost, h.URL+"/jmap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "e2e-session", Value: strconv.FormatUint(uint64(f.alice.ID), 10)})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("POST /jmap: %v", err)
	}
	defer res.Body.Close()
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	raw, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(raw, &out); err != nil || len(out.MethodResponses) != 1 {
		f.t.Fatalf("JMAP response %d: %s", res.StatusCode, raw)
	}
	var name string
	var args map[string]any
	_ = json.Unmarshal(out.MethodResponses[0][0], &name)
	_ = json.Unmarshal(out.MethodResponses[0][1], &args)
	return name, args
}

// seedThread stores a two-message thread in alice's INBOX and returns
// its id and the raw messages in receivedAt order.
func (f *appsFlow) seedThread() {
	f.t.Helper()
	ctx := context.Background()
	boxes, err := f.st.Meta().ListMailboxes(ctx, f.alice.ID)
	if err != nil {
		f.t.Fatalf("ListMailboxes: %v", err)
	}
	var inbox store.MailboxID
	for _, mb := range boxes {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			inbox = mb.ID
		}
	}
	if inbox == 0 {
		f.t.Fatal("alice has no INBOX")
	}
	var key uint64
	for i, subj := range []string{"Old computers to give away", "Re: Old computers to give away"} {
		raw := []byte(fmt.Sprintf("From: dora@donor.example\r\nTo: %s\r\nSubject: %s\r\n"+
			"Message-ID: <e2e-apps-%d@donor.example>\r\n\r\nmessage %d of the thread\r\n",
			f.alice.CanonicalEmail, subj, i, i+1))
		ref, err := f.st.Blobs().Put(ctx, bytes.NewReader(raw))
		if err != nil {
			f.t.Fatalf("Put: %v", err)
		}
		at := f.clk.Now().Add(time.Duration(i-2) * time.Hour)
		if _, _, err := f.st.Meta().InsertMessage(ctx, store.Message{
			PrincipalID: f.alice.ID, InternalDate: at, ReceivedAt: at, Size: ref.Size, Blob: ref, ThreadID: key,
			Envelope: store.Envelope{Subject: subj, MessageID: fmt.Sprintf("e2e-apps-%d@donor.example", i)},
		}, []store.MessageMailbox{{MailboxID: inbox}}); err != nil {
			f.t.Fatalf("InsertMessage: %v", err)
		}
		f.raws = append(f.raws, raw)
		if i == 0 {
			msgs, err := f.st.Meta().ListMessages(ctx, inbox, store.MessageFilter{Limit: 10})
			if err != nil || len(msgs) != 1 {
				f.t.Fatalf("ListMessages: %v (%d)", err, len(msgs))
			}
			key = uint64(msgs[0].ID)
		}
	}
	f.thread = fmt.Sprintf("t%d", key)
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestApps_EndToEnd drives the whole apps flow against the in-tree fake
// app on both backends (REQ-APP-72, REQ-TEXP-10): the admin registers,
// approves and enables the app over REST; the thread downloads as a zip
// of its messages; AppAction/invoke hands the thread to the fake, which
// verifies the signature and receives the messages in order, and each
// of the fake's answers maps to its JMAP result: openUrl, notice,
// appRejected, appUnavailable (5xx and timeout) and tooLarge.
func TestApps_EndToEnd(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		f := &appsFlow{t: t, st: fixtures.Prepare(t, newStore), clk: fixtures.NewFakeClock()}
		f.clk.SetNow(time.Date(2026, 10, 13, 8, 12, 0, 0, time.UTC))
		f.fake = fakeapp.New(t, fakeapp.Options{SlowDelay: 10 * time.Second, Now: f.clk.Now})

		dir := directory.New(f.st.Meta(), nil, f.clk, nil)
		rp := directoryoidc.New(f.st.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, f.clk)
		adminSrv := protoadmin.NewServer(f.st, dir, rp, nil, f.clk, protoadmin.Options{
			BootstrapPerWindow: 10, BootstrapWindow: time.Minute, RequestsPerMinutePerKey: 10000,
			Apps: protoadmin.AppsOptions{
				Client:  apps.NewClient(apps.ClientOptions{AllowedInsecureOrigins: []string{f.fake.Origin()}}),
				DataKey: appsE2EDataKey,
			},
		})
		f.admin = httptest.NewServer(adminSrv.Handler())
		t.Cleanup(f.admin.Close)

		// Operator: bootstrap, create alice, register + approve + enable
		// the fake app (REQ-APP-10..13).
		status, body := f.adminDo("POST", "/api/v1/bootstrap", map[string]any{"email": "admin@apps.e2e.test"})
		if status != http.StatusCreated {
			t.Fatalf("bootstrap = %d: %s", status, body)
		}
		var boot struct {
			Key string `json:"initial_api_key"`
		}
		_ = json.Unmarshal(body, &boot)
		f.key = boot.Key
		status, body = f.adminDo("POST", "/api/v1/principals", map[string]any{
			"email": "alice@apps.e2e.test", "password": "correct-horse-battery-staple"})
		if status != http.StatusCreated {
			t.Fatalf("create alice = %d: %s", status, body)
		}
		var err error
		if f.alice, err = f.st.Meta().GetPrincipalByEmail(context.Background(), "alice@apps.e2e.test"); err != nil {
			t.Fatalf("GetPrincipalByEmail: %v", err)
		}
		status, body = f.adminDo("POST", "/api/v1/apps", map[string]any{"origin": f.fake.Origin()})
		if status != http.StatusCreated {
			t.Fatalf("register app = %d: %s", status, body)
		}
		var app struct {
			ID     uint64 `json:"id"`
			Secret string `json:"secret"`
			Digest string `json:"pending_manifest_digest"`
		}
		_ = json.Unmarshal(body, &app)
		f.fake.SetSecret(app.Secret)
		appID := strconv.FormatUint(app.ID, 10)
		if status, body = f.adminDo("POST", "/api/v1/apps/"+appID+"/approve",
			map[string]any{"manifest_digest": app.Digest}); status != http.StatusOK {
			t.Fatalf("approve = %d: %s", status, body)
		}
		if status, body = f.adminDo("PATCH", "/api/v1/apps/"+appID,
			map[string]any{"principals": []string{"alice@apps.e2e.test"}}); status != http.StatusOK {
			t.Fatalf("enable for alice = %d: %s", status, body)
		}
		f.seedThread()

		jmap := f.newJMAP(50<<20, 1500*time.Millisecond)

		// Thread download: one .eml per message, in order (REQ-TEXP-10/11).
		res, zipBody := f.jmapGet(jmap, fmt.Sprintf("/jmap/download-thread/%s/%s/thread",
			protojmap.AccountIDForPrincipal(f.alice.ID), f.thread))
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" {
			t.Fatalf("download-thread = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
		}
		zr, err := zip.NewReader(bytes.NewReader(zipBody), int64(len(zipBody)))
		if err != nil || len(zr.File) != 2 {
			t.Fatalf("zip: %v (%d entries)", err, len(zr.File))
		}
		for i, zf := range zr.File {
			rc, _ := zf.Open()
			got, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.HasPrefix(zf.Name, fmt.Sprintf("%d-2026-10-13-", i+1)) || !bytes.Equal(got, f.raws[i]) {
				t.Errorf("zip entry %d = %q (%d bytes)", i, zf.Name, len(got))
			}
		}

		// The session offers the fake's five actions (REQ-APP-50).
		_, sess := f.jmapGet(jmap, "/.well-known/jmap")
		var sd struct {
			Accounts map[string]struct {
				AccountCapabilities map[string]struct {
					Actions []struct {
						AppID, ExtensionID string
					} `json:"actions"`
				} `json:"accountCapabilities"`
			} `json:"accounts"`
		}
		_ = json.Unmarshal(sess, &sd)
		if acts := sd.Accounts[protojmap.AccountIDForPrincipal(f.alice.ID)].AccountCapabilities[string(appaction.Capability)].Actions; len(acts) != 5 {
			t.Fatalf("offered actions = %+v (session %s)", acts, sess)
		}

		// openUrl: a correctly signed request with both messages in order.
		name, args := f.invoke(jmap, appID, fakeapp.ExtOpenURL)
		if name != "AppAction/invoke" || args["type"] != "openUrl" ||
			!strings.HasPrefix(fmt.Sprint(args["url"]), f.fake.Origin()+"/opened/") {
			t.Fatalf("open-url = %s %v", name, args)
		}
		invs := f.fake.Invocations()
		if len(invs) != 1 {
			t.Fatalf("fake received %d invocations", len(invs))
		}
		inv := invs[0]
		if !inv.SignatureValid || inv.PartCount != 2 || inv.AppAPI != "1" || inv.ContextType != "application/json" {
			t.Fatalf("invocation = %+v", inv)
		}
		for i, p := range inv.Parts {
			if p.Name != "message[]" || p.FileName != fmt.Sprintf("%02d.eml", i+1) ||
				p.ContentType != "message/rfc822" || p.SHA256 != sha(f.raws[i]) {
				t.Errorf("part %d = %+v", i, p)
			}
		}
		var ctxDoc struct {
			HeroldApp int    `json:"herold_app"`
			AppKey    string `json:"app_key"`
			Ext       string `json:"extension_id"`
			User      struct{ Email string }
			Thread    struct {
				ID           string
				Subject      string
				MessageCount int `json:"message_count"`
			}
			Messages []struct{ Index int }
		}
		_ = json.Unmarshal(inv.Context, &ctxDoc)
		if ctxDoc.HeroldApp != 1 || ctxDoc.AppKey != fakeapp.DefaultAppKey || ctxDoc.Ext != fakeapp.ExtOpenURL ||
			ctxDoc.User.Email != "alice@apps.e2e.test" || ctxDoc.Thread.ID != f.thread ||
			ctxDoc.Thread.Subject != "Old computers to give away" || ctxDoc.Thread.MessageCount != 2 ||
			len(ctxDoc.Messages) != 2 || ctxDoc.Messages[0].Index != 1 {
			t.Fatalf("context = %s", inv.Context)
		}
		page, err := http.Get(fmt.Sprint(args["url"]))
		if err != nil {
			t.Fatalf("GET opened page: %v", err)
		}
		pb, _ := io.ReadAll(page.Body)
		page.Body.Close()
		if !strings.Contains(string(pb), "Received 2 messages") {
			t.Fatalf("opened page = %s", pb)
		}

		// notice
		if _, args = f.invoke(jmap, appID, fakeapp.ExtNotice); args["type"] != "notice" ||
			args["text"] != "Fake app received 2 messages." {
			t.Errorf("notice = %v", args)
		}
		// appRejected carries the app's text.
		if name, args = f.invoke(jmap, appID, fakeapp.ExtReject); name != "error" || args["type"] != "appRejected" ||
			args["description"] != fakeapp.RejectText {
			t.Errorf("reject = %s %v", name, args)
		}
		// appUnavailable: 5xx without a body, and a response past the
		// invoke timeout.
		if _, args = f.invoke(jmap, appID, fakeapp.ExtFail); args["type"] != "appUnavailable" {
			t.Errorf("fail = %v", args)
		}
		start := time.Now()
		if _, args = f.invoke(jmap, appID, fakeapp.ExtSlow); args["type"] != "appUnavailable" {
			t.Errorf("slow = %v", args)
		}
		if d := time.Since(start); d > 8*time.Second {
			t.Errorf("slow invocation took %s, want about the 1.5 s invoke timeout", d)
		}
		for _, inv := range f.fake.Invocations() {
			if !inv.SignatureValid {
				t.Errorf("fake saw an invalid signature on %s: %s", inv.Extension, inv.SignatureError)
			}
		}

		// tooLarge: refused before any byte reaches the app.
		small := f.newJMAP(int64(len(f.raws[0])), 1500*time.Millisecond)
		before := len(f.fake.Invocations())
		if _, args = f.invoke(small, appID, fakeapp.ExtNotice); args["type"] != "tooLarge" {
			t.Errorf("oversized thread = %v", args)
		}
		if after := len(f.fake.Invocations()); after != before {
			t.Errorf("tooLarge invocation reached the app (%d -> %d)", before, after)
		}

		// Audit and last_invoked_at (REQ-APP-45).
		entries, err := f.st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "app.invoke"})
		if err != nil || len(entries) != 6 {
			t.Fatalf("app.invoke audit rows = %d, %v", len(entries), err)
		}
		stored, _ := f.st.Meta().GetApp(context.Background(), store.AppID(app.ID))
		if stored.LastInvokedAt.IsZero() {
			t.Error("last_invoked_at not set after successful invocations")
		}
	})
}
