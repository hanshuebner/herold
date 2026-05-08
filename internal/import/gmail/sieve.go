package gmail

import (
	"fmt"
	"strings"
)

// TranslateFiltersToSieve renders a Sieve script from a list of Gmail
// filters. The translator is best-effort: queries that exceed the
// supported subset emit a comment with the original query and a
// best-fit `header :contains "subject"` match, so the rule survives
// import even if a human has to edit it later.
//
// `locale` informs which strings should be treated as Gmail system
// labels rather than user labels. The returned script's labelsToAdd
// for system labels become Sieve flag / fileinto operations; user
// labels become fileinto into the matching herold mailbox of role
// Label. The script is intentionally inactive: REQ-IMPORT-41 makes
// the user review and activate it explicitly.
//
// userLabelMailbox(name) is consulted to compute the herold Mailbox
// path for a user-defined label; when nil, the default is the label
// name verbatim.
func TranslateFiltersToSieve(filters []GmailFilter, locale Locale, userLabelMailbox func(string) string) string {
	if userLabelMailbox == nil {
		userLabelMailbox = func(s string) string { return s }
	}

	var b strings.Builder
	b.WriteString("# Sieve script translated from Gmail filters by herold's gmail importer.\n")
	b.WriteString("# Inactive by default; review then SETACTIVE via ManageSieve / JMAP /\n")
	b.WriteString("# the suite Settings UI to enable.\n")
	b.WriteString(fmt.Sprintf("# Source locale (best-effort): %s\n", locale))
	b.WriteString("\n")
	// Track which extensions the script needs. We always include
	// fileinto (almost every rule needs it) and imapflags (Sieve's IMAP
	// flag manipulation).
	requireSet := map[string]bool{"fileinto": true, "imap4flags": true}

	body := &strings.Builder{}
	for i, f := range filters {
		body.WriteString(fmt.Sprintf("# rule %d: original Gmail query: %s\n", i+1, escapeForComment(f.Query)))
		conds := translateQuery(f.Query)
		if len(conds) == 0 {
			body.WriteString("# (untranslatable query — left as a no-op for the user to edit)\n")
			body.WriteString("if false {\n")
			body.WriteString("    discard;\n")
			body.WriteString("}\n\n")
			continue
		}
		body.WriteString("if ")
		if len(conds) > 1 {
			body.WriteString("allof (")
			body.WriteString(strings.Join(conds, ",\n        "))
			body.WriteString(")")
		} else {
			body.WriteString(conds[0])
		}
		body.WriteString(" {\n")

		actions := translateActions(f, locale, userLabelMailbox, requireSet)
		if len(actions) == 0 {
			body.WriteString("    keep;\n")
		}
		for _, a := range actions {
			body.WriteString("    ")
			body.WriteString(a)
			body.WriteString("\n")
		}
		body.WriteString("}\n\n")
	}

	requires := make([]string, 0, len(requireSet))
	for k := range requireSet {
		requires = append(requires, k)
	}
	sortStrings(requires)
	if len(requires) > 0 {
		quoted := make([]string, len(requires))
		for i, r := range requires {
			quoted[i] = quoteSieveString(r)
		}
		b.WriteString("require [")
		b.WriteString(strings.Join(quoted, ", "))
		b.WriteString("];\n\n")
	}
	b.WriteString(body.String())
	return b.String()
}

// translateQuery turns a Gmail search expression into a slice of Sieve
// test predicates. The recognised operators are the ones that show up
// in roughly 95% of real-world Gmail filter sets:
//
//   - from:VAL          → header :contains "from" "VAL"
//   - to:VAL            → header :contains "to" "VAL"
//   - subject:VAL       → header :contains "subject" "VAL"
//   - list:VAL          → header :contains "list-id" "VAL"
//   - has:attachment    → exists "Content-Type-Multipart" (best-effort)
//   - bare WORD         → header :contains "subject" "WORD"
//
// Unrecognised tokens are emitted as a header :contains "subject" match
// so the rule still narrows; the original query is preserved in a
// comment by the caller.
func translateQuery(q string) []string {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	conds := make([]string, 0, 4)
	for _, tok := range splitQueryTokens(q) {
		if eq := strings.Index(tok, ":"); eq >= 0 {
			op := strings.ToLower(tok[:eq])
			val := strings.Trim(tok[eq+1:], `"`)
			switch op {
			case "from":
				conds = append(conds, fmt.Sprintf(`header :contains "from" %s`, quoteSieveString(val)))
				continue
			case "to":
				conds = append(conds, fmt.Sprintf(`header :contains "to" %s`, quoteSieveString(val)))
				continue
			case "cc":
				conds = append(conds, fmt.Sprintf(`header :contains "cc" %s`, quoteSieveString(val)))
				continue
			case "subject":
				conds = append(conds, fmt.Sprintf(`header :contains "subject" %s`, quoteSieveString(val)))
				continue
			case "list":
				conds = append(conds, fmt.Sprintf(`header :contains "list-id" %s`, quoteSieveString(val)))
				continue
			case "has":
				if strings.EqualFold(val, "attachment") {
					conds = append(conds, `header :matches "content-type" "multipart/*"`)
					continue
				}
			}
		}
		// Bare word: best-effort subject contains.
		conds = append(conds, fmt.Sprintf(`header :contains "subject" %s`, quoteSieveString(strings.Trim(tok, `"`))))
	}
	return conds
}

