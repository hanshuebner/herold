// Command herold-dns-manual is the first-party manual DNS plugin. It is
// the "I will publish records by hand" provider: dns.present writes the
// requested record to a JSON file (and an info log) and blocks waiting
// for the operator to acknowledge by deleting the file. dns.cleanup
// follows the same handshake. On timeout the plugin returns an error so
// the autodns / ACME caller can fall back gracefully.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

const (
	defaultOutputPath          = "/var/lib/herold/dns-pending.json"
	defaultConfirmTimeoutSec   = 600
	defaultPollIntervalSeconds = 5
)

var supportedRecordTypes = map[string]struct{}{
	"A":     {},
	"AAAA":  {},
	"TXT":   {},
	"MX":    {},
	"CNAME": {},
	"TLSA":  {},
}

var knownOptions = map[string]struct{}{
	"output_path":             {},
	"confirm_timeout_seconds": {},
	"poll_interval_seconds":   {},
}

// pendingRecord is the JSON shape written to output_path. Operators
// inspect this file, publish (or delete) the record at their provider,
// then `rm` the file to acknowledge.
type pendingRecord struct {
	Operation  string    `json:"operation"`
	ID         string    `json:"id"`
	Zone       string    `json:"zone"`
	RecordType string    `json:"record_type"`
	Name       string    `json:"name"`
	Value      string    `json:"value"`
	TTL        int       `json:"ttl"`
	IssuedAt   time.Time `json:"issued_at"`
}

type options struct {
	outputPath     string
	confirmTimeout time.Duration
	pollInterval   time.Duration
}

// records is an in-memory map id -> pendingRecord so DNSCleanup can
// resolve the original record details after an operator-issued id was
// returned by DNSPresent.
type handler struct {
	mu       sync.RWMutex
	opts     options
	records  map[string]pendingRecord
	inflight sync.WaitGroup
	// fileMu serialises writes to output_path. Concurrent dns.present /
	// dns.cleanup callers wait so the operator only ever sees one
	// pending record on disk at a time.
	fileMu sync.Mutex
	// nowFn returns the current time. Test seam.
	nowFn func() time.Time
}

func newHandler() *handler {
	return &handler{
		records: make(map[string]pendingRecord),
		nowFn:   time.Now,
	}
}

func (h *handler) OnConfigure(ctx context.Context, opts map[string]any) error {
	for k := range opts {
		if _, ok := knownOptions[k]; !ok {
			return fmt.Errorf("unknown option %q", k)
		}
	}
	cfg := options{
		outputPath:     defaultOutputPath,
		confirmTimeout: time.Duration(defaultConfirmTimeoutSec) * time.Second,
		pollInterval:   time.Duration(defaultPollIntervalSeconds) * time.Second,
	}
	if v, ok := opts["output_path"]; ok {
		s, err := asString(v, "output_path")
		if err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return errors.New("output_path must be non-empty")
		}
		cfg.outputPath = s
	}
	if v, ok := opts["confirm_timeout_seconds"]; ok {
		n, err := asInt(v, "confirm_timeout_seconds")
		if err != nil {
			return err
		}
		if n <= 0 || n > 86400 {
			return fmt.Errorf("confirm_timeout_seconds out of range (1..86400): %d", n)
		}
		cfg.confirmTimeout = time.Duration(n) * time.Second
	}
	if v, ok := opts["poll_interval_seconds"]; ok {
		// Accept fractional seconds as floats so tests can drive a
		// sub-second polling cadence without exposing a separate
		// poll_interval_ms knob.
		switch t := v.(type) {
		case float64:
			if t <= 0 {
				return fmt.Errorf("poll_interval_seconds must be positive, got %v", t)
			}
			cfg.pollInterval = time.Duration(t * float64(time.Second))
		case int:
			if t <= 0 {
				return fmt.Errorf("poll_interval_seconds must be positive, got %d", t)
			}
			cfg.pollInterval = time.Duration(t) * time.Second
		default:
			return fmt.Errorf("poll_interval_seconds must be a number, got %T", v)
		}
		if cfg.pollInterval < 10*time.Millisecond {
			cfg.pollInterval = 10 * time.Millisecond
		}
	}

	// Make sure the parent directory exists so the first present call
	// does not race with operator setup.
	if dir := filepath.Dir(cfg.outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			// Non-fatal: emit a warning. Production deployments may have
			// this directory pre-created with stricter ownership.
			sdk.Logf("warn", "herold-dns-manual: cannot create %s: %v", dir, err)
		}
	}

	h.mu.Lock()
	h.opts = cfg
	h.mu.Unlock()
	sdk.Logf("info", "herold-dns-manual configured output_path=%s confirm_timeout=%s",
		cfg.outputPath, cfg.confirmTimeout)
	return nil
}

func (h *handler) OnHealth(ctx context.Context) error { return nil }

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

func (h *handler) DNSPresent(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	return h.publish(ctx, "present", in)
}

// DNSReplace shares semantics with DNSPresent for the manual provider:
// the operator publishes the record themselves, so the plugin merely
// emits the request and waits for acknowledgement.
func (h *handler) DNSReplace(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	return h.publish(ctx, "replace", in)
}

