package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIDiagCollect_BundleContents(t *testing.T) {
	env := newCLITestEnv(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if _, _, err := env.run("diag", "collect", "--out="+bundleDir); err != nil {
		t.Fatalf("diag collect: %v", err)
	}
	expected := []string{
		"versions.json",
		"queue_stats.json",
		"certs.json",
		"audit_log.json",
		"principals.json",
		"domains.json",
		"server_status.json",
		"README.md",
	}
	for _, f := range expected {
		path := filepath.Join(bundleDir, f)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected bundle file %s: %v", f, err)
		}
		if st.Size() == 0 {
			t.Fatalf("bundle file %s is empty", f)
		}
	}
}

func TestCLIDiagCollect_RedactsSecretsInTOML(t *testing.T) {
	env := newCLITestEnv(t, nil)
	tomlPath := filepath.Join(t.TempDir(), "system.toml")
	tomlBody := `
[server]
hostname = "test.local"

[some_section]
api_key = "hk_secret_value_should_not_leak"
client_secret = "abc-123-leak"
password = "ohnoaplaintextpassword"
nonsecret = "fine to share"
`
	if err := os.WriteFile(tomlPath, []byte(tomlBody), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if _, _, err := env.run("--system-config", tomlPath, "diag", "collect", "--out="+bundleDir); err != nil {
		t.Fatalf("diag collect: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(bundleDir, "system.toml"))
	if err != nil {
		t.Fatalf("read bundle toml: %v", err)
	}
	body := string(got)
	for _, leak := range []string{"hk_secret_value_should_not_leak", "abc-123-leak", "ohnoaplaintextpassword"} {
		if strings.Contains(body, leak) {
			t.Fatalf("bundle leaked secret %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "fine to share") {
		t.Fatalf("non-secret value was clobbered: %s", body)
	}
	if !strings.Contains(body, "<redacted>") {
		t.Fatalf("expected <redacted> placeholder: %s", body)
	}
}
