package admin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/protoimg"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/snooze"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// StartOpts bundles optional StartServer knobs that have no home in
// sysconfig (test seams and runtime toggles).
type StartOpts struct {
	// Logger overrides the logger constructed from cfg.Observability. When
	// nil, observe.NewLogger is called.
	Logger *slog.Logger
	// Clock overrides the wall clock. When nil, clock.NewReal is used.
	Clock clock.Clock
	// Ready is closed once all listeners are bound and the server is ready
	// to accept traffic. Tests synchronise against it; production leaves it
	// nil and relies on sd_notify.
	Ready chan<- struct{}
	// ListenerAddrs, when non-nil, is populated with the resolved
	// net.Listener addresses keyed by listener name. Lets tests discover
	// the ephemeral port allocated by "127.0.0.1:0" binds.
	ListenerAddrs map[string]string
	// ListenerAddrsMu, when non-nil, guards ListenerAddrs. When nil the
	// caller must not read ListenerAddrs before Ready fires.
	ListenerAddrsMu *sync.Mutex
	// ExternalShutdown, when non-nil, replaces the default SIGTERM/SIGINT
	// handler registration so tests can drive shutdown explicitly.
	ExternalShutdown bool
}

// Runtime holds live handles so Reload and callers can inspect state.
type Runtime struct {
	mu     sync.Mutex
	cfg    atomic.Pointer[sysconfig.Config]
	level  *slog.LevelVar
	logger *slog.Logger
}

