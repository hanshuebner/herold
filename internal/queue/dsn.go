package queue

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// DSNKind classifies a DSN into the three RFC 3464 categories the
// orchestrator emits. The values are persisted only as a metric label;
// not stored in the database.
type DSNKind uint8

// DSNKind values.
const (
	// DSNKindUnknown is the zero value and must not be persisted.
	DSNKindUnknown DSNKind = iota
	// DSNKindSuccess corresponds to "delivered" actions (RFC 3464
	// §2.3.3). Emitted when NOTIFY=SUCCESS is set and the receiver
	// accepted the message.
	DSNKindSuccess
	// DSNKindFailure corresponds to "failed" actions. Emitted on
	// permanent failure (5xx, retry exhaustion) when NOTIFY=FAILURE is
	// set or NOTIFY is absent.
	DSNKindFailure
	// DSNKindDelay corresponds to "delayed" actions. Emitted when a
	// row remains deferred longer than the operator-configured delay
	// threshold, if NOTIFY=DELAY was requested.
	DSNKindDelay
)

// String returns the lowercase metric-label token for k.
func (k DSNKind) String() string {
	switch k {
	case DSNKindSuccess:
		return "success"
	case DSNKindFailure:
		return "failure"
	case DSNKindDelay:
		return "delay"
	default:
		return "unknown"
	}
}

// dsnAction returns the RFC 3464 §2.3.3 action token for k.
func (k DSNKind) action() string {
	switch k {
	case DSNKindSuccess:
		return "delivered"
	case DSNKindFailure:
		return "failed"
	case DSNKindDelay:
		return "delayed"
	default:
		return "failed"
	}
}

// dsnInput collects every field the DSN builder needs. It is small on
// purpose — most fields default sensibly when zero — so the tests can
// assert against a stable, minimal shape.
type dsnInput struct {
	Kind            DSNKind
	ReportingMTA    string // "dns; <hostname>"
	From            string // postmaster@<hostname>
	To              string // original MAIL FROM (or empty for double-bounce protection)
	OriginalRcpt    string
	FinalRcpt       string
	OriginalEnvID   string
	DiagnosticCode  string    // "smtp; 550 5.1.1 user unknown"
	StatusCode      string    // "5.1.1"
	RemoteMTA       string    // "dns; mx.example.test"
	WillRetryUntil  time.Time // for delay DSNs
	OriginalHeaders []byte
	Now             time.Time
	MessageID       string // optional; generated when empty
	Subject         string // optional override
}

// buildDSN renders a complete RFC 3464 multipart/report message.
// Output is CRLF-terminated and ready to enqueue. The boundary string
// is derived from a 16-byte crypto/rand draw so each invocation
// produces a unique multipart wrapper.
func buildDSN(in dsnInput) ([]byte, error) {
	if in.Kind == DSNKindUnknown {
		return nil, fmt.Errorf("queue: dsn kind unset")
	}
	boundary, err := newBoundary()
	if err != nil {
		return nil, err
	}
	msgID := in.MessageID
	if msgID == "" {
		// Best-effort message-id; collisions are not security-relevant.
		var rb [12]byte
		if _, err := rand.Read(rb[:]); err != nil {
			return nil, fmt.Errorf("queue: dsn message-id rand: %w", err)
		}
		host := strings.TrimPrefix(in.From, "postmaster@")
		if host == "" {
			host = "localhost"
		}
		msgID = fmt.Sprintf("<%s.dsn@%s>", hex.EncodeToString(rb[:]), host)
	}
	subject := in.Subject
	if subject == "" {
		switch in.Kind {
		case DSNKindFailure:
			subject = "Delivery Status Notification (Failure)"
		case DSNKindDelay:
			subject = "Delivery Status Notification (Delay)"
		case DSNKindSuccess:
			subject = "Delivery Status Notification (Success)"
		}
	}

	var buf bytes.Buffer
	// Outer headers.
	writeHeader(&buf, "From", in.From)
	to := in.To
	if to == "" {
		// Empty MAIL FROM (null sender, "<>") would loop a bounce; per
		// RFC 5321 §6.1 we deliver the bounce locally as a
		// double-bounce. The To: address is the postmaster.
		to = in.From
	}
	writeHeader(&buf, "To", to)
	writeHeader(&buf, "Subject", subject)
	writeHeader(&buf, "Date", in.Now.UTC().Format(time.RFC1123Z))
	writeHeader(&buf, "Message-ID", msgID)
	writeHeader(&buf, "MIME-Version", "1.0")
	writeHeader(&buf, "Auto-Submitted", "auto-replied")
	writeHeader(&buf, "Content-Type",
		fmt.Sprintf(`multipart/report; report-type=delivery-status; boundary="%s"`, boundary))
	buf.WriteString("\r\n")

	// Part 1: human-readable.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	writeHeader(&buf, "Content-Type", "text/plain; charset=utf-8")
	buf.WriteString("\r\n")
	switch in.Kind {
	case DSNKindFailure:
		fmt.Fprintf(&buf,
			"This is the Herold mail-delivery system.\r\n\r\n"+
				"Your message could not be delivered to one or more recipients.\r\n\r\n"+
				"  %s\r\n\r\n"+
				"Diagnostic: %s\r\n",
			in.FinalRcpt, in.DiagnosticCode)
	case DSNKindDelay:
		until := ""
		if !in.WillRetryUntil.IsZero() {
			until = " until " + in.WillRetryUntil.UTC().Format(time.RFC1123Z)
		}
		fmt.Fprintf(&buf,
			"This is the Herold mail-delivery system.\r\n\r\n"+
				"Your message has not yet been delivered to:\r\n\r\n"+
				"  %s\r\n\r\n"+
				"The system will retry%s.\r\n",
			in.FinalRcpt, until)
	case DSNKindSuccess:
		fmt.Fprintf(&buf,
			"This is the Herold mail-delivery system.\r\n\r\n"+
				"Your message was successfully delivered to:\r\n\r\n"+
				"  %s\r\n",
			in.FinalRcpt)
	}
	buf.WriteString("\r\n")

	// Part 2: machine-readable per-recipient delivery-status.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	writeHeader(&buf, "Content-Type", "message/delivery-status")
	buf.WriteString("\r\n")
	// Per-message fields.
	if in.ReportingMTA != "" {
		writeHeader(&buf, "Reporting-MTA", in.ReportingMTA)
	}
	if in.OriginalEnvID != "" {
		writeHeader(&buf, "Original-Envelope-Id", in.OriginalEnvID)
	}
	writeHeader(&buf, "Arrival-Date", in.Now.UTC().Format(time.RFC1123Z))
	buf.WriteString("\r\n")
	// Per-recipient fields.
	if in.OriginalRcpt != "" {
		writeHeader(&buf, "Original-Recipient", "rfc822;"+in.OriginalRcpt)
	}
	finalRcpt := in.FinalRcpt
	if finalRcpt == "" {
		finalRcpt = in.OriginalRcpt
	}
	writeHeader(&buf, "Final-Recipient", "rfc822;"+finalRcpt)
	writeHeader(&buf, "Action", in.Kind.action())
	if in.StatusCode != "" {
		writeHeader(&buf, "Status", in.StatusCode)
	} else {
		// RFC 3463 default for kind: 2.0.0 success, 4.0.0 delay,
		// 5.0.0 failure.
		switch in.Kind {
		case DSNKindSuccess:
			writeHeader(&buf, "Status", "2.0.0")
		case DSNKindDelay:
			writeHeader(&buf, "Status", "4.0.0")
		default:
			writeHeader(&buf, "Status", "5.0.0")
		}
	}
	if in.RemoteMTA != "" {
		writeHeader(&buf, "Remote-MTA", in.RemoteMTA)
	}
	if in.DiagnosticCode != "" {
		writeHeader(&buf, "Diagnostic-Code", in.DiagnosticCode)
	}
	if in.Kind == DSNKindDelay && !in.WillRetryUntil.IsZero() {
		writeHeader(&buf, "Will-Retry-Until",
			in.WillRetryUntil.UTC().Format(time.RFC1123Z))
	}
	buf.WriteString("\r\n")

	// Part 3: original headers (or full message). We always emit the
	// message/rfc822-headers part with whatever headers we have; an
	// empty headers blob produces an empty (but well-formed) part.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	writeHeader(&buf, "Content-Type", "message/rfc822-headers")
	buf.WriteString("\r\n")
	if len(in.OriginalHeaders) > 0 {
		buf.Write(ensureCRLF(in.OriginalHeaders))
		// Ensure trailing CRLF.
		if !bytes.HasSuffix(buf.Bytes(), []byte("\r\n")) {
			buf.WriteString("\r\n")
		}
	}
	buf.WriteString("\r\n")

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes(), nil
}

