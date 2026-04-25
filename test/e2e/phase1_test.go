// Package e2e holds the Phase 1 exit-criteria scenarios. Each test is
// parameterised over the registered backends (SQLite always; Postgres
// when HEROLD_PG_DSN is set) via fixtures.Run.
//
// Naming convention: TestPhase1_<Criterion>_<Shape>. This keeps the
// exit-criteria cross-reference obvious when reading test output.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/test/e2e/fixtures"
)

// TestPhase1_SMTPtoIMAP_Roundtrip exercises the canonical exit
// criterion: send a RFC 5322 message over SMTP, read it back over
// IMAPS, and assert the envelope + body survived verbatim.
func TestPhase1_SMTPtoIMAP_Roundtrip(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		f := fixtures.Build(t, fixtures.Opts{Store: fixtures.Prepare(t, newStore)})

		body := "From: bob@sender.test\r\n" +
			"To: " + f.Email + "\r\n" +
			"Subject: phase1-roundtrip\r\n" +
			"Date: Fri, 01 May 2026 12:00:00 +0000\r\n" +
			"Message-ID: <phase1-roundtrip@sender.test>\r\n" +
			"\r\n" +
			"hello from phase1\r\n"
		f.SendMessage(t, "bob@sender.test", []string{f.Email}, body, true)

		// Independent verification via the store surface.
		msgs := fixtures.LoadMessagesIn(t, f, f.Principal, "INBOX")
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message in INBOX, got %d", len(msgs))
		}
		if !bytes.Contains(msgs[0].Bytes, []byte("hello from phase1")) {
			t.Fatalf("body not preserved in stored blob")
		}
		if msgs[0].Message.Envelope.Subject != "phase1-roundtrip" {
			t.Fatalf("envelope subject: %q", msgs[0].Message.Envelope.Subject)
		}

		// IMAP FETCH path: LOGIN, SELECT, UID FETCH, assert body text
		// shows up on the wire.
		c := f.DialIMAP(t)
		defer c.Close()
		c.Login(f.Email, f.Password)
		if resp := c.Send("s1", "SELECT INBOX"); !containsOK(resp) {
			t.Fatalf("SELECT: %v", resp)
		}
		resp := c.Send("f1", "FETCH 1 (UID ENVELOPE BODY[])")
		joined := strings.Join(resp, "\n")
		for _, needle := range []string{
			"UID ",
			"phase1-roundtrip",   // subject in ENVELOPE
			"hello from phase1",  // body text
			"<phase1-roundtrip@", // Message-ID reported in ENVELOPE
		} {
			if !strings.Contains(joined, needle) {
				t.Fatalf("FETCH missing %q:\n%s", needle, joined)
			}
		}
	})
}

// TestPhase1_Sieve_SpamToJunk demonstrates that a principal's Sieve
// script routes spam to Junk while delivering ham to INBOX. Both paths
// run against the same fixture, against both backends.
func TestPhase1_Sieve_SpamToJunk(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		f := fixtures.Build(t, fixtures.Opts{Store: fixtures.Prepare(t, newStore)})

		// Fake classifier: verdict = spam when the message carries an
		// "X-Spam: yes" header; otherwise ham. The classifier speaks
		// through the fake plugin registered by fixtures.Build; the
		// spam package re-runs its own heuristics atop the plugin
		// response so we have to feed back either spam or ham in the
		// response JSON based on what the plugin sees.
		f.SpamPlugin.Handle("spam.classify", func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			// The spam package marshals a request with a Headers or
			// Body excerpt. We peek at the JSON for the "X-Spam" key.
			if bytes.Contains(params, []byte(`"X-Spam"`)) || bytes.Contains(params, []byte("X-Spam: yes")) {
				return json.RawMessage(`{"verdict":"spam","score":0.95}`), nil
			}
			return json.RawMessage(`{"verdict":"ham","score":0.1}`), nil
		})

		script := `require ["fileinto", "spamtest", "relational"];
if header :contains "Subject" "XSPAMX" { fileinto "Junk"; stop; }
if spamtest :value "ge" :comparator "i;ascii-numeric" "5" { fileinto "Junk"; stop; }
`
		if err := f.HA.Store.Meta().SetSieveScript(context.Background(), f.Principal, script); err != nil {
			t.Fatalf("SetSieveScript: %v", err)
		}

		// Spam message: the X-Spam: yes header triggers both the Sieve
		// subject-match (if present) and the plugin branch. We embed
		// both the header and the XSPAMX subject marker so either
		// Sieve path fires.
		spamBody := "From: spammer@phish.test\r\n" +
			"To: " + f.Email + "\r\n" +
			"Subject: XSPAMX cheap deals\r\n" +
			"X-Spam: yes\r\n" +
			"\r\n" +
			"spam body text\r\n"
		f.SendMessage(t, "spammer@phish.test", []string{f.Email}, spamBody, false)

		// Ham message: no X-Spam header, subject does not match.
		hamBody := "From: alice@friends.test\r\n" +
			"To: " + f.Email + "\r\n" +
			"Subject: dinner saturday?\r\n" +
			"\r\n" +
			"are you free?\r\n"
		f.SendMessage(t, "alice@friends.test", []string{f.Email}, hamBody, true)

		junk := fixtures.LoadMessagesIn(t, f, f.Principal, "Junk")
		if len(junk) != 1 {
			t.Fatalf("expected 1 message in Junk, got %d", len(junk))
		}
		if !bytes.Contains(junk[0].Bytes, []byte("cheap deals")) {
			t.Fatalf("wrong message in Junk:\n%s", junk[0].Bytes)
		}

		inbox := fixtures.LoadMessagesIn(t, f, f.Principal, "INBOX")
		if len(inbox) != 1 {
			t.Fatalf("expected 1 message in INBOX, got %d", len(inbox))
		}
		if !bytes.Contains(inbox[0].Bytes, []byte("dinner saturday?")) {
			t.Fatalf("wrong message in INBOX:\n%s", inbox[0].Bytes)
		}
	})
}

