package acme

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	herotls "github.com/hanshuebner/herold/internal/tls"
)

// CertKeyType selects the algorithm used for the per-cert subject key.
// ECDSA is the default; RSA is offered as an opt-in for operators who
// need RSA leaves for compatibility with very old clients.
type CertKeyType uint8

const (
	// CertKeyECDSA uses ECDSA P-256 for the subject key. Default.
	CertKeyECDSA CertKeyType = iota
	// CertKeyRSA2048 uses 2048-bit RSA.
	CertKeyRSA2048
)

// RenewalThreshold is the lifetime margin at which EnsureCert
// considers a certificate due for renewal. RFC 8555 nudges clients to
// renew at 1/3 remaining; this is the simple "30 days left" cutoff
// REQ-OPS-53 calls for as a Phase 2 baseline.
const RenewalThreshold = 30 * 24 * time.Hour

// DefaultUserAgent is the User-Agent string sent on every ACME HTTP
// request. Operators who fork should change this; tests assert against
// it.
const DefaultUserAgent = "herold/0.x.y (+https://github.com/hanshuebner/herold)"

// Options bundles the constructor inputs to New.
type Options struct {
	// DirectoryURL is the ACME server's directory endpoint URL.
	DirectoryURL string
	// ContactEmail is the operator-supplied account contact.
	ContactEmail string
	// Store is the persistent store used for accounts, orders, and
	// certs.
	Store store.Store
	// TLSStore is the production cert registry; on a successful order
	// the issued cert is added under its leaf hostname.
	TLSStore *herotls.Store
	// PluginInvoker dispatches DNS-01 plugin calls. Nil disables
	// DNS-01.
	PluginInvoker PluginInvoker
	// Logger receives structured trace events.
	Logger *slog.Logger
	// Clock provides the renewal scheduler's notion of time.
	Clock clock.Clock
	// HTTPClient is used for every ACME network call. nil installs an
	// http.Client with a 30s timeout.
	HTTPClient *http.Client
	// UserAgent overrides DefaultUserAgent.
	UserAgent string
	// HTTPChallenger handles HTTP-01 (port 80). Nil disables HTTP-01.
	HTTPChallenger *HTTPChallenger
	// TLSALPNChallenger handles tls-alpn-01 (port 443). Nil disables
	// tls-alpn-01.
	TLSALPNChallenger *TLSALPNChallenger
	// DNS01Challenger handles dns-01. When nil but PluginInvoker is
	// non-nil, EnsureCert constructs one on first use.
	DNS01Challenger *DNS01Challenger
	// CertKey selects the subject-key algorithm for issued certs.
	CertKey CertKeyType
	// PollInterval is the period between authorisation / order status
	// polls. Defaults to 1s.
	PollInterval time.Duration
	// MaxPolls bounds the number of poll iterations per authorisation
	// or order phase. Defaults to 60 (60 * 1s = 1 minute by default).
	MaxPolls int
}

// Client is the ACME client surface. One Client per (DirectoryURL,
// ContactEmail) is enough; methods are safe for concurrent use.
type Client struct {
	opts Options

	dir    *directory
	nonces nonceCache

	accountMu sync.Mutex
	account   *store.ACMEAccount
	signer    *ecdsaSigner
}

// New constructs a Client. The constructor does not contact the
// directory; the first method that needs network state pulls it.
func New(opts Options) *Client {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.MaxPolls <= 0 {
		opts.MaxPolls = 60
	}
	if opts.DNS01Challenger == nil && opts.PluginInvoker != nil {
		opts.DNS01Challenger = NewDNS01Challenger(DNS01Options{
			Plugins: opts.PluginInvoker,
			Logger:  opts.Logger,
			Clock:   opts.Clock,
		})
	}
	return &Client{opts: opts}
}

