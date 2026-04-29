package extsubmit

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protosmtp/session"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
)

// dialFunc is the injectable dialer type, identical to net.Dialer.DialContext.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Submitter performs external SMTP submission for a single IdentitySubmission.
// It is safe for concurrent use; each call to Submit or Probe creates its own
// SMTP session.
type Submitter struct {
	// DataKey is the 32-byte AEAD key used to unseal credential fields.
	DataKey []byte
	// HostName is the EHLO identity the submitter announces. Defaults to
	// "localhost" when empty.
	HostName string
	// Refresher handles OAuth token refresh. May be nil if no OAuth
	// identities will be submitted.
	Refresher *Refresher
	// dialFn is injectable for tests; nil uses net.Dialer.
	dialFn dialFunc
	// tlsWrapFn is injectable for tests; nil uses tls.Client with standard
	// verification against system roots.
	tlsWrapFn func(conn net.Conn, serverName string) (*tls.Conn, error)
	// Now is a clock function injected for deterministic testing.
	Now func() time.Time
}

// SetDialFn installs an injectable dialer for tests. The default dialer uses
// net.Dialer with a 30-second timeout. Tests should call this before Submit.
func (s *Submitter) SetDialFn(fn func(ctx context.Context, network, addr string) (net.Conn, error)) {
	s.dialFn = fn
}

// SetTLSWrapFn installs an injectable TLS wrapper for tests. The default
// wrapper performs a standard TLS handshake against system roots.
func (s *Submitter) SetTLSWrapFn(fn func(conn net.Conn, serverName string) (*tls.Conn, error)) {
	s.tlsWrapFn = fn
}

func (s *Submitter) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Submitter) hostName() string {
	if s.HostName != "" {
		return s.HostName
	}
	return "localhost"
}

// dial opens a TCP connection to host:port.
func (s *Submitter) dial(ctx context.Context, host string, port int) (net.Conn, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	if s.dialFn != nil {
		return s.dialFn(ctx, "tcp", addr)
	}
	d := &net.Dialer{Timeout: 30 * time.Second}
	return d.DialContext(ctx, "tcp", addr)
}

// tlsWrap upgrades conn to TLS against serverName.
func (s *Submitter) tlsWrap(conn net.Conn, serverName string) (*tls.Conn, error) {
	if s.tlsWrapFn != nil {
		return s.tlsWrapFn(conn, serverName)
	}
	cfg := &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}
	return tlsConn, nil
}

// accessToken returns the plaintext access token for sub, refreshing it if
// needed. The returned slice must be zeroed by the caller when done.
func (s *Submitter) accessToken(ctx context.Context, sub store.IdentitySubmission) ([]byte, error) {
	// If the token is not expired or refresh is not due yet, just open it.
	if s.Refresher != nil && !sub.RefreshDue.IsZero() && !s.now().Before(sub.RefreshDue) {
		// Token refresh is due. Refresher obtains and stores the new token;
		// but Refresh returns a plaintext string so we avoid double-open.
		// We seal a copy for the Refresher to store and use the returned
		// plaintext directly.
		if len(sub.OAuthRefreshCT) == 0 {
			// No refresh token; fall through to just opening the current one.
		} else {
			// Build OAuthClientCredentials from the sub row. In v1 the
			// operator-level credentials are carried on sub directly.
			creds := OAuthClientCredentials{
				ClientID:      sub.OAuthClientID,
				TokenEndpoint: sub.OAuthTokenEndpoint,
			}
			// ClientSecret is sealed in OAuthClientSecretCT when present.
			// In v1 this field may be nil (operator-level secret not
			// per-user). We leave ClientSecret empty in that case and let
			// the Refresher handle it — the token endpoint may not require
			// it for PKCE or public clients.
			if len(sub.OAuthClientSecretCT) > 0 {
				cs, err := secrets.Open(s.DataKey, sub.OAuthClientSecretCT)
				if err != nil {
					return nil, fmt.Errorf("extsubmit: open client secret: %w", err)
				}
				creds.ClientSecret = string(cs)
				for i := range cs {
					cs[i] = 0
				}
			}
			plaintext, err := s.Refresher.Refresh(ctx, sub, creds)
			if err != nil {
				return nil, err
			}
			return []byte(plaintext), nil
		}
	}
	return openAccessToken(s.DataKey, sub)
}

