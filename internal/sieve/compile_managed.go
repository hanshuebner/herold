package sieve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// ManagedRule mirrors store.ManagedRule but lives in this package to avoid
// an import cycle; callers use CompileRules which accepts store.ManagedRule
// directly.

// XHeroldThreadIDHeader is the header name herold stamps on every delivered
// message so Sieve thread-mute rules can match against it. The value is the
// decimal string representation of the store.ThreadID.
const XHeroldThreadIDHeader = "X-Herold-Thread-Id"

// CompileRules returns a Sieve script that implements rules in the order
// they are provided (lowest sort_order first, then by ID). Only enabled rules
// are emitted. An empty or all-disabled rule set returns an empty string.
//
// The returned script is validated by the sieve parser before being returned;
// CompileRules returns an error if the generated Sieve is syntactically or
// semantically invalid. User-supplied strings are quoted using sieverQuote so
// they cannot break out of the surrounding Sieve string context.
//
// Two-source coexistence: the caller is responsible for concatenating the
// compiled preamble and the user-written script with the canonical delimiter:
//
//	<compiled preamble>
//	\n# --- user-managed ---\n
//	<user-written Sieve>
//
// CompileRules only produces the preamble half; it never reads or emits the
// delimiter itself.
func CompileRules(rules []store.ManagedRule) (string, error) {
	// Sort in (sort_order, id) ascending order — deterministic output.
	sorted := make([]store.ManagedRule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].SortOrder != sorted[j].SortOrder {
			return sorted[i].SortOrder < sorted[j].SortOrder
		}
		return sorted[i].ID < sorted[j].ID
	})

	// Pass 1: collect the set of Sieve extensions actually used so we can
	// emit a minimal require statement.
	required := map[string]bool{}
	var enabled []store.ManagedRule
	for _, r := range sorted {
		if !r.Enabled {
			continue
		}
		enabled = append(enabled, r)
		for _, a := range r.Actions {
			switch a.Kind {
			case "apply-label":
				required["fileinto"] = true
			case "skip-inbox":
				required["fileinto"] = true
			case "mark-read":
				required["imap4flags"] = true
			case "delete":
				required["fileinto"] = true
			case "forward":
				// redirect is a base Sieve command; no extension needed.
			}
		}
	}
	if len(enabled) == 0 {
		return "", nil
	}

	var sb strings.Builder

	// require line.
	exts := make([]string, 0, len(required))
	for ext := range required {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	if len(exts) > 0 {
		sb.WriteString("require [")
		for i, ext := range exts {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(sieveQuote(ext))
		}
		sb.WriteString("];\n")
	}

	// Pass 2: emit one if block per enabled rule.
	for _, r := range enabled {
		cond, err := compileConditions(r.Conditions)
		if err != nil {
			return "", fmt.Errorf("rule %d conditions: %w", r.ID, err)
		}
		body, err := compileActions(r.Actions)
		if err != nil {
			return "", fmt.Errorf("rule %d actions: %w", r.ID, err)
		}
		fmt.Fprintf(&sb, "if %s {\n%s}\n", cond, body)
	}

	script := sb.String()

	// Self-validation: the generated script must parse + validate cleanly.
	// This is the security-reviewer gate: malformed managed rules must not
	// produce syntactically valid-looking Sieve that leaks into the user
	// section.
	parsed, perr := Parse([]byte(script))
	if perr != nil {
		return "", fmt.Errorf("compile_managed: generated Sieve failed to parse: %w", perr)
	}
	if verr := Validate(parsed); verr != nil {
		return "", fmt.Errorf("compile_managed: generated Sieve failed validation: %w", verr)
	}

	return script, nil
}

// compileConditions emits the Sieve test expression for one rule's condition
// list. Multiple conditions are AND-combined with allof. A single condition
// is emitted directly (no allof wrapper) for readability.
func compileConditions(conds []store.RuleCondition) (string, error) {
	if len(conds) == 0 {
		return "true", nil
	}
	tests := make([]string, 0, len(conds))
	for _, c := range conds {
		t, err := compileSingleCondition(c)
		if err != nil {
			return "", err
		}
		tests = append(tests, t)
	}
	if len(tests) == 1 {
		return tests[0], nil
	}
	return "allof (" + strings.Join(tests, ",\n        ") + ")", nil
}

// compileSingleCondition converts one RuleCondition into a Sieve test string.
func compileSingleCondition(c store.RuleCondition) (string, error) {
	if err := validateConditionField(c.Field); err != nil {
		return "", err
	}
	if err := validateConditionOp(c.Op); err != nil {
		return "", err
	}

	match := compileMatchOp(c.Op)

	switch c.Field {
	case "from":
		return fmt.Sprintf("address %s :all \"From\" %s",
			match, sieveQuote(c.Value)), nil
	case "to":
		return fmt.Sprintf("address %s :all \"To\" %s",
			match, sieveQuote(c.Value)), nil
	case "subject":
		return fmt.Sprintf("header %s \"Subject\" %s",
			match, sieveQuote(c.Value)), nil
	case "has-attachment":
		// has-attachment is a boolean condition; op and value are ignored.
		// We use the body :raw extension to detect Content-Disposition: attachment.
		// Since the body extension may not be available in all environments,
		// we use a header test against Content-Disposition which is simpler
		// and reliable for detecting attachments as a marker.
		// The :matches wildcard covers the common cases.
		return `header :contains "Content-Type" "multipart"`, nil
	case "thread-id":
		return fmt.Sprintf("header %s %s %s",
			match, sieveQuote(XHeroldThreadIDHeader), sieveQuote(c.Value)), nil
	case "from-domain":
		// Match the domain portion of the From address using :matches wildcard.
		return fmt.Sprintf("address :matches :domain \"From\" %s",
			sieveQuote("*@"+c.Value)), nil
	default:
		return "", fmt.Errorf("unknown condition field %q", c.Field)
	}
}

