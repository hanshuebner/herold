package protoimap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/sasl"
	"github.com/hanshuebner/herold/internal/store"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// sessionState enumerates the IMAP state-machine positions we track.
type sessionState int

const (
	stateNotAuthed sessionState = iota
	stateAuthed
	stateSelected
	stateLogout
)

// session is one IMAP connection. All per-connection state lives here; the
// server keeps a set of pointers for shutdown fan-out.
type session struct {
	s          *Server
	conn       net.Conn
	br         *bufio.Reader
	resp       *respWriter
	remote     string
	tlsActive  bool
	state      sessionState
	logger     *slog.Logger
	pid        store.PrincipalID
	bucket     *tokenBucket
	startTLSFn func() error // nil for implicit TLS / plaintext-locked listeners
	cmdCount   int

	// serverEndpoint is the RFC 5929 tls-server-end-point binding bytes
	// for the active TLS connection (zero-length when TLS is not up).
	// SCRAM-SHA-256-PLUS reads this through the SASL ctx.
	serverEndpoint []byte

	// selected mailbox state
	selMu sync.Mutex
	sel   selectedMailbox
}

type selectedMailbox struct {
	id                     store.MailboxID
	name                   string
	uidValidity            store.UIDValidity
	uidNext                store.UID
	msgs                   []store.Message // ordered by UID ascending; sequence number is index+1
	readOnly               bool
	lastSeenSeq            store.ChangeSeq
	subscribedToChangeFeed bool
}

func newSession(s *Server, c net.Conn, tlsActive bool) *session {
	ses := &session{
		s:         s,
		conn:      c,
		br:        bufio.NewReaderSize(c, 16*1024),
		resp:      newRespWriter(c),
		remote:    c.RemoteAddr().String(),
		tlsActive: tlsActive,
		state:     stateNotAuthed,
		logger:    s.logger.With("remote", c.RemoteAddr().String()),
	}
	if s.opts.DownloadBytesPerSecond > 0 {
		ses.bucket = newTokenBucket(s.clk, s.opts.DownloadBytesPerSecond, s.opts.DownloadBurstBytes)
	}
	return ses
}

func (ses *session) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	greet := "* OK [CAPABILITY " + ses.capabilityString() + "] " + ses.s.opts.ServerName + " IMAP ready"
	if err := ses.resp.writeLine(greet); err != nil {
		ses.logger.Debug("protoimap: greeting write failed", "err", err)
		return
	}
	for {
		if ses.state == stateLogout {
			return
		}
		if ses.s.opts.MaxCommandsPerSession > 0 && ses.cmdCount >= ses.s.opts.MaxCommandsPerSession {
			_ = ses.resp.writeLine("* BYE command budget exhausted")
			return
		}
		// SetReadDeadline must use wall time because net.Conn deadlines
		// are compared against the OS clock, not our injected Clock.
		_ = ses.conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
		cmd, err := readCommand(ses.br, ses.readLiteral)
		if err != nil {
			if !isClose(err) {
				ses.logger.Debug("protoimap: read command", "err", err)
			}
			return
		}
		if cmd.Tag == "" && cmd.Op == "" {
			continue
		}
		ses.cmdCount++
		if err := ses.dispatch(ctx, cmd); err != nil {
			// A dispatch error ends the session (protocol violation).
			ses.logger.Debug("protoimap: dispatch", "err", err)
			_ = ses.resp.taggedBAD(cmd.Tag, "", fmt.Sprintf("error: %v", err))
			return
		}
	}
}

func isClose(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF)
}

