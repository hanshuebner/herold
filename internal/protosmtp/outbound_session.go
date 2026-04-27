package protosmtp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// outboundSession is the per-MX SMTP exchange. One session is built per
// dial attempt; the underlying conn is owned exclusively by the session
// for its lifetime and closed in deliverToMX's defer.
type outboundSession struct {
	conn          net.Conn
	reader        *bufio.Reader
	writer        *bufio.Writer
	clk           timeSource
	policyDomain  string
	mxHost        string
	greetingLine  string
	extensions    map[string]string // upper-case extension name -> params
	enhancedCodes bool              // remote advertises ENHANCEDSTATUSCODES
	pipelining    bool              // remote advertises PIPELINING
}

// timeSource is the subset of clock.Clock the session needs. We avoid
// depending on the full Clock interface here to keep the session
// constructor self-contained for testing.
type timeSource interface {
	Now() time.Time
}

// newOutboundSession wraps a freshly-dialled net.Conn. The caller must
// have already set the SessionTimeout deadline on the conn; the session
// reads the SMTP greeting on construction.
func newOutboundSession(conn net.Conn, mxHost, policyDomain string, clk timeSource) *outboundSession {
	return &outboundSession{
		conn:         conn,
		reader:       bufio.NewReaderSize(conn, 4096),
		writer:       bufio.NewWriterSize(conn, 4096),
		policyDomain: policyDomain,
		mxHost:       mxHost,
		clk:          clk,
		extensions:   map[string]string{},
	}
}

// hasExtension reports whether the remote advertised name in its EHLO.
// Comparison is case-insensitive on the bare name (parameters ignored).
func (s *outboundSession) hasExtension(name string) bool {
	_, ok := s.extensions[strings.ToUpper(name)]
	return ok
}

// command writes one CRLF-terminated SMTP command line. It does not read
// a reply; pipelining-aware callers issue several commands in a row and
// then drain replies in order.
func (s *outboundSession) command(ctx context.Context, line string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.writer.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return s.writer.Flush()
}

// readReply consumes one full SMTP reply (one or more continuation lines
// terminated by a non-"-" prefix) and returns the numeric code, optional
// enhanced status, and the joined human-readable text. The session
// deadline applies to every line read.
func (s *outboundSession) readReply(ctx context.Context) (int, string, string, error) {
	var code int
	var enhanced string
	var lines []string
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", "", err
		}
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && len(lines) > 0 {
				break
			}
			return 0, "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			return 0, "", "", fmt.Errorf("short SMTP reply: %q", line)
		}
		c, perr := strconv.Atoi(line[:3])
		if perr != nil {
			return 0, "", "", fmt.Errorf("bad SMTP reply code: %q", line[:3])
		}
		code = c
		text := line[4:]
		// Enhanced status codes live at the start of the text portion
		// when ENHANCEDSTATUSCODES is in effect, e.g. "250 2.1.0 ok".
		if s.enhancedCodes && enhanced == "" {
			if eh, rest, ok := parseEnhanced(text); ok {
				enhanced = eh
				text = rest
			}
		}
		lines = append(lines, text)
		if line[3] == ' ' {
			break
		}
		if line[3] != '-' {
			return 0, "", "", fmt.Errorf("bad SMTP continuation marker in %q", line)
		}
	}
	return code, enhanced, strings.Join(lines, "\n"), nil
}

// parseEnhanced extracts an "X.Y.Z" enhanced status (RFC 3463) from the
// start of text. Returns the rest of the text without the prefix on hit.
func parseEnhanced(text string) (string, string, bool) {
	// Smallest valid form: "0.0.0" — three digits separated by two dots.
	sp := strings.IndexByte(text, ' ')
	if sp < 0 {
		return "", text, false
	}
	tok := text[:sp]
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", text, false
	}
	for _, p := range parts {
		if p == "" {
			return "", text, false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return "", text, false
		}
	}
	return tok, text[sp+1:], true
}

