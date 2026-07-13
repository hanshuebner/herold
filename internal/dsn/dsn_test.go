package dsn_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/dsn"
)

// postfixHardBounce is a representative Postfix permanent-failure DSN
// (5.1.1 unknown user), the most common real-world hard-bounce shape.
const postfixHardBounce = "From: MAILER-DAEMON@mail.example.com\r\n" +
	"To: list+bounce-abc123@example.com\r\n" +
	"Subject: Undelivered Mail Returned to Sender\r\n" +
	"Date: Mon, 01 Jun 2026 10:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
	"    boundary=\"1234567890ABCDEF\"\r\n" +
	"\r\n" +
	"--1234567890ABCDEF\r\n" +
	"Content-Description: Notification\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"This is the mail system at host mail.example.com.\r\n\r\n" +
	"I'm sorry to have to inform you that your message could not\r\n" +
	"be delivered to one or more recipients.\r\n\r\n" +
	"<bob@example.net>: host mx.example.net[192.0.2.1] said: 550 5.1.1\r\n" +
	"    <bob@example.net>: Recipient address rejected: User unknown in\r\n" +
	"    virtual mailbox table (in reply to RCPT TO command)\r\n" +
	"\r\n" +
	"--1234567890ABCDEF\r\n" +
	"Content-Description: Delivery report\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; mail.example.com\r\n" +
	"X-Postfix-Queue-ID: 4XXXXXXXXX\r\n" +
	"X-Postfix-Sender: rfc822; list+bounce-abc123@example.com\r\n" +
	"Arrival-Date: Mon, 1 Jun 2026 09:59:59 +0000 (UTC)\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822; bob@example.net\r\n" +
	"Original-Recipient: rfc822; bob@example.net\r\n" +
	"Action: failed\r\n" +
	"Status: 5.1.1\r\n" +
	"Remote-MTA: dns; mx.example.net\r\n" +
	"Diagnostic-Code: smtp; 550 5.1.1 <bob@example.net>: Recipient address rejected: User unknown in virtual mailbox table\r\n" +
	"Last-Attempt-Date: Mon, 1 Jun 2026 10:00:00 +0000 (UTC)\r\n" +
	"\r\n" +
	"--1234567890ABCDEF\r\n" +
	"Content-Description: Undelivered Message\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"From: poster@sender.test\r\n" +
	"To: list@example.com\r\n" +
	"Subject: quarterly update\r\n" +
	"\r\n" +
	"Body.\r\n" +
	"\r\n" +
	"--1234567890ABCDEF--\r\n"

// eximHardBounce is a representative Exim permanent-failure DSN. Exim's
// field ordering and its "R=... T=..." style human-readable part differ
// from Postfix's but the message/delivery-status structure is the same.
const eximHardBounce = "From: Mail Delivery System <Mailer-Daemon@mx.example.org>\r\n" +
	"To: list+bounce-def456@example.com\r\n" +
	"Subject: Mail delivery failed: returning message to sender\r\n" +
	"Date: Mon, 01 Jun 2026 11:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
	"	boundary=eximdsn-boundary-1\r\n" +
	"\r\n" +
	"--eximdsn-boundary-1\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"This message was created automatically by mail delivery software.\r\n\r\n" +
	"A message that you sent could not be delivered to one or more of\r\n" +
	"its recipients. This is a permanent error.\r\n\r\n" +
	"  carol@example.org\r\n" +
	"    SMTP error from remote mail server after RCPT TO:<carol@example.org>:\r\n" +
	"    host mx.example.org [192.0.2.9]: 550 5.1.1 No such user here\r\n" +
	"\r\n" +
	"--eximdsn-boundary-1\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; mx.example.org\r\n" +
	"\r\n" +
	"Action: failed\r\n" +
	"Final-Recipient: rfc822;carol@example.org\r\n" +
	"Status: 5.1.1\r\n" +
	"Remote-MTA: dns; mx.example.org\r\n" +
	"Diagnostic-Code: smtp; 550 5.1.1 No such user here\r\n" +
	"\r\n" +
	"--eximdsn-boundary-1--\r\n"

// gmailHardBounce is a representative Google/Gmail permanent-failure DSN.
const gmailHardBounce = "From: Mail Delivery Subsystem <mailer-daemon@googlemail.com>\r\n" +
	"To: list+bounce-ghi789@example.com\r\n" +
	"Subject: Delivery Status Notification (Failure)\r\n" +
	"Date: Mon, 01 Jun 2026 12:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
	"    boundary=\"0000000000005cabc1063deadbef\"\r\n" +
	"\r\n" +
	"--0000000000005cabc1063deadbef\r\n" +
	"Content-Type: text/plain; charset=UTF-8\r\n" +
	"\r\n" +
	"** Address not found **\r\n\r\n" +
	"Your message wasn't delivered to dana@gmail.com because the address\r\n" +
	"couldn't be found, or is unable to receive mail.\r\n" +
	"\r\n" +
	"--0000000000005cabc1063deadbef\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; googlemail.com\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822; dana@gmail.com\r\n" +
	"Action: failed\r\n" +
	"Status: 5.1.1\r\n" +
	"Diagnostic-Code: smtp; 550 5.1.1 The email account that you tried to\r\n" +
	" reach does not exist.\r\n" +
	"\r\n" +
	"--0000000000005cabc1063deadbef\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"From: poster@sender.test\r\n" +
	"To: list@example.com\r\n" +
	"\r\n" +
	"Body.\r\n" +
	"--0000000000005cabc1063deadbef--\r\n"

