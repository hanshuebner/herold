// Command herold-dns-route53 is the first-party Route53 DNS plugin. It
// implements the dns.* RPC surface using aws-sdk-go-v2. Authentication
// follows the standard AWS credential chain (env, shared file, IMDS,
// container role, ...). Hosted zones may be supplied directly via
// hosted_zone_id or auto-discovered from the request's zone name.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

const (
	defaultRegion             = "us-east-1"
	defaultPropagationWaitSec = 60
	defaultRequestTimeoutSec  = 30
	defaultRetryAttempts      = 3
	defaultDefaultTTL         = 300
)

var supportedRecordTypes = map[string]r53types.RRType{
	"A":     r53types.RRTypeA,
	"AAAA":  r53types.RRTypeAaaa,
	"TXT":   r53types.RRTypeTxt,
	"MX":    r53types.RRTypeMx,
	"CNAME": r53types.RRTypeCname,
	"TLSA":  r53types.RRTypeTlsa,
}

var knownOptions = map[string]struct{}{
	"aws_region":               {},
	"hosted_zone_id":           {},
	"propagation_wait_seconds": {},
	"request_timeout_seconds":  {},
	"retry_attempts":           {},
	"default_ttl":              {},
	"endpoint_url":             {},
}

type options struct {
	awsRegion       string
	hostedZoneID    string
	propagationWait time.Duration
	requestTimeout  time.Duration
	retryAttempts   int
	defaultTTL      int64
	endpointURL     string // for tests; real config leaves this empty
}

type handler struct {
	mu        sync.RWMutex
	opts      options
	r53       *route53.Client
	awsCfg    aws.Config
	httpCli   *http.Client
	inflight  sync.WaitGroup
	loadCfgFn func(ctx context.Context, opts options) (aws.Config, error)
}

func newHandler() *handler {
	return &handler{
		httpCli:   &http.Client{Timeout: 30 * time.Second},
		loadCfgFn: defaultLoadConfig,
	}
}

func defaultLoadConfig(ctx context.Context, opts options) (aws.Config, error) {
	loadOpts := []func(*config.LoadOptions) error{
		config.WithRegion(opts.awsRegion),
	}
	return config.LoadDefaultConfig(ctx, loadOpts...)
}

// OnConfigure validates options and constructs the Route53 client.
func (h *handler) OnConfigure(ctx context.Context, optsMap map[string]any) error {
	for k := range optsMap {
		if _, ok := knownOptions[k]; !ok {
			return fmt.Errorf("unknown option %q", k)
		}
	}
	cfg := options{
		awsRegion:       defaultRegion,
		propagationWait: time.Duration(defaultPropagationWaitSec) * time.Second,
		requestTimeout:  time.Duration(defaultRequestTimeoutSec) * time.Second,
		retryAttempts:   defaultRetryAttempts,
		defaultTTL:      defaultDefaultTTL,
	}
	if v, ok := optsMap["aws_region"]; ok {
		s, err := asString(v, "aws_region")
		if err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return errors.New("aws_region must be non-empty")
		}
		cfg.awsRegion = s
	}
	if v, ok := optsMap["hosted_zone_id"]; ok {
		s, err := asString(v, "hosted_zone_id")
		if err != nil {
			return err
		}
		cfg.hostedZoneID = strings.TrimSpace(s)
	}
	if v, ok := optsMap["propagation_wait_seconds"]; ok {
		n, err := asInt(v, "propagation_wait_seconds")
		if err != nil {
			return err
		}
		if n < 0 || n > 3600 {
			return fmt.Errorf("propagation_wait_seconds out of range (0..3600): %d", n)
		}
		cfg.propagationWait = time.Duration(n) * time.Second
	}
	if v, ok := optsMap["request_timeout_seconds"]; ok {
		n, err := asInt(v, "request_timeout_seconds")
		if err != nil {
			return err
		}
		if n <= 0 || n > 600 {
			return fmt.Errorf("request_timeout_seconds out of range (1..600): %d", n)
		}
		cfg.requestTimeout = time.Duration(n) * time.Second
	}
	if v, ok := optsMap["retry_attempts"]; ok {
		n, err := asInt(v, "retry_attempts")
		if err != nil {
			return err
		}
		if n < 0 || n > 10 {
			return fmt.Errorf("retry_attempts out of range (0..10): %d", n)
		}
		cfg.retryAttempts = n
	}
	if v, ok := optsMap["default_ttl"]; ok {
		n, err := asInt(v, "default_ttl")
		if err != nil {
			return err
		}
		if n < 1 || n > 86400 {
			return fmt.Errorf("default_ttl out of range (1..86400): %d", n)
		}
		cfg.defaultTTL = int64(n)
	}
	if v, ok := optsMap["endpoint_url"]; ok {
		s, err := asString(v, "endpoint_url")
		if err != nil {
			return err
		}
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if s != "" && !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return fmt.Errorf("endpoint_url must be http(s) URL, got %q", s)
		}
		cfg.endpointURL = s
	}

	awsCfg, err := h.loadCfgFn(ctx, cfg)
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	awsCfg.RetryMaxAttempts = cfg.retryAttempts + 1
	awsCfg.HTTPClient = h.httpCli

	clientOpts := []func(*route53.Options){}
	if cfg.endpointURL != "" {
		ep := cfg.endpointURL
		clientOpts = append(clientOpts, func(o *route53.Options) {
			o.BaseEndpoint = &ep
		})
	}
	r53 := route53.NewFromConfig(awsCfg, clientOpts...)

	h.mu.Lock()
	h.opts = cfg
	h.awsCfg = awsCfg
	h.r53 = r53
	h.mu.Unlock()
	sdk.Logf("info", "herold-dns-route53 configured region=%s hosted_zone_id=%q propagation_wait=%s",
		cfg.awsRegion, cfg.hostedZoneID, cfg.propagationWait)
	return nil
}