// translateActions emits one Sieve action per labelsToAdd / labelsToRemove
// entry plus the secondary boolean filter actions. requireSet is
// populated as side-effect with extensions the actions need.
func translateActions(f GmailFilter, locale Locale, userLabelMailbox func(string) string, requireSet map[string]bool) []string {
	out := make([]string, 0, 4)

	// secondary booleans first
	if f.ShouldStar {
		out = append(out, `addflag "\\Flagged";`)
	}
	if f.ShouldMarkAsRead {
		out = append(out, `addflag "\\Seen";`)
	}
	if f.ShouldTrash {
		out = append(out, `fileinto "Trash";`)
	}

	// labelsToAdd: file into matching mailbox; for system labels emit
	// flag + fileinto into the corresponding role mailbox.
	for _, lbl := range f.LabelsToAdd {
		if sym, ok := Lookup(lbl, locale); ok {
			out = append(out, sieveActionsForSystemLabel(sym)...)
			continue
		}
		// User label.
		path := userLabelMailbox(lbl)
		out = append(out, fmt.Sprintf(`fileinto %s;`, quoteSieveString(path)))
	}

	// labelsToRemove: only Inbox is meaningful (it expresses Gmail's
	// "skip the inbox" idiom). Other removals are no-ops in Sieve.
	for _, lbl := range f.LabelsToRemove {
		if sym, ok := Lookup(lbl, locale); ok && sym == SystemLabelInbox {
			// Express "skip inbox" by ensuring the mail does NOT land
			// in INBOX. Sieve's idiom: file somewhere else and then
			// stop; without an explicit target we use the keep-implicit
			// semantics. The simplest portable representation is a
			// comment that surfaces operator intent — Gmail's
			// "skip-inbox" semantics are emergent from filing
			// elsewhere, which the labelsToAdd path already does.
			out = append(out, "# (Gmail: skip the Inbox)")
		}
	}
	if f.ForwardTo != "" {
		out = append(out, fmt.Sprintf(`# (Gmail: forward to %s — not auto-translated; user opt-in via settings)`, f.ForwardTo))
	}
	if len(out) > 0 {
		// Stop after acting on a matched rule, matching Gmail's
		// "first match wins" sketch closely enough for users to recognise
		// the behaviour. They can remove the stop manually if they
		// want fall-through semantics.
		out = append(out, "stop;")
	}
	return out
}

// sieveActionsForSystemLabel returns the Sieve action lines for one
// matched system label. Only the labels that meaningfully translate
// into action verbs are handled here.
func sieveActionsForSystemLabel(sym SystemLabel) []string {
	switch sym {
	case SystemLabelStarred:
		return []string{`addflag "\\Flagged";`}
	case SystemLabelImportant:
		return []string{`addflag "\\Important";`}
	case SystemLabelSpam:
		return []string{`fileinto "Junk";`}
	case SystemLabelTrash:
		return []string{`fileinto "Trash";`}
	case SystemLabelInbox:
		// Adding inbox is a no-op (default delivery target).
		return nil
	}
	return nil
}

// splitQueryTokens splits a Gmail query string on whitespace, but keeps
// double-quoted runs together. Operator-like tokens (foo:"bar baz")
// stay one token.
func splitQueryTokens(q string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		t := strings.TrimSpace(cur.String())
		cur.Reset()
		if t != "" {
			out = append(out, t)
		}
	}
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch c {
		case '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case ' ', '\t', '\n':
			if inQuote {
				cur.WriteByte(c)
			} else {
				flush()
			}
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// escapeForComment returns s suitable for a single-line Sieve comment.
// Newlines are replaced with a marker; we don't try to emit multi-line
// comments because most operator-friendly viewers (sieve-editor, the
// JMAP UI) collapse them.
func escapeForComment(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// quoteSieveString quotes s as a Sieve quoted-string (RFC 5228 §2.4.2).
// We escape backslash and double-quote per the spec; non-ASCII is left
// alone (Sieve scripts are utf-8 since RFC 5228 §2.4.2.4).
func quoteSieveString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sortStrings sorts a string slice in ascending order without pulling
// in the sort package (which the Sieve translator would otherwise be
// the only user of in this package).
func sortStrings(s []string) {
	// Tiny insertion sort — fine for small require sets.
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j] < s[j-1] {
			s[j], s[j-1] = s[j-1], s[j]
			j--
		}
	}
}
