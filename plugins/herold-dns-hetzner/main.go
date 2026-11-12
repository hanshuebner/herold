// Command herold-dns-hetzner is the first-party Hetzner DNS plugin.
// It implements the dns.* RPC surface against Hetzner Cloud DNS
// (https://docs.hetzner.cloud/reference/cloud#zones) through
// hcloud-go. Authentication is a Hetzner Cloud API token sent as a
// Bearer token. The operator keeps the token in an environment
// variable and names it in system.toml as api_token_env = "$VAR".
//
// Hetzner Cloud DNS manages records as RRSets identified by
// (name, type). dns.present adds a single value to the RRSet and
// dns.cleanup removes that value again, so several concurrent ACME
// challenges on one name each keep their own TXT value.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

const (
	pluginName                = "herold-dns-hetzner"
	pluginVersion             = "0.2.0"
	defaultBaseURL            = "https://api.hetzner.cloud/v1"
	defaultPropagationWaitSec = 60
	defaultRequestTimeoutSec  = 30
	defaultRetryAttempts      = 3
	defaultTTL                = 300
	// minTTL is the smallest RRSet TTL Hetzner Cloud DNS accepts.
	minTTL = 60
	// maxTXTChunk is the RFC 1035 character-string limit; longer TXT
	// values are split into several quoted strings.
	maxTXTChunk = 255
)

var supportedRecordTypes = map[string]hcloud.ZoneRRSetType{
	"A":     hcloud.ZoneRRSetTypeA,
	"AAAA":  hcloud.ZoneRRSetTypeAAAA,
	"TXT":   hcloud.ZoneRRSetTypeTXT,
	"MX":    hcloud.ZoneRRSetTypeMX,
	"CNAME": hcloud.ZoneRRSetTypeCNAME,
	"TLSA":  hcloud.ZoneRRSetTypeTLSA,
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
	zoneID          string
	baseURL         string
	propagationWait time.Duration
	requestTimeout  time.Duration
	retryAttempts   int
	defaultTTL      int
}

type handler struct {
	mu       sync.RWMutex
	opts     options
	client   *hcloud.Client
	zones    map[string]*hcloud.Zone // zone name or configured zone_id -> zone
	inflight sync.WaitGroup
}

func newHandler() *handler {
	return &handler{zones: map[string]*hcloud.Zone{}}
}

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
		defaultTTL:      defaultTTL,
	}
	v, ok := opts["api_token_env"]
	if !ok {
		return errors.New("api_token_env is required")
	}
	ref, err := asString(v, "api_token_env")
	if err != nil {
		return err
	}
	tok, err := resolveToken(ref)
	if err != nil {
		return err
	}

	if v, ok := opts["zone_id"]; ok {
		s, err := asString(v, "zone_id")
		if err != nil {
			return err
		}
		cfg.zoneID = strings.TrimSuffix(strings.TrimSpace(s), ".")
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
		if n < minTTL || n > 86400 {
			return fmt.Errorf("default_ttl out of range (%d..86400): %d", minTTL, n)
		}
		cfg.defaultTTL = n
	}

	client := newClient(cfg, tok)

	h.mu.Lock()
	h.opts = cfg
	h.client = client
	h.zones = map[string]*hcloud.Zone{}
	h.mu.Unlock()
	sdk.Logf("info", "%s configured base=%s zone_id=%q propagation_wait=%s",
		pluginName, cfg.baseURL, cfg.zoneID, cfg.propagationWait)
	return nil
}

// envVarNameRE matches an environment variable identifier.
var envVarNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// resolveToken yields the API token from the api_token_env option.
// herold resolves the "$VAR" / "file:/path" reference written in
// system.toml before configuring the plugin, so the value normally is
// the token itself. A value naming a non-empty environment variable is
// read from that variable, which serves direct invocation with a bare
// variable name. The value is never echoed, since it may be the secret.
func resolveToken(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("api_token_env must be non-empty")
	}
	if envVarNameRE.MatchString(ref) {
		if tok := os.Getenv(ref); tok != "" {
			return tok, nil
		}
	}
	return ref, nil
}

