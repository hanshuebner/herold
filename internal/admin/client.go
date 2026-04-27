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
	"sync/atomic"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Client is the CLI-side admin REST client. It carries the API key (from
// env or credentials file), a base URL, and a bounded timeout. Every
// public method is a thin wrapper around an HTTP call returning a typed
// error (RFC 7807 problem+json).
type Client struct {
	base    string
	apiKey  string
	timeout time.Duration
	http    *http.Client
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
	return &Client{
		base:    strings.TrimRight(opts.BaseURL, "/"),
		apiKey:  key,
		timeout: timeout,
		http:    hc,
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
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("admin-client: marshal: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return fmt.Errorf("admin-client: request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("admin-client: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("admin-client: read body: %w", err)
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
	raw, err := toml.Marshal(credentialsFile{APIKey: apiKey, ServerURL: effectiveURL})
	if err != nil {
		return "", "", fmt.Errorf("admin-client: marshal credentials: %w", err)
	}
	// Write atomically: temp file in the same directory, then rename.
	// This ensures the final inode has 0600 permissions even if the
	// file already existed with looser permissions (O_TRUNC on an
	// existing file does not reset mode bits).
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", "", fmt.Errorf("admin-client: write credentials tmp: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", "", fmt.Errorf("admin-client: chmod credentials tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return "", "", fmt.Errorf("admin-client: rename credentials: %w", err)
	}
	return p, effectiveURL, nil
}
