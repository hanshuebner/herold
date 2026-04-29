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
	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/autodns"
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
	jmapchat "github.com/hanshuebner/herold/internal/protojmap/chat"
	jmapcoach "github.com/hanshuebner/herold/internal/protojmap/coach"
	jmapllmtransparency "github.com/hanshuebner/herold/internal/protojmap/llmtransparency"
	jmapmail "github.com/hanshuebner/herold/internal/protojmap/mail"
	jmapcatsettings "github.com/hanshuebner/herold/internal/protojmap/mail/categorysettings"
	"github.com/hanshuebner/herold/internal/protojmap/mail/emailsubmission"
	jmapidentity "github.com/hanshuebner/herold/internal/protojmap/mail/identity"
	jmapsearchsnippet "github.com/hanshuebner/herold/internal/protojmap/mail/searchsnippet"
	jmapseenaddress "github.com/hanshuebner/herold/internal/protojmap/mail/seenaddress"
	jmapthread "github.com/hanshuebner/herold/internal/protojmap/mail/thread"
	jmapvacation "github.com/hanshuebner/herold/internal/protojmap/mail/vacation"
	jmappush "github.com/hanshuebner/herold/internal/protojmap/push"
	"github.com/hanshuebner/herold/internal/protologin"
	"github.com/hanshuebner/herold/internal/protosend"
	"github.com/hanshuebner/herold/internal/protosmtp"
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
	heroldtls "github.com/hanshuebner/herold/internal/tls"
	"github.com/hanshuebner/herold/internal/vapid"
	"github.com/hanshuebner/herold/internal/webpush"
	"github.com/hanshuebner/herold/internal/webspa"
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
	// LogVerbose, when true, overrides every sink's activity filter to
	// allow-all and lowers every sink's level floor to debug (REQ-OPS-86c).
	// Set by --log-verbose CLI flag or HEROLD_LOG_VERBOSE=1.
	LogVerbose bool
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
// The sequence matches docs/design/server/architecture/01-system-overview.md §Lifecycle:
// parse -> observability -> store -> auxiliary subsystems -> plugins ->
// TLS -> protocol servers -> listeners bind -> mark ready -> serve ->
// drain on cancel.
func StartServer(ctx context.Context, cfg *sysconfig.Config, opts StartOpts) error {
	if cfg == nil {
		return errors.New("admin: nil Config")
	}

	// Observability. Build the multi-sink logger from sysconfig (REQ-OPS-80).
	levelVar := new(slog.LevelVar)
	levelVar.Set(parseSlogLevel(cfg.Observability.LogLevel))
	logger := opts.Logger
	if logger == nil {
		ml, err := observe.NewLogger(sysconfigToObserveCfg(cfg, opts.LogVerbose))
		if err != nil {
			// Non-fatal: fall back to a stderr JSON logger so startup
			// errors are still visible.
			slog.Default().LogAttrs(ctx, slog.LevelError,
				"admin: failed to build logger from config; falling back to default",
				slog.String("err", err.Error()),
			)
			ml, _ = observe.NewLogger(observe.ObservabilityConfig{})
		}
		logger = ml.Logger
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

	// autodns.Reporter (TLS-RPT aggregate reports, REQ-OPS-60..65).
	// Constructed before the outbound queue so the queue's SMTP client
	// can receive a non-nil reporter for per-failure Append calls.
	// The reporter itself is idle until RunDailyEmission fires; it does
	// not allocate goroutines on construction. The HTTP client uses a
	// 30-second timeout and no netguard restriction because rua= HTTPS
	// targets are operator-controlled (mail receivers, not user-supplied
	// URLs). The RuaResolver reads the `_smtp._tls.<domain>` TXT record
	// via the same mailauth.Resolver the rest of the server uses.
	tlsRPTHTTPClient := &http.Client{Timeout: 30 * time.Second}
	tlsRPTReporter := autodns.NewReporter(autodns.ReporterOptions{
		Store:           st,
		Logger:          logger.With("subsystem", "autodns-reporter"),
		Clock:           clk,
		HTTPClient:      tlsRPTHTTPClient,
		ReporterDomain:  cfg.Server.Hostname,
		ReporterContact: "tlsrpt-noreply@" + cfg.Server.Hostname,
		Hostname:        cfg.Server.Hostname,
	})
	// Queue is wired after outboundQ is constructed below (see
	// tlsRPTReporter.opts.Queue assignment — reporter has no setter;
	// we must pass nil here and start with nil Queue, logging warns
	// for any mailto: rua until the queue is available). Since the
	// queue is also not started until the errgroup fires, and no
	// emission tick fires until 24h from start, the nil Queue window
	// is zero in practice: the RuaResolver is called from the
	// emission loop, long after the queue is running. We construct a
	// second reporter below with the real queue once it exists.

	// Outbound queue construction (Phase 3 Wave 3.1.5). The queue
	// owns its scheduler / worker pool and is registered against the
	// lifecycle errgroup below so SIGTERM drains in-flight deliveries.
	// composeAdminAndUI receives the handle so JMAP EmailSubmission/set
	// and the HTTP send API enqueue through the same instance.
	outboundQ, err := buildOutboundQueue(cfg, st, dir, smtpServer, resolver, tlsRPTReporter, logger, clk)
	if err != nil {
		return fmt.Errorf("admin: outbound queue: %w", err)
	}
	// Now that outboundQ exists, rebuild the reporter with the real
	// queue so mailto: rua deliveries work. The SMTP client already
	// holds a pointer to tlsRPTReporter for Append calls; we replace
	// the reporter variable to get the queue-wired version for the
	// emission loop. The SMTP client's reference is the first
	// reporter; we need a second one with Queue set for emission.
	// Since Reporter is a struct (not an interface), we build a new one
	// and start its RunDailyEmission on the lifecycle errgroup below.
	tlsRPTEmitter := autodns.NewReporter(autodns.ReporterOptions{
		Store:           st,
		Logger:          logger.With("subsystem", "autodns-reporter"),
		Clock:           clk,
		HTTPClient:      tlsRPTHTTPClient,
		Queue:           queueTLSRPTAdapter{q: outboundQ},
		ReporterDomain:  cfg.Server.Hostname,
		ReporterContact: "tlsrpt-noreply@" + cfg.Server.Hostname,
		Hostname:        cfg.Server.Hostname,
	})
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
	// is persisted across restarts so signed fetch URLs remain valid
	// (REQ-HOOK-30..31). The dispatcher's change-feed Run loop and the
	// synthetic-recipient direct-dispatch path share the same instance;
	// both are bounded by the lifecycle errgroup gctx below.
	hookSigningKey, err := loadOrGenerateWebhookSigningKey(
		filepath.Join(cfg.Server.DataDir, "secrets", "webhook", "sign.key"),
		logger,
	)
	if err != nil {
		return fmt.Errorf("admin: webhook signing key: %w", err)
	}
	// Build the public base URL for fetch URLs (REQ-HOOK-30..31).
	// The fetch handler is mounted on the public listener; the URL
	// must match the externally-reachable address of that listener.
	publicBaseURL := cfg.Server.PublicBaseURL
	if publicBaseURL == "" {
		publicBaseURL = "https://" + cfg.Server.Hostname
	}
	webhookDispatcher := protowebhook.New(protowebhook.Options{
		Store:           st,
		Logger:          logger.With("subsystem", "protowebhook"),
		Clock:           clk,
		FetchURLBaseURL: publicBaseURL,
		SigningKey:      hookSigningKey,
	})
	smtpServer.SetWebhookDispatcher(syntheticDispatcherAdapter{d: webhookDispatcher})

	// Admin HTTP handler: the real protoadmin server. Options defaults
	// are applied inside NewServer; we pass only subsystem-level fields.
	// health was constructed before the ACME block above so the ACME gate
	// and protoadmin share the same instance (REQ-OPS-111).
	//
	// The Session config threads the cookie name + signing key into
	// protoadmin so the JSON login endpoint at POST /api/v1/auth/login
	// issues a cookie that requireAuth can subsequently verify
	// (REQ-AUTH-SESSION-REST, REQ-AUTH-CSRF).
	adminServer := protoadmin.NewServer(
		st,
		dir,
		oidc,
		logger.With("subsystem", "admin"),
		clk,
		protoadmin.Options{
			ServerVersion: "0.1.0",
			Health:        health,
			Session:       adminSessionCookieConfig(cfg),
		},
	)
	// REQ-AUTH-SESSION-REST: when no signing key is configured (typical
	// zero-config / Docker quickstart scenario), both the admin and public
	// cookie configs fall back to an ephemeral random key generated at
	// startup. Sessions issued with the ephemeral key are invalidated on
	// restart, which is acceptable for a development deployment. Operators
	// wanting session continuity across restarts set HEROLD_UI_SESSION_KEY
	// to a value of at least 32 bytes. The [server.ui].signing_key_env TOML
	// knob is a back-compat override that names an alternative env var; most
	// operators can ignore it and use HEROLD_UI_SESSION_KEY directly.
	//
	// Logged at WARN (not INFO) so an operator scanning logs after their
	// users complain about being logged out on every restart sees the
	// cause without trawling INFO traffic.
	effectiveEnv := cfg.Server.UI.SigningKeyEnv
	if effectiveEnv == "" {
		effectiveEnv = defaultSessionKeyEnv
	}
	if v := os.Getenv(effectiveEnv); len(v) < 32 {
		if len(v) == 0 {
			logger.Warn("session-cookie signing key not configured; " +
				"using ephemeral random key (admin and public sessions invalidated on every restart). " +
				"Set " + defaultSessionKeyEnv + " to a 32+ byte value for a persistent key.")
		} else {
			logger.Warn("session-cookie signing key too short; "+
				"using ephemeral random key (sessions invalidated on every restart)",
				"env", effectiveEnv,
				"min_bytes", 32,
				"got_bytes", len(v))
		}
	}
	// Parent mux composition (Phase 2 Wave 2.4): the admin HTTP
	// listener serves both the REST surface (protoadmin under
	// /api/v1) and the admin Svelte SPA (webspa.admin under /admin/).
	// We chose composition over a protoadmin.Mount(prefix, h) method
	// so protoadmin stays focused on its REST API and the SPA's
	// dependency on directory + store goes through its own
	// constructor. The two handlers are otherwise independent --
	// session cookies (SPA) and Bearer keys (REST) live in disjoint
	// header/cookie namespaces, and the URL prefixes do not overlap.
	bundle, err := composeAdminAndUI(ctx, cfg, st, dir, oidc, clk, logger, ftsIndex, tlsStore, outboundQ, adminServer.Handler(), smtpServer, hookSigningKey, health, sieveInterp)
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

	// TLS-RPT daily emission goroutine (REQ-OPS-60..65). Runs on a
	// 24-hour cadence; the RuaResolver adapts mailauth.Resolver.TXTLookup
	// to the autodns.RuaResolver shape (reads `_smtp._tls.<domain>` TXT).
	// RunDailyEmission returns nil on ctx cancellation so this goroutine
	// never fails the errgroup on graceful shutdown.
	tlsRPTRuaResolver := buildTLSRPTRuaResolver(resolver)
	g.Go(func() error {
		if err := tlsRPTEmitter.RunDailyEmission(gctx, tlsRPTRuaResolver); err != nil &&
			!errors.Is(err, context.Canceled) {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "tls-rpt emitter exited",
				slog.String("err", err.Error()))
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

	// ShortcutCoachStat GC tick (Phase 3 Wave 3.10 fixup, REQ-PROTO-110).
	// Deletes coach_events rows older than jmapcoach.GCWindow (90 days) on
	// a daily cadence with a 1-hour jitter to avoid thundering-herd on
	// multi-instance deployments. The tick runs 24 h after startup so
	// the server is fully warmed before the first GC pass; the jitter
	// is the modulo of the current Unix timestamp to spread instances
	// across the hour window.
	observe.RegisterCoachMetrics()
	g.Go(func() error {
		// Initial delay: 24h + jitter in [0, 1h) so multiple instances
		// do not all GC at exactly the same second.
		jitter := time.Duration(clk.Now().UnixNano()%int64(time.Hour)) / 10
		t := clk.After(24*time.Hour + jitter)
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-t:
			}
			cutoff := clk.Now().Add(-jmapcoach.GCWindow)
			n, err := st.Meta().GCCoachEvents(gctx, cutoff)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.LogAttrs(context.Background(), slog.LevelWarn,
					"coach gc: GCCoachEvents",
					slog.String("err", err.Error()))
			} else if n > 0 {
				if observe.CoachGCDeletedTotal != nil {
					observe.CoachGCDeletedTotal.Add(float64(n))
				}
				logger.LogAttrs(context.Background(), slog.LevelInfo,
					"coach gc: deleted expired events",
					slog.Int64("rows", n))
			}
			// Schedule next tick in 24h + same jitter so the schedule
			// stays predictable without pinning to wall-clock midnight.
			t = clk.After(24*time.Hour + jitter)
		}
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
		logger.LogAttrs(context.Background(), slog.LevelInfo,
			"shutdown: draining; press Ctrl-C again to force exit",
			slog.Duration("grace", grace))
		// A second SIGINT/SIGTERM during drain is the user telling us
		// to stop waiting. signal.NotifyContext above already consumed
		// the first signal and is no longer delivering them, so we
		// install our own handler for the duration of the drain.
		forceCh := make(chan os.Signal, 1)
		signal.Notify(forceCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(forceCh)
		// Periodic progress so the operator knows the process is
		// alive and how much of the grace window remains.
		const tick = 5 * time.Second
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		start := time.Now()
		deadlineCh := time.After(grace)
		for {
			select {
			case err := <-groupErr:
				return err
			case <-deadlineCh:
				logger.LogAttrs(context.Background(), slog.LevelWarn,
					"shutdown drain window elapsed; some goroutines did not exit",
					slog.Duration("grace", grace))
				return nil
			case <-ticker.C:
				remaining := (grace - time.Since(start)).Round(time.Second)
				if remaining < 0 {
					remaining = 0
				}
				logger.LogAttrs(context.Background(), slog.LevelInfo,
					"shutdown: waiting for goroutines to exit",
					slog.Duration("remaining", remaining))
			case sig := <-forceCh:
				logger.LogAttrs(context.Background(), slog.LevelWarn,
					"shutdown: second signal received; exiting now",
					slog.String("signal", sig.String()))
				return nil
			}
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
		return storesqlite.OpenWithOpts(ctx, cfg.Server.Storage.SQLite.Path,
			logger.With("subsystem", "store"), clk,
			storesqlite.Options{
				CacheSize:         cfg.Server.Storage.SQLite.CacheSize,
				WALAutocheckpoint: cfg.Server.Storage.SQLite.WALAutocheckpoint,
			})
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
		lns, fns, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, bundle, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		for i, ln := range lns {
			set.listeners = append(set.listeners, ln)
			set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fns[i]})
		}
	}
	for _, l := range adminBinds {
		lns, fns, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, bundle, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		for i, ln := range lns {
			set.listeners = append(set.listeners, ln)
			set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fns[i]})
		}
	}
	return set, nil
}

