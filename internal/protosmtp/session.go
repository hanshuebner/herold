package protosmtp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sasl"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// state enumerates the session's top-level state machine positions.
// The progression for one message is Init → Greeted → Envelope →
// Recipients → (optional Data collection) → Complete, with RSET always
// resetting to Greeted without touching TLS / AUTH.
type state int

const (
	stateInit state = iota
	stateGreeted
	stateEnvelope
	stateRecipients
)

// maxCmdLineBytes is the upper limit on a single ESMTP command line,
// tuned a bit above RFC 5321's 512-byte minimum so SMTPUTF8 + long
// parameter lists fit.
const maxCmdLineBytes = 4096

// session owns one accepted TCP/TLS connection. Every method runs on a
// single goroutine; there is no internal concurrency. Cancellation is
// via ctx or a top-level RSET-style error that closes the conn.
type session struct {
	srv      *Server
	mode     ListenerMode
	conn     net.Conn
	tlsConn  *tls.Conn
	reader   *bufio.Reader
	writer   *bufio.Writer
	remoteIP string
	sessID   string

	// protocol state
	st              state
	helo            string
	isEHLO          bool
	tlsEstablished  bool
	tlsCipherSuite  uint16
	tlsVersion      uint16
	tlsServerName   string
	authenticated   bool
	authPrincipal   directory.PrincipalID
	authMechName    string
	commandCount    int
	envelope        envelope
	ctx             context.Context
	cancel          context.CancelFunc
	serverEndpoint  []byte // RFC 5929 tls-server-end-point channel binding
	submissionAllow bool   // true when this listener accepts RCPT for non-local domains after AUTH
}

// envelope accumulates MAIL FROM + RCPT TO state for one in-flight
// transaction. It is reset by RSET / MAIL / successful DATA completion.
type envelope struct {
	mailFrom       string
	mailFromParams mailFromParams
	rcpts          []rcptEntry
	bdatBuf        []byte
}

// mailFromParams records the typed parameters attached to MAIL FROM.
type mailFromParams struct {
	size       int64
	body       string // "7BIT", "8BITMIME", or empty
	smtputf8   bool
	requireTLS bool
	ret        string // "FULL" or "HDRS" or empty
	envid      string
}

// rcptEntry holds one accepted RCPT TO: the raw address, its resolved
// principal (0 for relay acceptance), and the DSN NOTIFY / ORCPT tags.
//
// REQ-DIR-RCPT-07 synthetic recipient: when a plugin returned
// accept-without-principal, synthetic is true, principalID is 0, and
// routeTag carries the plugin's correlation token. The DATA-phase
// delivery path skips mailbox insert / per-recipient Sieve / spam
// classification (unless [smtp.inbound.spam_for_synthetic] is set)
// and dispatches to webhooks-only via the webhook subsystem (Track C).
type rcptEntry struct {
	addr        string
	localPart   string
	domain      string
	principalID directory.PrincipalID
	notify      string
	orcpt       string
	// synthetic flags an accept-without-principal RCPT (REQ-DIR-RCPT-07).
	synthetic bool
	// routeTag is the opaque correlation token returned by the
	// resolve_rcpt plugin; echoed into webhook payloads and audit log.
	routeTag string
	// decisionSource records the path that produced the verdict for
	// the connection-level SMTP log line: "internal" | "plugin:<name>" |
	// "catchall" (REQ-DIR-RCPT-09).
	decisionSource string
}

// runSession is invoked by Server.Serve for each accepted connection.
// It installs the top-level recover, derives a per-session ctx from the
// server's, performs the greeting, and runs the command loop.
//
// implicit / implicitLeaf are populated together for implicit-TLS
// listeners (port 465); the leaf is the x509 certificate the handshake
// served, captured by the GetCertificate wrapper in server.go so we can
// derive the RFC 5929 channel binding without re-parsing the on-wire
// state.
func (s *Server) runSession(raw net.Conn, mode ListenerMode, remoteIP string, implicit *tls.Conn, implicitLeaf *x509.Certificate) {
	sessCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	sess := &session{
		srv:      s,
		mode:     mode,
		conn:     raw,
		remoteIP: remoteIP,
		ctx:      sessCtx,
		cancel:   cancel,
		sessID:   newSessionID(),
	}
	if implicit != nil {
		sess.conn = implicit
		sess.tlsConn = implicit
		sess.tlsEstablished = true
		state := implicit.ConnectionState()
		sess.tlsCipherSuite = state.CipherSuite
		sess.tlsVersion = state.Version
		sess.tlsServerName = state.ServerName
		sess.serverEndpoint = endpointBinding(implicitLeaf)
	}
	sess.reader = bufio.NewReaderSize(sess.conn, maxCmdLineBytes)
	sess.writer = bufio.NewWriterSize(sess.conn, 4096)

	// Top-level recover per STANDARDS.md §6: a handler panic must not
	// kill the server; close the conn after a 421.
	defer func() {
		if rcv := recover(); rcv != nil {
			sess.srv.log.ErrorContext(sessCtx, "smtp session panic",
				slog.String("session_id", sess.sessID),
				slog.String("remote_ip", sess.remoteIP),
				slog.Any("panic", rcv))
			_ = sess.writeReplyLine("421 4.3.0 internal server error")
			_ = sess.writer.Flush()
		}
		_ = sess.conn.Close()
	}()

	// RBL hook at CONNECT (REQ-PROTO-15). Phase 1 ships no-op default.
	if s.opts.RBLHook != nil {
		if reject, reason := s.opts.RBLHook(sessCtx, sess.remoteIP); reject {
			if reason == "" {
				reason = "blocked by policy"
			}
			_ = sess.writeReplyLine("554 5.7.1 " + reason)
			_ = sess.writer.Flush()
			return
		}
	}

	// Greeting.
	greet := fmt.Sprintf("220 %s ESMTP Herold", s.opts.Hostname)
	if err := sess.writeReplyLine(greet); err != nil {
		return
	}
	if err := sess.writer.Flush(); err != nil {
		return
	}

	sess.commandLoop()
}

