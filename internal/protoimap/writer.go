package protoimap

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/observe"
)

// respWriter serialises untagged and tagged responses to the client. It is
// safe for concurrent use: IDLE delivery from the broadcaster goroutine
// holds the mutex while writing an untagged response, so no two responses
// can interleave on the wire.
//
// logger is stored behind an atomic pointer because it changes shape twice
// during a session's life without a new respWriter being built for either
// change: LOGIN/AUTHENTICATE narrow it to add principal_id, and both of
// those plus STARTTLS/COMPRESS (which do rebuild the respWriter) must keep
// the trace-level wire log (issue #320) attributed correctly regardless of
// which transport wraps the connection at the time.
type respWriter struct {
	mu     sync.Mutex
	bw     *bufio.Writer
	raw    io.Writer
	logger atomic.Pointer[slog.Logger]
}

func newRespWriter(w io.Writer, logger *slog.Logger) *respWriter {
	bw, ok := w.(*bufio.Writer)
	if !ok {
		bw = bufio.NewWriter(w)
	}
	rw := &respWriter{bw: bw, raw: w}
	rw.logger.Store(logger)
	return rw
}

// setLogger updates the logger used for trace-level response logging
// (issue #320). Called whenever the session's logger gains attributes
// (principal_id on LOGIN/AUTHENTICATE) or the respWriter is rebuilt over a
// new transport (STARTTLS, COMPRESS).
func (w *respWriter) setLogger(logger *slog.Logger) {
	w.logger.Store(logger)
}

// traceEnabled reports whether the current logger would accept a
// trace-level record. Callers building an expensive trace representation
// (e.g. the FETCH literal preview) MUST check this first so a session
// without protoimap at trace level pays no formatting cost (REQ-OPS-82).
func (w *respWriter) traceEnabled() bool {
	logger := w.logger.Load()
	return logger != nil && logger.Enabled(context.Background(), observe.LevelTrace)
}

// traceLine emits one trace-level log line for a response line sent to the
// client. kind is "tagged", "untagged", or "continuation".
func (w *respWriter) traceLine(kind, line string) {
	logger := w.logger.Load()
	if logger == nil {
		return
	}
	ctx := context.Background()
	if !logger.Enabled(ctx, observe.LevelTrace) {
		return
	}
	logger.Log(ctx, observe.LevelTrace, "protoimap: S: "+line,
		"activity", observe.ActivityAccess,
		"kind", kind,
	)
}

// writeLine writes a CRLF-terminated line and, at trace level, logs it
// (REQ-OPS-82, issue #320). This is the single hook point for the plain,
// tagged, and continuation response paths; STARTTLS and COMPRESS rebuild
// the respWriter over a new transport but every write still funnels
// through this method, so the trace log covers all three transports
// uniformly.
func (w *respWriter) writeLine(s string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.bw.WriteString(s); err != nil {
		return err
	}
	if _, err := w.bw.WriteString("\r\n"); err != nil {
		return err
	}
	err := w.bw.Flush()
	if err == nil {
		kind := "tagged"
		switch {
		case strings.HasPrefix(s, "* "):
			kind = "untagged"
		case strings.HasPrefix(s, "+ "):
			kind = "continuation"
		}
		w.traceLine(kind, s)
	}
	return err
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

// formatInternalDate renders t in IMAP internal-date form.
func formatInternalDate(t time.Time) string {
	if t.IsZero() {
		return `"01-Jan-1970 00:00:00 +0000"`
	}
	return `"` + t.Format("02-Jan-2006 15:04:05 -0700") + `"`
}
