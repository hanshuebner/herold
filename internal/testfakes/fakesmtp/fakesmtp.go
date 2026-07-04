// Package fakesmtp is an SMTP submission server for tests and development. It
// stands in for an external smart host (Gmail, Microsoft 365, a corporate
// relay) so herold's external-submission path can be exercised end to end
// without any network or a real provider.
//
// The server advertises STARTTLS (when configured for it) and AUTH with the
// PLAIN, LOGIN, and XOAUTH2 mechanisms, accepts a MAIL FROM / RCPT TO / DATA
// transaction, and records every accepted message — envelope, raw bytes, and
// the SASL identity that authenticated — for assertions. It binds to
// 127.0.0.1 on a kernel-picked port and serves concurrent connections, so it
// handles both the pre-persist probe and the real relay in one instance.
//
// Three security postures are supported: Plain (no TLS), STARTTLS (upgrade
// on the STARTTLS verb), and ImplicitTLS (TLS from the first byte). The TLS
// paths use a self-signed certificate generated in-process; TLSConfigForClient
// returns a *tls.Config trusting it for tests that dial the TLS variants.
//
// For tests, use New which registers cleanup on testing.TB. For standalone
// dev-tooling outside a test, use NewServer and call Close when done.
package fakesmtp

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Security selects the transport posture the server presents.
type Security int

const (
	// Plain serves cleartext SMTP with no STARTTLS advertised.
	Plain Security = iota
	// STARTTLS advertises STARTTLS and upgrades the connection on the verb.
	STARTTLS
	// ImplicitTLS wraps the connection in TLS from the first byte (port 465).
	ImplicitTLS
)

// Message is one accepted submission recorded by the server.
type Message struct {
	// MailFrom is the address given in MAIL FROM (without angle brackets).
	MailFrom string
	// RcptTo lists every accepted RCPT TO address (without angle brackets).
	RcptTo []string
	// Data is the raw message body received in the DATA phase, with SMTP
	// dot-unstuffing applied and the terminating "." removed.
	Data []byte
	// AuthMechanism is the SASL mechanism the client used ("PLAIN", "LOGIN",
	// or "XOAUTH2"), or "" if the client did not authenticate.
	AuthMechanism string
	// AuthIdentity is the authenticated identity: the login username for
	// PLAIN/LOGIN, or the user= field for XOAUTH2.
	AuthIdentity string
	// AuthSecret is the password (PLAIN/LOGIN) or bearer token (XOAUTH2)
	// the client presented, for tests that assert on the credential.
	AuthSecret string
	// OverTLS reports whether the transaction ran on a TLS-upgraded
	// connection.
	OverTLS bool
}

// Options configures a Server. The zero value yields a Plain server.
type Options struct {
	// Security selects the transport posture. Defaults to Plain.
	Security Security
	// Hostname is announced in the greeting and EHLO reply. Defaults to
	// "smtp.fake.test".
	Hostname string
}

// Server is a running fake SMTP submission server.
type Server struct {
	ln       net.Listener
	security Security
	hostname string
	tlsCfg   *tls.Config
	certPEM  []byte

	mu         sync.Mutex
	messages   []Message
	rejectAuth bool

	wg     sync.WaitGroup
	closed chan struct{}
}