// commandLoop is the session's top-level read-eval-reply loop. It
// returns when the conn is closed, the ctx is cancelled, the command
// budget is exhausted, or the client sends QUIT.
func (sess *session) commandLoop() {
	for {
		if err := sess.ctx.Err(); err != nil {
			return
		}
		if sess.srv.shutdown.Load() {
			_ = sess.writeReplyLine("421 4.3.2 server shutting down")
			_ = sess.writer.Flush()
			return
		}
		sess.commandCount++
		if sess.commandCount > sess.srv.opts.MaxCommandsPerSession {
			_ = sess.writeReplyLine("421 4.7.0 too many commands; closing connection")
			_ = sess.writer.Flush()
			return
		}

		if err := sess.conn.SetReadDeadline(time.Now().Add(sess.srv.opts.ReadTimeout)); err != nil {
			return
		}
		line, err := sess.readCommandLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			if errors.Is(err, errLineTooLong) {
				_ = sess.writeReplyLine("500 5.5.2 command line too long")
				_ = sess.writer.Flush()
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				_ = sess.writeReplyLine("421 4.4.2 timeout waiting for command")
				_ = sess.writer.Flush()
				return
			}
			return
		}
		if line == "" {
			continue
		}
		verb, rest := splitVerb(line)
		quit := sess.dispatch(verb, rest)
		if err := sess.writer.Flush(); err != nil {
			return
		}
		if quit {
			return
		}
	}
}

// dispatch routes a single command to its handler. Returns true when
// the session should close after the reply is flushed (QUIT or a fatal
// handler outcome).
func (sess *session) dispatch(verb, rest string) bool {
	switch verb {
	case "HELO":
		return sess.cmdHELO(rest)
	case "EHLO":
		return sess.cmdEHLO(rest)
	case "STARTTLS":
		return sess.cmdSTARTTLS(rest)
	case "AUTH":
		return sess.cmdAUTH(rest)
	case "MAIL":
		return sess.cmdMAIL(rest)
	case "RCPT":
		return sess.cmdRCPT(rest)
	case "DATA":
		return sess.cmdDATA()
	case "BDAT":
		return sess.cmdBDAT(rest)
	case "RSET":
		sess.resetEnvelope()
		sess.writeReply("250 2.0.0 reset")
		return false
	case "NOOP":
		sess.writeReply("250 2.0.0 ok")
		return false
	case "QUIT":
		sess.writeReply("221 2.0.0 " + sess.srv.opts.Hostname + " closing connection")
		return true
	case "VRFY":
		sess.writeReply("252 2.5.1 cannot verify user; try sending")
		return false
	case "EXPN":
		sess.writeReply("502 5.5.1 EXPN not supported")
		return false
	case "HELP":
		sess.writeReply("214 2.0.0 see https://example.invalid/herold")
		return false
	default:
		sess.writeReply("500 5.5.1 unrecognised command")
		return false
	}
}

// --- HELO / EHLO ------------------------------------------------------

func (sess *session) cmdHELO(rest string) bool {
	name := strings.TrimSpace(rest)
	if name == "" {
		sess.writeReply("501 5.5.4 HELO requires domain name")
		return false
	}
	sess.helo = name
	sess.isEHLO = false
	sess.st = stateGreeted
	sess.resetEnvelope()
	sess.writeReply(fmt.Sprintf("250 %s", sess.srv.opts.Hostname))
	return false
}

func (sess *session) cmdEHLO(rest string) bool {
	name := strings.TrimSpace(rest)
	if name == "" {
		sess.writeReply("501 5.5.4 EHLO requires domain name")
		return false
	}
	sess.helo = name
	sess.isEHLO = true
	sess.st = stateGreeted
	sess.resetEnvelope()
	lines := sess.ehloExtensions()
	first := fmt.Sprintf("%s Hello %s", sess.srv.opts.Hostname, name)
	replyMulti(sess, 250, "", first, lines)
	return false
}

// ehloExtensions returns the advertised EHLO extensions, ordered so
// STARTTLS / AUTH (the gate-keepers) come first. REQ-PROTO-04: only
// advertise what we implement.
func (sess *session) ehloExtensions() []string {
	var out []string
	if sess.mode != SubmissionImplicitTLS && !sess.tlsEstablished && sess.srv.tls != nil {
		out = append(out, "STARTTLS")
	}
	switch sess.mode {
	case SubmissionSTARTTLS, SubmissionImplicitTLS:
		mechs := sess.authMechanismList()
		if len(mechs) > 0 {
			out = append(out, "AUTH "+strings.Join(mechs, " "))
		}
	}
	out = append(out,
		fmt.Sprintf("SIZE %d", sess.srv.opts.MaxMessageSize),
		"PIPELINING",
		"8BITMIME",
		"SMTPUTF8",
		"CHUNKING",
		"DSN",
		"ENHANCEDSTATUSCODES",
		"REQUIRETLS",
	)
	return out
}

