package protomanagesieve

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// sessionState enumerates the RFC 5804 states the session can be in.
// The post-LOGOUT terminal state is "logout".
type sessionState int

const (
	stateUnauth sessionState = iota
	stateAuthed
	stateLogout
)

// session is one ManageSieve connection. State + I/O channels are
// per-instance; concurrency is single-goroutine per connection.
type session struct {
	s         *Server
	conn      net.Conn
	br        *bufio.Reader
	bw        *bufio.Writer
	tlsActive bool
	state     sessionState
	logger    *slog.Logger
	pid       store.PrincipalID
	remote    string

	// serverEndpoint is the RFC 5929 tls-server-end-point binding for
	// SCRAM-SHA-256-PLUS. Populated after STARTTLS or on implicit-TLS
	// constructors (none today; ManageSieve runs plaintext-on-accept).
	serverEndpoint []byte
}

func newSession(s *Server, c net.Conn, tlsActive bool) *session {
	return &session{
		s:         s,
		conn:      c,
		br:        bufio.NewReaderSize(c, 16*1024),
		bw:        bufio.NewWriter(c),
		tlsActive: tlsActive,
		state:     stateUnauth,
		logger:    s.logger.With("remote", c.RemoteAddr().String()),
		remote:    c.RemoteAddr().String(),
	}
}

// run drives the session: emit greeting, read + dispatch commands,
// terminate on LOGOUT / error / ctx cancel.
func (ses *session) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := ses.writeGreeting(); err != nil {
		ses.logger.Debug("protomanagesieve: greeting", "err", err)
		return
	}
	for {
		if ses.state == stateLogout {
			return
		}
		_ = ses.conn.SetReadDeadline(time.Now().Add(ses.s.opts.IdleTimeout))
		cmd, err := ses.readCommand()
		if err != nil {
			if !isClose(err) {
				ses.logger.Debug("protomanagesieve: read", "err", err)
			}
			return
		}
		if cmd.Verb == "" {
			continue
		}
		if err := ses.dispatch(ctx, cmd); err != nil {
			ses.logger.Debug("protomanagesieve: dispatch", "err", err)
			return
		}
	}
}

// writeGreeting emits the RFC 5804 §1.7 greeting: a multi-line
// capability response terminated with OK.
func (ses *session) writeGreeting() error {
	for _, line := range ses.capabilityLines() {
		if err := ses.writeLine(line); err != nil {
			return err
		}
	}
	return ses.writeOK("ManageSieve ready")
}

// capabilityLines returns the unsolicited capability set RFC 5804 §1.7
// expects. The list is rebuilt on each greeting so STARTTLS toggles
// the available SASL mechanisms (PLAIN/LOGIN gate on TLS).
func (ses *session) capabilityLines() []string {
	out := []string{
		fmt.Sprintf(`"IMPLEMENTATION" "%s"`, ses.s.opts.ServerName),
		`"VERSION" "1.0"`,
		fmt.Sprintf(`"SASL" %q`, strings.Join(ses.saslMechs(), " ")),
		fmt.Sprintf(`"SIEVE" %q`, strings.Join(supportedSieveExtensions(), " ")),
		`"MAXREDIRECTS" "5"`,
		`"NOTIFY" "mailto"`,
	}
	if !ses.tlsActive && ses.s.tlsStore != nil {
		out = append(out, `"STARTTLS"`)
	}
	return out
}

// saslMechs returns the SASL mechanism names appropriate for the
// session's current TLS state. PLAIN / LOGIN over cleartext are
// refused (RFC 5804 §1.5 mandates STARTTLS first).
func (ses *session) saslMechs() []string {
	mechs := []string{}
	if ses.tlsActive {
		mechs = append(mechs, "PLAIN", "LOGIN", "SCRAM-SHA-256")
		if ses.s.passwords != nil && len(ses.serverEndpoint) > 0 {
			mechs = append(mechs, "SCRAM-SHA-256-PLUS")
		}
		if ses.s.tokens != nil {
			mechs = append(mechs, "OAUTHBEARER", "XOAUTH2")
		}
	} else {
		// SCRAM does not require TLS but PLAIN / LOGIN do.
		mechs = append(mechs, "SCRAM-SHA-256")
		if ses.s.tokens != nil {
			mechs = append(mechs, "OAUTHBEARER", "XOAUTH2")
		}
	}
	return mechs
}

