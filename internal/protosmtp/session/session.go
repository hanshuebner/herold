package session

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// bufSize is the default read/write buffer size for session I/O.
const bufSize = 4096

// Session is a single outbound SMTP client session wrapping one net.Conn.
// Methods correspond to one conversational phase each; they do not block
// unless a network I/O is required and the connection deadline allows it.
// Session is not safe for concurrent use.
type Session struct {
	conn       net.Conn
	reader     *bufio.Reader
	writer     *bufio.Writer
	extensions map[string]string // upper-cased extension name -> params
	// enhancedCodes is true when the remote advertised ENHANCEDSTATUSCODES.
	enhancedCodes bool
	// Pipelining is true when the remote advertised PIPELINING.
	Pipelining bool
}

// New wraps conn in a Session. The caller is responsible for setting any
// deadline on conn before or after New returns.
func New(conn net.Conn) *Session {
	return &Session{
		conn:       conn,
		reader:     bufio.NewReaderSize(conn, bufSize),
		writer:     bufio.NewWriterSize(conn, bufSize),
		extensions: map[string]string{},
	}
}

// Conn returns the underlying connection, e.g. so the caller can perform
// a STARTTLS upgrade and then call UpgradeConn.
func (s *Session) Conn() net.Conn { return s.conn }

// UpgradeConn replaces the underlying connection with tlsConn. This is
// called after a successful STARTTLS handshake; the buffered reader/writer
// are re-created against the new connection and the extension map is cleared
// so the caller can re-issue EHLO and repopulate it.
func (s *Session) UpgradeConn(tlsConn *tls.Conn) {
	s.conn = tlsConn
	s.reader = bufio.NewReaderSize(tlsConn, bufSize)
	s.writer = bufio.NewWriterSize(tlsConn, bufSize)
	// Extensions advertised pre-TLS are stale; the caller must re-EHLO.
	s.extensions = map[string]string{}
	s.enhancedCodes = false
	s.Pipelining = false
}

// HasExtension reports whether the remote advertised name in its EHLO.
// Comparison is case-insensitive on the bare name; parameters are ignored.
func (s *Session) HasExtension(name string) bool {
	_, ok := s.extensions[strings.ToUpper(name)]
	return ok
}

// ExtensionParam returns the parameter string for a named EHLO extension,
// or empty string when the extension was not advertised.
func (s *Session) ExtensionParam(name string) string {
	return s.extensions[strings.ToUpper(name)]
}

// Reply is a fully-parsed SMTP reply. Multi-line replies are joined with
// "\n"; Code is the numeric code; Enhanced is the RFC 3463 enhanced-status
// triple when the remote advertised ENHANCEDSTATUSCODES.
type Reply struct {
	Code     int
	Enhanced string
	Text     string
}

// IsSuccess reports whether the reply code is in the 2xx range.
func (r Reply) IsSuccess() bool { return r.Code/100 == 2 }

// IsTransient reports whether the reply code is in the 4xx range.
func (r Reply) IsTransient() bool { return r.Code/100 == 4 }

// IsPermanent reports whether the reply code is in the 5xx range.
func (r Reply) IsPermanent() bool { return r.Code/100 == 5 }

// ReadGreeting reads the 220 banner. The caller checks Reply.IsSuccess.
func (s *Session) ReadGreeting() (Reply, error) {
	return s.readReply()
}

// Ehlo issues EHLO hostName, parses the advertised extensions, and returns
// the reply. On a 250 reply the extension map is populated. On a non-250
// reply the map is unchanged; the caller may fall back to HELO.
func (s *Session) Ehlo(hostName string) (Reply, error) {
	if err := s.sendLine("EHLO " + hostName); err != nil {
		return Reply{}, err
	}
	r, err := s.readReply()
	if err != nil {
		return Reply{}, err
	}
	if r.Code == 250 {
		s.parseExtensions(r.Text)
	}
	return r, nil
}

// Cmd sends an arbitrary command line (without CRLF) and returns the reply.
// This is the escape hatch for callers that need to send STARTTLS, QUIT, etc.
func (s *Session) Cmd(line string) (Reply, error) {
	if err := s.sendLine(line); err != nil {
		return Reply{}, err
	}
	return s.readReply()
}

// AuthPlain performs SASL PLAIN authentication (RFC 4616). authzid is
// typically empty. The IR is sent inline with the AUTH verb.
func (s *Session) AuthPlain(user, pass string) error {
	var ir strings.Builder
	ir.WriteString("\x00")
	ir.WriteString(user)
	ir.WriteString("\x00")
	ir.WriteString(pass)
	encoded := base64.StdEncoding.EncodeToString([]byte(ir.String()))
	r, err := s.Cmd("AUTH PLAIN " + encoded)
	if err != nil {
		return err
	}
	if r.Code == 235 {
		return nil
	}
	return fmt.Errorf("AUTH PLAIN: %d %s", r.Code, strings.TrimSpace(r.Text))
}