func (h *handler) publish(ctx context.Context, op string, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	if err := validateRecordType(in.RecordType); err != nil {
		return sdk.DNSPresentResult{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return sdk.DNSPresentResult{}, errors.New("name is required")
	}
	id := newRecordID()
	rec := pendingRecord{
		Operation:  op,
		ID:         id,
		Zone:       in.Zone,
		RecordType: in.RecordType,
		Name:       in.Name,
		Value:      in.Value,
		TTL:        in.TTL,
		IssuedAt:   h.nowFn(),
	}

	if err := h.handshake(ctx, rec); err != nil {
		return sdk.DNSPresentResult{}, err
	}
	h.mu.Lock()
	h.records[id] = rec
	h.mu.Unlock()
	return sdk.DNSPresentResult{ID: id}, nil
}

func (h *handler) DNSCleanup(ctx context.Context, in sdk.DNSCleanupParams) error {
	h.inflight.Add(1)
	defer h.inflight.Done()
	if strings.TrimSpace(in.ID) == "" {
		return errors.New("id is required")
	}
	h.mu.Lock()
	rec, ok := h.records[in.ID]
	delete(h.records, in.ID)
	h.mu.Unlock()
	if !ok {
		// Operator-confirm semantics: we still emit a cleanup request so
		// the operator can remove an externally-managed record by id.
		rec = pendingRecord{ID: in.ID, IssuedAt: h.nowFn()}
	}
	rec.Operation = "cleanup"
	rec.IssuedAt = h.nowFn()
	return h.handshake(ctx, rec)
}

// DNSList returns the records this plugin currently believes are
// published. It does not query any external system; the manual plugin
// has no view of the live zone beyond what the operator told it.
func (h *handler) DNSList(ctx context.Context, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]sdk.DNSRecord, 0, len(h.records))
	for id, r := range h.records {
		if in.RecordType != "" && !strings.EqualFold(r.RecordType, in.RecordType) {
			continue
		}
		if in.Name != "" && r.Name != in.Name {
			continue
		}
		if in.Zone != "" && r.Zone != "" && r.Zone != in.Zone {
			continue
		}
		out = append(out, sdk.DNSRecord{ID: id, Value: r.Value, TTL: r.TTL})
	}
	return out, nil
}

// handshake writes the pending record JSON to the configured output
// path, logs it for the operator, then polls until the file is
// removed. Returns an error on context cancellation or after the
// configured confirm timeout elapses.
func (h *handler) handshake(ctx context.Context, rec pendingRecord) error {
	h.mu.RLock()
	cfg := h.opts
	h.mu.RUnlock()
	if cfg.outputPath == "" {
		return errors.New("plugin not configured")
	}

	h.fileMu.Lock()
	defer h.fileMu.Unlock()

	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pending record: %w", err)
	}
	if err := writeFileAtomic(cfg.outputPath, append(raw, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", cfg.outputPath, err)
	}
	sdk.Logf("info",
		"herold-dns-manual %s zone=%s type=%s name=%s value=%q ttl=%d id=%s — please publish then `rm %s` to acknowledge",
		rec.Operation, rec.Zone, rec.RecordType, rec.Name, rec.Value, rec.TTL, rec.ID, cfg.outputPath)

	deadline := h.nowFn().Add(cfg.confirmTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()
	for {
		// Check immediately so the test path with a quick rm wins.
		if _, err := os.Stat(cfg.outputPath); errors.Is(err, os.ErrNotExist) {
			sdk.Logf("info", "herold-dns-manual %s acknowledged id=%s", rec.Operation, rec.ID)
			return nil
		}
		if h.nowFn().After(deadline) {
			// Best-effort cleanup so a stale file does not stick around.
			_ = os.Remove(cfg.outputPath)
			return fmt.Errorf("manual DNS confirm timeout after %s for record id=%s", cfg.confirmTimeout, rec.ID)
		}
		select {
		case <-ctx.Done():
			_ = os.Remove(cfg.outputPath)
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dns-pending-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		// Non-fatal: continue.
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func validateRecordType(rt string) error {
	if _, ok := supportedRecordTypes[strings.ToUpper(rt)]; !ok {
		return fmt.Errorf("unsupported record type %q", rt)
	}
	return nil
}

// newRecordID returns a short opaque id. We use the wall clock plus a
// small random suffix; uniqueness only has to hold inside one plugin
// process lifetime.
var idCounter struct {
	mu sync.Mutex
	n  uint64
}

func newRecordID() string {
	idCounter.mu.Lock()
	idCounter.n++
	n := idCounter.n
	idCounter.mu.Unlock()
	return fmt.Sprintf("manual-%d-%d", time.Now().UnixNano(), n)
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
		Name:                  "herold-dns-manual",
		Version:               "0.1.0",
		Type:                  plug.TypeDNS,
		Lifecycle:             plug.LifecycleLongRunning,
		MaxConcurrentRequests: 1, // operator can only handle one record at a time
		ABIVersion:            plug.ABIVersion,
		ShutdownGraceSec:      5,
		HealthIntervalSec:     60,
		Capabilities:          []string{sdk.MethodDNSPresent, sdk.MethodDNSCleanup, sdk.MethodDNSList, sdk.MethodDNSReplace},
		OptionsSchema: map[string]plug.OptionSchema{
			"output_path":             {Type: "string", Default: defaultOutputPath},
			"confirm_timeout_seconds": {Type: "integer", Default: defaultConfirmTimeoutSec},
			"poll_interval_seconds":   {Type: "number", Default: defaultPollIntervalSeconds},
		},
	}
	if err := sdk.Run(manifest, newHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "herold-dns-manual: %v\n", err)
		os.Exit(1)
	}
}
