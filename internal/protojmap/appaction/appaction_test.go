package appaction_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/apps"
	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/appaction"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

var dataKey = []byte("appaction-test-data-key-32bytes!")

const appSecret = "presented-secret"

// recordingApp is an app origin that records invocations and answers
// with a configurable response.
type recordingApp struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	reply   string
	status  int
	srv     *httptest.Server
	origin  string
}

func newRecordingApp(t *testing.T) *recordingApp {
	a := &recordingApp{status: 200}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		a.bodies = append(a.bodies, b)
		a.headers = append(a.headers, r.Header.Clone())
		reply, status := a.reply, a.status
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(a.srv.Close)
	a.origin, _ = sysconfig.NormalizeOrigin(a.srv.URL)
	return a
}

type fixture struct {
	t      *testing.T
	st     store.Store
	httpd  *httptest.Server
	alice  store.Principal
	bob    store.Principal
	key    string
	app    store.App
	thread string
	inbox  store.MailboxID
	rec    *recordingApp
}

func forEachBackend(t *testing.T, fn func(t *testing.T, st store.Store, clk *clock.FakeClock)) {
	t.Run("sqlite", func(t *testing.T) {
		clk := clock.NewFake(time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC))
		st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), nil, clk)
		if err != nil {
			t.Fatalf("storesqlite.Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		fn(t, st, clk)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("HEROLD_PG_DSN")
		if dsn == "" {
			t.Skip("HEROLD_PG_DSN not set")
		}
		clk := clock.NewFake(time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC))
		st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
		if err != nil {
			t.Fatalf("storepg.Open: %v", err)
		}
		if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
			_ = tr.TruncateAll(context.Background())
		}
		t.Cleanup(func() { _ = st.Close() })
		fn(t, st, clk)
	})
}

const manifestTmpl = `{"herold_app":1,"app_key":"test.appaction","name":{"en":"Forum"},
 "extensions":[{"point":"thread.action","id":"offer","label":{"en":"Create offer","de":"Angebot"},"icon":"gift",
 "scopes":[%s],"endpoint":"/hook"}]}`

func newFixture(t *testing.T, st store.Store, clk *clock.FakeClock, maxPayload int64, scopes string) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{t: t, st: st, rec: newRecordingApp(t)}
	mk := func(email string) store.Principal {
		p, err := st.Meta().InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser,
			CanonicalEmail: email, DisplayName: strings.Split(email, "@")[0]})
		if err != nil {
			t.Fatalf("InsertPrincipal: %v", err)
		}
		return p
	}
	f.alice = mk("alice@example.test")
	f.bob = mk("bob@example.test")
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: f.alice.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	f.inbox = mb.ID
	f.thread = f.seedThread(clk)

	m, _, err := apps.ParseManifest([]byte(fmt.Sprintf(manifestTmpl, scopes)))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	canon, digest := m.Canonical()
	sealed, err := secrets.Seal(dataKey, []byte(appSecret))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	app, err := st.Meta().InsertApp(ctx, store.App{AppKey: m.AppKey, Origin: f.rec.origin,
		PendingManifestJSON: canon, PendingManifestDigest: digest, SecretSealed: sealed})
	if err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	if f.app, err = st.Meta().ApproveAppManifest(ctx, app.ID, digest); err != nil {
		t.Fatalf("ApproveAppManifest: %v", err)
	}
	if err := st.Meta().SetAppPrincipals(ctx, app.ID, []store.PrincipalID{f.alice.ID}); err != nil {
		t.Fatalf("SetAppPrincipals: %v", err)
	}

	plaintext := "hk_appaction_test_key"
	sum := sha256.Sum256([]byte(plaintext))
	if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{PrincipalID: f.alice.ID,
		Hash: hex.EncodeToString(sum[:]), Name: "t", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	f.key = plaintext

	dir := directory.New(st.Meta(), nil, clk, nil)
	srv := protojmap.NewServer(st, dir, nil, nil, clk, protojmap.Options{
		DownloadRatePerSec: -1,
		SessionResolver: func(r *http.Request) (store.PrincipalID, auth.ScopeSet, bool) {
			c, err := r.Cookie("test-session")
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
		Store:           st,
		Client:          apps.NewClient(apps.ClientOptions{Timeout: 2 * time.Second, AllowedInsecureOrigins: []string{f.rec.origin}}),
		DataKey:         dataKey,
		MaxPayloadBytes: maxPayload,
		InvokeTimeout:   2 * time.Second,
		Clock:           clk,
	})
	f.httpd = httptest.NewServer(srv.Handler())
	t.Cleanup(f.httpd.Close)
	return f
}

func (f *fixture) seedThread(clk *clock.FakeClock) string {
	ctx := context.Background()
	var threadKey uint64
	for i, subj := range []string{"Old computers", "Re: Old computers"} {
		raw := fmt.Sprintf("From: donor@x.test\r\nTo: alice@example.test\r\nSubject: %s\r\nMessage-ID: <m%d@x.test>\r\n\r\nbody %d\r\n", subj, i, i)
		ref, err := f.st.Blobs().Put(ctx, bytes.NewReader([]byte(raw)))
		if err != nil {
			f.t.Fatalf("Put: %v", err)
		}
		at := clk.Now().Add(time.Duration(i) * time.Minute)
		if _, _, err := f.st.Meta().InsertMessage(ctx, store.Message{PrincipalID: f.alice.ID, InternalDate: at,
			ReceivedAt: at, Size: ref.Size, Blob: ref, ThreadID: threadKey,
			Envelope: store.Envelope{Subject: subj, MessageID: fmt.Sprintf("m%d@x.test", i)}},
			[]store.MessageMailbox{{MailboxID: f.inbox}}); err != nil {
			f.t.Fatalf("InsertMessage: %v", err)
		}
		if i == 0 {
			msgs, err := f.st.Meta().ListMessages(ctx, f.inbox, store.MessageFilter{Limit: 10})
			if err != nil || len(msgs) != 1 {
				f.t.Fatalf("ListMessages: %v (%d)", err, len(msgs))
			}
			threadKey = uint64(msgs[0].ID)
		}
	}
	return fmt.Sprintf("t%d", threadKey)
}

// call issues one JMAP request; cookie selects the session cookie path,
// otherwise the Bearer key is used.
func (f *fixture) call(cookie bool, pid store.PrincipalID, method string, args map[string]any) (string, map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"using":       []string{"urn:ietf:params:jmap:core", string(appaction.Capability)},
		"methodCalls": []any{[]any{method, args, "c0"}},
	})
	req, _ := http.NewRequest("POST", f.httpd.URL+"/jmap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie {
		req.AddCookie(&http.Cookie{Name: "test-session", Value: strconv.FormatUint(uint64(pid), 10)})
	} else {
		req.Header.Set("Authorization", "Bearer "+f.key)
	}
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
		f.t.Fatalf("response %d: %s", res.StatusCode, raw)
	}
	var name string
	var payload map[string]any
	_ = json.Unmarshal(out.MethodResponses[0][0], &name)
	_ = json.Unmarshal(out.MethodResponses[0][1], &payload)
	return name, payload
}