func newClient(cfg options, token string) *hcloud.Client {
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
	return hcloud.NewClient(
		hcloud.WithEndpoint(cfg.baseURL),
		hcloud.WithToken(token),
		hcloud.WithApplication(pluginName, pluginVersion),
		hcloud.WithHTTPClient(&http.Client{Transport: transport, Timeout: cfg.requestTimeout}),
		hcloud.WithRetryOpts(hcloud.RetryOpts{
			BackoffFunc: hcloud.ExponentialBackoffWithOpts(hcloud.ExponentialBackoffOpts{
				Base:       500 * time.Millisecond,
				Multiplier: 2,
				Cap:        8 * time.Second,
				Jitter:     true,
			}),
			MaxRetries: cfg.retryAttempts,
		}),
		hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(500 * time.Millisecond)}),
	)
}

func (h *handler) OnHealth(ctx context.Context) error {
	h.mu.RLock()
	cli := h.client
	h.mu.RUnlock()
	if cli == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := cli.Zone.List(probeCtx, hcloud.ZoneListOpts{ListOpts: hcloud.ListOpts{PerPage: 1}})
	if err != nil {
		return fmt.Errorf("hetzner health: %w", apiError(err))
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

// target is a resolved record location: the Hetzner zone and the
// zone-relative RRSet name.
type target struct {
	zone   *hcloud.Zone
	rrName string
	rrType hcloud.ZoneRRSetType
}

func (t target) rrset() *hcloud.ZoneRRSet {
	return &hcloud.ZoneRRSet{Zone: t.zone, Name: t.rrName, Type: t.rrType}
}

func (h *handler) resolveTarget(ctx context.Context, zone, name, recordType string) (target, error) {
	rrType, err := mapRecordType(recordType)
	if err != nil {
		return target{}, err
	}
	if strings.TrimSpace(name) == "" {
		return target{}, errors.New("name is required")
	}
	fqdn := composeRecordName(name, zone)
	z, err := h.resolveZone(ctx, zone, fqdn)
	if err != nil {
		return target{}, err
	}
	rel, err := relativeName(fqdn, z.Name)
	if err != nil {
		return target{}, err
	}
	return target{zone: z, rrName: rel, rrType: rrType}, nil
}

// DNSPresent adds in.Value to the RRSet (name, type), creating the
// RRSet when it does not exist yet. Values already present are left
// untouched.
func (h *handler) DNSPresent(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	t, err := h.resolveTarget(ctx, in.Zone, in.Name, in.RecordType)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	value := formatRecordValue(t.rrType, in.Value)
	existing, err := h.getRRSet(ctx, t)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	// add_records requires a TTL equal to the RRSet's own when one is
	// sent, so an existing RRSet keeps its TTL.
	ttl := h.ttl(in.TTL)
	if existing != nil {
		ttl = existing.TTL
		if slices.ContainsFunc(existing.Records, func(r hcloud.ZoneRRSetRecord) bool { return r.Value == value }) {
			return sdk.DNSPresentResult{ID: encodeID(t, value)}, nil
		}
	}
	err = h.runAction(ctx, "add_records", func(cli *hcloud.Client) (*hcloud.Action, error) {
		a, _, err := cli.Zone.AddRRSetRecords(ctx, t.rrset(), hcloud.ZoneRRSetAddRecordsOpts{
			Records: []hcloud.ZoneRRSetRecord{{Value: value}},
			TTL:     ttl,
		})
		return a, err
	})
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	h.waitForPropagation(ctx)
	return sdk.DNSPresentResult{ID: encodeID(t, value)}, nil
}

// DNSReplace makes in.Value the only value of the RRSet (name, type).
func (h *handler) DNSReplace(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	t, err := h.resolveTarget(ctx, in.Zone, in.Name, in.RecordType)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	value := formatRecordValue(t.rrType, in.Value)
	ttl := h.ttl(in.TTL)
	existing, err := h.getRRSet(ctx, t)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	records := []hcloud.ZoneRRSetRecord{{Value: value}}
	if existing == nil {
		err = h.runAction(ctx, "create_rrset", func(cli *hcloud.Client) (*hcloud.Action, error) {
			res, _, err := cli.Zone.CreateRRSet(ctx, t.zone, hcloud.ZoneRRSetCreateOpts{
				Name: t.rrName, Type: t.rrType, TTL: ttl, Records: records,
			})
			return res.Action, err
		})
	} else {
		err = h.runAction(ctx, "set_records", func(cli *hcloud.Client) (*hcloud.Action, error) {
			a, _, err := cli.Zone.SetRRSetRecords(ctx, t.rrset(), hcloud.ZoneRRSetSetRecordsOpts{Records: records})
			return a, err
		})
		if err == nil && (existing.TTL == nil || *existing.TTL != *ttl) {
			err = h.runAction(ctx, "change_ttl", func(cli *hcloud.Client) (*hcloud.Action, error) {
				a, _, err := cli.Zone.ChangeRRSetTTL(ctx, t.rrset(), hcloud.ZoneRRSetChangeTTLOpts{TTL: ttl})
				return a, err
			})
		}
	}
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	h.waitForPropagation(ctx)
	return sdk.DNSPresentResult{ID: encodeID(t, value)}, nil
}

// DNSCleanup removes the single value named by the id from its RRSet.
// Hetzner deletes the RRSet once its last value is gone. A missing
// RRSet counts as already cleaned up.
func (h *handler) DNSCleanup(ctx context.Context, in sdk.DNSCleanupParams) error {
	h.inflight.Add(1)
	defer h.inflight.Done()
	if strings.TrimSpace(in.ID) == "" {
		return errors.New("id is required")
	}
	t, value, err := decodeID(in.ID)
	if err != nil {
		return err
	}
	err = h.runAction(ctx, "remove_records", func(cli *hcloud.Client) (*hcloud.Action, error) {
		a, _, err := cli.Zone.RemoveRRSetRecords(ctx, t.rrset(), hcloud.ZoneRRSetRemoveRecordsOpts{
			Records: []hcloud.ZoneRRSetRecord{{Value: value}},
		})
		return a, err
	})
	if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
		return nil
	}
	return err
}

func (h *handler) DNSList(ctx context.Context, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	opts := hcloud.ZoneRRSetListOpts{ListOpts: hcloud.ListOpts{PerPage: 100}}
	if in.RecordType != "" {
		rt, err := mapRecordType(in.RecordType)
		if err != nil {
			return nil, err
		}
		opts.Type = []hcloud.ZoneRRSetType{rt}
	}
	fqdn := strings.TrimSuffix(strings.TrimSpace(in.Zone), ".")
	if in.Name != "" {
		fqdn = composeRecordName(in.Name, in.Zone)
	}
	z, err := h.resolveZone(ctx, in.Zone, fqdn)
	if err != nil {
		return nil, err
	}
	if in.Name != "" {
		rel, err := relativeName(fqdn, z.Name)
		if err != nil {
			return nil, err
		}
		opts.Name = rel
	}
	cli, err := h.getClient()
	if err != nil {
		return nil, err
	}
	sets, err := cli.Zone.AllRRSetsWithOpts(ctx, z, opts)
	if err != nil {
		return nil, fmt.Errorf("list rrsets: %w", apiError(err))
	}
	out := []sdk.DNSRecord{}
	for _, rs := range sets {
		if _, ok := supportedRecordTypes[string(rs.Type)]; !ok {
			continue
		}
		ttl := z.TTL
		if rs.TTL != nil {
			ttl = *rs.TTL
		}
		t := target{zone: z, rrName: rs.Name, rrType: rs.Type}
		for _, r := range rs.Records {
			out = append(out, sdk.DNSRecord{
				ID:    encodeID(t, r.Value),
				Value: parseRecordValue(rs.Type, r.Value),
				TTL:   ttl,
			})
		}
	}
	return out, nil
}

func (h *handler) getClient() (*hcloud.Client, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.client == nil {
		return nil, errors.New("plugin not configured")
	}
	return h.client, nil
}

func (h *handler) ttl(requested int) *int {
	ttl := requested
	if ttl <= 0 {
		h.mu.RLock()
		ttl = h.opts.defaultTTL
		h.mu.RUnlock()
	}
	ttl = max(ttl, minTTL)
	return &ttl
}

func (h *handler) getRRSet(ctx context.Context, t target) (*hcloud.ZoneRRSet, error) {
	cli, err := h.getClient()
	if err != nil {
		return nil, err
	}
	rs, _, err := cli.Zone.GetRRSetByNameAndType(ctx, t.zone, t.rrName, t.rrType)
	if err != nil {
		return nil, fmt.Errorf("get rrset %s/%s: %w", t.rrName, t.rrType, apiError(err))
	}
	return rs, nil
}

// runAction issues an RRSet action and waits until Hetzner reports it
// finished, so the change is live on the authoritative nameservers
// when the RPC returns.
func (h *handler) runAction(ctx context.Context, what string, call func(*hcloud.Client) (*hcloud.Action, error)) error {
	cli, err := h.getClient()
	if err != nil {
		return err
	}
	h.mu.RLock()
	timeout := h.opts.requestTimeout
	h.mu.RUnlock()
	action, err := call(cli)
	if err != nil {
		return fmt.Errorf("%s: %w", what, apiError(err))
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := cli.Action.WaitFor(waitCtx, action); err != nil {
		return fmt.Errorf("%s: wait for action: %w", what, apiError(err))
	}
	return nil
}

// resolveZone returns the Hetzner zone holding fqdn. A configured
// zone_id (numeric id or zone name) wins; otherwise the request zone
// and its parent domains are looked up by name, most specific first,
// because Hetzner zones are registrable domains while callers may pass
// a host name such as the certificate's domain.
func (h *handler) resolveZone(ctx context.Context, zoneName, fqdn string) (*hcloud.Zone, error) {
	cli, err := h.getClient()
	if err != nil {
		return nil, err
	}
	h.mu.RLock()
	zid := h.opts.zoneID
	h.mu.RUnlock()
	if zid != "" {
		if z := h.cachedZone(zid); z != nil {
			return z, nil
		}
		z, _, err := cli.Zone.Get(ctx, zid)
		if err != nil {
			return nil, fmt.Errorf("get zone %q: %w", zid, apiError(err))
		}
		if z == nil {
			return nil, fmt.Errorf("no Hetzner zone found for zone_id %q", zid)
		}
		h.cacheZone(zid, z)
		return z, nil
	}
	start := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
	if start == "" {
		start = strings.ToLower(fqdn)
	}
	if start == "" {
		return nil, errors.New("zone_id not configured and no zone supplied in request")
	}
	for candidate := start; strings.Contains(candidate, "."); candidate = candidate[strings.Index(candidate, ".")+1:] {
		if z := h.cachedZone(candidate); z != nil {
			return z, nil
		}
		zones, _, err := cli.Zone.List(ctx, hcloud.ZoneListOpts{Name: candidate})
		if err != nil {
			return nil, fmt.Errorf("list zones name=%s: %w", candidate, apiError(err))
		}
		for _, z := range zones {
			if strings.EqualFold(z.Name, candidate) {
				h.cacheZone(candidate, z)
				return z, nil
			}
		}
	}
	return nil, fmt.Errorf("no Hetzner zone found for %q", start)
}

func (h *handler) cachedZone(key string) *hcloud.Zone {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.zones[strings.ToLower(key)]
}

func (h *handler) cacheZone(key string, z *hcloud.Zone) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.zones[strings.ToLower(key)] = z
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

// apiError turns authentication failures into an explicit auth error
// and passes every other error through.
func apiError(err error) error {
	if hcloud.IsError(err, hcloud.ErrorCodeUnauthorized) || hcloud.IsError(err, hcloud.ErrorCodeForbidden) {
		return fmt.Errorf("auth error: %w", err)
	}
	return err
}

// encodeID packs the coordinates dns.cleanup needs into the opaque id:
// zone id, RRSet name, type, and the record value as stored by Hetzner.
func encodeID(t target, value string) string {
	return fmt.Sprintf("%d|%s|%s|%s", t.zone.ID, t.rrName, t.rrType, value)
}

func decodeID(id string) (target, string, error) {
	parts := strings.SplitN(id, "|", 4)
	if len(parts) != 4 {
		return target{}, "", fmt.Errorf("invalid record id %q", id)
	}
	zoneID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || zoneID <= 0 {
		return target{}, "", fmt.Errorf("invalid record id %q", id)
	}
	rrType, err := mapRecordType(parts[2])
	if err != nil {
		return target{}, "", err
	}
	if parts[1] == "" || parts[3] == "" {
		return target{}, "", fmt.Errorf("invalid record id %q", id)
	}
	return target{zone: &hcloud.Zone{ID: zoneID}, rrName: parts[1], rrType: rrType}, parts[3], nil
}

// composeRecordName turns the caller's (name, zone) pair into a
// fully-qualified, lower-case name without trailing dot. name may be
// relative to zone ("_acme-challenge"), already qualified, or "@".
func composeRecordName(name, zone string) string {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
	if zone == "" {
		return name
	}
	if name == "" || name == "@" {
		return zone
	}
	if name == zone || strings.HasSuffix(name, "."+zone) {
		return name
	}
	return name + "." + zone
}

// relativeName converts fqdn to the RRSet name Hetzner expects inside
// zone: "@" for the apex, otherwise the labels left of the zone name.
func relativeName(fqdn, zone string) (string, error) {
	zone = strings.ToLower(zone)
	switch {
	case fqdn == zone:
		return "@", nil
	case strings.HasSuffix(fqdn, "."+zone):
		return strings.TrimSuffix(fqdn, "."+zone), nil
	default:
		return "", fmt.Errorf("record %q is not inside zone %q", fqdn, zone)
	}
}

// formatRecordValue renders a value in the presentation format Hetzner
// Cloud DNS stores. TXT data is a sequence of quoted character-strings
// of at most 255 bytes each; a value that already starts with a quote
// is taken as preformatted.
func formatRecordValue(rt hcloud.ZoneRRSetType, value string) string {
	if rt != hcloud.ZoneRRSetTypeTXT || strings.HasPrefix(value, `"`) {
		return value
	}
	var chunks []string
	for len(value) > maxTXTChunk {
		chunks = append(chunks, value[:maxTXTChunk])
		value = value[maxTXTChunk:]
	}
	chunks = append(chunks, value)
	for i, c := range chunks {
		c = strings.ReplaceAll(c, `\`, `\\`)
		c = strings.ReplaceAll(c, `"`, `\"`)
		chunks[i] = `"` + c + `"`
	}
	return strings.Join(chunks, " ")
}

// parseRecordValue reverses formatRecordValue: TXT character-strings
// are unquoted and concatenated.
func parseRecordValue(rt hcloud.ZoneRRSetType, value string) string {
	if rt != hcloud.ZoneRRSetTypeTXT || !strings.HasPrefix(value, `"`) {
		return value
	}
	var b strings.Builder
	inQuote, escaped := false, false
	for _, r := range value {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case inQuote && r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case inQuote:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mapRecordType(rt string) (hcloud.ZoneRRSetType, error) {
	t, ok := supportedRecordTypes[strings.ToUpper(strings.TrimSpace(rt))]
	if !ok {
		return "", fmt.Errorf("unsupported record type %q", rt)
	}
	return t, nil
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
	case string:
		// system.toml plugin options are strings.
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer, got %q", name, t)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", name, v)
	}
}

func main() {
	manifest := sdk.Manifest{
		Name:                  pluginName,
		Version:               pluginVersion,
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
			"default_ttl":              {Type: "integer", Default: defaultTTL},
		},
	}
	if err := sdk.Run(manifest, newHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", pluginName, err)
		os.Exit(1)
	}
}