// StartServer is the whole-system boot path. It returns after ctx is
// cancelled and graceful shutdown has completed (or shutdown_grace has
// elapsed).
//
// The sequence matches docs/architecture/01-system-overview.md §Lifecycle:
// parse -> observability -> store -> auxiliary subsystems -> plugins ->
// TLS -> protocol servers -> listeners bind -> mark ready -> serve ->
// drain on cancel.
func StartServer(ctx context.Context, cfg *sysconfig.Config, opts StartOpts) error {
	if cfg == nil {
		return errors.New("admin: nil Config")
	}

	// Observability.
	levelVar := new(slog.LevelVar)
	levelVar.Set(parseSlogLevel(cfg.Observability.LogLevel))
	logger := opts.Logger
	if logger == nil {
		logger = observe.NewLogger(observe.ObservabilityConfig{
			LogFormat:    cfg.Observability.LogFormat,
			LogLevel:     cfg.Observability.LogLevel,
			MetricsBind:  cfg.Observability.MetricsBind,
			OTLPEndpoint: cfg.Observability.OTLPEndpoint,
		})
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "herold: startup",
		slog.String("hostname", cfg.Server.Hostname),
		slog.String("data_dir", cfg.Server.DataDir),
		slog.String("storage_backend", cfg.Server.Storage.Backend),
	)

	// Tracer (optional, off by default).
	tracer, traceShutdown, err := observe.NewTracer(ctx, cfg.Observability.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("admin: tracer: %w", err)
	}
	_ = tracer
	defer func() {
		if traceShutdown != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = traceShutdown(shutdownCtx)
		}
	}()

	clk := opts.Clock
	if clk == nil {
		clk = clock.NewReal()
	}

	// Store open + migrate.
	st, err := openStore(ctx, cfg, logger, clk)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "store close", slog.String("err", err.Error()))
		}
	}()

	// Integrity check: trivial SELECT 1 equivalent via a cheap metadata read.
	if _, err := st.Meta().ListLocalDomains(ctx); err != nil {
		return fmt.Errorf("admin: store integrity check failed: %w", err)
	}

	// Plugin manager + plugins.
	pluginMgr := plugin.NewManager(plugin.ManagerOptions{
		Logger:        logger.With("subsystem", "plugin"),
		Clock:         clk,
		ServerVersion: "0.1.0",
	})
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pluginMgr.Shutdown(shutdownCtx)
	}()
	for _, pSpec := range cfg.Plugin {
		spec := plugin.Spec{
			Name:      pSpec.Name,
			Path:      pSpec.Path,
			Type:      plugin.PluginType(pSpec.Type),
			Lifecycle: plugin.Lifecycle(pSpec.Lifecycle),
		}
		// Resolve secret-bearing options through sysconfig.ResolveSecret so
		// $ENV and file:/path references expand before the plugin sees them.
		resolved, err := resolvePluginOptions(pSpec.Options)
		if err != nil {
			return fmt.Errorf("admin: plugin %q options: %w", pSpec.Name, err)
		}
		spec.Options = resolved
		if _, err := pluginMgr.Start(ctx, spec); err != nil {
			return fmt.Errorf("admin: start plugin %q: %w", pSpec.Name, err)
		}
	}

	// Directory + OIDC + mail-auth verifiers.
	dir := directory.New(st.Meta(), logger.With("subsystem", "directory"), clk, nil)
	// Bound the OIDC HTTP client: discovery and JWKS fetches against a
	// hung IdP must not stall the auth hot path. STANDARDS §5 "Deadlines
	// on every network call". Matches directoryoidc/rp_test.go fixtures.
	oidcHTTP := &http.Client{Timeout: 10 * time.Second}
	oidc := directoryoidc.New(st.Meta(), logger.With("subsystem", "oidc"), oidcHTTP, clk)
	resolver := mailauth.NewSystemResolver()
	dkim := maildkim.New(resolver, logger.With("subsystem", "dkim"), clk)
	spf := mailspf.New(resolver, clk)
	dmarc := maildmarc.New(resolver)
	arc := mailarc.New(resolver)

	// Spam classifier.
	spamClassifier := spam.New(pluginInvoker{mgr: pluginMgr}, logger.With("subsystem", "spam"), clk)
	spamPluginName := firstPluginOfType(cfg.Plugin, "spam")

	// Sieve interpreter.
	sieveInterp := sieve.NewInterpreter()

	// TLS store.
	tlsStore, err := buildTLSStore(cfg, logger)
	if err != nil {
		return err
	}

	// FTS worker: storefts.Index + TextExtractor + Worker.
	ftsIndex, err := storefts.New(filepath.Join(cfg.Server.DataDir, "fts"), logger.With("subsystem", "fts"), clk)
	if err != nil {
		return fmt.Errorf("admin: fts index: %w", err)
	}
	defer ftsIndex.Close()
	ftsWorker := storefts.NewWorker(
		ftsIndex,
		st,
		storefts.NewMailparseExtractor(),
		logger.With("subsystem", "fts"),
		clk,
		storefts.WorkerOptions{},
	)

	// Protocol servers.
	smtpServer, err := protosmtp.New(protosmtp.Config{
		Store:     st,
		Directory: dir,
		DKIM:      dkim,
		SPF:       spf,
		DMARC:     dmarc,
		ARC:       arc,
		Spam:      spamClassifier,
		Sieve:     sieveInterp,
		TLS:       tlsStore,
		Resolver:  resolver,
		Clock:     clk,
		Logger:    logger.With("subsystem", "smtp"),
		Options: protosmtp.Options{
			Hostname:      cfg.Server.Hostname,
			ShutdownGrace: cfg.Server.ShutdownGrace.AsDuration(),
		},
		SpamPluginName: spamPluginName,
	})
	if err != nil {
		return fmt.Errorf("admin: protosmtp: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace.AsDuration())
		defer cancel()
		_ = smtpServer.Close(shutdownCtx)
	}()

	imapServer := protoimap.NewServer(
		st,
		dir,
		tlsStore,
		clk,
		logger.With("subsystem", "imap"),
		nil, // PasswordLookup: SCRAM not in Phase 1 exit scope
		nil, // TokenVerifier: OIDC SASL not in Phase 1 exit scope
		protoimap.Options{
			ServerName: cfg.Server.Hostname,
		},
	)
	defer imapServer.Close()

	// Admin HTTP handler: the real protoadmin server. Options defaults
	// are applied inside NewServer; we pass only subsystem-level fields.
	health := observe.NewHealth()
	adminServer := protoadmin.NewServer(
		st,
		dir,
		oidc,
		logger.With("subsystem", "admin"),
		clk,
		protoadmin.Options{
			ServerVersion: "0.1.0",
			Health:        health,
		},
	)
	// Parent mux composition (Phase 2 Wave 2.4): the admin HTTP
	// listener serves both the REST surface (protoadmin under
	// /api/v1) and the operator web UI (protoui under /ui). We
	// chose composition over a protoadmin.Mount(prefix, h) method so
	// protoadmin stays focused on its REST API and the UI's
	// dependency on directory + store goes through its own
	// constructor. The two handlers are otherwise independent —
	// session cookies (UI) and Bearer keys (REST) live in disjoint
	// header/cookie namespaces, and the URL prefixes do not overlap.
	adminHandler := composeAdminAndUI(ctx, cfg, st, dir, oidc, clk, logger, adminServer.Handler())

	// Bind listeners.
	boundListeners, err := bindListeners(ctx, cfg, logger, tlsStore, smtpServer, imapServer, adminHandler, opts)
	if err != nil {
		return err
	}
	defer boundListeners.Close()

	// Lifecycle errgroup: every long-running goroutine (FTS worker,
	// metrics serve, every protocol listener serve) is registered here
	// so the StartServer ctx-cancel path waits for them on shutdown.
	// STANDARDS §5: no fire-and-forget goroutines on the lifecycle
	// surface. The group's ctx is derived from the StartServer ctx so
	// any goroutine returning a non-nil error cancels its peers.
	g, gctx := errgroup.WithContext(ctx)

	// Register Go runtime + process collectors against observe.Registry
	// so /metrics surfaces standard runtime metrics. Idempotent: a test
	// that bounces StartServer multiple times in one process keeps its
	// single registration. Lives here (not metrics-bind-gated) so
	// scrapes against an alternative endpoint (e.g. an admin sidecar)
	// still see them.
	observe.RegisterRuntimeCollectors()

	// FTS worker goroutine — registered on the lifecycle group so
	// shutdown drains it.
	g.Go(func() error {
		if err := ftsWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "fts worker exited", slog.String("err", err.Error()))
			return err
		}
		return nil
	})

	// Snooze wake-up worker — Phase 2 REQ-PROTO-49. Polls
	// Metadata.ListDueSnoozedMessages and clears the per-message
	// snooze pair atomically through Metadata.SetSnooze. Bounded by
	// the lifecycle errgroup so shutdown drains it.
	snoozeWorker := snooze.NewWorker(snooze.Options{
		Store:        st,
		Logger:       logger.With("subsystem", "snooze"),
		Clock:        clk,
		PollInterval: cfg.Server.Snooze.PollInterval.AsDuration(),
	})
	g.Go(func() error {
		if err := snoozeWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "snooze worker exited", slog.String("err", err.Error()))
			return err
		}
		return nil
	})

	// Metrics HTTP server. Bound here under the same errgroup so
	// shutdown drains it; bind failures degrade to a warn log (not
	// fatal — operators can run without a metrics endpoint) but a
	// post-bind serve error propagates and triggers shutdown.
	var metricsShutdown func() error
	if cfg.Observability.MetricsBind != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", observe.MetricsHandler())
		srv := &http.Server{
			Addr:    cfg.Observability.MetricsBind,
			Handler: mux,
		}
		ln, lerr := net.Listen("tcp", cfg.Observability.MetricsBind)
		if lerr != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "metrics listen failed",
				slog.String("bind", cfg.Observability.MetricsBind),
				slog.String("err", lerr.Error()))
		} else {
			g.Go(func() error {
				if err := srv.Serve(ln); err != nil &&
					!errors.Is(err, http.ErrServerClosed) &&
					!errors.Is(err, net.ErrClosed) {
					logger.LogAttrs(context.Background(), slog.LevelWarn,
						"metrics listener exited",
						slog.String("err", err.Error()))
					return err
				}
				return nil
			})
			metricsShutdown = func() error {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				return srv.Shutdown(shutdownCtx)
			}
		}
	}

	// Protocol listener serve goroutines, all under the same lifecycle
	// group. A bind failure on one listener cancels gctx and all peers
	// drain.
	for _, ns := range boundListeners.serveFns {
		ns := ns
		g.Go(func() error {
			err := ns.fn(gctx)
			if err == nil ||
				errors.Is(err, http.ErrServerClosed) ||
				errors.Is(err, net.ErrClosed) ||
				errors.Is(err, context.Canceled) {
				return nil
			}
			logger.LogAttrs(context.Background(), slog.LevelError,
				"listener serve exited",
				slog.String("name", ns.name),
				slog.String("err", err.Error()))
			return err
		})
	}

	// Runtime snapshot for Reload.
	rt := &Runtime{level: levelVar, logger: logger}
	rt.cfg.Store(cfg)

	// Readiness.
	health.MarkReady()
	if opts.Ready != nil {
		close(opts.Ready)
	}
	notifySystemdReady(logger)

	logger.LogAttrs(ctx, slog.LevelInfo, "herold: ready")

	// SIGHUP -> reload.
	hupCh := make(chan os.Signal, 1)
	if !opts.ExternalShutdown {
		signal.Notify(hupCh, syscall.SIGHUP)
		defer signal.Stop(hupCh)
	}

	// Serve until ctx cancels or any registered goroutine fails. The
	// group's ctx (gctx) cancels in either case so all peers are
	// notified.
	groupErr := make(chan error, 1)
	go func() { groupErr <- g.Wait() }()

	drain := func() error {
		// Tear down listeners so the SMTP / IMAP / admin Serve loops
		// observe net.ErrClosed and unwind. The deferred
		// boundListeners.Close runs again on return; double-close on a
		// net.Listener is a no-op error that we already discard.
		boundListeners.Close()
		// Stop the protocol servers' inner accept loops — Server.Close
		// cancels their ctx and waits up to ShutdownGrace for sessions
		// to drain. We invoke these explicitly here (in addition to
		// the deferred per-server Close at the top of StartServer) so
		// the errgroup sees the Serve goroutines return before
		// g.Wait() does.
		grace := cfg.Server.ShutdownGrace.AsDuration()
		if grace <= 0 {
			grace = 10 * time.Second
		}
		shutCtx, cancelShut := context.WithTimeout(context.Background(), grace)
		defer cancelShut()
		_ = smtpServer.Close(shutCtx)
		_ = imapServer.Close()
		// Flip the metrics server into shutdown so its goroutine
		// returns.
		if metricsShutdown != nil {
			_ = metricsShutdown()
		}
		select {
		case err := <-groupErr:
			return err
		case <-time.After(grace):
			logger.LogAttrs(context.Background(), slog.LevelWarn,
				"shutdown drain window elapsed; some goroutines did not exit",
				slog.Duration("grace", grace))
			return nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			logger.LogAttrs(context.Background(), slog.LevelInfo, "herold: shutdown signal received")
			return drain()
		case err := <-groupErr:
			// A goroutine failed before the user-driven shutdown. Log
			// and surface the error; defers handle the rest.
			if err != nil {
				logger.LogAttrs(context.Background(), slog.LevelError,
					"herold: lifecycle goroutine failed",
					slog.String("err", err.Error()))
			}
			return err
		case <-hupCh:
			newCfg, err := sysconfig.Load(currentConfigPath())
			if err != nil {
				logger.LogAttrs(ctx, slog.LevelWarn, "reload: parse failed", slog.String("err", err.Error()))
				continue
			}
			if err := ReloadConfig(ctx, rt, newCfg); err != nil {
				logger.LogAttrs(ctx, slog.LevelWarn, "reload: apply failed", slog.String("err", err.Error()))
				continue
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "reload: applied")
		}
	}
}

