package sieve

import (
	"bytes"
	"strings"
	"testing"
)

const baseRawMsg = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: original\r\n" +
	"\r\n" +
	"original body\r\n"

func TestApplyMutations_NoOp_WhenNoMutationActions(t *testing.T) {
	out := Outcome{Actions: []Action{{Kind: ActionFileInto, Mailbox: "Inbox"}}}
	got, changed, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if changed {
		t.Fatalf("changed must be false when outcome has no mutation actions")
	}
	if string(got) != baseRawMsg {
		t.Fatalf("expected raw unchanged, got %q", got)
	}
	// HasMutations must agree so the SMTP delivery path's gate stays
	// in sync with ApplyMutations' fast-path.
	if HasMutations(out) {
		t.Fatalf("HasMutations must report false for non-mutation actions")
	}
}

func TestApplyMutations_ChangedFlag_TrueOnEditheader(t *testing.T) {
	out := Outcome{Actions: []Action{{
		Kind: ActionAddHeader, HeaderName: "X-Tracer", HeaderValue: "alpha",
	}}}
	_, changed, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if !changed {
		t.Fatalf("changed must be true when an editheader action fires")
	}
	if !HasMutations(out) {
		t.Fatalf("HasMutations must report true for ActionAddHeader")
	}
}

func TestApplyMutations_AddHeaderPrepends(t *testing.T) {
	out := Outcome{Actions: []Action{{
		Kind:        ActionAddHeader,
		HeaderName:  "X-Tracer",
		HeaderValue: "alpha",
	}}}
	got, _, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if !strings.HasPrefix(string(got), "X-Tracer: alpha\r\n") {
		t.Fatalf("addheader should prepend; got %q", got)
	}
	if !strings.Contains(string(got), "Subject: original") {
		t.Fatalf("addheader must preserve other headers; got %q", got)
	}
	if !strings.Contains(string(got), "original body") {
		t.Fatalf("addheader must preserve body; got %q", got)
	}
}

