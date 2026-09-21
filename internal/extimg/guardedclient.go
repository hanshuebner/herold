package extimg

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// NewGuardedClient builds an *http.Client whose Transport enforces the
// SSRF guard (REQ-EXTIMG-30..37) for one-off non-image fetches that
// still need the same "never let the destination resolve inside the
// operator's network" protection as the external-image fetcher --
// e.g. the Email/unsubscribe one-click POST (issue #412). cfg supplies
// the deny-list / allow-private / allowed-ports policy (typically the
// operator's [external_images] configuration, reused rather than
// duplicated); timeout bounds the whole request (connect + headers +
// body). Every redirect target is re-validated exactly like the image
// Fetcher's client (REQ-EXTIMG-26/34).
//
// When cfg.ExtraCACertPEM is set, the returned client trusts that
// certificate in addition to (never instead of) the process's system
// root pool. This is how a test/dev fake origin's self-signed
// certificate becomes trusted for exactly this guarded client, without
// touching process-wide TLS verification (e.g. via SSL_CERT_FILE,
// which some platforms' crypto/x509 implementations ignore).
func NewGuardedClient(cfg Config, timeout time.Duration) *http.Client {
	cfg.resolveOptional()
	guard := NewSSRFGuard(cfg)
	transport := &http.Transport{
		DialContext:           guard.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   cfg.PerImageConnectTimeout,
		ResponseHeaderTimeout: cfg.PerImageConnectTimeout,
	}
	if tlsCfg := extraCATLSConfig(cfg.ExtraCACertPEM); tlsCfg != nil {
		transport.TLSClientConfig = tlsCfg
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= cfg.FollowRedirectsMax {
				return errTooManyRedirects
			}
			return guard.ValidateURL(req.URL)
		},
	}
}

// extraCATLSConfig returns a *tls.Config trusting extraCACertPEM in
// addition to (never instead of) the process's system root pool, or
// nil when extraCACertPEM is empty or fails to parse. Shared by
// NewGuardedClient and NewFetcher so both guarded clients trust a
// configured [external_images.network] extra_ca_file identically --
// the operator/dev-instance origin issue #412 introduced this for
// (Email/unsubscribe) and issue #443 relies on for the image fetcher
// itself.
func extraCATLSConfig(extraCACertPEM []byte) *tls.Config {
	if len(extraCACertPEM) == 0 {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(extraCACertPEM) {
		return nil
	}
	return &tls.Config{RootCAs: pool}
}