// bindOne opens one or more TCP sockets for the listener spec and
// returns one serve function per socket. A literal `localhost:port`
// address expands to two sockets (127.0.0.1 and ::1) so a single
// configuration line covers both stacks; every other address yields a
// single socket. On error any sockets opened earlier in the call are
// closed before returning.
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
) ([]net.Listener, []listenerServeFn, error) {
	addrs, err := sysconfig.ResolveBindAddresses(l.Address)
	if err != nil {
		return nil, nil, fmt.Errorf("admin: listen %s: %w", l.Name, err)
	}
	var (
		listeners []net.Listener
		serves    []listenerServeFn
	)
	closeAll := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	for _, addr := range addrs {
		ln, fn, err := bindOneAddress(ctx, cfg, logger, l, addr, tlsStore, smtpServer, imapServer, bundle, opts, len(listeners) == 0)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		listeners = append(listeners, ln)
		serves = append(serves, fn)
	}
	return listeners, serves, nil
}

// bindOneAddress binds a single host:port and wires the protocol-specific
// serve function. The publishAddr flag controls whether this socket's
// resolved address is recorded in opts.ListenerAddrs; when bindOne expands
// a localhost listener into two sockets we publish only the first
// (IPv4) address so the existing test fixture contract — one entry per
// listener name — is preserved.
func bindOneAddress(
	ctx context.Context,
	cfg *sysconfig.Config,
	logger *slog.Logger,
	l sysconfig.ListenerConfig,
	bindAddr string,
	tlsStore *heroldtls.Store,
	smtpServer *protosmtp.Server,
	imapServer *protoimap.Server,
	bundle composedHandlers,
	opts StartOpts,
	publishAddr bool,
) (net.Listener, listenerServeFn, error) {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("admin: listen %s (%s): %w", l.Name, bindAddr, err)
	}
	if publishAddr && opts.ListenerAddrs != nil {
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
// and /metrics to the admin handler; everything else to public.
// The auth.RequireScope check on the admin paths still enforces the
// scope boundary; coMountHandler is a routing convenience, not a
// security boundary.
func coMountHandler(public, admin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/"),
			strings.HasPrefix(r.URL.Path, "/admin/"),
			strings.HasPrefix(r.URL.Path, "/metrics"),
			r.URL.Path == "/api/v1/principals" || strings.HasPrefix(r.URL.Path, "/api/v1/principals/"),
			r.URL.Path == "/api/v1/domains" || strings.HasPrefix(r.URL.Path, "/api/v1/domains/"),
			r.URL.Path == "/api/v1/aliases" || strings.HasPrefix(r.URL.Path, "/api/v1/aliases/"),
			r.URL.Path == "/api/v1/api-keys" || strings.HasPrefix(r.URL.Path, "/api/v1/api-keys/"),
			r.URL.Path == "/api/v1/audit",
			r.URL.Path == "/api/v1/queue" || strings.HasPrefix(r.URL.Path, "/api/v1/queue/"),
			r.URL.Path == "/api/v1/certs" || strings.HasPrefix(r.URL.Path, "/api/v1/certs/"),
			r.URL.Path == "/api/v1/spam/policy",
			r.URL.Path == "/api/v1/oidc/providers" || strings.HasPrefix(r.URL.Path, "/api/v1/oidc/providers/"),
			// /api/v1/oidc/callback is intentionally omitted: it is a
			// user-facing route (external IdP redirect) routed to public
			// per REQ-AUTH-51; the public handler forwards it to the
			// admin handler via an explicit mount in composeAdminAndUI.
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
// sysconfigToObserveCfg converts a *sysconfig.Config and StartOpts into an
// observe.ObservabilityConfig by mapping the [[log.sink]] entries and the
// observability knobs. The sysconfig package is the authoritative source for
// the parsed / validated sink list; observe is the layering consumer that must
// not import sysconfig. This adapter lives in admin (the integration layer
// that owns both sides) so neither package depends on the other.
//
// verbose carries the --log-verbose / HEROLD_LOG_VERBOSE flag (REQ-OPS-86c).
func sysconfigToObserveCfg(cfg *sysconfig.Config, verbose bool) observe.ObservabilityConfig {
	sinks := make([]observe.LogSinkConfig, 0, len(cfg.Log.Sink))
	for _, sc := range cfg.Log.Sink {
		var act observe.ActivityFilterConfig
		if len(sc.Activities.Allow) > 0 {
			act.Allow = append([]string(nil), sc.Activities.Allow...)
		}
		if len(sc.Activities.Deny) > 0 {
			act.Deny = append([]string(nil), sc.Activities.Deny...)
		}
		var mods map[string]string
		if len(sc.Modules) > 0 {
			mods = make(map[string]string, len(sc.Modules))
			for k, v := range sc.Modules {
				mods[k] = v
			}
		}
		sinks = append(sinks, observe.LogSinkConfig{
			Target:     sc.Target,
			Format:     sc.Format,
			Level:      sc.Level,
			Modules:    mods,
			Activities: act,
		})
	}
	return observe.ObservabilityConfig{
		Sinks:        sinks,
		Verbose:      verbose,
		MetricsBind:  cfg.Observability.MetricsBind,
		OTLPEndpoint: cfg.Observability.OTLPEndpoint,
		// SecretKeys: not overridden here; observe defaults apply.
	}
}

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
	webhookSigningKey []byte,
	health *observe.Health,
	sieveInterp *sieve.Interpreter,
) (composedHandlers, error) {
	// ftsIndex is the chat-side full-text search backend (Wave 2.9.6
	// Track D, REQ-CHAT-80..82). It is the same Bleve index the mail
	// FTS worker writes to; jmapchat.RegisterWithFTS below uses it so
	// Message/query routes free-text filters through SearchChatMessages.
	var bundle composedHandlers

	// Public-listener session resolver (Phase 3c-iii). Built as a
	// closure over authsession.ResolveSession so siblings that need
	// cookie auth (image proxy, chat, call, JMAP) no longer depend on
	// the deleted internal/protoui package. The cookie config is the
	// same one protologin uses when issuing the cookie, so HMAC
	// verification succeeds.
	publicCookieCfg := publicSessionCookieConfig(cfg)
	publicSessionResolver := func(r *http.Request) (store.PrincipalID, bool) {
		return authsession.ResolveSession(r, publicCookieCfg, st, clk)
	}
	publicSessionWithScopeResolver := func(r *http.Request) (store.PrincipalID, auth.ScopeSet, bool) {
		return authsession.ResolveSessionWithScope(r, publicCookieCfg, st, clk)
	}

	// ----- Admin handler -----
	adminMux := http.NewServeMux()
	adminMux.Handle("/api/v1/", adminHandler)
	// /metrics is always mounted on the admin listener
	// (REQ-OPS-ADMIN-LISTENER-01). The dedicated MetricsBind listener
	// remains for operators who want a separate scrape endpoint; this
	// mount ensures scrapes also work when MetricsBind is empty or the
	// admin listener is the only HTTP surface.
	adminMux.Handle("/metrics", observe.MetricsHandler())
	// Admin Svelte SPA at /admin/ (Phase 3b of the merge plan -- the
	// only admin UI). Always mounted on the admin listener.
	adminSPA, err := webspa.NewAdmin(webspa.AdminOptions{
		Logger:        logger.With("subsystem", "webspa.admin"),
		AdminAssetDir: cfg.Server.AdminSPA.AssetDir,
	})
	if err != nil {
		return composedHandlers{}, fmt.Errorf("admin: admin SPA: %w", err)
	}
	adminMux.Handle("/admin/",
		http.StripPrefix("/admin",
			withPanicRecover(logger.With("subsystem", "webspa.admin"),
				"webspa.admin", adminSPA.Handler())))
	// Legacy /ui/* paths on the admin listener -> 308 to /admin/ so
	// older bookmarks land on the new SPA without breaking. The path
	// is hardcoded to /ui/ (the only value that was ever used).
	adminMux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
	})
	adminMux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
	})
	// Bare `/` on the admin listener: redirect a browser to the
	// admin SPA. API consumers never hit `/`.
	adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
			return
		}
		http.NotFound(w, r)
	})
	bundle.admin = withPanicRecover(logger.With("subsystem", "admin-mux"),
		"admin.mux", adminMux)

	// ----- Public handler -----
	publicMux := http.NewServeMux()

	// /metrics must NOT be served by the public listener
	// (REQ-OPS-ADMIN-LISTENER-01). Register an explicit 404 handler
	// before the suite SPA catch-all so the route is unambiguous.
	// The SPA catch-all would otherwise absorb the request and return
	// 200 with the SPA shell, leaking the metrics surface path to
	// browser clients.
	publicMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	// OIDC callback: the external IdP redirects the user's browser to the
	// callback URL after authentication. Since the user arrives on the
	// public listener, POST /api/v1/oidc/callback must also be reachable
	// there (REQ-AUTH-51). The route is forwarded to the same admin
	// handler so the directoryoidc state-machine logic is shared.
	// The link initiator (GET /api/v1/oidc/providers/...) is admin-REST
	// only; only the callback completion needs to be public.
	publicMux.Handle("/api/v1/oidc/callback", adminHandler)

	// JSON login/logout on the public listener (Phase 3c-i, REQ-AUTH-SCOPE-01).
	// POST /api/v1/auth/login issues herold_public_session + herold_public_csrf
	// cookies with the end-user scope set so the Suite SPA can authenticate
	// without the HTML /login redirect dance. POST /api/v1/auth/logout clears
	// both cookies. This is the single login surface on the public listener;
	// the protoui HTML /login flow was retired in Phase 3c-iii.
	//
	// A no-op rate limiter is used here; a public-listener per-IP bucket will
	// be added as a follow-up to Phase 3c-i.
	publicLoginSrv := protologin.New(protologin.Options{
		Session:       publicCookieCfg,
		Store:         st,
		Directory:     dir,
		Clock:         clk,
		Logger:        logger.With("subsystem", "protologin", "listener", "public"),
		Listener:      "public",
		Scopes:        publicSessionScopes,
		AuditAppender: publicLoginAuditAppender(st, clk),
	})
	publicLoginSrv.Mount(publicMux)

	// Self-service REST routes (Phase 4a, REQ-ADM-203): the Suite SPA
	// /settings panel calls these endpoints with the public-listener
	// session cookie + CSRF token. A second protoadmin.Server instance
	// is constructed with the public cookie config so requireAuth inside
	// each handler verifies the herold_public_session cookie rather than
	// the admin one. Only the self-service subset is mounted; admin-only
	// routes (queue, certs, domains, audit, etc.) are not reachable on
	// the public listener.
	//
	// Ordering note: publicLoginSrv already registered
	// POST /api/v1/auth/login and POST /api/v1/auth/logout above; Go's
	// longest-prefix mux gives those registrations priority over the
	// self-service mounts below because they are more-specific patterns.
	// The self-service server registers only /api/v1/principals/,
	// /api/v1/api-keys, /api/v1/api-keys/, and /api/v1/healthz/ so
	// there is no overlap with the login/logout paths.
	//
	// The OIDC callback (POST /api/v1/oidc/callback) continues to
	// forward to adminHandler: it completes a flow started on the admin
	// side and uses admin session state.
	selfServiceSrv := protoadmin.NewServer(
		st,
		dir,
		oidcRP,
		logger.With("subsystem", "admin", "listener", "public-selfservice"),
		clk,
		protoadmin.Options{
			ServerVersion: "0.1.0",
			Health:        health,
			Session:       publicCookieCfg,
		},
	)
	selfServiceHandler := selfServiceSrv.SelfServiceHandler()
	publicMux.Handle("/api/v1/principals/", selfServiceHandler)
	publicMux.Handle("/api/v1/api-keys", selfServiceHandler)
	publicMux.Handle("/api/v1/api-keys/", selfServiceHandler)
	publicMux.Handle("/api/v1/healthz/", selfServiceHandler)

	// Image proxy (REQ-SEND-70..78). Public-listener-only: the
	// browser presenting an end-user cookie loads upstream-tracking-
	// free images without a separate auth dance.
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
			SessionResolver:     publicSessionResolver,
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
		chatSrv = protochat.New(protochat.Options{
			Store:            st,
			Logger:           logger.With("subsystem", "protochat"),
			Clock:            clk,
			SessionResolver:  publicSessionResolver,
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
			Authn:       newCallAuthn(st, publicSessionResolver),
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
	// (RFC 8621). Public-listener-only. Cookie-based auth is wired
	// here via the public-listener authsession resolver so a browser
	// logged in via /api/v1/auth/login can call JMAP without a
	// separate Bearer credential (Wave 3.7-A, REQ-AUTH-SCOPE-01).
	jmapSrv := protojmap.NewServer(st, dir, tlsStore, logger.With("subsystem", "jmap"), clk, protojmap.Options{
		SessionResolver: publicSessionWithScopeResolver,
	})
	// JMAP Mail core handlers: Mailbox/* + Email/* + Sieve/* +
	// per-account capability provider (REQ-PROTO-41, REQ-PROTO-53,
	// REQ-PROTO-56). The top-level jmapmail.Register bundles all three;
	// Thread, SearchSnippet, and VacationResponse have separate entry
	// points because jmapmail.Register does not include them.
	jmapmail.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-mail"), clk)
	// Thread/get + Thread/changes (REQ-PROTO-41).
	jmapthread.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-thread"), clk)
	// SearchSnippet/get (REQ-PROTO-41 / REQ-PROTO-47).
	jmapsearchsnippet.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-searchsnippet"), clk)
	// VacationResponse/get + VacationResponse/set (REQ-PROTO-41,
	// REQ-PROTO-46). The sieve interpreter is the same instance used
	// by the inbound delivery pipeline; vacation rules are compiled by
	// the sieve package at delivery time, not at JMAP read time.
	jmapvacation.Register(jmapSrv.Registry(), st, sieveInterp, logger.With("subsystem", "jmap-vacation"), clk)
	// Identity + EmailSubmission (REQ-PROTO-41, REQ-PROTO-42,
	// REQ-PROTO-57, REQ-PROTO-58). Identity Register returns the
	// provider that EmailSubmission's Register needs to resolve
	// per-identity send-from addresses.
	emailsubmission.Register(jmapSrv.Registry(), st, outboundQ, jmapidentity.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-identity"), clk),
		nil, nil, // extSub, extRouter: wired in commit 4 when external-submission is configured
		logger.With("subsystem", "jmap-emailsubmission"), clk)
	// SeenAddress (REQ-MAIL-11e..m): recipient autocomplete history, exposed
	// under urn:ietf:params:jmap:mail (no new capability URI needed).
	jmapseenaddress.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-seenaddress"), clk)
	// JMAP PushSubscription (REQ-PROTO-120..122). The VAPID key
	// reference is operator-supplied; an unconfigured deployment
	// still advertises the capability but omits applicationServerKey
	// so the suite SPA surfaces "push unavailable" rather than
	// trying to register against a missing key.
	vapidMgr := vapid.New()
	if ref := cfg.Server.Push.VAPIDPrivateKeyRef(); ref != "" {
		raw, err := sysconfig.ResolveSecretStrict(ref)
		if err != nil {
			logger.Warn("vapid: failed to resolve VAPID private key; falling back to ephemeral key",
				slog.String("err", err.Error()))
		} else if err := vapidMgr.Load([]byte(raw)); err != nil {
			logger.Warn("vapid: failed to load VAPID private key; falling back to ephemeral key",
				slog.String("err", err.Error()))
		} else {
			logger.Info("vapid: loaded VAPID key pair; Web Push enabled")
		}
	}
	if !vapidMgr.Configured() {
		// No operator-configured VAPID key (typical zero-config / Docker
		// quickstart scenario): generate an ephemeral P-256 key pair so
		// the suite SPA can register Web Push subscriptions out of the
		// box. Subscriptions registered against the ephemeral key are
		// invalidated on process restart (the applicationServerKey
		// changes), which is acceptable for development. Operators
		// wanting subscription continuity wire a persistent key via
		// [server.push].vapid_private_key_env or _file (see
		// `herold vapid generate`).
		kp, err := vapid.Generate(nil)
		if err != nil {
			logger.Warn("vapid: failed to generate ephemeral VAPID key; Web Push disabled",
				slog.String("err", err.Error()))
		} else {
			vapidMgr = vapid.NewWithKey(kp)
			logger.Info("vapid: using ephemeral VAPID key pair; Web Push enabled (subscriptions reset on restart -- configure [server.push].vapid_private_key_env for persistence)")
		}
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
	// CategorySettings/get + CategorySettings/set + CategorySettings/recategorise
	// (Wave 3.13, REQ-FILT-200..231). Both cat and jobs are nil when no LLM
	// endpoint is configured; the handlers advertise the capability and serve
	// get/set normally, returning serverFail only for recategorise.
	jmapcatsettings.Register(jmapSrv.Registry(), st, nil, nil, logger.With("subsystem", "jmap-categorysettings"), clk)
	// LLMTransparency/get + Email/llmInspect (G14, REQ-FILT-65..68 / REQ-FILT-216).
	// spamPolicy is nil until a spam plugin is configured (handler returns empty spam
	// fields). categoriserEndpoint/Model are empty strings; per-account overrides come
	// from the store's CategorisationConfig row.
	jmapllmtransparency.Register(jmapSrv.Registry(), st, nil, "", "")
	// Chat JMAP capability (REQ-CHAT-*). Advertised whenever the chat
	// subsystem is enabled (the chat WebSocket listener at /chat/ws is
	// gated on the same flag below). Without this registration the
	// Suite SPA reports "Chat is not configured on this server" because
	// the capability URL never appears in the session descriptor.
	if cfg.Server.Chat.Enabled == nil || *cfg.Server.Chat.Enabled {
		jmapchat.RegisterWithFTS(jmapSrv.Registry(), st, ftsIndex,
			logger.With("subsystem", "jmap-chat"), clk, jmapchat.DefaultLimits())
	}
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

	// Webhook fetch handler (REQ-HOOK-30..31). Mounted on the public
	// listener so external webhook receivers can GET signed blob URLs
	// delivered in webhook payloads. The signing key MUST match the
	// Dispatcher's SigningKey so token verification succeeds.
	// protowebhook.FetchPath = "/webhook-fetch/".
	fetchSrv := protowebhook.NewFetchServer(protowebhook.FetchOptions{
		Store:      st,
		Logger:     logger.With("subsystem", "protowebhook-fetch"),
		Clock:      clk,
		SigningKey: webhookSigningKey,
	})
	publicMux.Handle(protowebhook.FetchPath,
		withPanicRecover(logger.With("subsystem", "protowebhook-fetch"),
			"webhook.fetch", fetchSrv.FetchHandler()))

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

	// Block /admin/ on the public listener. The admin SPA lives on the
	// admin listener (kind = "admin") only; without these explicit
	// handlers Go's stdlib mux falls through to the Suite SPA catch-all
	// below, which serves index.html and confuses the operator (the
	// Suite hash router then snaps to its default /#/mail route).
	// Return 404 with a hint so a misdirected operator can correct
	// course rather than getting a silent miss.
	publicMux.HandleFunc("/admin/", adminListenerHint)
	publicMux.HandleFunc("/admin", adminListenerHint)

	// Suite SPA mount (REQ-DEPLOY-COLOC-01..05). When the operator
	// has not opted out (Suite.Enabled defaults true), the SPA
	// handler registers as the catch-all `/` on the public mux.
	// Go's longest-prefix routing means the more-specific API
	// mounts above (jmap, send, chat, image proxy, /ui/, ...)
	// retain priority; the SPA handler only sees requests that
	// did not match any other mount.
	//
	// When Suite.Enabled is explicitly false the catch-all is left
	// to the default 404 path so admin-only deployments do not
	// silently respond at /.
	if cfg.Server.Suite.Enabled == nil || *cfg.Server.Suite.Enabled {
		spaSrv, err := webspa.New(webspa.Options{
			Logger:        logger.With("subsystem", "webspa.suite"),
			SuiteAssetDir: cfg.Server.Suite.AssetDir,
			PublicHost:    cfg.Server.Hostname,
		})
		if err != nil {
			return composedHandlers{}, fmt.Errorf("admin: suite SPA: %w", err)
		}
		publicMux.Handle("/",
			withPanicRecover(logger.With("subsystem", "webspa.suite"),
				"webspa.suite", spaSrv.Handler()))
	}

	bundle.public = withPanicRecover(logger.With("subsystem", "public-mux"),
		"public.mux", publicMux)
	return bundle, nil
}

// adminListenerHint serves /admin and /admin/ on the public listener with
// a 404 plus a short text body redirecting the operator to the admin
// listener (kind = "admin") where the admin SPA actually lives. Without
// this, the Suite SPA catch-all would intercept the request, serve
// index.html, and the Suite's hash router would snap to its default
// /#/mail route -- confusing operators who typed /admin/ on the public
// host by mistake.
func adminListenerHint(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(
		"The herold admin SPA is served by the admin listener (kind = \"admin\")," +
			" not the public listener. Connect to the admin listener's host:port" +
			" instead -- typically 127.0.0.1:9443 reachable via 'ssh -L 9443:127.0.0.1:9443'.\n",
	))
}

// defaultSessionKeyEnv is the fixed env var name operators set to provide a
// persistent HMAC-SHA256 signing key for session cookies. Using a well-known
// name means operators do not need to read docs to discover the knob: the WARN
// log line emitted when the key is absent names this variable directly.
const defaultSessionKeyEnv = "HEROLD_UI_SESSION_KEY"

// resolveSessionSigningKey returns the HMAC-SHA256 signing key to use for
// session cookies. Resolution order:
//
//  1. If [server.ui].signing_key_env is set (explicit TOML override), read
//     that env var. This is the back-compat path for operators who wired the
//     old knob; the key must be >= 32 bytes.
//  2. Otherwise read the predefined env var HEROLD_UI_SESSION_KEY.
//  3. If neither yields a usable key, generate a fresh cryptographically-random
//     32-byte key for this process lifetime.
//
// Callers that need an admin key and a public key MUST call this function
// independently for each; the two keys are intentionally different so cookies
// issued on one listener are not accepted on the other (REQ-AUTH-COOKIE-SCOPE).
//
// A randomly-generated key means sessions are invalidated when the process
// restarts. This is acceptable for development deployments and the default
// Docker quickstart; operators wanting session continuity set HEROLD_UI_SESSION_KEY.
func resolveSessionSigningKey(cfg *sysconfig.Config) []byte {
	// Step 1: honour the explicit TOML override (back-compat).
	if env := cfg.Server.UI.SigningKeyEnv; env != "" {
		if v := os.Getenv(env); len(v) >= 32 {
			return []byte(v)
		}
	}
	// Step 2: read the predefined env var.
	if v := os.Getenv(defaultSessionKeyEnv); len(v) >= 32 {
		return []byte(v)
	}
	// Step 3: No usable configured key: generate a fresh ephemeral one. Each
	// call returns a different key, so the admin and public configs diverge
	// intentionally.
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		// rand.Read failing is OS-level catastrophic; panic rather than
		// silently issuing cookies signed with a zero key.
		panic("admin: failed to generate ephemeral session signing key: " + err.Error())
	}
	return key[:]
}