// outlookSoftBounce is a representative Exchange Online / Outlook.com
// transient-failure DSN (mailbox over quota).
const outlookSoftBounce = "From: Postmaster <postmaster@contoso-com.mail.protection.outlook.com>\r\n" +
	"To: list+bounce-jkl012@example.com\r\n" +
	"Subject: Undeliverable: quarterly update\r\n" +
	"Date: Mon, 01 Jun 2026 13:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
	"	boundary=\"outlook-boundary-1\"\r\n" +
	"\r\n" +
	"--outlook-boundary-1\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Your message wasn't delivered due to a mailbox full condition and\r\n" +
	"will be retried.\r\n" +
	"\r\n" +
	"--outlook-boundary-1\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns;contoso-com.mail.protection.outlook.com\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822;erin@contoso.com\r\n" +
	"Action: delayed\r\n" +
	"Status: 4.2.2\r\n" +
	"Diagnostic-Code: smtp;452 4.2.2 Mailbox full\r\n" +
	"Will-Retry-Until: Wed, 03 Jun 2026 13:00:00 +0000\r\n" +
	"\r\n" +
	"--outlook-boundary-1--\r\n"

// nonConformantOutlookBounce mimics an older on-premises Exchange NDR:
// a plain, non-multipart "Undeliverable" notice with no message/
// delivery-status part at all -- the "bounce-ish" case the parser must
// degrade gracefully on rather than guessing from free text.
const nonConformantOutlookBounce = "From: System Administrator <postmaster@corp.example>\r\n" +
	"To: list+bounce-mno345@example.com\r\n" +
	"Subject: Undeliverable: quarterly update\r\n" +
	"Date: Mon, 01 Jun 2026 14:00:00 +0000\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Your message did not reach some or all of the intended recipients.\r\n\r\n" +
	"      Subject: quarterly update\r\n" +
	"      Sent: 6/1/2026 2:00 PM\r\n\r\n" +
	"The following recipient(s) could not be reached:\r\n\r\n" +
	"      frank@corp.example\r\n" +
	"            The e-mail address could not be found.\r\n"

// successDeliveryReport is a delivery *success* notification -- must
// classify Unknown, never as a bounce.
const successDeliveryReport = "From: MAILER-DAEMON@mail.example.com\r\n" +
	"To: list+bounce-pqr678@example.com\r\n" +
	"Subject: Successful Mail Delivery Report\r\n" +
	"Date: Mon, 01 Jun 2026 15:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status;\r\n" +
	"    boundary=\"success-boundary-1\"\r\n" +
	"\r\n" +
	"--success-boundary-1\r\n" +
	"Content-Type: text/plain; charset=us-ascii\r\n" +
	"\r\n" +
	"Your message was successfully delivered.\r\n" +
	"\r\n" +
	"--success-boundary-1\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; mail.example.com\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822; grace@example.net\r\n" +
	"Action: delivered\r\n" +
	"Status: 2.0.0\r\n" +
	"\r\n" +
	"--success-boundary-1--\r\n"

func mustParse(t *testing.T, raw string) dsn.Report {
	t.Helper()
	report, err := dsn.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return report
}

func TestParse_Postfix_HardBounce(t *testing.T) {
	report := mustParse(t, postfixHardBounce)
	if report.Classification != dsn.ClassificationHard {
		t.Fatalf("Classification = %v, want Hard", report.Classification)
	}
	if report.ReportingMTA != "mail.example.com" {
		t.Errorf("ReportingMTA = %q, want mail.example.com", report.ReportingMTA)
	}
	if len(report.Recipients) != 1 {
		t.Fatalf("Recipients = %d, want 1", len(report.Recipients))
	}
	rs := report.Recipients[0]
	if rs.FinalRecipient != "bob@example.net" {
		t.Errorf("FinalRecipient = %q, want bob@example.net", rs.FinalRecipient)
	}
	if rs.Action != "failed" || rs.Status != "5.1.1" {
		t.Errorf("Action/Status = %q/%q, want failed/5.1.1", rs.Action, rs.Status)
	}
	if !strings.Contains(rs.DiagnosticCode, "User unknown") {
		t.Errorf("DiagnosticCode = %q, want it to contain the remote's text", rs.DiagnosticCode)
	}
}

func TestParse_Exim_HardBounce(t *testing.T) {
	report := mustParse(t, eximHardBounce)
	if report.Classification != dsn.ClassificationHard {
		t.Fatalf("Classification = %v, want Hard", report.Classification)
	}
	if len(report.Recipients) != 1 || report.Recipients[0].FinalRecipient != "carol@example.org" {
		t.Fatalf("Recipients = %+v, want one entry for carol@example.org", report.Recipients)
	}
}