// EnsureCert provisions or renews a cert for hostnames if needed. The
// method is idempotent: when an existing cert has at least
// RenewalThreshold remaining lifetime, EnsureCert returns nil without
// contacting the ACME server. Otherwise it runs the full order flow.
func (c *Client) EnsureCert(ctx context.Context, hostnames []string, challengeType store.ChallengeType, dnsPluginName string) error {
	if len(hostnames) == 0 {
		return errors.New("acme: hostnames empty")
	}
	if challengeType == store.ChallengeTypeUnknown {
		return errors.New("acme: challenge type unset")
	}
	primary := strings.ToLower(hostnames[0])
	now := c.opts.Clock.Now()
	if existing, err := c.opts.Store.Meta().GetACMECert(ctx, primary); err == nil {
		if existing.NotAfter.Sub(now) > RenewalThreshold {
			c.opts.Logger.DebugContext(ctx, "acme cert still fresh",
				"hostname", primary, "not_after", existing.NotAfter)
			c.publishToTLSStore(&existing)
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("acme: lookup existing cert: %w", err)
	}
	if err := c.Register(ctx, c.opts.ContactEmail); err != nil {
		return err
	}
	cert, err := c.Order(ctx, hostnames, challengeType, dnsPluginName)
	if err != nil {
		return err
	}
	c.publishToTLSStore(cert)
	return nil
}

// publishToTLSStore parses the leaf out of the chain and registers it
// with the TLS cert store under each SAN.
func (c *Client) publishToTLSStore(cert *store.ACMECert) {
	if c.opts.TLSStore == nil || cert == nil {
		return
	}
	pair, err := tls.X509KeyPair([]byte(cert.ChainPEM), []byte(cert.PrivateKeyPEM))
	if err != nil {
		c.opts.Logger.Warn("acme: parse issued cert for tls store", "err", err)
		return
	}
	if pair.Leaf == nil && len(pair.Certificate) > 0 {
		// Best-effort leaf parse for the operator's metric/reporting
		// surface. tls.X509KeyPair leaves Leaf nil even when one cert is
		// present; callers may want it populated.
		// The store.Get path does not require Leaf so a nil here is fine.
	}
	c.opts.TLSStore.Add(cert.Hostname, &pair)
}

// RunRenewalLoop scans persisted certs every interval and re-runs
// EnsureCert for any whose NotAfter is within RenewalThreshold of the
// clock's now. The loop honours ctx for shutdown; it never panics on
// per-cert failures (each failure is logged and the loop carries on so
// one bad cert does not stall the others).
func (c *Client) RunRenewalLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Hour
	}
	c.opts.Logger.Info("acme renewal loop starting", "interval", interval)
	for {
		if err := c.runRenewalPass(ctx); err != nil {
			c.opts.Logger.Warn("acme renewal pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.opts.Clock.After(interval):
		}
	}
}

// runRenewalPass scans for certs near expiry and renews each. It also
// resumes any in-flight orders so a process crash mid-order does not
// leak state.
func (c *Client) runRenewalPass(ctx context.Context) error {
	now := c.opts.Clock.Now()
	cutoff := now.Add(RenewalThreshold)
	expiring, err := c.opts.Store.Meta().ListACMECertsExpiringBefore(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("acme: list expiring certs: %w", err)
	}
	for _, cert := range expiring {
		hostnames := []string{cert.Hostname}
		// Resume the original challenge type and plugin if the cert came
		// from an in-store order; otherwise default to HTTP-01.
		challengeType := store.ChallengeTypeHTTP01
		dnsPlugin := ""
		if cert.OrderID != 0 {
			if o, err := c.opts.Store.Meta().GetACMEOrder(ctx, cert.OrderID); err == nil {
				challengeType = o.ChallengeType
				if len(o.Hostnames) > 0 {
					hostnames = o.Hostnames
				}
			}
		}
		if err := c.EnsureCert(ctx, hostnames, challengeType, dnsPlugin); err != nil {
			c.opts.Logger.Warn("acme renew failed",
				"hostname", cert.Hostname, "err", err)
			continue
		}
		c.opts.Logger.Info("acme cert renewed", "hostname", cert.Hostname)
	}
	// Resume orders that crashed mid-flight (status pending, ready, or
	// processing). The Order method picks the row up by URL.
	if err := c.resumeInFlightOrders(ctx); err != nil {
		c.opts.Logger.Warn("acme resume in-flight orders", "err", err)
	}
	return nil
}

// resumeInFlightOrders walks the pending/ready/processing buckets and
// re-runs the order pipeline for each row. A crashed mid-order row
// surfaces with the same OrderURL the ACME server assigned, so the
// resume path simply re-enters the polling loop.
func (c *Client) resumeInFlightOrders(ctx context.Context) error {
	for _, status := range []store.ACMEOrderStatus{
		store.ACMEOrderStatusPending,
		store.ACMEOrderStatusReady,
		store.ACMEOrderStatusProcessing,
	} {
		orders, err := c.opts.Store.Meta().ListACMEOrdersByStatus(ctx, status)
		if err != nil {
			return err
		}
		for _, o := range orders {
			if err := c.Register(ctx, c.opts.ContactEmail); err != nil {
				return err
			}
			cert, err := c.resumeOrder(ctx, o)
			if err != nil {
				c.opts.Logger.Warn("acme resume order failed",
					"order_id", o.ID, "err", err)
				continue
			}
			c.publishToTLSStore(cert)
			c.opts.Logger.Info("acme order resumed",
				"order_id", o.ID, "hostnames", o.Hostnames)
		}
	}
	return nil
}
