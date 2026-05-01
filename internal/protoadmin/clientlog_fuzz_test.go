package protoadmin

// FuzzClientlogJSONParser is the fuzz target for the client-log JSON parser
// (REQ-NFR-73). It feeds arbitrary bytes as the HTTP body to both the auth
// (full schema) and public (narrow schema) decoding paths and asserts that
// the parser never panics and produces a deterministic drop-or-accept result.
//
// The fuzz target lives in the internal (non-_test) package so it can reach
// the unexported wireEvent / wireNarrowEvent / wireRequest types directly
// without an export shim.

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzClientlogJSONParser exercises the JSON parsing paths that both handlers
// use. For each input it:
//  1. Attempts to decode a wireRequest (the wrapper).
//  2. For each raw event in the wrapper, attempts decode as wireEvent (full)
//     and wireNarrowEvent with DisallowUnknownFields (narrow/strict).
//  3. Calls clientlogValidKind / clientlogValidApp on any decoded event.
//  4. Calls capString and redactClientText on any decoded msg / stack.
//
// The only invariant enforced is "no panic". No assertion about the
// drop-or-accept decision is made because arbitrary byte sequences are
// intentionally malformed.
func FuzzClientlogJSONParser(f *testing.F) {
	// Seed corpus: valid auth event.
	f.Add([]byte(`{"events":[{"v":1,"kind":"error","level":"error","msg":"oops",` +
		`"client_ts":"2026-01-01T00:00:00Z","seq":1,"page_id":"p","app":"suite",` +
		`"build_sha":"abc","route":"/","ua":"ua"}]}`))

	// Seed corpus: valid narrow/public event.
	f.Add([]byte(`{"events":[{"v":1,"kind":"log","level":"info","msg":"hi",` +
		`"client_ts":"2026-01-01T00:00:00Z","seq":2,"page_id":"q","app":"admin",` +
		`"build_sha":"def","route":"/admin","ua":"bot"}]}`))

	// Seed corpus: event with breadcrumbs.
	f.Add([]byte(`{"events":[{"v":1,"kind":"error","level":"error","msg":"crash",` +
		`"client_ts":"2026-01-01T00:00:00Z","seq":3,"page_id":"r","app":"suite",` +
		`"build_sha":"ghi","route":"/settings","ua":"chrome",` +
		`"breadcrumbs":[{"ts":"2026-01-01T00:00:00Z","kind":"navigation","route":"/"}]}]}`))

	// Seed corpus: empty events array.
	f.Add([]byte(`{"events":[]}`))

	// Seed corpus: malformed JSON.
	f.Add([]byte(`{`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`[]`))

	// Seed corpus: oversized msg.
	f.Add([]byte(`{"events":[{"v":1,"kind":"log","level":"info",` +
		`"msg":"` + strings.Repeat("A", 6000) + `",` +
		`"client_ts":"2026-01-01T00:00:00Z","seq":1,"page_id":"p","app":"suite",` +
		`"build_sha":"abc","route":"/","ua":"ua"}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// --- wrapper decode ---
		var req wireRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return // malformed JSON: drop-or-accept = drop; no panic = pass
		}

		for _, raw := range req.Events {
			// --- full schema (auth endpoint) ---
			var ev wireEvent
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			if err := dec.Decode(&ev); err != nil {
				// schema mismatch -> drop; no panic = pass
				continue
			}

			// Validate kind and app.
			_ = clientlogValidKind(ev.Kind)
			_ = clientlogValidApp(ev.App)

			// Cap and redact fields as the pipeline would.
			_ = capString(ev.Msg, clientlogMaxMsgAuth)
			_ = capString(ev.Stack, clientlogMaxStackAuth)
			_ = redactClientText(ev.Msg)
			_ = redactClientText(ev.Stack)

			// Breadcrumb allowlist (auth endpoint allows up to 32 breadcrumbs).
			if len(ev.Breadcrumbs) > 0 {
				_, _ = validateBreadcrumbs(ev.Breadcrumbs, clientlogMaxBreadcrumbsAuth)
			}

			// --- narrow schema (public endpoint, strict) ---
			var narrow wireNarrowEvent
			dec2 := json.NewDecoder(strings.NewReader(string(raw)))
			dec2.DisallowUnknownFields()
			if err := dec2.Decode(&narrow); err != nil {
				// extra field or type mismatch -> strict reject; no panic = pass
				continue
			}

			_ = clientlogValidKind(narrow.Kind)
			_ = clientlogValidApp(narrow.App)
			_ = capString(narrow.Msg, clientlogMaxMsgPublic)
			_ = capString(narrow.Stack, clientlogMaxStackPublic)
			_ = redactClientText(narrow.Msg)
		}
	})
}
