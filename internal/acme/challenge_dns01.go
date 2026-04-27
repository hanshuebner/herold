package acme

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
)

// DNS01PropagationDelay is the time the challenger waits between a
// successful dns.present and reporting the challenge ready. The delay
// gives recursive resolvers time to pull the new authoritative TXT.
// Configurable for tests (the default is 30s).
const DNS01PropagationDelay = 30 * time.Second

// PluginInvoker is the minimal slice of *plugin.Manager DNS01Challenger
// needs: dispatch a JSON-RPC method on a named plugin and decode the
// result. Production code wires *plugin.Manager via a tiny adapter
// (mirrors the spam package's PluginInvoker).
type PluginInvoker interface {
	// Call invokes method on plugin with params; result is populated
	// from the plugin's JSON result.
	Call(ctx context.Context, plugin, method string, params any, result any) error
}

// dnsPresentParams is the wire shape for dns.present per
// docs/design/server/requirements/11-plugins.md §DNS provider.
type dnsPresentParams struct {
	Zone       string `json:"zone"`
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
}

// dnsPresentResult is the {id} return shape.
type dnsPresentResult struct {
	ID string `json:"id"`
}

// dnsCleanupParams carries the opaque id back to the plugin.
type dnsCleanupParams struct {
	ID string `json:"id"`
}

// DNS01Challenger answers ACME dns-01 challenges by delegating record
// publication to the configured DNS plugin. The plugin returns success
// once the record is propagated to the authoritative nameserver; the
// challenger then waits DNS01PropagationDelay before reporting back to
// the ACME server, giving recursive resolvers time to pull the change.
type DNS01Challenger struct {
	plugins        PluginInvoker
	logger         *slog.Logger
	clock          clock.Clock
	propagateDelay time.Duration

	// records maps domain name -> opaque plugin record id, captured
	// from dns.present so Cleanup can pass it to dns.cleanup.
	records map[string]string
}

// DNS01Options configures a DNS01Challenger.
type DNS01Options struct {
	// Plugins is the plugin invoker (the production wiring is a
	// *plugin.Manager-backed adapter).
	Plugins PluginInvoker
	// Logger receives structured plugin-call traces. nil falls back to
	// slog.Default.
	Logger *slog.Logger
	// Clock supplies the propagation-delay sleep.
	Clock clock.Clock
	// PropagationDelay overrides the post-present sleep. Zero means
	// DNS01PropagationDelay.
	PropagationDelay time.Duration
}

// NewDNS01Challenger constructs a challenger.
func NewDNS01Challenger(opts DNS01Options) *DNS01Challenger {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.NewReal()
	}
	delay := opts.PropagationDelay
	if delay <= 0 {
		delay = DNS01PropagationDelay
	}
	return &DNS01Challenger{
		plugins:        opts.Plugins,
		logger:         opts.Logger,
		clock:          opts.Clock,
		propagateDelay: delay,
		records:        make(map[string]string),
	}
}

// Provision invokes dns.present on the named DNS plugin to publish the
// _acme-challenge TXT record for domain. After the plugin acknowledges,
// the challenger sleeps PropagationDelay to give recursive resolvers
// time to pull the new record before the ACME server validates it.
//
// keyAuth is the RFC 8555 §8.1 key authorisation; the wire value is
// base64url(SHA-256(keyAuth)) per RFC 8555 §8.4.
func (d *DNS01Challenger) Provision(ctx context.Context, domain, keyAuth, pluginName string) error {
	if d.plugins == nil {
		return errors.New("acme: dns-01 plugin invoker not configured")
	}
	if pluginName == "" {
		return errors.New("acme: dns-01 plugin name empty")
	}
	if domain == "" {
		return errors.New("acme: dns-01 domain empty")
	}
	value := dns01TXTValue(keyAuth)
	zone, name := splitZone(domain)
	params := dnsPresentParams{
		Zone:       zone,
		RecordType: "TXT",
		Name:       name,
		Value:      value,
		TTL:        60,
	}
	var res dnsPresentResult
	if err := d.plugins.Call(ctx, pluginName, "dns.present", params, &res); err != nil {
		return fmt.Errorf("acme: dns.present: %w", err)
	}
	if res.ID == "" {
		return errors.New("acme: dns.present returned empty id")
	}
	d.records[strings.ToLower(domain)] = res.ID
	d.logger.InfoContext(ctx, "acme dns-01 record presented",
		"plugin", pluginName, "domain", domain, "record_id", res.ID,
		"propagation_delay", d.propagateDelay)
	if d.propagateDelay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.clock.After(d.propagateDelay):
		}
	}
	return nil
}

// Cleanup invokes dns.cleanup on the same plugin with the opaque id
// captured at Provision time. Safe to call after a Provision failure
// (no-op).
func (d *DNS01Challenger) Cleanup(ctx context.Context, domain, pluginName string) error {
	if d.plugins == nil || pluginName == "" {
		return nil
	}
	key := strings.ToLower(domain)
	id, ok := d.records[key]
	if !ok {
		return nil
	}
	delete(d.records, key)
	if err := d.plugins.Call(ctx, pluginName, "dns.cleanup", dnsCleanupParams{ID: id}, nil); err != nil {
		return fmt.Errorf("acme: dns.cleanup: %w", err)
	}
	d.logger.InfoContext(ctx, "acme dns-01 record cleaned",
		"plugin", pluginName, "domain", domain, "record_id", id)
	return nil
}

// dns01TXTValue computes base64url(SHA-256(keyAuth)) per RFC 8555 §8.4.
func dns01TXTValue(keyAuth string) string {
	sum := sha256.Sum256([]byte(keyAuth))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// splitZone splits domain into (zone, name) for the dns.present call.
// We pass the full domain as the zone and "_acme-challenge" as the
// name; production DNS plugins handle apex / subdomain composition
// internally because the plugin alone knows which zone the domain
// belongs to. The contract documented in
// docs/design/server/requirements/11-plugins.md is opaque: the plugin treats (zone,
// name) as the operator-visible coordinates.
func splitZone(domain string) (string, string) {
	return domain, "_acme-challenge"
}
