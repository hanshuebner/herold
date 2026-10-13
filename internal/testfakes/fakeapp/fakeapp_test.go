package fakeapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/apps"
)

func TestManifestValidates(t *testing.T) {
	s := New(t, Options{})
	b, _ := json.Marshal(s.Manifest())
	m, warnings, err := apps.ParseManifest(b)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("ParseManifest: %v %v", err, warnings)
	}
	if len(m.Extensions) != 5 || m.AppKey != DefaultAppKey {
		t.Fatalf("manifest = %+v", m)
	}
	c := apps.NewClient(apps.ClientOptions{AllowedInsecureOrigins: []string{s.Origin()}})
	if _, err := c.FetchManifest(context.Background(), s.Origin()); err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
}

func TestVerifySignature(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte("body")
	good := apps.SignatureHeader([]byte("s3cret"), now.Unix(), body)
	if err := verifySignature("s3cret", good, body, now); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	cases := map[string]func() error{
		"wrong secret": func() error { return verifySignature("other", good, body, now) },
		"wrong body":   func() error { return verifySignature("s3cret", good, []byte("bodY"), now) },
		"stale":        func() error { return verifySignature("s3cret", good, body, now.Add(301*time.Second)) },
		"malformed":    func() error { return verifySignature("s3cret", "v1=00", body, now) },
		"no secret":    func() error { return verifySignature("", good, body, now) },
	}
	for name, f := range cases {
		if f() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func invoke(t *testing.T, s *Server, c *apps.Client, ext string, key string) (apps.Result, error) {
	t.Helper()
	return c.Invoke(context.Background(), apps.Invocation{
		Origin: s.Origin(), Endpoint: "/actions/" + ext, Key: []byte(key), Now: time.Now(),
		Context: apps.InvocationContext{InvocationID: apps.NewInvocationID(), Messages: []apps.MessageInfo{{Index: 1}}},
		Messages: func(ctx context.Context, fn func(int, io.Reader) error) error {
			return fn(1, strings.NewReader("Subject: x\r\n\r\ny\r\n"))
		},
	})
}

func TestExtensions(t *testing.T) {
	s := New(t, Options{Secret: "k", SlowDelay: 2 * time.Second})
	c := apps.NewClient(apps.ClientOptions{Timeout: 500 * time.Millisecond, AllowedInsecureOrigins: []string{s.Origin()}})

	res, err := invoke(t, s, c, ExtOpenURL, "k")
	if err != nil || res.Type != apps.ResponseOpenURL || !strings.HasPrefix(res.URL, s.Origin()+"/opened/") {
		t.Fatalf("open-url = %+v, %v", res, err)
	}
	resp, err := http.Get(res.URL)
	if err != nil {
		t.Fatalf("GET opened: %v", err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Received 1 messages") {
		t.Fatalf("opened page = %s", page)
	}

	if res, err := invoke(t, s, c, ExtNotice, "k"); err != nil || res.Text != "Fake app received 1 messages." {
		t.Fatalf("notice = %+v, %v", res, err)
	}
	var ie *apps.InvokeError
	if _, err := invoke(t, s, c, ExtReject, "k"); !errors.As(err, &ie) || ie.Kind != apps.ErrorRejected || ie.Text != RejectText {
		t.Fatalf("reject = %v", err)
	}
	if _, err := invoke(t, s, c, ExtFail, "k"); !errors.As(err, &ie) || ie.Kind != apps.ErrorUnavailable {
		t.Fatalf("fail = %v", err)
	}
	if _, err := invoke(t, s, c, ExtSlow, "k"); !errors.As(err, &ie) || ie.Kind != apps.ErrorUnavailable || !ie.Timeout {
		t.Fatalf("slow = %v", err)
	}
	if _, err := invoke(t, s, c, ExtNotice, "wrong"); !errors.As(err, &ie) || ie.Kind != apps.ErrorRejected {
		t.Fatalf("bad signature = %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	invs := s.Invocations()
	if len(invs) < 5 {
		t.Fatalf("recorded %d invocations", len(invs))
	}
	first := invs[0]
	if !first.SignatureValid || first.PartCount != 1 || first.Parts[0].FileName != "01.eml" ||
		first.Parts[0].ContentType != "message/rfc822" || first.ContextType != "application/json" || first.AppAPI != "1" {
		t.Fatalf("first invocation = %+v", first)
	}
	if last := invs[len(invs)-1]; last.SignatureValid || last.Status != http.StatusUnauthorized {
		t.Fatalf("bad-signature invocation = %+v", last)
	}

	req, _ := http.NewRequest(http.MethodDelete, s.Origin()+"/requests", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /requests: %v", err)
	}
	if len(s.Invocations()) != 0 {
		t.Fatal("DELETE /requests did not clear")
	}
	req, _ = http.NewRequest(http.MethodPut, s.Origin()+"/secret", strings.NewReader("new-secret\n"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT /secret: %v", err)
	}
	if _, err := invoke(t, s, c, ExtNotice, "new-secret"); err != nil {
		t.Fatalf("notice after PUT /secret: %v", err)
	}
}