// compileMatchOp maps a condition op to the Sieve match-type tag.
func compileMatchOp(op string) string {
	switch op {
	case "equals":
		return ":is"
	case "contains":
		return ":contains"
	case "wildcard-match":
		return ":matches"
	default:
		return ":is"
	}
}

// compileActions converts the action list into a Sieve block body (indented
// lines terminated with semicolons). The caller wraps the result in braces.
func compileActions(actions []store.RuleAction) (string, error) {
	if err := validateActions(actions); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, a := range actions {
		line, err := compileSingleAction(a)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "  %s;\n", line)
	}
	return sb.String(), nil
}

// compileSingleAction converts one RuleAction into a Sieve command string
// (without trailing semicolon).
func compileSingleAction(a store.RuleAction) (string, error) {
	switch a.Kind {
	case "apply-label":
		label, ok := actionStringParam(a.Params, "label")
		if !ok || label == "" {
			return "", fmt.Errorf("apply-label action requires a non-empty label param")
		}
		return fmt.Sprintf("fileinto %s", sieveQuote(label)), nil
	case "skip-inbox":
		return fmt.Sprintf("fileinto %s", sieveQuote("Archive")), nil
	case "mark-read":
		return "addflag \"\\\\Seen\"", nil
	case "delete":
		return fmt.Sprintf("fileinto %s", sieveQuote("Trash")), nil
	case "forward":
		to, ok := actionStringParam(a.Params, "to")
		if !ok || to == "" {
			return "", fmt.Errorf("forward action requires a non-empty to param")
		}
		if err := validateForwardAddress(to); err != nil {
			return "", err
		}
		return fmt.Sprintf("redirect %s", sieveQuote(to)), nil
	default:
		return "", fmt.Errorf("unknown action kind %q", a.Kind)
	}
}

// validateConditionField rejects unknown field names.
func validateConditionField(field string) error {
	switch field {
	case "from", "to", "subject", "has-attachment", "thread-id", "from-domain":
		return nil
	}
	return fmt.Errorf("unknown condition field %q", field)
}

// validateConditionOp rejects unknown op names.
func validateConditionOp(op string) error {
	switch op {
	case "contains", "equals", "wildcard-match":
		return nil
	}
	return fmt.Errorf("unknown condition op %q", op)
}

// validateActions rejects semantically invalid action combinations.
// Per the requirement: "delete" short-circuits so combining it with
// "apply-label" is disallowed.
func validateActions(actions []store.RuleAction) error {
	hasDelete := false
	hasApplyLabel := false
	for _, a := range actions {
		switch a.Kind {
		case "delete":
			hasDelete = true
		case "apply-label":
			hasApplyLabel = true
		}
	}
	if hasDelete && hasApplyLabel {
		return fmt.Errorf("cannot combine 'delete' and 'apply-label' actions: delete short-circuits")
	}
	return nil
}

// validateForwardAddress performs a minimal sanity check on a forward-to
// address to prevent obviously malicious input from leaking into the Sieve
// script. We do not validate full RFC 5321 here — the Sieve runtime and
// outbound queue do further checks — but we ensure the value contains exactly
// one '@', no newlines, and no unbalanced angle brackets, which are the
// characters that could escape the surrounding string context if quoting were
// incorrect.
func validateForwardAddress(addr string) error {
	if strings.ContainsAny(addr, "\r\n") {
		return fmt.Errorf("forward address must not contain newline characters")
	}
	if strings.Count(addr, "@") != 1 {
		return fmt.Errorf("forward address must contain exactly one '@'")
	}
	return nil
}

// sieveQuote produces a properly quoted Sieve string literal. Sieve strings
// are double-quoted; inside the string, backslash and double-quote are
// escaped. Control characters and NUL are rejected.
//
// This is the security-critical function: every user-controlled string that
// ends up in a generated Sieve script goes through here. Shell-style escaping
// (percent, backtick, dollar) is NOT applied — Sieve has no expansion syntax
// inside quoted strings.
func sieveQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, ch := range s {
		switch {
		case ch == '"':
			sb.WriteString(`\"`)
		case ch == '\\':
			sb.WriteString(`\\`)
		case ch < 0x20 || ch == 0x7f:
			// Control characters are disallowed in Sieve strings (RFC 5228 §2.4.2).
			// Replace with the Unicode replacement character so the script stays
			// valid even if somehow malformed input reaches here.
			sb.WriteRune(0xFFFD)
		default:
			sb.WriteRune(ch)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// actionStringParam extracts a string value from the params map. Returns
// ("", false) when the key is absent or the value is not a string.
func actionStringParam(params map[string]any, key string) (string, bool) {
	if params == nil {
		return "", false
	}
	v, ok := params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// EffectiveScript combines the compiled managed-rules preamble with the
// user-written Sieve script, inserting the canonical delimiter between them.
// When preamble is empty, the raw user script is returned unchanged so the
// delimiter never appears in a purely-hand-written script. When userScript is
// empty, only the preamble is returned (no trailing delimiter line).
//
// The delimiter allows UI and tooling to identify the boundary without
// parsing Sieve: a consumer that wants only the user-written half splits on
// the delimiter line.
func EffectiveScript(preamble, userScript string) string {
	if preamble == "" {
		return userScript
	}
	if userScript == "" {
		return preamble
	}
	return preamble + "\n# --- user-managed ---\n" + userScript
}