// buildSession dials, optionally wraps in TLS (implicit), reads the greeting,
// sends EHLO, optionally upgrades to TLS (STARTTLS), re-EHLOs, and returns the
// ready session. The caller owns the conn and must close it.
func (s *Submitter) buildSession(ctx context.Context, sub store.IdentitySubmission) (*session.Session, net.Conn, error) {
	conn, err := s.dial(ctx, sub.SubmitHost, sub.SubmitPort)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s:%d: %w", sub.SubmitHost, sub.SubmitPort, err)
	}

	var sess *session.Session

	switch sub.SubmitSecurity {
	case "implicit_tls":
		tlsConn, terr := s.tlsWrap(conn, sub.SubmitHost)
		if terr != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("implicit TLS %s: %w", sub.SubmitHost, terr)
		}
		sess = session.New(tlsConn)
		conn = tlsConn
	default:
		sess = session.New(conn)
	}

	// Greeting.
	gr, err := sess.ReadGreeting()
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("greeting: %w", err)
	}
	if !gr.IsSuccess() {
		conn.Close()
		return nil, nil, fmt.Errorf("greeting rejected: %d %s", gr.Code, gr.Text)
	}

	// EHLO.
	er, err := sess.Ehlo(s.hostName())
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("ehlo: %w", err)
	}
	if er.Code != 250 {
		conn.Close()
		return nil, nil, fmt.Errorf("ehlo rejected: %d %s", er.Code, er.Text)
	}

	// STARTTLS.
	if sub.SubmitSecurity == "starttls" {
		if !sess.HasExtension("STARTTLS") {
			conn.Close()
			return nil, nil, fmt.Errorf("server %s does not offer STARTTLS but submit_security=starttls", sub.SubmitHost)
		}
		r, err := sess.Cmd("STARTTLS")
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("STARTTLS: %w", err)
		}
		if r.Code != 220 {
			conn.Close()
			return nil, nil, fmt.Errorf("STARTTLS rejected: %d %s", r.Code, r.Text)
		}
		tlsConn, terr := s.tlsWrap(conn, sub.SubmitHost)
		if terr != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("STARTTLS handshake %s: %w", sub.SubmitHost, terr)
		}
		sess.UpgradeConn(tlsConn)
		conn = tlsConn
		// Re-EHLO per RFC 3207 §4.2.
		er2, err := sess.Ehlo(s.hostName())
		if err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("post-TLS ehlo: %w", err)
		}
		if er2.Code != 250 {
			conn.Close()
			return nil, nil, fmt.Errorf("post-TLS ehlo rejected: %d %s", er2.Code, er2.Text)
		}
	}

	return sess, conn, nil
}

// auth performs the configured SASL AUTH exchange for sess.
// authUser is the SMTP AUTH username — for password auth it is the account's
// email address (typically the From address); for oauth2 it is the XOAUTH2
// user= field (also the email address).
func (s *Submitter) auth(ctx context.Context, sess *session.Session, sub store.IdentitySubmission, authUser string) error {
	switch sub.SubmitAuthMethod {
	case "password":
		pass, err := openPassword(s.DataKey, sub)
		if err != nil {
			return err
		}
		defer func() {
			for i := range pass {
				pass[i] = 0
			}
		}()
		// Try PLAIN first; fall back to LOGIN when the server does not
		// advertise PLAIN in its AUTH list.
		authParam := strings.ToUpper(sess.ExtensionParam("AUTH"))
		if strings.Contains(authParam, "PLAIN") {
			return sess.AuthPlain(authUser, string(pass))
		}
		return sess.AuthLogin(authUser, string(pass))

	case "oauth2":
		token, err := s.accessToken(ctx, sub)
		if err != nil {
			return fmt.Errorf("access token: %w", err)
		}
		defer func() {
			for i := range token {
				token[i] = 0
			}
		}()
		return sess.AuthXOAUTH2(authUser, string(token))

	default:
		return fmt.Errorf("unsupported submit_auth_method %q", sub.SubmitAuthMethod)
	}
}