// authMechanismList returns the SASL mechanisms currently offered on
// this session. Plain-text mechanisms (PLAIN / LOGIN) are only offered
// once TLS is established. SCRAM-SHA-256-PLUS is advertised only when
// TLS is up *and* a tls-server-end-point binding is available — RFC 5802
// §6 forbids -PLUS over cleartext, and STANDARDS rule 10 forbids
// advertising a wire extension we cannot honour.
func (sess *session) authMechanismList() []string {
	var out []string
	if sess.tlsEstablished {
		out = append(out, "PLAIN", "LOGIN")
	}
	if sess.srv.passLk != nil {
		out = append(out, "SCRAM-SHA-256")
		if sess.tlsEstablished && len(sess.serverEndpoint) > 0 {
			out = append(out, "SCRAM-SHA-256-PLUS")
		}
	}
	return out
}

// --- STARTTLS ---------------------------------------------------------

func (sess *session) cmdSTARTTLS(rest string) bool {
	if sess.mode == SubmissionImplicitTLS {
		sess.writeReply("503 5.5.1 TLS already active")
		return false
	}
	if strings.TrimSpace(rest) != "" {
		sess.writeReply("501 5.5.4 STARTTLS takes no arguments")
		return false
	}
	if sess.srv.tls == nil {
		sess.writeReply("454 4.7.0 TLS not available")
		return false
	}
	if sess.tlsEstablished {
		sess.writeReply("503 5.5.1 TLS already active")
		return false
	}
	sess.writeReply("220 2.0.0 ready to start TLS")
	if err := sess.writer.Flush(); err != nil {
		return true
	}
	cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(sess.srv.tls, heroldtls.Intermediate, nil))
	tlsConn := tls.Server(sess.conn, cap.Config())
	if err := tlsConn.HandshakeContext(sess.ctx); err != nil {
		sess.srv.log.InfoContext(sess.ctx, "starttls handshake failed",
			slog.String("session_id", sess.sessID),
			slog.String("remote_ip", sess.remoteIP),
			slog.String("err", err.Error()))
		return true
	}
	sess.conn = tlsConn
	sess.tlsConn = tlsConn
	sess.reader = bufio.NewReaderSize(tlsConn, maxCmdLineBytes)
	sess.writer = bufio.NewWriterSize(tlsConn, 4096)
	sess.tlsEstablished = true
	cs := tlsConn.ConnectionState()
	sess.tlsCipherSuite = cs.CipherSuite
	sess.tlsVersion = cs.Version
	sess.tlsServerName = cs.ServerName
	sess.serverEndpoint = endpointBinding(cap.Leaf())
	// Reset protocol state per RFC 3207 §4.2.
	sess.helo = ""
	sess.isEHLO = false
	sess.st = stateInit
	sess.authenticated = false
	sess.authPrincipal = 0
	sess.resetEnvelope()
	return false
}

// --- AUTH -------------------------------------------------------------

func (sess *session) cmdAUTH(rest string) bool {
	if sess.mode == RelayIn {
		sess.writeReply("502 5.5.1 AUTH not available on this port")
		return false
	}
	if sess.st == stateInit {
		sess.writeReply("503 5.5.1 EHLO first")
		return false
	}
	if sess.authenticated {
		sess.writeReply("503 5.5.1 already authenticated")
		return false
	}
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		sess.writeReply("501 5.5.4 AUTH requires a mechanism")
		return false
	}
	mechName := strings.ToUpper(fields[0])
	var ir []byte
	if len(fields) >= 2 {
		if fields[1] == "=" {
			ir = []byte{}
		} else {
			decoded, err := base64.StdEncoding.DecodeString(fields[1])
			if err != nil {
				sess.writeReply("501 5.5.2 AUTH initial response not valid base64")
				return false
			}
			ir = decoded
		}
	}
	mech, ok := sess.buildMechanism(mechName)
	if !ok {
		sess.writeReply("504 5.5.4 mechanism not supported")
		return false
	}
	ctx := sasl.WithTLS(sess.ctx, sess.tlsEstablished)
	if len(sess.serverEndpoint) > 0 {
		ctx = sasl.WithTLSServerEndpoint(ctx, sess.serverEndpoint)
	}

	challenge, done, err := mech.Start(ctx, ir)
	if err != nil {
		sess.writeAuthError(err)
		return false
	}
	for !done {
		if challenge == nil {
			challenge = []byte{}
		}
		sess.writeReply("334 " + base64.StdEncoding.EncodeToString(challenge))
		if err := sess.writer.Flush(); err != nil {
			return true
		}
		if err := sess.conn.SetReadDeadline(time.Now().Add(sess.srv.opts.ReadTimeout)); err != nil {
			return true
		}
		line, err := sess.readCommandLine()
		if err != nil {
			return true
		}
		if line == "*" {
			sess.writeReply("501 5.0.0 AUTH aborted by client")
			return false
		}
		resp, derr := base64.StdEncoding.DecodeString(line)
		if derr != nil {
			sess.writeReply("501 5.5.2 AUTH response not valid base64")
			return false
		}
		challenge, done, err = mech.Next(ctx, resp)
		if err != nil {
			sess.writeAuthError(err)
			return false
		}
	}
	pid, perr := mech.Principal()
	if perr != nil {
		sess.writeReply("535 5.7.8 authentication failed")
		return false
	}
	if len(challenge) > 0 {
		// Final server message (SCRAM v=...). Use a 334 so the client
		// gets the final server data, then an empty client response.
		sess.writeReply("334 " + base64.StdEncoding.EncodeToString(challenge))
		if err := sess.writer.Flush(); err != nil {
			return true
		}
		if err := sess.conn.SetReadDeadline(time.Now().Add(sess.srv.opts.ReadTimeout)); err != nil {
			return true
		}
		if _, err := sess.readCommandLine(); err != nil {
			return true
		}
	}
	sess.authenticated = true
	sess.authPrincipal = pid
	sess.authMechName = mech.Name()
	sess.submissionAllow = true
	sess.writeReply("235 2.7.0 authentication successful")
	return false
}

