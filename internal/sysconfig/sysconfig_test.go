package sysconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalNoObservability is a base config without any [observability] or
// [[log.sink]] blocks; used for log-sink specific tests so we can add sinks
// cleanly without conflicting with the legacy-shim path.
const minimalNoObs = `
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
name = "public"
address = "0.0.0.0:443"
protocol = "admin"
kind = "public"
tls = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name = "admin"
address = "127.0.0.1:9443"
protocol = "admin"
kind = "admin"
tls = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"
`

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
name = "public"
address = "0.0.0.0:443"
protocol = "admin"
kind = "public"
tls = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name = "admin"
address = "127.0.0.1:9443"
protocol = "admin"
kind = "admin"
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
	if len(cfg.Listener) != 3 {
		t.Fatalf("listeners: got %d, want 3", len(cfg.Listener))
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
	if cfg.Server.Snooze.PollInterval == 0 {
		t.Errorf("default snooze poll_interval: got 0, want 60s default")
	}
	// Chat ephemeral channel defaults (REQ-CHAT-40..46).
	if cfg.Server.Chat.Enabled == nil || !*cfg.Server.Chat.Enabled {
		t.Errorf("default chat enabled: want true")
	}
	if cfg.Server.Chat.MaxConnections != 4096 {
		t.Errorf("default chat max_connections: got %d", cfg.Server.Chat.MaxConnections)
	}
	if cfg.Server.Chat.PerPrincipalCap != 8 {
		t.Errorf("default chat per_principal_cap: got %d", cfg.Server.Chat.PerPrincipalCap)
	}
	if cfg.Server.Chat.PingIntervalSeconds != 30 {
		t.Errorf("default chat ping_interval_seconds: got %d", cfg.Server.Chat.PingIntervalSeconds)
	}
	if cfg.Server.Chat.PongTimeoutSeconds != 60 {
		t.Errorf("default chat pong_timeout_seconds: got %d", cfg.Server.Chat.PongTimeoutSeconds)
	}
	if cfg.Server.Chat.MaxFrameBytes != 65536 {
		t.Errorf("default chat max_frame_bytes: got %d", cfg.Server.Chat.MaxFrameBytes)
	}
}

func TestValidate_RejectsChatPongBelowPing(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.chat]
ping_interval_seconds = 60
pong_timeout_seconds = 30

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for pong < ping")
	}
	if !strings.Contains(err.Error(), "chat") {
		t.Fatalf("error %q should mention chat", err.Error())
	}
}

func TestParse_ChatRetentionDefaults(t *testing.T) {
	const bare = `
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
`
	cfg, err := Parse([]byte(bare))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.Chat.WriteTimeoutSeconds != 10 {
		t.Errorf("default chat write_timeout_seconds: got %d, want 10",
			cfg.Server.Chat.WriteTimeoutSeconds)
	}
	if cfg.Server.Chat.Retention.SweepIntervalSeconds != 60 {
		t.Errorf("default chat retention sweep_interval_seconds: got %d, want 60",
			cfg.Server.Chat.Retention.SweepIntervalSeconds)
	}
	if cfg.Server.Chat.Retention.BatchSize != 1000 {
		t.Errorf("default chat retention batch_size: got %d, want 1000",
			cfg.Server.Chat.Retention.BatchSize)
	}
}

func TestValidate_ChatRetentionRejectsLowSweep(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.chat.retention]
sweep_interval_seconds = 5
batch_size = 1000

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected sweep_interval_seconds floor error")
	}
	if !strings.Contains(err.Error(), "sweep_interval_seconds") {
		t.Errorf("error should name sweep_interval_seconds, got: %v", err)
	}
}

func TestValidate_ChatRetentionRejectsHighSweep(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.chat.retention]
sweep_interval_seconds = 90000
batch_size = 1000

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected sweep_interval_seconds ceiling error")
	}
	if !strings.Contains(err.Error(), "sweep_interval_seconds") {
		t.Errorf("error should name sweep_interval_seconds, got: %v", err)
	}
}

func TestValidate_ChatRetentionRejectsOversizedBatch(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.chat.retention]
sweep_interval_seconds = 60
batch_size = 100000

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected batch_size ceiling error")
	}
	if !strings.Contains(err.Error(), "batch_size") {
		t.Errorf("error should name batch_size, got: %v", err)
	}
}

func TestIsLoopbackBindAddr(t *testing.T) {
	cases := map[string]bool{
		"":               true,
		"127.0.0.1:9090": true,
		"localhost:9090": true,
		"[::1]:9090":     true,
		"0.0.0.0:9090":   false,
		"192.168.1.10:9": false,
		"not_a_bind":     true, // unparseable: don't warn
	}
	for in, want := range cases {
		if got := isLoopbackBindAddr(in); got != want {
			t.Errorf("isLoopbackBindAddr(%q): got %v, want %v", in, got, want)
		}
	}
}

func TestValidate_RejectsSubFiveSecondSnoozePoll(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.snooze]
poll_interval = "1s"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected error for poll_interval=1s, got nil")
	}
	if !strings.Contains(err.Error(), "snooze") {
		t.Fatalf("error %q should mention snooze", err.Error())
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

func TestValidate_AcmeAdminTLS_AcceptedWithAcmeBlock(t *testing.T) {
	// REQ-OPS-40: source="acme" is accepted when [acme] block is present.
	const acmeAdmin = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "acme"

[acme]
email = "ops@example.com"
directory_url = "https://acme-v02.api.letsencrypt.org/directory"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	cfg, err := Parse([]byte(acmeAdmin))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Server.AdminTLS.Source != "acme" {
		t.Errorf("expected source=acme, got %q", cfg.Server.AdminTLS.Source)
	}
}

func TestValidate_AcmeAdminTLS_RequiresAcmeBlock(t *testing.T) {
	// source="acme" without an [acme] block must be rejected.
	const noAcme = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "acme"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(noAcme))
	if err == nil {
		t.Fatal("expected error for acme source without [acme] block")
	}
	if !strings.Contains(err.Error(), "[acme]") {
		t.Errorf("error should mention [acme], got: %v", err)
	}
}

func TestValidate_AcmeBlock_Accepted(t *testing.T) {
	// REQ-OPS-50: [acme] block with email is accepted.
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
	cfg, err := Parse([]byte(acmeBlock))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Acme == nil || cfg.Acme.Email != "ops@example.com" {
		t.Errorf("expected acme.email = ops@example.com, got %v", cfg.Acme)
	}
}

