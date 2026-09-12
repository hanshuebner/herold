package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestPublicListener_AssetLinks_Configured boots a real herold instance
// with [server.ui] android_app_links set and drives the request through
// the composed public mux (the #249 routing-gap class: a handler that
// exists but is never mounted on the served mux would pass an
// httptest-of-bare-handler test while still 404ing in production).
func TestPublicListener_AssetLinks_Configured(t *testing.T) {
	_, cfg := minimalConfigFixture(t)
	cfg.Server.UI.AndroidAppLinks = []sysconfig.AndroidAppLinkConfig{{
		Package:                "com.netzhansa.herold.android",
		SHA256CertFingerprints: []string{"DF:1D:E8:CA:0B:62:34:FB:34:2F:49:32:F5:60:56:23:38:B7:A9:EC:A8:9D:CC:AE:A4:8F:3A:D1:B9:26:E3:72"},
	}}
	addrs, doneCh, cancel := startTestServerWithConfig(t, cfg)
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	resp, err := http.Get("http://" + publicAddr + "/.well-known/assetlinks.json")
	if err != nil {
		t.Fatalf("GET assetlinks.json: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s; want 200", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("Cache-Control=%q; want a one-day public cache", cc)
	}
	var doc []struct {
		Relation []string `json:"relation"`
		Target   struct {
			Namespace              string   `json:"namespace"`
			PackageName            string   `json:"package_name"`
			SHA256CertFingerprints []string `json:"sha256_cert_fingerprints"`
		} `json:"target"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal body %s: %v", body, err)
	}
	if len(doc) != 1 {
		t.Fatalf("len(doc)=%d; want 1, body=%s", len(doc), body)
	}
	if got := doc[0].Relation; len(got) != 1 || got[0] != "delegate_permission/common.handle_all_urls" {
		t.Errorf("relation=%v; want [delegate_permission/common.handle_all_urls]", got)
	}
	if doc[0].Target.Namespace != "android_app" {
		t.Errorf("target.namespace=%q; want android_app", doc[0].Target.Namespace)
	}
	if doc[0].Target.PackageName != "com.netzhansa.herold.android" {
		t.Errorf("target.package_name=%q; want com.netzhansa.herold.android", doc[0].Target.PackageName)
	}
	if want := []string{"DF:1D:E8:CA:0B:62:34:FB:34:2F:49:32:F5:60:56:23:38:B7:A9:EC:A8:9D:CC:AE:A4:8F:3A:D1:B9:26:E3:72"}; len(doc[0].Target.SHA256CertFingerprints) != 1 || doc[0].Target.SHA256CertFingerprints[0] != want[0] {
		t.Errorf("target.sha256_cert_fingerprints=%v; want %v", doc[0].Target.SHA256CertFingerprints, want)
	}
}

// TestPublicListener_AssetLinks_Unconfigured404s asserts that with no
// [server.ui] android_app_links entries, the route is entirely absent:
// an operator running no Android client gets the plain 404 rather than
// an empty-array document that would misleadingly claim the endpoint
// exists.
func TestPublicListener_AssetLinks_Unconfigured404s(t *testing.T) {
	_, addrs, doneCh, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down")
		}
	})
	publicAddr := addrs["public"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	resp, err := http.Get("http://" + publicAddr + "/.well-known/assetlinks.json")
	if err != nil {
		t.Fatalf("GET assetlinks.json: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d body=%s; want 404", resp.StatusCode, body)
	}
}
