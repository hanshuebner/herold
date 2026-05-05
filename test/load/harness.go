package load

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
	"github.com/hanshuebner/herold/internal/testharness/fakeplugin"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// Harness is the per-scenario load-test harness.  It embeds a live
// testharness.Server with SMTP + IMAP listeners and exposes helpers
// used by individual scenarios.
type Harness struct {
	// HA is the underlying test harness (store, clock, DNS, plugins).
	HA *testharness.Server
	// SMTPAddr is the TCP address of the SMTP relay-in listener.
	SMTPAddr string
	// IMAPSAddr is the TCP address of the implicit-TLS IMAPS listener.
	IMAPSAddr string
	// Domain is the test domain used for all principals.
	Domain string
	// Backend is "sqlite" or "postgres".
	Backend string
	// TLSClient is a *tls.Config suitable for connecting to the IMAPS listener.
	TLSClient *tls.Config
	// Dir is the testharness principal's directory.
	Dir *directory.Directory
	// RunsDir is the absolute path where pprof profiles are written.
	RunsDir string

	t           testing.TB
	smtpSrv     *protosmtp.Server
	imapSrv     *protoimap.Server
	pprofDir    string
	pprofCancel func()
}

// HarnessOpts configures newHarness.
type HarnessOpts struct {
	// Store provides the backing store.Store.  Required.
	Store store.Store
	// Backend is the human label ("sqlite" or "postgres").
	Backend string
	// Domain is the domain used for test principals.  Defaults to "load.test".
	Domain string
	// RunsDir is the directory where profiles are written.  Defaults to
	// test/load/runs/<timestamp>/ relative to the repo root.
	RunsDir string
	// MaxSMTPConns limits simultaneous SMTP sessions.  Default 1024.
	MaxSMTPConns int
	// MaxIMAPConns limits simultaneous IMAP sessions.  Default 2048.
	MaxIMAPConns int
	// EnablePprof, when true, starts CPU + block profiling immediately.
	EnablePprof bool
}

