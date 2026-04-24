package sieve

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultMaxScriptBytes bounds the source size Parse will accept. 256 KiB
// matches the upper bound ManageSieve operators typically enforce on
// uploaded scripts; larger scripts are a sign of abuse or corruption.
const DefaultMaxScriptBytes = 256 * 1024

// DefaultMaxNestingDepth bounds block + test nesting at 1024 per
// STANDARDS.md §9 input limits.
const DefaultMaxNestingDepth = 1024

// ParseError describes one parse failure with source position.
type ParseError struct {
	Line    int
	Column  int
	Message string
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	return fmt.Sprintf("sieve parse error at %d:%d: %s", e.Line, e.Column, e.Message)
}

// Parse turns source bytes into an AST. It enforces DefaultMaxScriptBytes
// and DefaultMaxNestingDepth; exceed either and Parse returns a ParseError.
// The returned Script is suitable input to Validate and the Interpreter.
func Parse(source []byte) (*Script, error) {
	if int64(len(source)) > DefaultMaxScriptBytes {
		return nil, &ParseError{Line: 1, Column: 1, Message: fmt.Sprintf("script exceeds %d bytes", DefaultMaxScriptBytes)}
	}
	p := &parser{src: source, line: 1, col: 1}
	script, err := p.parseScript()
	if err != nil {
		return nil, err
	}
	return script, nil
}

type parser struct {
	src  []byte
	pos  int
	line int
	col  int
	// depth tracks current nesting depth for block + test combinators.
	depth int
}

func (p *parser) errorf(line, col int, format string, args ...any) *ParseError {
	return &ParseError{Line: line, Column: col, Message: fmt.Sprintf(format, args...)}
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

// peek returns the rune at the current position without advancing. Returns
// utf8.RuneError at EOF.
func (p *parser) peek() rune {
	if p.eof() {
		return utf8.RuneError
	}
	r, _ := utf8.DecodeRune(p.src[p.pos:])
	return r
}

func (p *parser) advance() rune {
	if p.eof() {
		return utf8.RuneError
	}
	r, n := utf8.DecodeRune(p.src[p.pos:])
	p.pos += n
	if r == '\n' {
		p.line++
		p.col = 1
	} else {
		p.col++
	}
	return r
}

// skipWhitespaceAndComments advances over ASCII whitespace, Sieve bracket
// comments ("/* ... */"), and hash comments ("# ... \n"). Sieve permits
// both comment forms per RFC 5228 §2.3.
func (p *parser) skipWhitespaceAndComments() error {
	for !p.eof() {
		r := p.peek()
		switch {
		case unicode.IsSpace(r):
			p.advance()
		case r == '#':
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
		case r == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			startLine, startCol := p.line, p.col
			p.advance()
			p.advance()
			closed := false
			for !p.eof() {
				if p.peek() == '*' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/' {
					p.advance()
					p.advance()
					closed = true
					break
				}
				p.advance()
			}
			if !closed {
				return p.errorf(startLine, startCol, "unterminated bracket comment")
			}
		default:
			return nil
		}
	}
	return nil
}

// parseScript parses the top-level sequence of commands.
func (p *parser) parseScript() (*Script, error) {
	s := &Script{}
	for {
		if err := p.skipWhitespaceAndComments(); err != nil {
			return nil, err
		}
		if p.eof() {
			break
		}
		cmd, err := p.parseCommand()
		if err != nil {
			return nil, err
		}
		if cmd.Name == "require" {
			// require accepts a string or a string-list; flatten into
			// Script.Requires for downstream validation.
			if len(cmd.Args) != 1 {
				return nil, p.errorf(cmd.Line, cmd.Column, "require takes exactly one string-list argument")
			}
			switch cmd.Args[0].Kind {
			case ArgString:
				s.Requires = append(s.Requires, cmd.Args[0].Str)
			case ArgStringList:
				s.Requires = append(s.Requires, cmd.Args[0].StrList...)
			default:
				return nil, p.errorf(cmd.Line, cmd.Column, "require argument must be a string or string-list")
			}
			continue
		}
		s.Commands = append(s.Commands, cmd)
	}
	return s, nil
}

