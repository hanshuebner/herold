package admin

import (
	"context"
	"strings"
	"testing"

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
