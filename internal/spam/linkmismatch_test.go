package spam

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailauth"
)

// lureMessage builds a synthetic HTML message from sender@fromDomain with
// the given body.
func lureMessage(fromDomain, body string) string {
	return "From: Atendimento <sender@" + fromDomain + ">\r\n" +
		"To: me@example.com\r\n" +
		"Subject: Documento disponivel\r\n" +
		"Date: Mon, 02 Jan 2006 15:04:05 +0000\r\n" +
		"Message-ID: <lure@" + fromDomain + ">\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		body + "\r\n"
}

// TestBuildRequest_LinkHostMismatch (re #552): the request states, as a
// herold-determined fact, whether any web link leads to a host unrelated
// to the From domain.
func TestBuildRequest_LinkHostMismatch(t *testing.T) {
	cases := []struct {
		name       string
		fromDomain string // as the auth result reports it; "" means no auth result
		body       string
		want       bool
	}{
		{"same host", "sender.example", `<a href="https://sender.example/x">Go</a>`, false},
		{"subdomain of from domain", "sender.example", `<a href="https://links.mail.sender.example/x">Go</a>`, false},
		{"from subdomain, link at apex", "mail.sender.example", `<a href="https://www.sender.example/x">Go</a>`, false},
		{"unrelated host", "sender.example", `<a href="https://other.example/x">Go</a>`, true},
		{"lookalike suffix", "sender.example", `<a href="https://notsender.example/x">Go</a>`, true},
		{"from domain as subdomain label", "sender.example", `<a href="https://sender.example.evil.test/x">Go</a>`, true},
		{"second anchor mismatches", "sender.example", `<a href="https://sender.example/">A</a><a href="https://other.example/">B</a>`, true},
		{"multi-label public suffix", "shop.example.co.uk", `<a href="https://www.example.co.uk/">Go</a>`, false},
		{"different registrant under public suffix", "a.co.uk", `<a href="https://b.co.uk/">Go</a>`, true},
		{"ip literal host", "sender.example", `<a href="https://203.0.113.9/x">Go</a>`, true},
		{"backslash form hides the host", "sender.example", `<a href="https:\\other.example\x">Go</a>`, true},
		{"relative link resolved against base", "sender.example", `<base href="https://other.example/"><a href="x">Go</a>`, true},
		{"no links", "sender.example", `<p>Hello</p>`, false},
		{"mailto only", "sender.example", `<a href="mailto:a@other.example">m</a>`, false},
		{"plain text url is not a link", "sender.example", `<p>https://other.example/x</p>`, false},
		{"unknown from domain", "", `<a href="https://other.example/x">Go</a>`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var auth *mailauth.AuthResults
			headerDomain := "sender.example"
			if c.fromDomain != "" {
				auth = newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, c.fromDomain)
				headerDomain = c.fromDomain
			}
			raw := lureMessage(headerDomain, c.body)
			if c.fromDomain == "" {
				raw = strings.Replace(raw, "sender@"+headerDomain, "sender", 1)
			}
			req := BuildRequest(buildMessage(t, raw), auth)
			if req.LinkHostMismatch != c.want {
				t.Fatalf("LinkHostMismatch = %v, want %v (excerpt %q)", req.LinkHostMismatch, c.want, req.BodyExcerpt)
			}
		})
	}
}

// TestBuildRequest_LinkHostMismatchFromHeaderWithoutAuth: with no auth
// result (IMAP import) the From header supplies the domain.
func TestBuildRequest_LinkHostMismatchFromHeaderWithoutAuth(t *testing.T) {
	req := BuildRequest(buildMessage(t, lureMessage("sender.example", `<a href="https://other.example/x">Go</a>`)), nil)
	if !req.LinkHostMismatch {
		t.Fatalf("mismatch not set from the From header domain")
	}
	req = BuildRequest(buildMessage(t, lureMessage("sender.example", `<a href="https://www.sender.example/x">Go</a>`)), nil)
	if req.LinkHostMismatch {
		t.Fatalf("mismatch set for a link on the From domain")
	}
}