// adminSessionCookieConfig extracts the admin-listener cookie parameters
// from sysconfig and returns an authsession.SessionConfig suitable for
// passing to protoadmin.Options.Session. The returned config uses the same
// signing key and cookie names as the admin listener so cookies minted by
// protoadmin's JSON /api/v1/auth/login endpoint are verifiable by
// requireAuth (REQ-AUTH-SESSION-REST).
//
// When no persistent signing key is configured, resolveSessionSigningKey
// generates an ephemeral 32-byte key so cookie auth works out-of-the-box
// (fixes #6, #7: the public self-service endpoints returned 401 because
// the empty signing key caused authenticateWithMode to skip cookie auth).
func adminSessionCookieConfig(cfg *sysconfig.Config) authsession.SessionConfig {
	signingKey := resolveSessionSigningKey(cfg)
	secure := true
	if cfg.Server.UI.SecureCookies != nil {
		secure = *cfg.Server.UI.SecureCookies
	}
	cookieName := cfg.Server.UI.CookieName
	csrfName := cfg.Server.UI.CSRFCookieName
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
	return authsession.SessionConfig{
		SigningKey:     signingKey,
		CookieName:     cookieName,
		CSRFCookieName: csrfName,
		TTL:            cfg.Server.UI.SessionTTL.AsDuration(),
		SecureCookies:  secure,
	}
}

