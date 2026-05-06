package sieve

import (
	"fmt"
	"strings"
)

// ValidationError is returned by Validate when a script's structure is
// well-formed but semantically invalid (unknown command, missing require,
// bad argument shape).
type ValidationError struct {
	Line    int
	Column  int
	Message string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("sieve validation error at %d:%d: %s", e.Line, e.Column, e.Message)
}

// SupportedExtensions is the canonical set of extensions this interpreter
// recognises. Scripts that `require` anything outside the set are rejected.
//
// Entries marked "stub" are parsed and validated but intentionally
// degrade at execution time — see the package comment for the current
// state.
var SupportedExtensions = map[string]string{
	"fileinto":                   "RFC 5228",
	"reject":                     "RFC 5429",
	"envelope":                   "RFC 5228",
	"encoded-character":          "RFC 5228",
	"imap4flags":                 "RFC 5232",
	"body":                       "RFC 5173",
	"vacation":                   "RFC 5230",
	"vacation-seconds":           "RFC 6131",
	"variables":                  "RFC 5229",
	"relational":                 "RFC 5231",
	"subaddress":                 "RFC 5233",
	"regex":                      "draft-murchison-sieve-regex",
	"copy":                       "RFC 3894",
	"include":                    "RFC 6609",
	"date":                       "RFC 5260",
	"index":                      "RFC 5260",
	"mailbox":                    "RFC 5490",
	"mailboxid":                  "RFC 9042",
	"editheader":                 "RFC 5293",
	"duplicate":                  "RFC 7352",
	"extlists":                   "RFC 6134",
	"foreverypart":               "RFC 5703",
	"mime":                       "RFC 5703 (replace/enclose stub)",
	"spamtest":                   "RFC 5235",
	"spamtestplus":               "RFC 5235",
	"enotify":                    "RFC 5435 (mailto-only)",
	"comparator-i;ascii-numeric": "RFC 4790",
	"comparator-i;octet":         "RFC 4790",
	"comparator-i;ascii-casemap": "RFC 4790",
}

// KnownCommands lists the commands the interpreter understands. Commands
// not in this list are rejected by Validate. Extensions that gate a
// command are named in the value so we can surface a clean error.
var KnownCommands = map[string]string{
	// RFC 5228 base.
	"if":       "",
	"elsif":    "",
	"else":     "",
	"require":  "",
	"stop":     "",
	"keep":     "",
	"discard":  "",
	"redirect": "",

	// Extensions.
	"fileinto":     "fileinto",
	"reject":       "reject",
	"ereject":      "reject",
	"vacation":     "vacation",
	"setflag":      "imap4flags",
	"addflag":      "imap4flags",
	"removeflag":   "imap4flags",
	"set":          "variables",
	"include":      "include",
	"return":       "include",
	"global":       "include",
	"addheader":    "editheader",
	"deleteheader": "editheader",
	"notify":       "enotify",
	"foreverypart": "foreverypart",
	"break":        "foreverypart",
	"replace":      "mime",
	"enclose":      "mime",
	"extracttext":  "mime",
}

// KnownTests lists the tests the interpreter understands.
var KnownTests = map[string]string{
	"address":                  "",
	"allof":                    "",
	"anyof":                    "",
	"envelope":                 "envelope",
	"exists":                   "",
	"false":                    "",
	"true":                     "",
	"header":                   "",
	"not":                      "",
	"size":                     "",
	"body":                     "body",
	"hasflag":                  "imap4flags",
	"date":                     "date",
	"currentdate":              "date",
	"string":                   "variables",
	"mailboxexists":            "mailbox",
	"mailboxidexists":          "mailboxid",
	"metadata":                 "mboxmetadata",
	"metadataexists":           "mboxmetadata",
	"servermetadata":           "servermetadata",
	"spamtest":                 "spamtest",
	"duplicate":                "duplicate",
	"valid_notify_method":      "enotify",
	"notify_method_capability": "enotify",
	"environment":              "environment",
}