// ReloadConfig diffs old vs new via sysconfig.Diff and applies the changes
// that can be applied live. Reload of data_dir / run_as_user / run_as_group
// is rejected with a clear error; the caller keeps running on the old cfg.
func ReloadConfig(ctx context.Context, rt *Runtime, newCfg *sysconfig.Config) error {
	if rt == nil {
		return errors.New("admin: nil runtime")
	}
	oldCfg := rt.cfg.Load()
	if oldCfg == nil {
		return errors.New("admin: no previous config to diff against")
	}
	changes, err := sysconfig.Diff(oldCfg, newCfg)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, c := range changes {
		switch c.Path {
		case "observability":
			if rt.level != nil {
				rt.level.Set(parseSlogLevel(newCfg.Observability.LogLevel))
			}
		}
	}
	rt.cfg.Store(newCfg)
	_ = ctx
	return nil
}

// currentConfigPath is populated by cmd/herold at process start so SIGHUP
// can re-read the same file. A package-local global is the simplest
// plumbing; only the CLI writes it.
var currentCfgPath atomic.Pointer[string]

// SetConfigPath records the file path the root command parsed so SIGHUP
// can reopen the same file.
func SetConfigPath(path string) {
	s := path
	currentCfgPath.Store(&s)
}

func currentConfigPath() string {
	if p := currentCfgPath.Load(); p != nil {
		return *p
	}
	return ""
}