func TestValidate_AcmeBlock_MissingEmail(t *testing.T) {
	const noEmail = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[acme]
directory_url = "https://acme-v02.api.letsencrypt.org/directory"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(noEmail))
	if err == nil {
		t.Fatal("expected error for missing email")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Errorf("error should mention email, got: %v", err)
	}
}

func TestValidate_AcmeBlock_DNS01RequiresPlugin(t *testing.T) {
	const dns01 = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[acme]
email = "ops@example.com"
challenge_type = "dns-01"

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(dns01))
	if err == nil {
		t.Fatal("expected error for dns-01 without dns_plugin")
	}
	if !strings.Contains(err.Error(), "dns_plugin") {
		t.Errorf("error should mention dns_plugin, got: %v", err)
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

func TestValidate_ManageSieveProtocol(t *testing.T) {
	const ok = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "managesieve"
address = ":4190"
protocol = "managesieve"
tls = "starttls"
`
	_, err := Parse([]byte(ok))
	if err != nil {
		t.Fatalf("managesieve protocol must validate; got %v", err)
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
name = "public"
address = "0.0.0.0:443"
protocol = "admin"
kind = "public"
tls = "implicit"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "new-one"
address = ":4190"
protocol = "admin"
kind = "admin"
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
	// minimalValid carries smtp-relay + public + admin; the new
	// fixture replaces public/admin with public+new-one and changes
	// smtp-relay's address. So we expect: 2 updates (smtp-relay,
	// public) + 1 add (new-one) + 1 remove (admin).
	if kinds[ChangeListenerUpdate] != 2 || kinds[ChangeListenerAdd] != 1 || kinds[ChangeListenerRemove] != 1 {
		t.Errorf("unexpected change set: %+v", changes)
	}
}

// TestValidate_AdminListenerKindRequired verifies REQ-OPS-ADMIN-LISTENER-01:
// a config that mounts an HTTP listener without an explicit kind is
// rejected with a clear migration message, unless [server.dev_mode] is on.
func TestValidate_AdminListenerKindRequired(t *testing.T) {
	const noKind = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "admin"
address = ":443"
protocol = "admin"
tls = "implicit"
cert_file = "/a"
key_file = "/b"
`
	if _, err := Parse([]byte(noKind)); err == nil {
		t.Fatalf("expected error when admin listener lacks kind")
	} else if !strings.Contains(err.Error(), "REQ-OPS-ADMIN-LISTENER-01") {
		t.Errorf("error should reference REQ-OPS-ADMIN-LISTENER-01: %v", err)
	}
}

// TestValidate_DevModeAllowsCoMount verifies that [server.dev_mode] = true
// permits a single HTTP listener that co-mounts public + admin handlers.
func TestValidate_DevModeAllowsCoMount(t *testing.T) {
	const dev = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"
dev_mode = true

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "admin"
address = "127.0.0.1:8080"
protocol = "admin"
tls = "none"
`
	cfg, err := Parse([]byte(dev))
	if err != nil {
		t.Fatalf("dev_mode co-mount: %v", err)
	}
	if !cfg.Server.DevMode {
		t.Errorf("DevMode lost in parse")
	}
}

// TestValidate_MissingAdminKindWithPublicSet rejects a config that only
// declares a public-kind listener (admin would be co-mounted, which is
// the bug we're guarding against).
func TestValidate_MissingAdminKindWithPublicSet(t *testing.T) {
	const onlyPublic = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "public"
address = "0.0.0.0:443"
protocol = "admin"
kind = "public"
tls = "implicit"
cert_file = "/a"
key_file = "/b"
`
	if _, err := Parse([]byte(onlyPublic)); err == nil {
		t.Fatalf("expected error when admin-kind listener missing")
	} else if !strings.Contains(err.Error(), "kind=\"admin\"") {
		t.Errorf("error should mention required admin kind: %v", err)
	}
}

// TestValidate_RejectsKindOnNonAdmin checks that kind="..." on an SMTP
// listener fails (kind only applies to HTTP listeners).
func TestValidate_RejectsKindOnNonAdmin(t *testing.T) {
	const smtpKind = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "smtp"
address = ":25"
protocol = "smtp"
kind = "public"
tls = "starttls"
`
	if _, err := Parse([]byte(smtpKind)); err == nil {
		t.Fatalf("expected error when kind set on smtp listener")
	} else if !strings.Contains(err.Error(), "kind") {
		t.Errorf("error should mention kind: %v", err)
	}
}

func TestResolveSecret_Inline(t *testing.T) {
	// Legacy ResolveSecret keeps inline values working for
	// backwards compatibility with callers that intentionally hold
	// non-secret literal strings (model names, endpoints).
	got, err := ResolveSecret("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("got %q", got)
	}
}

func TestResolveSecretStrict_RefusesInline(t *testing.T) {
	// STANDARDS §9: ResolveSecretStrict refuses bare literal values.
	// Operators must wrap secrets in $ENV or file:/path references.
	if _, err := ResolveSecretStrict("literal_value"); err == nil {
		t.Fatal("expected ErrInlineSecretRefused, got nil")
	} else if !errors.Is(err, ErrInlineSecretRefused) {
		t.Errorf("wrong error: %v", err)
	}

	t.Setenv("HEROLD_TEST_STRICT", "ok")
	if got, err := ResolveSecretStrict("$HEROLD_TEST_STRICT"); err != nil || got != "ok" {
		t.Errorf("env path: got=%q err=%v", got, err)
	}
}

func TestIsSecretReference(t *testing.T) {
	cases := map[string]bool{
		"":            false,
		"literal":     false,
		"$ENV":        true,
		"$":           false,
		"file:/path":  true,
		"file:":       false,
		"file:/etc/x": true,
		"$HEROLD_X_Y": true,
	}
	for in, want := range cases {
		if got := IsSecretReference(in); got != want {
			t.Errorf("IsSecretReference(%q): got %v, want %v", in, got, want)
		}
	}
}

func TestValidate_RejectsInlinePluginSecret(t *testing.T) {
	// STANDARDS §9 + Wave-4 review: a plugin option whose key looks
	// like a secret but whose value is a bare literal MUST fail
	// Load. The error message must name the offending key so the
	// operator can find it.
	const cfg = `
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
options.api_token = "literal_value"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected inline-secret rejection, got nil")
	}
	if !strings.Contains(err.Error(), "api_token") {
		t.Errorf("error should name api_token, got: %v", err)
	}
	if !strings.Contains(err.Error(), "STANDARDS") {
		t.Errorf("error should cite STANDARDS, got: %v", err)
	}
}

