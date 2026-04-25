package autodns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// PluginInvoker is the supervisor surface the Publisher needs: the
// ability to dispatch a JSON-RPC method on a named DNS plugin and
// unmarshal the result. Mirrors spam.PluginInvoker.
//
// internal/plugin.Manager satisfies this via a tiny adapter; tests
// substitute a fake.
type PluginInvoker interface {
	// Call invokes method on the named plugin with params and decodes
	// the response into result.
	Call(ctx context.Context, plugin, method string, params any, result any) error
}

// Options configures a Publisher.
type Options struct {
	// Store is the metadata + blob handle. The publisher needs blob+queue
	// access only for the TLS-RPT mailto: emission path; PublishDomain
	// itself touches the store only when reading back state for verify.
	Store store.Store
	// Plugins is the plugin invoker used for every dns.* call.
	Plugins PluginInvoker
	// Logger is the structured logger; required.
	Logger *slog.Logger
	// Clock supplies monotonic time for policy IDs and TLS-RPT scheduling.
	Clock clock.Clock
	// HTTPClient is the client used by the TLS-RPT https: emission path.
	// Nil falls back to http.DefaultClient.
	HTTPClient *http.Client
	// DefaultPluginName is the DNS plugin name the publisher dispatches
	// to when the per-domain override is empty.
	DefaultPluginName string
	// Hostname is the server's own MX hostname; used as the default
	// MTA-STS mx whitelist entry when the operator does not pin one.
	Hostname string
}

// Publisher publishes herold-managed DNS records (DKIM TXT, MTA-STS TXT,
// TLSRPT TXT, DMARC TXT) for a domain and reconciles drift via the
// configured DNS plugin. One Publisher per server; safe for concurrent
// use.
type Publisher struct {
	opts Options

	mu       sync.Mutex
	policies map[string]*publishedPolicy // domain -> last published content (in-memory MTA-STS cache)
}

// publishedPolicy is the cached content used to serve MTA-STS over HTTPS.
// One row per domain.
type publishedPolicy struct {
	mtastsBody string
	mtastsTXT  string
	tlsrptTXT  string
	dmarcTXT   string
	dkimTXT    string
	dkimSel    string
}

// New returns a Publisher. Logger and Clock are required; the rest are
// optional and supply sensible production defaults.
func New(opts Options) *Publisher {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	return &Publisher{
		opts:     opts,
		policies: make(map[string]*publishedPolicy),
	}
}

// DomainPolicy is the operator-supplied content to publish for one
// domain. Every field is required; the publisher does not synthesise
// defaults — callers (admin REST, CLI) compose the policy explicitly so
// herold never publishes records the operator did not ask for.
type DomainPolicy struct {
	// DKIMSelector is the selector the active DKIM key uses.
	DKIMSelector string
	// DKIMPublicKey is the base64 public key bundled into the TXT.
	DKIMPublicKey string
	// DKIMAlgorithm selects the k= tag (rsa or ed25519).
	DKIMAlgorithm store.DKIMAlgorithm
	// MTASTSPolicy is the policy body served via HTTPS plus the TXT
	// pointer record.
	MTASTSPolicy MTASTSPolicy
	// TLSRPTRUA is the list of TLS-RPT report URIs (mailto:/https:).
	TLSRPTRUA []string
	// DMARC is the DMARC TXT content.
	DMARC DMARCPolicy
	// DNSPlugin overrides the publisher's DefaultPluginName when set.
	DNSPlugin string
}

// dnsPresentParams mirrors the DNS plugin SDK's DNSPresentParams. Kept
// local so this package does not import plugins/sdk (which would invert
// the dependency direction).
type dnsPresentParams struct {
	Zone       string `json:"zone"`
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
}

type dnsPresentResult struct {
	ID string `json:"id"`
}

type dnsListParams struct {
	Zone       string `json:"zone"`
	RecordType string `json:"record_type"`
	Name       string `json:"name,omitempty"`
}

type dnsRecord struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
}

// DefaultTTL is the TTL the publisher requests for every record. DNS
// plugins typically clamp; this is the herold-side default.
const DefaultTTL = 3600

