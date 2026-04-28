package vacation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/sieve"
)

// vacationParams captures the fields that round-trip between a JMAP
// VacationResponse object and a top-level Sieve `vacation` action. We
// only persist what the JMAP datatype defines; the Sieve action's
// :handle / :addresses / :mime tags are out of band of the JMAP object
// (the operator can still author them by hand via ManageSieve).
type vacationParams struct {
	IsEnabled bool
	FromDate  *time.Time // nil when no fromDate was set
	ToDate    *time.Time // nil when no toDate was set
	Subject   string
	TextBody  string
	HTMLBody  string
}

// errComplexScript is returned by readVacation when the script does
// not match the simple "top-level vacation rule" pattern this package
// can safely round-trip.
var errComplexScript = errors.New("vacation: script structure too complex to round-trip via JMAP")

// readVacation parses script and returns the embedded vacation action's
// JMAP-visible parameters. The function recognises three shapes:
//
//  1. The empty / absent script — returns IsEnabled=false (the
//     VacationResponse defaults).
//  2. A script consisting of one `vacation` command (optionally
//     wrapped in a comment) — IsEnabled=true and the action's params
//     are returned.
//  3. A script consisting of one `if currentdate :value "ge" "date"
//     "<from>" { if currentdate :value "le" "date" "<to>" { vacation
//     ...; } }` envelope — IsEnabled=true plus FromDate / ToDate.
//
// Any other shape (vacation embedded in a tested conditional, multiple
// vacation rules, vacation alongside non-trivial actions) returns
// errComplexScript.
func readVacation(script string) (vacationParams, error) {
	if strings.TrimSpace(script) == "" {
		return vacationParams{IsEnabled: false}, nil
	}
	parsed, err := sieve.Parse([]byte(script))
	if err != nil {
		return vacationParams{}, fmt.Errorf("vacation: parse sieve script: %w", err)
	}
	// We allow the script to also contain an "implicit-keep" call (the
	// no-op `keep;` command) before / after the vacation rule, since
	// users sometimes leave one in place. Anything else triggers
	// errComplexScript.
	var (
		vacCmd   *sieve.Command
		fromDate *time.Time
		toDate   *time.Time
	)
	if len(parsed.Commands) == 1 && parsed.Commands[0].Name == "vacation" {
		c := parsed.Commands[0]
		vacCmd = &c
	}
	isDisabled := false
	if vacCmd == nil {
		// Try the `if false { vacation ...; }` disabled-but-data pattern.
		if fd, td, inner, ok := matchFalseEnvelope(parsed.Commands); ok {
			vacCmd = inner
			fromDate = fd
			toDate = td
			isDisabled = true
		}
	}
	if vacCmd == nil {
		// Try the date-window envelope.
		fd, td, inner, ok := matchDateEnvelope(parsed.Commands)
		if !ok {
			return vacationParams{}, errComplexScript
		}
		vacCmd = inner
		fromDate = fd
		toDate = td
	}
	params := vacationParams{IsEnabled: !isDisabled, FromDate: fromDate, ToDate: toDate}
	hasMime := false
	for i := 0; i < len(vacCmd.Args); i++ {
		arg := vacCmd.Args[i]
		switch arg.Kind {
		case sieve.ArgTag:
			tag := strings.ToLower(arg.Tag)
			switch tag {
			case ":subject":
				if i+1 < len(vacCmd.Args) && vacCmd.Args[i+1].Kind == sieve.ArgString {
					params.Subject = vacCmd.Args[i+1].Str
					i++
				}
			case ":mime":
				hasMime = true
			case ":days", ":seconds", ":handle", ":addresses", ":from":
				// Skip the value argument when present; these tags do
				// not surface through JMAP.
				if i+1 < len(vacCmd.Args) {
					if vacCmd.Args[i+1].Kind != sieve.ArgTag {
						i++
					}
				}
			}
		case sieve.ArgString:
			// The vacation body — if :mime was seen, extract text/html
			// and text/plain parts; otherwise treat as plain textBody.
			if hasMime {
				extractMIMEBodies(arg.Str, &params)
			} else if params.TextBody == "" {
				params.TextBody = arg.Str
			}
		}
	}
	return params, nil
}

