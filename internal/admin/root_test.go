package admin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// TestRequireConfig_BadAssetDir verifies that when a config file exists but
// references a [server.tabard] asset_dir that is missing index.html, the
// error returned by requireConfig contains the asset_dir diagnostic
// ("asset_dir" and "missing index.html") and does NOT contain the
// misleading "config file not found" message.
//
// This is a regression test for the Bug A fix: sysconfig validation errors
// involving missing files were previously wrapped with %w, causing
// requireConfig's errors.Is(os.ErrNotExist) check to re-classify them as a
// missing config file.
func TestRequireConfig_BadAssetDir(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})

	// Create a real asset_dir but do NOT create index.html inside it.
	assetDir := filepath.Join(dir, "spa_dist")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir asset_dir: %v", err)
	}

	tomlContent := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.tabard]
asset_dir = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, certPath, keyPath, assetDir, filepath.Join(dir, "db.sqlite"))

	cfgPath := filepath.Join(dir, "system.toml")
	if err := os.WriteFile(cfgPath, []byte(tomlContent), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}

	g := &globalOptions{configPath: cfgPath}
	_, err := requireConfig(g)
	if err == nil {
		t.Fatal("requireConfig: expected error, got nil")
	}

	msg := err.Error()
	if strings.Contains(msg, "config file not found") {
		t.Errorf("requireConfig returned misleading 'config file not found' message: %v", err)
	}
	if !strings.Contains(msg, "asset_dir") {
		t.Errorf("requireConfig error should mention 'asset_dir': %v", err)
	}
	if !strings.Contains(msg, "missing index.html") {
		t.Errorf("requireConfig error should mention 'missing index.html': %v", err)
	}
}

// TestRequireConfig_MissingFile verifies that when the config path itself does
// not exist, requireConfig still returns the "config file not found" message.
func TestRequireConfig_MissingFile(t *testing.T) {
	g := &globalOptions{configPath: "/definitely/does/not/exist/system.toml"}
	_, err := requireConfig(g)
	if err == nil {
		t.Fatal("requireConfig: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Errorf("requireConfig should say 'config file not found': %v", err)
	}
}

// readCredFile is a test helper that parses a credentials TOML file.
func readCredFile(t *testing.T, p string) credentialsFile {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("readCredFile: %v", err)
	}
	var f credentialsFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("readCredFile: unmarshal: %v", err)
	}
	return f
}

// TestSaveCredentials_WritesNewFile verifies that saveCredentials creates the
// file when it does not yet exist and writes both api_key and server_url.
func TestSaveCredentials_WritesNewFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "credentials.toml")
	SetCredentialsPath(p)
	t.Cleanup(func() { SetCredentialsPath("") })

	_, err := saveCredentials("key123", "http://127.0.0.1:9080", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	f := readCredFile(t, p)
	if f.APIKey != "key123" {
		t.Errorf("api_key: got %q", f.APIKey)
	}
	if f.ServerURL != "http://127.0.0.1:9080" {
		t.Errorf("server_url: got %q", f.ServerURL)
	}
}

// TestSaveCredentials_DoesNotClobberExistingServerURL verifies the
// don't-clobber rule: an existing non-empty server_url is preserved even
// when a different URL is supplied.
func TestSaveCredentials_DoesNotClobberExistingServerURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "credentials.toml")
	SetCredentialsPath(p)
	t.Cleanup(func() { SetCredentialsPath("") })

	// Pre-populate file with a custom server_url.
	initial := credentialsFile{APIKey: "oldkey", ServerURL: "https://custom.example.com:9443"}
	raw, _ := toml.Marshal(initial)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatalf("write initial credentials: %v", err)
	}

	// Call saveCredentials with a new key and a different URL. The
	// don't-clobber rule will preserve the existing server_url and emit a
	// warning; discard it here (the warning behaviour is tested separately).
	if _, err := saveCredentials("newkey", "http://127.0.0.1:9080", &bytes.Buffer{}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	f := readCredFile(t, p)
	if f.APIKey != "newkey" {
		t.Errorf("api_key: got %q, want newkey", f.APIKey)
	}
	// The existing custom server_url must survive.
	if f.ServerURL != "https://custom.example.com:9443" {
		t.Errorf("server_url: got %q, want https://custom.example.com:9443", f.ServerURL)
	}
}

// TestSaveCredentials_PopulatesEmptyServerURL verifies that an existing file
// with an empty server_url gets it filled in.
func TestSaveCredentials_PopulatesEmptyServerURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "credentials.toml")
	SetCredentialsPath(p)
	t.Cleanup(func() { SetCredentialsPath("") })

	// Pre-populate with only an api_key (no server_url).
	initial := credentialsFile{APIKey: "oldkey"}
	raw, _ := toml.Marshal(initial)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatalf("write initial credentials: %v", err)
	}

	if _, err := saveCredentials("newkey", "http://127.0.0.1:9080", &bytes.Buffer{}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	f := readCredFile(t, p)
	if f.ServerURL != "http://127.0.0.1:9080" {
		t.Errorf("server_url: got %q, want http://127.0.0.1:9080", f.ServerURL)
	}
}

// TestSaveCredentials_NoServerURL verifies that passing an empty serverURL
// and having no pre-existing file results in an omitted server_url field.
func TestSaveCredentials_NoServerURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "credentials.toml")
	SetCredentialsPath(p)
	t.Cleanup(func() { SetCredentialsPath("") })

	if _, err := saveCredentials("mykey", "", &bytes.Buffer{}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	f := readCredFile(t, p)
	if f.APIKey != "mykey" {
		t.Errorf("api_key: got %q", f.APIKey)
	}
	if f.ServerURL != "" {
		t.Errorf("server_url: got %q, want empty", f.ServerURL)
	}
}