func TestValidate_AcceptsReferencedPluginSecret(t *testing.T) {
	// Symmetric positive: the same shape with a "$ENV" reference
	// passes validation. (We do not require the env to be set at
	// Load — that resolves at plugin-start time.)
	const cfg = `
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
options.api_token = "$HEROLD_API_TOKEN"
`
	if _, err := Parse([]byte(cfg)); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestValidate_NonSecretKeyAllowsLiteral(t *testing.T) {
	// Non-secret-shaped keys (endpoints, model names) keep working
	// as bare literals so operators don't have to wrap public values.
	const cfg = `
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
options.model = "llama3.2"
`
	if _, err := Parse([]byte(cfg)); err != nil {
		t.Fatalf("expected pass for non-secret-keyed options, got %v", err)
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

// smartHostBaseTOML is the minimum-viable TOML used as a base for the
// smart-host validation matrix. Tests append a [server.smart_host]
// block to it via fmt.Sprintf or string concatenation.
const smartHostBaseTOML = `
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
`

func TestSmartHost_DisabledIsAccepted(t *testing.T) {
	cfg, err := Parse([]byte(smartHostBaseTOML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.SmartHost.Enabled {
		t.Errorf("expected disabled by default")
	}
}

func TestSmartHost_HappyPath_PLAINEnv(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "plain"
username = "user@example.com"
password_env = "$SMARTHOST_PW"
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sh := cfg.Server.SmartHost
	if sh.TLSMode != "starttls" {
		t.Errorf("default tls_mode for 587: got %q want starttls", sh.TLSMode)
	}
	if sh.FallbackPolicy != "smart_host_only" {
		t.Errorf("default fallback_policy: got %q", sh.FallbackPolicy)
	}
	if sh.TLSVerifyMode != "system_roots" {
		t.Errorf("default tls_verify_mode: got %q", sh.TLSVerifyMode)
	}
	if sh.ConnectTimeoutSeconds != 10 || sh.ReadTimeoutSeconds != 30 || sh.FallbackAfterFailureSeconds != 300 {
		t.Errorf("timeouts: %+v", sh)
	}
}

func TestSmartHost_DefaultTLSModeFor465(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 465
auth_method = "login"
username = "u"
password_env = "$SH"
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.SmartHost.TLSMode != "implicit_tls" {
		t.Errorf("default tls_mode for 465: got %q want implicit_tls", cfg.Server.SmartHost.TLSMode)
	}
}

func TestSmartHost_RejectsInlinePassword(t *testing.T) {
	// Cannot be expressed via the TOML field name (no `password`
	// field exists). The check fires when an operator misuses
	// password_env with a literal: it is not a "$VAR" reference, so
	// IsSecretReference returns false and validate refuses.
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "plain"
username = "u"
password_env = "literal-secret"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected validate error for inline secret")
	}
}

func TestSmartHost_RequiresUsername_WhenAuth(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "plain"
password_env = "$SH"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected error: username required")
	}
}

func TestSmartHost_RequiresExactlyOnePasswordSlot(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "plain"
username = "u"
password_env = "$SH"
password_file = "/etc/herold/sh_pw"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected error: both password_env and password_file set")
	}
}

func TestSmartHost_TLSNoneRefusesAuth(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 25
tls_mode = "none"
auth_method = "plain"
username = "u"
password_env = "$SH"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected error: tls_mode=none with auth")
	}
}

func TestSmartHost_TLSNoneAuthNoneAccepted(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 25
tls_mode = "none"
auth_method = "none"
`
	if _, err := Parse([]byte(tomlSrc)); err != nil {
		t.Fatalf("expected validate to accept tls=none auth=none, got %v", err)
	}
}

func TestSmartHost_PinnedRequiresPath(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "none"
tls_verify_mode = "pinned"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected error: pinned without pinned_cert_path")
	}
}

func TestSmartHost_BadEnums(t *testing.T) {
	cases := []string{
		`[server.smart_host]
enabled = true
host = "h"
port = 587
auth_method = "none"
tls_mode = "wat"
`,
		`[server.smart_host]
enabled = true
host = "h"
port = 587
auth_method = "wat"
`,
		`[server.smart_host]
enabled = true
host = "h"
port = 587
auth_method = "none"
fallback_policy = "wat"
`,
		`[server.smart_host]
enabled = true
host = "h"
port = 587
auth_method = "none"
tls_verify_mode = "wat"
`,
	}
	for i, frag := range cases {
		if _, err := Parse([]byte(smartHostBaseTOML + frag)); err == nil {
			t.Errorf("case %d: expected validate failure", i)
		}
	}
}

func TestSmartHost_PerDomainOverride(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "plain"
username = "u"
password_env = "$SH"

[server.smart_host.per_domain."corp.example.com"]
host = "corp-relay.internal"
port = 465
auth_method = "login"
username = "corp-user"
password_file = "/etc/herold/corp_password"
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ov, ok := cfg.Server.SmartHost.PerDomain["corp.example.com"]
	if !ok {
		t.Fatalf("override not parsed: %+v", cfg.Server.SmartHost.PerDomain)
	}
	if ov.Host != "corp-relay.internal" || ov.Port != 465 || ov.TLSMode != "implicit_tls" {
		t.Errorf("override: %+v", ov)
	}
}

func TestSmartHost_PerDomainNestedRejected(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "none"

[server.smart_host.per_domain."corp.example.com"]
host = "corp-relay.internal"
port = 587
auth_method = "none"

[server.smart_host.per_domain."corp.example.com".per_domain."deep.example.com"]
host = "deeper"
port = 587
auth_method = "none"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected error: nested per_domain rejected")
	}
}

func TestSmartHost_PerDomainCaseSensitive(t *testing.T) {
	const tomlSrc = smartHostBaseTOML + `
[server.smart_host]
enabled = true
host = "smtp.example.com"
port = 587
auth_method = "none"

[server.smart_host.per_domain."CORP.example.com"]
host = "corp-relay.internal"
port = 587
auth_method = "none"
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected error: uppercase domain key rejected")
	}
}