func (f *fixture) invoke(cookie bool, pid store.PrincipalID) (string, map[string]any) {
	return f.call(cookie, pid, "AppAction/invoke", map[string]any{
		"accountId":   protojmap.AccountIDForPrincipal(f.alice.ID),
		"appId":       strconv.FormatUint(uint64(f.app.ID), 10),
		"extensionId": "offer",
		"threadId":    f.thread,
	})
}

type sessionDoc struct {
	Capabilities map[string]json.RawMessage `json:"capabilities"`
	Accounts     map[string]struct {
		AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
	} `json:"accounts"`
	State string `json:"state"`
}

func (f *fixture) session(pid store.PrincipalID) sessionDoc {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.httpd.URL+"/.well-known/jmap", nil)
	req.AddCookie(&http.Cookie{Name: "test-session", Value: strconv.FormatUint(uint64(pid), 10)})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("GET session: %v", err)
	}
	defer res.Body.Close()
	var d sessionDoc
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		f.t.Fatalf("decode session: %v", err)
	}
	return d
}

func TestCapability_OfferedActionsAndState(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store, clk *clock.FakeClock) {
		f := newFixture(t, st, clk, 0, `"thread.raw"`)
		ctx := context.Background()

		s := f.session(f.alice.ID)
		if _, ok := s.Capabilities[string(appaction.Capability)]; !ok {
			t.Fatalf("capability not advertised to alice: %v", s.Capabilities)
		}
		acct := s.Accounts[protojmap.AccountIDForPrincipal(f.alice.ID)]
		var v struct {
			Actions []map[string]any `json:"actions"`
		}
		if err := json.Unmarshal(acct.AccountCapabilities[string(appaction.Capability)], &v); err != nil || len(v.Actions) != 1 {
			t.Fatalf("account capability = %s", acct.AccountCapabilities[string(appaction.Capability)])
		}
		a := v.Actions[0]
		if a["appId"] != strconv.FormatUint(uint64(f.app.ID), 10) || a["extensionId"] != "offer" ||
			a["point"] != "thread.action" || a["icon"] != "gift" ||
			a["label"].(map[string]any)["de"] != "Angebot" || a["appName"].(map[string]any)["en"] != "Forum" {
			t.Fatalf("action = %v", a)
		}
		raw, _ := json.Marshal(a)
		if strings.Contains(string(raw), "hook") || strings.Contains(string(raw), "127.0.0.1") {
			t.Fatalf("action leaks endpoint or origin: %s", raw)
		}
		if _, ok := f.session(f.bob.ID).Capabilities[string(appaction.Capability)]; ok {
			t.Fatal("capability advertised to bob, who is not enabled")
		}

		// Disabling the app withdraws the capability and changes the state.
		before := s.State
		app := f.app
		app.State = store.AppStateDisabled
		if _, err := st.Meta().UpdateApp(ctx, app); err != nil {
			t.Fatalf("UpdateApp: %v", err)
		}
		s2 := f.session(f.alice.ID)
		if _, ok := s2.Capabilities[string(appaction.Capability)]; ok {
			t.Fatal("capability still advertised for a disabled app")
		}
		if s2.State == before {
			t.Fatal("session state unchanged after the offered actions changed")
		}
		if got := f.postState(f.alice.ID); got != s2.State {
			t.Fatalf("POST sessionState %q != GET state %q", got, s2.State)
		}

		// Re-enabling restores the descriptor and with it the state.
		app.State = store.AppStateActive
		if _, err := st.Meta().UpdateApp(ctx, app); err != nil {
			t.Fatalf("UpdateApp: %v", err)
		}
		if got := f.postState(f.alice.ID); got != before {
			t.Fatalf("session state after re-enabling = %q, want %q", got, before)
		}

		// Enabling the app for bob changes bob's state and leaves
		// alice's alone; withdrawing it reverts bob's.
		bobBefore := f.postState(f.bob.ID)
		if err := st.Meta().SetAppPrincipals(ctx, f.app.ID, []store.PrincipalID{f.alice.ID, f.bob.ID}); err != nil {
			t.Fatalf("SetAppPrincipals: %v", err)
		}
		bobEnabled := f.postState(f.bob.ID)
		if bobEnabled == bobBefore {
			t.Fatal("bob's session state unchanged after the app was enabled for him")
		}
		if bs := f.session(f.bob.ID); bs.State != bobEnabled {
			t.Fatalf("bob GET state %q != POST sessionState %q", bs.State, bobEnabled)
		} else if _, ok := bs.Capabilities[string(appaction.Capability)]; !ok {
			t.Fatal("capability not advertised to bob after enabling")
		}
		if got := f.postState(f.alice.ID); got != before {
			t.Fatalf("alice's session state changed when bob was enabled: %q -> %q", before, got)
		}
		if err := st.Meta().SetAppPrincipals(ctx, f.app.ID, []store.PrincipalID{f.alice.ID}); err != nil {
			t.Fatalf("SetAppPrincipals: %v", err)
		}
		if got := f.postState(f.bob.ID); got != bobBefore {
			t.Fatalf("bob's session state after disabling = %q, want %q", got, bobBefore)
		}
	})
}

