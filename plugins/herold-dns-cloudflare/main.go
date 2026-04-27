// Command herold-dns-cloudflare is the first-party Cloudflare DNS plugin.
// It implements the dns.* RPC surface described in
// docs/design/server/requirements/11-plugins.md against the Cloudflare API v4.
//
// Authentication uses an API token resolved from an operator-named
// environment variable at configure time. The plugin holds a single
// pooled *http.Client for the life of the process and retries on 5xx
// responses with exponential backoff. The token never appears on the
// command line.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

const (
	defaultBaseURL            = "https://api.cloudflare.com/client/v4"
	defaultPropagationWaitSec = 30
	defaultRequestTimeoutSec  = 30
	defaultRetryAttempts      = 3
	defaultRetryBaseDelay     = 500 * time.Millisecond
	defaultRetryMaxDelay      = 8 * time.Second
)

// supportedRecordTypes enumerates the record types the Phase 2 caller
// can publish through this plugin. Cloudflare itself supports more, but
// the autodns and ACME callers only ever ask for these.
var supportedRecordTypes = map[string]struct{}{
	"A":     {},
	"AAAA":  {},
	"TXT":   {},
	"MX":    {},
	"CNAME": {},
	"TLSA":  {},
}

var knownOptions = map[string]struct{}{
	"api_token_env":            {},
	"zone_id":                  {},
	"base_url":                 {},
	"propagation_wait_seconds": {},
	"request_timeout_seconds":  {},
	"retry_attempts":           {},
	"default_ttl":              {},
}

type options struct {
	apiTokenEnv     string
	apiToken        string
	zoneID          string
	baseURL         string
	propagationWait time.Duration
	requestTimeout  time.Duration
	retryAttempts   int
	defaultTTL      int
}

type handler struct {
	mu         sync.RWMutex
	opts       options
	httpClient *http.Client
	inflight   sync.WaitGroup
}

func newHandler() *handler {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &handler{
		httpClient: &http.Client{Transport: transport},
	}
}

// OnConfigure validates options, resolves the API token, and (when no
// zone_id is supplied) auto-discovers the zone via /zones.
func (h *handler) OnConfigure(ctx context.Context, opts map[string]any) error {
	for k := range opts {
		if _, ok := knownOptions[k]; !ok {
			return fmt.Errorf("unknown option %q", k)
		}
	}
	cfg := options{
		baseURL:         defaultBaseURL,
		propagationWait: time.Duration(defaultPropagationWaitSec) * time.Second,
		requestTimeout:  time.Duration(defaultRequestTimeoutSec) * time.Second,
		retryAttempts:   defaultRetryAttempts,
		defaultTTL:      300,
	}

	v, ok := opts["api_token_env"]
	if !ok {
		return errors.New("api_token_env is required")
	}
	envName, err := asString(v, "api_token_env")
	if err != nil {
		return err
	}
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return errors.New("api_token_env must be non-empty")
	}
	tok := os.Getenv(envName)
	if tok == "" {
		return fmt.Errorf("api_token_env=%s is set but environment variable is empty", envName)
	}
	cfg.apiTokenEnv = envName
	cfg.apiToken = tok

	if v, ok := opts["zone_id"]; ok {
		s, err := asString(v, "zone_id")
		if err != nil {
			return err
		}
		cfg.zoneID = strings.TrimSpace(s)
	}
	if v, ok := opts["base_url"]; ok {
		s, err := asString(v, "base_url")
		if err != nil {
			return err
		}
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return fmt.Errorf("base_url must be http(s) URL, got %q", s)
		}
		cfg.baseURL = s
	}
	if v, ok := opts["propagation_wait_seconds"]; ok {
		n, err := asInt(v, "propagation_wait_seconds")
		if err != nil {
			return err
		}
		if n < 0 || n > 3600 {
			return fmt.Errorf("propagation_wait_seconds out of range (0..3600): %d", n)
		}
		cfg.propagationWait = time.Duration(n) * time.Second
	}
	if v, ok := opts["request_timeout_seconds"]; ok {
		n, err := asInt(v, "request_timeout_seconds")
		if err != nil {
			return err
		}
		if n <= 0 || n > 600 {
			return fmt.Errorf("request_timeout_seconds out of range (1..600): %d", n)
		}
		cfg.requestTimeout = time.Duration(n) * time.Second
	}
	if v, ok := opts["retry_attempts"]; ok {
		n, err := asInt(v, "retry_attempts")
		if err != nil {
			return err
		}
		if n < 0 || n > 10 {
			return fmt.Errorf("retry_attempts out of range (0..10): %d", n)
		}
		cfg.retryAttempts = n
	}
	if v, ok := opts["default_ttl"]; ok {
		n, err := asInt(v, "default_ttl")
		if err != nil {
			return err
		}
		if n < 1 || n > 86400 {
			return fmt.Errorf("default_ttl out of range (1..86400): %d", n)
		}
		cfg.defaultTTL = n
	}

	h.mu.Lock()
	h.opts = cfg
	h.mu.Unlock()

	sdk.Logf("info", "herold-dns-cloudflare configured base=%s zone_id=%q propagation_wait=%s",
		cfg.baseURL, cfg.zoneID, cfg.propagationWait)
	return nil
}

