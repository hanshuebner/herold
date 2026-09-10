package protoimap

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
)

// responseTraceLiteralMax caps the literal payload bytes shown in a
// trace-level response log line (issue #320): enough to tell whether a
// FETCH body actually carries the expected content without writing
// megabytes of message body into the log.
const responseTraceLiteralMax = 256

// imapSessionCounter disambiguates session ids minted within the same
// nanosecond (mirrors internal/protosmtp's newSessionID).
var imapSessionCounter atomic.Uint64

// newIMAPSessionID returns a short, low-collision id used to correlate
// every trace/log record emitted by one IMAP connection.
func newIMAPSessionID() string {
	var b [8]byte
	h := sha256.Sum256(fmt.Appendf(nil, "%d-%d", time.Now().UnixNano(), imapSessionCounter.Add(1)))
	copy(b[:], h[:8])
	return strings.ToLower(fmt.Sprintf("%x", b))
}

// traceCommand emits one trace-level log line for a command the session
// just received (REQ-OPS-82, issue #320): tag, verb, and arguments, with
// LOGIN/AUTHENTICATE credentials redacted and literal payloads replaced
// by their byte count. The Enabled check runs before any formatting so a
// logger without protoimap at trace level pays no cost.
func (ses *session) traceCommand(ctx context.Context, c *Command) {
	if !ses.logger.Enabled(ctx, observe.LevelTrace) {
		return
	}
	ses.logger.Log(ctx, observe.LevelTrace, "protoimap: C: "+traceCommandLine(c),
		"activity", observe.ActivityAccess,
		"tag", c.Tag,
		"verb", c.Op,
	)
}

// traceCommandLine renders the tag, verb, and arguments of c as they
// would appear on the wire, except that:
//   - LOGIN's username is shown but its password is replaced by REDACTED
//   - AUTHENTICATE's mechanism is shown but any initial response (which
//     carries the credential for PLAIN/LOGIN/OAUTHBEARER/XOAUTH2) is
//     replaced by REDACTED
//   - every other literal slot is replaced by "{N bytes}" using the size
//     recorded at read time; the literal content itself is never in Raw
//     by the time traceCommandLine runs
func traceCommandLine(c *Command) string {
	switch c.Op {
	case "LOGIN":
		return fmt.Sprintf("%s LOGIN %s REDACTED", c.Tag, c.LoginUser)
	case "AUTHENTICATE":
		if c.AuthInitial != nil {
			return fmt.Sprintf("%s AUTHENTICATE %s REDACTED", c.Tag, c.AuthMechanism)
		}
		return fmt.Sprintf("%s AUTHENTICATE %s", c.Tag, c.AuthMechanism)
	default:
		return substituteLiteralMarkers(c.Raw, c.LiteralSizes)
	}
}

// substituteLiteralMarkers replaces each NUL literal-slot marker left in
// raw by readCommand with "{N bytes}" in encounter order, so the trace
// line carries the size of every literal (message bodies, mailbox names,
// search strings, ...) without ever carrying its content.
func substituteLiteralMarkers(raw string, sizes []int64) string {
	if !strings.ContainsRune(raw, 0) {
		return raw
	}
	var sb strings.Builder
	idx := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == 0 {
			if idx < len(sizes) {
				fmt.Fprintf(&sb, "{%d bytes}", sizes[idx])
			} else {
				sb.WriteString("{literal}")
			}
			idx++
			continue
		}
		sb.WriteByte(raw[i])
	}
	return sb.String()
}

// traceLiteral renders a literal payload for a trace-level response log
// line: the byte count, and up to responseTraceLiteralMax bytes of the
// payload as a Go-quoted string so control and binary bytes stay on one
// line. The full length is always noted, truncated or not.
func traceLiteral(data []byte) string {
	if len(data) <= responseTraceLiteralMax {
		return fmt.Sprintf("{%d bytes}%q", len(data), data)
	}
	return fmt.Sprintf("{%d bytes, showing first %d}%q", len(data), responseTraceLiteralMax, data[:responseTraceLiteralMax])
}
