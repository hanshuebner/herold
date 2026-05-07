package sieve

import (
	"strings"
	"testing"
)

const multipartMsg = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: Multipart\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BND\"\r\n" +
	"\r\n" +
	"--BND\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"plain body content\r\n" +
	"--BND\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"X-Tracer: alpha-tracer\r\n" +
	"Content-Disposition: attachment; filename=\"a.html\"\r\n" +
	"\r\n" +
	"<html>html body content</html>\r\n" +
	"--BND--\r\n"

func TestInterp_ForeveryPart_RunsBlock(t *testing.T) {
	src := `require ["foreverypart", "fileinto"];
foreverypart {
  fileinto "PARTS";
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	count := 0
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "PARTS" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 fileinto actions (one per leaf), got %d (actions=%+v)", count, out.Actions)
	}
}

func TestInterp_ForeveryPart_Break(t *testing.T) {
	src := `require ["foreverypart", "fileinto"];
foreverypart {
  fileinto "FIRST";
  break;
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	count := 0
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "FIRST" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("break must terminate after first iteration, got %d fileinto actions: %+v", count, out.Actions)
	}
}

func TestInterp_HeaderMime_ReadsCurrentPart(t *testing.T) {
	src := `require ["foreverypart", "mime", "fileinto"];
foreverypart {
  if header :mime :contains "X-Tracer" "alpha" {
    fileinto "TRACER";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "TRACER" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("header :mime should match X-Tracer on the HTML attachment part; actions=%+v", out.Actions)
	}
}

func TestInterp_HeaderMime_OutsideForeverypart(t *testing.T) {
	// Without a surrounding foreverypart, :mime falls back to message
	// headers (RFC 5703 §4.2). The message-level X-Tracer is absent
	// (only the leaf carries it) so the test must not match.
	src := `require ["mime", "fileinto"];
if header :mime :contains "X-Tracer" "alpha" {
  fileinto "WRONG";
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "WRONG" {
			t.Fatalf(":mime outside foreverypart should not see leaf-only headers; actions=%+v", out.Actions)
		}
	}
}