func (sess *session) buildMechanism(name string) (sasl.Mechanism, bool) {
	switch name {
	case "PLAIN":
		if !sess.tlsEstablished {
			return nil, false
		}
		return sasl.NewPLAIN(sess.srv.dir), true
	case "LOGIN":
		if !sess.tlsEstablished {
			return nil, false
		}
		return sasl.NewLOGIN(sess.srv.dir), true
	case "SCRAM-SHA-256":
		if sess.srv.passLk == nil {
			return nil, false
		}
		return sasl.NewSCRAMSHA256(sess.srv.dir, sess.srv.passLk, false), true
	case "SCRAM-SHA-256-PLUS":
		if sess.srv.passLk == nil || !sess.tlsEstablished || len(sess.serverEndpoint) == 0 {
			return nil, false
		}
		return sasl.NewSCRAMSHA256(sess.srv.dir, sess.srv.passLk, true), true
	default:
		return nil, false
	}
}

func (sess *session) writeAuthError(err error) {
	switch {
	case errors.Is(err, sasl.ErrTLSRequired):
		sess.writeReply("538 5.7.11 encryption required for requested mechanism")
	case errors.Is(err, sasl.ErrMechanismUnsupported):
		sess.writeReply("504 5.5.4 mechanism not supported")
	case errors.Is(err, sasl.ErrChannelBindingMismatch):
		sess.writeReply("535 5.7.8 channel binding mismatch")
	case errors.Is(err, sasl.ErrInvalidMessage):
		sess.writeReply("501 5.5.2 invalid SASL message")
	case errors.Is(err, sasl.ErrProtocolError):
		sess.writeReply("503 5.5.1 SASL protocol error")
	default:
		sess.writeReply("535 5.7.8 authentication failed")
	}
}

// --- MAIL FROM --------------------------------------------------------

func (sess *session) cmdMAIL(rest string) bool {
	if sess.st == stateInit {
		sess.writeReply("503 5.5.1 HELO/EHLO first")
		return false
	}
	if sess.mode == SubmissionSTARTTLS && !sess.tlsEstablished {
		observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "policy").Inc()
		sess.writeReply("530 5.7.0 must issue STARTTLS first")
		return false
	}
	if (sess.mode == SubmissionSTARTTLS || sess.mode == SubmissionImplicitTLS) && !sess.authenticated {
		observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "auth").Inc()
		sess.writeReply("530 5.7.0 authentication required")
		return false
	}
	// Parse "FROM:<addr> [PARAMS]" case-insensitively.
	tail := strings.TrimSpace(rest)
	upper := strings.ToUpper(tail)
	if !strings.HasPrefix(upper, "FROM:") {
		sess.writeReply("501 5.5.4 syntax: MAIL FROM:<addr> [PARAMS]")
		return false
	}
	addrRest := strings.TrimSpace(tail[len("FROM:"):])
	addr, paramsPart, err := extractAngleAddr(addrRest)
	if err != nil {
		sess.writeReply("501 5.5.4 " + err.Error())
		return false
	}
	params, perr := parseMailFromParams(paramsPart)
	if perr != nil {
		sess.writeReply("555 5.5.4 " + perr.Error())
		return false
	}
	if params.size > sess.srv.opts.MaxMessageSize {
		observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "size").Inc()
		sess.writeReply(fmt.Sprintf("552 5.3.4 message size %d exceeds limit %d", params.size, sess.srv.opts.MaxMessageSize))
		return false
	}
	sess.resetEnvelope()
	sess.envelope.mailFrom = addr
	sess.envelope.mailFromParams = params
	sess.st = stateEnvelope
	sess.writeReply("250 2.1.0 sender ok")
	return false
}

// --- RCPT TO ----------------------------------------------------------