func TestParse_Gmail_HardBounce(t *testing.T) {
	report := mustParse(t, gmailHardBounce)
	if report.Classification != dsn.ClassificationHard {
		t.Fatalf("Classification = %v, want Hard", report.Classification)
	}
	if len(report.Recipients) != 1 || report.Recipients[0].FinalRecipient != "dana@gmail.com" {
		t.Fatalf("Recipients = %+v, want one entry for dana@gmail.com", report.Recipients)
	}
	// Folded Diagnostic-Code continuation line must be joined.
	if !strings.Contains(report.Recipients[0].DiagnosticCode, "does not exist") {
		t.Errorf("DiagnosticCode = %q, want the folded continuation joined in", report.Recipients[0].DiagnosticCode)
	}
}

func TestParse_Outlook_SoftBounce(t *testing.T) {
	report := mustParse(t, outlookSoftBounce)
	if report.Classification != dsn.ClassificationSoft {
		t.Fatalf("Classification = %v, want Soft", report.Classification)
	}
	if len(report.Recipients) != 1 || report.Recipients[0].Status != "4.2.2" {
		t.Fatalf("Recipients = %+v, want one 4.2.2 entry", report.Recipients)
	}
}

// TestParse_NonConformantBounce_ClassifiesUnknown exercises the "not
// every bounce is a conformant RFC 3464 report" requirement: a plain,
// non-multipart NDR with no message/delivery-status part at all
// classifies Unknown (no Recipients extracted) rather than guessing
// from the free text or erroring out.
func TestParse_NonConformantBounce_ClassifiesUnknown(t *testing.T) {
	report := mustParse(t, nonConformantOutlookBounce)
	if report.Classification != dsn.ClassificationUnknown {
		t.Fatalf("Classification = %v, want Unknown", report.Classification)
	}
	if len(report.Recipients) != 0 {
		t.Fatalf("Recipients = %+v, want none extracted from free text", report.Recipients)
	}
}

// TestParse_SuccessReport_ClassifiesUnknown exercises the conservative
// classification requirement from the other direction: a conformant
// DSN that reports SUCCESS must never be classified as a bounce.
func TestParse_SuccessReport_ClassifiesUnknown(t *testing.T) {
	report := mustParse(t, successDeliveryReport)
	if report.Classification != dsn.ClassificationUnknown {
		t.Fatalf("Classification = %v, want Unknown for a delivered report", report.Classification)
	}
}

// TestParse_NotAnEmailAtAll exercises the "malformed input never
// panics or errors the caller in a way that crashes" contract for
// bytes that are not even parseable as RFC 5322.
func TestParse_MalformedInputs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"garbage", "\x00\x01\x02not an email at all"},
		{"headers only no body", "From: a@b.test\r\nTo: c@d.test\r\n\r\n"},
		{"truncated multipart no closing boundary", "From: a@b.test\r\n" +
			"To: c@d.test\r\n" +
			"Content-Type: multipart/report; report-type=delivery-status; boundary=X\r\n" +
			"\r\n" +
			"--X\r\n" +
			"Content-Type: message/delivery-status\r\n" +
			"\r\n" +
			"Action: failed\r\n" +
			"Status: 5.1.1\r\n"},
		{"delivery-status with garbled fields", "From: a@b.test\r\n" +
			"To: c@d.test\r\n" +
			"Content-Type: multipart/report; report-type=delivery-status; boundary=X\r\n" +
			"\r\n" +
			"--X\r\n" +
			"Content-Type: message/delivery-status\r\n" +
			"\r\n" +
			"this is not a header block at all, just noise\xff\xfe\r\n" +
			"\r\n" +
			"--X--\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := dsn.Parse([]byte(tc.raw))
			if err != nil {
				// Acceptable outcome for genuinely non-RFC-5322 input
				// (e.g. "garbage", "empty"); must never panic (the fuzz
				// target covers that exhaustively) and must not report a
				// bounce classification alongside an error.
				if report.Classification != dsn.ClassificationUnknown {
					t.Errorf("error case reported Classification = %v, want Unknown (zero value)", report.Classification)
				}
				return
			}
			if report.Classification == dsn.ClassificationHard {
				t.Errorf("malformed/truncated input %q classified Hard; want conservative Unknown/Soft at most", tc.name)
			}
		})
	}
}

// TestClassification_String exercises the log/metric label rendering.
func TestClassification_String(t *testing.T) {
	cases := []struct {
		c    dsn.Classification
		want string
	}{
		{dsn.ClassificationUnknown, "unknown"},
		{dsn.ClassificationSoft, "soft"},
		{dsn.ClassificationHard, "hard"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("Classification(%d).String() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// ExampleParse demonstrates the package's entry point and its
// conservative classification contract (STANDARDS.md §8: documentation
// examples are executable tests).
func ExampleParse() {
	report, err := dsn.Parse([]byte(postfixHardBounce))
	if err != nil {
		panic(err)
	}
	fmt.Println(report.Classification)
	fmt.Println(report.Recipients[0].FinalRecipient)
	fmt.Println(report.Recipients[0].Status)
	// Output:
	// hard
	// bob@example.net
	// 5.1.1
}