// Submit performs the full SMTP exchange for env using the credentials and
// endpoint in sub. It returns an Outcome for every code path; errors are
// captured in Outcome.Diagnostic rather than returned as Go errors, matching
// the pattern used by the queue delivery workers.
//
// No local retry: the returned Outcome is final (REQ-AUTH-EXT-SUBMIT-05).
func (s *Submitter) Submit(ctx context.Context, sub store.IdentitySubmission, env Envelope) Outcome {
	out := Outcome{CorrelationID: env.CorrelationID}

	sess, conn, err := s.buildSession(ctx, sub)
	if err != nil {
		out.State = OutcomeUnreachable
		out.Diagnostic = err.Error()
		return out
	}
	defer func() {
		_ = sess.Quit()
		_ = conn.Close()
	}()

	// AUTH. The username for both password and oauth2 is the From address,
	// which is the same as env.MailFrom (the identity's email address).
	if err := s.auth(ctx, sess, sub, env.MailFrom); err != nil {
		if isAuthError(err) {
			out.State = OutcomeAuthFailed
			out.Diagnostic = fmt.Sprintf("auth: %s", err.Error())
		} else {
			out.State = OutcomeUnreachable
			out.Diagnostic = fmt.Sprintf("auth: %s", err.Error())
		}
		return out
	}

	// MAIL FROM.
	mailFrom := env.MailFrom
	r, err := sess.MailFrom(mailFrom)
	if err != nil {
		out.State = OutcomeUnreachable
		out.Diagnostic = fmt.Sprintf("MAIL FROM: %s", err.Error())
		return out
	}
	if !r.IsSuccess() {
		out.State = mapSMTPCode(r.Code)
		out.Diagnostic = fmt.Sprintf("MAIL FROM: %d %s", r.Code, r.Text)
		return out
	}

	// RCPT TO (all recipients).
	for _, rcpt := range env.RcptTo {
		r, err := sess.RcptTo(rcpt)
		if err != nil {
			out.State = OutcomeUnreachable
			out.Diagnostic = fmt.Sprintf("RCPT TO <%s>: %s", rcpt, err.Error())
			return out
		}
		if !r.IsSuccess() {
			out.State = mapSMTPCode(r.Code)
			out.Diagnostic = fmt.Sprintf("RCPT TO <%s>: %d %s", rcpt, r.Code, r.Text)
			return out
		}
	}

	// DATA.
	mtaID, err := sess.Data(env.Body)
	if err != nil {
		// err text already includes the SMTP code.
		if strings.HasPrefix(err.Error(), "DATA final:") {
			out.State = OutcomePermanent // default; mapSMTPCode not accessible here
		} else {
			out.State = OutcomeUnreachable
		}
		out.Diagnostic = fmt.Sprintf("DATA: %s", err.Error())
		return out
	}

	out.State = OutcomeOK
	out.MTAID = mtaID
	out.Diagnostic = fmt.Sprintf("accepted by %s: %s", sub.SubmitHost, mtaID)
	return out
}

// Probe performs an AUTH-only session (EHLO + AUTH + QUIT) to verify that the
// configured credentials are accepted by the remote. It does not send any mail.
// This avoids MAIL FROM:<> which Gmail and M365 reject.
func (s *Submitter) Probe(ctx context.Context, sub store.IdentitySubmission) Outcome {
	out := Outcome{}

	sess, conn, err := s.buildSession(ctx, sub)
	if err != nil {
		out.State = OutcomeUnreachable
		out.Diagnostic = err.Error()
		return out
	}
	defer func() {
		_ = sess.Quit()
		_ = conn.Close()
	}()

	// For Probe we need a username. Sub.OAuthClientID is used as a
	// fallback when the caller hasn't provided one via the envelope, but
	// Probe has no envelope. We use OAuthClientID here as it is the only
	// per-user identifier available on the sub row. Callers that want
	// to probe with the canonical email should set OAuthClientID to the
	// email address (which is the intended usage: OAuthClientID for the
	// per-user identifier in XOAUTH2 is the user's email, not the app's
	// client_id in v1).
	probeUser := sub.OAuthClientID
	if err := s.auth(ctx, sess, sub, probeUser); err != nil {
		if isAuthError(err) {
			out.State = OutcomeAuthFailed
			out.Diagnostic = fmt.Sprintf("auth probe: %s", err.Error())
		} else {
			out.State = OutcomeUnreachable
			out.Diagnostic = fmt.Sprintf("auth probe: %s", err.Error())
		}
		return out
	}

	out.State = OutcomeOK
	out.Diagnostic = fmt.Sprintf("probe ok: authenticated to %s", sub.SubmitHost)
	return out
}

// isAuthError reports whether err originated from an AUTH rejection.
// It checks for the error patterns produced by session.AuthPlain,
// AuthLogin, AuthXOAUTH2, and ErrAuthFailed from the OAuth refresher.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "auth plain:") ||
		strings.Contains(msg, "auth login:") ||
		strings.Contains(msg, "auth xoauth2:") ||
		strings.Contains(msg, "auth failed") ||
		// 535 appears in AUTH rejection messages from session methods.
		strings.Contains(msg, "535")
}

// mapSMTPCode maps a non-2xx SMTP reply code to an OutcomeState.
func mapSMTPCode(code int) OutcomeState {
	switch code / 100 {
	case 4:
		return OutcomeTransient
	case 5:
		return OutcomePermanent
	default:
		return OutcomeTransient
	}
}