// readGreeting reads the 220 banner. We accept any 2xx; non-2xx maps to
// a transient outcome at the call site.
func (s *outboundSession) readGreeting(ctx context.Context) (int, string, error) {
	code, _, text, err := s.readReply(ctx)
	if err != nil {
		return 0, "", err
	}
	s.greetingLine = text
	return code, text, nil
}

// ehlo issues EHLO and parses the advertised extensions. On a non-250
// reply the caller may fall back to HELO; we report the failure code so
// the caller can decide.
func (s *outboundSession) ehlo(ctx context.Context, hostName string) (int, string, error) {
	if err := s.command(ctx, "EHLO "+hostName); err != nil {
		return 0, "", err
	}
	code, _, text, err := s.readReply(ctx)
	if err != nil {
		return 0, "", err
	}
	if code == 250 {
		s.parseExtensions(text)
	}
	return code, text, nil
}

// helo is the RFC 5321 fallback for remotes that reject EHLO.
func (s *outboundSession) helo(ctx context.Context, hostName string) (int, string, error) {
	if err := s.command(ctx, "HELO "+hostName); err != nil {
		return 0, "", err
	}
	code, _, text, err := s.readReply(ctx)
	if err != nil {
		return 0, "", err
	}
	return code, text, nil
}

// parseExtensions reads the EHLO greeting body. The first line is the
// server's identity (we discard it); subsequent lines are extension
// names with optional parameters.
func (s *outboundSession) parseExtensions(text string) {
	s.extensions = map[string]string{}
	s.enhancedCodes = false
	s.pipelining = false
	for i, line := range strings.Split(text, "\n") {
		if i == 0 {
			continue // identity line
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var name, params string
		if sp := strings.IndexAny(line, " \t"); sp >= 0 {
			name = strings.ToUpper(line[:sp])
			params = strings.TrimSpace(line[sp+1:])
		} else {
			name = strings.ToUpper(line)
		}
		s.extensions[name] = params
		switch name {
		case "ENHANCEDSTATUSCODES":
			s.enhancedCodes = true
		case "PIPELINING":
			s.pipelining = true
		}
	}
}

// upgradeConn replaces the underlying conn with a TLS conn after a
// successful STARTTLS handshake. The buffered reader/writer are
// re-instantiated against the upgraded conn.
func (s *outboundSession) upgradeConn(tlsConn *tls.Conn) {
	s.conn = tlsConn
	s.reader = bufio.NewReaderSize(tlsConn, 4096)
	s.writer = bufio.NewWriterSize(tlsConn, 4096)
}

// close terminates the session. Best-effort QUIT then close.
func (s *outboundSession) close(ctx context.Context) {
	_ = s.command(ctx, "QUIT")
	// Drain at most one reply line for the QUIT to avoid leaving
	// half-read data on the conn. Errors are ignored; we are about to
	// close the socket.
	_, _, _, _ = s.readReply(ctx)
	_ = s.conn.Close()
}

// deliverToMX is the per-candidate workhorse: dial, greet, EHLO, optional
// STARTTLS + EHLO, MAIL/RCPT/DATA. Outcome and policy are populated on
// every return path so the caller never sees a zero-value Status.
func (c *Client) deliverToMX(
	ctx context.Context,
	req DeliveryRequest,
	domain string,
	cand mxCandidate,
	policy *MTASTSPolicy,
	startedAt time.Time,
) DeliveryOutcome {
	out := DeliveryOutcome{
		MXHost:      cand.host,
		AttemptedAt: startedAt,
	}

	// Dial with a per-candidate deadline.
	dialCtx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()
	conn, derr := c.dialMX(dialCtx, cand.host)
	if derr != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("dial %s: %s", cand.host, derr.Error())
		return out
	}
	// Apply the session-wide deadline; every read/write inherits it.
	deadline := c.clk.Now().Add(c.sessionTimeout)
	_ = conn.SetDeadline(deadline)

	sess := newOutboundSession(conn, cand.host, domain, c.clk)
	defer sess.close(ctx)

	// Greeting.
	gcode, gtext, err := sess.readGreeting(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("greeting read: %s", err.Error())
		return out
	}
	if gcode/100 != 2 {
		// Greeting refusal: 4xx → transient; 5xx → permanent.
		return mapReplyOutcome(out, gcode, "", gtext, "greeting")
	}

	// EHLO.
	ecode, etext, err := sess.ehlo(ctx, c.hostName)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("ehlo: %s", err.Error())
		return out
	}
	if ecode != 250 {
		// Try HELO fallback for ancient remotes.
		hcode, htext, herr := sess.helo(ctx, c.hostName)
		if herr != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("helo: %s", herr.Error())
			return out
		}
		if hcode != 250 {
			return mapReplyOutcome(out, hcode, "", htext, "helo")
		}
	}

	// TLS upgrade (optional, policy-driven).
	tlsConn, decision, terr := c.upgradeTLS(ctx, sess, cand.host, policy, req.REQUIRETLS)
	if terr != nil {
		// Strict-policy TLS failure → permanent; opportunistic failures
		// already returned (used=false, policy="none") with terr=nil.
		switch {
		case errors.Is(terr, errPolicyNoSTARTTLS):
			out.Status = DeliveryPermanent
			out.SMTPCode = 530
			out.EnhancedCode = "5.7.10"
			out.Diagnostic = fmt.Sprintf("STARTTLS not offered by %s but required by policy", cand.host)
		case errors.Is(terr, errDANEMismatch):
			out.Status = DeliveryPermanent
			out.SMTPCode = 554
			out.EnhancedCode = "5.7.0"
			out.Diagnostic = fmt.Sprintf("DANE TLSA validation failed for %s", cand.host)
		case errors.Is(terr, errTLSNegotiation):
			out.Status = DeliveryPermanent
			out.SMTPCode = 554
			out.EnhancedCode = "5.7.0"
			out.Diagnostic = fmt.Sprintf("TLS negotiation failed for %s under strict policy: %s", cand.host, terr.Error())
		default:
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("tls negotiation: %s", terr.Error())
		}
		return out
	}
	if tlsConn != nil {
		sess.upgradeConn(tlsConn)
		// Renew session deadline against the new conn.
		_ = tlsConn.SetDeadline(deadline)
		// EHLO again per RFC 3207 §4.2.
		ecode2, etext2, err := sess.ehlo(ctx, c.hostName)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("post-tls ehlo: %s", err.Error())
			return out
		}
		if ecode2 != 250 {
			return mapReplyOutcome(out, ecode2, "", etext2, "post-tls ehlo")
		}
	}
	out.TLSUsed = decision.used
	out.TLSPolicy = decision.policy
	if !decision.used {
		out.TLSPolicy = "none"
	}

	_ = etext // silence unused (kept for future logging)

	// REQUIRETLS gate after TLS negotiation: even if STARTTLS was
	// advertised, if the upgrade did not happen for some reason and
	// REQUIRETLS is set, refuse.
	if req.REQUIRETLS && !decision.used {
		out.Status = DeliveryPermanent
		out.SMTPCode = 530
		out.EnhancedCode = "5.7.10"
		out.Diagnostic = fmt.Sprintf("REQUIRETLS=true and TLS not negotiated with %s", cand.host)
		return out
	}

	// MAIL FROM, RCPT TO, DATA. With PIPELINING we issue all three at
	// once, then read replies in order. Without it, we serialise.
	mailLine := buildMailFromLine(req, sess)
	rcptLine := buildRcptToLine(req, sess)

	if sess.pipelining {
		if err := sess.command(ctx, mailLine); err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("mail write: %s", err.Error())
			return out
		}
		if err := sess.command(ctx, rcptLine); err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("rcpt write: %s", err.Error())
			return out
		}
		if err := sess.command(ctx, "DATA"); err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("data write: %s", err.Error())
			return out
		}
		// Drain three replies in order: MAIL, RCPT, DATA.
		mc, me, mt, err := sess.readReply(ctx)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("mail reply: %s", err.Error())
			return out
		}
		if mc/100 != 2 {
			return mapReplyOutcome(out, mc, me, mt, "mail from")
		}
		rc, re, rt, err := sess.readReply(ctx)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("rcpt reply: %s", err.Error())
			return out
		}
		if rc/100 != 2 {
			return mapReplyOutcome(out, rc, re, rt, "rcpt to")
		}
		dc, de, dt, err := sess.readReply(ctx)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("data reply: %s", err.Error())
			return out
		}
		if dc != 354 {
			return mapReplyOutcome(out, dc, de, dt, "data")
		}
	} else {
		if err := sess.command(ctx, mailLine); err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("mail write: %s", err.Error())
			return out
		}
		mc, me, mt, err := sess.readReply(ctx)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("mail reply: %s", err.Error())
			return out
		}
		if mc/100 != 2 {
			return mapReplyOutcome(out, mc, me, mt, "mail from")
		}
		if err := sess.command(ctx, rcptLine); err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("rcpt write: %s", err.Error())
			return out
		}
		rc, re, rt, err := sess.readReply(ctx)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("rcpt reply: %s", err.Error())
			return out
		}
		if rc/100 != 2 {
			return mapReplyOutcome(out, rc, re, rt, "rcpt to")
		}
		if err := sess.command(ctx, "DATA"); err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("data write: %s", err.Error())
			return out
		}
		dc, de, dt, err := sess.readReply(ctx)
		if err != nil {
			out.Status = DeliveryTransient
			out.Diagnostic = fmt.Sprintf("data reply: %s", err.Error())
			return out
		}
		if dc != 354 {
			return mapReplyOutcome(out, dc, de, dt, "data")
		}
	}

	// Body bytes followed by ".\r\n" terminator. We dot-stuff inline.
	if err := writeDotStuffed(sess.writer, req.Message); err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("data body write: %s", err.Error())
		return out
	}
	if _, err := sess.writer.WriteString(".\r\n"); err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("data terminator write: %s", err.Error())
		return out
	}
	if err := sess.writer.Flush(); err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("data flush: %s", err.Error())
		return out
	}
	fc, fe, ft, err := sess.readReply(ctx)
	if err != nil {
		out.Status = DeliveryTransient
		out.Diagnostic = fmt.Sprintf("data final reply: %s", err.Error())
		return out
	}
	if fc/100 == 2 {
		out.Status = DeliverySuccess
		out.SMTPCode = fc
		out.EnhancedCode = fe
		out.Diagnostic = ft
		c.log.InfoContext(ctx, "outbound: delivered",
			slog.String("domain", domain),
			slog.String("mx", cand.host),
			slog.String("rcpt", req.RcptTo),
			slog.Bool("tls", out.TLSUsed),
			slog.String("tls_policy", out.TLSPolicy),
			slog.String("message_id", req.MessageID))
		return out
	}
	return mapReplyOutcome(out, fc, fe, ft, "data final")
}

