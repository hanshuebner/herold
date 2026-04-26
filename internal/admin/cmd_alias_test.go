package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

func TestCLIAliasAddListDelete(t *testing.T) {
	env := newCLITestEnv(t, nil)
	// Seed a domain so InsertAlias's domain check passes.
	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "test.local",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	seedPrincipal(t, env, "target@test.local")

	out, _, err := env.run("alias", "add", "alias@test.local", "target@test.local", "--json")
	if err != nil {
		t.Fatalf("alias add: %v", err)
	}
	if !strings.Contains(out, "alias") {
		t.Fatalf("expected alias in add output: %s", out)
	}

	out, _, err = env.run("alias", "list", "--json")
	if err != nil {
		t.Fatalf("alias list: %v", err)
	}
	if !strings.Contains(out, "alias") {
		t.Fatalf("expected alias in list output: %s", out)
	}

	// list --domain
	out, _, err = env.run("alias", "list", "--domain", "test.local", "--json")
	if err != nil {
		t.Fatalf("alias list --domain: %v", err)
	}
	if !strings.Contains(out, "alias") {
		t.Fatalf("expected alias in domain-scoped list: %s", out)
	}

	// Pull aliases from the store to learn the inserted id.
	rows, err := env.store.Meta().ListAliases(context.Background(), "test.local")
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 alias row, got %d", len(rows))
	}
	id := rows[0].ID
	if _, _, err := env.run("alias", "delete",
		strings.TrimSpace(itoa(uint64(id)))); err != nil {
		t.Fatalf("alias delete: %v", err)
	}
	rows, err = env.store.Meta().ListAliases(context.Background(), "test.local")
	if err != nil {
		t.Fatalf("ListAliases post-delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 aliases after delete, got %d", len(rows))
	}
}

func TestCLIAliasAdd_BadAddress(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "target@test.local")
	_, _, err := env.run("alias", "add", "noatsign", "target@test.local")
	if err == nil {
		t.Fatalf("expected error for bad addr")
	}
	if !strings.Contains(err.Error(), "local@domain") {
		t.Fatalf("expected local@domain in error: %v", err)
	}
}

func TestCLIAliasAdd_UnknownTarget(t *testing.T) {
	env := newCLITestEnv(t, nil)
	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "test.local",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	_, _, err := env.run("alias", "add", "alias@test.local", "ghost@test.local")
	if err == nil {
		t.Fatalf("expected error for unknown target")
	}
}

func TestCLIAliasDelete_NotFound(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("alias", "delete", "9999")
	if err == nil {
		t.Fatalf("expected error for missing id")
	}
}

func TestSplitEmail(t *testing.T) {
	cases := []struct {
		in       string
		local    string
		domain   string
		wantsErr bool
	}{
		{"a@b", "a", "b", false},
		{"first.last@sub.example.org", "first.last", "sub.example.org", false},
		{"noatsign", "", "", true},
		{"@nolocal", "", "", true},
		{"nodomain@", "", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		l, d, err := splitEmail(c.in)
		if c.wantsErr {
			if err == nil {
				t.Errorf("splitEmail(%q): expected error", c.in)
			}
			continue
		}
		if err != nil || l != c.local || d != c.domain {
			t.Errorf("splitEmail(%q) = %q, %q, %v; want %q, %q", c.in, l, d, err, c.local, c.domain)
		}
	}
}

// itoa returns a base-10 representation; kept local to avoid importing
// strconv just for one call.
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
