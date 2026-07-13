package maillist_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/store"
)

func testList() store.MailingList {
	return store.MailingList{
		ID:             7,
		PostingAddress: "announce@example.test",
		DisplayName:    `Announce "Team"`,
		Domain:         "example.test",
		ARCSeal:        true,
	}
}

// TestListIDHeaderValue_Format exercises REQ-MLIST-20's exact shape:
// `"<display_name>" <listname.domain>`, with the display name's own
// double quotes escaped so the result is a single well-formed
// RFC 5322 quoted-string.
func TestListIDHeaderValue_Format(t *testing.T) {
	got := maillist.ListIDHeaderValue(testList())
	want := `"Announce \"Team\"" <announce.example.test>`
	if got != want {
		t.Fatalf("ListIDHeaderValue = %q, want %q", got, want)
	}
}

func TestListIdentifier(t *testing.T) {
	if got, want := maillist.ListIdentifier(testList()), "announce.example.test"; got != want {
		t.Fatalf("ListIdentifier = %q, want %q", got, want)
	}
}

func TestBounceAddress(t *testing.T) {
	if got, want := maillist.BounceAddress(testList()), "announce+bounce@example.test"; got != want {
		t.Fatalf("BounceAddress = %q, want %q", got, want)
	}
}

// TestVERPBounceAddress_ShapeAndRoundTrip exercises REQ-MLIST-50's
// address shape and that the embedded token round-trips through
// ParseVERPBounceLocalPart + TokenSigner.Verify back to the list and
// member it was minted for.
func TestVERPBounceAddress_ShapeAndRoundTrip(t *testing.T) {
	ml := testList()
	ts := maillist.NewTokenSigner(testDataKey())
	addr, err := maillist.VERPBounceAddress(ts, ml, 42)
	if err != nil {
		t.Fatalf("VERPBounceAddress: %v", err)
	}
	if !strings.HasPrefix(addr, "announce+bounce-") {
		t.Fatalf("VERPBounceAddress = %q, want prefix \"announce+bounce-\"", addr)
	}
	if !strings.HasSuffix(addr, "@example.test") {
		t.Fatalf("VERPBounceAddress = %q, want suffix \"@example.test\"", addr)
	}

	local := strings.TrimSuffix(addr, "@example.test")
	base, token, ok := maillist.ParseVERPBounceLocalPart(local)
	if !ok {
		t.Fatalf("ParseVERPBounceLocalPart(%q) did not recognise the VERP shape", local)
	}
	if base != "announce" {
		t.Fatalf("ParseVERPBounceLocalPart base = %q, want \"announce\"", base)
	}
	memberID, err := ts.Verify(maillist.TokenPurposeVERP, ml.ID, token, time.Now())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if memberID != 42 {
		t.Fatalf("Verify member id = %d, want 42", memberID)
	}
}

// TestParseVERPBounceLocalPart_OrdinaryAddressesNotMatched exercises the
// negative cases: the plain S1 bounce address (no token suffix) and
// ordinary local-parts must not be recognised as a VERP shape.
func TestParseVERPBounceLocalPart_OrdinaryAddressesNotMatched(t *testing.T) {
	cases := []string{
		"announce",
		"announce+bounce", // S1 shape, no token
		"alice+something", // unrelated sub-addressing
		"",
	}
	for _, lp := range cases {
		if _, _, ok := maillist.ParseVERPBounceLocalPart(lp); ok {
			t.Errorf("ParseVERPBounceLocalPart(%q) = ok, want not recognised", lp)
		}
	}
}

// TestLoopDetected exercises REQ-MLIST-30: a List-ID header naming this
// list (by its bracketed list-id token, regardless of display-name
// phrasing) is a loop; any other value, or none, is not.
func TestLoopDetected(t *testing.T) {
	ml := testList()
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"absent", "", false},
		{"exact", `"Announce" <announce.example.test>`, true},
		{"renamed display name still matches list-id token", `"New Name" <announce.example.test>`, true},
		{"case insensitive", `"Announce" <ANNOUNCE.EXAMPLE.TEST>`, true},
		{"different list", `"Other" <other.example.test>`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maillist.LoopDetected(c.header, ml); got != c.want {
				t.Errorf("LoopDetected(%q) = %v, want %v", c.header, got, c.want)
			}
		})
	}
}

