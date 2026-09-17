package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

func TestCLIAPIKeyList_Self(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("api-key", "list", "--json")
	if err != nil {
		t.Fatalf("api-key list: %v", err)
	}
	// The seeded admin already has one key (the test API key).
	if !strings.Contains(out, `"items"`) {
		t.Fatalf("expected items field; got %s", out)
	}
}

func TestCLIAPIKeyList_ByPrincipal(t *testing.T) {
	env := newCLITestEnv(t, nil)
	p := seedPrincipal(t, env, "key-owner@test.local")
	if _, err := env.store.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID,
		Hash:        "deadbeef",
		Name:        "dummy",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	out, _, err := env.run("api-key", "list", "--principal", "key-owner@test.local", "--json")
	if err != nil {
		t.Fatalf("api-key list --principal: %v", err)
	}
	if !strings.Contains(out, "dummy") {
		t.Fatalf("expected the seeded key label in output: %s", out)
	}
}

func TestCLIAPIKeyList_UnknownPrincipal(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("api-key", "list", "--principal", "ghost@test.local")
	if err == nil {
		t.Fatalf("expected error for unknown principal")
	}
}

// TestCLIAPIKeyCreate_ByEmail_ScopeBugReports proves `herold api-key
// create <email> --scope bug-reports` actually mints a key (re #416
// coordinator follow-up: the command used to POST the nonexistent
// /api/v1/api-keys instead of /api/v1/principals/{pid}/api-keys and
// always failed). The target principal is distinct from the caller so
// no TOTP self-service step-up applies to this creation.
func TestCLIAPIKeyCreate_ByEmail_ScopeBugReports(t *testing.T) {
	dir := t.TempDir()
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.BugReportsDir = dir
	})
	seedPrincipal(t, env, "bugfetch@test.local")

	out, stderr, err := env.run("api-key", "create", "bugfetch@test.local",
		"--scope", "bug-reports", "--label", "bug-fetch", "--json")
	if err != nil {
		t.Fatalf("api-key create: %v\nstdout=%s\nstderr=%s", err, out, stderr)
	}
	var created struct {
		ID    uint64   `json:"id"`
		Key   string   `json:"key"`
		Scope []string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode created key: %v: %s", err, out)
	}
	if created.ID == 0 || created.Key == "" {
		t.Fatalf("created key missing id/key: %+v", created)
	}
	if len(created.Scope) != 1 || created.Scope[0] != "bug-reports" {
		t.Fatalf("created key scope = %v, want [bug-reports]", created.Scope)
	}

	// The minted key authenticates the bug-reports list endpoint and
	// nothing else (proven separately by
	// internal/protoadmin/bugreports_test.go's scope tests).
	req, err := http.NewRequest(http.MethodGet, env.httpSrv.URL+"/api/v1/bug-reports", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/bug-reports: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/bug-reports with the minted key: status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"items"`) {
		t.Fatalf("GET /api/v1/bug-reports body missing items: %s", body)
	}
}
