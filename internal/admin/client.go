package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Client is the CLI-side admin REST client. It carries the API key (from
// env or credentials file), a base URL, and a bounded timeout. Every
// public method is a thin wrapper around an HTTP call returning a typed
// error (RFC 7807 problem+json).
type Client struct {
	apiKey  string
	timeout time.Duration
	http    *http.Client

	// mu guards base and fallbackURL, which can mutate after a
	// successful fallback (see switchToFallback).
	mu          sync.Mutex
	base        string
	fallbackURL string

	// credentialsPath is the credentials.toml that supplied base (empty
	// when base came from --server-url). It powers both the fallback
	// rewrite and the "may be stale" hint on a connection error.
	credentialsPath string
	// warnW receives the one-line fallback warning; defaults to
	// io.Discard.
	warnW    io.Writer
	warnOnce sync.Once
}

// ClientOptions configures a Client.
type ClientOptions struct {
	// BaseURL is the admin REST origin, e.g. "https://127.0.0.1:8080".
	// Required. Must be http or https.
	BaseURL string
	// APIKey overrides the key loaded from HEROLD_API_KEY / credentials.
	APIKey string
	// Timeout bounds each request. Zero falls back to 30s.
	Timeout time.Duration
	// HTTPClient replaces the default client (tests use this to attach
	// fake transports or self-signed trust roots).
	HTTPClient *http.Client

	// FallbackURL, when non-empty and different from BaseURL, is retried
	// once if a request to BaseURL fails at the connection level
	// (refused / dial timeout). CredentialsPath should also be set: on a
	// successful fallback the client emits one warning to WarnW naming
	// both URLs and the credentials file, rewrites the file with
	// FallbackURL, and uses FallbackURL for the rest of its calls.
	// re #315.
	FallbackURL string
	// CredentialsPath is the credentials.toml path that supplied
	// BaseURL (empty when BaseURL came from --server-url). It names the
	// file in both the fallback warning and in a connection error when
	// no fallback is available.
	CredentialsPath string
	// WarnW receives the one-line fallback warning. Defaults to
	// io.Discard.
	WarnW io.Writer
}

// NewClient constructs a Client. If opts.APIKey is empty, the env var
// HEROLD_API_KEY is consulted, then ~/.herold/credentials.toml.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("admin-client: base URL required (override via --server-url or config)")
	}
	parsed, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("admin-client: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("admin-client: unsupported scheme %q (want http or https)", parsed.Scheme)
	}
	key := opts.APIKey
	if key == "" {
		key = os.Getenv("HEROLD_API_KEY")
	}
	if key == "" {
		if loaded, ok := loadCredentials(); ok {
			key = loaded
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	warnW := opts.WarnW
	if warnW == nil {
		warnW = io.Discard
	}
	fallback := strings.TrimRight(opts.FallbackURL, "/")
	base := strings.TrimRight(opts.BaseURL, "/")
	if fallback == base {
		fallback = ""
	}
	return &Client{
		apiKey:          key,
		timeout:         timeout,
		http:            hc,
		base:            base,
		fallbackURL:     fallback,
		credentialsPath: opts.CredentialsPath,
		warnW:           warnW,
	}, nil
}

// ProblemDetails is the RFC 7807 error payload.
type ProblemDetails struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
	Code   string `json:"code,omitempty"`
}

// Error reports the problem as a plain Go error.
func (p *ProblemDetails) Error() string {
	switch {
	case p.Detail != "":
		return fmt.Sprintf("admin: %d %s: %s", p.Status, p.Title, p.Detail)
	case p.Title != "":
		return fmt.Sprintf("admin: %d %s", p.Status, p.Title)
	default:
		return fmt.Sprintf("admin: HTTP %d", p.Status)
	}
}

// do issues an HTTP request with the client's API key, decodes a typed
// body on 2xx, or a ProblemDetails on error.
func (c *Client) do(ctx context.Context, method, path string, body any, into any) error {
	if c == nil {
		return errors.New("admin-client: nil client")
	}
	resp, raw, err := c.execute(ctx, method, path, body, true)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var pd ProblemDetails
		if err := json.Unmarshal(raw, &pd); err != nil || pd.Status == 0 {
			pd = ProblemDetails{Status: resp.StatusCode, Title: http.StatusText(resp.StatusCode), Detail: string(raw)}
		}
		return &pd
	}
	if into != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, into); err != nil {
			return fmt.Errorf("admin-client: decode response: %w", err)
		}
	}
	return nil
}