func openStore(ctx context.Context, cfg *sysconfig.Config, logger *slog.Logger, clk clock.Clock) (store.Store, error) {
	switch cfg.Server.Storage.Backend {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(cfg.Server.Storage.SQLite.Path), 0o750); err != nil {
			return nil, fmt.Errorf("admin: create sqlite dir: %w", err)
		}
		return storesqlite.Open(ctx, cfg.Server.Storage.SQLite.Path, logger.With("subsystem", "store"), clk)
	case "postgres":
		blobDir := cfg.Server.Storage.Postgres.BlobDir
		if blobDir == "" {
			blobDir = filepath.Join(cfg.Server.DataDir, "blobs")
		}
		if err := os.MkdirAll(blobDir, 0o750); err != nil {
			return nil, fmt.Errorf("admin: create blob dir: %w", err)
		}
		return storepg.Open(ctx, cfg.Server.Storage.Postgres.DSN, blobDir, logger.With("subsystem", "store"), clk)
	default:
		return nil, fmt.Errorf("admin: unknown storage backend %q", cfg.Server.Storage.Backend)
	}
}

func buildTLSStore(cfg *sysconfig.Config, logger *slog.Logger) (*heroldtls.Store, error) {
	store := heroldtls.NewStore()
	var fallback *tls.Certificate
	// Admin TLS: becomes the fallback cert.
	if cfg.Server.AdminTLS.Source == "file" {
		cert, err := heroldtls.LoadFromFile(cfg.Server.AdminTLS.CertFile, cfg.Server.AdminTLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("admin: admin_tls load: %w", err)
		}
		fallback = cert
		store.SetDefault(cert)
	}
	// Per-listener file-backed certs.
	for _, l := range cfg.Listener {
		if l.CertFile == "" {
			continue
		}
		cert, err := heroldtls.LoadFromFile(l.CertFile, l.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("admin: listener %q cert: %w", l.Name, err)
		}
		store.Add(cfg.Server.Hostname, cert)
		if fallback == nil {
			store.SetDefault(cert)
			fallback = cert
		}
	}
	_ = logger
	return store, nil
}