// OnHealth pings the Route53 control plane with a small list call.
func (h *handler) OnHealth(ctx context.Context) error {
	h.mu.RLock()
	cli := h.r53
	h.mu.RUnlock()
	if cli == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	maxItems := int32(1)
	_, err := cli.ListHostedZonesByName(probeCtx, &route53.ListHostedZonesByNameInput{MaxItems: &maxItems})
	if err != nil {
		return fmt.Errorf("route53 health: %w", err)
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

func (h *handler) DNSPresent(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	return h.upsert(ctx, in, r53types.ChangeActionCreate)
}

func (h *handler) DNSReplace(ctx context.Context, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	return h.upsert(ctx, in, r53types.ChangeActionUpsert)
}

// upsert performs a ChangeResourceRecordSets with the given action.
// Route53 IDs are not stable per-record (everything is a record set
// identified by name+type), so we synthesize a deterministic id from
// the zone, type, and name. dns.cleanup parses that id back out.
func (h *handler) upsert(ctx context.Context, in sdk.DNSPresentParams, action r53types.ChangeAction) (sdk.DNSPresentResult, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	rrType, err := mapRecordType(in.RecordType)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return sdk.DNSPresentResult{}, errors.New("name is required")
	}

	hzID, err := h.resolveHostedZone(ctx, in.Zone)
	if err != nil {
		return sdk.DNSPresentResult{}, err
	}
	ttl := int64(in.TTL)
	if ttl <= 0 {
		h.mu.RLock()
		ttl = h.opts.defaultTTL
		h.mu.RUnlock()
	}
	value := formatRecordValue(in.RecordType, in.Value)

	// CREATE on an existing record set fails; CREATE on a new one
	// succeeds. To honor "present" semantics for ACME callers we map
	// CREATE to UPSERT when the record already matches.
	effective := action
	if action == r53types.ChangeActionCreate {
		effective = r53types.ChangeActionUpsert
	}

	change := r53types.Change{
		Action: effective,
		ResourceRecordSet: &r53types.ResourceRecordSet{
			Name: aws.String(in.Name),
			Type: rrType,
			TTL:  aws.Int64(ttl),
			ResourceRecords: []r53types.ResourceRecord{
				{Value: aws.String(value)},
			},
		},
	}
	if err := h.applyChange(ctx, hzID, change); err != nil {
		return sdk.DNSPresentResult{}, err
	}
	h.waitForPropagation(ctx)
	return sdk.DNSPresentResult{ID: encodeID(hzID, in.Name, in.RecordType, value)}, nil
}

func (h *handler) DNSCleanup(ctx context.Context, in sdk.DNSCleanupParams) error {
	h.inflight.Add(1)
	defer h.inflight.Done()
	if strings.TrimSpace(in.ID) == "" {
		return errors.New("id is required")
	}
	hzID, name, recordType, value, err := decodeID(in.ID)
	if err != nil {
		return err
	}
	rrType, err := mapRecordType(recordType)
	if err != nil {
		return err
	}
	// Route53 requires the exact value+TTL on DELETE. Look up the
	// existing record set so we send the matching values.
	rec, err := h.findRecord(ctx, hzID, name, rrType)
	if err != nil {
		return err
	}
	if rec == nil {
		return nil // already gone — idempotent cleanup
	}
	ttl := int64(defaultDefaultTTL)
	if rec.TTL != nil {
		ttl = *rec.TTL
	}
	if value == "" && len(rec.ResourceRecords) > 0 && rec.ResourceRecords[0].Value != nil {
		value = aws.ToString(rec.ResourceRecords[0].Value)
	}
	change := r53types.Change{
		Action: r53types.ChangeActionDelete,
		ResourceRecordSet: &r53types.ResourceRecordSet{
			Name:            aws.String(name),
			Type:            rrType,
			TTL:             aws.Int64(ttl),
			ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(value)}},
		},
	}
	return h.applyChange(ctx, hzID, change)
}