// parseCommand parses one identifier + args + optional test + optional
// block + trailing semicolon or `{`.
func (p *parser) parseCommand() (Command, error) {
	if err := p.skipWhitespaceAndComments(); err != nil {
		return Command{}, err
	}
	startLine, startCol := p.line, p.col
	name, err := p.readIdentifier()
	if err != nil {
		return Command{}, err
	}
	cmd := Command{Name: strings.ToLower(name), Line: startLine, Column: startCol}
	// Parse arguments until we hit ';', '{', or an identifier-looking token
	// that introduces a test (the test is itself introduced by an
	// identifier for if/elsif, otherwise arguments terminate at ';' or '{').
	for {
		if err := p.skipWhitespaceAndComments(); err != nil {
			return Command{}, err
		}
		if p.eof() {
			return Command{}, p.errorf(p.line, p.col, "unexpected EOF while parsing command %q", cmd.Name)
		}
		r := p.peek()
		switch {
		case r == ';':
			p.advance()
			return cmd, nil
		case r == '{':
			// Block form; parse child block.
			if err := p.enterDepth(startLine, startCol); err != nil {
				return Command{}, err
			}
			block, err := p.parseBlock()
			p.depth--
			if err != nil {
				return Command{}, err
			}
			cmd.Block = block
			return cmd, nil
		case r == ':' || r == '"' || r == '[' || (r >= '0' && r <= '9'):
			arg, err := p.parseArgument()
			if err != nil {
				return Command{}, err
			}
			cmd.Args = append(cmd.Args, arg)
		case p.pos+4 < len(p.src) && string(p.src[p.pos:p.pos+5]) == "text:":
			// "text:" starts a multiline-string argument.
			arg, err := p.parseArgument()
			if err != nil {
				return Command{}, err
			}
			cmd.Args = append(cmd.Args, arg)
		case isIdentStart(r):
			// For control-flow commands we allow exactly one Test here.
			// We don't dispatch on the command name; any command may have
			// zero or one tests trailing. Validation catches misuse.
			if cmd.Test != nil {
				return Command{}, p.errorf(p.line, p.col, "unexpected identifier %q after test", r)
			}
			test, err := p.parseTest()
			if err != nil {
				return Command{}, err
			}
			cmd.Test = &test
		default:
			return Command{}, p.errorf(p.line, p.col, "unexpected character %q in command arguments", r)
		}
	}
}

func (p *parser) enterDepth(line, col int) error {
	p.depth++
	if p.depth > DefaultMaxNestingDepth {
		return p.errorf(line, col, "nesting depth exceeds %d", DefaultMaxNestingDepth)
	}
	return nil
}

// parseBlock consumes `{ cmd; cmd; ... }`.
func (p *parser) parseBlock() ([]Command, error) {
	// leading '{'
	startLine, startCol := p.line, p.col
	if p.peek() != '{' {
		return nil, p.errorf(startLine, startCol, "expected '{'")
	}
	p.advance()
	var out []Command
	for {
		if err := p.skipWhitespaceAndComments(); err != nil {
			return nil, err
		}
		if p.eof() {
			return nil, p.errorf(startLine, startCol, "unterminated block")
		}
		if p.peek() == '}' {
			p.advance()
			return out, nil
		}
		cmd, err := p.parseCommand()
		if err != nil {
			return nil, err
		}
		out = append(out, cmd)
	}
}

// parseTest parses one test: identifier + args, or a combinator.
func (p *parser) parseTest() (Test, error) {
	if err := p.skipWhitespaceAndComments(); err != nil {
		return Test{}, err
	}
	startLine, startCol := p.line, p.col
	name, err := p.readIdentifier()
	if err != nil {
		return Test{}, err
	}
	t := Test{Name: strings.ToLower(name), Line: startLine, Column: startCol}
	switch t.Name {
	case "allof", "anyof":
		children, err := p.parseTestList()
		if err != nil {
			return Test{}, err
		}
		t.Children = children
		return t, nil
	case "not":
		if err := p.enterDepth(startLine, startCol); err != nil {
			return Test{}, err
		}
		child, err := p.parseTest()
		p.depth--
		if err != nil {
			return Test{}, err
		}
		t.Children = []Test{child}
		return t, nil
	case "true", "false":
		return t, nil
	}
	// Regular test: trailing tags + arguments up to end-of-test context
	// (terminator is whatever the caller decides — a ',' or ')' in a test
	// list, '{' starting a block at command level, or a ')' in not(...)).
	for {
		if err := p.skipWhitespaceAndComments(); err != nil {
			return Test{}, err
		}
		if p.eof() {
			return Test{}, p.errorf(p.line, p.col, "unexpected EOF in test %q", t.Name)
		}
		r := p.peek()
		switch {
		case r == ',' || r == ')' || r == '{' || r == ';':
			return t, nil
		case r == ':' || r == '"' || r == '[' || (r >= '0' && r <= '9'):
			arg, err := p.parseArgument()
			if err != nil {
				return Test{}, err
			}
			t.Args = append(t.Args, arg)
		default:
			return Test{}, p.errorf(p.line, p.col, "unexpected character %q in test %q", r, t.Name)
		}
	}
}

