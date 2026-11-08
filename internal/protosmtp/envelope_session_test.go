package protosmtp_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
)

// TestSession_Inbound_PathSyntax drives a live port-25 session: a quoted
// local part with whitespace is legal in both paths, genuine ESMTP
// parameters after the closing bracket are accepted, and whitespace inside
// the brackets (parameter smuggling) is a 501 (re #562).
func TestSession_Inbound_PathSyntax(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn})
	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)

	for _, line := range []string{
		`MAIL FROM:<"john doe"@sender.test> SIZE=100`,
		`MAIL FROM:<"a\ b"@sender.test>`,
		`MAIL FROM:<bob@sender.test> BODY=8BITMIME SMTPUTF8 RET=HDRS`,
		`MAIL FROM:<užívateľ@sender.test> SMTPUTF8`,
	} {
		cli.send(t, line)
		if code, text := cli.readReply(t); code != 250 {
			t.Fatalf("%s: %d %s", line, code, text)
		}
		cli.send(t, "RSET")
		mustOK(t, cli, 250)
	}

	cli.send(t, `MAIL FROM:<bob@sender.test NOTIFY=NEVER>`)
	if code, _ := cli.readReply(t); code != 501 {
		t.Fatalf("MAIL smuggling: %d, want 501", code)
	}
	cli.send(t, "MAIL FROM:<bob@sender.test>")
	mustOK(t, cli, 250)

	// An unknown recipient is refused for policy (550), never for syntax.
	for _, line := range []string{
		`RCPT TO:<"a b"@example.test>`,
		`RCPT TO:<"a\ b"@example.test> NOTIFY=SUCCESS,FAILURE ORCPT=rfc822;a@b.test`,
		`RCPT TO:<užívateľ@example.test>`,
	} {
		cli.send(t, line)
		if code, text := cli.readReply(t); code == 501 || code == 553 || code == 555 {
			t.Fatalf("%s: syntax refusal %d %s", line, code, text)
		}
	}
	cli.send(t, `RCPT TO:<bob@example.test NOTIFY=NEVER>`)
	if code, _ := cli.readReply(t); code != 501 {
		t.Fatalf("RCPT smuggling: %d, want 501", code)
	}
}

// TestSession_Submission_PathSyntax: on 587 an authenticated client's
// whitespace-bearing path is a 501, while quoted local parts and genuine
// parameters are accepted and queued unchanged.
func TestSession_Submission_PathSyntax(t *testing.T) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.SubmissionSTARTTLS})
	q := &fakeSubmissionQueue{}
	f.srv.SetSubmissionQueue(q)
	cli, closeFn := authPlainOnSubmission(t, f)
	defer closeFn()

	cli.send(t, `MAIL FROM:<alice@example.test NOTIFY=NEVER>`)
	if code, _ := cli.readReply(t); code != 501 {
		t.Fatalf("MAIL smuggling: %d, want 501", code)
	}
	cli.send(t, `MAIL FROM:<alice@example.test> SIZE=100 BODY=8BITMIME`)
	mustOK(t, cli, 250)
	cli.send(t, `RCPT TO:<bob@gmail.com NOTIFY=NEVER>`)
	if code, _ := cli.readReply(t); code != 501 {
		t.Fatalf("RCPT smuggling: %d, want 501", code)
	}
	cli.send(t, `RCPT TO:<bob@gmail.com> NOTIFY=SUCCESS,FAILURE ORCPT=rfc822;bob@gmail.com`)
	mustOK(t, cli, 250)
	cli.send(t, `RCPT TO:<"john doe"@gmail.com>`)
	mustOK(t, cli, 250)
}