// listenerServeFn is the shape of one bound listener's serve loop. It
// runs until the supplied ctx cancels or the listener fails; the
// returned error is propagated through the StartServer errgroup.
type listenerServeFn func(ctx context.Context) error

// boundListeners tracks the net.Listener instances bound by StartServer
// so a deferred Close can tear them down. The serveFns slice carries
// one closure per listener; StartServer launches them under an errgroup.
type boundListenerSet struct {
	listeners []net.Listener
	serveFns  []namedServe
	logger    *slog.Logger
}

type namedServe struct {
	name string
	fn   listenerServeFn
}

func (b *boundListenerSet) Close() {
	for _, ln := range b.listeners {
		_ = ln.Close()
	}
}

func bindListeners(
	ctx context.Context,
	cfg *sysconfig.Config,
	logger *slog.Logger,
	tlsStore *heroldtls.Store,
	smtpServer *protosmtp.Server,
	imapServer *protoimap.Server,
	adminHandler http.Handler,
	opts StartOpts,
) (*boundListenerSet, error) {
	set := &boundListenerSet{logger: logger}
	// Bind admin listener last per REQ-OPS lifecycle.
	var adminBinds []sysconfig.ListenerConfig
	for _, l := range cfg.Listener {
		if l.Protocol == "admin" {
			adminBinds = append(adminBinds, l)
			continue
		}
		ln, fn, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, adminHandler, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		set.listeners = append(set.listeners, ln)
		set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fn})
	}
	for _, l := range adminBinds {
		ln, fn, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, adminHandler, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		set.listeners = append(set.listeners, ln)
		set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fn})
	}
	return set, nil
}