func TestValidate_SMTPInbound_AcceptsDirectoryPlugin(t *testing.T) {
	src := minimalValid + `
[[plugin]]
name = "app-rcpt"
path = "/var/lib/herold/plugins/app-rcpt/app-rcpt"
type = "directory"
lifecycle = "long-running"

[smtp.inbound]
directory_resolve_rcpt_plugin = "app-rcpt"
plugin_first_for_domains = ["app.example.com"]
rcpt_rate_limit_per_ip_per_sec = 100
spam_for_synthetic = false
`
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SMTP.Inbound.DirectoryResolveRcptPlugin != "app-rcpt" {
		t.Fatalf("plugin name: %q", cfg.SMTP.Inbound.DirectoryResolveRcptPlugin)
	}
	if cfg.SMTP.Inbound.RcptRateLimitPerIPPerSec != 100 {
		t.Fatalf("rate limit: %d", cfg.SMTP.Inbound.RcptRateLimitPerIPPerSec)
	}
}

func TestValidate_SMTPInbound_RejectsUnknownPlugin(t *testing.T) {
	src := minimalValid + `
[smtp.inbound]
directory_resolve_rcpt_plugin = "missing"
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("expected error: unknown plugin name")
	} else if !strings.Contains(err.Error(), "directory_resolve_rcpt_plugin") {
		t.Fatalf("error should mention directory_resolve_rcpt_plugin: %v", err)
	}
}

func TestValidate_SMTPInbound_RejectsWrongPluginType(t *testing.T) {
	src := minimalValid + `
[[plugin]]
name = "spammer"
path = "/p"
type = "spam"
lifecycle = "long-running"

[smtp.inbound]
directory_resolve_rcpt_plugin = "spammer"
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("expected error: wrong plugin type")
	}
}

func TestValidate_SMTPInbound_RejectsTimeoutAboveCap(t *testing.T) {
	src := minimalValid + `
[smtp.inbound]
resolve_rcpt_timeout = "10s"
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("expected error: 10s exceeds 5s hard cap")
	} else if !strings.Contains(err.Error(), "hard cap") {
		t.Fatalf("error should mention hard cap: %v", err)
	}
}

func TestValidate_SMTPInbound_RejectsNegativeRateLimit(t *testing.T) {
	src := minimalValid + `
[smtp.inbound]
rcpt_rate_limit_per_ip_per_sec = -1
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("expected error: negative rate limit")
	}
}

func TestValidate_SMTPInbound_RejectsUppercaseDomain(t *testing.T) {
	src := minimalValid + `
[smtp.inbound]
plugin_first_for_domains = ["App.Example.com"]
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("expected error: uppercase domain")
	}
}

// TestValidate_QueueConfig_RejectsNegativeConcurrency asserts that a
// negative concurrency value is rejected at parse time.
func TestValidate_QueueConfig_RejectsNegativeConcurrency(t *testing.T) {
	src := minimalValid + `
[server.queue]
concurrency = -1
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for concurrency = -1, got nil")
	}
	if !strings.Contains(err.Error(), "concurrency") {
		t.Errorf("error should mention concurrency: %v", err)
	}
}

// TestValidate_QueueConfig_AcceptsZeroConcurrency verifies that concurrency
// = 0 is valid (means "use built-in default").
func TestValidate_QueueConfig_AcceptsZeroConcurrency(t *testing.T) {
	src := minimalValid + `
[server.queue]
concurrency = 0
`
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("expected concurrency = 0 to parse cleanly, got: %v", err)
	}
}

// TestValidate_QueueConfig_AcceptsNonZeroConcurrency verifies that a valid
// concurrency value is accepted and reflected in the parsed config.
func TestValidate_QueueConfig_AcceptsNonZeroConcurrency(t *testing.T) {
	src := minimalValid + `
[server.queue]
concurrency = 64
per_host_max = 8
`
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Queue.Concurrency != 64 {
		t.Errorf("concurrency: got %d, want 64", cfg.Server.Queue.Concurrency)
	}
	if cfg.Server.Queue.PerHostMax != 8 {
		t.Errorf("per_host_max: got %d, want 8", cfg.Server.Queue.PerHostMax)
	}
}

// TestValidate_QueueConfig_RejectsExcessiveConcurrency verifies that a
// concurrency above the 1024 sanity cap is rejected.
func TestValidate_QueueConfig_RejectsExcessiveConcurrency(t *testing.T) {
	src := minimalValid + `
[server.queue]
concurrency = 2048
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for concurrency = 2048, got nil")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Errorf("error should mention ceiling: %v", err)
	}
}

// TestValidate_QueueConfig_RejectsPerHostMaxExceedingConcurrency verifies
// that per_host_max > concurrency is rejected when both are non-zero.
func TestValidate_QueueConfig_RejectsPerHostMaxExceedingConcurrency(t *testing.T) {
	src := minimalValid + `
[server.queue]
concurrency = 16
per_host_max = 32
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for per_host_max > concurrency, got nil")
	}
	if !strings.Contains(err.Error(), "per_host_max") {
		t.Errorf("error should mention per_host_max: %v", err)
	}
}

