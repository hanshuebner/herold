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

func TestApplyMutations_NoHeaderBoundary_Errors(t *testing.T) {
	in := "no separator here at all"
	out := Outcome{Actions: []Action{{Kind: ActionAddHeader, HeaderName: "X", HeaderValue: "y"}}}
	_, err := ApplyMutations([]byte(in), out)
	if err == nil {
		t.Fatalf("expected error on malformed input")
	}
}
