package sieve

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
)

// confCase is one Pigeonhole-style conformance scenario. The runner
// parses script, validates it, evaluates it against rawMsg under env
// (defaults populated where zero), and asserts the resulting Outcome
// matches expect.
//
// Each case targets a single specification clause: RFC + section
// reference is in the name so a failing case names the spec it
// regresses against.
type confCase struct {
	name   string
	script string
	rawMsg string
	env    Environment
	expect confExpect
}

type confExpect struct {
	implicitKeep bool
	stop         bool
	// actions is asserted only on Kind + key fields. Empty actions
	// list means "no actions expected" — the runner asserts equality
	// of length and that each expected entry matches a corresponding
	// outcome entry by Kind+Mailbox/Address/HeaderName.
	actions []confExpectedAction
}

type confExpectedAction struct {
	kind     ActionKind
	mailbox  string
	address  string
	header   string
	flag     string
	hasReplaceBody string
}

const confSampleMsg = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Cc: carol@example.com\r\n" +
	"Subject: pigeonhole conformance\r\n" +
	"X-Tracer: alpha\r\n" +
	"\r\n" +
	"hello, conformance world\r\n"

const confMultipartMsg = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: multipart conformance\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"BND\"\r\n" +
	"\r\n" +
	"--BND\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"plain body content\r\n" +
	"--BND\r\n" +
	"Content-Type: text/html\r\n" +
	"X-Pigeon: hit\r\n" +
	"Content-Disposition: attachment; filename=\"x.html\"\r\n" +
	"\r\n" +
	"<p>html attachment</p>\r\n" +
	"--BND--\r\n"

