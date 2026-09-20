package extimg

import (
	"testing"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// TestShouldFlagOnDemand pins the policy switch (REQ-EXTIMG-91/92)
// shared by the Gmail Takeout and IMAP-mirror importers.
func TestShouldFlagOnDemand(t *testing.T) {
	cases := []struct {
		policy string
		want   bool
	}{
		{"", true},
		{"on_demand", true},
		{"off", false},
	}
	for _, c := range cases {
		if got := ShouldFlagOnDemand(c.policy); got != c.want {
			t.Errorf("ShouldFlagOnDemand(%q) = %v; want %v", c.policy, got, c.want)
		}
	}
}

// TestShouldFlagOnDemandForMode pins the mode-driven variant used by
// callers that already carry a live extimg.Config, such as the JMAP
// Email/import handler.
func TestShouldFlagOnDemandForMode(t *testing.T) {
	cases := []struct {
		mode Mode
		want bool
	}{
		{"", true},
		{ModeInternalize, true},
		{ModePassthrough, false},
	}
	for _, c := range cases {
		if got := ShouldFlagOnDemandForMode(c.mode); got != c.want {
			t.Errorf("ShouldFlagOnDemandForMode(%q) = %v; want %v", c.mode, got, c.want)
		}
	}
}

// TestHasExternalHTMLImage pins the cheap substring scan that gates
// the on-demand pending flag (REQ-EXTIMG-91).
func TestHasExternalHTMLImage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"img plus https", `<html><body><img src="https://example.test/x.png"></body></html>`, true},
		{"img plus http", `<html><body><img src="http://example.test/x.png"></body></html>`, true},
		{"img with no url scheme", `<html><body><img src="cid:abc"></body></html>`, false},
		{"http url but no img tag", `<html><body>see https://example.test</body></html>`, false},
		{"plain text, no html at all", "just a plain text body", false},
	}
	for _, c := range cases {
		if got := HasExternalHTMLImage([]byte(c.body)); got != c.want {
			t.Errorf("%s: HasExternalHTMLImage(%q) = %v; want %v", c.name, c.body, got, c.want)
		}
	}
}

// TestImportPolicyFromMode pins the mapping from the operator's
// [external_images] mode to the shared InternalizeImports policy
// string (REQ-EXTIMG-91/92): passthrough suppresses on-demand
// flagging; every other mode (including the empty default) enables it.
func TestImportPolicyFromMode(t *testing.T) {
	cases := []struct {
		mode sysconfig.ExternalImagesMode
		want string
	}{
		{"", "on_demand"},
		{sysconfig.ExternalImagesModeInternalize, "on_demand"},
		{sysconfig.ExternalImagesModePassthrough, "off"},
	}
	for _, c := range cases {
		if got := ImportPolicyFromMode(c.mode); got != c.want {
			t.Errorf("ImportPolicyFromMode(%q) = %q; want %q", c.mode, got, c.want)
		}
	}
}
