package mailhdr

import (
	"math/rand"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"unicode/utf8"
)

func unfold(s string) string { return strings.ReplaceAll(s, "\r\n", "") }

// TestFold_Property: for random values built from tokens, runs of spaces
// and tabs, every physical line stays within 998 octets, no continuation
// line is whitespace-only, and unfolding restores the input.
func TestFold_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(557))
	for iter := 0; iter < 3000; iter++ {
		var v strings.Builder
		n := rng.Intn(40)
		for i := 0; i < n; i++ {
			switch rng.Intn(4) {
			case 0:
				v.WriteString(strings.Repeat(" ", 1+rng.Intn(5)))
			case 1:
				v.WriteString("\t")
			default:
				l := 1 + rng.Intn(20)
				if rng.Intn(15) == 0 {
					l = 70 + rng.Intn(400) // over-long token
				}
				for j := 0; j < l; j++ {
					v.WriteByte(byte('a' + rng.Intn(26)))
				}
				v.WriteByte(' ')
			}
		}
		value := v.String()
		out := Fold("X-Test", value)
		if !strings.HasSuffix(out, "\r\n") {
			t.Fatalf("no trailing CRLF: %q", out)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
		for i, l := range lines {
			if len(l) > 998 {
				t.Fatalf("iter %d: line %d is %d octets", iter, i, len(l))
			}
			if i > 0 && strings.TrimLeft(l, " \t") == "" {
				t.Fatalf("iter %d: whitespace-only continuation line %q in %q", iter, l, out)
			}
		}
		if got, want := unfold(out), "X-Test: "+value; got != want {
			t.Fatalf("iter %d: unfold mismatch\n got %q\nwant %q", iter, got, want)
		}
	}
}

func TestFold_SpaceRunBeforeLongToken(t *testing.T) {
	long := strings.Repeat("x", 90)
	out := Fold("References", "<a@b>    "+long)
	for i, l := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if i > 0 && strings.TrimSpace(l) == "" {
			t.Fatalf("whitespace-only line in %q", out)
		}
	}
	if unfold(out) != "References: <a@b>    "+long {
		t.Fatalf("unfold mismatch: %q", out)
	}
}

// TestEncodeWords_LongNonASCIISubject: a long non-ASCII subject becomes
// encoded-words of at most 75 characters, every line stays within 78
// octets, and decoding restores the original.
func TestEncodeWords_LongNonASCIISubject(t *testing.T) {
	subj := strings.Repeat("Grüße aus München 日本語 ", 30)
	enc := EncodeWords(subj)
	out := Fold("Subject", enc)
	for _, l := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(l) > 78 {
			t.Fatalf("line of %d octets: %q", len(l), l)
		}
		for _, w := range strings.Fields(l) {
			if strings.HasPrefix(w, "=?") && len(w) > 75 {
				t.Fatalf("encoded-word of %d chars: %q", len(w), w)
			}
		}
	}
	msg, err := mail.ReadMessage(strings.NewReader(out + "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if dec != subj {
		t.Fatalf("subject did not round-trip: %q", dec)
	}
}

func TestEncodeWords_ASCIIUnchanged(t *testing.T) {
	if got := EncodeWords("plain subject"); got != "plain subject" {
		t.Fatalf("got %q", got)
	}
}

func TestExtendedParam_ContinuationsRoundTrip(t *testing.T) {
	name := strings.Repeat("Dateiname äöü 日本語 ", 40)
	p := ExtendedParam("filename", name)
	if !strings.Contains(p, "filename*0*=utf-8''") || !strings.Contains(p, "filename*1*=") {
		t.Fatalf("expected continuations: %q", p)
	}
	hdr := Fold("Content-Disposition", "attachment; "+p)
	for _, l := range strings.Split(strings.TrimSuffix(hdr, "\r\n"), "\r\n") {
		if len(l) > 998 {
			t.Fatalf("line of %d octets", len(l))
		}
	}
	_, params, err := mime.ParseMediaType(unfoldKeepSpace(hdr)[len("Content-Disposition: "):])
	if err != nil {
		t.Fatal(err)
	}
	if params["filename"] != name {
		t.Fatalf("filename did not round-trip: %q", params["filename"])
	}
	if !utf8.ValidString(params["filename"]) {
		t.Fatal("invalid utf-8")
	}
}

func unfoldKeepSpace(s string) string { return strings.ReplaceAll(s, "\r\n", "") }