// parseTestList consumes '(' test (',' test)* ')'.
func (p *parser) parseTestList() ([]Test, error) {
	if err := p.skipWhitespaceAndComments(); err != nil {
		return nil, err
	}
	if p.peek() != '(' {
		return nil, p.errorf(p.line, p.col, "expected '(' for test list")
	}
	if err := p.enterDepth(p.line, p.col); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	p.advance()
	var out []Test
	for {
		if err := p.skipWhitespaceAndComments(); err != nil {
			return nil, err
		}
		if p.peek() == ')' {
			p.advance()
			return out, nil
		}
		t, err := p.parseTest()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		if err := p.skipWhitespaceAndComments(); err != nil {
			return nil, err
		}
		if p.peek() == ',' {
			p.advance()
			continue
		}
		if p.peek() == ')' {
			p.advance()
			return out, nil
		}
		return nil, p.errorf(p.line, p.col, "expected ',' or ')' in test list")
	}
}

// parseArgument parses one Argument (tag / string / number / string-list).
func (p *parser) parseArgument() (Argument, error) {
	if err := p.skipWhitespaceAndComments(); err != nil {
		return Argument{}, err
	}
	startLine, startCol := p.line, p.col
	r := p.peek()
	switch {
	case r == ':':
		p.advance()
		id, err := p.readIdentifier()
		if err != nil {
			return Argument{}, err
		}
		return Argument{Kind: ArgTag, Tag: ":" + strings.ToLower(id), Line: startLine, Column: startCol}, nil
	case r == '"':
		s, err := p.readQuotedString()
		if err != nil {
			return Argument{}, err
		}
		return Argument{Kind: ArgString, Str: s, Line: startLine, Column: startCol}, nil
	case r == 't':
		// Could be "text:" multiline form. Peek ahead.
		if p.pos+4 < len(p.src) && string(p.src[p.pos:p.pos+5]) == "text:" {
			s, err := p.readMultilineString()
			if err != nil {
				return Argument{}, err
			}
			return Argument{Kind: ArgString, Str: s, Line: startLine, Column: startCol}, nil
		}
		return Argument{}, p.errorf(startLine, startCol, "unexpected identifier-looking token %q in argument", r)
	case r == '[':
		list, err := p.readStringList()
		if err != nil {
			return Argument{}, err
		}
		return Argument{Kind: ArgStringList, StrList: list, Line: startLine, Column: startCol}, nil
	case r >= '0' && r <= '9':
		n, err := p.readNumber()
		if err != nil {
			return Argument{}, err
		}
		return Argument{Kind: ArgNumber, Num: n, Line: startLine, Column: startCol}, nil
	default:
		return Argument{}, p.errorf(startLine, startCol, "expected argument, got %q", r)
	}
}

// readIdentifier reads [A-Za-z_][A-Za-z0-9_-]*.
func (p *parser) readIdentifier() (string, error) {
	if err := p.skipWhitespaceAndComments(); err != nil {
		return "", err
	}
	startLine, startCol := p.line, p.col
	if p.eof() {
		return "", p.errorf(startLine, startCol, "expected identifier, got EOF")
	}
	r := p.peek()
	if !isIdentStart(r) {
		return "", p.errorf(startLine, startCol, "expected identifier, got %q", r)
	}
	var b strings.Builder
	for !p.eof() {
		r = p.peek()
		if isIdentPart(r) {
			b.WriteRune(r)
			p.advance()
			continue
		}
		break
	}
	return b.String(), nil
}

func isIdentStart(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_'
}

func isIdentPart(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9') || r == '-'
}

// readQuotedString consumes a "..." quoted-string. Sieve quoted strings
// accept "\\" and "\"" escapes per RFC 5228 §2.4.2.
func (p *parser) readQuotedString() (string, error) {
	startLine, startCol := p.line, p.col
	if p.peek() != '"' {
		return "", p.errorf(startLine, startCol, "expected '\"'")
	}
	p.advance()
	var b strings.Builder
	for !p.eof() {
		r := p.peek()
		if r == '"' {
			p.advance()
			return expandEncodedChars(b.String()), nil
		}
		if r == '\\' {
			p.advance()
			if p.eof() {
				return "", p.errorf(startLine, startCol, "unterminated escape in quoted string")
			}
			esc := p.advance()
			switch esc {
			case '"', '\\':
				b.WriteRune(esc)
			default:
				// Sieve grammar leaves non-escaped chars as literal
				// pairs: "\n" is "\n" (two chars). RFC 5228 §2.4.2.
				b.WriteRune('\\')
				b.WriteRune(esc)
			}
			continue
		}
		b.WriteRune(r)
		p.advance()
	}
	return "", p.errorf(startLine, startCol, "unterminated quoted string")
}