// readLiteral satisfies the parser's literalReader hook. For a synchronising
// literal we write a "+ Ready for literal data" continuation then read the
// declared bytes. LITERAL+ skips the continuation.
func (ses *session) readLiteral(size int64, nonSync bool) ([]byte, error) {
	if !nonSync {
		if err := ses.resp.continuation("Ready for literal data"); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(ses.br, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// dispatch routes a parsed command to its handler. Handlers return an
// error only for fatal protocol faults (which end the session); per-IMAP
// "NO" / "BAD" results are written to the client as tagged responses and
// the handler returns nil.
func (ses *session) dispatch(ctx context.Context, c *Command) error {
	// Record the command on a bounded label set: only the verbs the
	// dispatch table understands are passed through; everything else
	// rolls up to "unknown" so cardinality is fixed.
	cmdLabel := imapCommandLabel(c.Op)
	observe.IMAPCommandsTotal.WithLabelValues(cmdLabel).Inc()
	switch c.Op {
	case "CAPABILITY":
		return ses.handleCAPABILITY(c)
	case "NOOP":
		return ses.resp.taggedOK(c.Tag, "", "NOOP completed")
	case "LOGOUT":
		if err := ses.resp.writeLine("* BYE bye"); err != nil {
			return err
		}
		ses.state = stateLogout
		return ses.resp.taggedOK(c.Tag, "", "LOGOUT completed")
	case "ID":
		ses.logger.Debug("protoimap: client ID", "params", c.IDParams)
		// Respond with a minimal server ID.
		if err := ses.resp.untagged("ID (\"name\" \"" + ses.s.opts.ServerName + "\")"); err != nil {
			return err
		}
		return ses.resp.taggedOK(c.Tag, "", "ID completed")
	case "ENABLE":
		// Accept silently for Phase 1.
		return ses.resp.taggedOK(c.Tag, "", "ENABLE completed")
	case "NAMESPACE":
		if err := ses.resp.untagged(`NAMESPACE (("" "/")) NIL NIL`); err != nil {
			return err
		}
		return ses.resp.taggedOK(c.Tag, "", "NAMESPACE completed")
	case "STARTTLS":
		return ses.handleSTARTTLS(ctx, c)
	case "AUTHENTICATE":
		return ses.handleAUTHENTICATE(ctx, c)
	case "LOGIN":
		return ses.handleLOGIN(ctx, c)
	case "SELECT":
		return ses.handleSELECT(ctx, c, false)
	case "EXAMINE":
		return ses.handleSELECT(ctx, c, true)
	case "UNSELECT", "CLOSE":
		if ses.state != stateSelected {
			return ses.resp.taggedBAD(c.Tag, "", "not in SELECTED state")
		}
		ses.state = stateAuthed
		ses.sel = selectedMailbox{}
		return ses.resp.taggedOK(c.Tag, "", c.Op+" completed")
	case "CHECK":
		if ses.state != stateSelected {
			return ses.resp.taggedBAD(c.Tag, "", "not in SELECTED state")
		}
		return ses.resp.taggedOK(c.Tag, "", "CHECK completed")
	case "CREATE":
		return ses.handleCREATE(ctx, c)
	case "DELETE":
		return ses.handleDELETE(ctx, c)
	case "RENAME":
		return ses.handleRENAME(ctx, c)
	case "SUBSCRIBE", "UNSUBSCRIBE":
		return ses.handleSUBSCRIBE(ctx, c, c.Op == "SUBSCRIBE")
	case "LIST", "LSUB":
		return ses.handleLIST(ctx, c, c.Op == "LSUB")
	case "STATUS":
		return ses.handleSTATUS(ctx, c)
	case "APPEND":
		return ses.handleAPPEND(ctx, c)
	case "FETCH":
		return ses.handleFETCH(ctx, c)
	case "STORE":
		return ses.handleSTORE(ctx, c)
	case "SEARCH":
		return ses.handleSEARCH(ctx, c)
	case "EXPUNGE":
		return ses.handleEXPUNGE(ctx, c)
	case "IDLE":
		return ses.handleIDLE(ctx, c)
	default:
		return ses.resp.taggedBAD(c.Tag, "", "unknown command "+c.Op)
	}
}

// capabilityString returns the space-separated capability list for the
// session's current state. REQ-PROTO-04 demands it match implementation.
func (ses *session) capabilityString() string {
	caps := []string{
		string(imap.CapIMAP4rev2),
		string(imap.CapIMAP4rev1),
		string(imap.CapEnable),
		string(imap.CapIdle),
		string(imap.CapLiteralPlus),
		string(imap.CapUIDPlus),
		string(imap.CapESearch),
		string(imap.CapSearchRes),
		string(imap.CapUnselect),
		string(imap.CapNamespace),
		string(imap.CapID),
		string(imap.CapListExtended),
		string(imap.CapListStatus),
		string(imap.CapSpecialUse),
		string(imap.CapCreateSpecialUse),
		string(imap.CapUTF8Accept),
		string(imap.CapQuota),
		string(imap.CapSASLIR),
	}
	if !ses.tlsActive && ses.startTLSAllowed() {
		caps = append(caps, string(imap.CapStartTLS))
	}
	// Auth mechanisms depend on TLS state: PLAIN / LOGIN over TLS only.
	// SCRAM-SHA-256-PLUS is advertised only when TLS is up *and* a
	// tls-server-end-point binding is available (RFC 5802 §6 forbids
	// PLUS over cleartext, and STANDARDS rule 10 forbids advertising a
	// wire extension we cannot honour).
	if ses.tlsActive {
		caps = append(caps,
			"AUTH=PLAIN", "AUTH=LOGIN",
			"AUTH=SCRAM-SHA-256",
		)
		if ses.s.passwords != nil && len(ses.serverEndpoint) > 0 {
			caps = append(caps, "AUTH=SCRAM-SHA-256-PLUS")
		}
		caps = append(caps, "AUTH=OAUTHBEARER", "AUTH=XOAUTH2")
	} else {
		// SCRAM does not require TLS; OAuth tokens arguably also refuse
		// over cleartext but we advertise them under the same gate.
		caps = append(caps, "AUTH=SCRAM-SHA-256")
		if ses.s.opts.AllowPlainLoginWithoutTLS {
			caps = append(caps, "AUTH=PLAIN", "AUTH=LOGIN")
		} else {
			caps = append(caps, "LOGINDISABLED")
		}
	}
	return strings.Join(caps, " ")
}

func (ses *session) startTLSAllowed() bool {
	// STARTTLS is meaningful only on plaintext listeners.
	return !ses.tlsActive && ses.s.tlsStore != nil
}

func (ses *session) handleCAPABILITY(c *Command) error {
	if err := ses.resp.untagged("CAPABILITY " + ses.capabilityString()); err != nil {
		return err
	}
	return ses.resp.taggedOK(c.Tag, "", "CAPABILITY completed")
}

func (ses *session) handleSTARTTLS(ctx context.Context, c *Command) error {
	if ses.tlsActive {
		return ses.resp.taggedBAD(c.Tag, "", "TLS already active")
	}
	if ses.s.tlsStore == nil {
		return ses.resp.taggedNO(c.Tag, "", "TLS not available")
	}
	if err := ses.resp.taggedOK(c.Tag, "", "Begin TLS negotiation now"); err != nil {
		return err
	}
	cap := sasl.NewCapturingTLSConfig(heroldtls.TLSConfig(ses.s.tlsStore, heroldtls.Intermediate, nil))
	tlsConn := tls.Server(ses.conn, cap.Config())
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		ses.logger.Warn("protoimap: STARTTLS", "err", err)
		return err
	}
	ses.conn = tlsConn
	ses.br = bufio.NewReaderSize(tlsConn, 16*1024)
	ses.resp = newRespWriter(tlsConn)
	ses.tlsActive = true
	if leaf := cap.Leaf(); leaf != nil {
		if cb, err := sasl.TLSServerEndpoint(leaf); err == nil {
			ses.serverEndpoint = cb
		}
	}
	return nil
}

func (ses *session) handleLOGIN(ctx context.Context, c *Command) error {
	if ses.state != stateNotAuthed {
		return ses.resp.taggedBAD(c.Tag, "", "already authenticated")
	}
	if !ses.tlsActive && !ses.s.opts.AllowPlainLoginWithoutTLS {
		return ses.resp.taggedNO(c.Tag, "PRIVACYREQUIRED", "LOGIN refused without TLS")
	}
	ctx = directory.WithAuthSource(ctx, ses.remote)
	pid, err := ses.s.dir.Authenticate(ctx, c.LoginUser, c.LoginPass)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "AUTHENTICATIONFAILED", "LOGIN failed")
	}
	ses.pid = pid
	ses.state = stateAuthed
	return ses.resp.taggedOK(c.Tag, "CAPABILITY "+ses.capabilityString(), "LOGIN completed")
}