func (sess *session) cmdRCPT(rest string) bool {
	if sess.st != stateEnvelope && sess.st != stateRecipients {
		sess.writeReply("503 5.5.1 MAIL first")
		return false
	}
	if len(sess.envelope.rcpts) >= sess.srv.opts.MaxRecipientsPerMessage {
		sess.writeReply("452 4.5.3 too many recipients")
		return false
	}
	tail := strings.TrimSpace(rest)
	upper := strings.ToUpper(tail)
	if !strings.HasPrefix(upper, "TO:") {
		sess.writeReply("501 5.5.4 syntax: RCPT TO:<addr> [PARAMS]")
		return false
	}
	addrRest := strings.TrimSpace(tail[len("TO:"):])
	addr, paramsPart, err := extractAngleAddr(addrRest)
	if err != nil {
		sess.writeReply("501 5.5.4 " + err.Error())
		return false
	}
	rp, perr := parseRcptParams(paramsPart)
	if perr != nil {
		sess.writeReply("555 5.5.4 " + perr.Error())
		return false
	}
	local, domain, ok := splitAddress(addr)
	if !ok {
		sess.writeReply("553 5.1.3 invalid recipient address")
		return false
	}

	// Greylist hook (REQ-PROTO-14).
	if sess.srv.opts.Greylist && sess.srv.opts.GreylistHook != nil {
		if def := sess.srv.opts.GreylistHook(sess.ctx, sess.remoteIP, sess.envelope.mailFrom, addr); def {
			sess.writeReply("451 4.7.1 greylisted; try again later")
			return false
		}
	}

	entry := rcptEntry{
		addr:      addr,
		localPart: local,
		domain:    domain,
		notify:    rp.notify,
		orcpt:     rp.orcpt,
	}

	// REQ-DIR-RCPT-12: the resolve_rcpt hook is inbound-only. Submission
	// listeners follow the existing Phase 2 rule (local-only).
	switch sess.mode {
	case RelayIn:
		accept := sess.resolveInboundRcpt(&entry)
		if !accept {
			return false
		}
	case SubmissionSTARTTLS, SubmissionImplicitTLS:
		// First attempt local resolution; if the address is local,
		// behave exactly like RelayIn so "send-to-self" works. Otherwise
		// refuse: the Phase 2 queue is not wired yet (see ticket scope).
		pid, err := sess.srv.dir.ResolveAddress(sess.ctx, local, domain)
		if err == nil {
			entry.principalID = pid
			entry.decisionSource = "internal"
		} else if errors.Is(err, directory.ErrNotFound) {
			sess.writeReply("550 5.7.1 relaying to remote addresses is not yet enabled (Phase 2)")
			return false
		} else {
			sess.writeReply("451 4.3.0 directory lookup failed")
			return false
		}
	}
	sess.envelope.rcpts = append(sess.envelope.rcpts, entry)
	sess.st = stateRecipients
	sess.writeReply("250 2.1.5 recipient ok")
	return false
}

// rcptResolveOutcome enumerates the three terminal states the
// resolve_rcpt resolution chain can land in for one RCPT TO.
type rcptResolveOutcome int

const (
	// rcptOutcomeAccept signals the caller that entry is fully
	// populated and should be appended to the envelope. The caller
	// writes the 250 reply.
	rcptOutcomeAccept rcptResolveOutcome = iota
	// rcptOutcomeRefused signals that a 4xx / 5xx reply has already
	// been written; the caller must NOT also emit a success reply.
	rcptOutcomeRefused
)

// resolveInboundRcpt drives the REQ-DIR-RCPT-03 resolution order for
// one RCPT TO on the relay-in listener. It mutates entry in-place
// when a verdict is reached and writes the appropriate SMTP reply on
// any non-accept outcome. The outcome tells the caller whether to
// append entry and emit 250.
func (sess *session) resolveInboundRcpt(entry *rcptEntry) bool {
	out := sess.runRcptResolutionChain(entry)
	return out == rcptOutcomeAccept
}

func (sess *session) runRcptResolutionChain(entry *rcptEntry) rcptResolveOutcome {
	dir := sess.srv.dir
	resolver := sess.srv.rcptResolver
	pluginName := sess.srv.rcptPluginNm
	domain := strings.ToLower(entry.domain)
	_, pluginFirst := sess.srv.rcptPluginFor[domain]
	pluginConfigured := resolver != nil && pluginName != ""

	// Step 1: when plugin_first_for_domains contains the recipient
	// domain, the plugin owns the address space — call it first
	// (REQ-DIR-RCPT-03 inversion).
	if pluginConfigured && pluginFirst {
		out, fallthroughChain := sess.applyResolveRcpt(entry)
		if !fallthroughChain {
			return out
		}
		// fallthrough action — fall through to internal resolution.
	}

	// Step 2: internal directory lookup.
	pid, err := dir.ResolveAddress(sess.ctx, entry.localPart, entry.domain)
	if err == nil {
		entry.principalID = pid
		entry.decisionSource = "internal"
		return rcptOutcomeAccept
	}
	if !errors.Is(err, directory.ErrNotFound) && !errors.Is(err, directory.ErrInvalidEmail) {
		sess.writeReply("451 4.3.0 directory lookup failed")
		return rcptOutcomeRefused
	}

	// Step 3: plugin (when not already consulted plugin-first).
	if pluginConfigured && !pluginFirst {
		out, fallthroughChain := sess.applyResolveRcpt(entry)
		if !fallthroughChain {
			return out
		}
	}

	// Step 4: no match.
	sess.writeReply("550 5.1.1 mailbox does not exist")
	return rcptOutcomeRefused
}