// NewServer starts a fake SMTP server bound to 127.0.0.1 on a kernel-picked
// port and returns it. Call Close when done. For test code that wants
// automatic cleanup, use New instead.
func NewServer(opts Options) (*Server, error) {
	if opts.Hostname == "" {
		opts.Hostname = "smtp.fake.test"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fakesmtp: listen: %w", err)
	}
	s := &Server{
		ln:       ln,
		security: opts.Security,
		hostname: opts.Hostname,
		closed:   make(chan struct{}),
	}
	if opts.Security != Plain {
		tlsCfg, certPEM, err := selfSignedTLSNoT(opts.Hostname)
		if err != nil {
			_ = ln.Close()
			return nil, err
		}
		s.tlsCfg = tlsCfg
		s.certPEM = certPEM
	}
	if opts.Security == ImplicitTLS {
		s.ln = tls.NewListener(ln, s.tlsCfg)
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// New starts a fake SMTP server bound to 127.0.0.1 on a kernel-picked port
// and registers cleanup on t.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s, err := NewServer(opts)
	if err != nil {
		t.Fatalf("fakesmtp.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// HTTPHandler returns an http.Handler that exposes the server's recorded
// messages for dev-instance status checks. Mount it on a separate listener.
//
// Routes:
//   - GET /messages — JSON array of envelope records (no raw body bytes).
//   - GET /count    — JSON object {"count": N}.
func (s *Server) HTTPHandler() http.Handler {
	type msgJSON struct {
		MailFrom      string   `json:"mail_from"`
		RcptTo        []string `json:"rcpt_to"`
		AuthMechanism string   `json:"auth_mechanism,omitempty"`
		AuthIdentity  string   `json:"auth_identity,omitempty"`
		OverTLS       bool     `json:"over_tls,omitempty"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/messages", func(w http.ResponseWriter, _ *http.Request) {
		msgs := s.Messages()
		out := make([]msgJSON, len(msgs))
		for i, m := range msgs {
			out[i] = msgJSON{
				MailFrom:      m.MailFrom,
				RcptTo:        m.RcptTo,
				AuthMechanism: m.AuthMechanism,
				AuthIdentity:  m.AuthIdentity,
				OverTLS:       m.OverTLS,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/count", func(w http.ResponseWriter, _ *http.Request) {
		n := len(s.Messages())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "{\"count\":%d}\n", n)
	})
	return mux
}

// Addr is the "host:port" the server is listening on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Host is the listening host (always "127.0.0.1").
func (s *Server) Host() string {
	host, _, _ := net.SplitHostPort(s.ln.Addr().String())
	return host
}

// Port is the listening TCP port.
func (s *Server) Port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

// SetRejectAuth toggles AUTH rejection. When set, the server answers every
// AUTH command with 535 (authentication failed) and records nothing, so a
// submission attempt fails with an auth error. It is safe to call at runtime
// between transactions, letting a test simulate an expired/revoked credential
// (identity parked held-for-reauth) and then recovery (re-enable and retry).
func (s *Server) SetRejectAuth(reject bool) {
	s.mu.Lock()
	s.rejectAuth = reject
	s.mu.Unlock()
}

func (s *Server) authRejected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejectAuth
}

// Messages returns a snapshot of every accepted message.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, len(s.messages))
	copy(out, s.messages)
	return out
}

// TLSConfigForClient returns a *tls.Config that trusts the server's
// self-signed certificate, for tests that dial the STARTTLS/ImplicitTLS
// variants directly. Returns nil for a Plain server.
func (s *Server) TLSConfigForClient() *tls.Config {
	if s.certPEM == nil {
		return nil
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(s.certPEM)
	return &tls.Config{RootCAs: pool, ServerName: s.hostname}
}

// Close stops the server and waits for in-flight handlers to finish.
func (s *Server) Close() {
	select {
	case <-s.closed:
		return
	default:
	}
	close(s.closed)
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handle(c)
		}(conn)
	}
}

// session carries per-connection state through the command loop.
type session struct {
	mailFrom string
	rcptTo   []string
	authMech string
	authID   string
	authSec  string
	overTLS  bool
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	sess := &session{overTLS: s.security == ImplicitTLS}
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = bw.WriteString(line + "\r\n")
		_ = bw.Flush()
	}
	readLine := func() (string, bool) {
		l, err := br.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimRight(l, "\r\n"), true
	}

	write("220 " + s.hostname + " ESMTP fakesmtp")

	for {
		line, ok := readLine()
		if !ok {
			return
		}
		verb, rest := splitVerb(line)
		switch verb {
		case "EHLO", "HELO":
			write("250-" + s.hostname)
			write("250-8BITMIME")
			write("250-AUTH PLAIN LOGIN XOAUTH2")
			if s.security == STARTTLS && !sess.overTLS {
				write("250-STARTTLS")
			}
			write("250 SMTPUTF8")

		case "STARTTLS":
			if s.security != STARTTLS || sess.overTLS {
				write("503 5.5.1 STARTTLS not available")
				continue
			}
			write("220 2.0.0 ready to start TLS")
			tconn := tls.Server(conn, s.tlsCfg)
			if err := tconn.Handshake(); err != nil {
				return
			}
			conn = tconn
			br = bufio.NewReader(conn)
			bw = bufio.NewWriter(conn)
			write = func(l string) {
				_, _ = bw.WriteString(l + "\r\n")
				_ = bw.Flush()
			}
			readLine = func() (string, bool) {
				l, err := br.ReadString('\n')
				if err != nil {
					return "", false
				}
				return strings.TrimRight(l, "\r\n"), true
			}
			*sess = session{overTLS: true} // RFC 3207: discard prior state

		case "AUTH":
			s.handleAuth(sess, rest, write, readLine)

		case "MAIL":
			sess.mailFrom = extractAddr(rest)
			sess.rcptTo = nil
			write("250 2.1.0 sender ok")

		case "RCPT":
			sess.rcptTo = append(sess.rcptTo, extractAddr(rest))
			write("250 2.1.5 recipient ok")

		case "DATA":
			if sess.mailFrom == "" || len(sess.rcptTo) == 0 {
				write("503 5.5.1 need MAIL and RCPT first")
				continue
			}
			write("354 end data with <CRLF>.<CRLF>")
			data, ok := readData(readLine)
			if !ok {
				return
			}
			s.record(sess, data)
			write("250 2.0.0 accepted as FAKE-" + time.Now().Format("150405.000"))
			// Ready for another transaction on the same connection.
			sess.mailFrom = ""
			sess.rcptTo = nil

		case "RSET":
			sess.mailFrom = ""
			sess.rcptTo = nil
			write("250 2.0.0 reset")

		case "NOOP":
			write("250 2.0.0 ok")

		case "QUIT":
			write("221 2.0.0 bye")
			return

		default:
			write("502 5.5.1 command not implemented")
		}
	}
}

// handleAuth processes the AUTH command for PLAIN, LOGIN, and XOAUTH2. It
// records the presented identity/secret on the session and always accepts;
// credential-rejection scenarios are out of scope for this sink.
func (s *Server) handleAuth(sess *session, rest string, write func(string), readLine func() (string, bool)) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		write("501 5.5.4 AUTH mechanism required")
		return
	}
	mech := strings.ToUpper(fields[0])
	switch mech {
	case "PLAIN":
		var ir string
		if len(fields) >= 2 {
			ir = fields[1]
		} else {
			write("334 ")
			line, ok := readLine()
			if !ok {
				return
			}
			ir = line
		}
		user, secret := decodePlain(ir)
		if s.authRejected() {
			write("535 5.7.8 authentication credentials invalid")
			return
		}
		sess.authMech, sess.authID, sess.authSec = "PLAIN", user, secret
		write("235 2.7.0 authentication successful")

	case "LOGIN":
		write("334 " + b64("Username:"))
		userB64, ok := readLine()
		if !ok {
			return
		}
		write("334 " + b64("Password:"))
		passB64, ok := readLine()
		if !ok {
			return
		}
		if s.authRejected() {
			write("535 5.7.8 authentication credentials invalid")
			return
		}
		sess.authMech = "LOGIN"
		sess.authID = string(mustDecode(userB64))
		sess.authSec = string(mustDecode(passB64))
		write("235 2.7.0 authentication successful")

	case "XOAUTH2":
		var ir string
		if len(fields) >= 2 {
			ir = fields[1]
		} else {
			write("334 ")
			line, ok := readLine()
			if !ok {
				return
			}
			ir = line
		}
		user, token := decodeXOAuth2(ir)
		if s.authRejected() {
			write("535 5.7.8 authentication credentials invalid")
			return
		}
		sess.authMech, sess.authID, sess.authSec = "XOAUTH2", user, token
		write("235 2.7.0 authentication successful")

	default:
		write("504 5.7.4 unrecognized authentication type")
	}
}

func (s *Server) record(sess *session, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, Message{
		MailFrom:      sess.mailFrom,
		RcptTo:        append([]string(nil), sess.rcptTo...),
		Data:          data,
		AuthMechanism: sess.authMech,
		AuthIdentity:  sess.authID,
		AuthSecret:    sess.authSec,
		OverTLS:       sess.overTLS,
	})
}

// readData reads the DATA payload until the terminating "." line, applying
// dot-unstuffing.
func readData(readLine func() (string, bool)) ([]byte, bool) {
	var b strings.Builder
	for {
		line, ok := readLine()
		if !ok {
			return nil, false
		}
		if line == "." {
			return []byte(b.String()), true
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
}

// splitVerb uppercases and splits the SMTP verb from the remainder of a line.
func splitVerb(line string) (verb, rest string) {
	line = strings.TrimSpace(line)
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return strings.ToUpper(line), ""
}

// extractAddr pulls the addr-spec out of a "FROM:<addr>" / "TO:<addr>"
// parameter, tolerating optional angle brackets and trailing ESMTP params.
func extractAddr(rest string) string {
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[i+1:]
	}
	rest = strings.TrimSpace(rest)
	if i := strings.IndexByte(rest, '<'); i >= 0 {
		rest = rest[i+1:]
		if j := strings.IndexByte(rest, '>'); j >= 0 {
			rest = rest[:j]
		}
	} else if i := strings.IndexByte(rest, ' '); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

// decodePlain decodes a SASL PLAIN initial response
// (base64 of "authzid\0authcid\0passwd") into (authcid, passwd).
func decodePlain(ir string) (user, pass string) {
	raw := mustDecode(ir)
	parts := strings.Split(string(raw), "\x00")
	if len(parts) == 3 {
		return parts[1], parts[2]
	}
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// decodeXOAuth2 decodes an XOAUTH2 initial response
// ("user=<id>\x01auth=Bearer <token>\x01\x01") into (id, token).
func decodeXOAuth2(ir string) (user, token string) {
	raw := string(mustDecode(ir))
	for _, part := range strings.Split(raw, "\x01") {
		switch {
		case strings.HasPrefix(part, "user="):
			user = strings.TrimPrefix(part, "user=")
		case strings.HasPrefix(part, "auth=Bearer "):
			token = strings.TrimPrefix(part, "auth=Bearer ")
		}
	}
	return user, token
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustDecode(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return b
}

// selfSignedTLSNoT generates a self-signed ECDSA certificate for hostname and
// returns a server *tls.Config plus the certificate in PEM form. It returns
// an error instead of calling t.Fatalf, making it usable outside test code.
func selfSignedTLSNoT(hostname string) (*tls.Config, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("fakesmtp: gen key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{hostname},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("fakesmtp: create cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("fakesmtp: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("fakesmtp: x509 keypair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, certPEM, nil
}