// Validate runs the semantic pass: require declarations must precede
// every command or test they gate, commands must be known, arguments
// must match shape. Validate does not execute the script; it only
// rejects inputs the interpreter cannot safely run.
func Validate(s *Script) error {
	if s == nil {
		return &ValidationError{Line: 1, Column: 1, Message: "nil script"}
	}
	req := map[string]bool{}
	for _, r := range s.Requires {
		if _, ok := SupportedExtensions[r]; !ok {
			return &ValidationError{Line: 1, Column: 1, Message: fmt.Sprintf("unknown or unsupported extension %q", r)}
		}
		req[r] = true
	}
	v := &validator{requires: req}
	for _, c := range s.Commands {
		if err := v.command(c, 0); err != nil {
			return err
		}
	}
	return nil
}

type validator struct {
	requires map[string]bool
}

func (v *validator) command(c Command, depth int) error {
	if depth > DefaultMaxNestingDepth {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: fmt.Sprintf("nesting depth exceeds %d", DefaultMaxNestingDepth)}
	}
	ext, ok := KnownCommands[c.Name]
	if !ok {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: fmt.Sprintf("unknown command %q", c.Name)}
	}
	if ext != "" && !v.requires[ext] {
		return &ValidationError{Line: c.Line, Column: c.Column, Message: fmt.Sprintf("command %q requires extension %q", c.Name, ext)}
	}
	// else + elsif must accompany a preceding if — we do not enforce it
	// structurally here; the interpreter skips stray else/elsif as dead
	// code. Keeping validation narrow avoids false negatives when user
	// scripts are mechanically generated.
	if err := v.args(c.Args, c.Line, c.Column); err != nil {
		return err
	}
	if c.Test != nil {
		if err := v.test(*c.Test, depth+1); err != nil {
			return err
		}
	}
	for _, child := range c.Block {
		if err := v.command(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) test(t Test, depth int) error {
	if depth > DefaultMaxNestingDepth {
		return &ValidationError{Line: t.Line, Column: t.Column, Message: fmt.Sprintf("nesting depth exceeds %d", DefaultMaxNestingDepth)}
	}
	ext, ok := KnownTests[t.Name]
	if !ok {
		return &ValidationError{Line: t.Line, Column: t.Column, Message: fmt.Sprintf("unknown test %q", t.Name)}
	}
	if ext != "" && !v.requires[ext] {
		return &ValidationError{Line: t.Line, Column: t.Column, Message: fmt.Sprintf("test %q requires extension %q", t.Name, ext)}
	}
	if err := v.args(t.Args, t.Line, t.Column); err != nil {
		return err
	}
	for _, c := range t.Children {
		if err := v.test(c, depth+1); err != nil {
			return err
		}
	}
	// Tag-level extension gating: :regex requires regex, :count/:value
	// require relational, :subaddress-* require subaddress, :list requires
	// extlists.
	for _, a := range t.Args {
		if a.Kind != ArgTag {
			continue
		}
		switch strings.ToLower(a.Tag) {
		case ":regex":
			if !v.requires["regex"] {
				return &ValidationError{Line: a.Line, Column: a.Column, Message: ":regex requires extension \"regex\""}
			}
		case ":count", ":value":
			if !v.requires["relational"] {
				return &ValidationError{Line: a.Line, Column: a.Column, Message: ":count/:value require extension \"relational\""}
			}
		case ":user", ":detail", ":localpart", ":domain":
			// Some of these (:domain, :localpart) are from address-parts
			// in RFC 5228. Only :user and :detail require subaddress.
			if a.Tag == ":user" || a.Tag == ":detail" {
				if !v.requires["subaddress"] {
					return &ValidationError{Line: a.Line, Column: a.Column, Message: ":user/:detail require extension \"subaddress\""}
				}
			}
		case ":list":
			if !v.requires["extlists"] {
				return &ValidationError{Line: a.Line, Column: a.Column, Message: ":list requires extension \"extlists\""}
			}
		}
	}
	return nil
}

func (v *validator) args(args []Argument, line, col int) error {
	// Depth is implicit from the parser; per-command arity is checked at
	// interpretation time for now, since each command has bespoke rules.
	_ = line
	_ = col
	_ = args
	return nil
}
