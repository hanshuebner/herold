package contacts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap/contacts"
)

// canonicalize re-serialises a JSON value through map[string]any so
// the result is byte-identical regardless of map iteration order. Used
// by round-trip assertions.
func canonicalize(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("canonicalize: %v: %s", err, b)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonicalize re-marshal: %v", err)
	}
	return out
}

func TestJSContact_RoundTrip_AllStandardFields(t *testing.T) {
	// Fully-populated Card — typed fields plus an unknown-to-us key
	// ("nicknames") that must flow through RawJSON unchanged.
	in := []byte(`{
		"version":"1.0",
		"kind":"individual",
		"uid":"urn:uuid:abc",
		"name":{"full":"Ada Lovelace","components":[{"kind":"given","value":"Ada"},{"kind":"surname","value":"Lovelace"}]},
		"emails":{"e1":{"address":"ada@example.test","pref":1}},
		"phones":{"p1":{"number":"+44 20 7946 0958"}},
		"addresses":{"a1":{"full":"London"}},
		"organizations":{"o1":{"name":"Analytical Engine Co."}},
		"titles":{"t1":{"name":"Mathematician"}},
		"nicknames":{"n1":{"name":"Countess"}}
	}`)
	var c contacts.Card
	if err := c.UnmarshalJSON(in); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(canonicalize(t, in)) != string(canonicalize(t, out)) {
		t.Fatalf("round-trip differs:\n in: %s\nout: %s", canonicalize(t, in), canonicalize(t, out))
	}
}

func TestJSContact_PrimaryEmail_PicksPrimaryFlag(t *testing.T) {
	in := []byte(`{
		"version":"1.0","uid":"urn:uuid:1",
		"emails":{
			"a":{"address":"work@example.test","pref":2},
			"b":{"address":"primary@example.test","pref":1},
			"c":{"address":"alt@example.test"}
		}
	}`)
	var c contacts.Card
	if err := c.UnmarshalJSON(in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := c.PrimaryEmail(); got != "primary@example.test" {
		t.Fatalf("PrimaryEmail = %q, want primary@example.test", got)
	}

	// No pref flags anywhere -> first key in sorted order wins.
	in2 := []byte(`{
		"version":"1.0","uid":"urn:uuid:2",
		"emails":{
			"b":{"address":"second@example.test"},
			"a":{"address":"first@example.test"}
		}
	}`)
	var c2 contacts.Card
	if err := c2.UnmarshalJSON(in2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := c2.PrimaryEmail(); got != "first@example.test" {
		t.Fatalf("PrimaryEmail (no pref) = %q, want first@example.test", got)
	}
}

func TestJSContact_Validate_RejectsBadVersion(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "wrong version",
			body:    `{"version":"2.0","uid":"urn:uuid:1"}`,
			wantErr: "version",
		},
		{
			name:    "missing version",
			body:    `{"uid":"urn:uuid:1"}`,
			wantErr: "version",
		},
		{
			name:    "missing uid",
			body:    `{"version":"1.0"}`,
			wantErr: "uid",
		},
		{
			name:    "bad kind",
			body:    `{"version":"1.0","uid":"urn:uuid:1","kind":"bogus"}`,
			wantErr: "kind",
		},
		{
			name:    "valid",
			body:    `{"version":"1.0","uid":"urn:uuid:1","kind":"group"}`,
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c contacts.Card
			if err := c.UnmarshalJSON([]byte(tc.body)); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate: want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate error = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

func TestJSContact_DisplayName_FallbackOrder(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "full wins",
			body: `{"version":"1.0","uid":"u","name":{"full":"Ada Lovelace","components":[{"kind":"given","value":"Ada"}]},"emails":{"e":{"address":"a@b"}}}`,
			want: "Ada Lovelace",
		},
		{
			name: "components when full empty",
			body: `{"version":"1.0","uid":"u","name":{"components":[{"kind":"given","value":"Ada"},{"kind":"surname","value":"Lovelace"}]}}`,
			want: "Ada Lovelace",
		},
		{
			name: "components surname only",
			body: `{"version":"1.0","uid":"u","name":{"components":[{"kind":"surname","value":"Lovelace"}]}}`,
			want: "Lovelace",
		},
		{
			name: "org when no name",
			body: `{"version":"1.0","uid":"u","organizations":{"o":{"name":"Analytical Engine"}}}`,
			want: "Analytical Engine",
		},
		{
			name: "primary email when nothing else",
			body: `{"version":"1.0","uid":"u","emails":{"e":{"address":"ada@example.test","pref":1}}}`,
			want: "ada@example.test",
		},
		{
			name: "empty when nothing populated",
			body: `{"version":"1.0","uid":"u"}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c contacts.Card
			if err := c.UnmarshalJSON([]byte(tc.body)); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := c.DisplayName(); got != tc.want {
				t.Fatalf("DisplayName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJSContact_SearchBlob_PopulatesFromTypedFields(t *testing.T) {
	in := []byte(`{
		"version":"1.0","uid":"u-search",
		"name":{"full":"Ada Lovelace","components":[{"kind":"given","value":"Ada"},{"kind":"surname","value":"Lovelace"}]},
		"emails":{"e":{"address":"ada@example.test"}},
		"organizations":{"o":{"name":"AnaEng"}}
	}`)
	var c contacts.Card
	if err := c.UnmarshalJSON(in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	blob := c.SearchBlob()
	for _, want := range []string{"ada lovelace", "ada", "lovelace", "ada@example.test", "anaeng", "u-search"} {
		if !strings.Contains(blob, want) {
			t.Errorf("SearchBlob missing %q: %q", want, blob)
		}
	}
}