// applyResolveRcpt invokes the directory.resolve_rcpt plugin and
// translates the verdict into SMTP replies / entry state.
//
// Returns:
//   - outcome: rcptOutcomeAccept when the plugin returned accept
//     (the caller should append entry and write 250); rcptOutcomeRefused
//     when reject / defer / rate-limit / unknown-principal — the reply
//     line is already written.
//   - fallthroughChain: true only when the plugin returned
//     "fallthrough" so the caller continues the resolution chain. When
//     true, outcome is unspecified.
//
// On accept-with-principal the principal is verified against the
// store; an unknown id is downgraded to defer 4.3.0 per REQ-DIR-RCPT-08.
func (sess *session) applyResolveRcpt(entry *rcptEntry) (rcptResolveOutcome, bool) {
	resolver := sess.srv.rcptResolver
	pluginName := sess.srv.rcptPluginNm
	req := directory.ResolveRcptRequest{
		Recipient: entry.addr,
		Envelope: directory.ResolveRcptEnvelope{
			MailFrom:   sess.envelope.mailFrom,
			HeloDomain: sess.helo,
			SourceIP:   sess.remoteIP,
			Listener:   "inbound",
		},
		Context: directory.ResolveRcptContext{
			PluginName: pluginName,
			RequestID:  sess.sessID,
		},
	}
	dec := resolver.Resolve(sess.ctx, pluginName, req)
	src := "plugin:" + pluginName
	switch dec.Action {
	case directory.ResolveRcptAccept:
		if dec.PrincipalID != nil {
			pid := directory.PrincipalID(*dec.PrincipalID)
			if _, err := sess.srv.store.Meta().GetPrincipalByID(sess.ctx, pid); err != nil {
				sess.srv.log.WarnContext(sess.ctx, "resolve_rcpt accept references unknown principal",
					slog.String("plugin", pluginName),
					slog.String("recipient", entry.addr),
					slog.Uint64("principal_id", *dec.PrincipalID),
					slog.String("err", err.Error()))
				sess.writeReply("450 4.3.0 directory state error")
				entry.decisionSource = src
				return rcptOutcomeRefused, false
			}
			entry.principalID = pid
		} else {
			entry.synthetic = true
		}
		entry.routeTag = dec.RouteTag
		entry.decisionSource = src
		return rcptOutcomeAccept, false
	case directory.ResolveRcptReject:
		code := dec.Code
		if code == "" {
			code = "5.1.1"
		}
		reason := dec.Reason
		if reason == "" {
			reason = "no such recipient"
		}
		sess.writeReply(fmt.Sprintf("550 %s %s", code, reason))
		entry.decisionSource = src
		return rcptOutcomeRefused, false
	case directory.ResolveRcptDefer:
		code := dec.Code
		if code == "" {
			code = "4.5.1"
		}
		reason := dec.Reason
		if reason == "" {
			reason = "try again later"
		}
		sess.writeReply(fmt.Sprintf("450 %s %s", code, reason))
		entry.decisionSource = src
		return rcptOutcomeRefused, false
	case directory.ResolveRcptRateLimited:
		code := dec.Code
		if code == "" {
			code = "4.7.1"
		}
		sess.writeReply(fmt.Sprintf("450 %s try again later", code))
		entry.decisionSource = src
		return rcptOutcomeRefused, false
	case directory.ResolveRcptFallthrough:
		// Continue with the rest of the resolution chain.
		return rcptOutcomeRefused, true
	default:
		// Should never happen; treat as fallthrough.
		return rcptOutcomeRefused, true
	}
}

// --- DATA -------------------------------------------------------------

func (sess *session) cmdDATA() bool {
	if sess.st != stateRecipients || len(sess.envelope.rcpts) == 0 {
		sess.writeReply("503 5.5.1 no recipients")
		return false
	}
	sess.writeReply("354 end data with <CR><LF>.<CR><LF>")
	if err := sess.writer.Flush(); err != nil {
		return true
	}
	_ = sess.conn.SetReadDeadline(time.Now().Add(sess.srv.opts.DataTimeout))
	body, err := readDotStream(sess.reader, sess.srv.opts.MaxMessageSize)
	_ = sess.conn.SetReadDeadline(time.Time{})
	if err != nil {
		if errors.Is(err, errMessageTooLarge) {
			observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "size").Inc()
			sess.writeReply("552 5.3.4 message too large")
			sess.resetEnvelope()
			return false
		}
		if errors.Is(err, io.EOF) {
			return true
		}
		sess.writeReply("451 4.3.0 I/O error reading DATA")
		sess.resetEnvelope()
		return false
	}
	sess.finishMessage(body)
	return false
}

// --- BDAT -------------------------------------------------------------

func (sess *session) cmdBDAT(rest string) bool {
	if sess.st != stateRecipients || len(sess.envelope.rcpts) == 0 {
		// RFC 3030 §4: BDAT is only valid after RCPT.
		sess.writeReply("503 5.5.1 no recipients")
		return false
	}
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		sess.writeReply("501 5.5.4 BDAT requires chunk size")
		return false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || n < 0 {
		sess.writeReply("501 5.5.4 BDAT bad chunk size")
		return false
	}
	last := false
	if len(fields) >= 2 && strings.EqualFold(fields[1], "LAST") {
		last = true
	}
	// Existing accumulator lives on session; initialise lazily.
	if sess.envelope.bdatBuf == nil {
		sess.envelope.bdatBuf = make([]byte, 0, n)
	}
	// Per-session size cap.
	if int64(len(sess.envelope.bdatBuf))+n > sess.srv.opts.MaxMessageSize {
		// Consume n bytes before rejecting so the framing remains valid.
		_ = sess.conn.SetReadDeadline(time.Now().Add(sess.srv.opts.DataTimeout))
		_, _ = io.CopyN(io.Discard, sess.reader, n)
		_ = sess.conn.SetReadDeadline(time.Time{})
		observe.SMTPMessagesRejectedTotal.WithLabelValues(sess.mode.String(), "size").Inc()
		sess.writeReply("552 5.3.4 message too large")
		sess.resetEnvelope()
		return false
	}
	chunk := make([]byte, n)
	_ = sess.conn.SetReadDeadline(time.Now().Add(sess.srv.opts.DataTimeout))
	_, rerr := io.ReadFull(sess.reader, chunk)
	_ = sess.conn.SetReadDeadline(time.Time{})
	if rerr != nil {
		return true
	}
	sess.envelope.bdatBuf = append(sess.envelope.bdatBuf, chunk...)
	if !last {
		sess.writeReply(fmt.Sprintf("250 2.0.0 %d bytes received", n))
		return false
	}
	body := sess.envelope.bdatBuf
	sess.envelope.bdatBuf = nil
	sess.finishMessage(body)
	return false
}