// PublishDomain publishes every herold-managed DNS record for domain.
// The publisher dispatches one dns.replace call per record (DKIM TXT at
// `<sel>._domainkey.<domain>`, MTA-STS TXT at `_mta-sts.<domain>`,
// TLSRPT TXT at `_smtp._tls.<domain>`, DMARC TXT at `_dmarc.<domain>`).
// dns.replace is upsert-on-the-plugin-side per the manifest; re-calling
// with identical content is a no-op against state and the wire.
func (p *Publisher) PublishDomain(ctx context.Context, domain string, policy DomainPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return errors.New("autodns: empty domain")
	}
	pluginName := policy.DNSPlugin
	if pluginName == "" {
		pluginName = p.opts.DefaultPluginName
	}
	if pluginName == "" {
		return errors.New("autodns: no DNS plugin configured")
	}
	if p.opts.Plugins == nil {
		return errors.New("autodns: no plugin invoker")
	}

	dkimTXT, err := BuildDKIMRecord(policy.DKIMAlgorithm, policy.DKIMPublicKey)
	if err != nil {
		return fmt.Errorf("autodns: build DKIM: %w", err)
	}
	if policy.DKIMSelector == "" {
		return errors.New("autodns: empty DKIM selector")
	}

	// MTA-STS: the policy ID must be stable when the policy body is
	// stable, so we use the cached one when present and the body matches.
	now := p.opts.Clock.Now()
	mtastsPolicy := policy.MTASTSPolicy
	mtastsTXT, mtastsBody, err := BuildMTASTSPolicy(mtastsPolicy, now.Unix())
	if err != nil {
		return fmt.Errorf("autodns: build MTA-STS: %w", err)
	}

	tlsrptTXT, err := BuildTLSRPTRecord(policy.TLSRPTRUA)
	if err != nil {
		return fmt.Errorf("autodns: build TLSRPT: %w", err)
	}

	dmarcTXT, err := BuildDMARCRecord(policy.DMARC)
	if err != nil {
		return fmt.Errorf("autodns: build DMARC: %w", err)
	}

	// Reuse the cached MTA-STS ID when the body is unchanged: an MTA-STS
	// id= bump means "go re-fetch the policy"; we should not advertise
	// that without good reason.
	p.mu.Lock()
	prev := p.policies[domain]
	if prev != nil && prev.mtastsBody == mtastsBody {
		mtastsTXT = prev.mtastsTXT
	}
	p.mu.Unlock()

	type pub struct {
		name, value string
	}
	records := []pub{
		{name: policy.DKIMSelector + "._domainkey." + domain, value: dkimTXT},
		{name: "_mta-sts." + domain, value: mtastsTXT},
		{name: "_smtp._tls." + domain, value: tlsrptTXT},
		{name: "_dmarc." + domain, value: dmarcTXT},
	}
	for _, r := range records {
		if err := p.replaceTXT(ctx, pluginName, domain, r.name, r.value); err != nil {
			return fmt.Errorf("autodns: publish %s: %w", r.name, err)
		}
	}

	p.mu.Lock()
	p.policies[domain] = &publishedPolicy{
		mtastsBody: mtastsBody,
		mtastsTXT:  mtastsTXT,
		tlsrptTXT:  tlsrptTXT,
		dmarcTXT:   dmarcTXT,
		dkimTXT:    dkimTXT,
		dkimSel:    policy.DKIMSelector,
	}
	p.mu.Unlock()

	p.opts.Logger.InfoContext(ctx, "autodns: domain published",
		slog.String("domain", domain),
		slog.String("plugin", pluginName),
	)
	return nil
}

// UpdateDKIMRecord publishes only the DKIM TXT for domain. Called from
// the rotation worker when a new selector is brought online; the rest of
// the records are unchanged so we don't re-emit them.
func (p *Publisher) UpdateDKIMRecord(ctx context.Context, domain string, key store.DKIMKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return errors.New("autodns: empty domain")
	}
	if key.Selector == "" {
		return errors.New("autodns: empty DKIM selector")
	}
	pluginName := p.opts.DefaultPluginName
	if pluginName == "" {
		return errors.New("autodns: no DNS plugin configured")
	}
	dkimTXT, err := BuildDKIMRecord(key.Algorithm, key.PublicKeyB64)
	if err != nil {
		return fmt.Errorf("autodns: build DKIM: %w", err)
	}
	name := key.Selector + "._domainkey." + domain
	if err := p.replaceTXT(ctx, pluginName, domain, name, dkimTXT); err != nil {
		return fmt.Errorf("autodns: publish %s: %w", name, err)
	}

	p.mu.Lock()
	if pp, ok := p.policies[domain]; ok {
		pp.dkimTXT = dkimTXT
		pp.dkimSel = key.Selector
	} else {
		p.policies[domain] = &publishedPolicy{dkimTXT: dkimTXT, dkimSel: key.Selector}
	}
	p.mu.Unlock()

	p.opts.Logger.InfoContext(ctx, "autodns: DKIM updated",
		slog.String("domain", domain),
		slog.String("selector", key.Selector),
	)
	return nil
}

