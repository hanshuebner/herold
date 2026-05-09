package gmail

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// MboxMessage is one message yielded by an MboxReader: its raw RFC 5322
// bytes (headers + blank line + body, with mboxrd From-line escaping
// already undone) plus the unparsed envelope-from line for context.
type MboxMessage struct {
	// FromLine is the raw mbox "From " envelope line that opened the
	// message, without trailing newline. Format: "From <addr> <ctime-date>".
	// Used by the importer to populate INTERNALDATE per REQ-IMPORT-27.
	FromLine string
	// Body is the full RFC 5322 message bytes the headers + blank line +
	// body, ready to feed into a MIME parser. mboxrd ">From " escaping is
	// already undone (one leading '>' stripped from any line that begins
	// with one or more '>' followed by "From ").
	Body []byte
	// ByteOffset is the byte offset of the "From " line in the input,
	// useful for error reporting.
	ByteOffset int64
}

// MboxReader streams an mbox file one message at a time. The mbox format
// is a concatenation of messages; each message starts with a line that
// begins with "From " (capital F, single space) and contains an
// envelope sender plus a date. Subsequent occurrences of "From " inside
// a message body are escaped to ">From ", ">>From ", and so on
// (mboxrd, the variant Google Takeout emits).
//
// MboxReader handles arbitrary message sizes by streaming with a
// bufio.Reader; the only in-memory state is the current message's bytes.
// For typical Takeout messages (a few KB to a few MB) this is fine; the
// importer's overall memory budget caps the per-message size at
// MaxMessageSize.
type MboxReader struct {
	br      *bufio.Reader
	pending []byte // a "From " line we read while scanning for the next boundary
	offset  int64  // running byte offset of the next read
	atEOF   bool
}

// MaxMessageSize caps the size of a single mbox message. 1 GiB matches
// Gmail's per-message attachment ceiling with comfortable headroom.
// Messages larger than this fail the entire import — which is the right
// outcome: an mbox with a giant message is almost certainly corrupt.
const MaxMessageSize = 1 << 30

// NewMboxReader wraps r with a buffered reader sized to comfortably hold
// a single header block.
func NewMboxReader(r io.Reader) *MboxReader {
	return &MboxReader{br: bufio.NewReaderSize(r, 1<<16)}
}

// Next reads the next message and returns it. On end-of-stream Next
// returns (zero MboxMessage, io.EOF). On a malformed header it returns
// a non-EOF error.
func (m *MboxReader) Next() (MboxMessage, error) {
	if m.atEOF {
		return MboxMessage{}, io.EOF
	}

	var startOffset int64
	// Read the opening "From " line. There may be leading blank lines
	// between messages (Takeout sometimes emits one); skip them.
	var fromLine string
	if m.pending != nil {
		fromLine = strings.TrimRight(string(m.pending), "\r\n")
		startOffset = m.offset - int64(len(m.pending))
		m.pending = nil
	} else {
		for {
			line, err := m.readLine()
			if err == io.EOF {
				m.atEOF = true
				return MboxMessage{}, io.EOF
			}
			if err != nil {
				return MboxMessage{}, fmt.Errorf("mbox: read From line: %w", err)
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				// Inter-message blank line; skip.
				continue
			}
			if !strings.HasPrefix(trimmed, "From ") {
				return MboxMessage{}, fmt.Errorf(
					"mbox: expected 'From ' envelope at offset %d, got %q",
					m.offset-int64(len(line)), firstChars(trimmed, 80))
			}
			fromLine = trimmed
			startOffset = m.offset - int64(len(line))
			break
		}
	}

	// Now read the body until we hit the next "From " line at column 0
	// or EOF. mboxrd: lines that begin with one or more '>' followed by
	// "From " are escapes — strip exactly one '>' and keep them in the
	// body.
	body := bytes.NewBuffer(make([]byte, 0, 4096))
	for {
		line, err := m.readLine()
		if err == io.EOF {
			m.atEOF = true
			break
		}
		if err != nil {
			return MboxMessage{}, fmt.Errorf("mbox: read body line: %w", err)
		}
		if strings.HasPrefix(line, "From ") {
			// Boundary: this line opens the NEXT message. Save it for
			// the next call.
			m.pending = []byte(line)
			break
		}
		if isQuotedFrom(line) {
			// Strip exactly one leading '>'.
			body.WriteString(line[1:])
		} else {
			body.WriteString(line)
		}
		if body.Len() > MaxMessageSize {
			return MboxMessage{}, fmt.Errorf("mbox: message at offset %d exceeds %d bytes", startOffset, MaxMessageSize)
		}
	}

	return MboxMessage{
		FromLine:   fromLine,
		Body:       body.Bytes(),
		ByteOffset: startOffset,
	}, nil
}

// readLine returns the next line including its terminator, advancing
// m.offset by len(line). On EOF with no remaining bytes it returns
// ("", io.EOF). On EOF after a partial last line it returns the partial
// line and nil; the next call returns io.EOF.
func (m *MboxReader) readLine() (string, error) {
	var buf bytes.Buffer
	for {
		b, err := m.br.ReadByte()
		if err == io.EOF {
			if buf.Len() == 0 {
				return "", io.EOF
			}
			s := buf.String()
			m.offset += int64(len(s))
			return s, nil
		}
		if err != nil {
			return "", err
		}
		buf.WriteByte(b)
		if b == '\n' {
			s := buf.String()
			m.offset += int64(len(s))
			return s, nil
		}
		if buf.Len() > 1<<20 {
			return "", fmt.Errorf("mbox: line exceeds 1 MiB without newline")
		}
	}
}

// isQuotedFrom reports whether line begins with one or more '>' bytes
// followed immediately by "From " (mboxrd quoting).
func isQuotedFrom(line string) bool {
	i := 0
	for i < len(line) && line[i] == '>' {
		i++
	}
	if i == 0 {
		return false
	}
	return strings.HasPrefix(line[i:], "From ")
}

// firstChars returns the first n bytes of s, marked with an ellipsis
// when truncated. Used in error messages so a malformed header doesn't
// flood the log with a multi-megabyte body.
func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