func bindOne(
	ctx context.Context,
	cfg *sysconfig.Config,
	logger *slog.Logger,
	l sysconfig.ListenerConfig,
	tlsStore *heroldtls.Store,
	smtpServer *protosmtp.Server,
	imapServer *protoimap.Server,
	adminHandler http.Handler,
	opts StartOpts,
) (net.Listener, listenerServeFn, error) {
	ln, err := net.Listen("tcp", l.Address)
	if err != nil {
		return nil, nil, fmt.Errorf("admin: listen %s (%s): %w", l.Name, l.Address, err)
	}
	if opts.ListenerAddrs != nil {
		addr := ln.Addr().String()
		if opts.ListenerAddrsMu != nil {
			opts.ListenerAddrsMu.Lock()
			opts.ListenerAddrs[l.Name] = addr
			opts.ListenerAddrsMu.Unlock()
		} else {
			opts.ListenerAddrs[l.Name] = addr
		}
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "listener bound",
		slog.String("name", l.Name),
		slog.String("protocol", l.Protocol),
		slog.String("tls", l.TLS),
		slog.String("addr", ln.Addr().String()),
	)
	switch l.Protocol {
	case "smtp":
		return ln, func(ctx context.Context) error {
			return smtpServer.Serve(ctx, ln, protosmtp.RelayIn)
		}, nil
	case "smtp-submission":
		mode := protosmtp.SubmissionSTARTTLS
		if l.TLS == "implicit" {
			mode = protosmtp.SubmissionImplicitTLS
		}
		return ln, func(ctx context.Context) error {
			return smtpServer.Serve(ctx, ln, mode)
		}, nil
	case "imap":
		mode := protoimap.ListenerModeSTARTTLS
		if l.TLS == "implicit" {
			mode = protoimap.ListenerModeImplicit993
		}
		return ln, func(ctx context.Context) error {
			return imapServer.Serve(ctx, ln, mode)
		}, nil
	case "imaps":
		return ln, func(ctx context.Context) error {
			return imapServer.Serve(ctx, ln, protoimap.ListenerModeImplicit993)
		}, nil
	case "admin":
		spec := l
		return ln, func(ctx context.Context) error {
			return serveAdmin(ctx, ln, spec, tlsStore, adminHandler, logger)
		}, nil
	default:
		_ = ln.Close()
		_ = cfg
		return nil, nil, fmt.Errorf("admin: unknown listener protocol %q", l.Protocol)
	}
}