// dialMX dials the SMTP service on host (port 25). When dialFunc is set
// (tests), it is called directly with "host:25" and is expected to do
// its own IP resolution / mapping. In production, we resolve A/AAAA via
// the Resolver and dial each address until one succeeds; the Resolver
// call is the only DNS step (REQ-PROTO: never call net.Lookup* directly).
func (c *Client) dialMX(ctx context.Context, host string) (net.Conn, error) {
	if c.dialFunc != nil {
		return c.dialFunc(ctx, "tcp", net.JoinHostPort(host, "25"))
	}
	ips, err := c.resolver.IPLookup(ctx, host)
	if err != nil {
		// No address records — surface as a dial-shaped error so the
		// caller maps it to Transient (next-MX fallback).
		return nil, fmt.Errorf("ip lookup %s: %w", host, err)
	}
	var lastErr error
	for _, ip := range ips {
		addr := net.JoinHostPort(ip.String(), "25")
		conn, derr := c.dialer.DialContext(ctx, "tcp", addr)
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses for %s", host)
	}
	return nil, lastErr
}

// mapReplyOutcome maps a non-2xx SMTP reply onto a DeliveryOutcome.
// REQ-PROTO-04 calls out the 4xx → Transient, 5xx → Permanent split.
// Phase tags the diagnostic with the conversational phase the failure
// happened in (e.g. "rcpt to") for log + DSN clarity.
func mapReplyOutcome(out DeliveryOutcome, code int, enhanced, text, phase string) DeliveryOutcome {
	out.SMTPCode = code
	out.EnhancedCode = enhanced
	switch code / 100 {
	case 4:
		out.Status = DeliveryTransient
	case 5:
		out.Status = DeliveryPermanent
	default:
		// Unknown family — treat as transient so the queue retries
		// rather than hard-bouncing on a code we cannot interpret.
		out.Status = DeliveryTransient
	}
	out.Diagnostic = fmt.Sprintf("%s: %d %s%s", phase, code,
		enhancedPrefix(enhanced), trimReplyText(text))
	return out
}

