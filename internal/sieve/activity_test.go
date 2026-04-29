package sieve

// Activity-tagging tests for the Sieve interpreter (REQ-OPS-86a).
//
// These tests exercise the paths that must emit activity-tagged log records:
//   - Interpreter action logging (system/debug) for keep, fileinto, redirect,
//     reject, vacation actions.
//   - Vacation-response sent (system/info).
//   - Runtime error (internal/warn) when the sandbox budget is exceeded.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
)

// evaluateWithLog is a test helper that runs a script against sampleMsg with
// the provided logger injected into the environment.
func evaluateWithLog(t *testing.T, src string, env Environment, log *slog.Logger) (Outcome, error) {
	t.Helper()
	script, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(script); err != nil {
		t.Fatalf("validate: %v", err)
	}
	msg := buildMessage(t, sampleMsg)
	env.Logger = log
	in := NewInterpreter()
	return in.Evaluate(context.Background(), script, msg, env)
}

// TestActivityTagging_Action_FileInto asserts that fileinto emits an
// activity-tagged log record (REQ-OPS-86a, system/debug per the guide).
func TestActivityTagging_Action_FileInto(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		src := `require "fileinto";
if header :contains "Subject" "Greetings" {
  fileinto "INBOX.Test";
}`
		_, err := evaluateWithLog(t, src, Environment{}, log)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	})
}

// TestActivityTagging_Action_Redirect asserts redirect emits an activity-tagged
// record (REQ-OPS-86a).
func TestActivityTagging_Action_Redirect(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		src := `redirect "fwd@example.com";`
		_, err := evaluateWithLog(t, src, Environment{}, log)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	})
}

// TestActivityTagging_Action_Keep asserts keep emits an activity-tagged record.
func TestActivityTagging_Action_Keep(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		src := `keep;`
		_, err := evaluateWithLog(t, src, Environment{}, log)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	})
}

// TestActivityTagging_Action_Discard asserts discard emits an activity-tagged
// record (REQ-OPS-86a).
func TestActivityTagging_Action_Discard(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		src := `discard;`
		_, err := evaluateWithLog(t, src, Environment{}, log)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	})
}

// TestActivityTagging_Action_Reject asserts reject emits an activity-tagged
// record (REQ-OPS-86a).
func TestActivityTagging_Action_Reject(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		src := `require "reject"; reject "nope";`
		_, err := evaluateWithLog(t, src, Environment{}, log)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	})
}

// TestActivityTagging_Vacation_SystemInfo asserts vacation-response sent emits a
// system/info record (REQ-OPS-86 activity guide).
func TestActivityTagging_Vacation_SystemInfo(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		src := `require "vacation";
vacation :days 7 :handle "hh" "I am away";`
		vs := NewInMemoryVacationStore()
		clk := clock.NewFake(time.Unix(1700000000, 0))
		env := Environment{
			Sender:   "sender@example.com",
			Vacation: vs,
			Clock:    clk,
			Now:      clk.Now(),
		}
		_, err := evaluateWithLog(t, src, env, log)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	})
}

// TestActivityTagging_RuntimeError asserts that a sandbox budget-exceeded error
// emits an internal/warn record (REQ-OPS-86 activity guide).
func TestActivityTagging_RuntimeError(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		var b strings.Builder
		b.WriteString(`require "variables";`)
		b.WriteString("if allof(")
		for i := 0; i < 1000; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("true")
		}
		b.WriteString(`) { set "x" "y"; }`)
		script := mustParse(t, b.String())
		if err := Validate(script); err != nil {
			t.Fatal(err)
		}
		msg := buildMessage(t, sampleMsg)
		env := Environment{
			Limits: SandboxLimits{
				MaxInstructions:       50,
				MaxVariables:          10,
				MaxVariableBytes:      100,
				MaxTotalVariableBytes: 1000,
				MaxRedirects:          5,
				MaxNotifies:           2,
				MaxOutcomeActions:     10,
			},
			Logger: log,
		}
		// The error is expected; we only care that the log record was emitted.
		_, _ = NewInterpreter().Evaluate(context.Background(), script, msg, env)
	})
}