// publicSessionCookieConfig extracts the public-listener cookie parameters
// from sysconfig and returns an authsession.SessionConfig for
// protologin.Options.Session and the authsession-based resolvers wired
// into protoimg, protochat, protocall, and protojmap. Cookies issued by
// protologin's JSON /api/v1/auth/login are verified by those resolvers
// (REQ-AUTH-SESSION-REST).
//
// Critically, this function is called ONCE inside composeAdminAndUI and the
// returned SessionConfig is shared between publicLoginSrv (cookie issuance)
// and selfServiceSrv (cookie verification). Both consumers must use the
// same SigningKey or HMAC verification will fail for every cookie.
//
// When no persistent signing key is configured, resolveSessionSigningKey
// generates an ephemeral 32-byte key (fixes #6, #7).
func publicSessionCookieConfig(cfg *sysconfig.Config) authsession.SessionConfig {
	signingKey := resolveSessionSigningKey(cfg)
	secure := true
	if cfg.Server.UI.SecureCookies != nil {
		secure = *cfg.Server.UI.SecureCookies
	}
	cookieName := cfg.Server.UI.CookieName
	csrfName := cfg.Server.UI.CSRFCookieName
	// sysconfig.Load fills in "herold_ui_session" / "herold_ui_csrf" as
	// defaults when the operator omits them. Treat those defaults as "not
	// operator-supplied" the same as empty string so we can apply the
	// public-listener names. The admin-listener function uses the same
	// pattern (see adminSessionCookieConfig above).
	if cookieName == "" || cookieName == "herold_ui_session" {
		cookieName = "herold_public_session"
	}
	if csrfName == "" || csrfName == "herold_ui_csrf" {
		csrfName = "herold_public_csrf"
	}
	return authsession.SessionConfig{
		SigningKey:     signingKey,
		CookieName:     cookieName,
		CSRFCookieName: csrfName,
		TTL:            cfg.Server.UI.SessionTTL.AsDuration(),
		SecureCookies:  secure,
	}
}