// TestAdminRESTURL covers the derivation helper that bootstrap uses to
// write server_url into ~/.herold/credentials.toml.
func TestAdminRESTURL(t *testing.T) {
	tests := []struct {
		name         string
		listeners    []ListenerConfig
		wantURL      string
		wantOK       bool
		wantWarnings bool // true when the warnings slice must be non-empty
	}{
		{
			name: "tls_none_produces_http",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "127.0.0.1:9080", Protocol: "admin", TLS: "none"},
			},
			wantURL: "http://127.0.0.1:9080",
			wantOK:  true,
		},
		{
			name: "tls_starttls_produces_https",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "127.0.0.1:9443", Protocol: "admin", TLS: "starttls"},
			},
			wantURL: "https://127.0.0.1:9443",
			wantOK:  true,
		},
		{
			name: "tls_implicit_produces_https",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "127.0.0.1:9443", Protocol: "admin", TLS: "implicit"},
			},
			wantURL: "https://127.0.0.1:9443",
			wantOK:  true,
		},
		{
			// 0.0.0.0 is not loopback, so tls=none triggers the cleartext warning.
			name: "wildcard_ipv4_translates_to_loopback",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "0.0.0.0:9080", Protocol: "admin", TLS: "none"},
			},
			wantURL:      "http://127.0.0.1:9080",
			wantOK:       true,
			wantWarnings: true,
		},
		{
			// [::] is not loopback, so tls=none triggers the cleartext warning.
			name: "wildcard_ipv6_translates_to_loopback",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "[::]:9080", Protocol: "admin", TLS: "none"},
			},
			wantURL:      "http://[::1]:9080",
			wantOK:       true,
			wantWarnings: true,
		},
		{
			name: "explicit_hostname_passed_through",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "mail.example.com:9443", Protocol: "admin", TLS: "implicit"},
			},
			wantURL: "https://mail.example.com:9443",
			wantOK:  true,
		},
		{
			name: "no_admin_listener_returns_false",
			listeners: []ListenerConfig{
				{Kind: "public", Address: "0.0.0.0:443", Protocol: "admin", TLS: "implicit"},
			},
			wantURL: "",
			wantOK:  false,
		},
		{
			name:      "empty_listeners_returns_false",
			listeners: nil,
			wantURL:   "",
			wantOK:    false,
		},
		{
			name: "first_admin_listener_wins",
			listeners: []ListenerConfig{
				{Kind: "public", Address: "0.0.0.0:443", Protocol: "admin", TLS: "implicit"},
				{Kind: "admin", Address: "127.0.0.1:9080", Protocol: "admin", TLS: "none"},
				{Kind: "admin", Address: "127.0.0.1:9443", Protocol: "admin", TLS: "implicit"},
			},
			wantURL: "http://127.0.0.1:9080",
			wantOK:  true,
		},
		{
			// REQ-OPS-01 security: tls=none on a non-loopback bind should
			// produce a warning so the operator knows the API key flows
			// in cleartext.
			name: "tls_none_non_loopback_warns",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "0.0.0.0:9080", Protocol: "admin", TLS: "none"},
			},
			wantURL:      "http://127.0.0.1:9080",
			wantOK:       true,
			wantWarnings: true,
		},
		{
			// Malformed address must not panic; returns ("", nil, false).
			name: "malformed_address_returns_false",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "notanaddress", Protocol: "admin", TLS: "none"},
			},
			wantURL: "",
			wantOK:  false,
		},
		{
			// `localhost` expands to dual-stack at bind time but the
			// saved server_url is pinned to 127.0.0.1 so the value is
			// deterministic across machines.
			name: "localhost_pins_to_ipv4_loopback",
			listeners: []ListenerConfig{
				{Kind: "admin", Address: "localhost:9443", Protocol: "admin", TLS: "none"},
			},
			wantURL: "http://127.0.0.1:9443",
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Listener: tt.listeners}
			got, warns, ok := AdminRESTURL(cfg)
			if ok != tt.wantOK {
				t.Fatalf("AdminRESTURL ok=%v, want %v (url=%q)", ok, tt.wantOK, got)
			}
			if got != tt.wantURL {
				t.Errorf("AdminRESTURL url=%q, want %q", got, tt.wantURL)
			}
			if tt.wantWarnings && len(warns) == 0 {
				t.Errorf("AdminRESTURL: expected non-empty warnings for non-loopback http bind")
			}
			if !tt.wantWarnings && len(warns) != 0 {
				t.Errorf("AdminRESTURL: unexpected warnings: %v", warns)
			}
		})
	}
}

func TestResolveBindAddresses(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    []string
		wantErr bool
	}{
		{
			name:    "ipv4_loopback",
			address: "127.0.0.1:1143",
			want:    []string{"127.0.0.1:1143"},
		},
		{
			name:    "ipv6_loopback",
			address: "[::1]:1143",
			want:    []string{"[::1]:1143"},
		},
		{
			name:    "ipv4_wildcard",
			address: "0.0.0.0:1143",
			want:    []string{"0.0.0.0:1143"},
		},
		{
			name:    "ipv6_wildcard",
			address: "[::]:1143",
			want:    []string{"[::]:1143"},
		},
		{
			name:    "localhost_expands_to_both_stacks",
			address: "localhost:1143",
			want:    []string{"127.0.0.1:1143", "[::1]:1143"},
		},
		{
			name:    "localhost_uppercase_still_expands",
			address: "LOCALHOST:1143",
			want:    []string{"127.0.0.1:1143", "[::1]:1143"},
		},
		{
			name:    "explicit_hostname_kept_verbatim",
			address: "mail.example.com:993",
			want:    []string{"mail.example.com:993"},
		},
		{
			name:    "empty_passes_through",
			address: "",
			want:    []string{""},
		},
		{
			name:    "malformed_address_errors",
			address: "no-port-here",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveBindAddresses(tt.address)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---- [[log.sink]] multi-sink tests (REQ-OPS-80..86) ----

// TestLogSink_DefaultInserted verifies that when neither [observability] nor
// [[log.sink]] is configured a single stderr/auto/info sink is synthesised.
func TestLogSink_DefaultInserted(t *testing.T) {
	cfg, err := Parse([]byte(minimalNoObs))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Log.Sink) != 1 {
		t.Fatalf("expected 1 default sink, got %d", len(cfg.Log.Sink))
	}
	s := cfg.Log.Sink[0]
	if s.Target != "stderr" {
		t.Errorf("default sink target: got %q, want stderr", s.Target)
	}
	if s.Format != "auto" {
		t.Errorf("default sink format: got %q, want auto", s.Format)
	}
	if s.Level != "info" {
		t.Errorf("default sink level: got %q, want info", s.Level)
	}
}

// TestLogSink_ExplicitSinks verifies that explicit [[log.sink]] entries are
// parsed and validated correctly.
func TestLogSink_ExplicitSinks(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "console"
level  = "info"
activities = { deny = ["poll", "access"] }

[[log.sink]]
target = "/var/log/herold/herold.jsonl"
format = "json"
level  = "debug"
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Log.Sink) != 2 {
		t.Fatalf("expected 2 sinks, got %d", len(cfg.Log.Sink))
	}
	s0 := cfg.Log.Sink[0]
	if s0.Target != "stderr" || s0.Format != "console" || s0.Level != "info" {
		t.Errorf("sink 0: got %+v", s0)
	}
	if len(s0.Activities.Deny) != 2 {
		t.Errorf("sink 0 activities deny: got %v", s0.Activities.Deny)
	}
	s1 := cfg.Log.Sink[1]
	if s1.Target != "/var/log/herold/herold.jsonl" || s1.Format != "json" || s1.Level != "debug" {
		t.Errorf("sink 1: got %+v", s1)
	}
}

