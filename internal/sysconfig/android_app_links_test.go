package sysconfig

// android_app_links_test.go — validation coverage for [server.ui]
// android_app_links (REQ-AND-SYS-11, issue #365): the Digital Asset
// Links statements herold serves at /.well-known/assetlinks.json for
// Android App Links verification.

import (
	"strings"
	"testing"
)

func TestUIConfig_AndroidAppLinks_Unconfigured(t *testing.T) {
	cfg, err := Parse([]byte(minimalNoObs))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Server.UI.AndroidAppLinks) != 0 {
		t.Errorf("android_app_links = %+v; want empty when unconfigured", cfg.Server.UI.AndroidAppLinks)
	}
}

func TestUIConfig_AndroidAppLinks_Valid(t *testing.T) {
	toml := minimalNoObs + `
[[server.ui.android_app_links]]
package = "com.netzhansa.herold.android"
sha256_cert_fingerprints = ["DF:1D:E8:CA:0B:62:34:FB:34:2F:49:32:F5:60:56:23:38:B7:A9:EC:A8:9D:CC:AE:A4:8F:3A:D1:B9:26:E3:72"]
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Server.UI.AndroidAppLinks) != 1 {
		t.Fatalf("android_app_links = %+v; want 1 entry", cfg.Server.UI.AndroidAppLinks)
	}
	link := cfg.Server.UI.AndroidAppLinks[0]
	if link.Package != "com.netzhansa.herold.android" {
		t.Errorf("package = %q", link.Package)
	}
	if len(link.SHA256CertFingerprints) != 1 {
		t.Errorf("sha256_cert_fingerprints = %v", link.SHA256CertFingerprints)
	}
}

func TestUIConfig_AndroidAppLinks_MissingPackageRejected(t *testing.T) {
	toml := minimalNoObs + `
[[server.ui.android_app_links]]
sha256_cert_fingerprints = ["DF:1D:E8:CA:0B:62:34:FB:34:2F:49:32:F5:60:56:23:38:B7:A9:EC:A8:9D:CC:AE:A4:8F:3A:D1:B9:26:E3:72"]
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for a missing package, got nil")
	}
	if !strings.Contains(err.Error(), "package is required") {
		t.Errorf("error should mention the missing package, got: %v", err)
	}
}

func TestUIConfig_AndroidAppLinks_EmptyFingerprintsRejected(t *testing.T) {
	toml := minimalNoObs + `
[[server.ui.android_app_links]]
package = "com.netzhansa.herold.android"
sha256_cert_fingerprints = []
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for an empty fingerprint list, got nil")
	}
	if !strings.Contains(err.Error(), "at least one sha256_cert_fingerprints entry") {
		t.Errorf("error should mention the missing fingerprint, got: %v", err)
	}
}

func TestUIConfig_AndroidAppLinks_MalformedFingerprintRejected(t *testing.T) {
	toml := minimalNoObs + `
[[server.ui.android_app_links]]
package = "com.netzhansa.herold.android"
sha256_cert_fingerprints = ["not-a-fingerprint"]
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for a malformed fingerprint, got nil")
	}
	if !strings.Contains(err.Error(), "colon-separated upper-case hex byte pairs") {
		t.Errorf("error should mention the expected fingerprint shape, got: %v", err)
	}
}
