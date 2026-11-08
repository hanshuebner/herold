package email

// bodybuild_linelen_test.go -- every line of a composed outbound message
// stays within RFC 5322 section 2.1.1 (998 octets, 78 recommended) and
// base64 bodies wrap at 76 columns (RFC 2045 section 6.8). A receiving MX
// rejects an over-long line with "501 Syntax error - line too long" (#557).

import (
	"bytes"
	"context"
	"fmt"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/jhillyerd/enmime"

	"github.com/hanshuebner/herold/internal/store"
)

func TestBuildEmail_WrappedAttachmentDecodesLossless(t *testing.T) {
	raw := buildLongShape(t)
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(env.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(env.Attachments))
	}
	got := env.Attachments[0].Content
	if len(got) != 1_570_978 {
		t.Fatalf("decoded attachment is %d bytes, want 1570978", len(got))
	}
	for i := range got {
		if got[i] != byte(i*131+i>>8) {
			t.Fatalf("attachment byte %d differs", i)
		}
	}
}

type memBlobReader struct{ *bytes.Reader }

func (memBlobReader) Close() error { return nil }

type memBlobs struct {
	store.Blobs
	data map[string][]byte
}

func (m memBlobs) Get(_ context.Context, hash string) (store.BlobReader, error) {
	b, ok := m.data[hash]
	if !ok {
		return nil, store.ErrNotFound
	}
	return memBlobReader{bytes.NewReader(b)}, nil
}

func maxLine(raw []byte) (int, int) {
	max, at := 0, 0
	for i, l := range strings.Split(string(raw), "\r\n") {
		if len(l) > max {
			max, at = len(l), i
		}
	}
	return max, at
}

func buildLongShape(t *testing.T) []byte {
	t.Helper()
	att := make([]byte, 1_570_978)
	for i := range att {
		att[i] = byte(i*131 + i>>8)
	}
	blobs := memBlobs{data: map[string][]byte{"att1": att}}
	refs := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		refs = append(refs, fmt.Sprintf("<ref-%02d-0123456789abcdef0123456789abcdef@mail.example.test>", i))
	}
	var to []emailAddress
	for i := 0; i < 40; i++ {
		to = append(to, emailAddress{Name: fmt.Sprintf("Recipient Number %d", i), Email: fmt.Sprintf("rcpt%d@example.test", i)})
	}
	subj := strings.Repeat("Ein sehr langer Betreff mit Umlauten aeoeue äöü ", 8)
	in := emailCreateInput{
		From:       []emailAddress{{Email: "me@example.test"}},
		To:         to,
		Subject:    &subj,
		InReplyTo:  []string{refs[len(refs)-1]},
		References: refs,
		BodyValues: map[string]emailBodyValue{"1": {Value: strings.Repeat("word ", 400)}},
		TextBody:   []emailBodyPart{{PartID: "1", Type: "text/plain"}},
		attachmentParts: []emailBodyStructurePart{
			{Type: "image/png", BlobID: "att1", Name: "shot.png", Disposition: "attachment"},
		},
	}
	raw, err := buildEmailFromProperties(context.Background(), nil, blobs, in, time.Unix(1794121361, 0), "mail.example.test")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return raw
}

func TestBuildEmail_LineLengthWithinRFC5322(t *testing.T) {
	raw := buildLongShape(t)
	if max, at := maxLine(raw); max > 998 {
		t.Fatalf("line %d is %d octets, exceeds 998", at, max)
	}
}

func TestBuildEmail_Base64WrappedAt76(t *testing.T) {
	raw := buildLongShape(t)
	_, body, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	inB64, seen := false, false
	for _, l := range strings.Split(string(body), "\r\n") {
		switch {
		case strings.HasPrefix(l, "Content-Transfer-Encoding: base64"):
			inB64, seen = true, true
		case strings.HasPrefix(l, "--"):
			inB64 = false
		case inB64 && len(l) > 76:
			t.Fatalf("base64 line of %d octets, want <= 76", len(l))
		}
	}
	if !seen {
		t.Fatal("no base64 part found")
	}
}

func TestBuildEmail_LongHeadersFoldAndUnfoldLossless(t *testing.T) {
	raw := buildLongShape(t)
	head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
	for _, l := range strings.Split(string(head), "\r\n") {
		if len(l) > 78 && strings.ContainsAny(strings.TrimSpace(l), " \t") {
			t.Fatalf("header line of %d octets is foldable but not folded: %.12q", len(l), l)
		}
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	refs := strings.Fields(msg.Header.Get("References"))
	if len(refs) != 60 {
		t.Fatalf("References has %d ids after unfolding, want 60", len(refs))
	}
	if to, err := msg.Header.AddressList("To"); err != nil || len(to) != 40 {
		t.Fatalf("To parsed %d addrs err=%v, want 40", len(to), err)
	}
}