func (h *handler) DNSList(ctx context.Context, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	h.inflight.Add(1)
	defer h.inflight.Done()

	hzID, err := h.resolveHostedZone(ctx, in.Zone)
	if err != nil {
		return nil, err
	}
	var startName *string
	var startType r53types.RRType
	if in.Name != "" {
		startName = aws.String(in.Name)
	}
	if in.RecordType != "" {
		rt, err := mapRecordType(in.RecordType)
		if err != nil {
			return nil, err
		}
		startType = rt
	}

	h.mu.RLock()
	cli := h.r53
	timeout := h.opts.requestTimeout
	h.mu.RUnlock()
	if cli == nil {
		return nil, errors.New("plugin not configured")
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := []sdk.DNSRecord{}
	maxItems := int32(100)
	in2 := &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(hzID),
		MaxItems:        &maxItems,
		StartRecordName: startName,
		StartRecordType: startType,
	}
	resp, err := cli.ListResourceRecordSets(callCtx, in2)
	if err != nil {
		return nil, fmt.Errorf("ListResourceRecordSets: %w", err)
	}
	for _, rs := range resp.ResourceRecordSets {
		if in.RecordType != "" && rs.Type != startType {
			continue
		}
		if in.Name != "" && !strings.EqualFold(aws.ToString(rs.Name), in.Name) &&
			!strings.EqualFold(aws.ToString(rs.Name), in.Name+".") {
			continue
		}
		ttl := 0
		if rs.TTL != nil {
			ttl = int(*rs.TTL)
		}
		for _, r := range rs.ResourceRecords {
			val := aws.ToString(r.Value)
			out = append(out, sdk.DNSRecord{
				ID:    encodeID(hzID, aws.ToString(rs.Name), string(rs.Type), val),
				Value: val,
				TTL:   ttl,
			})
		}
	}
	return out, nil
}