func (h *handler) OnHealth(ctx context.Context) error {
	h.mu.RLock()
	cfg := h.opts
	h.mu.RUnlock()
	if cfg.apiToken == "" {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := h.newRequest(probeCtx, http.MethodGet, "/user/tokens/verify", nil)
	if err != nil {
		return err
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare health: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("cloudflare health: status %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("cloudflare health: auth failed (status %d)", resp.StatusCode)
	}
	return nil
}

func (h *handler) OnShutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() { h.inflight.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cfRecord mirrors the JSON shape Cloudflare returns for a DNS record.
// We only decode the fields we use.
type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	ZoneID  string `json:"zone_id,omitempty"`
}

// cfEnvelope wraps every Cloudflare API response.
type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfAPIError    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfAPIError) String() string { return fmt.Sprintf("[%d] %s", e.Code, e.Message) }

// DNSPresent creates a new record. Cloudflare's POST endpoint is the
// authoritative create operation; for upsert semantics, see DNSReplace.
func (h *handler) DNSPresent(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	if err := validateRecordType(in.RecordType); err != nil {
		return sdk.DNSPresentResult{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return sdk.DNSPresentResult{}, errors.New("name is required")
	}
	zoneID, err := h.resolveZone(ctx, in.Zone)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	ttl := in.TTL
	if ttl <= 0 {
		h.mu.RLock()
		ttl = h.opts.defaultTTL
		h.mu.RUnlock()
	}
	body := map[string]any{
		"type":    in.RecordType,
		"name":    in.Name,
		"content": in.Value,
		"ttl":     ttl,
	}
	rec, err := h.createRecord(ctx, zoneID, body)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	h.waitForPropagation(ctx)
	return sdk.DNSPresentResult{ID: rec.ID}, nil
}

// DNSReplace upserts a record at (zone, type, name) to the supplied
// value. If exactly one record matches, we PUT it; otherwise we POST.
func (h *handler) DNSReplace(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	if err := validateRecordType(in.RecordType); err != nil {
		return sdk.DNSPresentResult{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return sdk.DNSPresentResult{}, errors.New("name is required")
	}
	zoneID, err := h.resolveZone(ctx, in.Zone)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	ttl := in.TTL
	if ttl <= 0 {
		h.mu.RLock()
		ttl = h.opts.defaultTTL
		h.mu.RUnlock()
	}

	existing, err := h.listRecords(ctx, zoneID, in.RecordType, in.Name)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	body := map[string]any{
		"type":    in.RecordType,
		"name":    in.Name,
		"content": in.Value,
		"ttl":     ttl,
	}
	if len(existing) == 1 {
		rec, err := h.updateRecord(ctx, zoneID, existing[0].ID, body)
		if err != nil {
			return sdk.DNSPresentResult{}, err
		}
		h.waitForPropagation(ctx)
		return sdk.DNSPresentResult{ID: rec.ID}, nil
	}
	// Zero or multiple matches — delete duplicates then create fresh.
	for _, r := range existing {
		if err := h.deleteRecord(ctx, zoneID, r.ID); err != nil {
			return sdk.DNSPresentResult{}, err
		}
	}
	rec, err := h.createRecord(ctx, zoneID, body)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	h.waitForPropagation(ctx)
	return sdk.DNSPresentResult{ID: rec.ID}, nil
}

// DNSCleanup removes a record by id.
func (h *handler) DNSCleanup(ctx context.Context, in sdk.DNSCleanupParams) error {
	h.inflight.Add(1)
	defer h.inflight.Done()

	if strings.TrimSpace(in.ID) == "" {
		return errors.New("id is required")
	}
	zoneID, err := h.resolveZone(ctx, "")
	if err != nil {
		return err
	}
	return h.deleteRecord(ctx, zoneID, in.ID)
}

// DNSList returns records for the supplied zone/type/name filter.
func (h *handler) DNSList(ctx context.Context, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	zoneID, err := h.resolveZone(ctx, in.Zone)
	if err != nil {
		return nil, err
	}
	if in.RecordType != "" {
		if err := validateRecordType(in.RecordType); err != nil {
			return nil, err
		}
	}
	recs, err := h.listRecords(ctx, zoneID, in.RecordType, in.Name)
	if err != nil {
		return nil, err
	}
	out := make([]sdk.DNSRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, sdk.DNSRecord{ID: r.ID, Value: r.Content, TTL: r.TTL})
	}
	return out, nil
}

// resolveZone returns the configured zone_id when set, otherwise it
// asks Cloudflare for the zone matching the supplied DNS zone name.
func (h *handler) resolveZone(ctx context.Context, zoneName string) (string, error) {
	h.mu.RLock()
	zid := h.opts.zoneID
	h.mu.RUnlock()
	if zid != "" {
		return zid, nil
	}
	if strings.TrimSpace(zoneName) == "" {
		return "", errors.New("zone_id not configured and no zone supplied in request")
	}
	q := url.Values{}
	q.Set("name", strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
	req, err := h.newRequest(ctx, http.MethodGet, "/zones?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	body, err := h.do(req)
	if err != nil {
		return "", err
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &zones); err != nil {
		return "", fmt.Errorf("decode zones: %w", err)
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("no Cloudflare zone found for %q", zoneName)
	}
	return zones[0].ID, nil
}

func (h *handler) listRecords(ctx context.Context, zoneID, recordType, name string) ([]cfRecord, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	if recordType != "" {
		q.Set("type", recordType)
	}
	if name != "" {
		q.Set("name", name)
	}
	path := "/zones/" + zoneID + "/dns_records?" + q.Encode()
	req, err := h.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	body, err := h.do(req)
	if err != nil {
		return nil, err
	}
	var recs []cfRecord
	if err := json.Unmarshal(body, &recs); err != nil {
		return nil, fmt.Errorf("decode records: %w", err)
	}
	return recs, nil
}

func (h *handler) createRecord(ctx context.Context, zoneID string, body map[string]any) (cfRecord, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return cfRecord{}, fmt.Errorf("marshal record: %w", err)
	}
	req, err := h.newRequest(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", bytes.NewReader(raw))
	if err != nil {
		return cfRecord{}, err
	}
	respBody, err := h.do(req)
	if err != nil {
		return cfRecord{}, err
	}
	var rec cfRecord
	if err := json.Unmarshal(respBody, &rec); err != nil {
		return cfRecord{}, fmt.Errorf("decode created record: %w", err)
	}
	return rec, nil
}

func (h *handler) updateRecord(ctx context.Context, zoneID, recID string, body map[string]any) (cfRecord, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return cfRecord{}, fmt.Errorf("marshal record: %w", err)
	}
	req, err := h.newRequest(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+recID, bytes.NewReader(raw))
	if err != nil {
		return cfRecord{}, err
	}
	respBody, err := h.do(req)
	if err != nil {
		return cfRecord{}, err
	}
	var rec cfRecord
	if err := json.Unmarshal(respBody, &rec); err != nil {
		return cfRecord{}, fmt.Errorf("decode updated record: %w", err)
	}
	return rec, nil
}

func (h *handler) deleteRecord(ctx context.Context, zoneID, recID string) error {
	req, err := h.newRequest(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recID, nil)
	if err != nil {
		return err
	}
	_, err = h.do(req)
	return err
}

func (h *handler) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	h.mu.RLock()
	cfg := h.opts
	h.mu.RUnlock()
	if cfg.apiToken == "" {
		return nil, errors.New("plugin not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do issues req with bounded retries on 5xx responses. The return value
// is the decoded `result` field of the Cloudflare envelope.
func (h *handler) do(req *http.Request) ([]byte, error) {
	h.mu.RLock()
	attempts := h.opts.retryAttempts
	timeout := h.opts.requestTimeout
	h.mu.RUnlock()

	var lastErr error
	for attempt := 0; attempt <= attempts; attempt++ {
		// Each attempt gets its own ctx-bounded deadline.
		ctx, cancel := context.WithTimeout(req.Context(), timeout)
		callReq := req.Clone(ctx)
		// Replay request body when present. http.NewRequestWithContext
		// populates GetBody automatically for *bytes.Reader payloads.
		if req.GetBody != nil {
			if rb, err := req.GetBody(); err == nil {
				callReq.Body = rb
			}
		}
		resp, err := h.httpClient.Do(callReq)
		if err != nil {
			cancel()
			lastErr = err
			if !shouldRetry(err) || attempt == attempts {
				return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
			}
			sleepBackoff(req.Context(), attempt)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = readErr
			if attempt == attempts {
				return nil, fmt.Errorf("read response: %w", readErr)
			}
			sleepBackoff(req.Context(), attempt)
			continue
		}
		// Honor Cloudflare's Retry-After on 429 / 5xx.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, truncate(string(body), 200))
			if attempt == attempts {
				return nil, lastErr
			}
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if d, ok := parseRetryAfter(ra); ok {
					if !sleepFor(req.Context(), d) {
						return nil, req.Context().Err()
					}
					continue
				}
			}
			sleepBackoff(req.Context(), attempt)
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%s %s: auth error (status %d): %s", req.Method, req.URL.Path, resp.StatusCode, truncate(string(body), 200))
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode, truncate(string(body), 200))
		}
		var env cfEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("decode envelope: %w", err)
		}
		if !env.Success {
			msg := "cloudflare API error"
			if len(env.Errors) > 0 {
				msg = env.Errors[0].String()
			}
			return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, msg)
		}
		return env.Result, nil
	}
	return nil, lastErr
}