// --- session helpers --------------------------------------------------

func (sess *session) resetEnvelope() {
	sess.envelope = envelope{}
	if sess.st == stateEnvelope || sess.st == stateRecipients {
		sess.st = stateGreeted
	}
}

// writeReply writes a single-line reply and flushes later at the
// command-loop boundary. Used for the overwhelming-majority single-line
// responses.
func (sess *session) writeReply(line string) {
	_ = sess.writeReplyLine(line)
}

// writeReplyLine writes one reply line with a write deadline. The line
// is emitted as-is; callers supply the numeric code + enhanced status.
func (sess *session) writeReplyLine(line string) error {
	_ = sess.conn.SetWriteDeadline(time.Now().Add(sess.srv.opts.WriteTimeout))
	defer sess.conn.SetWriteDeadline(time.Time{})
	if !strings.HasSuffix(line, "\r\n") {
		line = line + "\r\n"
	}
	_, err := sess.writer.WriteString(line)
	return err
}

// readCommandLine reads one CRLF-terminated command. It caps the line
// at maxCmdLineBytes and returns errLineTooLong on overrun.
func (sess *session) readCommandLine() (string, error) {
	var buf strings.Builder
	for {
		frag, isPrefix, err := sess.reader.ReadLine()
		if err != nil {
			return "", err
		}
		if buf.Len()+len(frag) > maxCmdLineBytes {
			// Drain the rest of the oversized line so the conn stays
			// in sync, then report.
			for isPrefix {
				_, isPrefix, err = sess.reader.ReadLine()
				if err != nil {
					return "", err
				}
			}
			return "", errLineTooLong
		}
		buf.Write(frag)
		if !isPrefix {
			break
		}
	}
	return buf.String(), nil
}

// splitVerb extracts the first whitespace-delimited token (upper-cased)
// and the remainder. Used by dispatch.
func splitVerb(line string) (verb, rest string) {
	line = strings.TrimSpace(line)
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return strings.ToUpper(line), ""
	}
	return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
}

// extractAngleAddr returns the address inside <...>, the remainder
// after the closing '>', and an error for malformed input. Accepts a
// bare address when there are no angle brackets (tolerant).
func extractAngleAddr(s string) (addr, rest string, err error) {
	if strings.HasPrefix(s, "<") {
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return "", "", errors.New("missing '>'")
		}
		inner := s[1:end]
		// RFC 5321 path syntax does not permit nested '<' or '>' inside
		// the address. Reject malformed input rather than silently
		// returning an address that still contains angle brackets.
		if strings.ContainsAny(inner, "<>") {
			return "", "", errors.New("nested angle brackets in address")
		}
		return inner, strings.TrimSpace(s[end+1:]), nil
	}
	// Bare form: first token.
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, "", nil
	}
	return s[:i], strings.TrimSpace(s[i+1:]), nil
}

// splitAddress returns local/domain for local@domain. Null addresses
// (MAIL FROM:<>) return ok == false.
func splitAddress(addr string) (local, domain string, ok bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", false
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", "", false
	}
	return addr[:at], addr[at+1:], true
}

// parseMailFromParams parses the ESMTP extension parameters attached to
// a MAIL FROM: command. Keys are case-insensitive; unknown keys fail.
func parseMailFromParams(s string) (mailFromParams, error) {
	var out mailFromParams
	for _, tok := range splitParams(s) {
		if tok == "" {
			continue
		}
		key, val, _ := strings.Cut(tok, "=")
		switch strings.ToUpper(key) {
		case "SIZE":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 {
				return mailFromParams{}, fmt.Errorf("bad SIZE=%q", val)
			}
			out.size = n
		case "BODY":
			u := strings.ToUpper(val)
			switch u {
			case "7BIT", "8BITMIME":
				out.body = u
			default:
				return mailFromParams{}, fmt.Errorf("bad BODY=%q", val)
			}
		case "SMTPUTF8":
			out.smtputf8 = true
		case "REQUIRETLS":
			out.requireTLS = true
		case "RET":
			u := strings.ToUpper(val)
			switch u {
			case "FULL", "HDRS":
				out.ret = u
			default:
				return mailFromParams{}, fmt.Errorf("bad RET=%q", val)
			}
		case "ENVID":
			out.envid = val
		case "AUTH":
			// RFC 4954 AUTH= parameter: recorded but not enforced in
			// this wave (we do not re-emit on outbound).
		default:
			return mailFromParams{}, fmt.Errorf("unknown parameter %q", key)
		}
	}
	return out, nil
}

