package imapimport

// fakedialer_test.go provides fake Dialer implementations used by
// the 3a-3e tests. White-box (package imapimport) so seam interfaces
// are directly visible without exported wrappers.

import (
	"context"
	"crypto/tls"
	"fmt"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gosql "github.com/emersion/go-sasl"
)

// --------------------------------------------------------------------------
// fakeDialer
// --------------------------------------------------------------------------

// fakeDialer connects to the in-process testIMAPServer with the TLS
// mode indicated by p.TLSMode. Authentication follows the production
// path so auth-method tests have real code coverage.
//
// If overrideFail is non-nil, Dial returns it immediately without
// touching the test server.
type fakeDialer struct {
	ts           *testIMAPServer
	overrideFail error
}

func (d *fakeDialer) Dial(_ context.Context, p dialParams) (Conn, error) {
	if d.overrideFail != nil {
		return nil, d.overrideFail
	}

	var addr string
	var opts *imapclient.Options
	switch p.TLSMode {
	case "implicit":
		addr = d.ts.ImplicitAddr
		opts = &imapclient.Options{
			TLSConfig: &tls.Config{
				RootCAs:    d.ts.ClientTLSConfig.RootCAs,
				ServerName: "127.0.0.1",
			},
		}
	case "starttls":
		addr = d.ts.StartTLSAddr
		opts = &imapclient.Options{
			TLSConfig: &tls.Config{
				RootCAs:    d.ts.ClientTLSConfig.RootCAs,
				ServerName: "127.0.0.1",
			},
		}
	default:
		return nil, fmt.Errorf("fakeDialer: unsupported TLSMode=%q", p.TLSMode)
	}

	var client *imapclient.Client
	var err error
	switch p.TLSMode {
	case "implicit":
		client, err = imapclient.DialTLS(addr, opts)
	case "starttls":
		client, err = imapclient.DialStartTLS(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("fakeDialer: dial %s: %w", addr, err)
	}

	if authErr := fakeAuth(client, p); authErr != nil {
		client.Close()
		return nil, authErr
	}
	return &prodConn{client: client}, nil
}

// fakeAuth mirrors productionDialer.authenticate for the fake path.
func fakeAuth(client *imapclient.Client, p dialParams) error {
	switch p.AuthMethod {
	case "password", "app_password":
		caps := client.Caps()
		if caps.Has(imap.AuthCap("PLAIN")) {
			saslClient := gosql.NewPlainClient("", p.Username, p.CredentialPlaintext)
			if err := client.Authenticate(saslClient); err != nil {
				return fmt.Errorf("authenticate: PLAIN: %w", err)
			}
			return nil
		}
		if err := client.Login(p.Username, p.CredentialPlaintext).Wait(); err != nil {
			return fmt.Errorf("authenticate: LOGIN: %w", err)
		}
		return nil
	case "xoauth2":
		// The imapmemserver supports only LOGIN; test the XOAUTH2 SASL
		// payload construction via a real SASL exchange in xoauth2_test.go.
		// Here we map the already-exchanged access token to LOGIN so the
		// accountWorker's token-exchange path is exercised end-to-end.
		if err := client.Login(p.Username, p.CredentialPlaintext).Wait(); err != nil {
			return fmt.Errorf("authenticate: xoauth2-test-LOGIN: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("fakeDialer: unknown auth_method=%q", p.AuthMethod)
	}
}

// --------------------------------------------------------------------------
// badCredDialer: always returns an auth-flavored error
// --------------------------------------------------------------------------

// badCredDialer always returns an authentication error. It never
// reaches the server so tests can run without a live listener.
type badCredDialer struct{}

func (d *badCredDialer) Dial(_ context.Context, _ dialParams) (Conn, error) {
	return nil, fmt.Errorf("authenticate: LOGIN failed: [NO] Invalid credentials")
}

// --------------------------------------------------------------------------
// fakeTokenSource: injectable oauthTokenSource
// --------------------------------------------------------------------------

// fakeTokenSource returns a fixed access token or error. Tests inspect
// the calls counter to verify the worker called Token the right number
// of times.
type fakeTokenSource struct {
	accessToken string
	err         error
	calls       int
}

func (f *fakeTokenSource) Token(_ context.Context, _ string) (string, error) {
	f.calls++
	return f.accessToken, f.err
}

// xoauthFakeDialer uses a fakeTokenSource, performs the token exchange
// inside Dial (mirroring the accountWorker's openCredential flow for the
// xoauth2 path), and then authenticates against the in-process server
// using the exchanged access token as a LOGIN password.
type xoauthFakeDialer struct {
	ts  *testIMAPServer
	tok *fakeTokenSource
}

func (d *xoauthFakeDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	if p.AuthMethod != "xoauth2" {
		return nil, fmt.Errorf("xoauthFakeDialer: expected xoauth2, got %q", p.AuthMethod)
	}
	// The refreshToken is in CredentialPlaintext at this point because
	// accountWorker.openCredential delegates to tokenSourceForProvider for
	// xoauth2. However, for the xoauth2 test path the accountWorker has
	// already called the token source (via openCredential) and passed the
	// access token as CredentialPlaintext. So here CredentialPlaintext IS
	// the access token; we just use it as a LOGIN password.
	accessToken := p.CredentialPlaintext
	_ = accessToken

	opts := &imapclient.Options{
		TLSConfig: &tls.Config{
			RootCAs:    d.ts.ClientTLSConfig.RootCAs,
			ServerName: "127.0.0.1",
		},
	}
	client, err := imapclient.DialTLS(d.ts.ImplicitAddr, opts)
	if err != nil {
		return nil, fmt.Errorf("xoauthFakeDialer: dial: %w", err)
	}
	if err := client.Login(p.Username, accessToken).Wait(); err != nil {
		client.Close()
		return nil, fmt.Errorf("authenticate: xoauth2 LOGIN failed: %w", err)
	}
	return &prodConn{client: client}, nil
}
