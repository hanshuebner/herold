package acme

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// quickPoll is the poll interval used in client tests so the order
// pipeline does not stall on the clock. The stub flips authz / order
// status synchronously inside the challenge-ack handler, so a 1ms
// poll is enough for the next iteration to observe the transition.
const quickPoll = time.Millisecond

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newFS constructs the fakestore + fake clock used in every ACME test.
func newFS(t *testing.T) (*fakestore.Store, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs, clk
}

// httpFetcherForChallenger builds the stub's HTTP-01 validator: it pulls
// the keyAuth from the challenger and compares against the expected
// value (token + "." + thumbprint). The thumbprint is recomputed from
// the client's signer.
func httpFetcherForChallenger(t *testing.T, ch *HTTPChallenger) func(token string) (string, error) {
	return func(token string) (string, error) {
		ch.mu.RLock()
		v, ok := ch.tokens[token]
		ch.mu.RUnlock()
		if !ok {
			return "", errors.New("http-01: token not provisioned")
		}
		return v, nil
	}
}

func TestEnsureCert_HTTP01_HappyPath(t *testing.T) {
	fs, clk := newFS(t)
	stub := newStubServer(t)
	httpCh := NewHTTPChallenger()
	stub.httpFetcher = httpFetcherForChallenger(t, httpCh)

	c := New(Options{
		DirectoryURL:   stub.directoryURL(),
		ContactEmail:   "ops@example.test",
		Store:          fs,
		Logger:         discardLogger(),
		Clock:          clock.NewReal(), // real clock so polling fires on quickPoll
		HTTPChallenger: httpCh,
		PollInterval:   quickPoll,
		MaxPolls:       200,
	})
	_ = clk
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeHTTP01, ""); err != nil {
		t.Fatalf("EnsureCert: %v", err)
	}
	got, err := fs.Meta().GetACMECert(ctx, "mail.example.test")
	if err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
	if got.Hostname != "mail.example.test" {
		t.Fatalf("hostname: %q", got.Hostname)
	}
	if !strings.Contains(got.ChainPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("ChainPEM missing CERTIFICATE block")
	}
	leaf := parsePEMCert(t, got.ChainPEM)
	if leaf.Subject.CommonName != "mail.example.test" {
		t.Fatalf("leaf CN: %q", leaf.Subject.CommonName)
	}
}

func TestEnsureCert_TLSALPN01_HappyPath(t *testing.T) {
	fs, _ := newFS(t)
	stub := newStubServer(t)
	tlsCh := NewTLSALPNChallenger()

	c := New(Options{
		DirectoryURL:      stub.directoryURL(),
		ContactEmail:      "ops@example.test",
		Store:             fs,
		Logger:            discardLogger(),
		Clock:             clock.NewReal(),
		TLSALPNChallenger: tlsCh,
		PollInterval:      quickPoll,
		MaxPolls:          200,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeTLSALPN01, ""); err != nil {
		t.Fatalf("EnsureCert: %v", err)
	}
	if _, err := fs.Meta().GetACMECert(ctx, "mail.example.test"); err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
}

// fakeDNSInvoker adapts fakeplugin.Registry to acme.PluginInvoker.
type fakeDNSInvoker struct {
	reg *fakeplugin.Registry
}

func (f fakeDNSInvoker) Call(ctx context.Context, plugin, method string, params, result any) error {
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

func TestEnsureCert_DNS01_HappyPath_FakePlugin(t *testing.T) {
	fs, _ := newFS(t)
	stub := newStubServer(t)

	plug := fakeplugin.New("test-dns", "dns")
	var (
		mu       sync.Mutex
		presents []string
		idCount  atomic.Int64
	)
	plug.Handle("dns.present", func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p struct {
			Zone, Name, Value, RecordType string
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		mu.Lock()
		presents = append(presents, p.Value)
		mu.Unlock()
		id := idCount.Add(1)
		out, _ := json.Marshal(map[string]string{"id": "rec-" + strings.ToLower(strings.TrimSpace(itoaInt64(id)))})
		return out, nil
	})
	plug.Handle("dns.cleanup", func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage("null"), nil
	})
	reg := fakeplugin.NewRegistry()
	reg.Register(plug)

	dns01 := NewDNS01Challenger(DNS01Options{
		Plugins:          fakeDNSInvoker{reg: reg},
		Logger:           discardLogger(),
		Clock:            clock.NewReal(),
		PropagationDelay: time.Microsecond, // bypass propagation wait
	})
	c := New(Options{
		DirectoryURL:    stub.directoryURL(),
		ContactEmail:    "ops@example.test",
		Store:           fs,
		Logger:          discardLogger(),
		Clock:           clock.NewReal(),
		DNS01Challenger: dns01,
		PollInterval:    quickPoll,
		MaxPolls:        200,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeDNS01, "test-dns"); err != nil {
		t.Fatalf("EnsureCert: %v", err)
	}
	if _, err := fs.Meta().GetACMECert(ctx, "mail.example.test"); err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(presents) == 0 {
		t.Fatalf("dns.present was never called")
	}
}

func itoaInt64(n int64) string {
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

func TestEnsureCert_Idempotent_ExistingCert_NoOp(t *testing.T) {
	fs, clk := newFS(t)
	ctx := t.Context()
	// Seed a fresh cert with NotAfter ~90d ahead so EnsureCert short-circuits.
	now := clk.Now()
	if err := fs.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname:      "mail.example.test",
		ChainPEM:      "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n",
		NotBefore:     now.Add(-time.Hour),
		NotAfter:      now.Add(90 * 24 * time.Hour),
		Issuer:        "stub",
	}); err != nil {
		t.Fatalf("UpsertACMECert: %v", err)
	}
	httpCh := NewHTTPChallenger()
	c := New(Options{
		// Deliberately broken DirectoryURL so any network attempt fails.
		DirectoryURL:   "http://127.0.0.1:1/should-not-be-called",
		ContactEmail:   "ops@example.test",
		Store:          fs,
		Logger:         discardLogger(),
		Clock:          clk,
		HTTPChallenger: httpCh,
		PollInterval:   quickPoll,
	})
	if err := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeHTTP01, ""); err != nil {
		t.Fatalf("EnsureCert idempotent: %v", err)
	}
}