// doRaw issues a GET (or other method with no request body) and returns
// the raw response bytes verbatim on 2xx, without attempting a JSON
// decode -- for endpoints that serve a non-JSON content type (e.g.
// GET .../held/{hid}/raw's message/rfc822). Errors still decode as
// RFC 7807 problem+json, exactly like do.
func (c *Client) doRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	if c == nil {
		return nil, errors.New("admin-client: nil client")
	}
	resp, raw, err := c.execute(ctx, method, path, body, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var pd ProblemDetails
		if err := json.Unmarshal(raw, &pd); err != nil || pd.Status == 0 {
			pd = ProblemDetails{Status: resp.StatusCode, Title: http.StatusText(resp.StatusCode), Detail: string(raw)}
		}
		return nil, &pd
	}
	return raw, nil
}

// execute issues an HTTP request against the client's base URL and
// returns the response with its body fully read. If the request fails at
// the connection level (refused / dial timeout) and a differing
// fallbackURL is configured (re #315: --system-config names a listener
// the stored credentials.toml server_url has drifted from), it retries
// once against the fallback. A successful retry emits one warning naming
// both URLs and the credentials file, rewrites the file with the
// fallback URL, and switches the client to it for subsequent calls. With
// no usable fallback, the returned error names the credentials file (when
// one supplied the base URL) and the --server-url override.
func (c *Client) execute(ctx context.Context, method, path string, body any, wantAccept bool) (*http.Response, []byte, error) {
	c.mu.Lock()
	base := c.base
	fallback := c.fallbackURL
	c.mu.Unlock()

	resp, err := c.sendRequest(ctx, method, base, path, body, wantAccept)
	if err != nil && fallback != "" && fallback != base {
		fbResp, fbErr := c.sendRequest(ctx, method, fallback, path, body, wantAccept)
		if fbErr == nil {
			c.switchToFallback(base, fallback)
			resp, err = fbResp, nil
		} else {
			return nil, nil, fmt.Errorf("admin-client: %s %s: %s unreachable (%v); fallback %s also unreachable: %w",
				method, path, base, err, fallback, fbErr)
		}
	}
	if err != nil {
		return nil, nil, c.connError(method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("admin-client: read body: %w", err)
	}
	return resp, raw, nil
}

// sendRequest builds and issues a single HTTP request against base+path.
// A non-nil error here is a transport-level failure (dial/connection
// refused/timeout, etc.) -- HTTP status codes are reported on the
// returned *http.Response, not as an error.
func (c *Client) sendRequest(ctx context.Context, method, base, path string, body any, wantAccept bool) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("admin-client: marshal: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("admin-client: request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if wantAccept {
		req.Header.Set("Accept", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return c.http.Do(req)
}

// switchToFallback records that base is unreachable and fallback isn't:
// it warns once, best-effort rewrites credentialsPath with fallback, and
// moves the client onto fallback for the rest of its calls.
func (c *Client) switchToFallback(stale, fallback string) {
	c.warnOnce.Do(func() {
		w := c.warnW
		if w == nil {
			w = io.Discard
		}
		if c.credentialsPath != "" {
			fmt.Fprintf(w, "admin: server_url %s in %s is unreachable; using %s derived from --system-config instead and updating the file\n",
				stale, c.credentialsPath, fallback)
			if err := rewriteCredentialsServerURL(c.credentialsPath, fallback); err != nil {
				fmt.Fprintf(w, "admin: warn: could not update %s: %v\n", c.credentialsPath, err)
			}
		} else {
			fmt.Fprintf(w, "admin: %s is unreachable; using %s derived from --system-config instead\n", stale, fallback)
		}
	})
	c.mu.Lock()
	c.base = fallback
	c.fallbackURL = ""
	c.mu.Unlock()
}

// connError wraps a transport-level failure with a hint naming the
// credentials file and the --server-url override, when the failing base
// URL came from a credentials file (re #315).
func (c *Client) connError(method, path string, err error) error {
	if c.credentialsPath != "" {
		return fmt.Errorf("admin-client: %s %s: %w (server_url in %s may be stale; override with --server-url or edit the file)",
			method, path, err, c.credentialsPath)
	}
	return fmt.Errorf("admin-client: %s %s: %w", method, path, err)
}

// credentialsFile is the CLI's on-disk store of the API key. It lives
// under the user's $HOME/.herold/ by default; tests override via
// SetCredentialsPath.
var credentialsPath atomic.Pointer[string]

// SetCredentialsPath overrides the location the admin client uses for
// ~/.herold/credentials.toml. Pass an empty string to revert to the
// default. Test seam; not for production callers.
func SetCredentialsPath(p string) {
	if p == "" {
		credentialsPath.Store(nil)
		return
	}
	credentialsPath.Store(&p)
}

// DefaultCredentialsPath returns the resolved path used by
// loadCredentials / saveCredentials.
func DefaultCredentialsPath() string {
	if ptr := credentialsPath.Load(); ptr != nil && *ptr != "" {
		return *ptr
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".herold", "credentials.toml")
}

type credentialsFile struct {
	APIKey    string `toml:"api_key"`
	ServerURL string `toml:"server_url,omitempty"`
}

func loadCredentials() (string, bool) {
	p := DefaultCredentialsPath()
	if p == "" {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var f credentialsFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return "", false
	}
	return f.APIKey, f.APIKey != ""
}

// saveCredentials writes apiKey (and optionally serverURL) to the default
// credentials path, chmod 0600. Returns the resolved path and the
// server_url that was actually written.
//
// Server-URL precedence: a non-empty incoming serverURL always wins. When
// it differs from the value already in the file a warning is emitted so the
// operator notices that an earlier customisation was overwritten. If the
// incoming serverURL is empty the existing value (if any) is preserved.
// The api_key is always written/overwritten.
//
// Rationale for the inversion: bootstrap is the realistic source of a
// stale credentials.toml — wiping the data dir does not wipe $HOME, so a
// previous install's server_url will silently override the URL derived
// from the new system.toml unless the new one wins. The warning surfaces
// the divergence; operators who customised the URL re-apply after
// bootstrap.
//
// warnW receives operator warnings (non-fatal). Pass cmd.ErrOrStderr() from
// the CLI or any io.Writer in tests.
func saveCredentials(apiKey, serverURL string, warnW io.Writer) (string, string, error) {
	p := DefaultCredentialsPath()
	if p == "" {
		return "", "", errors.New("admin-client: cannot resolve home directory for credentials file")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", "", fmt.Errorf("admin-client: create credentials dir: %w", err)
	}
	// Load any existing file so we can warn on divergence and preserve an
	// existing server_url when the caller has nothing to supply.
	existing := credentialsFile{}
	if raw, err := os.ReadFile(p); err == nil {
		// Ignore parse errors on a corrupt file — we'll just overwrite.
		_ = toml.Unmarshal(raw, &existing)
	}
	effectiveURL := serverURL
	switch {
	case serverURL == "" && existing.ServerURL != "":
		effectiveURL = existing.ServerURL
	case serverURL != "" && existing.ServerURL != "" && existing.ServerURL != serverURL:
		fmt.Fprintf(warnW,
			"saveCredentials: overwriting existing server_url=%s with %s; "+
				"if the previous value was an intentional customisation "+
				"(e.g. a reverse-proxy URL), edit %s after bootstrap.\n",
			existing.ServerURL, serverURL, p,
		)
	}
	if err := writeCredentialsFile(p, credentialsFile{APIKey: apiKey, ServerURL: effectiveURL}); err != nil {
		return "", "", err
	}
	return p, effectiveURL, nil
}

// writeCredentialsFile marshals f as TOML and writes it to p atomically:
// a temp file in the same directory, chmod 0600, then rename. The rename
// ensures the final inode has 0600 permissions even if p already existed
// with looser permissions (O_TRUNC on an existing file does not reset
// mode bits).
func writeCredentialsFile(p string, f credentialsFile) error {
	raw, err := toml.Marshal(f)
	if err != nil {
		return fmt.Errorf("admin-client: marshal credentials: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("admin-client: write credentials tmp: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("admin-client: chmod credentials tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("admin-client: rename credentials: %w", err)
	}
	return nil
}

// rewriteCredentialsServerURL updates only the server_url field of the
// credentials file at p, preserving its api_key and file permissions
// (re #315: called after a successful fallback to a --system-config-
// derived admin URL, so the next invocation needs no retry).
func rewriteCredentialsServerURL(p, newURL string) error {
	raw, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("admin-client: read credentials: %w", err)
	}
	var f credentialsFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("admin-client: parse credentials: %w", err)
	}
	f.ServerURL = newURL
	return writeCredentialsFile(p, f)
}
