package protochat

import (
	"encoding/binary"
	"errors"
	"io"
)

// RFC 6455 opcodes. We implement only the subset chat needs:
// continuation (for fragmentation), text (the JSON envelope shape),
// binary (reserved; we currently reject), close, ping, pong.
const (
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA
)

// closeCode is the 16-bit application-level close reason RFC 6455
// §7.4 mandates as the first two bytes of a close-frame payload.
type closeCode uint16

const (
	closeNormalClosure   closeCode = 1000
	closeGoingAway       closeCode = 1001
	closeProtocolError   closeCode = 1002
	closeUnsupportedData closeCode = 1003
	closeInvalidPayload  closeCode = 1007
	closePolicyViolation closeCode = 1008
	closeMessageTooBig   closeCode = 1009
	closeInternalError   closeCode = 1011
)

// frame is a decoded RFC 6455 frame. We keep the structure flat: the
// codec resolves masking before handing the payload to callers, so
// the rest of the package never sees masking keys.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// Errors returned by the framing layer. The connection-level handler
// translates each into a close code per RFC 6455 §7.4.1.
var (
	errFrameRSVBitsSet     = errors.New("protochat: RSV1/2/3 set; no extension negotiated")
	errFrameUnmaskedClient = errors.New("protochat: client frame missing mask bit")
	errFrameMaskedServer   = errors.New("protochat: server received masked frame from server peer (impossible)")
	errFrameTooLarge       = errors.New("protochat: frame payload exceeds maxBytes")
	errFrameBadOpcode      = errors.New("protochat: reserved or unknown opcode")
	errFrameBadControl     = errors.New("protochat: control frame violates RFC 6455 §5.5")
)

// readFrame parses one RFC 6455 frame from r. The clientToServer flag
// selects the mask-bit policy: client → server frames MUST be masked
// (RFC 6455 §5.1); server → client frames MUST NOT. maxBytes is the
// per-frame byte cap; a payload that would exceed it returns
// errFrameTooLarge before any allocation happens.
//
// Fragmentation: the codec returns each frame separately. Callers
// reassemble by checking f.fin and accumulating continuation frames.
// We support fragmentation but our application protocol doesn't emit
// it; oversize messages get rejected at the application layer.
func readFrame(r io.Reader, clientToServer bool, maxBytes int) (frame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	fin := hdr[0]&0x80 != 0
	if hdr[0]&0x70 != 0 {
		return frame{}, errFrameRSVBitsSet
	}
	opcode := hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	plen := int64(hdr[1] & 0x7F)

	switch opcode {
	case opContinuation, opText, opBinary, opClose, opPing, opPong:
	default:
		return frame{}, errFrameBadOpcode
	}
	// RFC 6455 §5.5: control frames must not be fragmented and must
	// have a payload of <=125 bytes.
	if (opcode == opClose || opcode == opPing || opcode == opPong) && (!fin || plen > 125) {
		return frame{}, errFrameBadControl
	}

	switch plen {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		plen = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		plen = int64(binary.BigEndian.Uint64(ext[:]))
		if plen < 0 {
			return frame{}, errFrameTooLarge
		}
	}
	if maxBytes > 0 && plen > int64(maxBytes) {
		return frame{}, errFrameTooLarge
	}
	if clientToServer && !masked {
		return frame{}, errFrameUnmaskedClient
	}
	if !clientToServer && masked {
		return frame{}, errFrameMaskedServer
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return frame{}, err
		}
	}
	payload := make([]byte, plen)
	if plen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return frame{}, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
	}
	return frame{fin: fin, opcode: opcode, payload: payload}, nil
}

// writeFrame encodes f into w using server-to-client framing rules:
// the mask bit is always zero. fin is honoured; callers may emit a
// fragmented stream by passing fin=false on the leading frames and
// fin=true on the terminator (we do not in practice — every
// application message fits in one frame).
func writeFrame(w io.Writer, f frame) error {
	var hdr [10]byte
	n := 2
	if f.fin {
		hdr[0] |= 0x80
	}
	hdr[0] |= f.opcode & 0x0F
	plen := len(f.payload)
	switch {
	case plen <= 125:
		hdr[1] = byte(plen)
	case plen <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(plen))
		n = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(plen))
		n = 10
	}
	if _, err := w.Write(hdr[:n]); err != nil {
		return err
	}
	if plen == 0 {
		return nil
	}
	_, err := w.Write(f.payload)
	return err
}

// writeCloseFrame writes a close frame with the supplied code and
// optional reason. RFC 6455 §5.5.1 caps the reason at 123 bytes (the
// frame's two-byte status code consumes the rest of the 125-byte
// control-frame budget).
func writeCloseFrame(w io.Writer, code closeCode, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], uint16(code))
	copy(payload[2:], reason)
	return writeFrame(w, frame{fin: true, opcode: opClose, payload: payload})
}

// decodeCloseFrame extracts the status code and reason from a close
// frame's payload. Empty payload is the spec-allowed "no status
// code" case which we treat as a normal closure.
func decodeCloseFrame(payload []byte) (closeCode, string) {
	if len(payload) < 2 {
		return closeNormalClosure, ""
	}
	code := closeCode(binary.BigEndian.Uint16(payload[:2]))
	return code, string(payload[2:])
}