// readMultilineString consumes a "text:" multiline. RFC 5228 §2.4.2.1:
// starts after "text:" + newline, ends with a line of "." on its own. Dot
// stuffing is applied (leading ".." → ".").
func (p *parser) readMultilineString() (string, error) {
	startLine, startCol := p.line, p.col
	for i := 0; i < 5; i++ {
		p.advance()
	}
	// Optional CR LF or LF must follow.
	// Skip any optional spaces then a required newline.
	for p.peek() == ' ' || p.peek() == '\t' {
		p.advance()
	}
	if p.eof() {
		return "", p.errorf(startLine, startCol, "unterminated text: block")
	}
	if p.peek() == '\r' {
		p.advance()
	}
	if p.peek() != '\n' {
		return "", p.errorf(startLine, startCol, "expected newline after text:")
	}
	p.advance()
	var b strings.Builder
	for !p.eof() {
		// Read one line.
		lineStart := p.pos
		for !p.eof() && p.peek() != '\n' {
			p.advance()
		}
		line := string(p.src[lineStart:p.pos])
		// Strip trailing CR.
		if strings.HasSuffix(line, "\r") {
			line = line[:len(line)-1]
		}
		if line == "." {
			if !p.eof() {
				p.advance()
			}
			return expandEncodedChars(b.String()), nil
		}
		// Dot-stuffing.
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if !p.eof() {
			p.advance()
		}
	}
	return "", p.errorf(startLine, startCol, "unterminated text: block")
}

// readStringList parses [ "a", "b", ... ].
func (p *parser) readStringList() ([]string, error) {
	if p.peek() != '[' {
		return nil, p.errorf(p.line, p.col, "expected '['")
	}
	p.advance()
	var out []string
	for {
		if err := p.skipWhitespaceAndComments(); err != nil {
			return nil, err
		}
		if p.peek() == ']' {
			p.advance()
			return out, nil
		}
		if p.peek() != '"' {
			return nil, p.errorf(p.line, p.col, "expected '\"' in string-list")
		}
		s, err := p.readQuotedString()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		if err := p.skipWhitespaceAndComments(); err != nil {
			return nil, err
		}
		if p.peek() == ',' {
			p.advance()
			continue
		}
		if p.peek() == ']' {
			p.advance()
			return out, nil
		}
		return nil, p.errorf(p.line, p.col, "expected ',' or ']' in string-list")
	}
}

// readNumber parses a decimal number with an optional K/M/G quantity
// suffix (RFC 5228 §2.4.1). The value is normalised to bytes.
func (p *parser) readNumber() (int64, error) {
	startLine, startCol := p.line, p.col
	var n int64
	saw := false
	for !p.eof() {
		r := p.peek()
		if r < '0' || r > '9' {
			break
		}
		saw = true
		d := int64(r - '0')
		if n > (1<<62)/10 {
			return 0, p.errorf(startLine, startCol, "number overflow")
		}
		n = n*10 + d
		p.advance()
	}
	if !saw {
		return 0, p.errorf(startLine, startCol, "expected digit")
	}
	mult := int64(1)
	if !p.eof() {
		switch p.peek() {
		case 'K', 'k':
			mult = 1024
			p.advance()
		case 'M', 'm':
			mult = 1024 * 1024
			p.advance()
		case 'G', 'g':
			mult = 1024 * 1024 * 1024
			p.advance()
		}
	}
	if mult != 1 && n > (1<<62)/mult {
		return 0, p.errorf(startLine, startCol, "number overflow after quantity suffix")
	}
	return n * mult, nil
}

// expandEncodedChars implements the encoded-character extension (RFC 5228
// §2.4.2.4): "${unicode:XXXX}" and "${hex:XX}" within a string literal are
// replaced by the referenced codepoint. The function is always applied —
// scripts must still `require "encoded-character"` for the grammar to be
// semantically valid, but the parser performs the transform unconditionally
// since the replacement is a pure string transform.
func expandEncodedChars(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				return b.String()
			}
			inner := s[i+2 : i+2+end]
			if r, ok := decodeEncodedRef(inner); ok {
				b.WriteRune(r)
				i = i + 2 + end + 1
				continue
			}
			// Not an encoded-char reference; leave verbatim. Validate
			// pass will reject "${" forms for variables if the variables
			// extension is not declared.
			b.WriteString(s[i : i+2+end+1])
			i = i + 2 + end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func decodeEncodedRef(s string) (rune, bool) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "unicode:"):
		hex := s[len("unicode:"):]
		return parseHex(hex)
	case strings.HasPrefix(s, "hex:"):
		hex := s[len("hex:"):]
		return parseHex(hex)
	default:
		return 0, false
	}
}

func parseHex(s string) (rune, bool) {
	var n rune
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += c - '0'
		case c >= 'a' && c <= 'f':
			n += c - 'a' + 10
		case c >= 'A' && c <= 'F':
			n += c - 'A' + 10
		default:
			return 0, false
		}
		if n > 0x10FFFF {
			return 0, false
		}
	}
	return n, true
}
