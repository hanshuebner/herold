package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
