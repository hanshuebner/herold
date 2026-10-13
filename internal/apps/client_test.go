package apps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// newOriginServer starts a plain-HTTP origin on 127.0.0.1 and returns
// it with its normalised origin.
func newOriginServer(t *testing.T, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	origin, err := sysconfig.NormalizeOrigin(srv.URL)
	if err != nil {
		t.Fatalf("NormalizeOrigin: %v", err)
	}
	return srv, origin
}

func manifestHandler(status int, ctype, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WellKnownManifestPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestCheckOrigin(t *testing.T) {
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{"http://127.0.0.1:18080"}})
	if o, err := c.CheckOrigin("https://Forum.Example.org/"); err != nil || o != "https://forum.example.org" {
		t.Fatalf("CheckOrigin https = %q, %v", o, err)
	}
	if o, err := c.CheckOrigin("http://127.0.0.1:18080"); err != nil || o != "http://127.0.0.1:18080" {
		t.Fatalf("CheckOrigin allowlisted http = %q, %v", o, err)
	}
	if _, err := c.CheckOrigin("http://forum.example.org"); err == nil {
		t.Fatal("CheckOrigin accepted http origin that is not allowlisted")
	}
	if _, err := c.CheckOrigin("https://forum.example.org/path"); err == nil {
		t.Fatal("CheckOrigin accepted an origin with a path")
	}
}

func TestFetchManifest_OK(t *testing.T) {
	_, origin := newOriginServer(t, manifestHandler(200, "application/json; charset=utf-8", specExample))
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}})
	fm, err := c.FetchManifest(context.Background(), origin)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	canon, digest := fm.Manifest.Canonical()
	if fm.Digest != digest || string(fm.Canonical) != string(canon) {
		t.Fatalf("fetched digest/canonical mismatch")
	}
	if fm.Manifest.AppKey != "de.classic-computing.forum.donations" {
		t.Fatalf("app key = %q", fm.Manifest.AppKey)
	}
}

func TestFetchManifest_Failures(t *testing.T) {
	cases := map[string]struct {
		h    http.Handler
		want string
	}{
		"not found":    {manifestHandler(404, "application/json", `{}`), "answered 404"},
		"content type": {manifestHandler(200, "text/html", specExample), "Content-Type"},
		"too large": {manifestHandler(200, "application/json",
			`{"x":"`+strings.Repeat("a", MaxManifestBytes)+`"}`), "exceeds"},
		"redirect": {http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example/manifest.json", http.StatusFound)
		}), "answered 302"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, origin := newOriginServer(t, tc.h)
			c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}})
			_, err := c.FetchManifest(context.Background(), origin)
			var fe *FetchError
			if !errors.As(err, &fe) || !strings.Contains(fe.Reason, tc.want) {
				t.Fatalf("err = %v, want FetchError mentioning %q", err, tc.want)
			}
		})
	}
}

func TestFetchManifest_InvalidManifest(t *testing.T) {
	_, origin := newOriginServer(t, manifestHandler(200, "application/json", `{"herold_app":1}`))
	c := NewClient(ClientOptions{AllowedInsecureOrigins: []string{origin}})
	_, err := c.FetchManifest(context.Background(), origin)
	var me *ManifestError
	if !errors.As(err, &me) {
		t.Fatalf("err = %v, want *ManifestError", err)
	}
}

// TestFetchManifest_GuardRefusesLoopback checks that an origin outside
// allowed_insecure_origins is not dialled on a loopback address, even
// when it is https.
func TestFetchManifest_GuardRefusesLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(manifestHandler(200, "application/json", specExample))
	t.Cleanup(srv.Close)
	origin, _ := sysconfig.NormalizeOrigin(srv.URL)
	c := NewClient(ClientOptions{Timeout: 2 * time.Second})
	_, err := c.FetchManifest(context.Background(), origin)
	var fe *FetchError
	if !errors.As(err, &fe) || !strings.Contains(fe.Reason, "blocked") {
		t.Fatalf("err = %v, want blocked-address FetchError", err)
	}
	// An http origin that is not allowlisted is refused before dialling.
	plain, porigin := newOriginServer(t, manifestHandler(200, "application/json", specExample))
	_ = plain
	if _, err := c.FetchManifest(context.Background(), porigin); err == nil {
		t.Fatal("http origin outside the allowlist was fetched")
	}
}

func TestFetchManifest_Timeout(t *testing.T) {
	release := make(chan struct{})
	_, origin := newOriginServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer close(release)
	c := NewClient(ClientOptions{Timeout: 200 * time.Millisecond, AllowedInsecureOrigins: []string{origin}})
	if _, err := c.FetchManifest(context.Background(), origin); err == nil {
		t.Fatal("FetchManifest did not time out")
	}
}
