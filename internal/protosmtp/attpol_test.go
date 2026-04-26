package protosmtp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// fixtures for the six MIME shapes the REQ-FLOW-ATTPOL-01..02 spec
// requires the header-only check to classify (a)–(f).
var attpolFixtures = map[string]struct {
	body          []byte
	wantHeader    bool   // header-only check refuses?
	wantPostWalk  bool   // post-acceptance walker refuses?
	wantHdrReason string // optional substring in header reason on reject
}{
	"a_multipart_mixed_with_attachment": {
		body: []byte(`From: bob@x.test
To: alice@y.test
Subject: with attachment
Content-Type: multipart/mixed; boundary="b"
MIME-Version: 1.0

--b
Content-Type: text/plain

text body
--b
Content-Type: application/pdf
Content-Disposition: attachment; filename="x.pdf"

PDF-bytes-here
--b--
`),
		wantHeader:    true,
		wantPostWalk:  true,
		wantHdrReason: "multipart_mixed",
	},
	"b_multipart_alternative_text_html": {
		body: []byte(`From: bob@x.test
To: alice@y.test
Subject: alt only
Content-Type: multipart/alternative; boundary="b"
MIME-Version: 1.0

--b
Content-Type: text/plain

plain body
--b
Content-Type: text/html

<p>html body</p>
--b--
`),
		wantHeader:   false,
		wantPostWalk: false,
	},
	"c_multipart_alternative_with_nested_mixed": {
		body: []byte(`From: bob@x.test
To: alice@y.test
Subject: nested
Content-Type: multipart/alternative; boundary="outer"
MIME-Version: 1.0

--outer
Content-Type: text/plain

plain
--outer
Content-Type: multipart/mixed; boundary="inner"

--inner
Content-Type: text/html

<p>html</p>
--inner
Content-Type: application/pdf
Content-Disposition: attachment; filename="hidden.pdf"

PDF-bytes
--inner--
--outer--
`),
		wantHeader:   false,
		wantPostWalk: true,
	},
	"d_text_plain": {
		body: []byte(`From: bob@x.test
To: alice@y.test
Subject: plain
Content-Type: text/plain

just text
`),
		wantHeader:   false,
		wantPostWalk: false,
	},
	"e_top_level_application_pdf": {
		body: []byte(`From: bob@x.test
To: alice@y.test
Subject: pdf only
Content-Type: application/pdf
Content-Disposition: attachment; filename="x.pdf"

PDF-bytes
`),
		wantHeader:    true,
		wantPostWalk:  true,
		wantHdrReason: "top_level_non_text",
	},
	"f_multipart_related_inline_image": {
		body: []byte(`From: bob@x.test
To: alice@y.test
Subject: inline images
Content-Type: multipart/related; boundary="b"
MIME-Version: 1.0

--b
Content-Type: text/html

<p>has <img src="cid:img1"></p>
--b
Content-Type: image/png
Content-Disposition: inline
Content-ID: <img1>

PNG-bytes
--b--
`),
		wantHeader:   false,
		wantPostWalk: false,
	},
}

func TestAttPolHeaderCheck_AllSixCases(t *testing.T) {
	t.Parallel()
	for name, tc := range attpolFixtures {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := normaliseCRLF(tc.body)
			msg, err := mailparse.Parse(bytes.NewReader(body), mailparse.NewParseOptions())
			if err != nil {
				t.Fatalf("mailparse.Parse: %v", err)
			}
			rejected, reason := attpolHeaderCheck(msg)
			if rejected != tc.wantHeader {
				t.Fatalf("attpolHeaderCheck rejected=%v reason=%q; want=%v", rejected, reason, tc.wantHeader)
			}
			if rejected && tc.wantHdrReason != "" && !strings.Contains(reason, tc.wantHdrReason) {
				t.Errorf("reason %q does not contain %q", reason, tc.wantHdrReason)
			}
		})
	}
}

func TestAttPolPostAcceptanceWalk_AllSixCases(t *testing.T) {
	t.Parallel()
	for name, tc := range attpolFixtures {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := normaliseCRLF(tc.body)
			msg, err := mailparse.Parse(bytes.NewReader(body), mailparse.NewParseOptions())
			if err != nil {
				t.Fatalf("mailparse.Parse: %v", err)
			}
			rejected, reason := attpolPostAcceptanceWalk(msg)
			if rejected != tc.wantPostWalk {
				t.Fatalf("attpolPostAcceptanceWalk rejected=%v reason=%q; want=%v",
					rejected, reason, tc.wantPostWalk)
			}
		})
	}
}

func TestAttPolRejectReply_DefaultText(t *testing.T) {
	got := attpolRejectReply(store.InboundAttachmentPolicyRow{
		Policy: store.AttPolicyRejectAtData,
	})
	want := "552 5.3.4 attachments not accepted on this address"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestAttPolRejectReply_OperatorOverride(t *testing.T) {
	got := attpolRejectReply(store.InboundAttachmentPolicyRow{
		Policy:     store.AttPolicyRejectAtData,
		RejectText: "no PDFs please",
	})
	want := "552 5.3.4 no PDFs please"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestAttPolDiagnosticCode_StablePrefix(t *testing.T) {
	got := attpolDiagnosticCode(store.InboundAttachmentPolicyRow{
		Policy: store.AttPolicyRejectAtData,
	})
	if !strings.HasPrefix(got, "smtp; 552 5.3.4 ") {
		t.Errorf("diagnostic %q lacks RFC 3464 prefix", got)
	}
}

func TestAttPolDomainOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alice@example.test", "example.test"},
		{"Alice@Example.TEST", "example.test"},
		{"weird", ""},
		{"", ""},
		{"@example.test", ""},
		{"alice@", ""},
	}
	for _, c := range cases {
		if got := attpolDomainOf(c.in); got != c.want {
			t.Errorf("attpolDomainOf(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestParseInboundAttachmentPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want store.InboundAttachmentPolicy
	}{
		{"accept", store.AttPolicyAccept},
		{"reject_at_data", store.AttPolicyRejectAtData},
		{"", store.AttPolicyUnset},
		{"bogus", store.AttPolicyUnset},
	}
	for _, c := range cases {
		if got := store.ParseInboundAttachmentPolicy(c.in); got != c.want {
			t.Errorf("ParseInboundAttachmentPolicy(%q) = %v; want %v", c.in, got, c.want)
		}
	}
}

// normaliseCRLF makes a test fixture string CRLF-line-ended. The
// fixtures above are written with LF for readability; mailparse's
// strict-boundary check requires CRLF.
func normaliseCRLF(in []byte) []byte {
	out := make([]byte, 0, len(in)+8)
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch c {
		case '\r':
			out = append(out, '\r')
			if i+1 < len(in) && in[i+1] == '\n' {
				out = append(out, '\n')
				i++
			} else {
				out = append(out, '\n')
			}
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, c)
		}
	}
	return out
}