// postState returns the sessionState of a Core/echo call by pid over
// the session cookie.
func (f *fixture) postState(pid store.PrincipalID) string {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"using":       []string{"urn:ietf:params:jmap:core"},
		"methodCalls": []any{[]any{"Core/echo", map[string]any{}, "c0"}},
	})
	req, _ := http.NewRequest("POST", f.httpd.URL+"/jmap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "test-session", Value: strconv.FormatUint(uint64(pid), 10)})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("POST /jmap: %v", err)
	}
	defer res.Body.Close()
	var out struct {
		SessionState string `json:"sessionState"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.SessionState == "" {
		f.t.Fatalf("decode POST /jmap: %v (%+v)", err, out)
	}
	return out.SessionState
}

func TestInvoke_OpenURLAndAudit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store, clk *clock.FakeClock) {
		f := newFixture(t, st, clk, 0, `"thread.raw"`)
		f.rec.reply = `{"type":"open_url","url":"` + f.rec.origin + `/draft/42"}`
		name, resp := f.invoke(true, f.alice.ID)
		if name != "AppAction/invoke" || resp["type"] != "openUrl" || resp["url"] != f.rec.origin+"/draft/42" {
			t.Fatalf("invoke = %s %v", name, resp)
		}
		if len(f.rec.bodies) != 1 {
			t.Fatalf("app received %d requests", len(f.rec.bodies))
		}
		body, hdr := f.rec.bodies[0], f.rec.headers[0]
		ts := strings.TrimPrefix(strings.Split(hdr.Get(apps.HeaderSignature), ",")[0], "t=")
		n, _ := strconv.ParseInt(ts, 10, 64)
		if hdr.Get(apps.HeaderSignature) != apps.SignatureHeader([]byte(appSecret), n, body) {
			t.Fatal("request not signed with the presented secret")
		}
		for _, want := range []string{`"app_key":"test.appaction"`, `"extension_id":"offer"`,
			`"email":"alice@example.test"`, `"subject":"Old computers"`, `"message_count":2`,
			`filename="01.eml"`, `filename="02.eml"`, "body 0", "body 1"} {
			if !bytes.Contains(body, []byte(want)) {
				t.Errorf("request body lacks %q", want)
			}
		}
		if bytes.Index(body, []byte("body 0")) > bytes.Index(body, []byte("body 1")) {
			t.Error("messages not in receivedAt order")
		}
		app, _ := st.Meta().GetApp(context.Background(), f.app.ID)
		if app.LastInvokedAt.IsZero() {
			t.Error("last_invoked_at not recorded")
		}
		entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{Action: "app.invoke"})
		if err != nil || len(entries) != 1 {
			t.Fatalf("audit entries = %v, %v", entries, err)
		}
		e := entries[0]
		if e.Subject != "app:test.appaction/offer" || e.ActorID != strconv.FormatUint(uint64(f.alice.ID), 10) ||
			e.Metadata["message_count"] != "2" || e.Metadata["status"] != "200" ||
			e.Metadata["outcome"] != "open_url" || e.Metadata["request_bytes"] != strconv.Itoa(len(body)) {
			t.Fatalf("audit entry = %+v", e)
		}
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "Old computers") || strings.Contains(string(raw), "donor@") {
			t.Fatalf("audit entry carries message content: %s", raw)
		}
	})
}

func TestInvoke_Refusals(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store, clk *clock.FakeClock) {
		f := newFixture(t, st, clk, 10, `"thread.raw"`)
		f.rec.reply = `{"type":"notice","text":"ok"}`

		if _, resp := f.invoke(false, f.alice.ID); resp["type"] != "forbidden" {
			t.Errorf("Bearer invoke = %v, want forbidden", resp)
		}
		if _, resp := f.invoke(true, f.bob.ID); resp["type"] != "forbidden" && resp["type"] != "accountNotFound" {
			t.Errorf("bob invoking on alice's account = %v", resp)
		}
		if _, resp := f.invoke(true, f.alice.ID); resp["type"] != "tooLarge" {
			t.Errorf("oversized thread = %v, want tooLarge", resp)
		}
		_, resp := f.call(true, f.alice.ID, "AppAction/invoke", map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(f.alice.ID), "appId": strconv.FormatUint(uint64(f.app.ID), 10),
			"extensionId": "missing", "threadId": f.thread})
		if resp["type"] != "notFound" {
			t.Errorf("unknown extension = %v", resp)
		}
		_, resp = f.call(true, f.alice.ID, "AppAction/invoke", map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(f.alice.ID), "appId": strconv.FormatUint(uint64(f.app.ID), 10),
			"extensionId": "offer", "threadId": "t999999"})
		if resp["type"] != "notFound" {
			t.Errorf("unknown thread = %v", resp)
		}
		if len(f.rec.bodies) != 0 {
			t.Fatalf("refused invocations reached the app %d times", len(f.rec.bodies))
		}
	})
}

func TestInvoke_AppErrors(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store, clk *clock.FakeClock) {
		f := newFixture(t, st, clk, 0, `"thread.raw"`)
		f.rec.status, f.rec.reply = 403, `{"error":"Not a board member"}`
		if _, resp := f.invoke(true, f.alice.ID); resp["type"] != "appRejected" || resp["description"] != "Not a board member" {
			t.Errorf("4xx = %v", resp)
		}
		f.rec.status, f.rec.reply = 500, ``
		if _, resp := f.invoke(true, f.alice.ID); resp["type"] != "appUnavailable" {
			t.Errorf("5xx = %v", resp)
		}
		f.rec.status, f.rec.reply = 200, `{"type":"open_url","url":"https://elsewhere.example/x"}`
		if _, resp := f.invoke(true, f.alice.ID); resp["type"] != "appUnavailable" {
			t.Errorf("foreign open_url = %v", resp)
		}
		app, _ := st.Meta().GetApp(context.Background(), f.app.ID)
		if !app.LastInvokedAt.IsZero() {
			t.Error("last_invoked_at recorded for failed invocations")
		}
	})
}

func TestInvoke_WithoutThreadRawSendsContextOnly(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store, clk *clock.FakeClock) {
		f := newFixture(t, st, clk, 10, ``)
		f.rec.reply = `{"type":"notice","text":"noted"}`
		if _, resp := f.invoke(true, f.alice.ID); resp["type"] != "notice" || resp["text"] != "noted" {
			t.Fatalf("invoke = %v", resp)
		}
		body := f.rec.bodies[0]
		if bytes.Contains(body, []byte("message[]")) || bytes.Contains(body, []byte("Old computers")) {
			t.Fatalf("thread content sent without thread.raw: %s", body)
		}
	})
}
