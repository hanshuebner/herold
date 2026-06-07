package imapimport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"

	imapclient "github.com/emersion/go-imap/v2/imapclient"
	gosql "github.com/emersion/go-sasl"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// productionDialer is the Dialer implementation that uses the
// emersion/go-imap/v2 imapclient package. It is the only file that
// imports imapclient; the rest of the package tests against fakeDialer.
type productionDialer struct {
	cfg sysconfig.IMAPImportConfig
}

func newProductionDialer(cfg sysconfig.IMAPImportConfig) *productionDialer {
	return &productionDialer{cfg: cfg}
}

// Dial connects to the upstream IMAP server as described in p, completes
// the TLS handshake and IMAP authentication, then returns an open Conn.
// The CredentialPlaintext field is consumed during authentication and
// never stored (REQ-IMAP-IMP-70).
func (d *productionDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	// Defensive: tls_mode=none must never reach here (REQ-IMAP-IMP-06).
	if p.TLSMode != string(store.IMAPImportTLSModeImplicit) &&
		p.TLSMode != string(store.IMAPImportTLSModeSTARTTLS) {
		return nil, fmt.Errorf("imapimport: tls_mode=%q is not permitted; connection must use TLS (REQ-IMAP-IMP-06)", p.TLSMode)
	}

	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: p.Host},
		// DebugWriter intentionally left nil — wire-level tracing is
		// only enabled via a separate debug flag and must redact auth
		// payloads (REQ-IMAP-IMP-71).
	}

	var client *imapclient.Client
	var err error

	switch p.TLSMode {
	case string(store.IMAPImportTLSModeImplicit):
		client, err = imapclient.DialTLS(addr, opts)
	case string(store.IMAPImportTLSModeSTARTTLS):
		client, err = imapclient.DialStartTLS(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("imapimport: dial %s: %w", addr, err)
	}

	// Authenticate — credentials used here and then discarded.
	if authErr := d.authenticate(client, p); authErr != nil {
		client.Close()
		return nil, authErr
	}

	return &prodConn{client: client}, nil
}

// authenticate performs the IMAP authentication exchange using the
// appropriate mechanism. Credentials are consumed here and never stored
// in the returned Conn. (REQ-IMAP-IMP-70, REQ-IMAP-IMP-71.)
func (d *productionDialer) authenticate(client *imapclient.Client, p dialParams) error {
	switch p.AuthMethod {
	case string(store.IMAPImportAuthMethodPassword),
		string(store.IMAPImportAuthMethodAppPassword):
		// Both use the same IMAP mechanism (LOGIN or AUTHENTICATE PLAIN).
		// Prefer AUTHENTICATE PLAIN when the server advertises AUTH=PLAIN,
		// otherwise fall back to LOGIN.
		caps := client.Caps()
		if caps.Has(imap.AuthCap("PLAIN")) {
			saslClient := gosql.NewPlainClient("", p.Username, p.CredentialPlaintext)
			if err := client.Authenticate(saslClient); err != nil {
				return fmt.Errorf("imapimport: AUTHENTICATE PLAIN: %w", err)
			}
			return nil
		}
		if err := client.Login(p.Username, p.CredentialPlaintext).Wait(); err != nil {
			return fmt.Errorf("imapimport: LOGIN: %w", err)
		}
		return nil

	case string(store.IMAPImportAuthMethodXOAuth2):
		// CredentialPlaintext is a short-lived access token at this point
		// (the accountWorker already called oauthTokenSource.Token before
		// calling Dial). The XOAUTH2 SASL payload is:
		//   base64("user=" + username + "\x01auth=Bearer " + token + "\x01\x01")
		saslClient := newXOAuth2Client(p.Username, p.CredentialPlaintext)
		if err := client.Authenticate(saslClient); err != nil {
			return fmt.Errorf("imapimport: AUTHENTICATE XOAUTH2: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("imapimport: unknown auth_method=%q", p.AuthMethod)
	}
}

// prodConn wraps *imapclient.Client and satisfies the Conn interface.
// Sub-steps 3b-3e will add Select, Search, Fetch, Store, Idle methods
// to both this struct and the Conn interface.
type prodConn struct {
	client *imapclient.Client
}

func (c *prodConn) Caps() imap.CapSet {
	return c.client.Caps()
}

func (c *prodConn) Logout() error {
	cmd := c.client.Logout()
	return cmd.Wait()
}

func (c *prodConn) Close() error {
	return c.client.Close()
}