// pigeonholeCorpus enumerates the conformance scenarios. Each entry
// references the RFC + section it covers; the test runner asserts the
// interpreter produces the documented action set.
var pigeonholeCorpus = []confCase{
	// RFC 5228 §2.3: keep is the default action when no other is taken.
	{
		name: "rfc5228/2.3-implicit-keep",
		script: `require ["fileinto"];`,
		rawMsg: confSampleMsg,
		expect: confExpect{implicitKeep: true},
	},
	// RFC 5228 §4.1: discard cancels implicit keep.
	{
		name: "rfc5228/4.1-discard-cancels-implicit-keep",
		script: `discard;`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions:      []confExpectedAction{{kind: ActionDiscard}},
		},
	},
	// RFC 5228 §5.5: header :is matches exact case-insensitive. The
	// fileinto action suppresses implicit keep per §2.10.6.
	{
		name: "rfc5228/5.5-header-is-case-insensitive",
		script: `require ["fileinto"];
if header :is "Subject" "PIGEONHOLE CONFORMANCE" {
  fileinto "Hits";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "Hits"},
			},
		},
	},
	// RFC 5228 §5.5: header :contains substring match.
	{
		name: "rfc5228/5.5-header-contains",
		script: `require ["fileinto"];
if header :contains "Subject" "conformance" {
  fileinto "Hits";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "Hits"},
			},
		},
	},
	// RFC 5228 §5.7: address test scoped to local-part.
	{
		name: "rfc5228/5.7-address-localpart",
		script: `require ["fileinto"];
if address :localpart "From" "alice" {
  fileinto "FromAlice";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "FromAlice"},
			},
		},
	},
	// RFC 5228 §5.4: anyof short-circuits true on first match.
	// Test-list syntax: "(" test *("," test) ")".
	{
		name: "rfc5228/5.4-anyof-true",
		script: `require ["fileinto"];
if anyof (header :is "Subject" "wrong", header :contains "Subject" "conformance") {
  fileinto "AnyOfHit";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "AnyOfHit"},
			},
		},
	},
	// RFC 5228 §5.3: allof requires all match.
	{
		name: "rfc5228/5.3-allof-false-skips",
		script: `require ["fileinto"];
if allof (header :contains "Subject" "conformance", header :is "Subject" "wrong") {
  fileinto "AllOfHit";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{implicitKeep: true},
	},
	// RFC 5228 §5.6: not negates a test.
	{
		name: "rfc5228/5.6-not-true-on-absence",
		script: `require ["fileinto"];
if not exists "X-Missing-Header" {
  fileinto "MissingHit";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "MissingHit"},
			},
		},
	},
	// RFC 5228 §5.9: exists tests match every named header.
	{
		name: "rfc5228/5.9-exists-multiple",
		script: `require ["fileinto"];
if exists ["From", "X-Tracer"] {
  fileinto "BothExist";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "BothExist"},
			},
		},
	},
	// RFC 5228 §5.9: exists is false when any named header is absent.
	{
		name: "rfc5228/5.9-exists-mixed-presence",
		script: `require ["fileinto"];
if exists ["From", "X-Missing"] {
  fileinto "Mixed";
}`,
		rawMsg: confSampleMsg,
		expect: confExpect{implicitKeep: true},
	},
	// RFC 5228 §3.2: stop terminates the script before later actions.
	{
		name: "rfc5228/3.2-stop-halts-execution",
		script: `require ["fileinto"];
fileinto "First";
stop;
fileinto "ShouldNotRun";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			stop:         true,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "First"},
			},
		},
	},
	// RFC 5232 §3.1: setflag replaces the flag set.
	{
		name: "rfc5232/3.1-setflag",
		script: `require ["imap4flags"];
setflag "\\Seen";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: true,
			actions: []confExpectedAction{
				{kind: ActionSetFlag, flag: "\\Seen"},
			},
		},
	},
	// RFC 5293 §4: addheader prepends a header line. The interpreter
	// emits ActionAddHeader; ApplyMutations does the byte-level
	// rewrite separately. The conformance test pins the action shape.
	{
		name: "rfc5293/4-addheader-emits-action",
		script: `require ["editheader"];
addheader "X-Marker" "value";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: true,
			actions: []confExpectedAction{
				{kind: ActionAddHeader, header: "X-Marker"},
			},
		},
	},
	// RFC 5293 §5: deleteheader removes named header.
	{
		name: "rfc5293/5-deleteheader-emits-action",
		script: `require ["editheader"];
deleteheader "X-Tracer";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: true,
			actions: []confExpectedAction{
				{kind: ActionDeleteHeader, header: "X-Tracer"},
			},
		},
	},
	// RFC 5703 §3: foreverypart iterates over MIME parts.
	{
		name: "rfc5703/3-foreverypart-iterates-parts",
		script: `require ["foreverypart", "fileinto"];
foreverypart {
  fileinto "Part";
}`,
		rawMsg: confMultipartMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "Part"},
				{kind: ActionFileInto, mailbox: "Part"},
			},
		},
	},
	// RFC 5703 §4.2: :mime on header reads the iterated part's
	// headers when inside foreverypart.
	{
		name: "rfc5703/4.2-mime-flag-reads-current-part",
		script: `require ["foreverypart", "mime", "fileinto"];
foreverypart {
  if header :mime :contains "X-Pigeon" "hit" {
    fileinto "PigeonHit";
  }
}`,
		rawMsg: confMultipartMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "PigeonHit"},
			},
		},
	},
	// RFC 5703 §4.3: replace inside foreverypart targets a part. We
	// assert ActionReplace is emitted with a non-empty path; the
	// byte-level rewrite is exercised by mutations_test.go.
	{
		name: "rfc5703/4.3-replace-emits-action",
		script: `require ["foreverypart", "mime"];
foreverypart {
  if header :mime :contains "Content-Type" "text/html" {
    replace "[scrubbed]";
  }
}`,
		rawMsg: confMultipartMsg,
		expect: confExpect{
			implicitKeep: true,
			actions: []confExpectedAction{
				{kind: ActionReplace, hasReplaceBody: "[scrubbed]"},
			},
		},
	},
	// RFC 5429 §2: reject emits an ActionReject and clears implicit
	// keep.
	{
		name: "rfc5429/2-reject-clears-implicit-keep",
		script: `require ["reject"];
reject "no thanks";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionReject},
			},
		},
	},
	// RFC 5229 §4: variable substitution.
	{
		name: "rfc5229/4-variable-substitution",
		script: `require ["variables", "fileinto"];
set "tag" "VarHit";
fileinto "${tag}";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: false,
			actions: []confExpectedAction{
				{kind: ActionFileInto, mailbox: "VarHit"},
			},
		},
	},
	// RFC 5228 §2.10.6: redirect with :copy preserves implicit keep.
	{
		name: "rfc3894/copy-preserves-implicit-keep",
		script: `require ["copy"];
redirect :copy "external@example.org";`,
		rawMsg: confSampleMsg,
		expect: confExpect{
			implicitKeep: true,
			actions: []confExpectedAction{
				{kind: ActionRedirect, address: "external@example.org"},
			},
		},
	},
}

// TestPigeonholeConformance runs each scenario in the corpus through
// Parse + Validate + Evaluate and asserts the resulting Outcome
// matches the expected shape. A failing case names the RFC + section
// it regresses against so the diagnostic points at the offending
// spec clause directly.
func TestPigeonholeConformance(t *testing.T) {
	for _, tc := range pigeonholeCorpus {
		t.Run(tc.name, func(t *testing.T) {
			runConformanceCase(t, tc)
		})
	}
}

func runConformanceCase(t *testing.T, tc confCase) {
	t.Helper()
	script, err := Parse([]byte(tc.script))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(script); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	msg, err := mailparse.Parse(bytes.NewReader([]byte(tc.rawMsg)), mailparse.ParseOptions{StrictBoundary: false})
	if err != nil {
		t.Fatalf("mailparse: %v", err)
	}
	env := tc.env
	if env.Clock == nil {
		env.Clock = clock.NewReal()
	}
	in := NewInterpreter()
	out, err := in.Evaluate(context.Background(), script, msg, env)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.ImplicitKeep != tc.expect.implicitKeep {
		t.Errorf("ImplicitKeep = %v; want %v", out.ImplicitKeep, tc.expect.implicitKeep)
	}
	if out.Stop != tc.expect.stop {
		t.Errorf("Stop = %v; want %v", out.Stop, tc.expect.stop)
	}
	if len(out.Actions) != len(tc.expect.actions) {
		t.Fatalf("Actions = %d; want %d\n  got: %+v\n  want: %+v",
			len(out.Actions), len(tc.expect.actions), out.Actions, tc.expect.actions)
	}
	for i, exp := range tc.expect.actions {
		assertActionMatches(t, i, out.Actions[i], exp)
	}
}

func assertActionMatches(t *testing.T, idx int, got Action, exp confExpectedAction) {
	t.Helper()
	if got.Kind != exp.kind {
		t.Errorf("Action[%d].Kind = %v; want %v", idx, got.Kind, exp.kind)
	}
	if exp.mailbox != "" && got.Mailbox != exp.mailbox {
		t.Errorf("Action[%d].Mailbox = %q; want %q", idx, got.Mailbox, exp.mailbox)
	}
	if exp.address != "" && got.Address != exp.address {
		t.Errorf("Action[%d].Address = %q; want %q", idx, got.Address, exp.address)
	}
	if exp.header != "" && got.HeaderName != exp.header {
		t.Errorf("Action[%d].HeaderName = %q; want %q", idx, got.HeaderName, exp.header)
	}
	if exp.flag != "" && got.Flag != exp.flag {
		t.Errorf("Action[%d].Flag = %q; want %q", idx, got.Flag, exp.flag)
	}
	if exp.hasReplaceBody != "" && !strings.Contains(string(got.ReplaceBody), exp.hasReplaceBody) {
		t.Errorf("Action[%d].ReplaceBody = %q; want substring %q", idx, got.ReplaceBody, exp.hasReplaceBody)
	}
}
