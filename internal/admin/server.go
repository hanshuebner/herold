package admin

import (
	"context"
	"crypto/rand"
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

	"github.com/hanshuebner/herold/internal/acme"
	"github.com/hanshuebner/herold/internal/chatretention"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/mailspf"
	"github.com/hanshuebner/herold/internal/netguard"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protocall"
	"github.com/hanshuebner/herold/internal/protochat"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/protoimg"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/calendars/imip"
	"github.com/hanshuebner/herold/internal/protojmap/mail/emailsubmission"
	jmapidentity "github.com/hanshuebner/herold/internal/protojmap/mail/identity"
	jmappush "github.com/hanshuebner/herold/internal/protojmap/push"
	"github.com/hanshuebner/herold/internal/protosend"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/protoui"
	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/sesinbound"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/snooze"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storefts"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/tabardspa"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
	"github.com/hanshuebner/herold/internal/vapid"
	"github.com/hanshuebner/herold/internal/webpush"
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
// The sequence matches docs/design/architecture/01-system-overview.md §Lifecycle:
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

	// Health tracker: created early so the ACME wiring and protoadmin
	// server both share the same instance (REQ-OPS-111).
	health := observe.NewHealth()

	// ACME cert manager (REQ-OPS-40..55, REQ-OPS-111). Build client when
	// [acme] block is configured; gate health readiness on account load.
	var acmeClient *acme.Client
	var acmeHTTPChallenger *acme.HTTPChallenger
	var acmeTLSALPNChallenger *acme.TLSALPNChallenger
	observe.RegisterTLSCertMetrics()
	if cfg.Acme != nil {
		health.MarkACMERequired()
		acmeHTTPChallenger = acme.NewHTTPChallenger()
		acmeTLSALPNChallenger = acme.NewTLSALPNChallenger()
		// Wire the TLS-ALPN-01 challenger into the TLS store so the
		// production listener serves challenge certs during ACME validation
		// without a separate port (RFC 8737, REQ-OPS-50).
		tlsStore.SetALPNChallenger(acmeTLSALPNChallenger)

		directoryURL := cfg.Acme.DirectoryURL
		if directoryURL == "" {
			directoryURL = "https://acme-v02.api.letsencrypt.org/directory"
		}

		// pluginInvokerAdapter adapts *plugin.Manager to acme.PluginInvoker.
		acmeInvoker := acmePluginAdapter{mgr: pluginMgr}

		acmeClient = acme.New(acme.Options{
			DirectoryURL:      directoryURL,
			ContactEmail:      cfg.Acme.Email,
			Store:             st,
			TLSStore:          tlsStore,
			PluginInvoker:     acmeInvoker,
			Logger:            logger.With("subsystem", "acme"),
			Clock:             clk,
			HTTPChallenger:    acmeHTTPChallenger,
			TLSALPNChallenger: acmeTLSALPNChallenger,
		})

		// Initial cert provisioning for server.hostname (REQ-OPS-50).
		// Run in-line at startup so the server does not mark ready until
		// at least one cert is available (REQ-OPS-111).
		challengeType := parseChallengeType(cfg.Acme.ChallengeType)
		initCtx, initCancel := context.WithTimeout(ctx, 2*time.Minute)
		initErr := acmeClient.EnsureCert(initCtx, []string{cfg.Server.Hostname}, challengeType, cfg.Acme.DNSPlugin)
		initCancel()
		if initErr != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "acme: initial cert provisioning failed; will retry in renewal loop",
				slog.String("hostname", cfg.Server.Hostname),
				slog.String("err", initErr.Error()))
		} else {
			health.MarkACMEReady()
		}
	} else {
		health.MarkACMENotRequired()
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

	// REQ-DIR-RCPT-01..12: directory.resolve_rcpt RCPT-time hook. When
	// [smtp.inbound.directory_resolve_rcpt_plugin] is non-empty, the
	// SMTP server consults the named plugin at RCPT TO time before
	// emitting 250 / 4xx / 5xx. The breaker + rate limit are owned by
	// the resolver; the plugin manager satisfies the invoker
	// interface.
	rcptResolverInst, err := directory.NewRcptResolver(directory.RcptResolverConfig{
		Invoker:  pluginMgr,
		Clock:    clk,
		Logger:   logger.With("subsystem", "directory.resolve_rcpt"),
		Metadata: st.Meta(),
		Limiter:  directory.NewResolveRcptRateLimiter(clk, cfg.SMTP.Inbound.RcptRateLimitPerIPPerSec),
	})
	if err != nil {
		return fmt.Errorf("admin: directory.resolve_rcpt resolver: %w", err)
	}

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
		SpamPluginName:         spamPluginName,
		RcptResolver:           rcptResolverInst,
		RcptPluginName:         cfg.SMTP.Inbound.DirectoryResolveRcptPlugin,
		RcptPluginFirstDomains: cfg.SMTP.Inbound.PluginFirstForDomains,
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

	// Outbound queue construction (Phase 3 Wave 3.1.5). The queue
	// owns its scheduler / worker pool and is registered against the
	// lifecycle errgroup below so SIGTERM drains in-flight deliveries.
	// composeAdminAndUI receives the handle so JMAP EmailSubmission/set
	// and the HTTP send API enqueue through the same instance.
	//
	// TODO(3.1.5-coord): autodns.Reporter (TLS-RPT mailto emission
	// path) is not constructed in production today; when the reporter
	// is wired, pass outboundQ via ReporterOptions.Queue and start
	// RunDailyEmission on the lifecycle errgroup. Tracked in the
	// queue-delivery-implementor backlog; defer because constructing
	// the reporter also needs a RuaResolver, an HTTPDoer, and a
	// sysconfig knob for the operator's reporter contact / domain
	// — all out of scope for the wiring-only Wave 3.1.5.
	outboundQ, err := buildOutboundQueue(cfg, st, resolver, logger, clk)
	if err != nil {
		return fmt.Errorf("admin: outbound queue: %w", err)
	}
	// Wire the queue as the SMTP server's BouncePoster so the
	// REQ-FLOW-ATTPOL-02 post-acceptance walker can enqueue a 5.3.4
	// DSN to the original sender. The setter is called pre-listener-
	// bind below, so no in-flight session can race the assignment.
	smtpServer.SetBouncePoster(queueBouncePosterAdapter{q: outboundQ})
	// Wire the outbound queue as the SMTP submission-listener path
	// (Wave 3.1.6, REQ-FLOW-* + REQ-PROTO-42). Authenticated MUA-clients
	// on port 587 / 465 hand non-local recipients off to the same
	// queue.Submit shape JMAP EmailSubmission/set and the HTTP send API
	// already use post-3.1.5.
	smtpServer.SetSubmissionQueue(outboundQ)

	// Webhook dispatcher (Phase 3 Wave 3.5c-Z + Track A/C). Constructs
	// a process-local signing key for fetch URLs; persistent rotation
	// is a future-wave operator knob. The dispatcher's change-feed Run
	// loop and the synthetic-recipient direct-dispatch path share the
	// same instance; both are bounded by the lifecycle errgroup gctx
	// below.
	hookSigningKey := make([]byte, 32)
	if _, krErr := rand.Read(hookSigningKey); krErr != nil {
		return fmt.Errorf("admin: webhook signing key: %w", krErr)
	}
	webhookDispatcher := protowebhook.New(protowebhook.Options{
		Store:           st,
		Logger:          logger.With("subsystem", "protowebhook"),
		Clock:           clk,
		FetchURLBaseURL: "", // empty = inline-only deliveries; operators wire the admin URL when
		// the FetchHandler mounting lands. Until then extracted-mode + fetch_url-mode
		// subscriptions emit a build-payload error and the dispatcher logs at warn;
		// inline-mode subscriptions still POST.
		SigningKey: hookSigningKey,
	})
	smtpServer.SetWebhookDispatcher(syntheticDispatcherAdapter{d: webhookDispatcher})

	// Admin HTTP handler: the real protoadmin server. Options defaults
	// are applied inside NewServer; we pass only subsystem-level fields.
	// health was constructed before the ACME block above so the ACME gate
	// and protoadmin share the same instance (REQ-OPS-111).
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
	bundle, err := composeAdminAndUI(ctx, cfg, st, dir, oidc, clk, logger, ftsIndex, tlsStore, outboundQ, adminServer.Handler(), smtpServer)
	if err != nil {
		return err
	}
	suiteSrvs := bundle.srvs
	defer func() {
		// Best-effort cleanup if StartServer returns before the lifecycle
		// goroutines wire these into the errgroup; the errgroup-side
		// shutdown below is the primary drain.
		if suiteSrvs.callSrv != nil {
			_ = suiteSrvs.callSrv.Close()
		}
		if suiteSrvs.sendSrv != nil {
			_ = suiteSrvs.sendSrv.Close()
		}
	}()

	// Bind listeners.
	boundListeners, err := bindListeners(ctx, cfg, logger, tlsStore, smtpServer, imapServer, bundle, opts)
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

	// Suite-level server lifecycles (protocall reaper, protochat
	// connection drain). Wave 2.9.5 Track B closed the gap where
	// /chat/ws and /api/v1/call/credentials had unkillable background
	// goroutines: register their shutdown hooks against the errgroup so
	// gctx cancellation drains both before serveAdmin returns.
	if suiteSrvs.callSrv != nil {
		callSrv := suiteSrvs.callSrv
		g.Go(func() error {
			<-gctx.Done()
			return callSrv.Close()
		})
	}
	if suiteSrvs.sendSrv != nil {
		sendSrv := suiteSrvs.sendSrv
		g.Go(func() error {
			<-gctx.Done()
			return sendSrv.Close()
		})
	}
	if suiteSrvs.chatSrv != nil {
		chatSrv := suiteSrvs.chatSrv
		g.Go(func() error {
			<-gctx.Done()
			grace := cfg.Server.ShutdownGrace.AsDuration()
			if grace <= 0 {
				grace = 10 * time.Second
			}
			shutCtx, cancel := context.WithTimeout(context.Background(), grace)
			defer cancel()
			if err := chatSrv.Shutdown(shutCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.LogAttrs(context.Background(), slog.LevelWarn,
					"protochat shutdown",
					slog.String("err", err.Error()))
			}
			return nil
		})
	}

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

	// ACME lifecycle goroutines: HTTP-01 challenge listener + renewal loop.
	if acmeClient != nil {
		// HTTP-01 challenge listener on :80 (REQ-OPS-50). Serve ONLY the
		// ACME challenge path; all other paths return 404. The listener is
		// started only when challenge_type is http-01 (default).
		if cfg.Acme.ChallengeType == "" || cfg.Acme.ChallengeType == "http-01" {
			acmeLogger := logger.With("subsystem", "acme")
			http01Mux := http.NewServeMux()
			http01Mux.Handle("/.well-known/acme-challenge/", acmeHTTPChallenger.Handler())
			http01Srv := &http.Server{
				Addr:        ":80",
				Handler:     http01Mux,
				ReadTimeout: 15 * time.Second,
			}
			http01Ln, http01Err := net.Listen("tcp", ":80")
			if http01Err != nil {
				acmeLogger.LogAttrs(ctx, slog.LevelWarn,
					"acme: HTTP-01 listener bind failed; http-01 challenges will not be served",
					slog.String("err", http01Err.Error()))
			} else {
				g.Go(func() error {
					if err := http01Srv.Serve(http01Ln); err != nil &&
						!errors.Is(err, http.ErrServerClosed) &&
						!errors.Is(err, net.ErrClosed) {
						acmeLogger.LogAttrs(context.Background(), slog.LevelWarn,
							"acme http-01 listener exited", slog.String("err", err.Error()))
					}
					return nil
				})
				g.Go(func() error {
					<-gctx.Done()
					shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return http01Srv.Shutdown(shutCtx)
				})
			}
		}

		// Renewal loop (REQ-OPS-53). Ticks every hour; renews certs at
		// 1/3 remaining lifetime. A failed renewal is logged but does not
		// crash the server; the current cert stays in use until expiry.
		acmeLogger := logger.With("subsystem", "acme")
		g.Go(func() error {
			if err := acmeClient.RunRenewalLoop(gctx, time.Hour); err != nil &&
				!errors.Is(err, context.Canceled) {
				acmeLogger.LogAttrs(context.Background(), slog.LevelWarn,
					"acme renewal loop exited", slog.String("err", err.Error()))
			}
			return nil
		})

		// Cert-expiry metric housekeeping: update herold_tls_cert_expiry_seconds
		// on a 1-minute tick (REQ-OPS-91).
		g.Go(func() error {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-gctx.Done():
					return nil
				case <-ticker.C:
					updateCertExpiryMetrics(gctx, st, acmeLogger)
				}
			}
		})
	}

	// Outbound queue scheduler goroutine (Phase 3 Wave 3.1.5). The
	// queue.Run loop blocks until gctx cancels; ShutdownGrace bounds
	// the drain inside Run itself. STANDARDS §5: registered on the
	// lifecycle errgroup so SIGTERM waits for in-flight deliveries.
	queueLogger := logger.With("subsystem", "queue")
	g.Go(func() error {
		if err := outboundQ.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			queueLogger.LogAttrs(context.Background(), slog.LevelWarn, "queue run exited",
				slog.String("err", err.Error()))
			return err
		}
		return nil
	})

	// Webhook dispatcher scheduler (Phase 3 Wave 3.5c). The change-
	// feed-driven Run loop services principal-bound deliveries (the
	// existing Phase 2 path); the synthetic-recipient direct-dispatch
	// path shares the same Dispatcher's bounded goroutine pool and is
	// drained by the same gctx cancellation.
	hookLogger := logger.With("subsystem", "protowebhook")
	g.Go(func() error {
		if err := webhookDispatcher.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			hookLogger.LogAttrs(context.Background(), slog.LevelWarn, "webhook dispatcher run exited",
				slog.String("err", err.Error()))
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

	// Outbound Web Push dispatcher — Phase 3 Wave 3.8b
	// (REQ-PROTO-123 + 125 + 126). Drives the change-feed-driven
	// fan-out to PushSubscription rows. The dispatcher's Run loop
	// also handles RFC 8620 §7.2 verification ping outcomes (the
	// JMAP push handler fires the ping in a short-lived goroutine
	// off the JMAP request, but the destroy-on-410 path lives in
	// the dispatcher). Bounded by the lifecycle errgroup so
	// shutdown drains it.
	if disp := bundle.srvs.webpushDispatch; disp != nil {
		dispLogger := logger.With("subsystem", "webpush")
		enabled := cfg.Server.Push.DispatcherEnabled == nil || *cfg.Server.Push.DispatcherEnabled
		if enabled {
			g.Go(func() error {
				if err := disp.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
					dispLogger.LogAttrs(context.Background(), slog.LevelWarn,
						"webpush dispatcher exited",
						slog.String("err", err.Error()))
					return err
				}
				return nil
			})
		} else {
			dispLogger.Info("webpush: dispatcher disabled by config; verification ping path still active")
		}
	}

	// Chat retention sweeper — Phase 2 Wave 2.9.6 REQ-CHAT-92. Hard-
	// deletes chat_messages whose conversation override or owning
	// account default has expired the per-message retention window.
	// Bounded by the lifecycle errgroup so shutdown drains it.
	chatRetentionWorker := chatretention.NewWorker(chatretention.Options{
		Store:         st,
		Logger:        logger.With("subsystem", "chatretention"),
		Clock:         clk,
		SweepInterval: time.Duration(cfg.Server.Chat.Retention.SweepIntervalSeconds) * time.Second,
		BatchSize:     cfg.Server.Chat.Retention.BatchSize,
	})
	g.Go(func() error {
		if err := chatRetentionWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "chatretention worker exited", slog.String("err", err.Error()))
			return err
		}
		return nil
	})

	// iMIP intake worker (Phase 2 Wave 2.7 / REQ-PROTO-56). Reads the
	// global change feed for new EntityKindEmail rows, walks each
	// message's MIME tree for text/calendar parts, and applies the
	// scheduling METHOD (REQUEST / CANCEL / REPLY / COUNTER) to the
	// recipient's calendar. Bounded by the lifecycle errgroup so
	// shutdown drains it.
	imipWorker := imip.New(imip.Options{
		Store:  st,
		Logger: logger.With("subsystem", "imip"),
		Clock:  clk,
	})
	g.Go(func() error {
		if err := imipWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "imip worker exited", slog.String("err", err.Error()))
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
	// Admin TLS: file source loads immediately; acme source defers to the
	// ACME client which populates the store after account registration.
	switch cfg.Server.AdminTLS.Source {
	case "file":
		cert, err := heroldtls.LoadFromFile(cfg.Server.AdminTLS.CertFile, cfg.Server.AdminTLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("admin: admin_tls load: %w", err)
		}
		fallback = cert
		store.SetDefault(cert)
	case "acme":
		// Populated later by the ACME client. Log so the operator
		// knows the store starts empty and will be filled on first
		// cert issue.
		_ = logger // logger may be used for future trace; keep the reference.
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
	bundle composedHandlers,
	opts StartOpts,
) (*boundListenerSet, error) {
	set := &boundListenerSet{logger: logger}
	// Bind HTTP listeners last per REQ-OPS lifecycle.
	var adminBinds []sysconfig.ListenerConfig
	for _, l := range cfg.Listener {
		if l.Protocol == "admin" {
			adminBinds = append(adminBinds, l)
			continue
		}
		ln, fn, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, bundle, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		set.listeners = append(set.listeners, ln)
		set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fn})
	}
	for _, l := range adminBinds {
		ln, fn, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, bundle, opts)
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
	bundle composedHandlers,
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
		// Pick the handler matching the listener kind. Production
		// configs declare both kinds (validate enforces); dev_mode
		// allows a single un-kinded HTTP listener that co-mounts
		// public + admin behind a small composing mux per
		// REQ-OPS-ADMIN-LISTENER-01 dev escape.
		handler := pickHTTPHandler(cfg, l, bundle)
		return ln, func(ctx context.Context) error {
			return serveAdmin(ctx, ln, spec, tlsStore, handler, logger)
		}, nil
	default:
		_ = ln.Close()
		_ = cfg
		return nil, nil, fmt.Errorf("admin: unknown listener protocol %q", l.Protocol)
	}
}