// extractMIMEBodies parses a simple MIME body string (as emitted by
// synthesizeVacation) and populates params.TextBody and params.HTMLBody.
// This is not a full RFC 2045 parser — it handles only the fixed format
// that synthesizeVacation emits. The body may have CRLF or LF line
// endings depending on whether the Sieve text: heredoc preserved CRLF.
func extractMIMEBodies(raw string, params *vacationParams) {
	// splitHeaderBody splits at the first blank line and returns the
	// separator length (2 for \n\n, 4 for \r\n\r\n).
	splitHeaderBody := func(s string) (header, body string, ok bool) {
		if idx := strings.Index(s, "\r\n\r\n"); idx >= 0 {
			return s[:idx], s[idx+4:], true
		}
		if idx := strings.Index(s, "\n\n"); idx >= 0 {
			return s[:idx], s[idx+2:], true
		}
		return "", "", false
	}

	// Check for multipart/alternative.
	if strings.Contains(raw, "multipart/alternative") {
		const boundary = "jmap-vacation-b"
		parts := strings.Split(raw, "--"+boundary)
		for _, part := range parts {
			part = strings.Trim(part, "\r\n")
			if part == "" || part == "-" || strings.HasPrefix(part, "--") {
				continue
			}
			header, body, ok := splitHeaderBody(part)
			if !ok {
				continue
			}
			body = strings.TrimRight(body, "\r\n")
			if strings.Contains(header, "text/plain") {
				params.TextBody = body
			} else if strings.Contains(header, "text/html") {
				params.HTMLBody = body
			}
		}
		return
	}
	// Single body (text/html or other).
	header, body, ok := splitHeaderBody(raw)
	if ok {
		body = strings.TrimRight(body, "\r\n")
		if strings.Contains(header, "text/html") {
			params.HTMLBody = body
		} else {
			params.TextBody = body
		}
	} else {
		// Fallback: treat entire body as TextBody.
		params.TextBody = raw
	}
}

// matchFalseEnvelope recognises the disabled-but-data pattern emitted by
// synthesizeVacation when IsEnabled=false:
//
//	if false { vacation ...; }
//
// or with date conditions nested inside:
//
//	if false { if allof(...) { vacation ...; } }
//
// Returns the inner vacation command and (fromDate, toDate) on a match.
// The fromDate/toDate are nil when no date conditions were nested.
func matchFalseEnvelope(cmds []sieve.Command) (*time.Time, *time.Time, *sieve.Command, bool) {
	if len(cmds) != 1 || cmds[0].Name != "if" || cmds[0].Test == nil {
		return nil, nil, nil, false
	}
	if cmds[0].Test.Name != "false" {
		return nil, nil, nil, false
	}
	body := cmds[0].Block
	if len(body) == 1 && body[0].Name == "vacation" {
		vc := body[0]
		return nil, nil, &vc, true
	}
	// Try the nested date-window envelope inside the false block.
	if fd, td, vc, ok := matchDateEnvelope(body); ok {
		return fd, td, vc, true
	}
	return nil, nil, nil, false
}

// matchDateEnvelope recognises the date-window patterns that
// synthesizeVacation emits when fromDate / toDate is active.
// Returns (fromDate, toDate, vacationCmd, true) on a match.
//
// Handled patterns:
//
//  1. allof(ge, le): both from and to dates.
//  2. ge only (fromDate without toDate): single if with ge test.
//  3. le only (toDate without fromDate): single if with le test.
//  4. ge + nested le if: nested-if form (legacy).
func matchDateEnvelope(cmds []sieve.Command) (*time.Time, *time.Time, *sieve.Command, bool) {
	if len(cmds) != 1 || cmds[0].Name != "if" || cmds[0].Test == nil {
		return nil, nil, nil, false
	}
	outer := cmds[0]
	body := outer.Block

	var from, to *time.Time

	switch {
	case outer.Test.Name == "allof" && len(outer.Test.Children) == 2:
		// allof(ge, le) — both dates.
		f, ok1 := matchCurrentDate(&outer.Test.Children[0], "ge")
		t, ok2 := matchCurrentDate(&outer.Test.Children[1], "le")
		if !ok1 || !ok2 {
			return nil, nil, nil, false
		}
		from, to = f, t

	case outer.Test.Name == "currentdate":
		// Single currentdate test — could be ge (from-only), le (to-only),
		// or the nested-if form (ge + inner if le).
		if f, ok := matchCurrentDate(outer.Test, "ge"); ok {
			from = f
			// Check for nested if le (legacy nested form).
			if len(body) == 1 && body[0].Name == "if" && body[0].Test != nil {
				if t, ok := matchCurrentDate(body[0].Test, "le"); ok {
					to = t
					body = body[0].Block
				}
			}
		} else if t, ok := matchCurrentDate(outer.Test, "le"); ok {
			to = t
		} else {
			return nil, nil, nil, false
		}

	default:
		return nil, nil, nil, false
	}

	// Body must be exactly one vacation command.
	if len(body) != 1 || body[0].Name != "vacation" {
		return nil, nil, nil, false
	}
	vc := body[0]
	return from, to, &vc, true
}