// TestAutoSubmittedBlocks exercises REQ-MLIST-31.
func TestAutoSubmittedBlocks(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"no", false},
		{"No", false},
		{" no ", false},
		{"no; foo=bar", false},
		{"auto-replied", true},
		{"auto-generated", true},
		{"auto-forwarded", true},
	}
	for _, c := range cases {
		if got := maillist.AutoSubmittedBlocks(c.value); got != c.want {
			t.Errorf("AutoSubmittedBlocks(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}

// TestSizeExceeds exercises REQ-MLIST-32's list-override-then-deployment-
// default resolution.
func TestSizeExceeds(t *testing.T) {
	ml := testList()
	if maillist.SizeExceeds(100, ml, 0) {
		t.Errorf("no limit configured anywhere: SizeExceeds should be false")
	}
	if !maillist.SizeExceeds(100, ml, 50) {
		t.Errorf("deployment default 50 < size 100: SizeExceeds should be true")
	}
	ml.MaxMessageSizeBytes = 200
	if maillist.SizeExceeds(100, ml, 50) {
		t.Errorf("list override 200 >= size 100: SizeExceeds should be false, list override must win over the smaller deployment default")
	}
	if !maillist.SizeExceeds(300, ml, 50) {
		t.Errorf("list override 200 < size 300: SizeExceeds should be true")
	}
}

const crlfMessage = "From: poster@sender.test\r\n" +
	"To: announce@example.test\r\n" +
	"Subject: quarterly update\r\n" +
	"Message-ID: <shape-1@sender.test>\r\n" +
	"\r\n" +
	"Body text.\r\n"

// TestShapeMessage_PrependsListHeaders exercises REQ-MLIST-20/24: every
// shaped copy carries List-ID, List-Post, and Auto-Submitted:
// auto-forwarded, and the original headers/body survive untouched.
func TestShapeMessage_PrependsListHeaders(t *testing.T) {
	ml := testList()
	out := string(maillist.ShapeMessage([]byte(crlfMessage), ml, "quarterly update"))

	if !strings.Contains(out, "List-ID: \"Announce \\\"Team\\\"\" <announce.example.test>\r\n") {
		t.Errorf("missing/incorrect List-ID header:\n%s", out)
	}
	if !strings.Contains(out, "List-Post: <mailto:announce@example.test>\r\n") {
		t.Errorf("missing/incorrect List-Post header:\n%s", out)
	}
	if !strings.Contains(out, "Auto-Submitted: auto-forwarded\r\n") {
		t.Errorf("missing Auto-Submitted: auto-forwarded header:\n%s", out)
	}
	if !strings.Contains(out, "From: poster@sender.test\r\n") {
		t.Errorf("original From: header not preserved:\n%s", out)
	}
	if !strings.Contains(out, "Body text.\r\n") {
		t.Errorf("original body not preserved:\n%s", out)
	}
}

// TestShapeMessage_StripsExistingAutoSubmitted verifies the shaped copy
// carries exactly one Auto-Submitted header even when the accepted post
// already had one (which, per the REQ-MLIST-31 guard, can only be the
// literal "no" by the time ShapeMessage runs).
func TestShapeMessage_StripsExistingAutoSubmitted(t *testing.T) {
	raw := "From: poster@sender.test\r\n" +
		"To: announce@example.test\r\n" +
		"Subject: hi\r\n" +
		"Auto-Submitted: no\r\n" +
		"\r\n" +
		"Body.\r\n"
	out := string(maillist.ShapeMessage([]byte(raw), testList(), "hi"))
	if n := strings.Count(out, "Auto-Submitted:"); n != 1 {
		t.Fatalf("Auto-Submitted header count = %d, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "Auto-Submitted: auto-forwarded\r\n") {
		t.Errorf("Auto-Submitted value not replaced with auto-forwarded:\n%s", out)
	}
}

// TestShapeMessage_SubjectTag_Idempotent exercises REQ-MLIST-23: the
// tag is prepended once, and a reply that already carries the tag is
// not double-tagged.
func TestShapeMessage_SubjectTag_Idempotent(t *testing.T) {
	tag := "ANN"
	ml := testList()
	ml.SubjectTag = &tag

	untagged := maillist.ShapeMessage([]byte(crlfMessage), ml, "quarterly update")
	if !strings.Contains(string(untagged), "Subject: [ANN] quarterly update\r\n") {
		t.Fatalf("expected tagged subject, got:\n%s", untagged)
	}

	alreadyTagged := "From: poster@sender.test\r\n" +
		"To: announce@example.test\r\n" +
		"Subject: [ANN] quarterly update\r\n" +
		"Message-ID: <shape-2@sender.test>\r\n" +
		"\r\n" +
		"Body text.\r\n"
	out := string(maillist.ShapeMessage([]byte(alreadyTagged), ml, "[ANN] quarterly update"))
	if n := strings.Count(out, "[ANN]"); n != 1 {
		t.Fatalf("subject tag applied %d times, want 1 (idempotent):\n%s", n, out)
	}
}

// TestShapeMessage_NoSubjectHeader exercises the defensive fallback
// when a post configured with a subject tag has no Subject header at
// all.
func TestShapeMessage_NoSubjectHeader(t *testing.T) {
	tag := "ANN"
	ml := testList()
	ml.SubjectTag = &tag
	raw := "From: poster@sender.test\r\nTo: announce@example.test\r\n\r\nBody.\r\n"
	out := string(maillist.ShapeMessage([]byte(raw), ml, ""))
	if !strings.Contains(out, "Subject: [ANN] \r\n") {
		t.Fatalf("expected a synthesized tagged Subject header, got:\n%s", out)
	}
}

// TestShapeMessage_FoldedSubjectPreserved verifies a folded (multi-line)
// Subject header's continuation lines survive the tag splice unchanged.
func TestShapeMessage_FoldedSubjectPreserved(t *testing.T) {
	tag := "ANN"
	ml := testList()
	ml.SubjectTag = &tag
	raw := "From: poster@sender.test\r\n" +
		"Subject: a very long subject that\r\n" +
		" wraps onto a continuation line\r\n" +
		"\r\n" +
		"Body.\r\n"
	out := string(maillist.ShapeMessage([]byte(raw), ml, "a very long subject that wraps onto a continuation line"))
	if !strings.Contains(out, "Subject: [ANN] a very long subject that\r\n wraps onto a continuation line\r\n") {
		t.Fatalf("folded subject not preserved correctly:\n%s", out)
	}
}