func (ses *session) handleAUTHENTICATE(ctx context.Context, c *Command) error {
	if ses.state != stateNotAuthed {
		return ses.resp.taggedBAD(c.Tag, "", "already authenticated")
	}
	mech, err := ses.makeMechanism(c.AuthMechanism)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "AUTHENTICATIONFAILED", err.Error())
	}
	if ses.tlsActive {
		ctx = sasl.WithTLS(ctx, true)
		if len(ses.serverEndpoint) > 0 {
			ctx = sasl.WithTLSServerEndpoint(ctx, ses.serverEndpoint)
		}
	}
	initial := c.AuthInitial
	var (
		challenge []byte
		done      bool
	)
	challenge, done, err = mech.Start(ctx, initial)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "AUTHENTICATIONFAILED", "SASL failure")
	}
	for !done {
		if err := ses.resp.continuation(encodeB64(challenge)); err != nil {
			return err
		}
		line, rerr := readLine(ses.br)
		if rerr != nil {
			return rerr
		}
		if line == "*" {
			return ses.resp.taggedBAD(c.Tag, "", "client aborted SASL")
		}
		resp, derr := decodeB64(line)
		if derr != nil {
			return ses.resp.taggedBAD(c.Tag, "", "bad base64")
		}
		challenge, done, err = mech.Next(ctx, resp)
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "AUTHENTICATIONFAILED", "SASL failure")
		}
	}
	pid, perr := mech.Principal()
	if perr != nil {
		return ses.resp.taggedNO(c.Tag, "AUTHENTICATIONFAILED", "SASL failure")
	}
	// Final challenge (for SCRAM success message) needs to go before OK.
	if len(challenge) > 0 {
		if err := ses.resp.continuation(encodeB64(challenge)); err != nil {
			return err
		}
		// Consume the mandatory "" client response line.
		_, _ = readLine(ses.br)
	}
	ses.pid = pid
	ses.state = stateAuthed
	return ses.resp.taggedOK(c.Tag, "CAPABILITY "+ses.capabilityString(), "AUTHENTICATE completed")
}