// matchCurrentDate inspects a Test that should be of the form
//
//	currentdate :value "<op>" "date" "<RFC 3339 date>"
//
// and returns the parsed date. op selects which comparator to expect.
func matchCurrentDate(t *sieve.Test, op string) (*time.Time, bool) {
	if t == nil || t.Name != "currentdate" {
		return nil, false
	}
	var (
		sawValueOp bool
		sawPart    bool
		dateStr    string
	)
	for i := 0; i < len(t.Args); i++ {
		a := t.Args[i]
		switch a.Kind {
		case sieve.ArgTag:
			if strings.ToLower(a.Tag) == ":value" {
				if i+1 < len(t.Args) && t.Args[i+1].Kind == sieve.ArgString &&
					strings.EqualFold(t.Args[i+1].Str, op) {
					sawValueOp = true
					i++
				}
			}
		case sieve.ArgString:
			if !sawPart {
				if strings.EqualFold(a.Str, "date") {
					sawPart = true
				}
				continue
			}
			if dateStr == "" {
				dateStr = a.Str
			}
		}
	}
	if !sawValueOp || !sawPart || dateStr == "" {
		return nil, false
	}
	tt, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		// Try the date-only form; RFC 8621 §9.1.1 fromDate/toDate carry
		// UTC datetime values, but a hand-written script may omit the
		// time component.
		if t2, err2 := time.Parse("2006-01-02", dateStr); err2 == nil {
			tt = t2
		} else {
			return nil, false
		}
	}
	return &tt, true
}

