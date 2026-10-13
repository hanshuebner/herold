package protoadmin_test

// apps_test.go covers the registered-apps admin REST surface
// (REQ-APP-10..14, REQ-APP-30..31, REQ-ADM-300).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanshuebner/herold/internal/apps"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

var appsDataKey = []byte("apps-admin-test-data-key-32byte!")

// fakeManifestOrigin serves a mutable manifest at the well-known path.
type fakeManifestOrigin struct {
	mu     sync.Mutex
	body   string
	srv    *httptest.Server
	origin string
}

func newFakeManifestOrigin(t *testing.T, body string) *fakeManifestOrigin {
	t.Helper()
	f := &fakeManifestOrigin{body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apps.WellKnownManifestPath {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		b := f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(f.srv.Close)
	f.origin, _ = sysconfig.NormalizeOrigin(f.srv.URL)
	return f
}

func (f *fakeManifestOrigin) set(body string) {
	f.mu.Lock()
	f.body = body
	f.mu.Unlock()
}

func testManifest(appKey, endpoint string, scopes string) string {
	return `{"herold_app":1,"app_key":"` + appKey + `","name":{"en":"Test app","de":"Test-App"},
	  "extensions":[{"point":"thread.action","id":"act","label":{"en":"Do it"},"icon":"gift",
	  "scopes":[` + scopes + `],"endpoint":"` + endpoint + `"},
	  {"point":"message.action","id":"later","label":{"en":"Later"},"scopes":[],"endpoint":"/l"}]}`
}

type appResp struct {
	ID                    uint64             `json:"id"`
	AppKey                string             `json:"app_key"`
	Origin                string             `json:"origin"`
	State                 string             `json:"state"`
	Name                  map[string]string  `json:"name"`
	AllPrincipals         bool               `json:"all_principals"`
	Principals            []map[string]any   `json:"principals"`
	Manifest              *apps.Manifest     `json:"manifest"`
	ManifestDigest        string             `json:"manifest_digest"`
	PendingManifest       *apps.Manifest     `json:"pending_manifest"`
	PendingManifestDigest string             `json:"pending_manifest_digest"`
	PendingDiff           *apps.ManifestDiff `json:"pending_diff"`
	LastInvokedAt         *string            `json:"last_invoked_at"`
	Secret                string             `json:"secret"`
	Warnings              []string           `json:"warnings"`
	ManifestChanged       *bool              `json:"manifest_changed"`
}

func decodeApp(t *testing.T, buf []byte) appResp {
	t.Helper()
	var a appResp
	if err := json.Unmarshal(buf, &a); err != nil {
		t.Fatalf("decode app: %v: %s", err, buf)
	}
	return a
}

func appsOpts(origin string, dataKey []byte) func(*protoadmin.Options) {
	return func(o *protoadmin.Options) {
		o.Apps = protoadmin.AppsOptions{
			Client:  apps.NewClient(apps.ClientOptions{AllowedInsecureOrigins: []string{origin}}),
			DataKey: dataKey,
		}
	}
}

func TestApps_RequireDataKey(t *testing.T) {
	h := newHarnessWithOpts(t, appsOpts("http://127.0.0.1:1", nil))
	_, key := h.bootstrap("admin@example.local")
	for _, path := range []string{"/api/v1/apps", "/api/v1/apps/1"} {
		res, buf := h.doRequest("GET", path, key, nil)
		if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(buf), "apps/data_key_missing") {
			t.Fatalf("GET %s without data key = %d: %s", path, res.StatusCode, buf)
		}
	}
}

func TestApps_RequireAdmin(t *testing.T) {
	h := newHarnessWithOpts(t, appsOpts("http://127.0.0.1:1", appsDataKey))
	_, adminKey := h.bootstrap("admin@example.local")
	pid := h.createPrincipal(adminKey, "user@example.local")
	_, userKey := h.createScopedAPIKey(adminKey, pid, []string{"mail.send"}, false)
	res, buf := h.doRequest("GET", "/api/v1/apps", userKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin GET /apps = %d: %s", res.StatusCode, buf)
	}
}

func TestApps_Lifecycle_BothBackends(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			fake := newFakeManifestOrigin(t, testManifest("test.lifecycle.app", "/hook", `"thread.raw"`))
			h := newHarnessWithStoreOpts(t, be.fs, be.clk, appsOpts(fake.origin, appsDataKey))
			_, key := h.bootstrap("admin@example.local")
			alice := h.createPrincipal(key, "alice@example.local")
			bob := h.createPrincipal(key, "bob@example.local")

			// Create: pending, secret once, warnings for the dropped point.
			res, buf := h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": fake.srv.URL + "/"})
			if res.StatusCode != http.StatusCreated {
				t.Fatalf("create = %d: %s", res.StatusCode, buf)
			}
			created := decodeApp(t, buf)
			if res.Header.Get("Location") != fmt.Sprintf("/api/v1/apps/%d", created.ID) {
				t.Errorf("Location = %q", res.Header.Get("Location"))
			}
			if created.State != "pending_approval" || created.Origin != fake.origin ||
				created.AppKey != "test.lifecycle.app" || created.Manifest != nil ||
				created.PendingManifest == nil || created.PendingManifestDigest == "" ||
				created.Name["de"] != "Test-App" || created.AllPrincipals || len(created.Principals) != 0 {
				t.Fatalf("created = %+v", created)
			}
			if len(created.Warnings) != 1 || !strings.Contains(created.Warnings[0], "message.action") {
				t.Errorf("warnings = %v", created.Warnings)
			}
			if raw, err := base64.RawURLEncoding.DecodeString(created.Secret); err != nil || len(raw) != 32 {
				t.Fatalf("secret %q is not 32 bytes of base64url", created.Secret)
			}
			stored, err := be.fs.Meta().GetApp(context.Background(), store.AppID(created.ID))
			if err != nil {
				t.Fatalf("GetApp: %v", err)
			}
			if plain, err := secrets.Open(appsDataKey, stored.SecretSealed); err != nil || string(plain) != created.Secret {
				t.Fatalf("sealed secret does not open to the presented text: %v", err)
			}
			if created.PendingDiff == nil || len(created.PendingDiff.AddedExtensions) != 1 {
				t.Errorf("pending diff = %+v", created.PendingDiff)
			}

			// Read paths never return the secret.
			res, buf = h.doRequest("GET", fmt.Sprintf("/api/v1/apps/%d", created.ID), key, nil)
			if res.StatusCode != http.StatusOK || decodeApp(t, buf).Secret != "" {
				t.Fatalf("get = %d: %s", res.StatusCode, buf)
			}
			res, buf = h.doRequest("GET", "/api/v1/apps", key, nil)
			if res.StatusCode != http.StatusOK || strings.Contains(string(buf), created.Secret) {
				t.Fatalf("list = %d: %s", res.StatusCode, buf)
			}

			// Duplicate registration.
			res, buf = h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": fake.origin})
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("duplicate create = %d: %s", res.StatusCode, buf)
			}

			// Enabling a pending app is refused.
			res, _ = h.doRequest("POST", fmt.Sprintf("/api/v1/apps/%d/enable", created.ID), key, nil)
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("enable pending = %d", res.StatusCode)
			}

			// Approve: wrong digest is 409, the reviewed one activates.
			path := fmt.Sprintf("/api/v1/apps/%d", created.ID)
			res, buf = h.doRequest("POST", path+"/approve", key, map[string]any{"manifest_digest": "deadbeef"})
			if res.StatusCode != http.StatusConflict || !strings.Contains(string(buf), "apps/manifest_changed") {
				t.Fatalf("approve wrong digest = %d: %s", res.StatusCode, buf)
			}
			res, buf = h.doRequest("POST", path+"/approve", key, map[string]any{"manifest_digest": created.PendingManifestDigest})
			if res.StatusCode != http.StatusOK {
				t.Fatalf("approve = %d: %s", res.StatusCode, buf)
			}
			approved := decodeApp(t, buf)
			if approved.State != "active" || approved.ManifestDigest != created.PendingManifestDigest ||
				approved.PendingManifest != nil || approved.PendingDiff != nil {
				t.Fatalf("approved = %+v", approved)
			}

			// Principals by id and by email; all_principals toggle.
			res, buf = h.doRequest("PATCH", path, key, map[string]any{"principals": []any{alice, "bob@example.local"}})
			if res.StatusCode != http.StatusOK {
				t.Fatalf("patch principals = %d: %s", res.StatusCode, buf)
			}
			if p := decodeApp(t, buf).Principals; len(p) != 2 {
				t.Fatalf("principals = %v", p)
			}
			offered, _ := be.fs.Meta().ListAppsForPrincipal(context.Background(), store.PrincipalID(bob))
			if len(offered) != 1 {
				t.Fatalf("app not offered to bob: %v", offered)
			}
			res, buf = h.doRequest("PATCH", path, key, map[string]any{"principals": []any{"nobody@example.local"}})
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("patch unknown principal = %d: %s", res.StatusCode, buf)
			}
			res, buf = h.doRequest("PATCH", path, key, map[string]any{"all_principals": true, "principals": []any{}})
			if res.StatusCode != http.StatusOK {
				t.Fatalf("patch all = %d: %s", res.StatusCode, buf)
			}
			if a := decodeApp(t, buf); !a.AllPrincipals || len(a.Principals) != 0 {
				t.Fatalf("after patch all = %+v", a)
			}

			// Refresh: unchanged is a no-op; a new manifest becomes pending
			// while the approved one keeps serving.
			res, buf = h.doRequest("POST", path+"/refresh", key, nil)
			if res.StatusCode != http.StatusOK || decodeApp(t, buf).ManifestChanged == nil || *decodeApp(t, buf).ManifestChanged {
				t.Fatalf("refresh unchanged = %d: %s", res.StatusCode, buf)
			}
			fake.set(testManifest("test.lifecycle.app", "/hook2", ""))
			res, buf = h.doRequest("POST", path+"/refresh", key, nil)
			refreshed := decodeApp(t, buf)
			if res.StatusCode != http.StatusOK || !*refreshed.ManifestChanged || refreshed.PendingManifest == nil ||
				refreshed.ManifestDigest != approved.ManifestDigest || refreshed.PendingDiff == nil ||
				len(refreshed.PendingDiff.ChangedEndpoints) != 1 || len(refreshed.PendingDiff.RemovedScopes) != 1 {
				t.Fatalf("refresh changed = %d: %+v", res.StatusCode, refreshed)
			}
			fake.set(testManifest("other.app.key", "/hook", `"thread.raw"`))
			res, buf = h.doRequest("POST", path+"/refresh", key, nil)
			if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(buf), "app_key") {
				t.Fatalf("refresh with changed app_key = %d: %s", res.StatusCode, buf)
			}
			res, buf = h.doRequest("POST", path+"/approve", key, map[string]any{"manifest_digest": refreshed.PendingManifestDigest})
			if res.StatusCode != http.StatusOK || decodeApp(t, buf).Manifest.Extensions[0].Endpoint != "/hook2" {
				t.Fatalf("approve update = %d: %s", res.StatusCode, buf)
			}

			// Rotate: a new secret, returned once.
			res, buf = h.doRequest("POST", path+"/rotate-secret", key, nil)
			rotated := decodeApp(t, buf)
			if res.StatusCode != http.StatusOK || rotated.Secret == "" || rotated.Secret == created.Secret {
				t.Fatalf("rotate = %d: %s", res.StatusCode, buf)
			}
			stored, _ = be.fs.Meta().GetApp(context.Background(), store.AppID(created.ID))
			if plain, _ := secrets.Open(appsDataKey, stored.SecretSealed); string(plain) != rotated.Secret {
				t.Fatal("rotated secret not stored")
			}

			// Disable / enable.
			res, buf = h.doRequest("POST", path+"/disable", key, nil)
			if res.StatusCode != http.StatusOK || decodeApp(t, buf).State != "disabled" {
				t.Fatalf("disable = %d: %s", res.StatusCode, buf)
			}
			res, buf = h.doRequest("POST", path+"/enable", key, nil)
			if res.StatusCode != http.StatusOK || decodeApp(t, buf).State != "active" {
				t.Fatalf("enable = %d: %s", res.StatusCode, buf)
			}

			// Delete.
			res, _ = h.doRequest("DELETE", path, key, nil)
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("delete = %d", res.StatusCode)
			}
			res, _ = h.doRequest("GET", path, key, nil)
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("get after delete = %d", res.StatusCode)
			}

			// Every mutation is audit-logged with subject app:<app_key>.
			res, buf = h.doRequest("GET", "/api/v1/audit?limit=100", key, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("audit = %d: %s", res.StatusCode, buf)
			}
			var page struct {
				Items []struct {
					Action  string `json:"action"`
					Subject string `json:"subject"`
				} `json:"items"`
			}
			_ = json.Unmarshal(buf, &page)
			got := map[string]int{}
			for _, it := range page.Items {
				if it.Subject == "app:test.lifecycle.app" {
					got[it.Action]++
				}
			}
			for _, action := range []string{"app.create", "app.approve", "app.update", "app.refresh",
				"app.rotate_secret", "app.disable", "app.enable", "app.delete"} {
				if got[action] == 0 {
					t.Errorf("no audit entry for %s (got %v)", action, got)
				}
			}
			if strings.Contains(string(buf), created.Secret) || strings.Contains(string(buf), rotated.Secret) {
				t.Error("a secret leaked into the audit log")
			}
		})
	}
}