// publicSessionScopes returns the end-user scope set for cookies issued on
// the public listener (REQ-AUTH-SCOPE-01). The set covers all nine end-user
// scopes: end-user, mail.send, mail.receive, chat.read, chat.write, cal.read,
// cal.write, contacts.read, contacts.write.
//
// Today the principal flags do not bifurcate access at a finer granularity
// than "user has TOTP enabled"; a constant AllEndUserScopes set is therefore
// correct. When per-subsystem principal flags are added in a future phase,
// this function should be updated to gate the mail.*/chat.*/cal.*/contacts.*
// scopes against those flags.
func publicSessionScopes(_ store.Principal) auth.ScopeSet {
	return auth.NewScopeSet(auth.AllEndUserScopes...)
}

// publicLoginAuditAppender returns an AuditAppender func that writes records
// to the store's metadata audit log. It is the thin shim that lets
// protologin (which does not depend on any specific store implementation)
// write audit events via the store.Metadata interface (REQ-ADM-300).
//
// The actor defaults to ActorSystem / "system"; protologin's login handler
// overrides this by attaching the principal to the context before calling
// the appender on successful login.
func publicLoginAuditAppender(st store.Store, clk clock.Clock) func(ctx context.Context, action, subject string, outcome store.AuditOutcome, message string, meta map[string]string) {
	return func(ctx context.Context, action, subject string, outcome store.AuditOutcome, message string, meta map[string]string) {
		_ = st.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
			At:        clk.Now(),
			ActorKind: store.ActorSystem,
			ActorID:   "system",
			Action:    action,
			Subject:   subject,
			Outcome:   outcome,
			Message:   message,
			Metadata:  meta,
		})
	}
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

