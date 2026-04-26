package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

func TestCLIAuditList(t *testing.T) {
	env := newCLITestEnv(t, nil)
	// Seed one audit entry so the list isn't empty.
	if err := env.store.Meta().AppendAuditLog(context.Background(), store.AuditLogEntry{
		At:        env.clk.Now(),
		ActorKind: store.ActorPrincipal,
		ActorID:   "1",
		Action:    "principal.create",
		Subject:   "principal:1",
		Outcome:   store.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	out, _, err := env.run("audit", "list", "--json")
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if !strings.Contains(out, "principal.create") {
		t.Fatalf("expected seeded action in output: %s", out)
	}
}

func TestCLIAuditList_FilterAction(t *testing.T) {
	env := newCLITestEnv(t, nil)
	if err := env.store.Meta().AppendAuditLog(context.Background(), store.AuditLogEntry{
		At:        env.clk.Now(),
		ActorKind: store.ActorPrincipal,
		ActorID:   "1",
		Action:    "principal.delete",
		Subject:   "principal:2",
		Outcome:   store.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	if err := env.store.Meta().AppendAuditLog(context.Background(), store.AuditLogEntry{
		At:        env.clk.Now(),
		ActorKind: store.ActorPrincipal,
		ActorID:   "1",
		Action:    "alias.create",
		Subject:   "alias:1",
		Outcome:   store.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("AppendAuditLog: %v", err)
	}
	out, _, err := env.run("audit", "list", "--action", "alias.create", "--json")
	if err != nil {
		t.Fatalf("audit list --action: %v", err)
	}
	if !strings.Contains(out, "alias.create") {
		t.Fatalf("expected alias.create in output: %s", out)
	}
	if strings.Contains(out, "principal.delete") {
		t.Fatalf("did not expect principal.delete in filtered output: %s", out)
	}
}

func TestCLIAuditList_ResourceWarn(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, stderr, err := env.run("audit", "list", "--resource", "any", "--json")
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	if !strings.Contains(stderr, "--resource is not wired") {
		t.Fatalf("expected resource-not-wired warning; stderr=%q", stderr)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
		err  bool
	}{
		{"1h", now.Add(-time.Hour), false},
		{"30m", now.Add(-30 * time.Minute), false},
		{"2026-04-24T10:00:00Z", time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC), false},
		{"-1h", time.Time{}, true},
		{"foo", time.Time{}, true},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, now)
		if c.err {
			if err == nil {
				t.Errorf("parseSince(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSince(%q): unexpected err: %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