// pickHTTPHandler returns the http.Handler appropriate to a single
// listener entry. Production configs (DevMode == false) declare an
// explicit Kind on every HTTP listener; the bundle's public handler
// is wired to the kind="public" listener and the admin handler to
// kind="admin". A listener that lands here without a Kind is
// dev_mode-only territory and gets a co-mount mux that dispatches
// admin paths to bundle.admin and everything else to bundle.public;
// the inner auth.RequireScope check is the security boundary in that
// shape.
func pickHTTPHandler(cfg *sysconfig.Config, l sysconfig.ListenerConfig, bundle composedHandlers) http.Handler {
	switch l.Kind {
	case sysconfig.ListenerKindPublic:
		if bundle.public != nil {
			return bundle.public
		}
		return bundle.admin
	case sysconfig.ListenerKindAdmin:
		if bundle.admin != nil {
			return bundle.admin
		}
		return bundle.public
	default:
		// Dev-mode co-mount: admin paths go to admin handler, every
		// other path goes to public handler. The split is along the
		// well-known admin namespaces so a /api/v1/admin REST hit
		// reaches the protoadmin server while a /jmap hit reaches
		// the JMAP server. This shape is documented as dev-only;
		// production deployments declare both Kinds.
		if bundle.admin == nil {
			return bundle.public
		}
		if bundle.public == nil {
			return bundle.admin
		}
		_ = cfg
		return coMountHandler(bundle.public, bundle.admin)
	}
}