// replaceTXT issues one dns.replace call with TTL=DefaultTTL. The DNS
// plugin's manifest declares dns.replace as upsert.
func (p *Publisher) replaceTXT(ctx context.Context, pluginName, zone, name, value string) error {
	params := dnsPresentParams{
		Zone:       zone,
		RecordType: "TXT",
		Name:       name,
		Value:      value,
		TTL:        DefaultTTL,
	}
	var res dnsPresentResult
	if err := p.opts.Plugins.Call(ctx, pluginName, "dns.replace", params, &res); err != nil {
		return err
	}
	return nil
}

// VerifyState is the per-record verdict returned in a VerifyReport.
type VerifyState string

// VerifyState values. "match" means the published content equals the
// expected content; "drift" means external content differs; "missing"
// means the record is absent on the plugin's view.
const (
	VerifyStateMatch   VerifyState = "match"
	VerifyStateDrift   VerifyState = "drift"
	VerifyStateMissing VerifyState = "missing"
)

// VerifyRecord is one published-record reconciliation result.
type VerifyRecord struct {
	Name     string
	Expected string
	Actual   string
	State    VerifyState
}

// VerifyReport is the structured output of VerifyDomain.
type VerifyReport struct {
	Domain  string
	Records []VerifyRecord
	OK      bool
}

// VerifyDomain queries the DNS plugin's dns.list for every herold-managed
// record name on domain and reports drift relative to the publisher's
// last-published content. A domain that was never PublishDomain'd
// returns a report with OK=true and Records nil; the caller treats that
// as "nothing to verify".
func (p *Publisher) VerifyDomain(ctx context.Context, domain string) (VerifyReport, error) {
	if err := ctx.Err(); err != nil {
		return VerifyReport{}, err
	}
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	pluginName := p.opts.DefaultPluginName
	if pluginName == "" {
		return VerifyReport{}, errors.New("autodns: no DNS plugin configured")
	}
	p.mu.Lock()
	pp := p.policies[domain]
	p.mu.Unlock()
	if pp == nil {
		return VerifyReport{Domain: domain, OK: true}, nil
	}
	type expected struct{ name, value string }
	var checks []expected
	if pp.dkimTXT != "" && pp.dkimSel != "" {
		checks = append(checks, expected{pp.dkimSel + "._domainkey." + domain, pp.dkimTXT})
	}
	if pp.mtastsTXT != "" {
		checks = append(checks, expected{"_mta-sts." + domain, pp.mtastsTXT})
	}
	if pp.tlsrptTXT != "" {
		checks = append(checks, expected{"_smtp._tls." + domain, pp.tlsrptTXT})
	}
	if pp.dmarcTXT != "" {
		checks = append(checks, expected{"_dmarc." + domain, pp.dmarcTXT})
	}
	report := VerifyReport{Domain: domain, OK: true}
	for _, c := range checks {
		recs, err := p.listTXT(ctx, pluginName, domain, c.name)
		if err != nil {
			return report, fmt.Errorf("autodns: verify %s: %w", c.name, err)
		}
		state, actual := classify(c.value, recs)
		if state != VerifyStateMatch {
			report.OK = false
		}
		report.Records = append(report.Records, VerifyRecord{
			Name:     c.name,
			Expected: c.value,
			Actual:   actual,
			State:    state,
		})
	}
	return report, nil
}

func classify(expected string, recs []dnsRecord) (VerifyState, string) {
	if len(recs) == 0 {
		return VerifyStateMissing, ""
	}
	for _, r := range recs {
		if r.Value == expected {
			return VerifyStateMatch, r.Value
		}
	}
	return VerifyStateDrift, recs[0].Value
}

func (p *Publisher) listTXT(ctx context.Context, pluginName, zone, name string) ([]dnsRecord, error) {
	params := dnsListParams{Zone: zone, RecordType: "TXT", Name: name}
	var res []dnsRecord
	if err := p.opts.Plugins.Call(ctx, pluginName, "dns.list", params, &res); err != nil {
		return nil, err
	}
	return res, nil
}
