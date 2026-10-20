package email

import (
	"encoding/json"
	"testing"
)

// FuzzHeaderPropertyValue feeds arbitrary header sections through the
// header field splitter and every header form: none may panic, and each
// value must be valid JSON.
func FuzzHeaderPropertyValue(f *testing.F) {
	f.Add([]byte("To: Friends: b@example.test, c@example.test;, d@example.test\r\n\r\n"))
	f.Add([]byte("Subject: =?UTF-8?Q?gr=C3=BC=C3=9F?=\r\n folded\r\nX: a\nX: b\n\nbody"))
	f.Add([]byte("List-Unsubscribe: <mailto:u@example.test>, (c <x>) \"<q>\" <https://e/u>\r\n"))
	f.Add([]byte("To: \"a:b\" <x@y>, g: ;, (comment: ;) h:\r\n"))
	forms := []string{"", ":asRaw", ":asText", ":asAddresses", ":asGroupedAddresses",
		":asMessageIds", ":asDate", ":asURLs"}
	f.Fuzz(func(t *testing.T, raw []byte) {
		fields := rawHeaderFields(raw)
		names := []string{"To", "Subject", "X", "List-Unsubscribe"}
		for _, fl := range fields {
			names = append(names, fl.Name)
		}
		for _, name := range names {
			for _, form := range forms {
				for _, all := range []string{"", ":all"} {
					v := headerPropertyValue(fields, "header:"+name+form+all)
					if !json.Valid(v) {
						t.Fatalf("header:%s%s%s = %q, not JSON", name, form, all, v)
					}
				}
			}
		}
	})
}
