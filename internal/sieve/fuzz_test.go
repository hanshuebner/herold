package sieve

import (
	"context"
	"testing"
	"time"
)

// FuzzParse drives Parse over random-ish byte strings. Parse must never
// panic: every input either returns a valid *Script or a typed
// *ParseError.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"keep;",
		`require "fileinto"; if header :is "X" "y" { fileinto "A"; }`,
		`if allof(true, not false) { discard; }`,
		"text:\n.\n",
		`set "v" "abc${hex:20}def";`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		// Parse MUST NOT panic regardless of input.
		_, _ = Parse(in)
	})
}

// FuzzInterp runs the interpreter against a canonical valid script with
// fuzzed message bytes. This exercises header/body/envelope handling on
// arbitrary inputs.
func FuzzInterp(f *testing.F) {
	script, err := Parse([]byte(`require ["fileinto","envelope","body","relational","regex"];
if header :contains "Subject" "urgent" { fileinto "INBOX.Urgent"; }
if size :over 1 { fileinto "INBOX.Large"; }
if body :contains "unsubscribe" { discard; }`))
	if err != nil {
		f.Fatal(err)
	}
	if err := Validate(script); err != nil {
		f.Fatal(err)
	}
	seeds := []string{
		"From: a@b\r\nSubject: urgent\r\n\r\nbody",
		"From: a@b\r\n\r\nunsubscribe",
		"Subject:\r\n\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	in := NewInterpreter()
	f.Fuzz(func(t *testing.T, raw []byte) {
		msg, err := parseSafe(raw)
		if err != nil {
			return
		}
		// Interpreter must not panic on arbitrary messages. Errors are
		// fine (sandbox budgets, bad test args); panics are not. The
		// per-input deadline is generous (1 s) because shared CI runners
		// occasionally see slow ticks that would otherwise mark a clean
		// fuzz pass as a timeout failure.
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_, _ = in.Evaluate(ctx, script, msg, Environment{Limits: DefaultSandboxLimits()})
	})
}