// AuthLogin performs SASL LOGIN authentication. The exchange is three turns:
// the client sends AUTH LOGIN; the server challenges for username then
// password; the client responds to each.
func (s *Session) AuthLogin(user, pass string) error {
	if err := s.sendLine("AUTH LOGIN"); err != nil {
		return err
	}
	// Server sends 334 <base64("Username:")> or similar.
	r, err := s.readReply()
	if err != nil {
		return err
	}
	if r.Code != 334 {
		return fmt.Errorf("AUTH LOGIN: expected 334 for username challenge, got %d %s", r.Code, r.Text)
	}
	if err := s.sendLine(base64.StdEncoding.EncodeToString([]byte(user))); err != nil {
		return err
	}
	// Server sends 334 <base64("Password:")> or similar.
	r, err = s.readReply()
	if err != nil {
		return err
	}
	if r.Code != 334 {
		return fmt.Errorf("AUTH LOGIN: expected 334 for password challenge, got %d %s", r.Code, r.Text)
	}
	if err := s.sendLine(base64.StdEncoding.EncodeToString([]byte(pass))); err != nil {
		return err
	}
	r, err = s.readReply()
	if err != nil {
		return err
	}
	if r.Code == 235 {
		return nil
	}
	return fmt.Errorf("AUTH LOGIN: %d %s", r.Code, strings.TrimSpace(r.Text))
}

// AuthXOAUTH2 performs SASL XOAUTH2 authentication (Google's non-standard
// OAuth bearer flavour). The SASL string format is per RFC 7628:
//
//	user=<email>\x01auth=Bearer <token>\x01\x01
//
// The IR is sent inline with AUTH XOAUTH2. When the server rejects the token
// with a 334 JSON error challenge, the session sends an empty response and
// returns an error on the subsequent 535.
func (s *Session) AuthXOAUTH2(user, accessToken string) error {
	var msg strings.Builder
	msg.WriteString("user=")
	msg.WriteString(user)
	msg.WriteByte(0x01)
	msg.WriteString("auth=Bearer ")
	msg.WriteString(accessToken)
	msg.WriteByte(0x01)
	msg.WriteByte(0x01)
	encoded := base64.StdEncoding.EncodeToString([]byte(msg.String()))
	if err := s.sendLine("AUTH XOAUTH2 " + encoded); err != nil {
		return err
	}
	r, err := s.readReply()
	if err != nil {
		return err
	}
	if r.Code == 235 {
		return nil
	}
	if r.Code == 334 {
		// Server sent a JSON error body. Send an empty response to let the
		// server conclude the exchange with a 5xx.
		if err := s.sendLine(""); err != nil {
			return err
		}
		r, err = s.readReply()
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("AUTH XOAUTH2: %d %s", r.Code, strings.TrimSpace(r.Text))
}

// Opt carries optional ESMTP parameters for MAIL FROM / RCPT TO commands.
// Zero values are suppressed.
type Opt struct {
	// Body is the BODY= parameter ("7BIT", "8BITMIME"); empty suppresses.
	Body string
	// SMTPUTF8, when true and the remote advertised SMTPUTF8, appends the
	// SMTPUTF8 keyword.
	SMTPUTF8 bool
	// REQUIRETLS, when true and the remote advertised REQUIRETLS, appends
	// the REQUIRETLS keyword.
	REQUIRETLS bool
	// EnvID is the RFC 3461 ENVID parameter; empty suppresses.
	EnvID string
	// Notify is the RFC 3461 NOTIFY parameter; empty suppresses.
	Notify string
}

// MailFrom issues MAIL FROM:<addr> with optional ESMTP parameters.
func (s *Session) MailFrom(addr string, opts ...Opt) (Reply, error) {
	var b strings.Builder
	b.WriteString("MAIL FROM:<")
	b.WriteString(addr)
	b.WriteString(">")
	if len(opts) > 0 {
		o := opts[0]
		if o.Body != "" && s.HasExtension("8BITMIME") {
			b.WriteString(" BODY=")
			b.WriteString(o.Body)
		}
		if o.SMTPUTF8 && s.HasExtension("SMTPUTF8") {
			b.WriteString(" SMTPUTF8")
		}
		if o.REQUIRETLS && s.HasExtension("REQUIRETLS") {
			b.WriteString(" REQUIRETLS")
		}
		if o.EnvID != "" && s.HasExtension("DSN") {
			b.WriteString(" ENVID=")
			b.WriteString(o.EnvID)
		}
	}
	return s.Cmd(b.String())
}

// RcptTo issues RCPT TO:<addr> with optional DSN NOTIFY parameter.
func (s *Session) RcptTo(addr string, opts ...Opt) (Reply, error) {
	var b strings.Builder
	b.WriteString("RCPT TO:<")
	b.WriteString(addr)
	b.WriteString(">")
	if len(opts) > 0 {
		o := opts[0]
		if o.Notify != "" && s.HasExtension("DSN") {
			b.WriteString(" NOTIFY=")
			b.WriteString(o.Notify)
		}
	}
	return s.Cmd(b.String())
}

// Data performs the DATA exchange: sends DATA, reads the 354 invitation,
// writes the dot-stuffed payload, sends the terminating ".\r\n", and reads
// the final reply. On a 2xx final reply it returns the MTA-supplied queue-id
// string (the text of the reply) and a nil error. On a non-2xx it returns the
// reply text as the diagnostic string and a non-nil error carrying the code.
func (s *Session) Data(payload io.Reader) (mtaID string, err error) {
	r, err := s.Cmd("DATA")
	if err != nil {
		return "", err
	}
	if r.Code != 354 {
		return "", fmt.Errorf("DATA: %d %s", r.Code, strings.TrimSpace(r.Text))
	}
	// Dot-stuff and stream the body.
	if err := writeDotStuffed(s.writer, payload); err != nil {
		return "", fmt.Errorf("DATA body write: %w", err)
	}
	if _, err := s.writer.WriteString(".\r\n"); err != nil {
		return "", fmt.Errorf("DATA terminator write: %w", err)
	}
	if err := s.writer.Flush(); err != nil {
		return "", fmt.Errorf("DATA flush: %w", err)
	}
	r, err = s.readReply()
	if err != nil {
		return "", err
	}
	if r.Code/100 == 2 {
		return strings.TrimSpace(r.Text), nil
	}
	return "", fmt.Errorf("DATA final: %d %s", r.Code, strings.TrimSpace(r.Text))
}

// Quit sends QUIT and reads the 221 reply. Errors are ignored after the
// command is sent since the caller is about to close the connection.
func (s *Session) Quit() error {
	_ = s.sendLine("QUIT")
	_, _ = s.readReply()
	return s.conn.Close()
}

// ReadReply reads one full SMTP reply (single or multi-line). It is exported
// so callers that issue raw commands via Cmd can also read the reply
// themselves, and so fuzz targets can exercise the parser directly.
func (s *Session) ReadReply() (Reply, error) {
	return s.readReply()
}

// readReply is the internal multi-line reply parser.
func (s *Session) readReply() (Reply, error) {
	return ParseReply(s.reader, s.enhancedCodes)
}

// ParseReply reads one full SMTP reply from r. It is exported for use by
// fuzz targets and other callers that want to parse raw SMTP reply bytes.
// When enhancedCodes is true, the RFC 3463 enhanced-status prefix is
// extracted from the first line's text.
func ParseReply(r *bufio.Reader, enhancedCodes bool) (Reply, error) {
	var code int
	var enhanced string
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && len(lines) > 0 {
				break
			}
			return Reply{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			return Reply{}, fmt.Errorf("short SMTP reply: %q", line)
		}
		c, perr := strconv.Atoi(line[:3])
		if perr != nil {
			return Reply{}, fmt.Errorf("bad SMTP reply code: %q", line[:3])
		}
		code = c
		text := line[4:]
		if enhancedCodes && enhanced == "" {
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
			return Reply{}, fmt.Errorf("bad SMTP continuation marker in %q", line)
		}
	}
	return Reply{
		Code:     code,
		Enhanced: enhanced,
		Text:     strings.Join(lines, "\n"),
	}, nil
}