// TestLogSink_LegacyTranslation verifies that the old [observability]
// log_format / log_level / log_modules fields are synthesised into a single
// [[log.sink]] entry.
func TestLogSink_LegacyTranslation(t *testing.T) {
	// Use non-default values so hasLegacyLog triggers.
	toml := minimalNoObs + `
[observability]
log_format = "text"
log_level  = "debug"
log_modules = { smtp = "trace" }
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Log.Sink) != 1 {
		t.Fatalf("legacy translation: expected 1 sink, got %d", len(cfg.Log.Sink))
	}
	s := cfg.Log.Sink[0]
	if s.Target != "stderr" {
		t.Errorf("legacy sink target: got %q", s.Target)
	}
	// "text" maps to "console" in the shim.
	if s.Format != "console" {
		t.Errorf("legacy sink format: got %q, want console", s.Format)
	}
	if s.Level != "debug" {
		t.Errorf("legacy sink level: got %q, want debug", s.Level)
	}
	if s.Modules["smtp"] != "trace" {
		t.Errorf("legacy sink modules: got %v", s.Modules)
	}
}

// TestLogSink_RejectRelativePath verifies that relative file targets are rejected.
func TestLogSink_RejectRelativePath(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "relative/path.log"
format = "json"
level  = "info"
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention absolute path, got: %v", err)
	}
}

// TestLogSink_RejectDevNull verifies that /dev/null is rejected.
func TestLogSink_RejectDevNull(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "/dev/null"
format = "json"
level  = "info"
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for /dev/null, got nil")
	}
	if !strings.Contains(err.Error(), "/dev/null") {
		t.Errorf("error should mention /dev/null, got: %v", err)
	}
}

// TestLogSink_RejectAllowAndDeny verifies that setting both allow and deny is rejected.
func TestLogSink_RejectAllowAndDeny(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "json"
level  = "info"
activities = { allow = ["user"], deny = ["poll"] }
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for allow+deny set together, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutually exclusive, got: %v", err)
	}
}

// TestLogSink_RejectUnknownActivity verifies that unknown activity values are rejected.
func TestLogSink_RejectUnknownActivity(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "json"
level  = "info"
activities = { deny = ["not_an_activity"] }
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for unknown activity, got nil")
	}
	if !strings.Contains(err.Error(), "not_an_activity") {
		t.Errorf("error should mention the bad activity value, got: %v", err)
	}
}

// TestLogSink_RejectDuplicateFilePath verifies that two sinks targeting the
// same file path are rejected.
func TestLogSink_RejectDuplicateFilePath(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "/var/log/herold/a.jsonl"
format = "json"
level  = "info"

[[log.sink]]
target = "/var/log/herold/a.jsonl"
format = "json"
level  = "debug"
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for duplicate file path, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

// TestLogSink_PerModuleLevels verifies that modules overrides are parsed.
func TestLogSink_PerModuleLevels(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "json"
level  = "info"
modules = { smtp = "debug", queue = "warn" }
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := cfg.Log.Sink[0]
	if s.Modules["smtp"] != "debug" {
		t.Errorf("smtp module: got %q", s.Modules["smtp"])
	}
	if s.Modules["queue"] != "warn" {
		t.Errorf("queue module: got %q", s.Modules["queue"])
	}
}

// TestLogSink_AutoFormatDefault verifies that a sink without an explicit format
// gets "auto" applied by applyDefaults.
func TestLogSink_AutoFormatDefault(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "stderr"
level  = "info"
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Log.Sink[0].Format != "auto" {
		t.Errorf("default format: got %q, want auto", cfg.Log.Sink[0].Format)
	}
}

// TestLogSink_AllowActivity verifies the allow filter is parsed correctly.
func TestLogSink_AllowActivity(t *testing.T) {
	toml := minimalNoObs + `
[[log.sink]]
target = "stderr"
format = "json"
level  = "info"
activities = { allow = ["user", "audit"] }
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	act := cfg.Log.Sink[0].Activities
	if len(act.Allow) != 2 {
		t.Errorf("allow list: got %v", act.Allow)
	}
	if len(act.Deny) != 0 {
		t.Errorf("deny list should be empty: got %v", act.Deny)
	}
}

// -- External submission + secrets validation (REQ-AUTH-EXT-SUBMIT-01..10) --

// TestValidate_ExternalSubmission_EnabledRequiresDataKey checks that enabling
// external_submission without a data_key_ref is a hard validation error.
func TestValidate_ExternalSubmission_EnabledRequiresDataKey(t *testing.T) {
	const cfg = `
[server]
hostname = "mail.example.com"
data_dir  = "/var/lib/herold"

[server.admin_tls]
source   = "file"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[server.external_submission]
enabled = true

[[listener]]
name     = "smtp"
address  = "0.0.0.0:25"
protocol = "smtp"
tls      = "starttls"

[[listener]]
name     = "public"
address  = "0.0.0.0:443"
protocol = "admin"
kind     = "public"
tls      = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name     = "admin"
address  = "127.0.0.1:9443"
protocol = "admin"
kind     = "admin"
tls      = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error when external_submission.enabled=true without data_key_ref")
	}
	if !strings.Contains(err.Error(), "data_key_ref") {
		t.Errorf("error should mention data_key_ref: %v", err)
	}
}

// TestValidate_ExternalSubmission_DisabledWithoutDataKey passes when enabled=false
// and no data_key_ref is set (the default zero-value state).
func TestValidate_ExternalSubmission_DisabledWithoutDataKey(t *testing.T) {
	cfg, err := Parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.ExternalSubmission.Enabled {
		t.Error("external_submission.enabled should default to false")
	}
}

// TestValidate_Secrets_InlineRefusedInToml checks that setting data_key_ref to
// a literal string (not $VAR or file:/) is rejected at validate time.
func TestValidate_Secrets_InlineRefusedInToml(t *testing.T) {
	const cfg = `
[server]
hostname  = "mail.example.com"
data_dir  = "/var/lib/herold"

[server.admin_tls]
source    = "file"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[server.secrets]
data_key_ref = "not-a-reference-but-an-inline-value"

[[listener]]
name     = "smtp"
address  = "0.0.0.0:25"
protocol = "smtp"
tls      = "starttls"

[[listener]]
name     = "public"
address  = "0.0.0.0:443"
protocol = "admin"
kind     = "public"
tls      = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name     = "admin"
address  = "127.0.0.1:9443"
protocol = "admin"
kind     = "admin"
tls      = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"
`
	_, err := Parse([]byte(cfg))
	if err == nil {
		t.Fatal("expected error for inline data_key_ref")
	}
	if !strings.Contains(err.Error(), "STANDARDS §9") {
		t.Errorf("error should cite STANDARDS §9: %v", err)
	}
}

// TestValidate_Secrets_EnvRefAccepted verifies that a $VAR reference for
// data_key_ref is accepted at parse time even when the env var is not set
// (resolution happens at runtime, not at config parse).
func TestValidate_Secrets_EnvRefAccepted(t *testing.T) {
	const cfg = `
[server]
hostname  = "mail.example.com"
data_dir  = "/var/lib/herold"

[server.admin_tls]
source    = "file"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[server.secrets]
data_key_ref = "$HEROLD_DATA_KEY"

[[listener]]
name     = "smtp"
address  = "0.0.0.0:25"
protocol = "smtp"
tls      = "starttls"

[[listener]]
name     = "public"
address  = "0.0.0.0:443"
protocol = "admin"
kind     = "public"
tls      = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"

[[listener]]
name     = "admin"
address  = "127.0.0.1:9443"
protocol = "admin"
kind     = "admin"
tls      = "implicit"
cert_file = "/etc/herold/admin.crt"
key_file  = "/etc/herold/admin.key"
`
	c, err := Parse([]byte(cfg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Server.Secrets.DataKeyRef != "$HEROLD_DATA_KEY" {
		t.Errorf("DataKeyRef = %q; want $HEROLD_DATA_KEY", c.Server.Secrets.DataKeyRef)
	}
}

// oauthProviderBaseTOML is a minimal config base with external_submission
// enabled and a data key, used by OAuth provider tests.
const oauthProviderBaseTOML = minimalNoObs + `
[server.external_submission]
enabled = true

[server.secrets]
data_key_ref = "$HEROLD_DATA_KEY"
`

// TestOAuthProviders_AllFieldsLoad verifies that a well-formed
// [server.oauth_providers.gmail] block parses and loads cleanly.
func TestOAuthProviders_AllFieldsLoad(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.gmail]
client_id         = "123.apps.googleusercontent.com"
client_secret_ref = "$HEROLD_GMAIL_SECRET"
auth_url          = "https://accounts.google.com/o/oauth2/v2/auth"
token_url         = "https://oauth2.googleapis.com/token"
scopes            = ["https://mail.google.com/"]
`
	c, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	p, ok := c.Server.OAuthProviders["gmail"]
	if !ok {
		t.Fatalf("OAuthProviders missing gmail entry; got %v", c.Server.OAuthProviders)
	}
	if p.ClientID != "123.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q; want %q", p.ClientID, "123.apps.googleusercontent.com")
	}
	if p.ClientSecretRef != "$HEROLD_GMAIL_SECRET" {
		t.Errorf("ClientSecretRef = %q; want $HEROLD_GMAIL_SECRET", p.ClientSecretRef)
	}
	if len(p.Scopes) != 1 || p.Scopes[0] != "https://mail.google.com/" {
		t.Errorf("Scopes = %v; want [https://mail.google.com/]", p.Scopes)
	}
}

