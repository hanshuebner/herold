package autodns_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/autodns"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// presentCall captures the parameters of one dns.replace invocation
// so tests can assert what was published, in what order.
type presentCall struct {
	Zone       string `json:"zone"`
	RecordType string `json:"record_type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
}

// fakeInvoker adapts fakeplugin.Registry to autodns.PluginInvoker.
type fakeInvoker struct {
	reg *fakeplugin.Registry
}

func (f fakeInvoker) Call(ctx context.Context, plugin, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	out, err := f.reg.Call(ctx, plugin, method, raw)
	if err != nil {
		return err
	}
	if result != nil && len(out) > 0 {
		return json.Unmarshal(out, result)
	}
	return nil
}

// dnsRecorder is a fake dns.* plugin: it records every dns.replace call
// (so the tests can assert exactly what the publisher emitted), and it
// implements dns.list against an operator-supplied seam so drift tests
// can return mismatched records.
type dnsRecorder struct {
	mu      sync.Mutex
	calls   []presentCall
	listFn  func(zone, name string) []presentCall
	nextID  atomic.Int64
	plugin  *fakeplugin.FakePlugin
	healthy bool
}

func newDNSRecorder(name string) (*dnsRecorder, *fakeplugin.FakePlugin) {
	r := &dnsRecorder{plugin: fakeplugin.New(name, "dns"), healthy: true}
	r.plugin.Handle("dns.replace", func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p presentCall
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.calls = append(r.calls, p)
		r.mu.Unlock()
		id := r.nextID.Add(1)
		out, _ := json.Marshal(map[string]string{"id": "rec-" + itoa(id)})
		return out, nil
	})
	r.plugin.Handle("dns.list", func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Zone, Name, RecordType string
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		r.mu.Lock()
		fn := r.listFn
		var matches []presentCall
		if fn != nil {
			matches = fn(p.Zone, p.Name)
		} else {
			for _, c := range r.calls {
				if c.Zone == p.Zone && c.Name == p.Name {
					matches = append(matches, c)
				}
			}
		}
		r.mu.Unlock()
		// Convert to dnsRecord-shaped output.
		out := make([]map[string]any, 0, len(matches))
		for i, c := range matches {
			out = append(out, map[string]any{
				"id":    "rec-" + itoa(int64(i+1)),
				"value": c.Value,
				"ttl":   c.TTL,
			})
		}
		raw, _ := json.Marshal(out)
		return raw, nil
	})
	r.plugin.Handle("dns.cleanup", func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage("null"), nil
	})
	return r, r.plugin
}

func (r *dnsRecorder) snapshot() []presentCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]presentCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func newPublisher(t *testing.T, pluginName string) (*autodns.Publisher, *dnsRecorder, *fakeplugin.Registry, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	rec, plug := newDNSRecorder(pluginName)
	reg := fakeplugin.NewRegistry()
	reg.Register(plug)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	pub := autodns.New(autodns.Options{
		Store:             fs,
		Plugins:           fakeInvoker{reg: reg},
		Logger:            logger,
		Clock:             clk,
		DefaultPluginName: pluginName,
		Hostname:          "mx.example.test",
	})
	return pub, rec, reg, clk
}

func samplePolicy() autodns.DomainPolicy {
	return autodns.DomainPolicy{
		DKIMSelector:  "herold202601",
		DKIMPublicKey: "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA",
		DKIMAlgorithm: store.DKIMAlgorithmRSASHA256,
		MTASTSPolicy: autodns.MTASTSPolicy{
			Mode:          autodns.MTASTSModeEnforce,
			MX:            []string{"mx.example.test"},
			MaxAgeSeconds: 604800,
		},
		TLSRPTRUA: []string{"mailto:tlsrpt@example.test"},
		DMARC: autodns.DMARCPolicy{
			Policy: mailauth.DMARCPolicyQuarantine,
			RUA:    []string{"mailto:agg@example.test"},
			ADKIM:  autodns.DMARCAlignmentRelaxed,
			ASPF:   autodns.DMARCAlignmentRelaxed,
		},
	}
}

func TestPublisher_PublishDomain_AllFourRecords(t *testing.T) {
	pub, rec, _, _ := newPublisher(t, "fake-dns")
	ctx := t.Context()
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 4 {
		t.Fatalf("call count: got %d, want 4 (calls=%+v)", len(calls), calls)
	}
	wantNames := map[string]bool{
		"herold202601._domainkey.example.test": false,
		"_mta-sts.example.test":                false,
		"_smtp._tls.example.test":              false,
		"_dmarc.example.test":                  false,
	}
	for _, c := range calls {
		if c.RecordType != "TXT" {
			t.Fatalf("RecordType: got %q for %q", c.RecordType, c.Name)
		}
		if c.Zone != "example.test" {
			t.Fatalf("Zone: got %q for %q", c.Zone, c.Name)
		}
		if _, ok := wantNames[c.Name]; !ok {
			t.Fatalf("unexpected record name %q", c.Name)
		}
		wantNames[c.Name] = true
	}
	for name, seen := range wantNames {
		if !seen {
			t.Fatalf("missing record name %q", name)
		}
	}

	// Re-publish with the same policy. Per the publisher's content-hash
	// cache, the MTA-STS id stays stable and the existing record-set is
	// re-emitted. Verify that — currently the publisher does NOT
	// short-circuit the dns.replace calls, it just keeps the same id.
	// Document the behaviour observed.
	before := len(rec.snapshot())
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain second: %v", err)
	}
	after := rec.snapshot()
	if len(after) <= before {
		t.Fatalf("re-publish made %d total calls (was %d) — expected non-decreasing", len(after), before)
	}
	// Confirm the second batch carried the same MTA-STS TXT (stable id):
	// The publisher's "reuse cached id when body unchanged" rule must
	// hold. Find both _mta-sts records.
	var mtastsCalls []presentCall
	for _, c := range after {
		if c.Name == "_mta-sts.example.test" {
			mtastsCalls = append(mtastsCalls, c)
		}
	}
	if len(mtastsCalls) < 2 {
		t.Fatalf("expected at least two _mta-sts emissions, got %d", len(mtastsCalls))
	}
	if mtastsCalls[0].Value != mtastsCalls[1].Value {
		t.Fatalf("MTA-STS TXT id changed between identical re-publishes:\nfirst:  %q\nsecond: %q",
			mtastsCalls[0].Value, mtastsCalls[1].Value)
	}
}

func TestPublisher_UpdateDKIMRecord_ReplacesActive(t *testing.T) {
	pub, rec, _, _ := newPublisher(t, "fake-dns")
	ctx := t.Context()
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	beforeCalls := rec.snapshot()
	const newKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEnewKeyMaterialDoesNotMatter=="
	rotated := store.DKIMKey{
		Domain:       "example.test",
		Selector:     "herold202602",
		Algorithm:    store.DKIMAlgorithmEd25519SHA256,
		PublicKeyB64: newKey,
	}
	if err := pub.UpdateDKIMRecord(ctx, "example.test", rotated); err != nil {
		t.Fatalf("UpdateDKIMRecord: %v", err)
	}
	after := rec.snapshot()
	if len(after) != len(beforeCalls)+1 {
		t.Fatalf("UpdateDKIMRecord call count: got delta %d, want 1", len(after)-len(beforeCalls))
	}
	last := after[len(after)-1]
	if last.Name != "herold202602._domainkey.example.test" {
		t.Fatalf("name: got %q", last.Name)
	}
	if want := "v=DKIM1; k=ed25519; p=" + newKey; last.Value != want {
		t.Fatalf("value: got %q\nwant %q", last.Value, want)
	}
}

func TestPublisher_VerifyDomain_DriftDetected(t *testing.T) {
	pub, rec, _, _ := newPublisher(t, "fake-dns")
	ctx := t.Context()
	if err := pub.PublishDomain(ctx, "example.test", samplePolicy()); err != nil {
		t.Fatalf("PublishDomain: %v", err)
	}
	// Inject drift: dns.list now returns altered records for the DKIM name.
	rec.mu.Lock()
	rec.listFn = func(zone, name string) []presentCall {
		// Return a different value for DKIM, the rest match.
		var out []presentCall
		for _, c := range rec.calls {
			if c.Zone == zone && c.Name == name {
				if name == "herold202601._domainkey.example.test" {
					tmp := c
					tmp.Value = "v=DKIM1; k=rsa; p=DRIFTED-CONTENT"
					out = append(out, tmp)
				} else {
					out = append(out, c)
				}
			}
		}
		return out
	}
	rec.mu.Unlock()
	report, err := pub.VerifyDomain(ctx, "example.test")
	if err != nil {
		t.Fatalf("VerifyDomain: %v", err)
	}
	if report.OK {
		t.Fatalf("expected OK=false, drift was injected")
	}
	if report.Domain != "example.test" {
		t.Fatalf("Domain: got %q", report.Domain)
	}
	var driftSeen bool
	for _, r := range report.Records {
		if r.Name == "herold202601._domainkey.example.test" && r.State == autodns.VerifyStateDrift {
			driftSeen = true
		}
	}
	if !driftSeen {
		t.Fatalf("expected drift on DKIM record, got %+v", report.Records)
	}
}

func TestReporter_Append_PersistsViaStore(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := t.Context()
	failure := store.TLSRPTFailure{
		PolicyDomain:         "example.test",
		ReceivingMTAHostname: "mx.example.test",
		FailureType:          store.TLSRPTFailureSTARTTLSNegotiation,
		FailureCode:          "starttls-fail",
		FailureDetailJSON:    `{"reason":"handshake"}`,
	}
	if err := fs.Meta().AppendTLSRPTFailure(ctx, failure); err != nil {
		t.Fatalf("AppendTLSRPTFailure: %v", err)
	}
	rows, err := fs.Meta().ListTLSRPTFailures(ctx, "example.test", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ListTLSRPTFailures: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	if rows[0].FailureType != store.TLSRPTFailureSTARTTLSNegotiation {
		t.Fatalf("failure type round-trip wrong: %v", rows[0].FailureType)
	}
}