func enhancedPrefix(e string) string {
	if e == "" {
		return ""
	}
	return e + " "
}

func trimReplyText(text string) string {
	t := strings.TrimSpace(text)
	if len(t) > 200 {
		t = t[:200] + "…"
	}
	return t
}

// buildMailFromLine renders MAIL FROM with the parameters the remote
// advertised. The submitter's own MAIL FROM parameters are not propagated
// 1:1 — only the ones our outbound spec calls out (BODY=, SMTPUTF8,
// REQUIRETLS, RET=, ENVID=).
func buildMailFromLine(req DeliveryRequest, sess *outboundSession) string {
	var b strings.Builder
	b.WriteString("MAIL FROM:<")
	b.WriteString(req.MailFrom)
	b.WriteString(">")
	if req.EightBitMIME && sess.hasExtension("8BITMIME") {
		b.WriteString(" BODY=8BITMIME")
	}
	if req.SMTPUTF8 && sess.hasExtension("SMTPUTF8") {
		b.WriteString(" SMTPUTF8")
	}
	if req.REQUIRETLS && sess.hasExtension("REQUIRETLS") {
		b.WriteString(" REQUIRETLS")
	}
	if req.EnvID != "" && sess.hasExtension("DSN") {
		b.WriteString(" ENVID=")
		b.WriteString(req.EnvID)
	}
	return b.String()
}

