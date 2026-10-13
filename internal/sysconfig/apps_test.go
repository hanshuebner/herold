// Tests for the [server.apps] system-config block
// (docs/design/server/requirements/29-apps.md REQ-APP-42, REQ-APP-61).

package sysconfig

import (
	"strings"
	"testing"
	"time"
)

func TestApps_Defaults(t *testing.T) {
	cfg, err := Parse([]byte(bareForShares))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := cfg.Server.Apps
	if !a.AppsEnabled() {
		t.Error("apps should be enabled by default")
	}
	if got, want := a.MaxPayloadBytes.AsInt64(), int64(50*1024*1024); got != want {
		t.Errorf("max_payload_bytes = %d, want %d", got, want)
	}
	if got, want := a.InvokeTimeout.AsDuration(), 15*time.Second; got != want {
		t.Errorf("invoke_timeout = %s, want %s", got, want)
	}
	if len(a.AllowedInsecureOrigins) != 0 {
		t.Errorf("allowed_insecure_origins = %v, want empty", a.AllowedInsecureOrigins)
	}
}

// TestApps_DocumentedExample parses the [server.apps] snippet from
// docs/manual/admin/configure.mdoc.
func TestApps_DocumentedExample(t *testing.T) {
	cfg, err := Parse([]byte(bareForShares + `
[server.apps]
enabled = true
max_payload_bytes = "50 MiB"
invoke_timeout = "15s"
allowed_insecure_origins = ["http://127.0.0.1:18080"]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := cfg.Server.Apps
	if a.MaxPayloadBytes.AsInt64() != 50*1024*1024 || a.InvokeTimeout.AsDuration() != 15*time.Second {
		t.Fatalf("parsed %+v", a)
	}
	if len(a.AllowedInsecureOrigins) != 1 || a.AllowedInsecureOrigins[0] != "http://127.0.0.1:18080" {
		t.Fatalf("allowed_insecure_origins = %v", a.AllowedInsecureOrigins)
	}
}

func TestApps_DisabledAndNormalised(t *testing.T) {
	cfg, err := Parse([]byte(bareForShares + `
[server.apps]
enabled = false
max_payload_bytes = "1 MiB"
invoke_timeout = "2s"
allowed_insecure_origins = ["HTTP://LocalHost:8443/", "https://dev.example.test"]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a := cfg.Server.Apps
	if a.AppsEnabled() {
		t.Error("enabled = false not honoured")
	}
	want := []string{"http://localhost:8443", "https://dev.example.test"}
	for i, w := range want {
		if a.AllowedInsecureOrigins[i] != w {
			t.Errorf("origin[%d] = %q, want %q", i, a.AllowedInsecureOrigins[i], w)
		}
	}
}

func TestApps_Rejects(t *testing.T) {
	cases := map[string]string{
		"timeout too long":  `invoke_timeout = "10m"`,
		"origin with path":  `allowed_insecure_origins = ["http://127.0.0.1:1/x"]`,
		"origin with query": `allowed_insecure_origins = ["http://127.0.0.1:1?a=b"]`,
		"origin scheme":     `allowed_insecure_origins = ["ftp://127.0.0.1"]`,
		"origin userinfo":   `allowed_insecure_origins = ["http://u:p@127.0.0.1"]`,
		"origin no host":    `allowed_insecure_origins = ["http://"]`,
		"origin bad port":   `allowed_insecure_origins = ["http://127.0.0.1:99999"]`,
		"unknown key":       `max_payload = 1`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(bareForShares + "\n[server.apps]\n" + body + "\n"))
			if err == nil {
				t.Fatalf("Parse accepted %s", body)
			}
			if !strings.Contains(err.Error(), "apps") && name != "unknown key" {
				t.Errorf("error does not name the section: %v", err)
			}
		})
	}
}

func TestNormalizeOrigin(t *testing.T) {
	cases := map[string]string{
		"https://Forum.Example.org":     "https://forum.example.org",
		"http://127.0.0.1:18080":        "http://127.0.0.1:18080",
		"http://[::1]:8080/":            "http://[::1]:8080",
		"https://forum.example.org:443": "https://forum.example.org",
		"http://example.org:443":        "http://example.org:443",
		"https://[::1]:443":             "https://[::1]",
	}
	for in, want := range cases {
		got, err := NormalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
