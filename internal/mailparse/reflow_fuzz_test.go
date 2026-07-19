package mailparse

import "testing"

// FuzzReflowFormatFlowed exercises the RFC 3676 reflow algorithm.
// ReflowFormatFlowed is a wire-facing parser (it interprets sender-controlled
// Content-Type: text/plain; format=flowed bodies), so per STANDARDS sec 8.2
// it ships a fuzz target; the invariant under fuzzing is simply "never
// panics", since the function's output is unstructured prose with no
// re-parseable grammar of its own to round-trip.
func FuzzReflowFormatFlowed(f *testing.F) {
	seeds := []struct {
		body  string
		delSp bool
	}{
		{"", false},
		{"\n", false},
		{"one \ntwo\n", false},
		{"one \ntwo\n", true},
		{"one \r\ntwo\r\n", false},
		{"one \ntwo", false},
		{"> quoted line one \n> quoted line two\nunquoted reply\n", false},
		{">> deep \n> shallow\n", false},
		{" stuffed line\n", false},
		{" From the report\n", false},
		{"Regards,\n-- \nAlice\n", false},
		{"-- \n", true},
		{"> -- \n", false},
		{">>>>>>>>>>\n", false},
		{"   \n", true},
		{"\r\r\r", false},
		{"a \n\n\nb \n", true},
	}
	for _, s := range seeds {
		f.Add(s.body, s.delSp)
	}
	f.Fuzz(func(t *testing.T, body string, delSp bool) {
		out := ReflowFormatFlowed(body, delSp)
		// No invariant beyond "does not panic" is required (see doc
		// comment), but a trivially checkable one: an empty input
		// always yields an empty output.
		if body == "" && out != "" {
			t.Fatalf("ReflowFormatFlowed(\"\", %v) = %q, want \"\"", delSp, out)
		}
	})
}
