package store

// FuzzChatAttachmentHashes drives store.ChatAttachmentHashes — the
// JSON parser introduced by Wave 2.9.7 Track B that the SQLite and
// Postgres metadata backends use to decide which blob_refs.ref_count
// rows to bump on a chat-message insert and to clear on a hard-
// delete. The bytes arrive from chat_messages.attachments_json which,
// in turn, originates with the JMAP client — i.e. arbitrary user
// input. A panic here would crash whichever metadata-store call is
// holding the txn (fan-out: SetMessage, HardDelete, Retention worker)
// and stall all chat I/O for the affected principal until the worker
// restarts; STANDARDS §8.2 therefore requires a fuzz target.
//
// Asserted invariants:
//
//  1. ChatAttachmentHashes never panics on any input.
//  2. On a non-error return, every entry has a non-empty Hash field
//     (the loop in metadata.go assumes that to drive INSERT OR
//     UPDATE blob_refs by primary key).
//  3. The returned slice is duplicate-free on Hash. Wave 2.9.7's
//     contract is "first occurrence wins, subsequent dups are
//     dropped"; double-counting would corrupt blob_refs.ref_count.
//  4. If the input was a syntactically valid JSON array of objects,
//     len(out) <= len(arr); we cannot manufacture entries from thin
//     air.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func FuzzChatAttachmentHashes(f *testing.F) {
	// Seed 1: minimal valid single-entry array.
	f.Add([]byte(`[{"blob_hash":"abc","size":1}]`))
	// Seed 2: oversize array (~50 entries) to push the dedup map.
	{
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < 50; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			// Mix duplicates and unique hashes so the dedup logic is
			// exercised on a realistic input shape.
			if i%3 == 0 {
				b.WriteString(`{"blob_hash":"dup","size":42}`)
			} else {
				b.WriteString(`{"blob_hash":"h`)
				// crude itoa to avoid a strconv import in the seed.
				b.WriteByte(byte('0' + (i/10)%10))
				b.WriteByte(byte('0' + i%10))
				b.WriteString(`","size":7}`)
			}
		}
		b.WriteByte(']')
		f.Add([]byte(b.String()))
	}
	// Seed 3: malformed JSON (truncated, embedded null bytes, deeply
	// nested object). One seed per pathology, to give the engine
	// distinct coverage signatures.
	f.Add([]byte(`[{"blob_hash":"abc","size":`)) // truncated
	f.Add([]byte("[{\"blob_hash\":\"a\x00b\",\"size\":1}]"))
	{
		// 1024 levels of nesting in a single value — deep enough to
		// stress the JSON decoder's recursion handling.
		var b bytes.Buffer
		b.WriteByte('[')
		for i := 0; i < 1024; i++ {
			b.WriteByte('{')
			b.WriteString(`"x":`)
		}
		b.WriteString(`null`)
		for i := 0; i < 1024; i++ {
			b.WriteByte('}')
		}
		b.WriteByte(']')
		f.Add(b.Bytes())
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := ChatAttachmentHashes(in)
		if err != nil {
			// Errored returns are allowed; we only care about the
			// success path's structural invariants. But the slice on
			// error must be empty per the function contract.
			if len(out) != 0 {
				t.Fatalf("error return with non-empty slice: out=%d err=%v", len(out), err)
			}
			return
		}
		seen := make(map[string]struct{}, len(out))
		for i, entry := range out {
			if entry.Hash == "" {
				t.Fatalf("entry %d has empty Hash", i)
			}
			if _, dup := seen[entry.Hash]; dup {
				t.Fatalf("entry %d duplicates Hash %q already in slice", i, entry.Hash)
			}
			seen[entry.Hash] = struct{}{}
		}
		// Upper-bound check: len(out) cannot exceed len(arr) on a
		// syntactically valid JSON array. We re-decode here only as a
		// cross-check; the parser itself gates on this already.
		if len(in) > 0 {
			var arr []map[string]any
			if jerr := json.Unmarshal(in, &arr); jerr == nil {
				if len(out) > len(arr) {
					t.Fatalf("len(out)=%d > len(arr)=%d", len(out), len(arr))
				}
			}
		}
	})
}