// serveAdmin runs one admin HTTP server until ctx cancels or Serve
// returns. Returns nil for the canonical http.ErrServerClosed and
// net.ErrClosed conditions; any other error is propagated to the
// errgroup so StartServer logs it and triggers shutdown.
func serveAdmin(
	ctx context.Context,
	ln net.Listener,
	l sysconfig.ListenerConfig,
	tlsStore *heroldtls.Store,
	handler http.Handler,
	logger *slog.Logger,
) error {
	srv := &http.Server{
		Handler:     handler,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	if l.TLS == "implicit" {
		srv.TLSConfig = heroldtls.TLSConfig(tlsStore, heroldtls.Intermediate, nil)
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	var err error
	if l.TLS == "implicit" {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "admin listener exited", slog.String("err", err.Error()))
	}
	return err
}

// pluginInvoker adapts *plugin.Manager to spam.PluginInvoker.
type pluginInvoker struct {
	mgr *plugin.Manager
}

// Call implements spam.PluginInvoker by dispatching to the named plugin.
func (p pluginInvoker) Call(ctx context.Context, pluginName, method string, params any, result any) error {
	pl := p.mgr.Get(pluginName)
	if pl == nil {
		return fmt.Errorf("admin: plugin %q not registered", pluginName)
	}
	return pl.Call(ctx, method, params, result)
}

func firstPluginOfType(plugins []sysconfig.PluginConfig, kind string) string {
	for _, p := range plugins {
		if p.Type == kind {
			return p.Name
		}
	}
	return ""
}

// resolvePluginOptions expands any options value that starts with "$" or
// "file:" through sysconfig.ResolveSecret. Other values are passed through
// unchanged so typical scalar options (endpoints, model names) survive.
func resolvePluginOptions(in map[string]string) (map[string]any, error) {
	if len(in) == 0 {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		// api_key_env is the convention the spam-llm plugin uses: the value
		// names the environment variable holding the key. We resolve "$VAR"
		// forms here so plugins see the secret verbatim without the indirection.
		if strings.HasPrefix(v, "$") || strings.HasPrefix(v, "file:") {
			resolved, err := sysconfig.ResolveSecret(v)
			if err != nil {
				return nil, fmt.Errorf("option %q: %w", k, err)
			}
			out[k] = resolved
			continue
		}
		out[k] = v
	}
	return out, nil
}

// parseSlogLevel translates a sysconfig log-level string to slog.Level.
func parseSlogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// composeAdminAndUI returns an http.Handler that routes /api/v1/...
// and /healthz/... to the protoadmin handler, the configured UI path
// prefix to a freshly constructed protoui server, and (when image
// proxy is enabled) /proxy/image to a protoimg.Server reusing the
// UI's session for authentication. When the UI is disabled
// (cfg.Server.UI.Enabled == false) the returned handler is the
// protoadmin handler verbatim — the image proxy depends on the
// suite session and so degrades with it.
//
// Mount mode (b) per the Wave 2.4 ticket: composition over a parent
// mux. We deliberately avoid extending protoadmin with a Mount
// method; each surface stays independent and a future fourth surface
// (REQ-HOOK Part B) can join without touching any of them.
func composeAdminAndUI(
	ctx context.Context,
	cfg *sysconfig.Config,
	st store.Store,
	dir *directory.Directory,
	oidcRP *directoryoidc.RP,
	clk clock.Clock,
	logger *slog.Logger,
	adminHandler http.Handler,
) http.Handler {
	if cfg.Server.UI.Enabled != nil && !*cfg.Server.UI.Enabled {
		return adminHandler
	}
	prefix := cfg.Server.UI.PathPrefix
	if prefix == "" {
		prefix = "/ui"
	}
	signingKey := []byte{}
	if env := cfg.Server.UI.SigningKeyEnv; env != "" {
		if v := os.Getenv(env); v != "" {
			signingKey = []byte(v)
		}
	}
	secure := true
	if cfg.Server.UI.SecureCookies != nil {
		secure = *cfg.Server.UI.SecureCookies
	}
	uiSrv, err := protoui.NewServer(st, dir, oidcRP, clk, protoui.Options{
		PathPrefix: prefix,
		Logger:     logger.With("subsystem", "ui"),
		Session: protoui.SessionConfig{
			SigningKey:     signingKey,
			CookieName:     cfg.Server.UI.CookieName,
			CSRFCookieName: cfg.Server.UI.CSRFCookieName,
			TTL:            cfg.Server.UI.SessionTTL.AsDuration(),
			SecureCookies:  secure,
		},
	})
	if err != nil {
		// Falling back to admin-only is the least-surprising
		// behaviour; we log loudly so the operator sees the
		// degradation.
		logger.LogAttrs(ctx, slog.LevelError, "protoui: construct failed; UI disabled",
			slog.String("err", err.Error()))
		return adminHandler
	}
	parent := http.NewServeMux()
	parent.Handle("/api/v1/", adminHandler)
	parent.Handle(prefix+"/", uiSrv.Handler())

	// Image proxy (REQ-SEND-70..78). Mounted only when enabled in
	// sysconfig; uses the UI session for authentication so a browser
	// already logged into /ui can render upstream-tracking-free
	// images without a separate auth dance.
	if cfg.Server.ImageProxy.Enabled == nil || *cfg.Server.ImageProxy.Enabled {
		ipCfg := cfg.Server.ImageProxy
		imgSrv := protoimg.New(protoimg.Options{
			Logger:              logger.With("subsystem", "protoimg"),
			Clock:               clk,
			MaxBytes:            ipCfg.MaxBytes,
			CacheMaxBytes:       ipCfg.CacheMaxBytes,
			CacheMaxEntries:     ipCfg.CacheMaxEntries,
			CacheMaxAge:         time.Duration(ipCfg.CacheMaxAgeSeconds) * time.Second,
			PerUserPerMin:       ipCfg.PerUserPerMinute,
			PerUserOriginPerMin: ipCfg.PerUserOriginPerMinute,
			PerUserConcurrent:   ipCfg.PerUserConcurrent,
			SessionResolver:     uiSrv.ResolveSession,
		})
		parent.Handle("/proxy/image", imgSrv.Handler())
	}

	// Bare `/` and unknown roots: send a browser hitting the admin
	// host directly to the UI login. API consumers never request `/`,
	// so this hop only affects operators using a browser.
	parent.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
			return
		}
		http.NotFound(w, r)
	})
	return parent
}

// notifySystemdReady implements a minimal sd_notify(READY=1) compatible
// with systemd Type=notify without pulling in the coreos/go-systemd
// dependency. If NOTIFY_SOCKET is unset (development, container without
// systemd, tests) this is a no-op.
func notifySystemdReady(logger *slog.Logger) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return
	}
	addr := &net.UnixAddr{Name: sock, Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		logger.Warn("sd_notify: dial", "err", err.Error())
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("READY=1\n")); err != nil {
		logger.Warn("sd_notify: write", "err", err.Error())
	}
}
