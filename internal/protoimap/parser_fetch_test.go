package protoimap

import (
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"
)

// TestParseFetch_SingleItemBodyPeek covers the unparenthesised single-item
// FETCH path. RFC 9051 §6.4.6 allows `FETCH 1 BODY.PEEK[]` and
// `FETCH 1 BODY[]` without the surrounding parens; the earlier parser
// only handled the macro / no-bracket case there and rejected mutt's
// `FETCH 1 BODY.PEEK[]` body fetch with "unknown fetch item BODY.PEEK".
func TestParseFetch_SingleItemBodyPeek(t *testing.T) {
	cases := []struct {
		name string
		line string
		want func(t *testing.T, opts *imap.FetchOptions)
	}{
		{
			name: "BODY.PEEK_empty_section",
			line: "BODY.PEEK[]",
			want: func(t *testing.T, opts *imap.FetchOptions) {
				if len(opts.BodySection) != 1 {
					t.Fatalf("want 1 body section, got %d", len(opts.BodySection))
				}
				if !opts.BodySection[0].Peek {
					t.Errorf("want Peek=true")
				}
			},
		},
		{
			name: "BODY.PEEK_HEADER",
			line: "BODY.PEEK[HEADER]",
			want: func(t *testing.T, opts *imap.FetchOptions) {
				if len(opts.BodySection) != 1 {
					t.Fatalf("want 1 body section, got %d", len(opts.BodySection))
				}
				if !opts.BodySection[0].Peek {
					t.Errorf("want Peek=true")
				}
				if opts.BodySection[0].Specifier != imap.PartSpecifierHeader {
					t.Errorf("want HEADER specifier, got %v", opts.BodySection[0].Specifier)
				}
			},
		},
		{
			name: "BODY_empty_section",
			line: "BODY[]",
			want: func(t *testing.T, opts *imap.FetchOptions) {
				if len(opts.BodySection) != 1 {
					t.Fatalf("want 1 body section, got %d", len(opts.BodySection))
				}
				if opts.BodySection[0].Peek {
					t.Errorf("want Peek=false for BODY[]")
				}
			},
		},
		{
			name: "macro_ALL_still_works",
			line: "ALL",
			want: func(t *testing.T, opts *imap.FetchOptions) {
				if !opts.Flags || !opts.InternalDate || !opts.RFC822Size || !opts.Envelope {
					t.Errorf("ALL macro did not populate all expected flags: %+v", opts)
				}
			},
		},
		{
			name: "single_FLAGS_still_works",
			line: "FLAGS",
			want: func(t *testing.T, opts *imap.FetchOptions) {
				if !opts.Flags {
					t.Errorf("FLAGS not set")
				}
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &Command{Raw: "A1 FETCH 1 " + tt.line, IsUID: false}
			p := &parser{src: []byte("1 " + tt.line)}
			if err := parseFetch(p, cmd); err != nil {
				t.Fatalf("parseFetch(%q): %v", tt.line, err)
			}
			tt.want(t, cmd.FetchOptions)
		})
	}
}

// TestParseFetch_RejectsTrulyUnknownItems verifies the parser still
// rejects nonsense fetch items rather than silently accepting them.
func TestParseFetch_RejectsTrulyUnknownItems(t *testing.T) {
	cmd := &Command{}
	p := &parser{src: []byte("1 NOTAREALFETCHITEM")}
	if err := parseFetch(p, cmd); err == nil {
		t.Fatal("expected parse error for unknown fetch item")
	} else if !strings.Contains(err.Error(), "unknown fetch item") {
		t.Errorf("err=%q, want \"unknown fetch item\"", err)
	}
}