// TestBuildRequest_LinkHostMismatchWire: the fact is on the wire (and so
// in the stored prompt-as-applied) only when true.
func TestBuildRequest_LinkHostMismatchWire(t *testing.T) {
	wire := func(body string) map[string]any {
		req := BuildRequest(buildMessage(t, lureMessage("sender.example", body)), nil)
		raw, err := req.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if v, ok := wire(`<a href="https://other.example/">Go</a>`)["link_host_mismatch"]; !ok || v != true {
		t.Fatalf("link_host_mismatch missing or not true on the wire")
	}
	if _, ok := wire(`<a href="https://sender.example/">Go</a>`)["link_host_mismatch"]; ok {
		t.Fatalf("link_host_mismatch present although false")
	}
}

// TestLinkHostMismatchIsNotDecisive: the fact is an input to the
// classifier's judgement; it never forces a verdict.
func TestLinkHostMismatchIsNotDecisive(t *testing.T) {
	for _, s := range DefaultDecisiveSpamSignals {
		if strings.Contains(strings.ToLower(s), "link_host_mismatch") || strings.Contains(strings.ToLower(s), "mismatch") {
			t.Fatalf("decisive signal %q derived from the link-host fact", s)
		}
	}
}

// TestAuthSummaryPassWording (re #552): a DMARC pass proves the From
// domain sent the message and says nothing about legitimacy.
func TestAuthSummaryPassWording(t *testing.T) {
	req := BuildRequest(buildMessage(t, canonMsg),
		newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, "sender.example"))
	s := req.AuthSummary
	for _, want := range []string{"sender.example", "sent by", "legitimate", "known to the recipient", "not a ham signal"} {
		if !strings.Contains(s, want) {
			t.Errorf("auth_summary missing %q: %q", want, s)
		}
	}
	for _, bad := range []string{"Sender identity is verified", "not by re-litigating", "spoof", "impersonat", "forg"} {
		if strings.Contains(s, bad) {
			t.Errorf("auth_summary contains %q: %q", bad, s)
		}
	}
}

// portugueseLure is a synthetic document-download lure modelled on the
// shape of #551: an unrelated sender domain that passes authentication,
// a Portuguese body, and one call-to-action button whose host has
// nothing to do with the sender.
const portugueseLure = `<html><body>
<p>Ola,</p>
<p>Foi partilhado consigo um documento com a referencia 48213. O ficheiro fica disponivel durante 48 horas.</p>
<p><a href="https://files.docs-share.example/d/48213?t=abc" style="padding:12px;background:#0a66c2">Transferir documento</a></p>
<p>Cumprimentos,<br>Departamento de Formacao</p>
</body></html>`

// TestBuildRequest_PortugueseLureFixture (re #552): the classify request
// for the lure shows the link host in the excerpt, states the mismatch,
// and carries the reworded authentication summary.
func TestBuildRequest_PortugueseLureFixture(t *testing.T) {
	auth := newAuth(mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthPass, mailauth.AuthNone, "formacao-exemplo.example")
	req := BuildRequest(buildMessage(t, lureMessage("formacao-exemplo.example", portugueseLure)), auth)
	if hosts := markerHosts(req.BodyExcerpt); len(hosts) != 1 || hosts[0] != "files.docs-share.example" {
		t.Fatalf("excerpt markers = %v; excerpt %q", hosts, req.BodyExcerpt)
	}
	if !strings.Contains(req.BodyExcerpt, "Transferir documento") {
		t.Fatalf("anchor text missing: %q", req.BodyExcerpt)
	}
	if strings.Contains(req.BodyExcerpt, "48213?t=abc") {
		t.Fatalf("excerpt carries the full URL: %q", req.BodyExcerpt)
	}
	if !req.LinkHostMismatch {
		t.Fatalf("link_host_mismatch not set")
	}
	if !strings.Contains(req.AuthSummary, "says nothing about whether that sender is legitimate") {
		t.Fatalf("auth_summary not reworded: %q", req.AuthSummary)
	}
	if req.DMARC != "pass" {
		t.Fatalf("dmarc = %q", req.DMARC)
	}
}
