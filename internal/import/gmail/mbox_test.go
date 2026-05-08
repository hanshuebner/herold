package gmail

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestMboxReaderTwoMessages(t *testing.T) {
	const data = `From sender@example.com Wed May 06 18:21:37 +0000 2026
From: a@example.com
To: b@example.com
Subject: first
Message-ID: <msg-1@example.com>

body of first
>From this line is mboxrd-quoted
multiple lines
From sender@example.com Thu May 07 12:42:31 +0000 2026
From: c@example.com
To: d@example.com
Subject: second
Message-ID: <msg-2@example.com>

body of second
`

	r := NewMboxReader(strings.NewReader(data))
	first, err := r.Next()
	if err != nil {
		t.Fatalf("Next #1: %v", err)
	}
	if !strings.Contains(first.FromLine, "sender@example.com") {
		t.Errorf("Next #1 FromLine = %q", first.FromLine)
	}
	if !strings.Contains(string(first.Body), "Subject: first") {
		t.Errorf("Next #1 missing first subject; body=%q", first.Body)
	}
	if !strings.Contains(string(first.Body), "From this line is mboxrd-quoted") {
		t.Errorf("Next #1 mboxrd unescape missing; body=%q", first.Body)
	}
	if strings.Contains(string(first.Body), ">From this line") {
		t.Errorf("Next #1 still has the >From quoted form; body=%q", first.Body)
	}

	second, err := r.Next()
	if err != nil {
		t.Fatalf("Next #2: %v", err)
	}
	if !strings.Contains(string(second.Body), "Subject: second") {
		t.Errorf("Next #2 missing second subject; body=%q", second.Body)
	}

	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next #3: want io.EOF, got %v", err)
	}
}

func TestMboxReaderEmptyInput(t *testing.T) {
	r := NewMboxReader(strings.NewReader(""))
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty mbox: want io.EOF, got %v", err)
	}
}

func TestMboxReaderRejectsMalformedFromLine(t *testing.T) {
	r := NewMboxReader(strings.NewReader("not-a-from-line\nbody\n"))
	if _, err := r.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("malformed input: want non-EOF error, got %v", err)
	}
}

func TestMboxReaderHandlesDoubleQuotedFrom(t *testing.T) {
	const data = `From x Wed May 06 18:21:37 +0000 2026
Subject: q

>>From doubled-quote
>>>From triple
end
`
	r := NewMboxReader(strings.NewReader(data))
	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	body := string(msg.Body)
	if !strings.Contains(body, ">From doubled-quote") {
		t.Errorf("double quote unescape: body=%q", body)
	}
	if !strings.Contains(body, ">>From triple") {
		t.Errorf("triple quote unescape: body=%q", body)
	}
}

func TestMboxReaderLineAtEOFWithoutNewline(t *testing.T) {
	const data = "From a Wed May 06 18:21:37 +0000 2026\nSubject: t\n\nbody no trailing newline"
	r := NewMboxReader(strings.NewReader(data))
	msg, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !strings.HasSuffix(string(msg.Body), "body no trailing newline") {
		t.Errorf("body suffix mismatch: %q", msg.Body)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after last: want io.EOF, got %v", err)
	}
}

func TestIsQuotedFrom(t *testing.T) {
	cases := map[string]bool{
		">From foo":   true,
		">>From foo":  true,
		">>>From foo": true,
		"From foo":    false,
		">notfrom":    false,
		"":            false,
	}
	for in, want := range cases {
		if got := isQuotedFrom(in); got != want {
			t.Errorf("isQuotedFrom(%q) = %v, want %v", in, got, want)
		}
	}
}
