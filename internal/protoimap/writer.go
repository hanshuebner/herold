package protoimap

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	imap "github.com/emersion/go-imap/v2"
)

// respWriter serialises untagged and tagged responses to the client. It is
// safe for concurrent use: IDLE delivery from the broadcaster goroutine
// holds the mutex while writing an untagged response, so no two responses
// can interleave on the wire.
type respWriter struct {
	mu  sync.Mutex
	bw  *bufio.Writer
	raw io.Writer
}

func newRespWriter(w io.Writer) *respWriter {
	bw, ok := w.(*bufio.Writer)
	if !ok {
		bw = bufio.NewWriter(w)
	}
	return &respWriter{bw: bw, raw: w}
}

func (w *respWriter) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bw.Flush()
}

// writeLine writes a CRLF-terminated line.
func (w *respWriter) writeLine(s string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.bw.WriteString(s); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\r\n"); err != nil {
		return err
	}
	return w.bw.Flush()
}

// writeRaw writes arbitrary bytes (no framing) under the mutex. Used for
// literal payloads embedded in untagged FETCH responses.
func (w *respWriter) writeRaw(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.bw.Write(b)
	return err
}

// untagged writes "* <line>\r\n".
func (w *respWriter) untagged(line string) error { return w.writeLine("* " + line) }

// taggedOK writes "<tag> OK [code] text\r\n".
func (w *respWriter) taggedOK(tag, code, text string) error {
	return w.writeLine(buildStatus(tag, imap.StatusResponseTypeOK, imap.ResponseCode(code), text))
}

func (w *respWriter) taggedNO(tag, code, text string) error {
	return w.writeLine(buildStatus(tag, imap.StatusResponseTypeNo, imap.ResponseCode(code), text))
}

func (w *respWriter) taggedBAD(tag, code, text string) error {
	return w.writeLine(buildStatus(tag, imap.StatusResponseTypeBad, imap.ResponseCode(code), text))
}

func (w *respWriter) continuation(text string) error {
	return w.writeLine("+ " + text)
}

func buildStatus(tag string, typ imap.StatusResponseType, code imap.ResponseCode, text string) string {
	var sb strings.Builder
	sb.WriteString(tag)
	sb.WriteByte(' ')
	sb.WriteString(string(typ))
	sb.WriteByte(' ')
	if code != "" {
		sb.WriteByte('[')
		sb.WriteString(string(code))
		sb.WriteByte(']')
		sb.WriteByte(' ')
	}
	if text == "" {
		text = "completed"
	}
	sb.WriteString(text)
	return sb.String()
}

// imapQuote encodes s as an IMAP quoted string, falling back to a
// literal for values containing CR, LF, or NUL.
func imapQuote(s string) string {
	if strings.ContainsAny(s, "\x00\r\n") {
		return fmt.Sprintf("{%d}\r\n%s", len(s), s)
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			sb.WriteByte('\\')
		}
		sb.WriteByte(c)
	}
	sb.WriteByte('"')
	return sb.String()
}

// imapNString returns either NIL or a quoted/literal string.
func imapNString(s string) string {
	if s == "" {
		return "NIL"
	}
	return imapQuote(s)
}

// flagListString formats a bitfield + keyword list as a parenthesised
// flag-list.
func flagListString(flags []string) string {
	return "(" + strings.Join(flags, " ") + ")"
}

// formatEnvelope renders an imap.Envelope as an IMAP envelope.
func formatEnvelope(e imap.Envelope) string {
	var sb strings.Builder
	sb.WriteByte('(')
	if !e.Date.IsZero() {
		sb.WriteString(imapQuote(e.Date.Format(time.RFC1123Z)))
	} else {
		sb.WriteString("NIL")
	}
	sb.WriteByte(' ')
	sb.WriteString(imapNString(e.Subject))
	sb.WriteByte(' ')
	sb.WriteString(formatAddrList(e.From))
	sb.WriteByte(' ')
	sb.WriteString(formatAddrList(e.Sender))
	sb.WriteByte(' ')
	sb.WriteString(formatAddrList(e.ReplyTo))
	sb.WriteByte(' ')
	sb.WriteString(formatAddrList(e.To))
	sb.WriteByte(' ')
	sb.WriteString(formatAddrList(e.Cc))
	sb.WriteByte(' ')
	sb.WriteString(formatAddrList(e.Bcc))
	sb.WriteByte(' ')
	if len(e.InReplyTo) > 0 {
		sb.WriteString(imapNString(strings.Join(e.InReplyTo, " ")))
	} else {
		sb.WriteString("NIL")
	}
	sb.WriteByte(' ')
	sb.WriteString(imapNString(e.MessageID))
	sb.WriteByte(')')
	return sb.String()
}

func formatAddrList(addrs []imap.Address) string {
	if len(addrs) == 0 {
		return "NIL"
	}
	var sb strings.Builder
	sb.WriteByte('(')
	for i, a := range addrs {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte('(')
		sb.WriteString(imapNString(a.Name))
		sb.WriteByte(' ')
		sb.WriteString("NIL") // source-route, always NIL
		sb.WriteByte(' ')
		sb.WriteString(imapNString(a.Mailbox))
		sb.WriteByte(' ')
		sb.WriteString(imapNString(a.Host))
		sb.WriteByte(')')
	}
	sb.WriteByte(')')
	return sb.String()
}

// formatNumSet renders a SeqSet or UIDSet canonically.
func formatNumSet(ns imap.NumSet) string {
	if ns == nil {
		return ""
	}
	return ns.String()
}

// formatInternalDate renders t in IMAP internal-date form.
func formatInternalDate(t time.Time) string {
	if t.IsZero() {
		return `"01-Jan-1970 00:00:00 +0000"`
	}
	return `"` + t.Format("02-Jan-2006 15:04:05 -0700") + `"`
}

func uintString(n uint64) string { return strconv.FormatUint(n, 10) }