// synthesizeVacation renders a vacation params object into the
// canonical Sieve script that readVacation can round-trip.
//
// When disabled AND there is no data worth preserving, returns the
// empty string (no script).
//
// When disabled but there is data (dates, subject, or body), wraps
// the vacation command in `if false { ... }` so the data survives a
// round-trip without executing. readVacation recognises this pattern.
//
// When both textBody and htmlBody are set, the body is encoded as a
// MIME multipart/alternative message using the :mime flag so that both
// bodies survive a round-trip (RFC 5230 §4 :mime tag). When only
// htmlBody is set, it is stored with :mime as text/html.
func synthesizeVacation(p vacationParams) string {
	hasData := p.FromDate != nil || p.ToDate != nil || p.Subject != "" ||
		p.TextBody != "" || p.HTMLBody != ""
	if !p.IsEnabled && !hasData {
		return ""
	}
	var b strings.Builder
	b.WriteString("require [\"vacation\"")
	if p.FromDate != nil || p.ToDate != nil {
		b.WriteString(", \"date\", \"relational\"")
	}
	b.WriteString("];\n")
	indent := ""
	// closingBraces tracks how many `}` closers to emit at the end.
	closingBraces := 0
	if !p.IsEnabled {
		// When disabled, wrap in `if false { ... }` to preserve data without
		// executing. readVacation treats the false-test pattern as IsEnabled=false.
		// Any date conditions are emitted inside the false block so they
		// survive the round-trip.
		b.WriteString("if false {\n")
		closingBraces++
		indent = "  "
		if p.FromDate != nil && p.ToDate != nil {
			b.WriteString(indent + `if allof(currentdate :value "ge" "date" "`)
			b.WriteString(p.FromDate.UTC().Format("2006-01-02"))
			b.WriteString(`", currentdate :value "le" "date" "`)
			b.WriteString(p.ToDate.UTC().Format("2006-01-02"))
			b.WriteString("\") {\n")
			closingBraces++
			indent = "    "
		} else if p.FromDate != nil {
			b.WriteString(indent + `if currentdate :value "ge" "date" "`)
			b.WriteString(p.FromDate.UTC().Format("2006-01-02"))
			b.WriteString("\" {\n")
			closingBraces++
			indent = "    "
		} else if p.ToDate != nil {
			b.WriteString(indent + `if currentdate :value "le" "date" "`)
			b.WriteString(p.ToDate.UTC().Format("2006-01-02"))
			b.WriteString("\" {\n")
			closingBraces++
			indent = "    "
		}
	} else if p.FromDate != nil && p.ToDate != nil {
		b.WriteString(`if allof(currentdate :value "ge" "date" "`)
		b.WriteString(p.FromDate.UTC().Format("2006-01-02"))
		b.WriteString(`", currentdate :value "le" "date" "`)
		b.WriteString(p.ToDate.UTC().Format("2006-01-02"))
		b.WriteString("\") {\n")
		closingBraces++
		indent = "  "
	} else if p.FromDate != nil {
		b.WriteString(`if currentdate :value "ge" "date" "`)
		b.WriteString(p.FromDate.UTC().Format("2006-01-02"))
		b.WriteString("\" {\n")
		closingBraces++
		indent = "  "
	} else if p.ToDate != nil {
		b.WriteString(`if currentdate :value "le" "date" "`)
		b.WriteString(p.ToDate.UTC().Format("2006-01-02"))
		b.WriteString("\" {\n")
		closingBraces++
		indent = "  "
	}
	b.WriteString(indent)
	b.WriteString("vacation")
	if p.Subject != "" {
		b.WriteString(" :subject ")
		writeQuoted(&b, p.Subject)
	}

	switch {
	case p.TextBody != "" && p.HTMLBody != "":
		// Both bodies: emit a MIME multipart/alternative using :mime.
		// We use a fixed boundary "b" for simplicity; the Sieve action
		// uses it verbatim as the MIME body.
		b.WriteString(" :mime")
		mimeBody := buildMIMEAlternative(p.TextBody, p.HTMLBody)
		b.WriteString(" ")
		writeQuoted(&b, mimeBody)
	case p.HTMLBody != "":
		// HTML only: emit as text/html with :mime tag.
		b.WriteString(" :mime")
		mimeBody := "Content-Type: text/html; charset=utf-8\r\n\r\n" + p.HTMLBody
		b.WriteString(" ")
		writeQuoted(&b, mimeBody)
	default:
		// Text only (or empty — use default).
		body := p.TextBody
		if body == "" {
			body = "I am away."
		}
		b.WriteString(" ")
		writeQuoted(&b, body)
	}

	b.WriteString(";\n")
	// Close any open if blocks, innermost first. Each closing brace is
	// at one level less indentation than the block it closes.
	for closingBraces > 0 {
		closeIndent := ""
		if len(indent) >= 2 {
			closeIndent = indent[:len(indent)-2]
		}
		b.WriteString(closeIndent + "}\n")
		indent = closeIndent
		closingBraces--
	}
	return b.String()
}

// buildMIMEAlternative builds a minimal multipart/alternative MIME body
// containing a text/plain and a text/html part. The boundary is fixed
// at "jmap-vacation-b" for determinism.
func buildMIMEAlternative(text, html string) string {
	const boundary = "jmap-vacation-b"
	var b strings.Builder
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("MIME-Version: 1.0\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(text)
	b.WriteString("\r\n--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(html)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.String()
}

// writeQuoted writes a Sieve quoted-string to b. The Sieve grammar
// accepts only `\"` and `\\` as escapes; everything else is literal.
// Multi-line bodies use the `text:` heredoc form (RFC 5228 §2.4.2.1).
// The heredoc terminator is a lone "." on its own line followed by a
// newline; the caller must NOT append a newline before the terminator.
func writeQuoted(b *strings.Builder, s string) {
	if strings.ContainsAny(s, "\r\n") {
		b.WriteString("text:\n")
		// Apply RFC 5228 §2.4.2.1 dot-stuffing.
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, ".") {
				b.WriteByte('.')
			}
			b.WriteString(strings.TrimRight(line, "\r"))
			b.WriteByte('\n')
		}
		b.WriteString(".\n") // terminator must be followed by newline
		return
	}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
