package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

// withDKIMManager returns an optsMutator that wires a real keymgmt.Manager
// so the protoadmin DKIM endpoints return 200 instead of 501.
func withDKIMManager(env *cliTestEnv) func(*protoadmin.Options) {
	return func(opts *protoadmin.Options) {
		mgr := keymgmt.NewManager(
			env.store.Meta(),
			discardLogger(),
			env.clk,
			nil,
		)
		opts.DKIMKeyManager = mgr
	}
}

func TestCLIDKIMGenerate(t *testing.T) {
	env := newCLITestEnv(t, nil)
	// Wire the DKIM manager after env is constructed.
	rebuildAdminServer(t, env, withDKIMManager(env))

	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "gen.test.local",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}

	// generate — human output.
	out, _, err := env.run("dkim", "generate", "gen.test.local")
	if err != nil {
		t.Fatalf("dkim generate: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "selector") {
		t.Fatalf("expected 'selector' in output: %s", out)
	}
	if !strings.Contains(out, "v=DKIM1") {
		t.Fatalf("expected 'v=DKIM1' in output: %s", out)
	}
}

func TestCLIDKIMGenerate_JSON(t *testing.T) {
	env := newCLITestEnv(t, nil)
	rebuildAdminServer(t, env, withDKIMManager(env))

	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "genj.test.local",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}

	out, _, err := env.run("dkim", "generate", "genj.test.local", "--json")
	if err != nil {
		t.Fatalf("dkim generate --json: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "selector") {
		t.Fatalf("expected 'selector' in JSON output: %s", out)
	}
	if !strings.Contains(out, "txt_record") {
		t.Fatalf("expected 'txt_record' in JSON output: %s", out)
	}
}

func TestCLIDKIMShow(t *testing.T) {
	env := newCLITestEnv(t, nil)
	rebuildAdminServer(t, env, withDKIMManager(env))

	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "show.test.local",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}

	// Generate a key first so show has something to list.
	if _, _, err := env.run("dkim", "generate", "show.test.local"); err != nil {
		t.Fatalf("dkim generate: %v", err)
	}

	out, _, err := env.run("dkim", "show", "show.test.local")
	if err != nil {
		t.Fatalf("dkim show: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "selector") {
		t.Fatalf("expected 'selector' in show output: %s", out)
	}
	if !strings.Contains(out, "v=DKIM1") {
		t.Fatalf("expected 'v=DKIM1' in show output: %s", out)
	}
}

func TestCLIDKIMShow_Empty(t *testing.T) {
	env := newCLITestEnv(t, nil)
	rebuildAdminServer(t, env, withDKIMManager(env))

	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "empty.test.local",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}

	out, _, err := env.run("dkim", "show", "empty.test.local")
	if err != nil {
		t.Fatalf("dkim show empty: %v (out=%s)", err, out)
	}
	if !strings.Contains(out, "no DKIM keys") {
		t.Fatalf("expected 'no DKIM keys' in output: %s", out)
	}
}

func TestCLIDKIMGenerate_UnknownDomain(t *testing.T) {
	env := newCLITestEnv(t, nil)
	rebuildAdminServer(t, env, withDKIMManager(env))

	_, _, err := env.run("dkim", "generate", "ghost.test.local")
	if err == nil {
		t.Fatalf("expected error for unknown domain")
	}
}
