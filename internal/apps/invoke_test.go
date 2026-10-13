package apps

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedRequest is one POST captured by the test app.
type recordedRequest struct {
	header http.Header
	body   []byte
}

type testApp struct {
	mu       sync.Mutex
	requests []recordedRequest
	respond  func(w http.ResponseWriter, r *http.Request)
}

func (a *testApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.requests = append(a.requests, recordedRequest{header: r.Header.Clone(), body: body})
	a.mu.Unlock()
	a.respond(w, r)
}

func jsonReply(status int, v string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(v))
	}
}

func messagesSource(msgs ...string) MessageSource {
	return func(ctx context.Context, fn func(int, io.Reader) error) error {
		for i, m := range msgs {
			if err := fn(i+1, strings.NewReader(m)); err != nil {
				return err
			}
		}
		return nil
	}
}

var testNow = time.Date(2026, 10, 13, 8, 12, 0, 0, time.UTC)

func newInvocation(origin string, msgs ...string) Invocation {
	inv := Invocation{
		Origin:   origin,
		Endpoint: "/actions/act",
		Key:      []byte("presented-secret-text"),
		Now:      testNow,
		Context: InvocationContext{
			InvocationID: "inv-1",
			AppKey:       "test.app",
			ExtensionID:  "act",
			User:         Party{Email: "alice@example.local", DisplayName: "Alice"},
			Account:      Party{Email: "alice@example.local"},
			Thread:       ThreadInfo{ID: "t7", Subject: "Old computers", MessageCount: len(msgs)},
		},
	}
	for i, m := range msgs {
		inv.Context.Messages = append(inv.Context.Messages, MessageInfo{
			Index: i + 1, ReceivedAt: testNow.Format(time.RFC3339), Size: int64(len(m))})
	}
	if len(msgs) > 0 {
		inv.Messages = messagesSource(msgs...)
	}
	return inv
}

func TestSignatureHeader_Vector(t *testing.T) {
	key := []byte("k")
	body := []byte("hello")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("v1:1700000000:hello"))
	want := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil))
	if got := SignatureHeader(key, 1700000000, body); got != want {
		t.Fatalf("SignatureHeader = %q, want %q", got, want)
	}
}

func TestInvoke_RequestShape(t *testing.T) {
	app := &testApp{respond: jsonReply(200, `{"type":"notice","text":"Thanks"}`)}
	_, origin := newOriginServer(t, app)
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}, UserAgent: "herold/test"})
	msg1 := "From: a@b\r\nSubject: one\r\n\r\nbody one\r\n"
	msg2 := "From: c@d\r\nSubject: two\r\n\r\nbody two\r\n"
	res, err := c.Invoke(context.Background(), newInvocation(origin, msg1, msg2))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Type != ResponseNotice || res.Text != "Thanks" || res.Status != 200 {
		t.Fatalf("result = %+v", res)
	}
	if len(app.requests) != 1 {
		t.Fatalf("requests = %d", len(app.requests))
	}
	req := app.requests[0]
	if res.RequestBytes != int64(len(req.body)) {
		t.Errorf("RequestBytes = %d, body = %d", res.RequestBytes, len(req.body))
	}
	if req.header.Get(HeaderInvocationID) != "inv-1" || req.header.Get(HeaderAppAPI) != "1" ||
		req.header.Get("User-Agent") != "herold/test" {
		t.Errorf("headers = %v", req.header)
	}
	ts := strconv.FormatInt(testNow.Unix(), 10)
	if want := SignatureHeader([]byte("presented-secret-text"), testNow.Unix(), req.body); req.header.Get(HeaderSignature) != want ||
		!strings.HasPrefix(want, "t="+ts+",v1=") {
		t.Errorf("signature %q, want %q", req.header.Get(HeaderSignature), want)
	}

	mt, params, err := mime.ParseMediaType(req.header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("Content-Type = %q", req.header.Get("Content-Type"))
	}
	mr := multipart.NewReader(bytes.NewReader(req.body), params["boundary"])
	var parts []*multipart.Part
	var contents [][]byte
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p)
		contents = append(contents, b)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want context + 2 messages", len(parts))
	}
	if parts[0].FormName() != "context" || parts[0].Header.Get("Content-Type") != "application/json" {
		t.Errorf("first part = %v", parts[0].Header)
	}
	var ctxDoc map[string]any
	if err := json.Unmarshal(contents[0], &ctxDoc); err != nil {
		t.Fatalf("context JSON: %v", err)
	}
	if ctxDoc["herold_app"] != float64(1) || ctxDoc["issued_at"] != "2026-10-13T08:12:00Z" ||
		ctxDoc["invocation_id"] != "inv-1" || ctxDoc["extension_id"] != "act" {
		t.Errorf("context = %v", ctxDoc)
	}
	for i, want := range []string{msg1, msg2} {
		p := parts[i+1]
		if p.FormName() != "message[]" || p.FileName() != fmt.Sprintf("%02d.eml", i+1) ||
			p.Header.Get("Content-Type") != "message/rfc822" || string(contents[i+1]) != want {
			t.Errorf("part %d: name=%q file=%q type=%q body=%q", i+1, p.FormName(), p.FileName(),
				p.Header.Get("Content-Type"), contents[i+1])
		}
	}
}

func TestInvoke_ContextOnlyWithoutMessages(t *testing.T) {
	app := &testApp{respond: jsonReply(200, `{"type":"notice","text":"ok"}`)}
	_, origin := newOriginServer(t, app)
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}})
	if _, err := c.Invoke(context.Background(), newInvocation(origin)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if n := strings.Count(string(app.requests[0].body), "Content-Disposition"); n != 1 {
		t.Fatalf("parts = %d, want the context part only", n)
	}
	if !strings.Contains(string(app.requests[0].body), `"messages":[]`) {
		t.Fatalf("context lacks an empty messages list: %s", app.requests[0].body)
	}
}