func (h *handler) findRecord(ctx context.Context, hzID, name string, rrType r53types.RRType) (*r53types.ResourceRecordSet, error) {
	h.mu.RLock()
	cli := h.r53
	timeout := h.opts.requestTimeout
	h.mu.RUnlock()
	if cli == nil {
		return nil, errors.New("plugin not configured")
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxItems := int32(1)
	resp, err := cli.ListResourceRecordSets(callCtx, &route53.ListResourceRecordSetsInput{
		HostedZoneId:    aws.String(hzID),
		StartRecordName: aws.String(name),
		StartRecordType: rrType,
		MaxItems:        &maxItems,
	})
	if err != nil {
		return nil, fmt.Errorf("ListResourceRecordSets: %w", err)
	}
	for i := range resp.ResourceRecordSets {
		rs := &resp.ResourceRecordSets[i]
		if rs.Type != rrType {
			continue
		}
		got := strings.TrimSuffix(aws.ToString(rs.Name), ".")
		want := strings.TrimSuffix(name, ".")
		if !strings.EqualFold(got, want) {
			continue
		}
		return rs, nil
	}
	return nil, nil
}

func (h *handler) applyChange(ctx context.Context, hzID string, change r53types.Change) error {
	h.mu.RLock()
	cli := h.r53
	timeout := h.opts.requestTimeout
	h.mu.RUnlock()
	if cli == nil {
		return errors.New("plugin not configured")
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := cli.ChangeResourceRecordSets(callCtx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(hzID),
		ChangeBatch:  &r53types.ChangeBatch{Changes: []r53types.Change{change}},
	})
	if err != nil {
		return fmt.Errorf("ChangeResourceRecordSets: %w", err)
	}
	return nil
}

func (h *handler) resolveHostedZone(ctx context.Context, zoneName string) (string, error) {
	h.mu.RLock()
	zid := h.opts.hostedZoneID
	cli := h.r53
	timeout := h.opts.requestTimeout
	h.mu.RUnlock()
	if zid != "" {
		return zid, nil
	}
	if strings.TrimSpace(zoneName) == "" {
		return "", errors.New("hosted_zone_id not configured and no zone supplied in request")
	}
	if cli == nil {
		return "", errors.New("plugin not configured")
	}
	wantName := strings.TrimSuffix(zoneName, ".") + "."
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxItems := int32(2)
	resp, err := cli.ListHostedZonesByName(callCtx, &route53.ListHostedZonesByNameInput{
		DNSName:  aws.String(wantName),
		MaxItems: &maxItems,
	})
	if err != nil {
		return "", fmt.Errorf("ListHostedZonesByName: %w", err)
	}
	for _, hz := range resp.HostedZones {
		if strings.EqualFold(aws.ToString(hz.Name), wantName) {
			return strings.TrimPrefix(aws.ToString(hz.Id), "/hostedzone/"), nil
		}
	}
	return "", fmt.Errorf("no Route53 hosted zone found for %q", zoneName)
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

// encodeID packs the four fields needed to delete a Route53 record into
// a single opaque string. Callers (autodns, ACME) treat the id as
// opaque; the format is internal to this plugin.
func encodeID(hzID, name, recordType, value string) string {
	return fmt.Sprintf("%s|%s|%s|%s", hzID, strings.TrimSuffix(name, "."), strings.ToUpper(recordType), value)
}

func decodeID(id string) (hzID, name, recordType, value string, err error) {
	parts := strings.SplitN(id, "|", 4)
	if len(parts) < 3 {
		return "", "", "", "", fmt.Errorf("invalid record id %q", id)
	}
	hzID, name, recordType = parts[0], parts[1], parts[2]
	if len(parts) == 4 {
		value = parts[3]
	}
	return
}

// formatRecordValue applies record-type-specific formatting required
// by Route53. TXT values must be wrapped in double quotes; the others
// pass through unchanged.
func formatRecordValue(recordType, value string) string {
	if strings.ToUpper(recordType) == "TXT" {
		v := strings.Trim(value, `"`)
		// Escape any embedded quotes per RFC 1035.
		v = strings.ReplaceAll(v, `"`, `\"`)
		return `"` + v + `"`
	}
	return value
}

func mapRecordType(rt string) (r53types.RRType, error) {
	t, ok := supportedRecordTypes[strings.ToUpper(rt)]
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
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", name, v)
	}
}

func main() {
	manifest := sdk.Manifest{
		Name:                  "herold-dns-route53",
		Version:               "0.1.0",
		Type:                  plug.TypeDNS,
		Lifecycle:             plug.LifecycleLongRunning,
		MaxConcurrentRequests: 8,
		ABIVersion:            plug.ABIVersion,
		ShutdownGraceSec:      10,
		HealthIntervalSec:     60,
		Capabilities:          []string{sdk.MethodDNSPresent, sdk.MethodDNSCleanup, sdk.MethodDNSList, sdk.MethodDNSReplace},
		OptionsSchema: map[string]plug.OptionSchema{
			"aws_region":               {Type: "string", Default: defaultRegion},
			"hosted_zone_id":           {Type: "string"},
			"propagation_wait_seconds": {Type: "integer", Default: defaultPropagationWaitSec},
			"request_timeout_seconds":  {Type: "integer", Default: defaultRequestTimeoutSec},
			"retry_attempts":           {Type: "integer", Default: defaultRetryAttempts},
			"default_ttl":              {Type: "integer", Default: defaultDefaultTTL},
			"endpoint_url":             {Type: "string"},
		},
	}
	if err := sdk.Run(manifest, newHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "herold-dns-route53: %v\n", err)
		os.Exit(1)
	}
}