// newHarness starts a fully-wired in-process server suitable for load
// tests.  The caller must call h.Close() (or use t.Cleanup) to shut it
// down.  All resources are scoped to t.
func newHarness(t testing.TB, opts HarnessOpts) *Harness {
	t.Helper()
	if opts.Domain == "" {
		opts.Domain = "load.test"
	}
	if opts.MaxSMTPConns <= 0 {
		opts.MaxSMTPConns = 1024
	}
	if opts.MaxIMAPConns <= 0 {
		opts.MaxIMAPConns = 2048
	}

	hOpts := testharness.Options{
		Store: opts.Store,
		// Real clock: load tests measure wall-time latency.
		Clock: clock.NewReal(),
		Listeners: []testharness.ListenerSpec{
			{Name: "smtp", Protocol: "smtp"},
			{Name: "imaps", Protocol: "imaps"},
		},
	}
	ha, _ := testharness.Start(t, hOpts)

	ctx := context.Background()
	// Insert domain.
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: opts.Domain, IsLocal: true}); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("load harness: insert domain %s: %v", opts.Domain, err)
		}
	}

	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)

	tlsStore, clientCfg := loadTLSStore(t, opts.Domain)

	// Spam plugin: always ham (not the concern of load tests).
	spamPlug := fakeplugin.New("spam", "spam")
	spamPlug.Handle("spam.classify", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.0}`), nil
	})
	ha.RegisterPlugin("spam", spamPlug)
	invoker := &pluginInvoker{reg: ha.Plugins}
	spamCls := spam.New(invoker, ha.Logger, ha.Clock)

	resolver := fakednsAdapter{ha.DNS}
	dkimV := maildkim.New(resolver, ha.Logger, ha.Clock)
	spfV := mailspf.New(resolver, ha.Clock)
	dmarcV := maildmarc.New(resolver)
	arcV := mailarc.New(resolver)
	interp := sieve.NewInterpreter()

	smtpSrv, err := protosmtp.New(protosmtp.Config{
		Store:     ha.Store,
		Directory: dir,
		DKIM:      dkimV,
		SPF:       spfV,
		DMARC:     dmarcV,
		ARC:       arcV,
		Spam:      spamCls,
		Sieve:     interp,
		TLS:       tlsStore,
		Resolver:  resolver,
		Clock:     ha.Clock,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Options: protosmtp.Options{
			Hostname:                 "mx." + opts.Domain,
			AuthservID:               "mx." + opts.Domain,
			MaxMessageSize:           1 * 1024 * 1024,
			ReadTimeout:              30 * time.Second,
			WriteTimeout:             30 * time.Second,
			DataTimeout:              60 * time.Second,
			ShutdownGrace:            5 * time.Second,
			MaxRecipientsPerMessage:  4,
			MaxCommandsPerSession:    200,
			MaxConcurrentConnections: opts.MaxSMTPConns,
			MaxConcurrentPerIP:       opts.MaxSMTPConns, // load clients all come from 127.0.0.1
		},
	})
	if err != nil {
		t.Fatalf("load harness: protosmtp.New: %v", err)
	}
	t.Cleanup(func() { _ = smtpSrv.Close(context.Background()) })
	ha.AttachSMTP("smtp", smtpSrv, protosmtp.RelayIn)

	imapSrv := protoimap.NewServer(
		ha.Store, dir, tlsStore, ha.Clock,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		nil, nil,
		protoimap.Options{
			MaxConnections:        opts.MaxIMAPConns,
			MaxConnectionsPerIP:   opts.MaxIMAPConns,
			MaxCommandsPerSession: 10000,
			IdleMaxDuration:       10 * time.Minute,
			ServerName:            "herold-load",
		},
	)
	t.Cleanup(func() { _ = imapSrv.Close() })
	ha.AttachIMAP("imaps", imapSrv, protoimap.ListenerModeImplicit993)

	smtpNetAddr, ok := ha.ListenerAddr("smtp")
	if !ok {
		t.Fatal("load harness: no smtp listener")
	}
	imapNetAddr, ok := ha.ListenerAddr("imaps")
	if !ok {
		t.Fatal("load harness: no imaps listener")
	}
	smtpAddr := smtpNetAddr.String()
	imapAddr := imapNetAddr.String()

	runsDir := opts.RunsDir
	if runsDir == "" {
		ts := time.Now().UTC().Format("20060102T150405Z")
		// Resolve relative to the file's directory is fragile; use CWD at runtime.
		// Scenarios/tests can override via HarnessOpts.RunsDir.
		runsDir = filepath.Join(os.TempDir(), "herold-load-runs", ts)
	}

	h := &Harness{
		HA:        ha,
		SMTPAddr:  smtpAddr,
		IMAPSAddr: imapAddr,
		Domain:    opts.Domain,
		Backend:   opts.Backend,
		TLSClient: clientCfg,
		Dir:       dir,
		RunsDir:   runsDir,
		t:         t,
		smtpSrv:   smtpSrv,
		imapSrv:   imapSrv,
	}

	if opts.EnablePprof {
		pprofDir := filepath.Join(runsDir, "pprof")
		if err := os.MkdirAll(pprofDir, 0o755); err != nil {
			t.Logf("load harness: pprof mkdir: %v", err)
		} else {
			h.pprofDir = pprofDir
			h.startPprof(pprofDir)
		}
	}

	return h
}

// CreatePrincipal creates a mailbox principal in the harness domain and
// seeds an INBOX mailbox.  Returns the PrincipalID.
func (h *Harness) CreatePrincipal(ctx context.Context, localpart, password string) (store.PrincipalID, error) {
	email := localpart + "@" + h.Domain
	pid, err := h.Dir.CreatePrincipal(ctx, email, password)
	if err != nil {
		return 0, fmt.Errorf("create principal %s: %w", email, err)
	}
	_, err = h.HA.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox | store.MailboxAttrSubscribed,
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return 0, fmt.Errorf("insert inbox for %s: %w", email, err)
	}
	return pid, nil
}

// startPprof starts CPU and block profiling, writing to pprofDir.
// The profiles are stopped when h.stopPprof is called.
func (h *Harness) startPprof(pprofDir string) {
	cpuF, err := os.Create(filepath.Join(pprofDir, "cpu.pprof"))
	if err != nil {
		h.t.Logf("pprof cpu create: %v", err)
		return
	}
	if err := pprof.StartCPUProfile(cpuF); err != nil {
		_ = cpuF.Close()
		h.t.Logf("pprof start cpu: %v", err)
		return
	}

	// Block profiling at maximum rate.
	runtime_SetBlockProfileRate(1)

	h.pprofCancel = func() {
		pprof.StopCPUProfile()
		_ = cpuF.Close()

		// Heap, goroutine, block.
		for _, kind := range []string{"heap", "goroutine", "block"} {
			p := pprof.Lookup(kind)
			if p == nil {
				continue
			}
			f, err := os.Create(filepath.Join(pprofDir, kind+".pprof"))
			if err != nil {
				continue
			}
			_ = p.WriteTo(f, 0)
			_ = f.Close()
		}
		runtime_SetBlockProfileRate(0)
	}
}

// StopPprof finalises and closes all profiling handles.  Safe to call
// even when pprof was not started; idempotent.
func (h *Harness) StopPprof() string {
	if h.pprofCancel != nil {
		h.pprofCancel()
		h.pprofCancel = nil
	}
	return h.pprofDir
}

// PprofHandler returns an http.Handler that serves pprof data from the
// live server — useful when pointing a browser at a long-running load test.
func (h *Harness) PprofHandler() http.Handler {
	return http.DefaultServeMux
}

// ---- internal helpers --------------------------------------------------

func loadTLSStore(t testing.TB, domain string) (*heroldtls.Store, *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("load tls gen key: %v", err)
	}
	host := "mx." + domain
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("load tls cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	leaf, _ := x509.ParseCertificate(der)
	cert.Leaf = leaf
	st := heroldtls.NewStore()
	st.SetDefault(&cert)
	st.Add(host, &cert)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return st, &tls.Config{RootCAs: pool, ServerName: host}
}

// pluginInvoker adapts fakeplugin.Registry to spam.Classifier's invoker
// interface (mirrors the implementation in test/e2e/fixtures).
type pluginInvoker struct{ reg *fakeplugin.Registry }

func (f *pluginInvoker) Call(ctx context.Context, plugin, method string, params any, result any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	raw, err := f.reg.Call(ctx, plugin, method, paramsJSON)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}

// fakednsAdapter wires fakedns.Resolver into mailauth.Resolver.
type fakednsAdapter struct{ d *fakedns.Resolver }

func (a fakednsAdapter) TXTLookup(ctx context.Context, name string) ([]string, error) {
	out, err := a.d.LookupTXT(ctx, name)
	if err != nil {
		if errors.Is(err, fakedns.ErrNoRecords) {
			return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, name)
		}
		return nil, err
	}
	return out, nil
}

func (a fakednsAdapter) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	mxs, err := a.d.LookupMX(ctx, domain)
	if err != nil {
		if errors.Is(err, fakedns.ErrNoRecords) {
			return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, domain)
		}
		return nil, err
	}
	out := make([]*net.MX, 0, len(mxs))
	for _, m := range mxs {
		out = append(out, &net.MX{Host: m.Host, Pref: m.Preference})
	}
	return out, nil
}

func (a fakednsAdapter) IPLookup(ctx context.Context, host string) ([]net.IP, error) {
	v4, err4 := a.d.LookupA(ctx, host)
	v6, err6 := a.d.LookupAAAA(ctx, host)
	if err4 != nil && err6 != nil {
		return nil, fmt.Errorf("%w: %s", mailauth.ErrNoRecords, host)
	}
	return append(append([]net.IP{}, v4...), v6...), nil
}
