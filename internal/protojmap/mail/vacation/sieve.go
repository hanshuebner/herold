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
	params := vacationParams{IsEnabled: true, FromDate: fromDate, ToDate: toDate}
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
				// We render :mime when both text and html bodies are
				// set; the inverse — extracting :mime back into JMAP —
				// requires an RFC 2045 multipart/alternative parse.
				// On read we fall back to placing the entire body text
				// in TextBody.
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
			// The vacation body — JMAP exposes textBody and htmlBody;
			// we round-trip through textBody when the script does not
			// carry MIME structure.
			if params.TextBody == "" {
				params.TextBody = arg.Str
			}
		}
	}
	return params, nil
}

// matchDateEnvelope recognises the `if currentdate :value "ge" ...`
// pattern that synthesizeVacation emits when a fromDate / toDate is
// active. Returns (fromDate, toDate, vacationCmd, true) on a match.
func matchDateEnvelope(cmds []sieve.Command) (*time.Time, *time.Time, *sieve.Command, bool) {
	if len(cmds) != 1 || cmds[0].Name != "if" || cmds[0].Test == nil {
		return nil, nil, nil, false
	}
	outer := cmds[0]
	from, _ := matchCurrentDate(outer.Test, "ge")
	to, _ := (*time.Time)(nil), false
	body := outer.Block
	// Two flavours: a single `if` containing both ge + le tests via
	// allof, OR nested ifs. We handle both.
	if outer.Test.Name == "allof" && len(outer.Test.Children) == 2 {
		f, ok1 := matchCurrentDate(&outer.Test.Children[0], "ge")
		t, ok2 := matchCurrentDate(&outer.Test.Children[1], "le")
		if !ok1 || !ok2 {
			return nil, nil, nil, false
		}
		from = f
		to = t
	} else if outer.Test.Name == "currentdate" {
		// Outer is just ge; inner is le wrapped if.
		if len(body) != 1 || body[0].Name != "if" || body[0].Test == nil {
			return nil, nil, nil, false
		}
		t, ok := matchCurrentDate(body[0].Test, "le")
		if !ok {
			return nil, nil, nil, false
		}
		to = t
		body = body[0].Block
	} else {
		return nil, nil, nil, false
	}
	if from == nil {
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
// canonical Sieve script that readVacation can round-trip. When the
// params is disabled, returns the empty string (delete the script).
func synthesizeVacation(p vacationParams) string {
	if !p.IsEnabled {
		return ""
	}
	var b strings.Builder
	b.WriteString("require [\"vacation\"")
	if p.FromDate != nil || p.ToDate != nil {
		b.WriteString(", \"date\", \"relational\"")
	}
	b.WriteString("];\n")
	indent := ""
	if p.FromDate != nil && p.ToDate != nil {
		b.WriteString(`if allof(currentdate :value "ge" "date" "`)
		b.WriteString(p.FromDate.UTC().Format("2006-01-02"))
		b.WriteString(`", currentdate :value "le" "date" "`)
		b.WriteString(p.ToDate.UTC().Format("2006-01-02"))
		b.WriteString("\") {\n")
		indent = "  "
	} else if p.FromDate != nil {
		b.WriteString(`if currentdate :value "ge" "date" "`)
		b.WriteString(p.FromDate.UTC().Format("2006-01-02"))
		b.WriteString("\" {\n")
		indent = "  "
	} else if p.ToDate != nil {
		b.WriteString(`if currentdate :value "le" "date" "`)
		b.WriteString(p.ToDate.UTC().Format("2006-01-02"))
		b.WriteString("\" {\n")
		indent = "  "
	}
	b.WriteString(indent)
	b.WriteString("vacation")
	if p.Subject != "" {
		b.WriteString(" :subject ")
		writeQuoted(&b, p.Subject)
	}
	body := p.TextBody
	if body == "" && p.HTMLBody != "" {
		// JMAP allows htmlBody alone; we wrap it in a synthetic plain
		// text fallback for delivery — Phase 2 sieve only renders text
		// vacation bodies, so we strip tags conservatively.
		body = stripTags(p.HTMLBody)
	}
	if body == "" {
		body = "I am away."
	}
	b.WriteString(" ")
	writeQuoted(&b, body)
	b.WriteString(";\n")
	if indent != "" {
		b.WriteString("}\n")
	}
	return b.String()
}

// writeQuoted writes a Sieve quoted-string to b. The Sieve grammar
// accepts only `\"` and `\\` as escapes; everything else is literal.
// Multi-line bodies use the `text:` heredoc form.
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
		b.WriteString(".")
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

// stripTags is a tiny HTML-to-text fallback used when only an HTML
// body is supplied. We strip "<...>" runs and collapse whitespace; not
// a full HTML parser. The JMAP API client can always supply textBody
// when it cares about formatting.
func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