// rcptParams is the typed view of NOTIFY / ORCPT DSN parameters.
type rcptParams struct {
	notify string
	orcpt  string
}

func parseRcptParams(s string) (rcptParams, error) {
	var out rcptParams
	for _, tok := range splitParams(s) {
		if tok == "" {
			continue
		}
		key, val, _ := strings.Cut(tok, "=")
		switch strings.ToUpper(key) {
		case "NOTIFY":
			out.notify = strings.ToUpper(val)
		case "ORCPT":
			out.orcpt = val
		default:
			return rcptParams{}, fmt.Errorf("unknown parameter %q", key)
		}
	}
	return out, nil
}

// splitParams splits on whitespace, preserving tokens that embed '='.
func splitParams(s string) []string {
	return strings.Fields(strings.TrimSpace(s))
}

// readDotStream reads SMTP DATA body bytes (RFC 5321 §4.1.1.4): lines
// are CRLF-terminated, leading '.' on a data line is unescaped, and the
// body terminates on a bare ".\r\n". Returns errMessageTooLarge when
// the accumulated body exceeds maxSize. The returned body uses CRLF
// line endings.
func readDotStream(r *bufio.Reader, maxSize int64) ([]byte, error) {
	buf := make([]byte, 0, 8192)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		// Strip trailing CRLF/LF for inspection; we re-append CRLF
		// canonically.
		stripped := line
		if n := len(stripped); n >= 2 && stripped[n-2] == '\r' && stripped[n-1] == '\n' {
			stripped = stripped[:n-2]
		} else if n := len(stripped); n >= 1 && stripped[n-1] == '\n' {
			stripped = stripped[:n-1]
		}
		// End-of-data marker.
		if len(stripped) == 1 && stripped[0] == '.' {
			return buf, nil
		}
		// Dot un-escape.
		if len(stripped) > 0 && stripped[0] == '.' {
			stripped = stripped[1:]
		}
		if int64(len(buf))+int64(len(stripped))+2 > maxSize {
			// Drain to the real terminator so framing stays intact.
			for {
				nx, err := r.ReadBytes('\n')
				if err != nil {
					return nil, errMessageTooLarge
				}
				if len(nx) >= 3 && nx[0] == '.' && (nx[1] == '\r' || nx[1] == '\n') {
					break
				}
				// Cheap bail: after N drain rounds treat as closed.
				if len(nx) > 0 && nx[len(nx)-1] == '\n' && string(nx[:len(nx)-1]) == "." {
					break
				}
			}
			return nil, errMessageTooLarge
		}
		buf = append(buf, stripped...)
		buf = append(buf, '\r', '\n')
	}
}

// errLineTooLong and errMessageTooLarge are sentinels used to map I/O
// outcomes onto SMTP error codes without leaking stack traces through
// the wire reply.
var (
	errLineTooLong     = errors.New("protosmtp: command line too long")
	errMessageTooLarge = errors.New("protosmtp: message size exceeds limit")
)

// replyMulti emits a multi-line 250 reply for EHLO. firstHeading is the
// single-line greeting (after code-prefix "250-"); lines are rendered
// with continuation "250-" except the last which uses "250 ".
func replyMulti(sess *session, code int, enhanced, firstHeading string, lines []string) {
	n := len(lines)
	if n == 0 {
		sess.writeReply(fmt.Sprintf("%d %s", code, firstHeading))
		return
	}
	sess.writeReply(fmt.Sprintf("%d-%s", code, firstHeading))
	for i, l := range lines {
		if i == n-1 {
			if enhanced != "" {
				sess.writeReply(fmt.Sprintf("%d %s %s", code, enhanced, l))
			} else {
				sess.writeReply(fmt.Sprintf("%d %s", code, l))
			}
		} else {
			sess.writeReply(fmt.Sprintf("%d-%s", code, l))
		}
	}
}

// endpointBinding is the RFC 5929 tls-server-end-point binding value
// for SCRAM-PLUS. Computed from the leaf certificate that the TLS
// handshake actually served (captured via the GetCertificate wrapper
// installed by serverTLSConfig); the digest is hashed per RFC 5929 §4.1
// (SHA-256 family by default, SHA-384/512 for stronger cert signatures).
//
// Returns nil when no leaf certificate is available; SCRAM-SHA-256-PLUS
// then refuses gracefully with ErrChannelBindingMismatch.
func endpointBinding(leaf *x509.Certificate) []byte {
	cb, err := sasl.TLSServerEndpoint(leaf)
	if err != nil {
		return nil
	}
	return cb
}

// newSessionID returns a short random-ish identifier usable for log
// correlation. It is not cryptographically strong; the clock + counter
// pair is enough to disambiguate concurrent sessions in tests.
func newSessionID() string {
	var b [8]byte
	h := sha256.Sum256(fmt.Appendf(nil, "%d-%d", time.Now().UnixNano(), nextSessionCounter()))
	copy(b[:], h[:8])
	return strings.ToLower(fmt.Sprintf("%x", b))
}

// sessionCounter disambiguates session IDs produced within the same
// nanosecond. Atomic so concurrent runSession goroutines can increment
// without a mutex.
var sessionCounter atomic.Uint64

func nextSessionCounter() uint64 {
	return sessionCounter.Add(1)
}