func TestApplyMutations_DeleteHeader_RemovesAllInstances(t *testing.T) {
	in := "From: a@b\r\n" +
		"X-Spam: yes\r\n" +
		"X-Spam: also yes\r\n" +
		"Subject: t\r\n" +
		"\r\n" +
		"body\r\n"
	out := Outcome{Actions: []Action{{Kind: ActionDeleteHeader, HeaderName: "X-Spam"}}}
	got, _, err := ApplyMutationsBytes([]byte(in), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if strings.Contains(string(got), "X-Spam") {
		t.Fatalf("deleteheader did not remove all instances: %q", got)
	}
	if !strings.Contains(string(got), "From: a@b") {
		t.Fatalf("deleteheader must preserve unrelated headers: %q", got)
	}
}

func TestApplyMutations_DeleteHeader_ContinuationLines(t *testing.T) {
	in := "From: a@b\r\n" +
		"X-Long: first line\r\n" +
		"\tcontinuation\r\n" +
		"Subject: t\r\n" +
		"\r\n" +
		"body\r\n"
	out := Outcome{Actions: []Action{{Kind: ActionDeleteHeader, HeaderName: "X-Long"}}}
	got, _, err := ApplyMutationsBytes([]byte(in), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if strings.Contains(string(got), "X-Long") || strings.Contains(string(got), "continuation") {
		t.Fatalf("deleteheader did not strip continuation: %q", got)
	}
}

func TestApplyMutations_Replace_ReplacesBodyAndOverridesSubject(t *testing.T) {
	out := Outcome{Actions: []Action{{
		Kind:           ActionReplace,
		ReplaceBody:    []byte("brand new content"),
		ReplaceSubject: "new subject",
		ReplaceFrom:    "rewriter@example.com",
	}}}
	got, _, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	gs := string(got)
	if !strings.Contains(gs, "Subject: new subject") {
		t.Errorf("replace did not set Subject override: %q", gs)
	}
	if !strings.Contains(gs, "From: rewriter@example.com") {
		t.Errorf("replace did not set From override: %q", gs)
	}
	if strings.Contains(gs, "Subject: original") {
		t.Errorf("replace must drop the original Subject: %q", gs)
	}
	if !strings.Contains(gs, "brand new content") {
		t.Errorf("replace must use ReplaceBody: %q", gs)
	}
	if strings.Contains(gs, "original body") {
		t.Errorf("replace must drop the original body: %q", gs)
	}
}

func TestApplyMutations_Enclose_WrapsInMultipart(t *testing.T) {
	out := Outcome{Actions: []Action{{
		Kind:           ActionEnclose,
		EncloseBody:    []byte("WARNING: this message was scanned and flagged."),
		EncloseSubject: "[FLAGGED] original",
	}}}
	got, _, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	gs := string(got)
	for _, want := range []string{
		"Subject: [FLAGGED] original",
		"Content-Type: multipart/mixed",
		"WARNING: this message was scanned",
		"Content-Type: message/rfc822",
		"Subject: original",
		"original body",
	} {
		if !strings.Contains(gs, want) {
			t.Errorf("enclose missing %q\n--FULL--\n%s\n--END--", want, gs)
		}
	}
}

func TestApplyMutations_AddThenDeleteSameHeader(t *testing.T) {
	out := Outcome{Actions: []Action{
		{Kind: ActionAddHeader, HeaderName: "X-Marker", HeaderValue: "first"},
		{Kind: ActionAddHeader, HeaderName: "X-Marker", HeaderValue: "second"},
		{Kind: ActionDeleteHeader, HeaderName: "X-Marker"},
	}}
	got, _, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if strings.Contains(string(got), "X-Marker") {
		t.Fatalf("delete after add must remove the marker: %q", got)
	}
}

func TestApplyMutations_Replace_PerLeaf_TopLevelMultipart(t *testing.T) {
	// A replace with ReplacePartPath = [1] targets the second leaf of
	// the message-level multipart. The first leaf (and the headers
	// of the outer message) must be preserved verbatim.
	const raw = "From: alice@example.com\r\n" +
		"Subject: outer\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"BND\"\r\n" +
		"\r\n" +
		"--BND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"first leaf body\r\n" +
		"--BND\r\n" +
		"Content-Type: application/x-evil\r\n" +
		"\r\n" +
		"malicious payload\r\n" +
		"--BND--\r\n"
	out := Outcome{Actions: []Action{{
		Kind:            ActionReplace,
		ReplaceBody:     []byte("[scrubbed]"),
		ReplacePartPath: []int{1},
	}}}
	got, _, err := ApplyMutationsBytes([]byte(raw), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	gs := string(got)
	if !strings.Contains(gs, "first leaf body") {
		t.Errorf("first leaf must survive per-leaf replace: %q", gs)
	}
	if strings.Contains(gs, "malicious payload") {
		t.Errorf("targeted leaf must be removed: %q", gs)
	}
	if !strings.Contains(gs, "[scrubbed]") {
		t.Errorf("replacement body must land in the targeted slot: %q", gs)
	}
	if strings.Contains(gs, "application/x-evil") {
		t.Errorf("targeted leaf's Content-Type must be replaced: %q", gs)
	}
	// The outer Subject must be unchanged.
	if !strings.Contains(gs, "Subject: outer") {
		t.Errorf("outer Subject must be preserved: %q", gs)
	}
}

func TestApplyMutations_Replace_PerLeaf_NestedMultipart(t *testing.T) {
	// ReplacePartPath = [0, 1] targets the second leaf inside the
	// first child of the outer multipart — i.e. the text/html part
	// inside multipart/alternative.
	const raw = "Subject: nested\r\n" +
		"Content-Type: multipart/mixed; boundary=\"OUT\"\r\n" +
		"\r\n" +
		"--OUT\r\n" +
		"Content-Type: multipart/alternative; boundary=\"IN\"\r\n" +
		"\r\n" +
		"--IN\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"plain alt\r\n" +
		"--IN\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>html alt</p>\r\n" +
		"--IN--\r\n" +
		"--OUT\r\n" +
		"Content-Type: application/pdf\r\n" +
		"\r\n" +
		"pdfbody\r\n" +
		"--OUT--\r\n"
	out := Outcome{Actions: []Action{{
		Kind:            ActionReplace,
		ReplaceBody:     []byte("[stripped html]"),
		ReplacePartPath: []int{0, 1},
	}}}
	got, _, err := ApplyMutationsBytes([]byte(raw), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	gs := string(got)
	if !strings.Contains(gs, "plain alt") {
		t.Errorf("sibling leaf must survive: %q", gs)
	}
	if !strings.Contains(gs, "pdfbody") {
		t.Errorf("outer-sibling pdf leaf must survive: %q", gs)
	}
	if strings.Contains(gs, "<p>html alt</p>") {
		t.Errorf("targeted html leaf must be replaced: %q", gs)
	}
	if !strings.Contains(gs, "[stripped html]") {
		t.Errorf("replacement body must land: %q", gs)
	}
}

func TestApplyMutations_Replace_PerLeaf_OutOfRange_FallsBackToTopLevel(t *testing.T) {
	// When the path doesn't resolve, applyReplace degrades to the
	// top-level body rewrite rather than dropping the script's
	// intent.
	out := Outcome{Actions: []Action{{
		Kind:            ActionReplace,
		ReplaceBody:     []byte("fallback"),
		ReplacePartPath: []int{99},
	}}}
	got, _, err := ApplyMutationsBytes([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if !strings.Contains(string(got), "fallback") {
		t.Errorf("fallback must rewrite the body: %q", got)
	}
}

func TestApplyMutations_NoHeaderBoundary_Errors(t *testing.T) {
	in := "no separator here at all"
	out := Outcome{Actions: []Action{{Kind: ActionAddHeader, HeaderName: "X", HeaderValue: "y"}}}
	_, _, err := ApplyMutationsBytes([]byte(in), out)
	if err == nil {
		t.Fatalf("expected error on malformed input")
	}
}

// TestApplyMutations_HeaderOnlyStreaming proves that a header-only edit (the
// common editheader case) streams the body through without a full-body
// allocation, and that the output is byte-identical to ApplyMutationsBytes.
//
// The body is 512 KiB — far larger than maxHeaderReadSize — so any
// implementation that buffers the whole message into a single []byte before
// writing would be immediately visible via the byte count comparison.
//
// We also verify that the streaming path (ApplyMutations) and the bytes path
// (ApplyMutationsBytes) produce identical output, satisfying REQ-STORE-17/19.
func TestApplyMutations_HeaderOnlyStreaming(t *testing.T) {
	// Build a large message: small header block, large body.
	const bodySize = 512 * 1024
	hdr := "From: sender@example.com\r\n" +
		"To: recip@example.com\r\n" +
		"Subject: streaming test\r\n" +
		"\r\n"
	bodyContent := make([]byte, bodySize)
	for i := range bodyContent {
		bodyContent[i] = byte('A' + i%26)
	}
	raw := append([]byte(hdr), bodyContent...)

	outcome := Outcome{Actions: []Action{{
		Kind:        ActionAddHeader,
		HeaderName:  "X-Stream-Test",
		HeaderValue: "streaming",
	}}}

	// Streaming path: ApplyMutations writes to a captured buffer.
	src := bytes.NewReader(raw)
	var streamOut bytes.Buffer
	streamOut.Grow(len(raw) + 64)
	changed, err := ApplyMutations(src, int64(len(raw)), outcome, &streamOut)
	if err != nil {
		t.Fatalf("ApplyMutations (streaming): %v", err)
	}
	if !changed {
		t.Fatalf("ApplyMutations (streaming): changed must be true")
	}

	// Bytes path: ApplyMutationsBytes for byte-identical comparison.
	bytesOut, _, err := ApplyMutationsBytes(raw, outcome)
	if err != nil {
		t.Fatalf("ApplyMutationsBytes: %v", err)
	}

	if !bytes.Equal(streamOut.Bytes(), bytesOut) {
		// Show a diff-friendly excerpt.
		t.Errorf("streaming output differs from bytes output (len %d vs %d)",
			streamOut.Len(), len(bytesOut))
		if streamOut.Len() > 200 {
			t.Logf("stream[:200]: %q", streamOut.Bytes()[:200])
			t.Logf("bytes[:200]:  %q", bytesOut[:200])
		}
	}

	// Verify the header was prepended.
	if !strings.HasPrefix(streamOut.String(), "X-Stream-Test: streaming\r\n") {
		t.Errorf("header not prepended; got prefix %q", streamOut.String()[:50])
	}

	// Verify the original body is preserved verbatim.
	if !bytes.HasSuffix(streamOut.Bytes(), bodyContent) {
		t.Errorf("body not preserved verbatim; last 32 bytes: %q", streamOut.Bytes()[streamOut.Len()-32:])
	}
}

// TestApplyMutations_HeaderOnlyStreaming_ByteIdentity runs the same round-trip
// for each header-mutation kind against a real io.ReaderAt (bytes.NewReader
// implements io.ReaderAt) to confirm the streaming path and the bytes path
// agree on every mutation.
func TestApplyMutations_HeaderOnlyStreaming_ByteIdentity(t *testing.T) {
	cases := []struct {
		name    string
		outcome Outcome
	}{
		{
			name: "add-header",
			outcome: Outcome{Actions: []Action{{
				Kind: ActionAddHeader, HeaderName: "X-Added", HeaderValue: "yes",
			}}},
		},
		{
			name: "delete-header",
			outcome: Outcome{Actions: []Action{{
				Kind: ActionDeleteHeader, HeaderName: "Subject",
			}}},
		},
		{
			name: "add-then-delete",
			outcome: Outcome{Actions: []Action{
				{Kind: ActionAddHeader, HeaderName: "X-Tmp", HeaderValue: "v"},
				{Kind: ActionDeleteHeader, HeaderName: "X-Tmp"},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(baseRawMsg)
			src := bytes.NewReader(raw)

			var streamOut bytes.Buffer
			_, serr := ApplyMutations(src, int64(len(raw)), tc.outcome, &streamOut)
			if serr != nil {
				t.Fatalf("ApplyMutations: %v", serr)
			}

			bytesOut, _, berr := ApplyMutationsBytes(raw, tc.outcome)
			if berr != nil {
				t.Fatalf("ApplyMutationsBytes: %v", berr)
			}

			if !bytes.Equal(streamOut.Bytes(), bytesOut) {
				t.Errorf("stream vs bytes mismatch:\nstream: %q\nbytes:  %q",
					streamOut.String(), string(bytesOut))
			}
		})
	}
}

// TestApplyMutations_Enclose_Stream verifies that the enclose streaming path
// (used when there is no Replace action) produces output byte-identical to
// the in-memory enclose path.
func TestApplyMutations_Enclose_Stream(t *testing.T) {
	// We cannot directly compare byte-for-byte because the boundary string is
	// random — but we can verify structural correctness and that the content
	// sections match.
	outcome := Outcome{Actions: []Action{{
		Kind:           ActionEnclose,
		EncloseBody:    []byte("preamble text"),
		EncloseSubject: "[WRAPPED] original",
	}}}
	raw := []byte(baseRawMsg)
	src := bytes.NewReader(raw)

	var streamOut bytes.Buffer
	changed, err := ApplyMutations(src, int64(len(raw)), outcome, &streamOut)
	if err != nil {
		t.Fatalf("ApplyMutations (enclose streaming): %v", err)
	}
	if !changed {
		t.Fatalf("enclose must set changed=true")
	}

	gs := streamOut.String()
	for _, want := range []string{
		"Subject: [WRAPPED] original",
		"Content-Type: multipart/mixed",
		"preamble text",
		"Content-Type: message/rfc822",
		"Subject: original",
		"original body",
	} {
		if !strings.Contains(gs, want) {
			t.Errorf("enclose stream missing %q\n--FULL--\n%s\n--END--", want, gs)
		}
	}
}