func TestInvoke_Responses(t *testing.T) {
	cases := []struct {
		name     string
		insecure bool
		respond  func(origin string) func(http.ResponseWriter, *http.Request)
		wantType string
		wantURL  string
		wantText string
		wantKind ErrorKind
	}{
		{name: "open_url under origin", insecure: true,
			respond: func(o string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(200, `{"type":"open_url","url":"`+o+`/opened/1?x=y"}`)
			}, wantType: ResponseOpenURL, wantURL: "/opened/1?x=y"},
		{name: "open_url elsewhere", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(200, `{"type":"open_url","url":"https://evil.example/x"}`)
			}, wantKind: ErrorUnavailable},
		{name: "open_url javascript", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(200, `{"type":"open_url","url":"javascript:alert(1)"}`)
			}, wantKind: ErrorUnavailable},
		{name: "notice", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(201, `{"type":"notice","text":"Created\u0007 draft"}`)
			}, wantType: ResponseNotice, wantText: "Created  draft"},
		{name: "unknown type", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(200, `{"type":"open_window"}`)
			}, wantKind: ErrorUnavailable},
		{name: "not json", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }
			}, wantKind: ErrorUnavailable},
		{name: "4xx with error", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(422, `{"error":"You are not a board member."}`)
			}, wantKind: ErrorRejected, wantText: "You are not a board member."},
		{name: "4xx bare", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }
			}, wantKind: ErrorRejected},
		{name: "5xx with error", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(503, `{"error":"maintenance"}`)
			}, wantKind: ErrorRejected, wantText: "maintenance"},
		{name: "5xx bare", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }
			}, wantKind: ErrorUnavailable},
		{name: "redirect", insecure: true,
			respond: func(o string) func(http.ResponseWriter, *http.Request) {
				return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, o+"/elsewhere", http.StatusFound) }
			}, wantKind: ErrorUnavailable},
		{name: "oversized", insecure: true,
			respond: func(string) func(http.ResponseWriter, *http.Request) {
				return jsonReply(200, `{"type":"notice","text":"`+strings.Repeat("a", MaxResponseBytes)+`"}`)
			}, wantKind: ErrorUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &testApp{}
			_, origin := newOriginServer(t, app)
			app.respond = tc.respond(origin)
			c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}})
			res, err := c.Invoke(context.Background(), newInvocation(origin, "m"))
			if tc.wantKind != "" {
				var ie *InvokeError
				if !errors.As(err, &ie) || ie.Kind != tc.wantKind || ie.Text != tc.wantText {
					t.Fatalf("err = %#v, want %s %q", err, tc.wantKind, tc.wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			if res.Type != tc.wantType || res.Text != tc.wantText ||
				(tc.wantURL != "" && res.URL != origin+tc.wantURL) {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestInvoke_OpenURLSchemeRules(t *testing.T) {
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{"http://127.0.0.1:18080"}})
	if _, err := c.checkOpenURL("http://127.0.0.1:18080", "http://127.0.0.1:18080/opened/x"); err != nil {
		t.Errorf("http URL under an insecure origin rejected: %v", err)
	}
	if _, err := c.checkOpenURL("https://forum.example.org", "https://forum.example.org:443/t/1"); err != nil {
		t.Errorf("https URL with explicit default port rejected: %v", err)
	}
	for _, bad := range []string{
		"http://forum.example.org/t/1",
		"https://forum.example.org.evil.test/t/1",
		"https://user@forum.example.org/t/1",
		"https://forum.example.org:8443/t/1",
		"/relative",
	} {
		if _, err := c.checkOpenURL("https://forum.example.org", bad); err == nil {
			t.Errorf("checkOpenURL accepted %q", bad)
		}
	}
}

func TestInvoke_Timeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	app := &testApp{respond: func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}}
	_, origin := newOriginServer(t, app)
	c := NewClient(ClientOptions{Timeout: 300 * time.Millisecond, AllowedInsecureOrigins: []string{origin}})
	_, err := c.Invoke(context.Background(), newInvocation(origin, "m"))
	var ie *InvokeError
	if !errors.As(err, &ie) || ie.Kind != ErrorUnavailable || !ie.Timeout {
		t.Fatalf("err = %#v, want unavailable timeout", err)
	}
}

func TestInvoke_BodyBuildErrorSendsNothing(t *testing.T) {
	app := &testApp{respond: jsonReply(200, `{"type":"notice","text":"x"}`)}
	_, origin := newOriginServer(t, app)
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}})
	boom := errors.New("export too large")
	inv := newInvocation(origin, "m")
	inv.Messages = func(ctx context.Context, fn func(int, io.Reader) error) error { return boom }
	_, err := c.Invoke(context.Background(), inv)
	if !errors.Is(err, ErrBodyBuild) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want ErrBodyBuild wrapping the source error", err)
	}
	if len(app.requests) != 0 {
		t.Fatalf("app received %d requests", len(app.requests))
	}
}

func TestPlainText(t *testing.T) {
	if got := PlainText("  a\tb\nc\x00 "); got != "a b\nc" {
		t.Errorf("PlainText = %q", got)
	}
	if got := PlainText(strings.Repeat("ä", 600)); len([]rune(got)) != MaxTextRunes {
		t.Errorf("PlainText length = %d", len([]rune(got)))
	}
}