// coMountHandler is the dev-mode-only fallback. Routes /api/v1/, /admin/,
// /metrics, and /ui/ to the admin handler; everything else to public.
// The auth.RequireScope check on the admin paths still enforces the
// scope boundary; coMountHandler is a routing convenience, not a
// security boundary.
func coMountHandler(public, admin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/"),
			strings.HasPrefix(r.URL.Path, "/admin/"),
			strings.HasPrefix(r.URL.Path, "/metrics"),
			strings.HasPrefix(r.URL.Path, "/ui/"),
			r.URL.Path == "/api/v1/principals" || strings.HasPrefix(r.URL.Path, "/api/v1/principals/"),
			r.URL.Path == "/api/v1/domains" || strings.HasPrefix(r.URL.Path, "/api/v1/domains/"),
			r.URL.Path == "/api/v1/aliases" || strings.HasPrefix(r.URL.Path, "/api/v1/aliases/"),
			r.URL.Path == "/api/v1/api-keys" || strings.HasPrefix(r.URL.Path, "/api/v1/api-keys/"),
			r.URL.Path == "/api/v1/audit",
			r.URL.Path == "/api/v1/queue" || strings.HasPrefix(r.URL.Path, "/api/v1/queue/"),
			r.URL.Path == "/api/v1/certs" || strings.HasPrefix(r.URL.Path, "/api/v1/certs/"),
			r.URL.Path == "/api/v1/spam/policy",
			r.URL.Path == "/api/v1/oidc/providers" || strings.HasPrefix(r.URL.Path, "/api/v1/oidc/providers/"),
			r.URL.Path == "/api/v1/oidc/callback",
			r.URL.Path == "/api/v1/server/status" || r.URL.Path == "/api/v1/server/config-check",
			r.URL.Path == "/api/v1/healthz/live" || r.URL.Path == "/api/v1/healthz/ready",
			r.URL.Path == "/api/v1/bootstrap",
			strings.HasPrefix(r.URL.Path, "/api/v1/webhooks"),
			strings.HasPrefix(r.URL.Path, "/api/v1/diag/"),
			strings.HasPrefix(r.URL.Path, "/api/v1/jobs/"),
			strings.HasPrefix(r.URL.Path, "/api/v1/mailboxes/"):
			admin.ServeHTTP(w, r)
		default:
			public.ServeHTTP(w, r)
		}
	})
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

