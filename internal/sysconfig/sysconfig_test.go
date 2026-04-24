package sysconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalValid = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"
run_as_user = "herold"
run_as_group = "herold"

[server.admin_tls]
source = "file"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name = "smtp-relay"
address = "0.0.0.0:25"
protocol = "smtp"
tls = "starttls"

[[listener]]
name = "admin"
address = "127.0.0.1:8080"
protocol = "admin"
tls = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[observability]
log_format = "json"
log_level = "info"
metrics_bind = "127.0.0.1:9090"
`

func TestParse_Minimal(t *testing.T) {
	cfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if cfg.Server.Hostname != "mail.example.com" {
		t.Errorf("hostname: got %q", cfg.Server.Hostname)
	}
	if len(cfg.Listener) != 2 {
		t.Fatalf("listeners: got %d, want 2", len(cfg.Listener))
	}
	if cfg.Observability.MetricsBind != "127.0.0.1:9090" {
		t.Errorf("metrics bind: got %q", cfg.Observability.MetricsBind)
	}
}

func TestParse_DefaultsApplied(t *testing.T) {
	const bare = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name = "smtp-relay"
address = "0.0.0.0:25"
protocol = "smtp"
tls = "starttls"
`
	cfg, err := Parse([]byte(bare))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Observability.LogFormat != "json" {
		t.Errorf("default log_format: got %q, want json", cfg.Observability.LogFormat)
	}
	if cfg.Observability.LogLevel != "info" {
		t.Errorf("default log_level: got %q, want info", cfg.Observability.LogLevel)
	}
	if cfg.Observability.MetricsBind != "127.0.0.1:9090" {
		t.Errorf("default metrics_bind: got %q", cfg.Observability.MetricsBind)
	}
}

func TestParse_UnknownKeyRejected(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"
future_flag = true

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
	if !strings.Contains(err.Error(), "future_flag") {
		t.Errorf("error should mention offending key, got: %v", err)
	}
}

func TestValidate_AcmeRejected(t *testing.T) {
	const acmeAdmin = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "acme"
acme_account = "default"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(acmeAdmin))
	if err == nil {
		t.Fatal("expected ACME-rejected error")
	}
	if !strings.Contains(err.Error(), "Phase") {
		t.Errorf("error should mention Phase, got: %v", err)
	}
}

func TestValidate_AcmeBlockRejected(t *testing.T) {
	const acmeBlock = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[acme]
email = "ops@example.com"
directory_url = "https://acme-v02.api.letsencrypt.org/directory"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(acmeBlock))
	if err == nil {
		t.Fatal("expected [acme] block rejected")
	}
	if !strings.Contains(err.Error(), "[acme]") {
		t.Errorf("error should name [acme], got: %v", err)
	}
}

func TestValidate_DuplicateListenerName(t *testing.T) {
	const dup = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "same"
address = ":25"
protocol = "smtp"
tls = "starttls"

[[listener]]
name = "same"
address = ":587"
protocol = "smtp-submission"
tls = "starttls"
`
	_, err := Parse([]byte(dup))
	if err == nil {
		t.Fatal("expected duplicate listener name error")
	}
}

func TestValidate_AdminTLSFileMissingCert(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected missing-cert error")
	}
	if !strings.Contains(err.Error(), "cert_file") {
		t.Errorf("error should mention cert_file, got: %v", err)
	}
}

func TestValidate_ListenerCertKeyPairing(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
cert_file = "/x"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected cert/key pairing error")
	}
}

func TestValidate_ListenerCertWithTLSNone(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "none"
cert_file = "/x"
key_file = "/y"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected tls=none + cert error")
	}
}

func TestValidate_BadProtocol(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "gopher"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected bad-protocol error")
	}
}

func TestValidate_BadLogLevel(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"

[observability]
log_level = "loud"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected bad log_level error")
	}
}

func TestValidate_PluginFields(t *testing.T) {
	const withPlugin = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"

[[plugin]]
name = "spam-llm"
path = "/usr/lib/herold/plugins/herold-spam-llm"
type = "spam"
lifecycle = "long-running"
options.endpoint = "http://localhost:11434/v1"
`
	cfg, err := Parse([]byte(withPlugin))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Plugin) != 1 || cfg.Plugin[0].Name != "spam-llm" {
		t.Fatalf("plugin parse: %+v", cfg.Plugin)
	}
	if cfg.Plugin[0].Options["endpoint"] != "http://localhost:11434/v1" {
		t.Errorf("plugin options: %+v", cfg.Plugin[0].Options)
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system.toml")
	if err := os.WriteFile(path, []byte(minimalValid), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Hostname != "mail.example.com" {
		t.Errorf("Load: hostname %q", cfg.Server.Hostname)
	}
}

func TestDiff_NoChanges(t *testing.T) {
	cfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Diff(cfg, cfg)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("expected no changes, got %+v", changes)
	}
}

func TestDiff_DataDirChangeRejected(t *testing.T) {
	oldCfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatal(err)
	}
	newCfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatal(err)
	}
	newCfg.Server.DataDir = "/srv/herold"
	if _, err := Diff(oldCfg, newCfg); !errors.Is(err, ErrCannotApplyLive) {
		t.Fatalf("expected ErrCannotApplyLive, got %v", err)
	}
}

func TestDiff_LogLevelUpdatable(t *testing.T) {
	oldCfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatal(err)
	}
	newCfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatal(err)
	}
	newCfg.Observability.LogLevel = "debug"
	changes, err := Diff(oldCfg, newCfg)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "observability" {
		t.Errorf("expected 1 observability change, got %+v", changes)
	}
}

func TestDiff_ListenerAddRemoveUpdate(t *testing.T) {
	oldCfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatal(err)
	}
	const added = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"
run_as_user = "herold"
run_as_group = "herold"

[server.admin_tls]
source = "file"
cert_file = "/etc/herold/admin.crt"
key_file = "/etc/herold/admin.key"

[[listener]]
name = "smtp-relay"
address = "0.0.0.0:2525"
protocol = "smtp"
tls = "starttls"

[[listener]]
name = "new-one"
address = ":4190"
protocol = "admin"
tls = "implicit"
cert_file = "/a"
key_file = "/b"
`
	newCfg, err := Parse([]byte(added))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Diff(oldCfg, newCfg)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	kinds := map[ChangeKind]int{}
	for _, c := range changes {
		kinds[c.Kind]++
	}
	if kinds[ChangeListenerUpdate] != 1 || kinds[ChangeListenerAdd] != 1 || kinds[ChangeListenerRemove] != 1 {
		t.Errorf("unexpected change set: %+v", changes)
	}
}

func TestResolveSecret_Inline(t *testing.T) {
	got, err := ResolveSecret("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("got %q", got)
	}
}

func TestResolveSecret_Env(t *testing.T) {
	t.Setenv("HEROLD_TEST_SECRET", "s3cret")
	got, err := ResolveSecret("$HEROLD_TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Errorf("env secret: %q", got)
	}
	if _, err := ResolveSecret("$HEROLD_DEFINITELY_UNSET_VAR_XYZ"); err == nil {
		t.Fatal("expected unset-var error")
	}
}

func TestResolveSecret_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("f1le-s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveSecret("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "f1le-s3cret" {
		t.Errorf("file secret: %q", got)
	}
}

func TestResolveSecret_Empty(t *testing.T) {
	if _, err := ResolveSecret(""); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