// TestOAuthProviders_NameNormalisedToLowercase verifies that a mixed-case
// provider name is normalised to lowercase in applyDefaults.
func TestOAuthProviders_NameNormalisedToLowercase(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.Gmail]
client_id         = "id"
client_secret_ref = "$HEROLD_GMAIL_SECRET"
auth_url          = "https://accounts.google.com/o/oauth2/v2/auth"
token_url         = "https://oauth2.googleapis.com/token"
scopes            = ["https://mail.google.com/"]
`
	c, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if _, ok := c.Server.OAuthProviders["gmail"]; !ok {
		t.Errorf("OAuthProviders: want normalised key \"gmail\"; got keys %v", func() []string {
			ks := make([]string, 0, len(c.Server.OAuthProviders))
			for k := range c.Server.OAuthProviders {
				ks = append(ks, k)
			}
			return ks
		}())
	}
}

// TestOAuthProviders_InlineSecretRejected verifies that a literal
// client_secret_ref value (not a $VAR or file:/path) is rejected at Validate
// per STANDARDS §9.
func TestOAuthProviders_InlineSecretRejected(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.gmail]
client_id         = "id"
client_secret_ref = "literally-inline-secret"
auth_url          = "https://accounts.google.com/o/oauth2/v2/auth"
token_url         = "https://oauth2.googleapis.com/token"
scopes            = ["https://mail.google.com/"]
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected validate error for inline client_secret_ref")
	}
}

// TestOAuthProviders_MissingClientSecretRef verifies that omitting
// client_secret_ref is rejected.
func TestOAuthProviders_MissingClientSecretRef(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.gmail]
client_id = "id"
auth_url  = "https://accounts.google.com/o/oauth2/v2/auth"
token_url = "https://oauth2.googleapis.com/token"
scopes    = ["https://mail.google.com/"]
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected validate error for missing client_secret_ref")
	}
}

// TestOAuthProviders_BadAuthURL verifies that a non-URL auth_url is rejected.
func TestOAuthProviders_BadAuthURL(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.gmail]
client_id         = "id"
client_secret_ref = "$HEROLD_GMAIL_SECRET"
auth_url          = "not-a-url"
token_url         = "https://oauth2.googleapis.com/token"
scopes            = ["https://mail.google.com/"]
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected validate error for bad auth_url")
	}
}

// TestOAuthProviders_BadTokenURL verifies that a non-URL token_url is rejected.
func TestOAuthProviders_BadTokenURL(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.gmail]
client_id         = "id"
client_secret_ref = "$HEROLD_GMAIL_SECRET"
auth_url          = "https://accounts.google.com/o/oauth2/v2/auth"
token_url         = "not-a-url"
scopes            = ["https://mail.google.com/"]
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected validate error for bad token_url")
	}
}

// TestOAuthProviders_EmptyScopesRejected verifies that scopes = [] is rejected.
func TestOAuthProviders_EmptyScopesRejected(t *testing.T) {
	const tomlSrc = oauthProviderBaseTOML + `
[server.oauth_providers.gmail]
client_id         = "id"
client_secret_ref = "$HEROLD_GMAIL_SECRET"
auth_url          = "https://accounts.google.com/o/oauth2/v2/auth"
token_url         = "https://oauth2.googleapis.com/token"
scopes            = []
`
	if _, err := Parse([]byte(tomlSrc)); err == nil {
		t.Fatal("expected validate error for empty scopes")
	}
}

// TestOAuthProviders_EmptyMapAccepted verifies that an operator omitting the
// [server.oauth_providers] block entirely is valid.
func TestOAuthProviders_EmptyMapAccepted(t *testing.T) {
	const tomlSrc = minimalNoObs
	if _, err := Parse([]byte(tomlSrc)); err != nil {
		t.Fatalf("expected no error for config without oauth_providers; got %v", err)
	}
}

// TestDirectoryAutocomplete_SectionOmitted verifies that omitting the section
// entirely defaults Mode to "domain".
func TestDirectoryAutocomplete_SectionOmitted(t *testing.T) {
	cfg, err := Parse([]byte(minimalNoObs))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.DirectoryAutocomplete.Mode != DirectoryAutocompleteModeDomain {
		t.Errorf("default mode: got %q, want %q", cfg.Server.DirectoryAutocomplete.Mode, DirectoryAutocompleteModeDomain)
	}
}

// TestDirectoryAutocomplete_ModeAll verifies that mode = "all" is accepted and
// surfaces as DirectoryAutocompleteModeAll.
func TestDirectoryAutocomplete_ModeAll(t *testing.T) {
	const tomlSrc = minimalNoObs + `