func TestRunRenewalLoop_RenewsExpiringCerts(t *testing.T) {
	fs, clk := newFS(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := clk.Now()
	// Seed a cert that expires in 7d -> below RenewalThreshold (30d).
	if err := fs.Meta().UpsertACMECert(ctx, store.ACMECert{
		Hostname:      "mail.example.test",
		ChainPEM:      "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n",
		NotBefore:     now.Add(-time.Hour),
		NotAfter:      now.Add(7 * 24 * time.Hour),
		Issuer:        "stub",
	}); err != nil {
		t.Fatalf("UpsertACMECert: %v", err)
	}

	stub := newStubServer(t)
	httpCh := NewHTTPChallenger()
	stub.httpFetcher = httpFetcherForChallenger(t, httpCh)

	// The order pipeline polls via this clock. Use Real so poll fires.
	c := New(Options{
		DirectoryURL:   stub.directoryURL(),
		ContactEmail:   "ops@example.test",
		Store:          fs,
		Logger:         discardLogger(),
		Clock:          clock.NewReal(),
		HTTPChallenger: httpCh,
		PollInterval:   quickPoll,
		MaxPolls:       200,
	})
	// Renewal pass exercised directly: this hits the "expiring" branch
	// without needing to coordinate goroutines + FakeClock.Advance.
	if err := c.runRenewalPass(ctx); err != nil {
		t.Fatalf("runRenewalPass: %v", err)
	}
	got, err := fs.Meta().GetACMECert(ctx, "mail.example.test")
	if err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
	if got.NotAfter.Sub(now) < 30*24*time.Hour {
		t.Fatalf("expected renewed cert with > 30d remaining, got NotAfter=%v (now=%v)", got.NotAfter, now)
	}
}

func TestOrderResume_AfterCrash(t *testing.T) {
	fs, _ := newFS(t)
	stub := newStubServer(t)
	httpCh := NewHTTPChallenger()
	stub.httpFetcher = httpFetcherForChallenger(t, httpCh)

	c := New(Options{
		DirectoryURL:   stub.directoryURL(),
		ContactEmail:   "ops@example.test",
		Store:          fs,
		Logger:         discardLogger(),
		Clock:          clock.NewReal(),
		HTTPChallenger: httpCh,
		PollInterval:   quickPoll,
		MaxPolls:       200,
	})
	// Drive a real registration first so c.account / c.signer are
	// populated; this mirrors the production flow where the supervisor
	// has already registered the account before the resume pass runs.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.Register(ctx, "ops@example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Now drive a partial order: hit newOrder so the stub assigns a
	// real OrderURL we can resume against, but do NOT complete it.
	dir, err := c.fetchDirectory(ctx)
	if err != nil {
		t.Fatalf("fetchDirectory: %v", err)
	}
	req := orderRequest{Identifiers: []orderIdentifier{{Type: "dns", Value: "mail.example.test"}}}
	var resp orderResponse
	raw, err := c.post(ctx, c.signer, c.account.KID, dir.NewOrder, req, &resp)
	if err != nil {
		t.Fatalf("newOrder: %v", err)
	}
	row := store.ACMEOrder{
		AccountID:     c.account.ID,
		Hostnames:     []string{"mail.example.test"},
		Status:        store.ACMEOrderStatusProcessing,
		OrderURL:      raw.Location,
		FinalizeURL:   resp.Finalize,
		ChallengeType: store.ChallengeTypeHTTP01,
		UpdatedAt:     time.Now(),
	}
	row, err = fs.Meta().InsertACMEOrder(ctx, row)
	if err != nil {
		t.Fatalf("InsertACMEOrder: %v", err)
	}
	// Resume: the resume path reads the order back via POST-as-GET and
	// drives it to completion against the stub.
	if err := c.resumeInFlightOrders(ctx); err != nil {
		t.Fatalf("resumeInFlightOrders: %v", err)
	}
	updated, err := fs.Meta().GetACMEOrder(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetACMEOrder: %v", err)
	}
	if updated.Status != store.ACMEOrderStatusValid {
		t.Fatalf("status: got %s, want valid", updated.Status)
	}
}

func TestEnsureCert_AccountReuse(t *testing.T) {
	fs, _ := newFS(t)
	stub := newStubServer(t)
	httpCh := NewHTTPChallenger()
	stub.httpFetcher = httpFetcherForChallenger(t, httpCh)

	c := New(Options{
		DirectoryURL:   stub.directoryURL(),
		ContactEmail:   "ops@example.test",
		Store:          fs,
		Logger:         discardLogger(),
		Clock:          clock.NewReal(),
		HTTPChallenger: httpCh,
		PollInterval:   quickPoll,
		MaxPolls:       200,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeHTTP01, ""); err != nil {
		t.Fatalf("EnsureCert #1: %v", err)
	}
	if err := c.EnsureCert(ctx, []string{"mail2.example.test"}, store.ChallengeTypeHTTP01, ""); err != nil {
		t.Fatalf("EnsureCert #2: %v", err)
	}
	accts, err := fs.Meta().ListACMEAccounts(ctx)
	if err != nil {
		t.Fatalf("ListACMEAccounts: %v", err)
	}
	if len(accts) != 1 {
		t.Fatalf("expected exactly one account row, got %d", len(accts))
	}
}

func TestEnsureCert_5xx_Retries_ThenSucceeds(t *testing.T) {
	fs, _ := newFS(t)
	stub := newStubServer(t)
	httpCh := NewHTTPChallenger()
	stub.httpFetcher = httpFetcherForChallenger(t, httpCh)

	// Inject one 5xx on newOrder, then succeed on the second attempt.
	stub.failOrder = true
	// flipAfterFirst clears the failOrder flag after the stub processes
	// the first /new-order request. The transport runs the round-trip
	// first so the stub's failOrder check is read with the original
	// value; on success the flag is cleared so the next call succeeds.
	var flipped atomic.Bool
	transport := &flipAfterTransport{
		Base: http.DefaultTransport,
		After: func(r *http.Request, _ *http.Response) {
			if strings.HasSuffix(r.URL.Path, "/new-order") && !flipped.Swap(true) {
				stub.mu.Lock()
				stub.failOrder = false
				stub.mu.Unlock()
			}
		},
	}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	c := New(Options{
		DirectoryURL:   stub.directoryURL(),
		ContactEmail:   "ops@example.test",
		Store:          fs,
		Logger:         discardLogger(),
		Clock:          clock.NewReal(),
		HTTPClient:     httpClient,
		HTTPChallenger: httpCh,
		PollInterval:   quickPoll,
		MaxPolls:       200,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	// Per the current client, a 5xx newOrder is surfaced as a retryable
	// problem but post() does not auto-retry on serverInternal — it
	// retries only on badNonce. So the first EnsureCert returns an
	// error; a second call (after the stub has been flipped back by the
	// transport's first attempt-side-effect) succeeds. This is the
	// "retry by re-invocation" semantic described in
	// docs/requirements/05-tls-acme.md §retry budget.
	firstErr := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeHTTP01, "")
	if firstErr == nil {
		t.Fatalf("first EnsureCert: expected 5xx error, got nil")
	}
	// Second call: stub has had its failOrder flipped; should succeed.
	if err := c.EnsureCert(ctx, []string{"mail.example.test"}, store.ChallengeTypeHTTP01, ""); err != nil {
		t.Fatalf("second EnsureCert: %v", err)
	}
	if _, err := fs.Meta().GetACMECert(ctx, "mail.example.test"); err != nil {
		t.Fatalf("GetACMECert: %v", err)
	}
}

// flipAfterTransport runs After on every request once the round-trip
// completes, so tests can mutate stub state for the next request.
type flipAfterTransport struct {
	Base  http.RoundTripper
	After func(r *http.Request, resp *http.Response)
}

func (f *flipAfterTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f.Base.RoundTrip(r)
	f.After(r, resp)
	return resp, err
}