func TestApps_CreateFailures(t *testing.T) {
	bad := newFakeManifestOrigin(t, `{"herold_app":1,"app_key":"x.y.z","name":{"en":"N"},
		"extensions":[{"point":"thread.action","id":"a","label":{"en":"A"},"scopes":["mailbox.all"],"endpoint":"/a"}]}`)
	h := newHarnessWithOpts(t, appsOpts(bad.origin, appsDataKey))
	_, key := h.bootstrap("admin@example.local")

	res, buf := h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": bad.origin})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid manifest = %d: %s", res.StatusCode, buf)
	}
	var prob struct {
		Type   string   `json:"type"`
		Errors []string `json:"errors"`
	}
	_ = json.Unmarshal(buf, &prob)
	if !strings.HasSuffix(prob.Type, "apps/invalid_manifest") || len(prob.Errors) != 1 ||
		!strings.Contains(prob.Errors[0], "unknown scope") {
		t.Fatalf("problem = %s", buf)
	}

	res, buf = h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": "http://forum.example.test"})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(buf), "apps/invalid_origin") {
		t.Fatalf("http origin = %d: %s", res.StatusCode, buf)
	}
	res, buf = h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": "https://forum.example.test/path"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("origin with path = %d: %s", res.StatusCode, buf)
	}

	bad.set(`not json`)
	res, buf = h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": bad.origin})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("non-JSON manifest = %d: %s", res.StatusCode, buf)
	}
	bad.srv.Close()
	res, buf = h.doRequest("POST", "/api/v1/apps", key, map[string]any{"origin": bad.origin})
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(buf), "apps/manifest_fetch_failed") {
		t.Fatalf("unreachable origin = %d: %s", res.StatusCode, buf)
	}
}