// loadOrGenerateWebhookSigningKey loads the 32-byte HMAC signing key from
// keyPath if it exists, or generates a fresh one and persists it. Mode 0600
// is enforced on creation. The key is never logged; callers audit-log
// the load action if needed (this function is pure I/O).
//
// If the parent directory does not exist it is created with mode 0700.
// An error is returned if the existing file is not exactly 32 bytes (corrupt).
func loadOrGenerateWebhookSigningKey(keyPath string, logger *slog.Logger) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("webhook signing key: create dir: %w", err)
	}
	raw, err := os.ReadFile(keyPath)
	if err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("webhook signing key: %q has %d bytes; want 32 (corrupt?)", keyPath, len(raw))
		}
		logger.LogAttrs(context.Background(), slog.LevelInfo,
			"webhook.signing_key_loaded",
			slog.String("action", "webhook.signing_key_loaded"),
			slog.String("path", keyPath))
		return raw, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("webhook signing key: read %q: %w", keyPath, err)
	}
	// Generate a new key.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("webhook signing key: generate: %w", err)
	}
	// Write atomically: write to a temp file then rename.
	tmpPath := keyPath + ".tmp"
	if err := os.WriteFile(tmpPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("webhook signing key: write %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, keyPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("webhook signing key: rename: %w", err)
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo,
		"webhook.signing_key_generated",
		slog.String("action", "webhook.signing_key_generated"),
		slog.String("path", keyPath))
	return key, nil
}

