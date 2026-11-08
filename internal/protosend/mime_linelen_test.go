package protosend

import (
	"bytes"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// TestBuildStructuredMessage_LineLengths: a long non-ASCII subject becomes
// encoded-words of at most 75 characters, a long recipient list folds, and
// no line exceeds 998 octets (headers 78 where foldable).
func TestBuildStructuredMessage_LineLengths(t *testing.T) {
	subj := strings.Repeat("Gr\u00fc\u00dfe aus M\u00fcnchen \u65e5\u672c\u8a9e ", 30)
	var to []string
	for i := 0; i < 40; i++ {
		to = append(to, fmt.Sprintf("recipient%d@example.test", i))
	}
	req := sendRequest{
		Source:      "me@example.test",
		Destination: destination{ToAddresses: to, CCAddresses: to},
		Message:     messageBody{Subject: subj, Body: body{Text: strings.Repeat("word ", 400)}},
	}
	built, err := buildStructuredMessage(req, "mail.example.test", time.Unix(1794121361, 0))
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := bytes.Cut(built.bytes, []byte("\r\n\r\n"))
	for _, l := range strings.Split(string(head), "\r\n") {
		if len(l) > 78 {
			t.Fatalf("header line of %d octets: %.20q", len(l), l)
		}
		for _, w := range strings.Fields(l) {
			if strings.HasPrefix(w, "=?") && len(w) > 75 {
				t.Fatalf("encoded-word of %d chars", len(w))
			}
		}
	}
	for _, l := range bytes.Split(built.bytes, []byte("\r\n")) {
		if len(l) > 998 {
			t.Fatalf("line of %d octets", len(l))
		}
	}
	msg, err := mail.ReadMessage(bytes.NewReader(built.bytes))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || dec != subj {
		t.Fatalf("subject did not round-trip (err=%v)", err)
	}
	if addrs, err := msg.Header.AddressList("To"); err != nil || len(addrs) != 40 {
		t.Fatalf("To: %d addrs, err=%v", len(addrs), err)
	}
}
