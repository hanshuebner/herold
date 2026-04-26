package protocall

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzSignalPayload covers the JSON-decode + Kind-switch boundary in
// HandleSignal. The dispatcher takes adversarial bytes off the chat
// WebSocket (the chat protocol forwards them verbatim into
// ClientFrame.Payload), and the fuzz target's job is to confirm no
// input panics the server.
//
// Invariants enforced (defence-in-depth per STANDARDS section 8.2):
//
//  1. HandleSignal never panics regardless of input: malformed JSON,
//     unknown discriminator kinds, oversized SDP blobs, embedded NUL
//     bytes, deeply nested arrays, and the server-only kinds (busy,
//     timeout) sent by a client all flow through the unmarshal +
//     dispatch path without crashing the goroutine.
//
//  2. Every code path under the kind switch produces zero or more
//     events on the broadcaster, never an unhandled panic. The
//     existing TestSignal_BusyAndTimeout_FromClient_Rejected and
//     TestSignal_InvalidPayload_Rejected unit tests cover the
//     dispatcher's reject responses; this fuzz target's job is to
//     stress the decode + dispatch boundary, not to re-assert the
//     dispatcher's reject vocabulary.
//
// Smoke run (recorded in the Track C report):
//
//	go test -run='ZZZ' -fuzz='FuzzSignalPayload' -fuzztime=10s ./internal/protocall/
func FuzzSignalPayload(f *testing.F) {
	// Seed corpus: the canonical shapes the dispatcher must survive
	// without panicking.
	//
	//  1. Minimal valid offer.
	//  2. Minimal valid answer.
	//  3. kind=hangup with extra fields.
	//  4. kind="busy": server-only kind sent by a client; rejected,
	//     not crashed.
	//  5. Embedded NUL byte in sdp.
	//  6. Oversized SDP body (1 MiB).
	seedOffer, _ := json.Marshal(SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
		SDP:            "v=0\r\n...",
	})
	seedAnswer, _ := json.Marshal(SignalPayload{
		Kind:           SignalKindAnswer,
		CallID:         "fuzz-call",
		ConversationID: "conv-dm",
		SDP:            "answer-sdp",
	})
	seedHangupExtra := []byte(`{
		"kind": "hangup",
		"callId": "fuzz-call",
		"conversationId": "conv-dm",
		"reason": "bye",
		"unknown_top_level_field": [1,2,3],
		"sdpMLineIndex": 9999
	}`)
	seedBusy, _ := json.Marshal(SignalPayload{
		Kind:           SignalKindBusy,
		CallID:         "fuzz-call",
		ConversationID: "conv-dm",
	})
	// Embed a NUL byte mid-string so the fuzz seed corpus exercises
	// json.Unmarshal's handling of in-string control bytes. The byte
	// is injected programmatically because Go source files reject NUL.
	seedNullByte, _ := json.Marshal(SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
		SDP:            "v=0\x00BAD",
	})
	seedOversized := mustMarshalOversizedOffer()

	for _, seed := range [][]byte{
		seedOffer,
		seedAnswer,
		seedHangupExtra,
		seedBusy,
		seedNullByte,
		seedOversized,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Bound the input so the fuzz engine does not spend hours on
		// pathological allocator sizes. The server enforces no SDP
		// ceiling at the protocall layer (the chat WebSocket frame
		// budget is upstream), so a multi-MB synthetic blob is on-
		// shape but outside the fuzz target's contract.
		if len(data) > 1<<20 { // 1 MiB
			t.Skip()
		}
		s, _, _, _, _, _ := newFixture(t)
		// HandleSignal swallows its own errors after logging them and
		// must not propagate panics. A failing fuzzer surfaces here as
		// a panic crashing the goroutine; the test framework catches
		// it and reports the minimised input.
		s.HandleSignal(context.Background(), 10, ClientFrame{
			Type:    "call.signal",
			Payload: data,
		})
	})
}

// mustMarshalOversizedOffer builds a near-1-MiB offer body so the
// fuzzer's seed corpus exercises the worst-case decode allocation in
// json.Unmarshal. Defensive cap: stay just under the 1 MiB limit
// imposed in the f.Fuzz body so the seed itself is not skipped.
func mustMarshalOversizedOffer() []byte {
	// 900 KiB of "a" so the encoded JSON stays under 1 MiB after the
	// envelope overhead.
	big := strings.Repeat("a", 900*1024)
	buf, err := json.Marshal(SignalPayload{
		Kind:           SignalKindOffer,
		ConversationID: "conv-dm",
		SDP:            big,
	})
	if err != nil {
		// Should never happen: SignalPayload marshals without errors.
		// If it ever does, return a sentinel that the fuzz target will
		// still tolerate.
		return []byte(`{"kind":"offer","conversationId":"conv-dm","sdp":"`)
	}
	// Sanity: confirm the body is non-empty bytes (not a stray
	// "null"). bytes.IndexByte on the marshalled payload guards
	// against a future SignalPayload type change that returns nil.
	if bytes.IndexByte(buf, '{') < 0 {
		return []byte(`{"kind":"offer","conversationId":"conv-dm"}`)
	}
	return buf
}
