package protochat

// RFC 6455 frame-codec fuzz target (STANDARDS §8.2). Track C of Wave
// 2.9.5: every wire parser shipped in Waves 2.5-2.8 must carry a fuzz
// harness. readFrame is the chat front-door: it sees raw bytes from
// untrusted browsers, so a panic here would translate into a server
// crash from a hostile client.
//
// The target invariants are minimal:
//
//   1. readFrame must never panic on any byte sequence — well-formed
//      or malformed.
//   2. On a successful return, the decoded payload must not exceed the
//      caller-supplied maxBytes cap (defence against integer-overflow
//      on the 64-bit length field).
//   3. Control frames (close/ping/pong) returned with err == nil must
//      satisfy fin == true and len(payload) <= 125, the RFC 6455 §5.5
//      structural rules.
//
// We do not assert anything about the connection-level state machine
// here; that lives in `server_test.go`. The fuzz target is purely the
// codec.

import (
	"bytes"
	"testing"
)

// FuzzReadFrame drives readFrame against arbitrary byte streams. The
// fuzzer toggles the clientToServer flag via the second byte of the
// input so both mask-bit policies get exercised; the maxBytes cap is
// fixed at 64 KiB which mirrors the production default.
func FuzzReadFrame(f *testing.F) {
	// Seed 1: minimal masked text frame. opcode=text, fin=1, masked,
	// payload-len=2, mask=0x00000000 (xor identity), "hi".
	f.Add([]byte{0x81, 0x82, 0x00, 0x00, 0x00, 0x00, 'h', 'i'}, true)
	// Seed 2: 16-bit length boundary frame. opcode=binary, fin=1,
	// masked, ext-len 0x0080 (128 bytes), mask=0x00000000, then 128
	// payload bytes.
	{
		hdr := []byte{0x82, 0xFE, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00}
		body := bytes.Repeat([]byte{'A'}, 128)
		f.Add(append(hdr, body...), true)
	}
	// Seed 3: control frame with FIN=0 must be rejected. opcode=ping,
	// fin=0, masked, len=0, mask=0. RFC 6455 §5.5 forbids fragmented
	// control frames; this exercises the rejection path.
	f.Add([]byte{0x09, 0x80, 0x00, 0x00, 0x00, 0x00}, true)
	// Extra adversarial seeds — short reads, oversize lengths, RSV
	// bits set, server-side unmasked, bad opcodes — so the fuzzer's
	// initial corpus is rich enough to find shallow regressions on
	// every PR.
	f.Add([]byte{}, true)
	f.Add([]byte{0x00}, true)
	f.Add([]byte{0xFF, 0xFF}, true)                                                 // FIN+RSV+masked, plen=127 (8-byte ext) but no body
	f.Add([]byte{0x81, 0xFF, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, true) // 64-bit max plen
	f.Add([]byte{0x83, 0x80, 0x00, 0x00, 0x00, 0x00}, true)                         // unknown opcode 0x3
	f.Add([]byte{0x81, 0x02, 'h', 'i'}, true)                                       // client->server but unmasked
	f.Add([]byte{0x81, 0x82, 0x00, 0x00, 0x00, 0x00, 'h', 'i'}, false)              // server->client but masked
	f.Add([]byte{0x88, 0x80, 0x00, 0x00, 0x00, 0x00}, true)                         // empty close

	f.Fuzz(func(t *testing.T, in []byte, c2s bool) {
		const maxBytes = 64 * 1024
		fr, err := readFrame(bytes.NewReader(in), c2s, maxBytes)
		if err != nil {
			return // typed error path is the documented failure mode
		}
		if len(fr.payload) > maxBytes {
			t.Fatalf("payload %d exceeds maxBytes %d", len(fr.payload), maxBytes)
		}
		// Control-frame structural rules must hold on any frame the
		// codec deemed acceptable.
		switch fr.opcode {
		case opClose, opPing, opPong:
			if !fr.fin {
				t.Fatalf("control frame with FIN=0 leaked through (op=%x)", fr.opcode)
			}
			if len(fr.payload) > 125 {
				t.Fatalf("control frame payload too large: %d", len(fr.payload))
			}
		}
	})
}