func (ses *session) makeMechanism(name string) (sasl.Mechanism, error) {
	switch name {
	case "PLAIN":
		if !ses.tlsActive && !ses.s.opts.AllowPlainLoginWithoutTLS {
			return nil, errors.New("PLAIN requires TLS")
		}
		return sasl.NewPLAIN(ses.s.dir), nil
	case "LOGIN":
		if !ses.tlsActive && !ses.s.opts.AllowPlainLoginWithoutTLS {
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

// imapCommandLabel folds an arbitrary command opcode into the bounded
// label set the herold_imap_commands_total metric expects. Unknown
// verbs roll up to "unknown" so a malicious client cannot blow up
// cardinality with random opcodes.
func imapCommandLabel(op string) string {
	switch op {
	case "CAPABILITY", "NOOP", "LOGOUT", "ID", "ENABLE", "NAMESPACE",
		"STARTTLS", "AUTHENTICATE", "LOGIN",
		"SELECT", "EXAMINE", "UNSELECT", "CLOSE", "CHECK",
		"CREATE", "DELETE", "RENAME", "SUBSCRIBE", "UNSUBSCRIBE",
		"LIST", "LSUB", "STATUS", "APPEND",
		"FETCH", "STORE", "SEARCH", "EXPUNGE", "IDLE":
		return op
	default:
		return "unknown"
	}
}