// suiteServers gathers the long-lived server objects composeAdminAndUI
// constructs alongside the http handler. The caller owns their
// lifecycle; the admin errgroup ties their shutdown to gctx.
type suiteServers struct {
	callSrv         *protocall.Server
	chatSrv         *protochat.Server
	sendSrv         *protosend.Server
	webpushDispatch *webpush.Dispatcher
}

// composedHandlers is the bundle of HTTP handlers the bind path
// installs on each listener. When DevMode co-mounts public + admin on
// a single HTTP listener (Wave 3.6 dev escape) the binding code
// chains both via a single mux; production deployments install the
// public and admin handlers on disjoint listeners per
// REQ-OPS-ADMIN-LISTENER-01.
type composedHandlers struct {
	public http.Handler
	admin  http.Handler
	srvs   suiteServers
}

// composeAdminAndUI returns the listener-split bundle described in
// REQ-OPS-ADMIN-LISTENER-01..03 (Wave 3.6). The bundle's public
// handler serves JMAP, the HTTP send API, call credentials, image
// proxy, chat WS, webhook ingress, and the public /login flow; the
// admin handler serves protoadmin REST, the admin UI, /metrics, and
// the admin /login flow with TOTP step-up. The two handlers do NOT
// share routes: an admin path on the public listener returns 404,
// and a public path on the admin listener returns 404.
//
// In DevMode the binding code may install both handlers on a single
// listener via a small composing mux; the scope check in
// auth.RequireScope is the inner guard.
//
// The second return value carries the protocall and protochat
// server handles so StartServer can register their shutdown hooks
// against the lifecycle errgroup; both are nil when the corresponding
// feature is disabled in cfg.
func composeAdminAndUI(
	ctx context.Context,
	cfg *sysconfig.Config,
	st store.Store,
	dir *directory.Directory,
	oidcRP *directoryoidc.RP,
	clk clock.Clock,
	logger *slog.Logger,
	ftsIndex *storefts.Index,
	tlsStore *heroldtls.Store,
	outboundQ *queue.Queue,
	adminHandler http.Handler,
	smtpSrv *protosmtp.Server,
) (composedHandlers, error) {
	// ftsIndex is the chat-side full-text search backend (Wave 2.9.6
	// Track D, REQ-CHAT-80..82). It is the same Bleve index the mail
	// FTS worker writes to; the chat JMAP handlers, when registered
	// here, pass it to chat.RegisterWithFTS so Message/query routes
	// free-text filters through SearchChatMessages. The chat JMAP
	// surface is not yet mounted on the production HTTP listener
	// (Phase 2 Wave 2.9 covers WebSocket only); when the JMAP wiring
	// lands, ftsIndex is already in scope here.
	_ = ftsIndex
	var bundle composedHandlers
	prefix := cfg.Server.UI.PathPrefix
	if prefix == "" {
		prefix = "/ui"
	}

	// Build admin-listener UI server. The admin /login flow issues
	// cookies carrying [admin] scope after TOTP step-up
	// (REQ-AUTH-SCOPE-03). Cookie name is herold_admin_session so
	// cross-listener cookie reuse is mechanically impossible at the
	// parser level (REQ-OPS-ADMIN-LISTENER-03 + REQ-AUTH-SCOPE-01).
	uiSrvAdmin, err := newProtoUIServer(cfg, st, dir, oidcRP, clk, logger, "admin")
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, "protoui: admin server failed; UI disabled",
			slog.String("err", err.Error()))
		uiSrvAdmin = nil
	}
	// Public-listener UI server (TODO(3.7): the SPA login page lands
	// then; for now the public /login is the same template flow and
	// issues end-user-scoped cookies).
	uiSrvPublic, err := newProtoUIServer(cfg, st, dir, oidcRP, clk, logger, "public")
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, "protoui: public server failed",
			slog.String("err", err.Error()))
		uiSrvPublic = nil
	}

	// ----- Admin handler -----
	adminMux := http.NewServeMux()
	adminMux.Handle("/api/v1/", adminHandler)
	if uiSrvAdmin != nil {
		adminMux.Handle(prefix+"/", uiSrvAdmin.Handler())
		// Bare `/` on the admin listener: redirect a browser to the
		// admin login page. API consumers never hit `/`.
		adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, prefix+"/login", http.StatusSeeOther)
				return
			}
			http.NotFound(w, r)
		})
	}
	bundle.admin = withPanicRecover(logger.With("subsystem", "admin-mux"),
		"admin.mux", adminMux)

	// ----- Public handler -----
	publicMux := http.NewServeMux()

	// Public /login flow (suite-login per REQ-AUTH-SCOPE-01) lives at
	// the same /ui path prefix on the public listener with end-user
	// scope issuance. The public protoui server uses a distinct
	// cookie name (herold_public_session) so an admin cookie
	// presented here is silently ignored at the parser level.
	if uiSrvPublic != nil {
		publicMux.Handle(prefix+"/", uiSrvPublic.Handler())

		// Root-path adapters for the login flow (REQ-AUTH-SCOPE-01).
		// The protoui server registers its routes under prefix+"/login"
		// etc., so a browser directed to /login?return=%2F%23%2Fmail
		// would 404 because only prefix+"/login" is mounted. These
		// adapters rewrite the inbound path to the prefix-based
		// equivalent and delegate to the same protoui handler without
		// mutating the original request. Go's longest-prefix routing
		// ensures these registrations take priority over the SPA
		// catch-all at "/", so the tabard SPA's defensive 404 for
		// /login never fires for these paths in practice.
		uiHandler := uiSrvPublic.Handler()
		for _, root := range []string{"/login", "/logout", "/oidc/"} {
			root := root // pin for closure
			publicMux.HandleFunc(root, func(w http.ResponseWriter, r *http.Request) {
				r2 := r.Clone(r.Context())
				r2.URL.Path = prefix + r.URL.Path
				uiHandler.ServeHTTP(w, r2)
			})
		}
	}

	// Image proxy (REQ-SEND-70..78). Public-listener-only: the
	// browser presenting an end-user cookie loads upstream-tracking-
	// free images without a separate auth dance.
	if cfg.Server.ImageProxy.Enabled == nil || *cfg.Server.ImageProxy.Enabled {
		ipCfg := cfg.Server.ImageProxy
		var imgResolver func(*http.Request) (store.PrincipalID, bool)
		if uiSrvPublic != nil {
			imgResolver = uiSrvPublic.ResolveSession
		}
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
			SessionResolver:     imgResolver,
		})
		publicMux.Handle("/proxy/image",
			withPanicRecover(logger.With("subsystem", "protoimg"),
				"proxy.image", imgSrv.Handler()))
	}

	// Chat ephemeral channel (REQ-CHAT-40..46). Public-listener-only.
	var chatBroadcaster *protochat.Broadcaster
	var chatSrv *protochat.Server
	if cfg.Server.Chat.Enabled == nil || *cfg.Server.Chat.Enabled {
		chatBroadcaster = protochat.NewBroadcaster(
			logger.With("subsystem", "protochat"),
			callChatMembersResolver(st))
		var chatResolver func(*http.Request) (store.PrincipalID, bool)
		if uiSrvPublic != nil {
			chatResolver = uiSrvPublic.ResolveSession
		}
		chatSrv = protochat.New(protochat.Options{
			Store:            st,
			Logger:           logger.With("subsystem", "protochat"),
			Clock:            clk,
			SessionResolver:  chatResolver,
			Broadcaster:      chatBroadcaster,
			Membership:       callChatMembershipResolver(st),
			PeersResolver:    callChatPeersResolver(st),
			MaxConnections:   cfg.Server.Chat.MaxConnections,
			PerPrincipalCap:  cfg.Server.Chat.PerPrincipalCap,
			PingInterval:     time.Duration(cfg.Server.Chat.PingIntervalSeconds) * time.Second,
			PongTimeout:      time.Duration(cfg.Server.Chat.PongTimeoutSeconds) * time.Second,
			WriteTimeout:     time.Duration(cfg.Server.Chat.WriteTimeoutSeconds) * time.Second,
			MaxFrameBytes:    cfg.Server.Chat.MaxFrameBytes,
			AllowedOrigins:   cfg.Server.Chat.AllowedOrigins,
			AllowEmptyOrigin: cfg.Server.Chat.AllowEmptyOrigin,
		})
		publicMux.Handle("/chat/ws",
			withPanicRecover(logger.With("subsystem", "protochat"),
				"chat.ws", chatSrv.Handler()))
		bundle.srvs.chatSrv = chatSrv
	}

	// Video calls (REQ-CALL-*). Public-listener-only. Two surfaces:
	//   - HTTP credential mint at /api/v1/call/credentials, sharing
	//     the suite session cookie with the UI and additionally
	//     accepting protoadmin Bearer API keys (kept on the public
	//     listener for browser-driven calling; an API key with
	//     ScopeEndUser hits this same endpoint).
	//   - Chat call.signal handler, registered against the chat
	//     protocol so call-lifecycle bookkeeping (call.started /
	//     call.ended system messages) lives outside the chat
	//     ephemeral surface.
	if cfg.Server.Call.Enabled == nil || *cfg.Server.Call.Enabled {
		var sharedSecret []byte
		if cfg.Server.TURN.SharedSecretEnv != "" {
			s, err := sysconfig.ResolveSecretStrict(cfg.Server.TURN.SharedSecretEnv)
			if err != nil {
				return composedHandlers{}, fmt.Errorf("admin: resolve TURN shared secret: %w", err)
			}
			sharedSecret = []byte(s)
		}
		var callResolver func(*http.Request) (store.PrincipalID, bool)
		if uiSrvPublic != nil {
			callResolver = uiSrvPublic.ResolveSession
		}
		callSrv := protocall.New(protocall.Options{
			Logger:         logger.With("subsystem", "protocall"),
			Clock:          clk,
			Broadcaster:    newCallBroadcasterAdapter(chatBroadcaster),
			Members:        newCallMembersAdapter(st),
			SystemMessages: newCallSysmsgsAdapter(st),
			Presence:       newCallPresenceAdapter(chatBroadcaster),
			TURN: protocall.TURNConfig{
				URIs:          cfg.Server.TURN.URIs,
				SharedSecret:  sharedSecret,
				CredentialTTL: time.Duration(cfg.Server.TURN.CredentialTTLSeconds) * time.Second,
			},
			Authn:       newCallAuthn(st, callResolver),
			RingTimeout: time.Duration(cfg.Server.Call.RingTimeoutSeconds) * time.Second,
		})
		publicMux.Handle("/api/v1/call/credentials",
			withPanicRecover(logger.With("subsystem", "protocall"),
				"call.credentials", callSrv.HTTPHandler()))
		if chatSrv != nil {
			if err := chatSrv.RegisterHandler("call.signal", callSignalForwarder(callSrv)); err != nil {
				return composedHandlers{}, fmt.Errorf("admin: register call.signal handler: %w", err)
			}
		}
		bundle.srvs.callSrv = callSrv
	}

	// JMAP Core (RFC 8620) + Mail / Identity / EmailSubmission
	// (RFC 8621). Public-listener-only.
	jmapSrv := protojmap.NewServer(st, dir, tlsStore, logger.With("subsystem", "jmap"), clk, protojmap.Options{})
	emailsubmission.Register(jmapSrv.Registry(), st, outboundQ, jmapidentity.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-identity"), clk),
		logger.With("subsystem", "jmap-emailsubmission"), clk)
	// JMAP PushSubscription (REQ-PROTO-120..122). The VAPID key
	// reference is operator-supplied; an unconfigured deployment
	// still advertises the capability but omits applicationServerKey
	// so the tabard SPA surfaces "push unavailable" rather than
	// trying to register against a missing key.
	vapidMgr := vapid.New()
	if ref := cfg.Server.Push.VAPIDPrivateKeyRef(); ref != "" {
		raw, err := sysconfig.ResolveSecretStrict(ref)
		if err != nil {
			logger.Warn("vapid: failed to resolve VAPID private key; Web Push disabled",
				slog.String("err", err.Error()))
		} else if err := vapidMgr.Load([]byte(raw)); err != nil {
			logger.Warn("vapid: failed to load VAPID private key; Web Push disabled",
				slog.String("err", err.Error()))
		} else {
			logger.Info("vapid: loaded VAPID key pair; Web Push enabled")
		}
	} else {
		logger.Info("vapid: no VAPID key pair configured; Web Push disabled")
	}
	// Outbound Web Push dispatcher (Wave 3.8b, REQ-PROTO-123 + 125 +
	// 126). Constructed unconditionally so the JMAP handler can call
	// SendVerificationPing — the dispatcher's Run loop short-circuits
	// when VAPID is unconfigured. The HTTP client uses
	// netguard.ControlContext so a misconfigured push endpoint that
	// resolves to a private IP is refused before connect.
	pushTimeoutSecs := cfg.Server.Push.HTTPTimeoutSeconds
	if pushTimeoutSecs <= 0 {
		pushTimeoutSecs = int(webpush.DefaultHTTPTimeout / time.Second)
	}
	pushDialer := &net.Dialer{
		Timeout:        time.Duration(pushTimeoutSecs) * time.Second,
		ControlContext: netguard.ControlContext(),
	}
	pushHTTPClient := &http.Client{
		Timeout: time.Duration(pushTimeoutSecs) * time.Second,
		Transport: &http.Transport{
			DialContext: pushDialer.DialContext,
		},
	}
	pushDispatcher, err := webpush.New(webpush.Options{
		Store:              st,
		VAPID:              vapidMgr,
		Clock:              clk,
		Logger:             logger.With("subsystem", "webpush"),
		HTTPDoer:           pushHTTPClient,
		Hostname:           cfg.Server.Hostname,
		Subject:            cfg.Server.Push.VAPIDSubject,
		PollInterval:       time.Duration(cfg.Server.Push.DispatcherPollIntervalSeconds) * time.Second,
		HTTPTimeout:        time.Duration(cfg.Server.Push.HTTPTimeoutSeconds) * time.Second,
		JWTExpiry:          time.Duration(cfg.Server.Push.JWTExpirySeconds) * time.Second,
		RateLimitPerMinute: cfg.Server.Push.RateLimitPerMinute,
		RateLimitPerDay:    cfg.Server.Push.RateLimitPerDay,
		CooldownDuration:   time.Duration(cfg.Server.Push.CooldownSeconds) * time.Second,
		CoalesceWindow:     time.Duration(cfg.Server.Push.CoalesceWindowSeconds) * time.Second,
	})
	if err != nil {
		return composedHandlers{}, fmt.Errorf("admin: webpush dispatcher: %w", err)
	}
	bundle.srvs.webpushDispatch = pushDispatcher
	jmappush.Register(jmapSrv.Registry(), st, vapidMgr, pushDispatcher, logger.With("subsystem", "jmap-push"), clk)
	jmapHandler := jmapSrv.Handler()
	publicMux.Handle("/.well-known/jmap",
		withPanicRecover(logger.With("subsystem", "jmap"), "jmap.session", jmapHandler))
	publicMux.Handle("/jmap",
		withPanicRecover(logger.With("subsystem", "jmap"), "jmap.api", jmapHandler))
	publicMux.Handle("/jmap/",
		withPanicRecover(logger.With("subsystem", "jmap"), "jmap.api", jmapHandler))

	// HTTP send API (REQ-SEND-*). Public-listener-only.
	sendSrv := protosend.NewServer(
		st,
		dir,
		outboundQ,
		tlsStore,
		logger.With("subsystem", "protosend"),
		clk,
		protosend.Options{
			Hostname: cfg.Server.Hostname,
		},
	)
	publicMux.Handle("/api/v1/mail/",
		withPanicRecover(logger.With("subsystem", "protosend"), "mail.send", sendSrv.Handler()))
	bundle.srvs.sendSrv = sendSrv

	// SES inbound webhook (REQ-HOOK-SES-01..07). Mounted on the public
	// listener only when [hooks.ses_inbound.enabled] is true.
	// sysconfig.Validate guarantees all required fields are set and
	// credentials are secret references; resolution failures here are
	// hard errors (operator misconfiguration detected at startup).
	if cfg.Hooks.SESInbound.Enabled {
		sesCfg := cfg.Hooks.SESInbound
		accessKeyID, err := sysconfig.ResolveSecretStrict(sesCfg.AWSAccessKeyIDEnv)
		if err != nil {
			return composedHandlers{}, fmt.Errorf("admin: ses_inbound: resolve access key id: %w", err)
		}
		secretAccessKey, err := sysconfig.ResolveSecretStrict(sesCfg.AWSSecretAccessKeyEnv)
		if err != nil {
			return composedHandlers{}, fmt.Errorf("admin: ses_inbound: resolve secret access key: %w", err)
		}
		sessionToken := ""
		if sesCfg.AWSSessionTokenEnv != "" {
			sessionToken, err = sysconfig.ResolveSecretStrict(sesCfg.AWSSessionTokenEnv)
			if err != nil {
				return composedHandlers{}, fmt.Errorf("admin: ses_inbound: resolve session token: %w", err)
			}
		}
		sesH := sesinbound.New(
			sesinbound.Config{
				AWSRegion:                  sesCfg.AWSRegion,
				S3BucketAllowlist:          sesCfg.S3BucketAllowlist,
				SNSTopicARNAllowlist:       sesCfg.SNSTopicARNAllowlist,
				SignatureCertHostAllowlist: sesCfg.SignatureCertHostAllowlist,
				AWSAccessKeyID:             accessKeyID,
				AWSSecretAccessKey:         secretAccessKey,
				AWSSessionToken:            sessionToken,
			},
			&sesPipelineAdapter{smtp: smtpSrv, meta: st.Meta()},
			&sesSeenStore{meta: st.Meta()},
			st.Meta(), // satisfies sesinbound.AuditLogger
			logger.With("subsystem", "ses_inbound"),
		)
		publicMux.Handle("/hooks/ses/inbound",
			withPanicRecover(logger.With("subsystem", "ses_inbound"),
				"hooks.ses.inbound", sesH))
		logger.InfoContext(ctx, "ses_inbound: handler mounted",
			slog.String("region", sesCfg.AWSRegion),
			slog.Int("buckets", len(sesCfg.S3BucketAllowlist)))
	}

	// Tabard SPA mount (REQ-DEPLOY-COLOC-01..05). When the operator
	// has not opted out (Tabard.Enabled defaults true), the SPA
	// handler registers as the catch-all `/` on the public mux.
	// Go's longest-prefix routing means the more-specific API
	// mounts above (jmap, send, chat, image proxy, /ui/, ...)
	// retain priority; the SPA handler only sees requests that
	// did not match any other mount.
	//
	// When Tabard.Enabled is explicitly false the catch-all is left
	// to the default 404 path so admin-only deployments do not
	// silently respond at /.
	if cfg.Server.Tabard.Enabled == nil || *cfg.Server.Tabard.Enabled {
		spaSrv, err := tabardspa.New(tabardspa.Options{
			Logger:     logger.With("subsystem", "tabardspa"),
			AssetDir:   cfg.Server.Tabard.AssetDir,
			PublicHost: cfg.Server.Hostname,
		})
		if err != nil {
			return composedHandlers{}, fmt.Errorf("admin: tabard SPA: %w", err)
		}
		publicMux.Handle("/",
			withPanicRecover(logger.With("subsystem", "tabardspa"),
				"tabardspa", spaSrv.Handler()))
	}

	bundle.public = withPanicRecover(logger.With("subsystem", "public-mux"),
		"public.mux", publicMux)
	return bundle, nil
}