[server.directory_autocomplete]
mode = "all"
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.DirectoryAutocomplete.Mode != DirectoryAutocompleteModeAll {
		t.Errorf("mode: got %q, want %q", cfg.Server.DirectoryAutocomplete.Mode, DirectoryAutocompleteModeAll)
	}
}

// TestDirectoryAutocomplete_ModeDomain verifies that mode = "domain" is
// accepted and surfaces as DirectoryAutocompleteModeDomain.
func TestDirectoryAutocomplete_ModeDomain(t *testing.T) {
	const tomlSrc = minimalNoObs + `
[server.directory_autocomplete]
mode = "domain"
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.DirectoryAutocomplete.Mode != DirectoryAutocompleteModeDomain {
		t.Errorf("mode: got %q, want %q", cfg.Server.DirectoryAutocomplete.Mode, DirectoryAutocompleteModeDomain)
	}
}

// TestDirectoryAutocomplete_ModeOff verifies that mode = "off" is accepted and
// surfaces as DirectoryAutocompleteModeOff.
func TestDirectoryAutocomplete_ModeOff(t *testing.T) {
	const tomlSrc = minimalNoObs + `
[server.directory_autocomplete]
mode = "off"
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.DirectoryAutocomplete.Mode != DirectoryAutocompleteModeOff {
		t.Errorf("mode: got %q, want %q", cfg.Server.DirectoryAutocomplete.Mode, DirectoryAutocompleteModeOff)
	}
}

// TestDirectoryAutocomplete_SectionPresentModeAbsent verifies that a section
// present with no mode field defaults to "domain".
func TestDirectoryAutocomplete_SectionPresentModeAbsent(t *testing.T) {
	const tomlSrc = minimalNoObs + `
[server.directory_autocomplete]
`
	cfg, err := Parse([]byte(tomlSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.DirectoryAutocomplete.Mode != DirectoryAutocompleteModeDomain {
		t.Errorf("default mode with section present: got %q, want %q", cfg.Server.DirectoryAutocomplete.Mode, DirectoryAutocompleteModeDomain)
	}
}

// TestDirectoryAutocomplete_InvalidMode verifies that an unrecognised mode
// value is rejected with a sysconfig validation error.
func TestDirectoryAutocomplete_InvalidMode(t *testing.T) {
	cases := []string{"Domain", "ALL", "yes", "1", " domain"}
	for _, bad := range cases {
		bad := bad
		t.Run(bad, func(t *testing.T) {
			tomlSrc := minimalNoObs + "\n[server.directory_autocomplete]\nmode = " + `"` + bad + `"` + "\n"
			_, err := Parse([]byte(tomlSrc))
			if err == nil {
				t.Fatalf("expected error for mode = %q, got nil", bad)
			}
			if !strings.Contains(err.Error(), "directory_autocomplete") {
				t.Errorf("error should mention directory_autocomplete: %v", err)
			}
			if !strings.Contains(err.Error(), bad) {
				t.Errorf("error should quote the bad value %q: %v", bad, err)
			}
		})
	}
}

func TestParse_TrashRetentionDefaults(t *testing.T) {
	const bare = `
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
`
	cfg, err := Parse([]byte(bare))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Server.TrashRetention.RetentionDays != 30 {
		t.Errorf("default trash retention_days: got %d, want 30",
			cfg.Server.TrashRetention.RetentionDays)
	}
	if cfg.Server.TrashRetention.SweepIntervalSeconds != 3600 {
		t.Errorf("default trash sweep_interval_seconds: got %d, want 3600",
			cfg.Server.TrashRetention.SweepIntervalSeconds)
	}
}

func TestValidate_TrashRetentionRejectsNegativeRetentionDays(t *testing.T) {
	// retention_days = 0 is treated as "use default" by applyDefaults
	// (zero-value convention); retention_days = -1 is an explicit negative
	// that applyDefaults leaves alone and Validate must reject.
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.trash_retention]
retention_days = -1

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected retention_days floor error")
	}
	if !strings.Contains(err.Error(), "retention_days") {
		t.Errorf("error should name retention_days, got: %v", err)
	}
}

func TestValidate_TrashRetentionRejectsLowSweepInterval(t *testing.T) {
	const bad = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[server.trash_retention]
retention_days = 30
sweep_interval_seconds = 5

[[listener]]
name = "l"
address = ":25"
protocol = "smtp"
tls = "starttls"
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatalf("expected sweep_interval_seconds floor error")
	}
	if !strings.Contains(err.Error(), "sweep_interval_seconds") {
		t.Errorf("error should name sweep_interval_seconds, got: %v", err)
	}
}

// TestValidate_PortZeroRequiresReportFile asserts that a [[listener]] with
// port 0 is rejected unless [server].port_report_file is also set, and that
// setting port_report_file clears the error.
func TestValidate_PortZeroRequiresReportFile(t *testing.T) {
	const base = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "smtp-relay"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
`
	// Without port_report_file: must error.
	_, err := Parse([]byte(base))
	if err == nil {
		t.Fatal("expected port 0 without port_report_file to fail validation")
	}
	if !strings.Contains(err.Error(), "port_report_file") {
		t.Errorf("error should mention port_report_file; got: %v", err)
	}
	if !strings.Contains(err.Error(), "smtp-relay") {
		t.Errorf("error should name the listener; got: %v", err)
	}

	// With port_report_file set: must pass.
	withReportFile := base + `
[server.port_report_file]
`
	// The field is a string, not a table; use correct TOML.
	const withFile = `
[server]
hostname = "mail.example.com"
data_dir = "/var/lib/herold"
port_report_file = "/tmp/herold-ports.toml"

[server.admin_tls]
source = "file"
cert_file = "/a"
key_file = "/b"

[[listener]]
name = "smtp-relay"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
`
	_ = withReportFile
	if _, err := Parse([]byte(withFile)); err != nil {
		t.Fatalf("port 0 with port_report_file set should pass; got: %v", err)
	}
}
