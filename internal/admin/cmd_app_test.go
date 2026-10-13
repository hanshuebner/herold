package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/apps"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

const cliAppManifest = `{"herold_app":1,"app_key":"test.cli.app","name":{"en":"CLI app"},
 "extensions":[{"point":"thread.action","id":"act","label":{"en":"Act"},"icon":"gift",
 "scopes":["thread.raw"],"endpoint":"/act"}]}`

// TestCLIApp_Lifecycle drives every herold app subcommand against the
// admin REST surface (REQ-APP-10).
func TestCLIApp_Lifecycle(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cliAppManifest))
	}))
	t.Cleanup(fake.Close)
	origin, _ := sysconfig.NormalizeOrigin(fake.URL)
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.Apps = protoadmin.AppsOptions{
			Client:  apps.NewClient(apps.ClientOptions{AllowedInsecureOrigins: []string{origin}}),
			DataKey: []byte("cli-apps-test-data-key-32-bytes!"),
		}
	})
	if _, err := env.store.Meta().InsertPrincipal(t.Context(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@test.local",
	}); err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}

	out, _, err := env.run("app", "add", origin, "--json")
	if err != nil {
		t.Fatalf("app add: %v", err)
	}
	var created struct {
		ID                    uint64 `json:"id"`
		Secret                string `json:"secret"`
		PendingManifestDigest string `json:"pending_manifest_digest"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.Secret == "" || created.PendingManifestDigest == "" {
		t.Fatalf("app add output: %v: %s", err, out)
	}
	id := uintStr(created.ID)

	if _, _, err := env.run("app", "approve", id); err == nil {
		t.Fatal("app approve without --digest succeeded")
	}
	steps := [][]string{
		{"app", "approve", id, "--digest", created.PendingManifestDigest},
		{"app", "set", id, "--principal", "alice@test.local"},
		{"app", "refresh", id},
		{"app", "disable", id},
		{"app", "enable", id},
	}
	for _, s := range steps {
		if _, _, err := env.run(s...); err != nil {
			t.Fatalf("%v: %v", s, err)
		}
	}
	out, _, err = env.run("app", "show", id, "--json")
	if err != nil {
		t.Fatalf("app show: %v", err)
	}
	if !strings.Contains(out, `"state": "active"`) || !strings.Contains(out, "alice@test.local") ||
		strings.Contains(out, created.Secret) {
		t.Fatalf("app show output: %s", out)
	}
	out, _, err = env.run("app", "rotate-secret", id, "--json")
	if err != nil || !strings.Contains(out, `"secret"`) || strings.Contains(out, created.Secret) {
		t.Fatalf("app rotate-secret: %v: %s", err, out)
	}
	out, _, err = env.run("app", "list", "--json")
	if err != nil || !strings.Contains(out, "test.cli.app") {
		t.Fatalf("app list: %v: %s", err, out)
	}
	if _, _, err := env.run("app", "set", id, "--all-principals=true", "--no-principals"); err != nil {
		t.Fatalf("app set --all-principals: %v", err)
	}
	if _, _, err := env.run("app", "remove", id); err != nil {
		t.Fatalf("app remove: %v", err)
	}
	if _, _, err := env.run("app", "show", id); err == nil {
		t.Fatal("app show after remove succeeded")
	}
}

func TestCLIApp_HumanOutput(t *testing.T) {
	var b strings.Builder
	err := writeAppHuman(&b, map[string]any{
		"id": float64(3), "app_key": "k.e.y", "origin": "https://a.example", "state": "pending_approval",
		"name":           map[string]any{"en": "App"},
		"all_principals": false, "principals": []any{map[string]any{"id": float64(2), "email": "a@b"}},
		"pending_manifest": map[string]any{"extensions": []any{map[string]any{
			"point": "thread.action", "id": "x", "label": map[string]any{"en": "X"}, "icon": "gift",
			"scopes": []any{"thread.raw"}, "endpoint": "/x"}}},
		"warnings": []any{"extensions[1]: unknown extension point \"m\" dropped"},
		"secret":   "c2VjcmV0",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := b.String()
	for _, want := range []string{"k.e.y", "a@b", "pending manifest", "thread.action  x", "unknown extension point", "secret: c2VjcmV0"} {
		if !strings.Contains(s, want) {
			t.Errorf("human output lacks %q:\n%s", want, s)
		}
	}
}