// parseEnhanced extracts an "X.Y.Z" enhanced status (RFC 3463) from the
// start of text. Returns the rest of the text without the prefix on hit.
func parseEnhanced(text string) (string, string, bool) {
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

// parseExtensions reads the EHLO greeting body. The first line is the
// server's identity (discarded); subsequent lines are extension names
// with optional parameters.
func (s *Session) parseExtensions(text string) {
	s.extensions = map[string]string{}
	s.enhancedCodes = false
	s.Pipelining = false
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
			s.Pipelining = true
		}
	}
}

// sendLine writes one CRLF-terminated command line and flushes.
func (s *Session) sendLine(line string) error {
	if _, err := s.writer.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return s.writer.Flush()
}

// writeDotStuffed copies body to w with RFC 5321 §4.5.2 transparency: a
// line beginning with '.' is escaped to "..".
func writeDotStuffed(w *bufio.Writer, body io.Reader) error {
	buf := make([]byte, 32*1024)
	var pending []byte
	for {
		n, err := body.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
		}
		// Flush whole lines from pending.
		for {
			nl := -1
			for i, b := range pending {
				if b == '\n' {
					nl = i
					break
				}
			}
			if nl < 0 {
				break
			}
			line := pending[:nl+1]
			pending = pending[nl+1:]
			if len(line) > 0 && line[0] == '.' {
				if werr := w.WriteByte('.'); werr != nil {
					return werr
				}
			}
			if _, werr := w.Write(line); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	// Flush any remaining bytes that did not end with a newline.
	if len(pending) > 0 {
		if len(pending) > 0 && pending[0] == '.' {
			if werr := w.WriteByte('.'); werr != nil {
				return werr
			}
		}
		if _, werr := w.Write(pending); werr != nil {
			return werr
		}
	}
	return nil
}