func (h *handler) waitForPropagation(ctx context.Context) {
	h.mu.RLock()
	d := h.opts.propagationWait
	h.mu.RUnlock()
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func validateRecordType(rt string) error {
	if _, ok := supportedRecordTypes[strings.ToUpper(rt)]; !ok {
		return fmt.Errorf("unsupported record type %q", rt)
	}
	return nil
}

func shouldRetry(err error) bool {
	// Any transport error (connection refused, EOF mid-response, etc.) is
	// safely retryable for idempotent verbs and for our POST/PUT/DELETE
	// payloads where Cloudflare deduplicates on (zone,type,name,content).
	return err != nil
}

func sleepBackoff(ctx context.Context, attempt int) bool {
	d := defaultRetryBaseDelay << attempt
	if d > defaultRetryMaxDelay {
		d = defaultRetryMaxDelay
	}
	return sleepFor(ctx, d)
}

func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func parseRetryAfter(s string) (time.Duration, bool) {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(s); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func asString(v any, name string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", name, v)
	}
	return s, nil
}

func asInt(v any, name string) (int, error) {
	switch t := v.(type) {
	case float64:
		if t != float64(int(t)) {
			return 0, fmt.Errorf("%s must be an integer, got %v", name, t)
		}
		return int(t), nil
	case int:
		return t, nil
	case int64:
		return int(t), nil
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", name, v)
	}
}

func main() {
	manifest := sdk.Manifest{
		Name:                  "herold-dns-cloudflare",
		Version:               "0.1.0",
		Type:                  plug.TypeDNS,
		Lifecycle:             plug.LifecycleLongRunning,
		MaxConcurrentRequests: 8,
		ABIVersion:            plug.ABIVersion,
		ShutdownGraceSec:      10,
		HealthIntervalSec:     60,
		Capabilities:          []string{sdk.MethodDNSPresent, sdk.MethodDNSCleanup, sdk.MethodDNSList, sdk.MethodDNSReplace},
		OptionsSchema: map[string]plug.OptionSchema{
			"api_token_env":            {Type: "string", Required: true, Secret: true},
			"zone_id":                  {Type: "string"},
			"base_url":                 {Type: "string", Default: defaultBaseURL},
			"propagation_wait_seconds": {Type: "integer", Default: defaultPropagationWaitSec},
			"request_timeout_seconds":  {Type: "integer", Default: defaultRequestTimeoutSec},
			"retry_attempts":           {Type: "integer", Default: defaultRetryAttempts},
			"default_ttl":              {Type: "integer", Default: 300},
		},
	}
	if err := sdk.Run(manifest, newHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "herold-dns-cloudflare: %v\n", err)
		os.Exit(1)
	}
}
