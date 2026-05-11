package taggedaddr_test

import (
	"testing"

	"github.com/hanshuebner/herold/internal/taggedaddr"
)

// TestSplit covers REQ-TAG-01..03: the `+` separator, lower-case
// normalisation of suffix and domain (REQ-TAG-02), and the empty-suffix
// edge case (REQ-TAG-03). The synthetic shape `alice++amazon@example`
// is included because REQ-TAG-01 explicitly says "Suffix bytes after
// the first `+` are taken verbatim — additional `+` characters are
// part of the suffix string, not separators."
func TestSplit(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantLocal string
		wantSfx   string
		wantBase  string
	}{
		{
			name:      "simple plain address has no suffix",
			in:        "alice@example.test",
			wantLocal: "alice",
			wantSfx:   "",
			wantBase:  "alice@example.test",
		},
		{
			name:      "simple suffix is extracted and lower-cased",
			in:        "alice+amazon@example.test",
			wantLocal: "alice",
			wantSfx:   "amazon",
			wantBase:  "alice@example.test",
		},
		{
			name:      "REQ-TAG-02 case-insensitive: mixed-case suffix lower-cases",
			in:        "Alice+Amazon@Example.TEST",
			wantLocal: "Alice",
			wantSfx:   "amazon",
			wantBase:  "alice@example.test",
		},
		{
			name:      "REQ-TAG-01 multiple plus: only the FIRST + splits, the rest are part of suffix",
			in:        "bob+a+b+c@example.test",
			wantLocal: "bob",
			wantSfx:   "a+b+c",
			wantBase:  "bob@example.test",
		},
		{
			name:      "REQ-TAG-01 multiple plus with case: suffix lower-cased verbatim",
			in:        "bob+Foo+Bar@example.test",
			wantLocal: "bob",
			wantSfx:   "foo+bar",
			wantBase:  "bob@example.test",
		},
		{
			name:      "REQ-TAG-03 empty suffix is treated as no suffix",
			in:        "alice+@example.test",
			wantLocal: "alice",
			wantSfx:   "",
			wantBase:  "alice@example.test",
		},
		{
			name:      "leading plus: empty base local-part with a non-empty suffix is still extracted",
			in:        "+lonely@example.test",
			wantLocal: "",
			wantSfx:   "lonely",
			wantBase:  "@example.test",
		},
		{
			name:      "no at-sign: malformed - empty triple",
			in:        "alice",
			wantLocal: "",
			wantSfx:   "",
			wantBase:  "",
		},
		{
			name:      "leading at-sign: malformed",
			in:        "@example.test",
			wantLocal: "",
			wantSfx:   "",
			wantBase:  "",
		},
		{
			name:      "trailing at-sign: malformed",
			in:        "alice@",
			wantLocal: "",
			wantSfx:   "",
			wantBase:  "",
		},
		{
			name:      "REQ-TAG-01 multi-@ uses LAST as boundary",
			in:        "alice+tag@subdomain@example.test",
			wantLocal: "alice",
			wantSfx:   "tag@subdomain",
			wantBase:  "alice@example.test",
		},
		{
			name:      "REQ-TAG-02 suffix with digits and dashes is preserved",
			in:        "alice+shop-123@example.test",
			wantLocal: "alice",
			wantSfx:   "shop-123",
			wantBase:  "alice@example.test",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotLocal, gotSfx, gotBase := taggedaddr.Split(tc.in)
			if gotLocal != tc.wantLocal {
				t.Errorf("baseLocal = %q, want %q", gotLocal, tc.wantLocal)
			}
			if gotSfx != tc.wantSfx {
				t.Errorf("suffix = %q, want %q", gotSfx, tc.wantSfx)
			}
			if gotBase != tc.wantBase {
				t.Errorf("baseEmail = %q, want %q", gotBase, tc.wantBase)
			}
		})
	}
}