// buildRcptToLine renders RCPT TO with optional NOTIFY parameter when the
// remote advertises DSN.
func buildRcptToLine(req DeliveryRequest, sess *outboundSession) string {
	var b strings.Builder
	b.WriteString("RCPT TO:<")
	b.WriteString(req.RcptTo)
	b.WriteString(">")
	if req.Notify != "" && sess.hasExtension("DSN") {
		b.WriteString(" NOTIFY=")
		b.WriteString(req.Notify)
	}
	return b.String()
}

// writeDotStuffed copies body to w with RFC 5321 §4.5.2 transparency: a
// line beginning with '.' is escaped to "..". Lines are CRLF-terminated;
// callers ensure the input already has CRLF endings.
func writeDotStuffed(w *bufio.Writer, body []byte) error {
	// Split into lines on "\r\n" (or bare "\n" defensively). For each
	// line, prepend an extra "." when the line itself begins with '.'.
	for len(body) > 0 {
		idx := bytes.IndexByte(body, '\n')
		var line []byte
		if idx < 0 {
			line = body
			body = nil
		} else {
			line = body[:idx+1]
			body = body[idx+1:]
		}
		if len(line) > 0 && line[0] == '.' {
			if err := w.WriteByte('.'); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	return nil
}

// _ keeps the store import alive on builds that elide unused imports;
// outbound_session.go references store via DeliveryOutcome's neighbours.
var _ = store.TLSRPTFailureType(0)
