package sieve

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// corpusCase is one Pigeonhole-shaped scenario: a script paired with an
// expected set of actions for a fixed canonical message.
type corpusCase struct {
	name   string
	script string
	env    Environment
	check  func(t *testing.T, out Outcome)
}

func TestCorpus_Keyword_Matrix(t *testing.T) {
	const msg = "From: alice@example.com\r\n" +
		"To: bob+sales@example.com\r\n" +
		"Cc: carol@other.example\r\n" +
		"Subject: Q4 Report\r\n" +
		"List-Id: <sales.example.com>\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Message-ID: <ref-1@a>\r\n" +
		"\r\n" +
		"Quarterly report attached. See https://reports.example.com for more.\r\n"
	clk := clock.NewFake(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))

	cases := []corpusCase{
		{
			name:   "base_keep_default",
			script: `keep;`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Kind != ActionKeep {
					t.Fatalf("expected keep; got %+v", out.Actions)
				}
			},
		},
		{
			name:   "fileinto_subject",
			script: `require "fileinto"; if header :contains "Subject" "Q4" { fileinto "Reports"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Reports" {
					t.Fatalf("fileinto: %+v", out.Actions)
				}
			},
		},
		{
			name:   "fileinto_noop_when_not_matching",
			script: `require "fileinto"; if header :contains "Subject" "NotMatching" { fileinto "Reports"; }`,
			check: func(t *testing.T, out Outcome) {
				if !out.ImplicitKeep {
					t.Fatalf("expected implicit keep; got %+v", out)
				}
			},
		},
		{
			name:   "subaddress_detail",
			script: `require ["subaddress","fileinto"]; if address :detail "To" "sales" { fileinto "Sales"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Sales" {
					t.Fatalf("detail match: %+v", out.Actions)
				}
			},
		},
		{
			name:   "subaddress_user",
			script: `require ["subaddress","fileinto"]; if address :user "To" "bob" { fileinto "Bob"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Bob" {
					t.Fatalf("user match: %+v", out.Actions)
				}
			},
		},
		{
			name:   "relational_count_eq_two_recipients",
			script: `require ["relational","comparator-i;ascii-numeric","fileinto"]; if address :count "eq" :comparator "i;ascii-numeric" ["To","Cc"] "2" { fileinto "Twofer"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Twofer" {
					t.Fatalf("count: %+v", out.Actions)
				}
			},
		},
		{
			name:   "regex_match_subject",
			script: `require ["regex","fileinto"]; if header :regex "Subject" "Q[0-9]+" { fileinto "R"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "R" {
					t.Fatalf("regex: %+v", out.Actions)
				}
			},
		},
		{
			name:   "body_contains_url",
			script: `require ["body","fileinto"]; if body :contains "reports.example.com" { fileinto "Links"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Links" {
					t.Fatalf("body: %+v", out.Actions)
				}
			},
		},
		{
			name:   "variables_and_string_test",
			script: `require ["variables","fileinto"]; set "folder" "Mine"; if string :is "${folder}" "Mine" { fileinto "${folder}"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Mine" {
					t.Fatalf("variables: %+v", out.Actions)
				}
			},
		},
		{
			name:   "date_currentdate",
			script: `require ["date","fileinto","relational","comparator-i;ascii-numeric"]; if currentdate :value "eq" :comparator "i;ascii-numeric" "year" "2024" { fileinto "Yr"; }`,
			env:    Environment{Clock: clk, Now: clk.Now()},
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "Yr" {
					t.Fatalf("currentdate: %+v", out.Actions)
				}
			},
		},
		{
			name:   "mailboxid_override",
			script: `require ["fileinto","mailboxid"]; fileinto :mailboxid "mb-123" "INBOX";`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].MailboxID != "mb-123" {
					t.Fatalf("mailboxid: %+v", out.Actions)
				}
			},
		},
		{
			name:   "editheader_add",
			script: `require "editheader"; addheader "X-Sieve" "passed";`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].HeaderName != "X-Sieve" {
					t.Fatalf("editheader: %+v", out.Actions)
				}
			},
		},
		{
			name:   "enotify_mailto",
			script: `require "enotify"; notify :message "New mail" "mailto:admin@example.com";`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Kind != ActionNotify {
					t.Fatalf("notify: %+v", out.Actions)
				}
			},
		},
		{
			name:   "imap4flags_add_remove",
			script: `require "imap4flags"; addflag "\\Seen"; removeflag "\\Flagged";`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 2 {
					t.Fatalf("flags: %+v", out.Actions)
				}
			},
		},
		{
			name:   "if_elsif_else_chain",
			script: `require "fileinto"; if header :contains "Subject" "NotThere" { fileinto "A"; } elsif header :contains "Subject" "Q4" { fileinto "B"; } else { fileinto "C"; }`,
			check: func(t *testing.T, out Outcome) {
				if len(out.Actions) != 1 || out.Actions[0].Mailbox != "B" {
					t.Fatalf("chain: %+v", out.Actions)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := runScript(t, c.script, c.env, msg)
			c.check(t, out)
		})
	}
}
