package sieve

import (
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
	got, err := ApplyMutations([]byte(baseRawMsg), out)
	if err != nil {
		t.Fatalf("ApplyMutations: %v", err)
	}
	if string(got) != baseRawMsg {
		t.Fatalf("expected raw unchanged, got %q", got)
	}
}

func TestApplyMutations_AddHeaderPrepends(t *testing.T) {
	out := Outcome{Actions: []Action{{
		Kind:        ActionAddHeader,
		HeaderName:  "X-Tracer",
		HeaderValue: "alpha",
	}}}
	got, err := ApplyMutations([]byte(baseRawMsg), out)
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
	got, err := ApplyMutations([]byte(in), out)
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
	got, err := ApplyMutations([]byte(in), out)
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
	got, err := ApplyMutations([]byte(baseRawMsg), out)
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
	got, err := ApplyMutations([]byte(baseRawMsg), out)
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
	got, err := ApplyMutations([]byte(baseRawMsg), out)
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
	got, err := ApplyMutations([]byte(raw), out)
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
	got, err := ApplyMutations([]byte(raw), out)
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
	got, err := ApplyMutations([]byte(baseRawMsg), out)
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
	_, err := ApplyMutations([]byte(in), out)
	if err == nil {
		t.Fatalf("expected error on malformed input")
	}
}