// TestPhase1_LLMClassifier_HeaderStamped asserts that the delivery
// pipeline stamps an Authentication-Results header before the body and
// includes the spam classifier verdict as an experimental
// "x-herold-spam=<verdict>" method token (RFC 8601 §2.7 extensibility).
// The header carries the score in parentheses so operators inspecting
// the stored message can see what the classifier decided.
func TestPhase1_LLMClassifier_HeaderStamped(t *testing.T) {
	fixtures.Run(t, func(t *testing.T, newStore fixtures.BackendFactory) {
		f := fixtures.Build(t, fixtures.Opts{Store: fixtures.Prepare(t, newStore)})

		f.SpamPlugin.Handle("spam.classify", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"verdict":"spam","score":0.9}`), nil
		})

		// Seed a DMARC record for the sender so the AR header has at
		// least one non-trivial method/result pair.
		f.HA.AddDNSRecord("_dmarc.sender.test", "TXT", "v=DMARC1; p=none;")

		body := "From: bob@sender.test\r\n" +
			"To: " + f.Email + "\r\n" +
			"Subject: classifier-header\r\n" +
			"\r\n" +
			"body\r\n"
		f.SendMessage(t, "bob@sender.test", []string{f.Email}, body, true)

		msgs := fixtures.LoadMessagesIn(t, f, f.Principal, "INBOX")
		if len(msgs) != 1 {
			t.Fatalf("expected 1 msg in INBOX, got %d", len(msgs))
		}
		raw := msgs[0].Bytes
		arIdx := bytes.Index(raw, []byte("Authentication-Results:"))
		if arIdx < 0 {
			t.Fatalf("no Authentication-Results in stored body:\n%s", raw)
		}
		// The header must appear before the From: to prove it was
		// prepended, not copied from the envelope.
		fromIdx := bytes.Index(raw, []byte("From: bob@sender.test"))
		if fromIdx < 0 || arIdx > fromIdx {
			t.Fatalf("AR not prepended:\n%s", raw)
		}
		// Mail-auth method tokens: at least one of the standard methods
		// must appear so we know the header is meaningful.
		if !regexp.MustCompile(`(spf|dkim|dmarc|arc)=`).Match(raw) {
			t.Errorf("AR missing mail-auth method tokens:\n%s", raw)
		}

		// Spam-verdict surface (Wave 4.5 / Finding 11): the classifier
		// verdict MUST be visible in the stored Authentication-Results
		// header as an "x-herold-spam=<verdict>" method token.
		if !bytes.Contains(raw, []byte("x-herold-spam=spam")) {
			t.Fatalf("expected x-herold-spam=spam method in AR; got:\n%s", raw)
		}
	})
}

// containsOK reports whether the last line of resp indicates an IMAP
// tagged OK.
func containsOK(resp []string) bool {
	if len(resp) == 0 {
		return false
	}
	return strings.Contains(resp[len(resp)-1], "OK")
}