// supportedSieveExtensions snapshots sieve.SupportedExtensions into a
// stable, alphabetically-ordered list. Pulling dynamically means the
// listener advertises whatever the interpreter actually supports
// (REQ-PROTO-51 — same parser at upload and delivery).
func supportedSieveExtensions() []string {
	out := make([]string, 0, len(sieve.SupportedExtensions))
	for k := range sieve.SupportedExtensions {
		// Skip comparator-* meta tokens — those advertise via the
		// COMPARATOR capability in IMAP-style protocols, not the
		// SIEVE extensions list (RFC 5804 §1.6 ties SIEVE values to
		// "require" tokens). The interpreter recognises them but
		// ManageSieve clients name them through the comparator
		// argument, not require.
		if strings.HasPrefix(k, "comparator-") {
			continue
		}
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// sortStrings is a tiny in-package sort to avoid pulling sort just for
// the capability render. Stable bubble; the list is short.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// dispatch routes one parsed command to its handler.
func (ses *session) dispatch(ctx context.Context, c *Command) error {
	switch strings.ToUpper(c.Verb) {
	case "STARTTLS":
		return ses.handleSTARTTLS(ctx, c)
	case "AUTHENTICATE":
		return ses.handleAUTHENTICATE(ctx, c)
	case "LOGOUT":
		_ = ses.writeOK("Bye")
		ses.state = stateLogout
		return nil
	case "CAPABILITY":
		return ses.handleCAPABILITY(c)
	case "NOOP":
		return ses.writeOK("NOOP completed")
	case "HAVESPACE":
		return ses.handleHAVESPACE(ctx, c)
	case "PUTSCRIPT":
		return ses.handlePUTSCRIPT(ctx, c)
	case "CHECKSCRIPT":
		return ses.handleCHECKSCRIPT(ctx, c)
	case "GETSCRIPT":
		return ses.handleGETSCRIPT(ctx, c)
	case "DELETESCRIPT":
		return ses.handleDELETESCRIPT(ctx, c)
	case "LISTSCRIPTS":
		return ses.handleLISTSCRIPTS(ctx, c)
	case "SETACTIVE":
		return ses.handleSETACTIVE(ctx, c)
	case "RENAMESCRIPT":
		return ses.handleRENAMESCRIPT(ctx, c)
	default:
		return ses.writeNO("", fmt.Sprintf("unknown command %q", c.Verb))
	}
}

// requireAuth checks that the session has authenticated. Every
// post-auth command (everything except STARTTLS / AUTHENTICATE /
// LOGOUT / CAPABILITY / NOOP) goes through this gate.
func (ses *session) requireAuth() error {
	if ses.state == stateAuthed {
		return nil
	}
	return ses.writeNO("", "Authenticate first")
}

// requireTLS enforces RFC 5804 §1.5: every command other than
// STARTTLS / CAPABILITY / LOGOUT requires TLS first. Authenticate is
// the strict case — PLAIN / LOGIN over cleartext are not advertised,
// and even SCRAM (which can run cleartext) is refused here so the
// post-AUTH command surface is uniformly TLS-protected.
func (ses *session) requireTLS() error {
	if ses.tlsActive {
		return nil
	}
	if ses.s.tlsStore == nil {
		// Operator deliberately skipped TLS — accept commands so
		// in-process tests work. RFC 5804 §1.5 is operator
		// responsibility.
		return nil
	}
	return ses.writeNO("ENCRYPT-NEEDED", "STARTTLS required")
}

// -----------------------------------------------------------------------------
// I/O helpers
// -----------------------------------------------------------------------------

func isClose(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF)
}

// writeLine writes one CRLF-terminated line and flushes.
func (ses *session) writeLine(line string) error {
	if _, err := ses.bw.WriteString(line); err != nil {
		return err
	}
	if _, err := ses.bw.WriteString("\r\n"); err != nil {
		return err
	}
	return ses.bw.Flush()
}

// writeOK / writeNO / writeBYE encode the standard ManageSieve status
// responses with optional response codes and quoted human text.
func (ses *session) writeOK(text string) error { return ses.writeStatus("OK", "", text) }
func (ses *session) writeNO(code, text string) error {
	return ses.writeStatus("NO", code, text)
}
func (ses *session) writeBYE(code, text string) error {
	return ses.writeStatus("BYE", code, text)
}

func (ses *session) writeStatus(verb, code, text string) error {
	var sb strings.Builder
	sb.WriteString(verb)
	if code != "" {
		sb.WriteString(" (")
		sb.WriteString(code)
		sb.WriteByte(')')
	}
	if text != "" {
		sb.WriteByte(' ')
		sb.WriteString(quoteString(text))
	}
	return ses.writeLine(sb.String())
}

// writeNOQuotedScriptName emits the RFC 5804 §2.4-style NO response
// that prefixes the active script name in parentheses (used by
// PUTSCRIPT / CHECKSCRIPT to surface a parse error against a name).
func (ses *session) writeNOQuotedScriptName(name, text string) error {
	return ses.writeLine(fmt.Sprintf(`NO (%s) %s`, quoteString(name), quoteString(text)))
}

// quoteString renders s as an RFC 5804 quoted string, falling back to
// a literal when s carries CR / LF / NUL or exceeds 1024 bytes (an
// arbitrary line-length safety bound).
func quoteString(s string) string {
	if strings.ContainsAny(s, "\r\n\x00") || len(s) > 1024 {
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

// readLineRaw returns one line without the CRLF, capped at 8 KiB.
func (ses *session) readLineRaw() (string, error) {
	const maxLine = 8 * 1024
	var sb strings.Builder
	for sb.Len() < maxLine {
		b, err := ses.br.ReadByte()
		if err != nil {
			if err == io.EOF && sb.Len() > 0 {
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		if b == '\r' {
			next, nerr := ses.br.ReadByte()
			if nerr != nil {
				return "", nerr
			}
			if next == '\n' {
				return sb.String(), nil
			}
			sb.WriteByte('\r')
			sb.WriteByte(next)
			continue
		}
		if b == '\n' {
			return sb.String(), nil
		}
		sb.WriteByte(b)
	}
	return "", fmt.Errorf("protomanagesieve: line exceeds %d bytes", maxLine)
}

// readScriptLiteral consumes a {N}/{N+}-prefixed literal. The prefix
// has already been peeked off the line; size is the byte count, sync
// is true when the literal is synchronising (the client is waiting
// for our continuation).
//
// Per RFC 5804 §1.6 the literal payload is N bytes followed by either
// (a) more command arguments on the same logical line, or (b) the
// CRLF that terminates the command. We do NOT consume the trailing
// bytes here; readCommand's outer loop reads the next physical line
// (which may be empty when N is the last argument), and the
// tokeniser handles trailing whitespace.
func (ses *session) readScriptLiteral(size int64, sync bool) ([]byte, error) {
	if size > ses.s.opts.MaxScriptBytes {
		return nil, fmt.Errorf("protomanagesieve: script literal %d > max %d", size, ses.s.opts.MaxScriptBytes)
	}
	if sync {
		if err := ses.writeLine("OK"); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(ses.br, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// makeMechanism constructs a SASL mechanism by name, refusing
// mechanisms that don't match the session's transport state.
func (ses *session) makeMechanism(name string) (sasl.Mechanism, error) {
	switch strings.ToUpper(name) {
	case "PLAIN":
		if !ses.tlsActive {
			return nil, errors.New("PLAIN requires TLS")
		}
		return sasl.NewPLAIN(ses.s.dir), nil
	case "LOGIN":
		if !ses.tlsActive {
			return nil, errors.New("LOGIN requires TLS")
		}
		return sasl.NewLOGIN(ses.s.dir), nil
	case "SCRAM-SHA-256":
		return sasl.NewSCRAMSHA256(ses.s.dir, ses.s.passwords, false), nil
	case "SCRAM-SHA-256-PLUS":
		if !ses.tlsActive {
			return nil, errors.New("SCRAM-SHA-256-PLUS requires TLS")
		}
		if len(ses.serverEndpoint) == 0 {
			return nil, errors.New("SCRAM-SHA-256-PLUS requires tls-server-end-point binding")
		}
		return sasl.NewSCRAMSHA256(ses.s.dir, ses.s.passwords, true), nil
	case "OAUTHBEARER":
		if ses.s.tokens == nil {
			return nil, errors.New("OAUTHBEARER unsupported")
		}
		return sasl.NewOAUTHBEARER(ses.s.tokens), nil
	case "XOAUTH2":
		if ses.s.tokens == nil {
			return nil, errors.New("XOAUTH2 unsupported")
		}
		return sasl.NewXOAUTH2(ses.s.tokens), nil
	}
	return nil, fmt.Errorf("mechanism %q not supported", name)
}