// newProtoUIServer constructs a protoui.Server for the named listener
// kind. The cookie name is forked per REQ-AUTH-SCOPE-01 +
// REQ-OPS-ADMIN-LISTENER-03 so cross-listener cookie reuse is
// mechanically impossible: a public-listener cookie presented to the
// admin listener fails the cookie-name lookup and conversely. Both
// servers share the same templates and signing key (the latter so a
// dev-mode co-mount that uses a single signing key still produces
// stable cookies; in production they're independent listener
// instances and the signing key environment variable is the same
// process-wide).
func newProtoUIServer(
	cfg *sysconfig.Config,
	st store.Store,
	dir *directory.Directory,
	oidcRP *directoryoidc.RP,
	clk clock.Clock,
	logger *slog.Logger,
	listenerKind string,
) (*protoui.Server, error) {
	if cfg.Server.UI.Enabled != nil && !*cfg.Server.UI.Enabled {
		return nil, errors.New("ui disabled")
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
	cookieName := cfg.Server.UI.CookieName
	csrfName := cfg.Server.UI.CSRFCookieName
	switch listenerKind {
	case "admin":
		// Admin-listener cookie. Distinct name per REQ-AUTH-SCOPE-01
		// so cross-listener cookie reuse is mechanically impossible
		// at the parser level. Operator-supplied CookieName is used
		// only for the public listener (the admin listener is
		// loopback-by-default and rarely renamed).
		if cookieName == "" || cookieName == "herold_ui_session" {
			cookieName = "herold_admin_session"
		} else {
			cookieName = cookieName + "_admin"
		}
		if csrfName == "" || csrfName == "herold_ui_csrf" {
			csrfName = "herold_admin_csrf"
		} else {
			csrfName = csrfName + "_admin"
		}
	default:
		// Public listener.
		if cookieName == "" {
			cookieName = "herold_public_session"
		}
		if csrfName == "" {
			csrfName = "herold_public_csrf"
		}
	}
	return protoui.NewServer(st, dir, oidcRP, clk, protoui.Options{
		PathPrefix:   prefix,
		Logger:       logger.With("subsystem", "ui", "listener", listenerKind),
		ListenerKind: listenerKind,
		Session: protoui.SessionConfig{
			SigningKey:     signingKey,
			CookieName:     cookieName,
			CSRFCookieName: csrfName,
			TTL:            cfg.Server.UI.SessionTTL.AsDuration(),
			SecureCookies:  secure,
		},
	})
}

// syntheticDispatcherAdapter adapts *protowebhook.Dispatcher to the
// protosmtp.WebhookDispatcher seam. The adapter translates between the
// SMTP-side and webhook-side SyntheticDispatch struct shapes (they
// carry the same fields but live in different packages so neither
// package has to import the other directly).
type syntheticDispatcherAdapter struct {
	d *protowebhook.Dispatcher
}

// MatchingSyntheticHooks implements protosmtp.WebhookDispatcher.
func (a syntheticDispatcherAdapter) MatchingSyntheticHooks(ctx context.Context, domain string) []store.Webhook {
	if a.d == nil {
		return nil
	}
	return a.d.MatchingSyntheticHooks(ctx, domain)
}

// DispatchSynthetic implements protosmtp.WebhookDispatcher.
func (a syntheticDispatcherAdapter) DispatchSynthetic(ctx context.Context, in protosmtp.SyntheticDispatch, hooks []store.Webhook) error {
	if a.d == nil {
		return errors.New("admin: nil webhook dispatcher")
	}
	return a.d.DispatchSynthetic(ctx, protowebhook.SyntheticDispatch{
		Domain:    in.Domain,
		Recipient: in.Recipient,
		MailFrom:  in.MailFrom,
		RouteTag:  in.RouteTag,
		BlobHash:  in.BlobHash,
		Size:      in.Size,
		Parsed:    in.Parsed,
	}, hooks)
}

// queueBouncePosterAdapter adapts *queue.Queue to the
// protosmtp.BouncePoster interface so the SMTP DATA-phase
// REQ-FLOW-ATTPOL-02 post-acceptance walker can enqueue a 5.3.4 DSN
// without protosmtp importing the queue package.
type queueBouncePosterAdapter struct{ q *queue.Queue }

// PostBounce implements protosmtp.BouncePoster.
func (a queueBouncePosterAdapter) PostBounce(ctx context.Context, in protosmtp.BounceInput) error {
	if a.q == nil {
		return errors.New("admin: queueBouncePosterAdapter has nil Queue")
	}
	return a.q.PostBounce(ctx, queue.BounceInput{
		MailFrom:        in.MailFrom,
		FinalRcpt:       in.FinalRcpt,
		OriginalRcpt:    in.OriginalRcpt,
		OriginalEnvID:   in.OriginalEnvID,
		OriginalHeaders: in.OriginalHeaders,
		MessageID:       in.MessageID,
		DiagnosticCode:  in.DiagnosticCode,
		StatusCode:      in.StatusCode,
	})
}

// acmePluginAdapter adapts *plugin.Manager to acme.PluginInvoker.
// The DNS-01 challenger calls dns.present / dns.cleanup on the named
// DNS plugin via this adapter.
type acmePluginAdapter struct {
	mgr *plugin.Manager
}

func (a acmePluginAdapter) Call(ctx context.Context, pluginName, method string, params any, result any) error {
	pl := a.mgr.Get(pluginName)
	if pl == nil {
		return fmt.Errorf("acme: dns plugin %q not registered", pluginName)
	}
	return pl.Call(ctx, method, params, result)
}

// parseChallengeType maps the sysconfig string to a store.ChallengeType.
// Empty string defaults to http-01 (REQ-OPS-50).
func parseChallengeType(s string) store.ChallengeType {
	switch s {
	case "tls-alpn-01":
		return store.ChallengeTypeTLSALPN01
	case "dns-01":
		return store.ChallengeTypeDNS01
	default:
		return store.ChallengeTypeHTTP01
	}
}

// updateCertExpiryMetrics queries all stored ACME certs and updates the
// herold_tls_cert_expiry_seconds gauge family (REQ-OPS-91). Called on a
// 1-minute housekeeping tick; also called after each renewal.
func updateCertExpiryMetrics(ctx context.Context, st store.Store, logger *slog.Logger) {
	if observe.TLSCertExpirySeconds == nil {
		return
	}
	cutoff := time.Now().Add(100 * 365 * 24 * time.Hour)
	certs, err := st.Meta().ListACMECertsExpiringBefore(ctx, cutoff)
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "acme: list certs for metric update", slog.String("err", err.Error()))
		return
	}
	for _, c := range certs {
		observe.TLSCertExpirySeconds.WithLabelValues(c.Hostname).Set(float64(c.NotAfter.Unix()))
	}
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
