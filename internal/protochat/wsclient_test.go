package protochat

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// testClient is a minimal hand-rolled WebSocket client used only by
// the protochat tests. It performs the RFC 6455 handshake, masks
// outbound frames per spec, and exposes Read/Write methods over the
// frame opcodes the protocol uses. We deliberately avoid pulling a
// third-party WebSocket dependency — the implementation under test
// is the framing codec; the test client validates the wire shape.
type testClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

// dialTestClient connects to addr, performs the upgrade handshake,
// and returns a ready client. Cookies and other headers are added by
// callers via the headers map.
func dialTestClient(addr string, headers map[string]string) (*testClient, *http.Response, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	host, _, _ := net.SplitHostPort(addr)
	if host == "" {
		host = addr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GET /chat/ws HTTP/1.1\r\n")
	fmt.Fprintf(&b, "Host: %s\r\n", addr)
	fmt.Fprintf(&b, "Upgrade: websocket\r\n")
	fmt.Fprintf(&b, "Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	fmt.Fprintf(&b, "Sec-WebSocket-Version: 13\r\n")
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, &http.Request{Method: "GET"})
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		// Don't close the conn here so callers can inspect resp.
		return nil, resp, nil
	}
	// Verify the accept hash matches the one we'd compute. Mirror
	// the server's algorithm so a regression in either side trips a
	// test.
	got := resp.Header.Get("Sec-WebSocket-Accept")
	want := computeAcceptKey(key)
	if got != want {
		_ = conn.Close()
		return nil, resp, fmt.Errorf("accept hash mismatch: got %q want %q", got, want)
	}
	return &testClient{conn: conn, reader: r}, resp, nil
}

func (c *testClient) Close() {
	_ = c.conn.Close()
}

// writeText sends a masked text frame containing payload.
func (c *testClient) writeText(payload []byte) error {
	return c.writeFrame(opText, payload, true)
}

// writeUnmasked sends a frame with the mask bit cleared. Used by
// the protocol-error test to verify the server rejects unmasked
// client frames.
func (c *testClient) writeUnmasked(opcode byte, payload []byte) error {
	return c.writeFrame(opcode, payload, false)
}

// writePong sends a control pong with empty payload. Used by the
// heartbeat tests.
func (c *testClient) writePong() error {
	return c.writeFrame(opPong, nil, true)
}

// writeFrame writes one frame to the wire. masked controls whether
// the spec-mandated masking is applied; tests that exercise the
// "unmasked client frame" failure mode pass false.
func (c *testClient) writeFrame(opcode byte, payload []byte, masked bool) error {
	var hdr [14]byte
	hdr[0] = 0x80 | (opcode & 0x0F) // FIN + opcode
	plen := len(payload)
	idx := 2
	switch {
	case plen <= 125:
		hdr[1] = byte(plen)
	case plen <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(plen))
		idx = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(plen))
		idx = 10
	}
	var maskKey [4]byte
	if masked {
		hdr[1] |= 0x80
		if _, err := rand.Read(maskKey[:]); err != nil {
			return err
		}
		copy(hdr[idx:idx+4], maskKey[:])
		idx += 4
	}
	if _, err := c.conn.Write(hdr[:idx]); err != nil {
		return err
	}
	if plen == 0 {
		return nil
	}
	body := make([]byte, plen)
	copy(body, payload)
	if masked {
		for i := range body {
			body[i] ^= maskKey[i%4]
		}
	}
	_, err := c.conn.Write(body)
	return err
}

// readNext reads one frame from the wire (no fragmentation
// reassembly — every frame the protochat server emits is a single-
// frame message). Returns the opcode and payload; control frames
// are surfaced too so the caller can react to ping / close.
func (c *testClient) readNext() (byte, []byte, error) {
	f, err := readFrame(c.reader, false, 1<<20)
	if err != nil {
		return 0, nil, err
	}
	return f.opcode, f.payload, nil
}

// readServerFrame reads the next text frame and JSON-decodes it
// into a ServerFrame. Skips inbound pings (auto-replies with a
// pong) and surfaces close frames as an error so test cases can
// react to unexpected close.
func (c *testClient) readServerFrame() (ServerFrame, error) {
	for {
		op, payload, err := c.readNext()
		if err != nil {
			return ServerFrame{}, err
		}
		switch op {
		case opPing:
			if err := c.writePong(); err != nil {
				return ServerFrame{}, err
			}
		case opPong:
			// ignore
		case opClose:
			code, reason := decodeCloseFrame(payload)
			return ServerFrame{}, fmt.Errorf("server closed: code=%d reason=%q", code, reason)
		case opText:
			var sf ServerFrame
			if err := json.Unmarshal(payload, &sf); err != nil {
				return ServerFrame{}, err
			}
			return sf, nil
		default:
			return ServerFrame{}, fmt.Errorf("unexpected opcode %x", op)
		}
	}
}

// readUntilClose drains the wire until a close frame arrives.
// Returns the close code observed and any non-EOF error.
func (c *testClient) readUntilClose() (closeCode, error) {
	for {
		op, payload, err := c.readNext()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, nil
			}
			return 0, err
		}
		if op == opClose {
			code, _ := decodeCloseFrame(payload)
			return code, nil
		}
		if op == opPing {
			_ = c.writePong()
		}
	}
}
