package sieve

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Script {
	t.Helper()
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return s
}

func TestParse_Empty(t *testing.T) {
	s := mustParse(t, "")
	if len(s.Commands) != 0 || len(s.Requires) != 0 {
		t.Fatalf("empty script must produce empty AST; got %+v", s)
	}
}

func TestParse_Require(t *testing.T) {
	s := mustParse(t, `require ["fileinto","envelope"];`)
	if len(s.Requires) != 2 || s.Requires[0] != "fileinto" || s.Requires[1] != "envelope" {
		t.Fatalf("requires: got %v", s.Requires)
	}
}

func TestParse_IfFileInto(t *testing.T) {
	src := `require "fileinto";
if header :contains "Subject" "hello" {
  fileinto "INBOX.Greetings";
}`
	s := mustParse(t, src)
	if len(s.Commands) != 1 || s.Commands[0].Name != "if" {
		t.Fatalf("expected one if, got %+v", s.Commands)
	}
	if s.Commands[0].Test == nil || s.Commands[0].Test.Name != "header" {
		t.Fatalf("expected header test, got %+v", s.Commands[0].Test)
	}
	if len(s.Commands[0].Block) != 1 || s.Commands[0].Block[0].Name != "fileinto" {
		t.Fatalf("expected fileinto, got %+v", s.Commands[0].Block)
	}
}

func TestParse_Comments(t *testing.T) {
	src := `# top level
/* block
comment */
keep;`
	s := mustParse(t, src)
	if len(s.Commands) != 1 || s.Commands[0].Name != "keep" {
		t.Fatalf("got %+v", s.Commands)
	}
}

func TestParse_EncodedChar(t *testing.T) {
	s := mustParse(t, `require "encoded-character"; set "v" "A${hex:20}B";`)
	// Not expecting variables require here but set uses variables; we just
	// want the literal expansion.
	if !strings.Contains(s.Commands[0].Args[1].Str, "A B") {
		t.Fatalf("encoded-character not expanded: %q", s.Commands[0].Args[1].Str)
	}
}

func TestParse_TextMultiline(t *testing.T) {
	src := "require \"vacation\"; vacation text:\nHi there\nSecond line\n..leading dot\n.\n;"
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(s.Commands) != 1 {
		t.Fatalf("cmds: %+v", s.Commands)
	}
	body := s.Commands[0].Args[0].Str
	if !strings.Contains(body, "Second line") {
		t.Fatalf("multiline missing second line: %q", body)
	}
	if !strings.Contains(body, ".leading dot") {
		t.Fatalf("dot stuffing failed: %q", body)
	}
}

func TestParse_ErrorsHaveLineColumn(t *testing.T) {
	// Unterminated quoted string — guaranteed to error.
	_, err := Parse([]byte(`keep "unterminated`))
	if err == nil {
		t.Fatal("expected parse error")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if pe.Line == 0 || pe.Column == 0 {
		t.Fatalf("ParseError missing line/col: %+v", pe)
	}
}

func TestParse_MaxSize(t *testing.T) {
	big := make([]byte, DefaultMaxScriptBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	_, err := Parse(big)
	if err == nil {
		t.Fatal("expected size error")
	}
}

func TestParse_NestingDepth(t *testing.T) {
	var b strings.Builder
	b.WriteString("require \"fileinto\";")
	for i := 0; i < DefaultMaxNestingDepth+2; i++ {
		b.WriteString(`if true {`)
	}
	b.WriteString("keep;")
	for i := 0; i < DefaultMaxNestingDepth+2; i++ {
		b.WriteString(`}`)
	}
	_, err := Parse([]byte(b.String()))
	if err == nil {
		t.Fatal("expected nesting-depth error")
	}
}

func TestParse_NumberSuffix(t *testing.T) {
	s := mustParse(t, `if size :over 10K {keep;}`)
	if s.Commands[0].Test.Args[1].Num != 10*1024 {
		t.Fatalf("expected 10240, got %d", s.Commands[0].Test.Args[1].Num)
	}
}

func TestParse_AllofTestList(t *testing.T) {
	s := mustParse(t, `if allof(true, false) {keep;}`)
	test := s.Commands[0].Test
	if test.Name != "allof" || len(test.Children) != 2 {
		t.Fatalf("bad test: %+v", test)
	}
}

func TestParse_UnknownEscapeLeftLiteral(t *testing.T) {
	s := mustParse(t, `if header :is "X" "a\nb" {keep;}`)
	// Sieve leaves unknown escapes as literal "\n" (two chars, not newline).
	if got := s.Commands[0].Test.Args[2].Str; got != `a\nb` {
		t.Fatalf("escape handling; got %q", got)
	}
}

func TestParse_StringList(t *testing.T) {
	s := mustParse(t, `if header :is "X" ["a","b","c"] {keep;}`)
	list := s.Commands[0].Test.Args[2].StrList
	if len(list) != 3 || list[0] != "a" {
		t.Fatalf("stringlist: %+v", list)
	}
}
