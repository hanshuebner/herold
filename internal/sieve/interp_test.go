package sieve

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
)

// buildMessage parses a raw RFC 5322 message via internal/mailparse.
func buildMessage(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.Parse(bytes.NewReader([]byte(raw)), mailparse.ParseOptions{StrictBoundary: false})
	if err != nil {
		t.Fatalf("mailparse: %v", err)
	}
	return msg
}

const sampleMsg = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: Greetings\r\n" +
	"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
	"Message-ID: <a@b>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello, how are you?\r\nPlease visit https://example.com\r\n"

func runScript(t *testing.T, src string, env Environment, raw string) Outcome {
	t.Helper()
	script, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(script); err != nil {
		t.Fatalf("validate: %v", err)
	}
	msg := buildMessage(t, raw)
	in := NewInterpreter()
	out, err := in.Evaluate(context.Background(), script, msg, env)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return out
}

func TestInterp_ImplicitKeep(t *testing.T) {
	out := runScript(t, `require "fileinto";`, Environment{}, sampleMsg)
	if !out.ImplicitKeep {
		t.Fatalf("expected implicit keep, got %+v", out)
	}
}

func TestInterp_Discard(t *testing.T) {
	out := runScript(t, `discard;`, Environment{}, sampleMsg)
	if out.ImplicitKeep {
		t.Fatal("discard must clear implicit keep")
	}
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionDiscard {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_FileInto(t *testing.T) {
	src := `require "fileinto";
if header :contains "Subject" "Greetings" {
  fileinto "INBOX.Hello";
}`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionFileInto || out.Actions[0].Mailbox != "INBOX.Hello" {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_AddressTest(t *testing.T) {
	src := `if address :is :domain "From" "example.com" { discard; }`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionDiscard {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_SizeOver(t *testing.T) {
	src := `if size :over 1 { discard; }`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_EnvelopeFrom(t *testing.T) {
	src := `require "envelope";
if envelope :is "from" "mallory@evil.example" { discard; }`
	out := runScript(t, src, Environment{Sender: "mallory@evil.example"}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionDiscard {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_BodyContains(t *testing.T) {
	src := `require "body";
if body :contains "visit" { discard; }`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionDiscard {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_Reject(t *testing.T) {
	src := `require "reject"; reject "nope";`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionReject || out.Actions[0].Reason != "nope" {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_IMAP4Flags(t *testing.T) {
	src := `require "imap4flags"; setflag "\\Seen";`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Flag != "\\Seen" {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_Variables(t *testing.T) {
	src := `require ["variables","fileinto"];
set "folder" "INBOX.Auto";
if header :contains "Subject" "Greet" {
  fileinto "${folder}";
}`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Mailbox != "INBOX.Auto" {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_Vacation_Dedup(t *testing.T) {
	src := `require "vacation";
vacation :days 7 :handle "hh" "I am away";`
	store := NewInMemoryVacationStore()
	clk := clock.NewFake(time.Unix(1700000000, 0))
	env := Environment{Sender: "s@x", Vacation: store, Clock: clk, Now: clk.Now()}
	out := runScript(t, src, env, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionVacation {
		t.Fatalf("first invocation should produce vacation: %+v", out.Actions)
	}
	// Second invocation inside the 7-day window must dedup.
	out2 := runScript(t, src, env, sampleMsg)
	for _, a := range out2.Actions {
		if a.Kind == ActionVacation {
			t.Fatalf("second invocation must dedup; got %+v", out2.Actions)
		}
	}
}

func TestInterp_Vacation_MissingStore(t *testing.T) {
	script, err := Parse([]byte(`require "vacation"; vacation "hi";`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(script); err != nil {
		t.Fatal(err)
	}
	msg := buildMessage(t, sampleMsg)
	_, err = NewInterpreter().Evaluate(context.Background(), script, msg, Environment{Sender: "s@x"})
	if err == nil || !strings.Contains(err.Error(), "no VacationStore") {
		t.Fatalf("expected missing-store error; got %v", err)
	}
}

func TestInterp_Duplicate(t *testing.T) {
	src := `require "duplicate";
if duplicate :handle "h" { discard; }`
	store := NewInMemoryDuplicateStore()
	env := Environment{Duplicate: store, Now: time.Unix(1700000000, 0)}
	out := runScript(t, src, env, sampleMsg)
	if len(out.Actions) != 0 {
		t.Fatalf("first run: duplicate false; actions: %+v", out.Actions)
	}
	out2 := runScript(t, src, env, sampleMsg)
	if len(out2.Actions) != 1 || out2.Actions[0].Kind != ActionDiscard {
		t.Fatalf("second run: expected discard; got %+v", out2.Actions)
	}
}

func TestInterp_Relational_Count(t *testing.T) {
	src := `require ["relational","comparator-i;ascii-numeric"];
if header :count "ge" :comparator "i;ascii-numeric" "Received" "0" { discard; }`
	raw := sampleMsg
	out := runScript(t, src, Environment{}, raw)
	if len(out.Actions) != 1 {
		t.Fatalf("expected discard for count(Received) >= 0; got %+v", out.Actions)
	}
}

func TestInterp_Spamtest(t *testing.T) {
	src := `require ["spamtest","relational","comparator-i;ascii-numeric"];
if spamtest :value "ge" :comparator "i;ascii-numeric" "5" { fileinto "Junk"; }
require "fileinto";`
	// Rebuild in the correct order since requires must come first.
	src = `require ["spamtest","relational","comparator-i;ascii-numeric","fileinto"];
if spamtest :value "ge" :comparator "i;ascii-numeric" "5" { fileinto "Junk"; }`
	env := Environment{SpamScore: 0.9, SpamVerdict: "spam"}
	out := runScript(t, src, env, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Junk" {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_IfElsifElse(t *testing.T) {
	src := `require "fileinto";
if header :contains "Subject" "bogus" { fileinto "A"; }
elsif header :contains "Subject" "Greet" { fileinto "B"; }
else { fileinto "C"; }`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Mailbox != "B" {
		t.Fatalf("expected B; got %+v", out.Actions)
	}
}

func TestInterp_StopHaltsExecution(t *testing.T) {
	src := `require "fileinto";
if true { stop; }
fileinto "Never";`
	out := runScript(t, src, Environment{}, sampleMsg)
	for _, a := range out.Actions {
		if a.Mailbox == "Never" {
			t.Fatalf("stop failed to halt; got %+v", out.Actions)
		}
	}
}

func TestInterp_RegexMatch(t *testing.T) {
	src := `require "regex";
if header :regex "Subject" "^Greet" { discard; }`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 || out.Actions[0].Kind != ActionDiscard {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_GlobMatches(t *testing.T) {
	src := `if header :matches "Subject" "Gre*" { discard; }`
	out := runScript(t, src, Environment{}, sampleMsg)
	if len(out.Actions) != 1 {
		t.Fatalf("actions: %+v", out.Actions)
	}
}

func TestInterp_MailboxIDOverride(t *testing.T) {
	src := `require ["fileinto","mailboxid"]; fileinto :mailboxid "MB-1" "Folder";`
	out := runScript(t, src, Environment{}, sampleMsg)
	if out.Actions[0].MailboxID != "MB-1" {
		t.Fatalf("mailboxid: %+v", out.Actions[0])
	}
}

func TestInterp_RedirectCopyKeepsImplicitKeep(t *testing.T) {
	src := `require "copy"; redirect :copy "archive@example.com";`
	out := runScript(t, src, Environment{}, sampleMsg)
	if !out.ImplicitKeep {
		t.Fatalf("redirect :copy must not clear implicit keep")
	}
}

// ------ Sandbox bounds -------------------------------------------------------

func TestSandbox_InfiniteLoopBounded(t *testing.T) {
	// Sieve has no loop construct in the base language, but recursive
	// via-include or deep `foreverypart` isn't available here. We
	// construct a deeply-tested anyof chain that forces many ticks.
	var b strings.Builder
	b.WriteString(`require "variables";`)
	// A deeply-nested allof chain. Each test adds to tick count, so
	// clamping MaxInstructions low will trip the budget.
	b.WriteString("if allof(")
	for i := 0; i < 1000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("true")
	}
	b.WriteString(") { set \"x\" \"y\"; }")
	script, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(script); err != nil {
		t.Fatal(err)
	}
	msg := buildMessage(t, sampleMsg)
	env := Environment{Limits: SandboxLimits{
		MaxInstructions:       50,
		MaxVariables:          10,
		MaxVariableBytes:      100,
		MaxTotalVariableBytes: 1000,
		MaxRedirects:          5,
		MaxNotifies:           2,
		MaxOutcomeActions:     10,
	}}
	_, err = NewInterpreter().Evaluate(context.Background(), script, msg, env)
	if !errors.Is(err, ErrInstructionBudget) {
		t.Fatalf("expected ErrInstructionBudget, got %v", err)
	}
}

func TestSandbox_VariableCountBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`require "variables";`)
	for i := 0; i < 300; i++ {
		b.WriteString("set \"v")
		b.WriteString(itoa(i))
		b.WriteString("\" \"x\";")
	}
	script := mustParse(t, b.String())
	if err := Validate(script); err != nil {
		t.Fatal(err)
	}
	msg := buildMessage(t, sampleMsg)
	env := Environment{Limits: DefaultSandboxLimits()}
	_, err := NewInterpreter().Evaluate(context.Background(), script, msg, env)
	if !errors.Is(err, ErrVariableBudget) {
		t.Fatalf("expected ErrVariableBudget, got %v", err)
	}
}

func TestSandbox_VariableSizeBounded(t *testing.T) {
	big := strings.Repeat("a", 10_000)
	src := `require "variables"; set "v" "` + big + `";`
	script := mustParse(t, src)
	if err := Validate(script); err != nil {
		t.Fatal(err)
	}
	msg := buildMessage(t, sampleMsg)
	env := Environment{Limits: DefaultSandboxLimits()}
	_, err := NewInterpreter().Evaluate(context.Background(), script, msg, env)
	if !errors.Is(err, ErrVariableSize) {
		t.Fatalf("expected ErrVariableSize, got %v", err)
	}
}

func TestSandbox_RedirectBounded(t *testing.T) {
	src := `redirect "a@x"; redirect "b@x"; redirect "c@x"; redirect "d@x"; redirect "e@x"; redirect "f@x";`
	script := mustParse(t, src)
	if err := Validate(script); err != nil {
		t.Fatal(err)
	}
	msg := buildMessage(t, sampleMsg)
	env := Environment{Limits: DefaultSandboxLimits()}
	_, err := NewInterpreter().Evaluate(context.Background(), script, msg, env)
	if !errors.Is(err, ErrRedirectBudget) {
		t.Fatalf("expected ErrRedirectBudget, got %v", err)
	}
}

func TestSandbox_NoFilesystemCommand(t *testing.T) {
	// Sieve in this implementation has no filesystem-opening command. A
	// user script attempting "open" or "exec" must be rejected.
	_, err := Parse([]byte(`open "/etc/passwd";`))
	if err != nil {
		// Parse can accept the identifier as a command name.
	}
	script, _ := Parse([]byte(`open "/etc/passwd";`))
	if script == nil {
		return
	}
	if err := Validate(script); err == nil {
		t.Fatalf("Validate must reject unknown command 'open'")
	}
}

// itoa is inlined because strconv adds a fuzz-irrelevant import path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
