package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// DefaultMaxFrameBytes caps a single JSON-RPC frame at 16 MiB. Frames larger
// than this are rejected — an adversarial plugin cannot drive the supervisor
// out of memory by streaming one enormous line.
const DefaultMaxFrameBytes = 16 * 1024 * 1024

// FrameReader reads newline-delimited JSON-RPC frames from an io.Reader.
// It is not safe for concurrent use; own one reader per goroutine.
type FrameReader struct {
	br           *bufio.Reader
	maxFrameSize int
}

// NewFrameReader wraps r. If maxFrameSize <= 0, DefaultMaxFrameBytes is used.
func NewFrameReader(r io.Reader, maxFrameSize int) *FrameReader {
	if maxFrameSize <= 0 {
		maxFrameSize = DefaultMaxFrameBytes
	}
	return &FrameReader{
		br:           bufio.NewReaderSize(r, 64*1024),
		maxFrameSize: maxFrameSize,
	}
}

// ErrFrameTooLarge is returned when a single frame exceeds the configured
// cap. The caller should treat this as fatal for the connection.
var ErrFrameTooLarge = errors.New("plugin: json-rpc frame exceeds maximum")

// ReadFrame reads one newline-delimited JSON frame and returns its raw bytes
// without the trailing newline. Returns io.EOF on clean end-of-stream.
func (fr *FrameReader) ReadFrame() ([]byte, error) {
	var buf bytes.Buffer
	for {
		chunk, err := fr.br.ReadSlice('\n')
		buf.Write(chunk)
		if buf.Len() > fr.maxFrameSize {
			return nil, ErrFrameTooLarge
		}
		if err == nil {
			// Trim trailing newline; preserve any \r for defensive parsers.
			b := buf.Bytes()
			if n := len(b); n > 0 && b[n-1] == '\n' {
				b = b[:n-1]
			}
			if n := len(b); n > 0 && b[n-1] == '\r' {
				b = b[:n-1]
			}
			if len(bytes.TrimSpace(b)) == 0 {
				// Empty line — skip; real frames carry JSON objects.
				buf.Reset()
				continue
			}
			// Copy out so later ReadFrame calls can reuse buf.
			out := make([]byte, len(b))
			copy(out, b)
			return out, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			// ReadSlice returned a partial line; keep reading.
			continue
		}
		if errors.Is(err, io.EOF) {
			if buf.Len() == 0 {
				return nil, io.EOF
			}
			// Trailing fragment without newline; treat as a frame.
			b := bytes.TrimRight(buf.Bytes(), "\r\n")
			if len(bytes.TrimSpace(b)) == 0 {
				return nil, io.EOF
			}
			out := make([]byte, len(b))
			copy(out, b)
			return out, nil
		}
		return nil, err
	}
}

// FrameWriter writes newline-delimited JSON-RPC frames to an io.Writer. All
// writes are serialized by a mutex so multiple goroutines may share one
// writer (e.g. the server dispatching requests and the client replying).
type FrameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewFrameWriter wraps w.
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

// WriteFrame marshals v to JSON and writes one framed line. Returns the
// first write error encountered.
func (fw *FrameWriter) WriteFrame(v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("plugin: marshal frame: %w", err)
	}
	if bytes.ContainsAny(buf, "\n") {
		// json.Marshal never emits literal newlines for valid inputs; this
		// protects against manual io.Writer implementations sneaking them in.
		return fmt.Errorf("plugin: marshalled frame contains newline")
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if _, err := fw.w.Write(buf); err != nil {
		return fmt.Errorf("plugin: write frame: %w", err)
	}
	if _, err := fw.w.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("plugin: write frame newline: %w", err)
	}
	if f, ok := fw.w.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return fmt.Errorf("plugin: flush frame: %w", err)
		}
	}
	return nil
}

// DecodeFrame parses a raw frame into a Request (for incoming server-to-
// plugin messages) or into a Response+flag (for incoming plugin-to-server
// messages). Callers that accept both on the same stream use ClassifyFrame.
func DecodeFrame(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("plugin: decode frame: %w", err)
	}
	return nil
}

// FrameKind classifies a raw frame as a request/notification or a response.
type FrameKind int

// Possible frame classifications.
const (
	FrameUnknown FrameKind = iota
	FrameRequest
	FrameNotification
	FrameResponse
)

// ClassifyFrame inspects raw JSON without unmarshalling into a concrete type
// and reports what kind of frame it is. Ambiguous frames (contain both
// "method" and "result"/"error") return FrameUnknown so the caller can log
// the malformed input and continue.
func ClassifyFrame(raw []byte) (FrameKind, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return FrameUnknown, fmt.Errorf("plugin: classify frame: %w", err)
	}
	_, hasMethod := probe["method"]
	_, hasResult := probe["result"]
	_, hasError := probe["error"]
	_, hasID := probe["id"]
	switch {
	case hasMethod && !hasResult && !hasError:
		if hasID {
			return FrameRequest, nil
		}
		return FrameNotification, nil
	case !hasMethod && (hasResult || hasError):
		return FrameResponse, nil
	default:
		return FrameUnknown, fmt.Errorf("plugin: ambiguous frame")
	}
}