// writeHeader writes a single "Name: value\r\n" header line. value is
// folded only if it already contains CRLF; the caller controls folding.
func writeHeader(buf *bytes.Buffer, name, value string) {
	buf.WriteString(name)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}

// newBoundary returns a 16-byte hex-encoded multipart boundary.
func newBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("queue: dsn boundary rand: %w", err)
	}
	return "herold-dsn-" + hex.EncodeToString(b[:]), nil
}

// ensureCRLF returns a CRLF-line-ended copy of in. Bare LF and bare CR
// are normalised; existing CRLF pairs are preserved. The implementation
// matches the canonicaliser in the blob store so DSNs inserted via
// Blobs.Put round-trip cleanly.
func ensureCRLF(in []byte) []byte {
	out := make([]byte, 0, len(in)+8)
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch c {
		case '\r':
			out = append(out, '\r')
			if i+1 < len(in) && in[i+1] == '\n' {
				out = append(out, '\n')
				i++
			} else {
				out = append(out, '\n')
			}
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, c)
		}
	}
	return out
}

// shouldEmitFailureDSN reports whether a failure DSN should be emitted
// for the given queue row given its NOTIFY mask. RFC 3461 §4.1: when
// NEVER is set no DSN is emitted; when NOTIFY is empty (DSNNotifyNone)
// the receiver default is failure-deliver; otherwise FAILURE must be
// present.
func shouldEmitFailureDSN(notify store.DSNNotifyFlags) bool {
	if notify&store.DSNNotifyNever != 0 {
		return false
	}
	if notify == store.DSNNotifyNone {
		return true
	}
	return notify&store.DSNNotifyFailure != 0
}

// shouldEmitSuccessDSN reports whether a success DSN should be
// emitted. The receiver default is success-suppress, so SUCCESS must be
// explicitly requested.
func shouldEmitSuccessDSN(notify store.DSNNotifyFlags) bool {
	if notify&store.DSNNotifyNever != 0 {
		return false
	}
	return notify&store.DSNNotifySuccess != 0
}

// shouldEmitDelayDSN reports whether a delay DSN should be emitted.
// The receiver default is delay-deliver: emit when NEVER is unset and
// either DELAY is set or NOTIFY is DSNNotifyNone.
func shouldEmitDelayDSN(notify store.DSNNotifyFlags) bool {
	if notify&store.DSNNotifyNever != 0 {
		return false
	}
	if notify == store.DSNNotifyNone {
		return true
	}
	return notify&store.DSNNotifyDelay != 0
}
