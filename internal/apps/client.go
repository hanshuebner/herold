package apps

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/netguard"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// WellKnownManifestPath is where an app publishes its manifest
// (REQ-APP-20).
const WellKnownManifestPath = "/.well-known/herold-app.json"

// DefaultTimeout bounds one manifest fetch or invocation when
// ClientOptions.Timeout is zero (REQ-APP-42).
const DefaultTimeout = 15 * time.Second

// ClientOptions configures a Client.
type ClientOptions struct {
	// Timeout bounds one request end to end: dial, TLS, request body,
	// response headers and response body.
	Timeout time.Duration
	// AllowedInsecureOrigins lists origins exempt from the HTTPS
	// requirement and the private/loopback refusal (REQ-APP-61).
	// Entries are normalised with sysconfig.NormalizeOrigin.
	AllowedInsecureOrigins []string
	// UserAgent is sent on every request ("herold/<version>").
	UserAgent string
	// RootCAs overrides the system trust store for HTTPS origins; nil
	// uses the system roots.
	RootCAs *x509.CertPool
}

// Client reaches app origins over HTTP through internal/netguard
// (REQ-APP-60). Every request is pinned to one origin: the dial target
// must be that origin's host and port, redirects are never followed,
// and private and loopback destinations are refused unless the origin
// is listed in AllowedInsecureOrigins.
type Client struct {
	timeout   time.Duration
	userAgent string
	rootCAs   *x509.CertPool
	insecure  map[string]bool
}

// NewClient builds a Client. Invalid entries in AllowedInsecureOrigins
// are ignored; sysconfig.Validate rejects them before boot.
func NewClient(opts ClientOptions) *Client {
	c := &Client{
		timeout:   opts.Timeout,
		userAgent: opts.UserAgent,
		rootCAs:   opts.RootCAs,
		insecure:  make(map[string]bool, len(opts.AllowedInsecureOrigins)),
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.userAgent == "" {
		c.userAgent = "herold"
	}
	for _, o := range opts.AllowedInsecureOrigins {
		if n, err := sysconfig.NormalizeOrigin(o); err == nil {
			c.insecure[n] = true
		}
	}
	return c
}

// IsInsecureAllowed reports whether origin (normalised) is exempt from
// the HTTPS and public-address requirements.
func (c *Client) IsInsecureAllowed(origin string) bool { return c.insecure[origin] }

// CheckOrigin normalises raw with sysconfig.NormalizeOrigin and
// verifies the result may be registered: https, or listed in
// AllowedInsecureOrigins.
func (c *Client) CheckOrigin(raw string) (string, error) {
	origin, err := sysconfig.NormalizeOrigin(raw)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(origin, "https://") && !c.insecure[origin] {
		return "", fmt.Errorf("origin %q: must use https", origin)
	}
	return origin, nil
}

// originHostPort returns the dial target "host:port" of a normalised
// origin.
func originHostPort(origin string) (string, string, string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", "", "", err
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return u.Scheme, u.Hostname(), port, nil
}

// httpClient builds the pinned, guarded client for one origin.
func (c *Client) httpClient(origin string) (*http.Client, error) {
	scheme, host, port, err := originHostPort(origin)
	if err != nil {
		return nil, err
	}
	insecure := c.insecure[origin]
	if scheme != "https" && !insecure {
		return nil, fmt.Errorf("origin %q: must use https", origin)
	}
	portNum, _ := strconv.Atoi(port)
	opts := netguard.GuardOptions{
		AllowedPorts: []int{portNum},
		RequireHTTPS: !insecure,
		DialTimeout:  c.timeout,
	}
	if insecure {
		opts.AllowedHosts = []string{host}
	}
	guard := netguard.NewGuard(opts)
	pinned := net.JoinHostPort(host, port)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, p, err := net.SplitHostPort(addr)
		if err != nil || !strings.EqualFold(net.JoinHostPort(h, p), pinned) {
			return nil, fmt.Errorf("apps: dial target %q is not the app origin", addr)
		}
		return guard.DialContext(ctx, network, addr)
	}
	tr := &http.Transport{
		DialContext:           dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.rootCAs},
		TLSHandshakeTimeout:   c.timeout,
		ResponseHeaderTimeout: c.timeout,
		MaxIdleConns:          1,
		DisableKeepAlives:     true,
		Proxy:                 nil,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   c.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// FetchError reports a manifest fetch that failed before validation:
// transport error, redirect, non-200 status, wrong content type or an
// oversized body.
type FetchError struct {
	Reason string
}

func (e *FetchError) Error() string { return "apps: manifest fetch: " + e.Reason }

// FetchedManifest is a fetched and validated manifest.
type FetchedManifest struct {
	Manifest  *Manifest
	Canonical []byte
	Digest    string
	Warnings  []string
}

// FetchManifest fetches GET {origin}/.well-known/herold-app.json and
// validates it (REQ-APP-20..22). origin must already be normalised.
// Returns *FetchError or *ManifestError on failure.
func (c *Client) FetchManifest(ctx context.Context, origin string) (*FetchedManifest, error) {
	hc, err := c.httpClient(origin)
	if err != nil {
		return nil, &FetchError{Reason: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+WellKnownManifestPath, nil)
	if err != nil {
		return nil, &FetchError{Reason: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Herold-App-Api", strconv.Itoa(ManifestAPIVersion))
	resp, err := hc.Do(req)
	if err != nil {
		return nil, &FetchError{Reason: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxManifestBytes+1))
	if err != nil {
		return nil, &FetchError{Reason: "read body: " + err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &FetchError{Reason: fmt.Sprintf("GET %s answered %d", WellKnownManifestPath, resp.StatusCode)}
	}
	if !isJSONContentType(resp.Header.Get("Content-Type")) {
		return nil, &FetchError{Reason: fmt.Sprintf("Content-Type %q is not application/json",
			resp.Header.Get("Content-Type"))}
	}
	if len(body) > MaxManifestBytes {
		return nil, &FetchError{Reason: fmt.Sprintf("manifest exceeds %d bytes", MaxManifestBytes)}
	}
	m, warnings, err := ParseManifest(body)
	if err != nil {
		return nil, err
	}
	canon, digest := m.Canonical()
	return &FetchedManifest{Manifest: m, Canonical: canon, Digest: digest, Warnings: warnings}, nil
}

func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}