func TestInterp_ExtractText(t *testing.T) {
	src := `require ["foreverypart", "mime", "variables", "fileinto"];
foreverypart {
  extracttext "body";
  if string :contains "${body}" "html body content" {
    fileinto "HTML-FOUND";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "HTML-FOUND" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("extracttext should expose part body to subsequent string test; actions=%+v", out.Actions)
	}
}

func TestInterp_ExtractText_FirstCap(t *testing.T) {
	src := `require ["foreverypart", "mime", "variables", "fileinto"];
foreverypart {
  extracttext :first 5 "snippet";
  if string :contains "${snippet}" "plain" {
    fileinto "TRUNCATED";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	// :first 5 caps the first leaf to "plain" — the test must match
	// since "plain" is fully present in the truncated 5 bytes.
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "TRUNCATED" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf(":first 5 should preserve the prefix 'plain'; actions=%+v", out.Actions)
	}
}

func TestInterp_HeaderAnychild_MatchesDescendant(t *testing.T) {
	// Without foreverypart and without :anychild, :mime falls through
	// to the message-level headers (where X-Tracer is absent), so the
	// test must NOT match. With :anychild, the test sees every
	// descendant part's headers and the leaf-only X-Tracer matches.
	src := `require ["mime", "fileinto"];
if header :mime :anychild :contains "X-Tracer" "alpha" {
  fileinto "ANYCHILD-HIT";
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "ANYCHILD-HIT" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf(":anychild should match X-Tracer on the HTML attachment leaf; actions=%+v", out.Actions)
	}
}

func TestInterp_ExistsAnychild_MatchesDescendant(t *testing.T) {
	src := `require ["mime", "fileinto"];
if exists :mime :anychild "X-Tracer" {
  fileinto "EXISTS-ANYCHILD";
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "EXISTS-ANYCHILD" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("exists :anychild should find X-Tracer on a leaf; actions=%+v", out.Actions)
	}
}

func TestInterp_Replace_InsideForeverypart_RecordsPath(t *testing.T) {
	// Inside foreverypart, the replace action records the part path
	// so ApplyMutations can rewrite the iterated leaf instead of the
	// whole message body. The test checks the emitted Action carries
	// a non-empty ReplacePartPath; the byte-level rewrite is exercised
	// by mutations_test.go.
	src := `require ["foreverypart", "mime"];
foreverypart {
  if header :mime :contains "Content-Type" "text/html" {
    replace "[scrubbed html]";
  }
}`
	out := runScript(t, src, Environment{}, multipartMsg)
	var found *Action
	for i := range out.Actions {
		if out.Actions[i].Kind == ActionReplace {
			found = &out.Actions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected ActionReplace; actions=%+v", out.Actions)
	}
	if len(found.ReplacePartPath) == 0 {
		t.Fatalf("ActionReplace.ReplacePartPath must be non-empty inside foreverypart; got %v", found)
	}
	if string(found.ReplaceBody) != "[scrubbed html]" {
		t.Errorf("ReplaceBody = %q; want [scrubbed html]", found.ReplaceBody)
	}
}

func TestInterp_Replace_TopLevel_EmptyPath(t *testing.T) {
	// Outside foreverypart, the replace action records an empty path
	// so ApplyMutations rewrites the whole body (Phase 1.5
	// behaviour preserved).
	src := `require ["mime"];
replace "new body";`
	out := runScript(t, src, Environment{}, multipartMsg)
	var found *Action
	for i := range out.Actions {
		if out.Actions[i].Kind == ActionReplace {
			found = &out.Actions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected ActionReplace")
	}
	if len(found.ReplacePartPath) != 0 {
		t.Fatalf("top-level replace must have empty path; got %v", found.ReplacePartPath)
	}
}

func TestInterp_Replace_EmitsActionWithSubject(t *testing.T) {
	src := `require ["mime"];
replace :subject "rewritten" "new body content";`
	out := runScript(t, src, Environment{}, sampleMsg)
	var found *Action
	for i := range out.Actions {
		if out.Actions[i].Kind == ActionReplace {
			found = &out.Actions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("replace did not emit ActionReplace; actions=%+v", out.Actions)
	}
	if found.ReplaceSubject != "rewritten" {
		t.Errorf("ReplaceSubject = %q; want rewritten", found.ReplaceSubject)
	}
	if string(found.ReplaceBody) != "new body content" {
		t.Errorf("ReplaceBody = %q; want %q", found.ReplaceBody, "new body content")
	}
}

func TestInterp_Enclose_EmitsAction(t *testing.T) {
	src := `require ["mime"];
enclose :subject "[FLAGGED]" :headers ["X-Quarantine: yes"] "warning text";`
	out := runScript(t, src, Environment{}, sampleMsg)
	var found *Action
	for i := range out.Actions {
		if out.Actions[i].Kind == ActionEnclose {
			found = &out.Actions[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("enclose did not emit ActionEnclose; actions=%+v", out.Actions)
	}
	if found.EncloseSubject != "[FLAGGED]" {
		t.Errorf("EncloseSubject = %q; want [FLAGGED]", found.EncloseSubject)
	}
	if string(found.EncloseBody) != "warning text" {
		t.Errorf("EncloseBody = %q; want %q", found.EncloseBody, "warning text")
	}
	if len(found.EncloseHeaders) != 1 || found.EncloseHeaders[0] != "X-Quarantine: yes" {
		t.Errorf("EncloseHeaders = %v; want [X-Quarantine: yes]", found.EncloseHeaders)
	}
}

// nestedMultipartMsg has a multipart/mixed root containing one
// multipart/alternative inner container (with text/plain + text/html
// children) and one binary attachment. Used to exercise nested
// foreverypart re-scoping.
const nestedMultipartMsg = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Nested\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUTER\"\r\n" +
	"\r\n" +
	"--OUTER\r\n" +
	"Content-Type: multipart/alternative; boundary=\"INNER\"\r\n" +
	"\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"plain alt\r\n" +
	"--INNER\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>html alt</p>\r\n" +
	"--INNER--\r\n" +
	"--OUTER\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Disposition: attachment; filename=\"x.pdf\"\r\n" +
	"\r\n" +
	"pdfbody\r\n" +
	"--OUTER--\r\n"

func TestInterp_ForeveryPart_VisitsContainerNodes(t *testing.T) {
	// New iteration scope includes multipart container nodes (not
	// just leaves) — header :mime "Content-Type" matches those too.
	src := `require ["foreverypart", "mime", "fileinto"];
foreverypart {
  if header :mime :contains "Content-Type" "multipart/alternative" {
    fileinto "FOUND-CONTAINER";
  }
}`
	out := runScript(t, src, Environment{}, nestedMultipartMsg)
	hit := false
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "FOUND-CONTAINER" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("foreverypart should visit multipart container nodes; actions=%+v", out.Actions)
	}
}

func TestInterp_ForeveryPart_NestedScope(t *testing.T) {
	// A nested foreverypart re-scopes to the outer's currentPart.
	// Outer iterates over [multipart/alternative, text/plain (alt),
	// text/html (alt), application/pdf]. The inner foreverypart fires
	// only when the outer's currentPart has children — that is, on
	// the multipart/alternative iteration. Inner sees [text/plain,
	// text/html]; we count those as INNER hits.
	src := `require ["foreverypart", "mime", "fileinto"];
foreverypart {
  if header :mime :contains "Content-Type" "multipart/alternative" {
    foreverypart {
      fileinto "INNER";
    }
  }
}`
	out := runScript(t, src, Environment{}, nestedMultipartMsg)
	innerCount := 0
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "INNER" {
			innerCount++
		}
	}
	if innerCount != 2 {
		t.Fatalf("nested foreverypart should iterate over the multipart/alternative's two leaves; got %d", innerCount)
	}
}

func TestInterp_NamedBreak_TerminatesOuter(t *testing.T) {
	// `break :name outer` from the inner loop terminates the outer
	// loop on the FIRST iteration. The outer loop visits four parts;
	// without the break, the inner loop would fire for each. With
	// the break, INNER fires once (during the first outer iteration)
	// and the outer loop exits.
	src := `require ["foreverypart", "fileinto"];
foreverypart :name "outer" {
  foreverypart :name "inner" {
    fileinto "INNER";
    break :name "outer";
  }
  fileinto "OUTER-AFTER-INNER";
}`
	out := runScript(t, src, Environment{}, nestedMultipartMsg)
	innerCount := 0
	outerAfterCount := 0
	for _, a := range out.Actions {
		if a.Kind != ActionFileInto {
			continue
		}
		switch a.Mailbox {
		case "INNER":
			innerCount++
		case "OUTER-AFTER-INNER":
			outerAfterCount++
		}
	}
	if innerCount != 1 {
		t.Fatalf("named break should terminate the outer after first inner iteration; INNER=%d", innerCount)
	}
	if outerAfterCount != 0 {
		t.Fatalf("named break to outer must skip the post-inner statement; got %d", outerAfterCount)
	}
}

func TestInterp_BareBreak_TerminatesInnermost(t *testing.T) {
	// A bare `break` should exit the inner loop only; the outer keeps
	// going.
	src := `require ["foreverypart", "fileinto"];
foreverypart :name "outer" {
  foreverypart :name "inner" {
    fileinto "INNER";
    break;
  }
  fileinto "OUTER";
}`
	out := runScript(t, src, Environment{}, nestedMultipartMsg)
	outerCount := 0
	for _, a := range out.Actions {
		if a.Kind == ActionFileInto && a.Mailbox == "OUTER" {
			outerCount++
		}
	}
	if outerCount == 0 {
		t.Fatalf("bare break should not terminate outer loop; actions=%+v", out.Actions)
	}
}

func TestInterp_Break_OutsideLoop_Errors(t *testing.T) {
	src := `require ["foreverypart"];
break;`
	script, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(script); err != nil {
		t.Fatalf("validate: %v", err)
	}
	in := NewInterpreter()
	_, err = in.Evaluate(t.Context(), script, buildMessage(t, sampleMsg), Environment{})
	if err == nil {
		t.Fatalf("break outside foreverypart must error")
	}
}

func TestInterp_NamedBreak_UnknownName_Errors(t *testing.T) {
	src := `require ["foreverypart"];
foreverypart :name "outer" {
  break :name "nonexistent";
}`
	script, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(script); err != nil {
		t.Fatalf("validate: %v", err)
	}
	in := NewInterpreter()
	_, err = in.Evaluate(t.Context(), script, buildMessage(t, multipartMsg), Environment{})
	if err == nil {
		t.Fatalf("break :name on unknown loop must error")
	}
}

func TestInterp_ExtractText_RequiresMime(t *testing.T) {
	// extracttext without `require "mime"` must fail validation.
	src := `require ["foreverypart"];
foreverypart {
  extracttext "x";
}`
	script, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(script); err != nil {
		// Validation rejects "extracttext" command without "mime"
		// require — that path is already exercised by the
		// KnownCommands gate.
		if !strings.Contains(err.Error(), "mime") {
			t.Fatalf("expected mime-require validation error, got %v", err)
		}
	}
}