// queueTLSRPTAdapter adapts *queue.Queue to autodns.QueueSubmitter so
// the TLS-RPT emitter can enqueue mailto: reports without the autodns
// package importing the queue package.
type queueTLSRPTAdapter struct{ q *queue.Queue }

// Submit implements autodns.QueueSubmitter.
func (a queueTLSRPTAdapter) Submit(ctx context.Context, msg autodns.ReportSubmission) (string, error) {
	if a.q == nil {
		return "", errors.New("admin: queueTLSRPTAdapter has nil Queue")
	}
	envID, err := a.q.Submit(ctx, queue.Submission{
		MailFrom:   msg.MailFrom,
		Recipients: msg.Recipients,
		Body:       strings.NewReader(string(msg.Body)),
		Sign:       msg.Sign,
	})
	return string(envID), err
}

// buildTLSRPTRuaResolver builds an autodns.RuaResolver from a
// mailauth.Resolver. RFC 8460 §3 specifies that rua= URIs are published
// in `_smtp._tls.<domain>` TXT records; the resolver reads them and
// splits the comma-separated "rua=..." value into individual URIs.
func buildTLSRPTRuaResolver(r mailauth.Resolver) autodns.RuaResolver {
	return func(ctx context.Context, domain string) []string {
		txts, err := r.TXTLookup(ctx, "_smtp._tls."+domain)
		if err != nil {
			return nil
		}
		var out []string
		for _, txt := range txts {
			// RFC 8460 §3: TXT record format is "v=TLSRPTv1; rua=<uri>[,<uri>...]"
			for _, field := range strings.Fields(txt) {
				field = strings.TrimRight(field, ";")
				if strings.HasPrefix(field, "rua=") {
					for _, u := range strings.Split(strings.TrimPrefix(field, "rua="), ",") {
						u = strings.TrimSpace(u)
						if u != "" {
							out = append(out, u)
						}
					}
				}
			}
		}
		return out
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
