package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// seedPrincipal inserts a fresh non-admin principal and returns the row.
func seedPrincipal(t *testing.T, env *cliTestEnv, email string) store.Principal {
	t.Helper()
	pid, err := env.store.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return pid
}

func TestCLIPrincipalShow_ByEmail(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "u1@test.local")
	out, _, err := env.run("principal", "show", "u1@test.local", "--json")
	if err != nil {
		t.Fatalf("principal show: %v", err)
	}
	if !strings.Contains(out, "u1@test.local") {
		t.Fatalf("expected email in output: %s", out)
	}
}

func TestCLIPrincipalShow_NotFound(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("principal", "show", "ghost@test.local")
	if err == nil {
		t.Fatalf("expected error for missing principal")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error: %v", err)
	}
}

func TestCLIPrincipalDisableEnable(t *testing.T) {
	env := newCLITestEnv(t, nil)
	p := seedPrincipal(t, env, "tog@test.local")
	if _, _, err := env.run("principal", "disable", "tog@test.local"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err := env.store.Meta().GetPrincipalByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Flags.Has(store.PrincipalFlagDisabled) {
		t.Fatalf("expected PrincipalFlagDisabled to be set; flags=%v", got.Flags)
	}
	if _, _, err := env.run("principal", "enable", "tog@test.local"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, err = env.store.Meta().GetPrincipalByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Flags.Has(store.PrincipalFlagDisabled) {
		t.Fatalf("expected disabled to be cleared; flags=%v", got.Flags)
	}
}

func TestCLIPrincipalQuota(t *testing.T) {
	env := newCLITestEnv(t, nil)
	p := seedPrincipal(t, env, "quota@test.local")
	if _, _, err := env.run("principal", "quota", "quota@test.local", "5G"); err != nil {
		t.Fatalf("quota: %v", err)
	}
	got, err := env.store.Meta().GetPrincipalByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	const expect = int64(5) << 30
	if got.QuotaBytes != expect {
		t.Fatalf("quota: got %d, want %d", got.QuotaBytes, expect)
	}
}

func TestCLIPrincipalQuota_BadValue(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "quota2@test.local")
	_, _, err := env.run("principal", "quota", "quota2@test.local", "notanumber")
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestCLIPrincipalGrantRevokeAdmin(t *testing.T) {
	env := newCLITestEnv(t, nil)
	p := seedPrincipal(t, env, "candidate@test.local")
	if _, _, err := env.run("principal", "grant-admin", "candidate@test.local"); err != nil {
		t.Fatalf("grant-admin: %v", err)
	}
	got, err := env.store.Meta().GetPrincipalByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Flags.Has(store.PrincipalFlagAdmin) {
		t.Fatalf("expected admin flag set; flags=%v", got.Flags)
	}
	if _, _, err := env.run("principal", "revoke-admin", "candidate@test.local"); err != nil {
		t.Fatalf("revoke-admin: %v", err)
	}
	got, err = env.store.Meta().GetPrincipalByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Flags.Has(store.PrincipalFlagAdmin) {
		t.Fatalf("expected admin flag cleared; flags=%v", got.Flags)
	}
}

func TestCLIPrincipalTOTPEnroll(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "totp@test.local")
	out, _, err := env.run("principal", "totp", "enroll", "totp@test.local", "--json")
	if err != nil {
		t.Fatalf("totp enroll: %v", err)
	}
	if !strings.Contains(out, "provisioning_uri") {
		t.Fatalf("expected provisioning_uri in output: %s", out)
	}
}

func TestCLIPrincipalTOTPDisable_RequiresPassword(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "totp2@test.local")
	_, _, err := env.run("principal", "totp", "disable", "totp2@test.local")
	if err == nil {
		t.Fatalf("expected error when --current-password is missing")
	}
	if !strings.Contains(err.Error(), "current-password") {
		t.Fatalf("expected current-password error; got %v", err)
	}
}

func TestParseHumanBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"1024", 1024, false},
		{"1k", 1 << 10, false},
		{"5G", 5 << 30, false},
		{"200M", 200 << 20, false},
		{"1T", 1 << 40, false},
		{"", 0, true},
		{"-5", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseHumanBytes(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseHumanBytes(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseHumanBytes(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseHumanBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSetStringFlag(t *testing.T) {
	got := setStringFlag([]string{"a", "b"}, "c", true)
	if !equalSlices(got, []string{"a", "b", "c"}) {
		t.Errorf("add new: got %v", got)
	}
	got = setStringFlag([]string{"a", "b"}, "b", false)
	if !equalSlices(got, []string{"a"}) {
		t.Errorf("remove existing: got %v", got)
	}
	got = setStringFlag([]string{"a", "b"}, "a", true)
	if !equalSlices(got, []string{"a", "b"}) {
		t.Errorf("idempotent add: got %v", got)
	}
	got = setStringFlag([]string{"a", "b"}, "c", false)
	if !equalSlices(got, []string{"a", "b"}) {
		t.Errorf("idempotent remove: got %v", got)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
