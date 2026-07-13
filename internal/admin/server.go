package admin

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/sync/errgroup"

	"github.com/hanshuebner/herold/internal/acme"
	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/autodns"
	"github.com/hanshuebner/herold/internal/bodymeta"
	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/chatretention"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/extimg/internalizeworker"
	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/fcm"
	"github.com/hanshuebner/herold/internal/identityverify"
	"github.com/hanshuebner/herold/internal/imapimport"
	"github.com/hanshuebner/herold/internal/linkpreview"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/maildmarc"
	"github.com/hanshuebner/herold/internal/maillist"
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
	jmapcontacts "github.com/hanshuebner/herold/internal/protojmap/contacts"
	jmapllmtransparency "github.com/hanshuebner/herold/internal/protojmap/llmtransparency"
	jmapmail "github.com/hanshuebner/herold/internal/protojmap/mail"
	jmapcatsettings "github.com/hanshuebner/herold/internal/protojmap/mail/categorysettings"
	jmapemail "github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/emailsubmission"
	jmapfileshare "github.com/hanshuebner/herold/internal/protojmap/mail/fileshare"
	jmapidentity "github.com/hanshuebner/herold/internal/protojmap/mail/identity"
	jmapimapimport "github.com/hanshuebner/herold/internal/protojmap/mail/imapimport"
	jmapsearchsnippet "github.com/hanshuebner/herold/internal/protojmap/mail/searchsnippet"
	jmapseenaddress "github.com/hanshuebner/herold/internal/protojmap/mail/seenaddress"
	jmaptaggedaddress "github.com/hanshuebner/herold/internal/protojmap/mail/taggedaddress"
	jmapthread "github.com/hanshuebner/herold/internal/protojmap/mail/thread"
	jmapvacation "github.com/hanshuebner/herold/internal/protojmap/mail/vacation"
	jmappush "github.com/hanshuebner/herold/internal/protojmap/push"
	"github.com/hanshuebner/herold/internal/protomanagesieve"
	"github.com/hanshuebner/herold/internal/protosend"
	"github.com/hanshuebner/herold/internal/protoshare"
	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/protowebhook"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/secrets"
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
	"github.com/hanshuebner/herold/internal/trashretention"
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
	cfg    *atomic.Pointer[sysconfig.Config]
	level  *slog.LevelVar
	logger *slog.Logger
}

// jmapMethodDeadlinesFromConfig extracts the JMAP-method entries from
// the unified [performance.method_deadline] map. Keys without the
// "IMAP:" prefix are taken to be JMAP method names verbatim.
func jmapMethodDeadlinesFromConfig(m map[string]sysconfig.Duration) map[string]time.Duration {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(m))
	for k, v := range m {
		if strings.HasPrefix(k, "IMAP:") {
			continue
		}
		out[k] = v.AsDuration()
	}
	return out
}

// imapCommandDeadlinesFromConfig extracts the IMAP-command entries from
// the unified [performance.method_deadline] map. Keys are expected to
// be "IMAP:<VERB>"; the prefix is stripped so the protoimap dispatcher
// can look up by raw verb.
func imapCommandDeadlinesFromConfig(m map[string]sysconfig.Duration) map[string]time.Duration {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(m))
	for k, v := range m {
		if !strings.HasPrefix(k, "IMAP:") {
			continue
		}
		out[strings.TrimPrefix(k, "IMAP:")] = v.AsDuration()
	}
	return out
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

	// OTLP log provider for client-log fan-out (REQ-OPS-205). Mirrors the
	// trace exporter wiring: empty endpoint yields a noop provider so the
	// slog half of ClientEmitter always fires regardless of OTLP config.
	otlpLogProvider, otlpLogShutdown, err := observe.NewOTLPLogProvider(ctx, observe.OTLPLoggerConfig{
		Endpoint: cfg.Observability.OTLPEndpoint,
	})
	if err != nil {
		return fmt.Errorf("admin: otlp log provider: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otlpLogShutdown(shutdownCtx)
	}()

	// clientEmitter fans each validated ClientEvent into slog and OTLP
	// (REQ-OPS-204, REQ-OPS-205). The PublicOTLPEgress flag is read from
	// cfg.ClientLog.Public.OTLPEgress (REQ-OPS-217, default false).
	clientEmitter := observe.NewClientEmitter(observe.ClientEmitterConfig{
		Logger:           logger,
		LogProvider:      otlpLogProvider,
		PublicOTLPEgress: cfg.ClientLog.Public.OTLPEgress,
	})

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

	// TelemetryGate for the clientlog ingest pipeline (REQ-OPS-208). Backed
	// by the sessions table so IsEnabled is a single indexed lookup at
	// ingest time. The adapter bridges directory.TelemetryGate (takes ctx)
	// to the protoadmin.TelemetryGate interface (no ctx).
	telemetryGate := &telemetryGateAdapter{
		gate: directory.NewTelemetryGate(st.Meta()),
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
	tlsStore, tlsWatchEntries, err := buildTLSStore(cfg, logger)
	if err != nil {
		return err
	}

	// File-cert watcher: start if any file-source certs are configured.
	// Stopped on shutdown via the errgroup gctx path below (registered
	// after gctx is created).
	tlsCertWatcher, err := heroldtls.StartFileWatcher(tlsStore, tlsWatchEntries, logger.With("subsystem", "tls"))
	if err != nil {
		return fmt.Errorf("admin: tls cert watcher: %w", err)
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
	// Memstats logger: a permanent slog history of runtime.MemStats so
	// we can reconstruct a memory event after the fact instead of needing
	// to be actively pprof'ing when it happens. Default 60 s interval is
	// quiet enough to leave on permanently. Override with
	// HEROLD_MEMSTATS_INTERVAL_SEC.
	memstatsInterval := 60 * time.Second
	if v := os.Getenv("HEROLD_MEMSTATS_INTERVAL_SEC"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			memstatsInterval = time.Duration(n) * time.Second
		}
	}
	observe.StartMemStatsLogger(ctx, logger.With("subsystem", "memstats"), memstatsInterval)
	// Heap-dump escape hatch: SIGUSR1 writes a heap profile to
	// $TMPDIR/herold-heap-<UTC-timestamp>.pprof. Useful when the admin
	// /debug/pprof/heap endpoint is unreachable. SIGUSR1 has no kernel
	// behaviour and is unused by herold's other signal handlers
	// (SIGHUP=reload, SIGINT/SIGTERM=shutdown).
	observe.StartHeapDumpOnSignal(ctx, logger.With("subsystem", "memstats"), "", syscall.SIGUSR1)
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
	//
	// Operator escape hatches (env vars):
	//   HEROLD_FTS_DISABLE=1            — skip the worker entirely
	//                                     (useful while diagnosing memory
	//                                     issues during a large bulk
	//                                     re-index after import).
	//   HEROLD_FTS_BATCH_SIZE=N         — cap docs per Bleve batch
	//                                     (default storefts.DefaultBatchSize).
	//                                     Each pending doc holds up to
	//                                     PerMessageMaxBytes of extracted
	//                                     text plus Bleve tokenizer state,
	//                                     so a smaller N caps peak heap.
	//   HEROLD_FTS_FLUSH_INTERVAL_MS=N  — wall-clock commit deadline.
	ftsDisabled := os.Getenv("HEROLD_FTS_DISABLE") == "1"
	ftsIndex, err := storefts.New(filepath.Join(cfg.Server.DataDir, "fts"), logger.With("subsystem", "fts"), clk)
	if err != nil {
		return fmt.Errorf("admin: fts index: %w", err)
	}
	defer ftsIndex.Close()
	// Replace the per-backend substring stub with the Bleve-backed
	// Composite so JMAP Email/query and IMAP SEARCH read from the real
	// index. The Composite preserves the backend's ReadChangeFeedForFTS
	// (still SQL-bound on state_changes) so the worker below keeps its
	// durable cursor on the relational feed.
	st = ftsOverride{Store: st, fts: storefts.NewComposite(ftsIndex, st.FTS())}
	ftsOpts := storefts.WorkerOptions{}
	if v := os.Getenv("HEROLD_FTS_BATCH_SIZE"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			ftsOpts.BatchSize = n
		}
	}
	if v := os.Getenv("HEROLD_FTS_MAX_BATCH_BYTES"); v != "" {
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			ftsOpts.MaxBatchBytes = n
		}
	}
	if v := os.Getenv("HEROLD_FTS_FLUSH_INTERVAL_MS"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			ftsOpts.FlushInterval = time.Duration(n) * time.Millisecond
		}
	}
	ftsWorker := storefts.NewWorker(
		ftsIndex,
		st,
		storefts.NewMailparseExtractor(),
		logger.With("subsystem", "fts"),
		clk,
		ftsOpts,
	)

	// External-image internalize worker (REQ-EXTIMG-BG-01). Drains the
	// importer-flagged backlog out-of-band so JMAP Email/get does not
	// block on per-message image fetches. The wake hook is registered
	// on the underlying storesqlite / storepg Store via a wrapper that
	// exposes SetInternalizeNotifyHook through ftsOverride; ftsOverride
	// embeds store.Store so the type assertion walks one level down.
	extimgWorker := internalizeworker.New(
		st,
		extimg.FromSysConfig(cfg.ExternalImages, cfg.Server.Hostname),
		logger.With("subsystem", "extimg-worker"),
		clk,
		internalizeworker.Options{},
	)
	registerInternalizeNotify(st, extimgWorker.Notify)

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

	// Categoriser: shared by SMTP inbound delivery and the IMAP import pool.
	// Constructed here (before smtpServer) so the same instance can be passed
	// to both. LLM endpoint / model / API key come from per-principal DB config
	// rows (CategorisationConfig), not sysconfig, so no operator fields need to
	// be threaded through — the Categoriser reads them lazily on each call via
	// GetCategorisationConfig. REQ-FILT-200.
	smtpCategoriser := categorise.New(categorise.Options{
		Store:  st,
		Logger: logger.With("subsystem", "categoriser"),
		Clock:  clk,
	})

	// Protocol servers.
	smtpServer, err := protosmtp.New(protosmtp.Config{
		Store:      st,
		Directory:  dir,
		DKIM:       dkim,
		SPF:        spf,
		DMARC:      dmarc,
		ARC:        arc,
		Spam:       spamClassifier,
		Sieve:      sieveInterp,
		Categorise: smtpCategoriser,
		TLS:        tlsStore,
		Resolver:   resolver,
		Clock:      clk,
		Logger:     logger.With("subsystem", "smtp"),
		Options: protosmtp.Options{
			Hostname:      cfg.Server.Hostname,
			ShutdownGrace: cfg.Server.ShutdownGrace.AsDuration(),
		},
		SpamPluginName:         spamPluginName,
		RcptResolver:           rcptResolverInst,
		RcptPluginName:         cfg.SMTP.Inbound.DirectoryResolveRcptPlugin,
		RcptPluginFirstDomains: cfg.SMTP.Inbound.PluginFirstForDomains,
		ExtImg:                 extimg.FromSysConfig(cfg.ExternalImages, cfg.Server.Hostname),
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
			ServerName:             cfg.Server.Hostname,
			DefaultCommandDeadline: cfg.Performance.DefaultDeadline.AsDuration(),
			CommandDeadlines:       imapCommandDeadlinesFromConfig(cfg.Performance.MethodDeadline),
		},
	)
	defer imapServer.Close()

	// ManageSieve (RFC 5804) listener — wired only when at least one
	// [[listener]] is declared with protocol = "managesieve". The
	// server is constructed unconditionally so bindOneAddress can
	// route the connection without a nil check; an unbound server is
	// inert.
	mssvServer := protomanagesieve.NewServer(
		st,
		dir,
		tlsStore,
		clk,
		logger.With("subsystem", "managesieve"),
		nil, // PasswordLookup: PLAIN auth via directory; SCRAM not yet
		nil, // TokenVerifier: OAUTHBEARER deferred
		protomanagesieve.Options{
			ServerName: cfg.Server.Hostname,
		},
	)
	defer mssvServer.Close()

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

	// Mailing-list fan-out (Stage 1, issue #183, REQ-MLIST-10..12/20..24/
	// 30..32). The Expander submits fan-out copies through the same
	// outboundQ every other outbound path uses and ARC-seals with a DKIM
	// key manager over the same store the operator's DKIM keys already
	// live in -- no separate signer setup. Built after outboundQ exists,
	// so wiring is late (SetMailingListExpander), mirroring
	// SetSubmissionQueue above.
	mlistKeyMgr := keymgmt.NewManager(st.Meta(), logger.With("subsystem", "maillist-arc"), clk, nil)
	mlistSealer := mailarc.NewSealer(mlistKeyMgr, nil, logger.With("subsystem", "maillist-arc"))
	mlistExpander := maillist.NewExpander(st.Meta(), outboundQ, mlistSealer, clk, logger.With("subsystem", "maillist"))

	// VERP bounce tokens (Stage 2, REQ-MLIST-50/51/52, issue #184).
	// Loaded independently of the external-submission/IMAP-import data-
	// key loads below (each guards its own feature's boot-time
	// availability): a missing/unresolvable data_key_ref degrades to
	// the S1 list-wide bounce address (logged loudly by Expander) rather
	// than failing boot, since a dev/test instance may not have
	// configured secrets yet.
	var mlistTokenSigner *maillist.TokenSigner
	if dk, dkErr := secrets.LoadDataKey(cfg.Server.Secrets); dkErr != nil {
		logger.Warn("maillist: data key not available; VERP bounce tokens disabled (falling back to the S1 list-wide bounce address)",
			slog.String("subsystem", "maillist"),
			slog.String("err", dkErr.Error()))
	} else {
		mlistTokenSigner = maillist.NewTokenSigner(dk)
	}
	mlistExpander.TokenSigner = mlistTokenSigner
	smtpServer.SetMailingListExpander(mlistExpander)

	mlistBounceProcessor := maillist.NewBounceProcessor(st.Meta(), st.Blobs(), mlistTokenSigner, clk, logger.With("subsystem", "maillist-bounce"))
	smtpServer.SetMailingListBounceProcessor(mlistBounceProcessor)

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

	// Attachment-share unlock-cookie signing key (REQ-SHARE-30). Persisted
	// under a distinct path from the webhook key so both survive independent
	// rotation. The file is only created / loaded when attachment_shares is
	// active; the zero-value key is never passed to protoshare.New.
	var shareSigningKey []byte
	sharesCfg := store.FileSharesConfig{
		DefaultTTL:             cfg.Server.AttachmentShares.DefaultTTL.AsDuration(),
		MaxTTL:                 cfg.Server.AttachmentShares.MaxTTL.AsDuration(),
		PendingTTL:             cfg.Server.AttachmentShares.PendingTTL.AsDuration(),
		RevokedGrace:           cfg.Server.AttachmentShares.RevokedGrace.AsDuration(),
		MaxSharesPerPrincipal:  int64(cfg.Server.AttachmentShares.MaxSharesPerPrincipal),
		ShareQuotaPerPrincipal: cfg.Server.AttachmentShares.ShareQuotaPerPrincipal.AsInt64(),
	}
	if cfg.Server.AttachmentShares.AttachmentSharesActive() {
		shareSigningKey, err = loadOrGenerateWebhookSigningKey(
			filepath.Join(cfg.Server.DataDir, "secrets", "share", "sign.key"),
			logger,
		)
		if err != nil {
			return fmt.Errorf("admin: share signing key: %w", err)
		}
	}

	// Build the public base URL for fetch URLs (REQ-HOOK-30..31).
	// The fetch handler is mounted on the public listener; the URL
	// must match the externally-reachable address of that listener.
	publicBaseURL := cfg.Server.PublicBaseURL
	if publicBaseURL == "" {
		publicBaseURL = "https://" + cfg.Server.Hostname
	}

	// REQ-MLIST-56: the same publicBaseURL backs the List-Unsubscribe
	// token URL, which must match the PublicServer mount composeAdminAndUI
	// adds to publicMux below (mlistTokenSigner is threaded through as a
	// parameter since publicMux itself is local to composeAdminAndUI).
	mlistExpander.PublicBaseURL = publicBaseURL

	webhookDispatcher := protowebhook.New(protowebhook.Options{
		Store:           st,
		Logger:          logger.With("subsystem", "protowebhook"),
		Clock:           clk,
		FetchURLBaseURL: publicBaseURL,
		SigningKey:      hookSigningKey,
	})
	smtpServer.SetWebhookDispatcher(syntheticDispatcherAdapter{d: webhookDispatcher})

	// External SMTP submission (REQ-AUTH-EXT-SUBMIT-01..10, Phase 4 cleanup).
	// The Submitter is built here — before adminServer — so that
	// protoadmin.Options.ExternalProbe can be set to the real probe rather
	// than the noopProbe fallback.  Without this wiring, PUT
	// /api/v1/identities/{id}/submission accepts any credential as "probe ok"
	// (REQ-MAIL-SUBMIT-03 silently broken). The same submitter is forwarded
	// into composeAdminAndUI for the JMAP EmailSubmission path, avoiding a
	// second data-key load.
	var prebuiltExtSubmitter *extsubmit.Submitter
	var extSubmitDataKey []byte
	var adminOAuthProviders map[string]protoadmin.OAuthProviderOptions
	if cfg.Server.ExternalSubmission.Enabled {
		dk, dkErr := secrets.LoadDataKey(cfg.Server.Secrets)
		if dkErr != nil {
			return fmt.Errorf("external submission: load data key: %w", dkErr)
		}
		extSubmitDataKey = dk
		// Refresher is shared between the Submitter (probe and test-sender
		// paths) and the background sweeper. Attaching it here ensures that
		// accessToken proactively refreshes an expired OAuth access token
		// during "Verbindung testen" — not just during the 60-second sweep
		// cycle (re #131).
		extSubmitRefresher := &extsubmit.Refresher{
			Meta:    st.Meta(),
			DataKey: dk,
		}
		prebuiltExtSubmitter = &extsubmit.Submitter{
			DataKey:   dk,
			HostName:  cfg.Server.Hostname,
			Refresher: extSubmitRefresher,
		}
		// Resolve OAuth provider client secrets from the sysconfig secret
		// references so the plaintext is available in-memory for the OAuth
		// start/callback handlers (REQ-AUTH-EXT-SUBMIT-03).
		if len(cfg.Server.OAuthProviders) > 0 {
			adminOAuthProviders = make(map[string]protoadmin.OAuthProviderOptions, len(cfg.Server.OAuthProviders))
			oauthCredsByTokenURL := make(map[string]extsubmit.OAuthClientCredentials, len(cfg.Server.OAuthProviders))
			for name, pc := range cfg.Server.OAuthProviders {
				cs, csErr := sysconfig.ResolveSecretStrict(pc.ClientSecretRef)
				if csErr != nil {
					return fmt.Errorf("external submission: oauth_providers.%s: resolve client_secret_ref: %w", name, csErr)
				}
				adminOAuthProviders[name] = protoadmin.OAuthProviderOptions{
					ClientID:        pc.ClientID,
					ClientSecret:    cs,
					AuthURL:         pc.AuthURL,
					TokenURL:        pc.TokenURL,
					Scopes:          pc.Scopes,
					ExtraAuthParams: pc.ExtraAuthParams,
				}
				// Index by token endpoint URL so accessToken can look up
				// operator credentials at refresh time (re #131).
				if pc.TokenURL != "" {
					oauthCredsByTokenURL[pc.TokenURL] = extsubmit.OAuthClientCredentials{
						ClientID:      pc.ClientID,
						ClientSecret:  cs,
						TokenEndpoint: pc.TokenURL,
					}
				}
			}
			prebuiltExtSubmitter.OAuthCredsByTokenURL = oauthCredsByTokenURL
		}
	}

	// IMAP import data key (REQ-IMAP-IMP-70, wave 5). Reuse the external-
	// submission data key if it was already loaded (same cfg.Server.Secrets
	// source); otherwise load it fresh. A not-configured or unresolvable key
	// is a non-fatal warning: the pool still starts (workers that try to open
	// a sealed credential will report an error per-account), and the admin
	// create/update endpoints gate-check the key themselves (returning 503).
	// Boot must not fail here (REQ-IMAP-IMP-70 / architecture §Lifecycle).
	var imapImportDataKey []byte
	if len(extSubmitDataKey) > 0 {
		// External submission already loaded the same key; share the slice.
		imapImportDataKey = extSubmitDataKey
	} else {
		if dk, dkErr := secrets.LoadDataKey(cfg.Server.Secrets); dkErr != nil {
			logger.Warn("imap-import: data key not available; credential sealing/opening will fail until data_key_ref is configured",
				slog.String("err", dkErr.Error()))
		} else {
			imapImportDataKey = dk
		}
	}

	// IMAP import categoriser adapter (REQ-IMAP-IMP-31). The same Categoriser
	// instance that is wired into the SMTP server above is reused here so that
	// a single LLM-client / HTTP-client pool backs all categorisation paths.
	imapImportCatAdapter := newIMAPImportCategoriserAdapter(
		st,
		smtpCategoriser,
		logger.With("subsystem", "imap-import-categoriser"),
	)

	// IMAP import worker pool (REQ-IMAP-IMP-26, wave 5). Constructed before
	// adminServerOpts so the pool pointer can be passed as IMAPImportStatus.
	// Dialer is nil (pool defaults to the production dialer, which wires
	// OAuth from cfg.IMAPImport.OAuth via accountWorker.tokenSourceForProvider).
	imapImportPool := imapimport.NewPool(imapimport.PoolOptions{
		Store:       st,
		DataKey:     imapImportDataKey,
		Categoriser: imapImportCatAdapter,
		Config:      cfg.IMAPImport,
		Logger:      logger.With("subsystem", "imap-import"),
		Clock:       clk,
	})

	// Admin HTTP handler: the real protoadmin server. Options defaults
	// are applied inside NewServer; we pass only subsystem-level fields.
	// health was constructed before the ACME block above so the ACME gate
	// and protoadmin share the same instance (REQ-OPS-111).
	//
	// The Session config threads the cookie name + signing key into
	// protoadmin so the JSON login endpoint at POST /api/v1/auth/login
	// issues a cookie that requireAuth can subsequently verify
	// (REQ-AUTH-SESSION-REST, REQ-AUTH-CSRF).
	// DKIM key manager backs POST/GET /api/v1/domains/{name}/dkim. Without
	// it those endpoints return 501; herold dkim generate / dkim show then
	// fail, and the bootstrap-time DKIM mint cannot run. A second manager
	// instance is fine -- keymgmt.Manager is stateless wrt the store, so
	// it composes with the queue's signer (see buildDKIMSigner in queue.go).
	adminDKIMManager := keymgmt.NewManager(
		st.Meta(),
		logger.With("subsystem", "dkim-keymgmt-admin"),
		clk,
		nil,
	)

	adminServerOpts := protoadmin.Options{
		ServerVersion: "0.1.0",
		Health:        health,
		// The admin REST surface is served on the public listener (re #58).
		// All web sessions (Suite and admin UI) share the public session cookie
		// (herold_public_session) and are governed by the unified idle-only
		// lifetime configured in publicSessionCookieConfig (REQ-AUTH-72, issue #78).
		Session:                   publicSessionCookieConfig(cfg, logger),
		ElevationTTL:              cfg.Server.UI.ElevationTTL.AsDuration(),
		ExternalSubmissionDataKey: extSubmitDataKey,
		OAuthProviders:            adminOAuthProviders,
		DKIMKeyManager:            adminDKIMManager,
		Clientlog: protoadmin.ClientlogOptions{
			Emitter:       clientEmitter,
			TelemetryGate: telemetryGate,
		},
		IMAPImportDataKey: imapImportDataKey,
		IMAPImportStatus:  imapImportPoolStatusAdapter{pool: imapImportPool},
		// Translation proxy (re #84): operator opt-in [translation] block.
		// When absent or disabled, POST /api/v1/translate returns 501 with
		// the stable "translation_not_configured" code and the Suite hides
		// the Translate affordance. The HTTP client is left nil so the
		// handler applies its bounded default timeout.
		Translation: &cfg.Translation,
		// Push (re #200): threads [server.push] through so GET
		// /api/v1/server/status can report read-only VAPID/FCM
		// configured-or-not status to the admin SPA. The credential
		// itself is never read back out through this Options field.
		Push: &cfg.Server.Push,
	}
	// External-submission retryer: redelivers submissions parked
	// held-for-reauth once the identity's auth recovers (re #70,
	// REQ-AUTH-EXT-SUBMIT-05). The same instance is shared by the OAuth
	// callback (user re-authenticates) and the sweeper (token refresh
	// succeeds); both invoke RetryForIdentity. Built here, before NewServer,
	// so it can be threaded into both call sites.
	var extRetryer *extsubmit.Retryer
	if prebuiltExtSubmitter != nil {
		adminServerOpts.ExternalProbe = protoadmin.DefaultProbeFromSubmitter(prebuiltExtSubmitter)
		adminServerOpts.ExternalTestSender = protoadmin.DefaultTestSenderFromSubmitter(prebuiltExtSubmitter)
		extRetryer = &extsubmit.Retryer{
			Meta:   st.Meta(),
			Blobs:  retryBlobAdapter{b: st.Blobs()},
			Submit: prebuiltExtSubmitter,
			Logger: logger.With("subsystem", "extsubmit-retryer"),
		}
		adminServerOpts.ExternalRetryer = extRetryer
	}
	adminServer := protoadmin.NewServer(
		st,
		dir,
		oidc,
		logger.With("subsystem", "admin"),
		clk,
		adminServerOpts,
	)
	// REQ-AUTH-SESSION-REST + issue #14: when neither
	// [server.ui].signing_key_env nor HEROLD_UI_SESSION_KEY is set, the
	// signing key is persisted to <data_dir>/secrets/ui-session-key on
	// first start and read back on every subsequent start. Sessions
	// therefore survive restarts without the operator wiring an env
	// var (the env path stays available as an override -- k8s secret
	// injection, external vault, etc).
	//
	// The one remaining footgun is "env is set but too short": we ignore
	// the value (falls through to the persisted path) but want the
	// operator to notice the misconfiguration.
	effectiveEnv := cfg.Server.UI.SigningKeyEnv
	if effectiveEnv == "" {
		effectiveEnv = defaultSessionKeyEnv
	}
	if v := os.Getenv(effectiveEnv); len(v) > 0 && len(v) < 32 {
		logger.Warn("session-cookie signing key env var is set but too short; ignoring it and using the persisted key under data_dir",
			"env", effectiveEnv,
			"min_bytes", 32,
			"got_bytes", len(v))
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
	//
	// sharedCfg is an atomic pointer shared between the JMAP handler
	// layer (for live directory-autocomplete mode reads) and the
	// Runtime (for config reload). Both hold the same pointer so
	// ReloadConfig updates propagate to in-flight JMAP calls.
	sharedCfg := new(atomic.Pointer[sysconfig.Config])
	sharedCfg.Store(cfg)
	bundle, err := composeAdminAndUI(ctx, cfg, sharedCfg, st, dir, oidc, clk, logger, ftsIndex, tlsStore, outboundQ, adminServer, smtpServer, hookSigningKey, shareSigningKey, sharesCfg, health, sieveInterp, prebuiltExtSubmitter, clientEmitter, telemetryGate, imapImportDataKey, mlistTokenSigner)
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
	boundListeners, err := bindListeners(ctx, cfg, logger, tlsStore, smtpServer, imapServer, mssvServer, bundle, opts)
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

	// TLS cert file watcher shutdown: stop when the server context cancels.
	// tlsCertWatcher is nil when no file-source certs are configured.
	if tlsCertWatcher != nil {
		cw := tlsCertWatcher
		g.Go(func() error {
			<-gctx.Done()
			cw.Stop()
			return nil
		})
	}

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
	// External submission metrics — registered unconditionally so the metric
	// names are always present in /metrics even when the feature is disabled,
	// allowing scrape configs to reference them without conditional logic.
	observe.RegisterExtSubMetrics()

	// FTS worker goroutine — registered on the lifecycle group so
	// shutdown drains it. Skipped when HEROLD_FTS_DISABLE=1; in that
	// mode IMAP SEARCH and JMAP Email/query lose body-text matching but
	// the rest of the server runs (useful as an escape hatch during
	// post-import re-index when peak memory exceeds the host).
	if ftsDisabled {
		logger.Warn("fts worker disabled by HEROLD_FTS_DISABLE; body-text search will not return results until re-enabled")
	} else {
		g.Go(func() error {
			if err := ftsWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.LogAttrs(context.Background(), slog.LevelWarn, "fts worker exited", slog.String("err", err.Error()))
				return err
			}
			return nil
		})
	}

	// IMAP import worker pool (REQ-IMAP-IMP-26, wave 5). Runs until gctx
	// cancels; mirrors every enabled account from its persisted cursor.
	// A nil return from Run means clean shutdown (context cancelled or no
	// enabled accounts); non-nil is a fatal setup failure.
	imapImportLog := logger.With("subsystem", "imap-import")
	g.Go(func() error {
		if err := imapImportPool.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			imapImportLog.LogAttrs(context.Background(), slog.LevelWarn,
				"imap-import pool exited", slog.String("err", err.Error()))
			return err
		}
		return nil
	})

	// External-image internalize worker (REQ-EXTIMG-BG-01). Runs from
	// startup so the importer-flagged backlog drains while the user
	// browses; freshly-arrived mail wakes the loop via the notify
	// hook registered at construction.
	g.Go(func() error {
		extimgWorker.Run(gctx)
		return nil
	})

	// Body-meta backfill worker. Sweeps messages whose body_meta_computed
	// flag is false, parses each body, and persists preview + hasAttachment
	// via SetMessageBodyMeta. Newest-first so the inbox backfills before
	// archive. The worker is modest by design: small batches and a long idle
	// sleep so it does not compete with real request traffic on the store.
	bodyMetaWorker := bodymeta.New(st, logger.With("subsystem", "bodymeta-worker"), clk, bodymeta.Options{})
	g.Go(func() error {
		bodyMetaWorker.Run(gctx)
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

	// Trash retention sweeper — REQ-STORE-90. Hard-deletes email messages
	// in each principal's Trash mailbox whose InternalDate is older than
	// the configured retention window. Bounded by the lifecycle errgroup
	// so shutdown drains it.
	trashRetentionWorker := trashretention.NewWorker(trashretention.Options{
		Store:         st,
		Logger:        logger.With("subsystem", "trashretention"),
		Clock:         clk,
		RetentionDays: cfg.Server.TrashRetention.RetentionDays,
		SweepInterval: time.Duration(cfg.Server.TrashRetention.SweepIntervalSeconds) * time.Second,
	})
	g.Go(func() error {
		if err := trashRetentionWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "trashretention worker exited", slog.String("err", err.Error()))
			return err
		}
		return nil
	})

	// Whole-mailbox async bulk-mutation drain worker (issue #149/#161,
	// REQ-PROTO-40..48). Bounded by the lifecycle errgroup so shutdown
	// drains it.
	if bulkJobWorker := bundle.srvs.emailBulkJobWorker; bulkJobWorker != nil {
		g.Go(func() error {
			if err := bulkJobWorker.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.LogAttrs(context.Background(), slog.LevelWarn, "email bulk job worker exited", slog.String("err", err.Error()))
				return err
			}
			return nil
		})
	}

	// Attachment-share sweeper (REQ-SHARE-23). Deletes pending shares
	// older than pending_ttl, active shares whose expires_at has passed,
	// and revoked shares whose revoked_grace window has closed. The
	// sweeper runs on the same 60-second cadence as other retention
	// workers; shares expire on minute+ scales so this is plenty
	// fine-grained. Only started when attachment_shares is active.
	if cfg.Server.AttachmentShares.AttachmentSharesActive() {
		shareSweeperCfg := sharesCfg // capture for the goroutine closure
		shareLogger := logger.With("subsystem", "fileshare-sweeper")
		g.Go(func() error {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-gctx.Done():
					return nil
				case <-ticker.C:
					stats, serr := st.Meta().SweepFileShares(gctx, clk.Now(), shareSweeperCfg)
					if serr != nil {
						if errors.Is(serr, context.Canceled) {
							return nil
						}
						shareLogger.LogAttrs(context.Background(), slog.LevelWarn,
							"fileshare sweeper: SweepFileShares",
							slog.String("err", serr.Error()))
						continue
					}
					if stats.DeletedPending > 0 || stats.DeletedExpired > 0 || stats.DeletedRevoked > 0 {
						shareLogger.LogAttrs(context.Background(), slog.LevelDebug,
							"fileshare sweeper: swept",
							slog.Int64("deleted_pending", stats.DeletedPending),
							slog.Int64("deleted_expired", stats.DeletedExpired),
							slog.Int64("deleted_revoked", stats.DeletedRevoked))
					}
				}
			}
		})
	}

	// JMAP clientlog livetail sweeper (REQ-OPS-211). Clears expired
	// clientlog_livetail_until_us column values every 60 s. Bounded by
	// the lifecycle errgroup so shutdown stops it cleanly.
	if suiteSrvs.jmapSrv != nil {
		jmapS := suiteSrvs.jmapSrv
		g.Go(func() error {
			return jmapS.RunLivetailSweeper(gctx)
		})
	}

	// Identity verification GC sweeper (REQ-IDENT-35). Clears expired
	// verification token trios on every tick and destroys unverified
	// Identity rows older than the operator-configured purge window
	// (default 7d). Only started when identity_creation.enabled is
	// true; otherwise the verification flow is inert and the sweeper
	// has no work.
	if cfg.Server.IdentityCreation.IsEnabled() {
		ivSweeper := identityverify.NewSweeper(identityverify.SweeperOptions{
			Store:                st,
			Logger:               logger.With("subsystem", "identityverify-sweeper"),
			Clock:                clk,
			Auditor:              identityVerifyAuditor{st: st},
			Interval:             cfg.Server.IdentityCreation.GCIntervalDuration(),
			UnverifiedPurgeAfter: cfg.Server.IdentityCreation.UnverifiedPurgeAfterDuration(),
		})
		g.Go(func() error {
			if err := ivSweeper.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.LogAttrs(context.Background(), slog.LevelWarn,
					"identityverify sweeper exited", slog.String("err", err.Error()))
				return err
			}
			return nil
		})
	}

	// Held-submission retry loop (REQ-AUTH-EXT-SUBMIT-05, re #131).
	// Periodically finds identities with parked (held_for_reauth=true) email
	// submissions and calls Retryer.RetryForIdentity for each. Each retry
	// attempt calls Submitter.Submit which performs on-demand OAuth token
	// refresh via accessToken when RefreshDue has passed (re #131). This
	// replaces the proactive OAuth token-refresh sweeper: refresh tokens are
	// long-lived; they only need to be used when a send is attempted, not on
	// a fixed schedule.
	if cfg.Server.ExternalSubmission.Enabled && extRetryer != nil {
		heldRetryInterval := 5 * time.Minute
		g.Go(func() error {
			t := clk.After(heldRetryInterval)
			for {
				select {
				case <-gctx.Done():
					return nil
				case <-t:
				}
				ids, err := st.Meta().ListIdentitiesWithHeldSubmissions(gctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					logger.LogAttrs(context.Background(), slog.LevelWarn,
						"held-submission retry: list identities",
						slog.String("err", err.Error()))
				}
				for _, identityID := range ids {
					sub, err := st.Meta().GetIdentitySubmission(gctx, identityID)
					if err != nil {
						logger.LogAttrs(context.Background(), slog.LevelWarn,
							"held-submission retry: get submission",
							slog.String("identity_id", identityID),
							slog.String("err", err.Error()))
						continue
					}
					extRetryer.RetryForIdentity(gctx, identityID, sub)
				}
				t = clk.After(heldRetryInterval)
			}
		})
	}

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

	// Runtime snapshot for Reload. sharedCfg is the same atomic that
	// was handed to composeAdminAndUI so JMAP handlers see the live
	// value on every ReloadConfig call.
	rt := &Runtime{cfg: sharedCfg, level: levelVar, logger: logger}

	// Port report file: written after all listeners are bound, before
	// marking ready, so test harnesses and dev launchers can discover
	// kernel-assigned ports (port 0 binds). Removed on graceful shutdown.
	if cfg.Server.PortReportFile != "" {
		if err := writePortReportFile(cfg.Server.PortReportFile, boundListeners.boundAddrs); err != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "port_report_file: write failed",
				slog.String("path", cfg.Server.PortReportFile),
				slog.String("err", err.Error()),
				slog.String("activity", "system"),
			)
		}
	}

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
		// Remove port report file on graceful shutdown (best-effort).
		if cfg.Server.PortReportFile != "" {
			if err := os.Remove(cfg.Server.PortReportFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.LogAttrs(context.Background(), slog.LevelDebug,
					"port_report_file: remove failed",
					slog.String("path", cfg.Server.PortReportFile),
					slog.String("err", err.Error()),
					slog.String("activity", "system"),
				)
			}
		}
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
	return openStoreWithBulk(ctx, cfg, logger, clk, false)
}

// openStoreBulk opens the store in a configuration tuned for bulk
// inserts: the SQLite backend uses synchronous=OFF, a 256 MiB cache,
// and a higher wal_autocheckpoint. The Postgres backend ignores the
// flag (its commit cost is dominated by network round-trips, not
// fsync). Used by `herold import gmail` (REQ-IMPORT-29 / REQ-IMPORT-34).
func openStoreBulk(ctx context.Context, cfg *sysconfig.Config, logger *slog.Logger, clk clock.Clock) (store.Store, error) {
	return openStoreWithBulk(ctx, cfg, logger, clk, true)
}

func openStoreWithBulk(ctx context.Context, cfg *sysconfig.Config, logger *slog.Logger, clk clock.Clock, bulk bool) (store.Store, error) {
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
				BulkImport:        bulk,
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

func buildTLSStore(cfg *sysconfig.Config, logger *slog.Logger) (*heroldtls.Store, []heroldtls.WatchEntry, error) {
	store := heroldtls.NewStore()
	var fallback *tls.Certificate
	var watchEntries []heroldtls.WatchEntry

	// Admin TLS: file source loads immediately; acme source defers to the
	// ACME client which populates the store after account registration.
	switch cfg.Server.AdminTLS.Source {
	case "file":
		cert, err := heroldtls.LoadFromFile(cfg.Server.AdminTLS.CertFile, cfg.Server.AdminTLS.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("admin: admin_tls load: %w", err)
		}
		fallback = cert
		store.SetDefault(cert)
		// Watch this pair; reload -> SetDefault (Hostname is empty).
		watchEntries = append(watchEntries, heroldtls.WatchEntry{
			CertFile: cfg.Server.AdminTLS.CertFile,
			KeyFile:  cfg.Server.AdminTLS.KeyFile,
		})
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
			return nil, nil, fmt.Errorf("admin: listener %q cert: %w", l.Name, err)
		}
		store.Add(cfg.Server.Hostname, cert)
		e := heroldtls.WatchEntry{
			CertFile: l.CertFile,
			KeyFile:  l.KeyFile,
			Hostname: cfg.Server.Hostname,
		}
		if fallback == nil {
			store.SetDefault(cert)
			fallback = cert
			// This entry is the fallback: reload also calls SetDefault.
			e.SetFallback = true
		}
		watchEntries = append(watchEntries, e)
	}
	_ = logger
	return store, watchEntries, nil
}

// listenerServeFn is the shape of one bound listener's serve loop. It
// runs until the supplied ctx cancels or the listener fails; the
// returned error is propagated through the StartServer errgroup.
type listenerServeFn func(ctx context.Context) error

// boundListeners tracks the net.Listener instances bound by StartServer
// so a deferred Close can tear them down. The serveFns slice carries
// one closure per listener; StartServer launches them under an errgroup.
// boundAddrs records the first (canonical) resolved address per listener
// name — used to write [server].port_report_file when port 0 binds are
// in use.
type boundListenerSet struct {
	listeners  []net.Listener
	serveFns   []namedServe
	boundAddrs []namedBoundAddr
	logger     *slog.Logger
}

// namedBoundAddr pairs a listener config name with the kernel-resolved
// address string (host:port) after a successful net.Listen call.
type namedBoundAddr struct {
	name string
	addr string
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
	mssvServer *protomanagesieve.Server,
	bundle composedHandlers,
	opts StartOpts,
) (*boundListenerSet, error) {
	set := &boundListenerSet{logger: logger}
	// Bind HTTP listeners last per REQ-OPS lifecycle.
	var adminBinds []sysconfig.ListenerConfig
	for _, l := range cfg.Listener {
		if l.Protocol == "http" {
			adminBinds = append(adminBinds, l)
			continue
		}
		lns, fns, canonical, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, mssvServer, bundle, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		for i, ln := range lns {
			set.listeners = append(set.listeners, ln)
			set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fns[i]})
		}
		if canonical != "" {
			set.boundAddrs = append(set.boundAddrs, namedBoundAddr{name: l.Name, addr: canonical})
		}
	}
	for _, l := range adminBinds {
		lns, fns, canonical, err := bindOne(ctx, cfg, logger, l, tlsStore, smtpServer, imapServer, mssvServer, bundle, opts)
		if err != nil {
			set.Close()
			return nil, err
		}
		for i, ln := range lns {
			set.listeners = append(set.listeners, ln)
			set.serveFns = append(set.serveFns, namedServe{name: l.Name, fn: fns[i]})
		}
		if canonical != "" {
			set.boundAddrs = append(set.boundAddrs, namedBoundAddr{name: l.Name, addr: canonical})
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
//
// The returned canonicalAddr is the kernel-resolved address of the first
// (canonical) socket — used to build the port report file when port 0
// binds are in use.
func bindOne(
	ctx context.Context,
	cfg *sysconfig.Config,
	logger *slog.Logger,
	l sysconfig.ListenerConfig,
	tlsStore *heroldtls.Store,
	smtpServer *protosmtp.Server,
	imapServer *protoimap.Server,
	mssvServer *protomanagesieve.Server,
	bundle composedHandlers,
	opts StartOpts,
) ([]net.Listener, []listenerServeFn, string, error) {
	addrs, err := sysconfig.ResolveBindAddresses(l.Address)
	if err != nil {
		return nil, nil, "", fmt.Errorf("admin: listen %s: %w", l.Name, err)
	}
	var (
		listeners     []net.Listener
		serves        []listenerServeFn
		canonicalAddr string
	)
	closeAll := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	for _, addr := range addrs {
		ln, fn, resolvedAddr, err := bindOneAddress(ctx, cfg, logger, l, addr, tlsStore, smtpServer, imapServer, mssvServer, bundle, opts, len(listeners) == 0)
		if err != nil {
			closeAll()
			return nil, nil, "", err
		}
		listeners = append(listeners, ln)
		serves = append(serves, fn)
		if canonicalAddr == "" {
			canonicalAddr = resolvedAddr
		}
	}
	return listeners, serves, canonicalAddr, nil
}

// bindOneAddress binds a single host:port and wires the protocol-specific
// serve function. The publishAddr flag controls whether this socket's
// resolved address is recorded in opts.ListenerAddrs; when bindOne expands
// a localhost listener into two sockets we publish only the first
// (IPv4) address so the existing test fixture contract — one entry per
// listener name — is preserved.
//
// The third return value is the kernel-resolved address string (host:port)
// of the bound socket, always populated on success regardless of publishAddr.
func bindOneAddress(
	ctx context.Context,
	cfg *sysconfig.Config,
	logger *slog.Logger,
	l sysconfig.ListenerConfig,
	bindAddr string,
	tlsStore *heroldtls.Store,
	smtpServer *protosmtp.Server,
	imapServer *protoimap.Server,
	mssvServer *protomanagesieve.Server,
	bundle composedHandlers,
	opts StartOpts,
	publishAddr bool,
) (net.Listener, listenerServeFn, string, error) {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, nil, "", fmt.Errorf("admin: listen %s (%s): %w", l.Name, bindAddr, err)
	}
	resolvedAddr := ln.Addr().String()
	if publishAddr && opts.ListenerAddrs != nil {
		if opts.ListenerAddrsMu != nil {
			opts.ListenerAddrsMu.Lock()
			opts.ListenerAddrs[l.Name] = resolvedAddr
			opts.ListenerAddrsMu.Unlock()
		} else {
			opts.ListenerAddrs[l.Name] = resolvedAddr
		}
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "listener bound",
		slog.String("name", l.Name),
		slog.String("protocol", l.Protocol),
		slog.String("tls", l.TLS),
		slog.String("addr", resolvedAddr),
	)
	// For mail protocol listeners with proxy_protocol = true, wrap
	// the listener so conn.RemoteAddr() returns the decoded real
	// client address. The protocol servers call conn.RemoteAddr()
	// directly and need no further change (issue #111).
	if l.ProxyProtocol {
		switch l.Protocol {
		case "smtp", "smtp-submission", "imap", "managesieve":
			ln = proxyProtocolListener(ln)
		}
	}
	switch l.Protocol {
	case "smtp":
		return ln, func(ctx context.Context) error {
			return smtpServer.Serve(ctx, ln, protosmtp.ListenerOptions{Mode: protosmtp.RelayIn})
		}, resolvedAddr, nil
	case "smtp-submission":
		mode := protosmtp.SubmissionSTARTTLS
		if l.TLS == "implicit" {
			mode = protosmtp.SubmissionImplicitTLS
		}
		return ln, func(ctx context.Context) error {
			return smtpServer.Serve(ctx, ln, protosmtp.ListenerOptions{Mode: mode, AllowPlainAuth: l.AllowPlainAuth})
		}, resolvedAddr, nil
	case "imap":
		// tls = "implicit" selects implicit-TLS IMAP (port 993,
		// historically protocol = "imap"); "starttls"/"none" serve
		// the STARTTLS variant.
		mode := protoimap.ListenerModeSTARTTLS
		if l.TLS == "implicit" {
			mode = protoimap.ListenerModeImplicit993
		}
		return ln, func(ctx context.Context) error {
			return imapServer.Serve(ctx, ln, protoimap.ListenerOptions{Mode: mode, AllowPlainAuth: l.AllowPlainAuth})
		}, resolvedAddr, nil
	case "managesieve":
		return ln, func(ctx context.Context) error {
			return mssvServer.Serve(ctx, ln, protomanagesieve.ListenerOptions{AllowPlainAuth: l.AllowPlainAuth})
		}, resolvedAddr, nil
	case "http":
		spec := l
		handler := pickHTTPHandler(cfg, l, bundle)
		if l.ProxyProtocol {
			// The listener is fronted by a TLS-terminating reverse
			// proxy that prepends a PROXY-protocol header (issue
			// #106). Decode the header so r.RemoteAddr carries the
			// real client IP, and mark the request as TLS so the
			// handlers that derive an external scheme from r.TLS
			// (the OAuth redirect_uri builder, the clientlog origin)
			// emit https rather than the plaintext loopback hop.
			ln = proxyProtocolListener(ln)
			handler = forwardedHTTPSHandler(handler)
		}
		return ln, func(ctx context.Context) error {
			return serveAdmin(ctx, ln, spec, tlsStore, handler, logger)
		}, resolvedAddr, nil
	default:
		_ = ln.Close()
		_ = cfg
		return nil, nil, "", fmt.Errorf("admin: unknown listener protocol %q", l.Protocol)
	}
}

// writePortReportFile atomically writes the port report TOML file at path.
// Each entry in addrs produces one [[listener]] section with the resolved
// name and address. The write is atomic: the content is written to a
// temporary file in the same directory and then renamed into place so
// readers never see a partial file.
func writePortReportFile(path string, addrs []namedBoundAddr) error {
	var buf strings.Builder
	buf.WriteString("# written by herold after all listeners bound\n")
	for _, a := range addrs {
		buf.WriteString("\n[[listener]]\n")
		buf.WriteString("name = ")
		buf.WriteString(fmt.Sprintf("%q", a.name))
		buf.WriteString("\n")
		buf.WriteString("address = ")
		buf.WriteString(fmt.Sprintf("%q", a.addr))
		buf.WriteString("\n")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".port-report-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("port_report_file: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(buf.String()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("port_report_file: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("port_report_file: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("port_report_file: rename: %w", err)
	}
	return nil
}

// pickHTTPHandler returns the http.Handler for a single listener entry.
// Since re #58 both bundle.public and bundle.admin point to the same
// unified handler; the kind value determines which logical surface the
// operator intended but the handler is identical. A listener without a
// Kind (dev-mode only) receives bundle.public directly.
func pickHTTPHandler(cfg *sysconfig.Config, l sysconfig.ListenerConfig, bundle composedHandlers) http.Handler {
	_ = cfg
	switch l.Kind {
	case sysconfig.ListenerKindAdmin:
		// Deprecated: kind="admin" was the loopback admin listener retired
		// in re #58. Operators should remove this stanza. During the
		// transition period it routes to the same public handler so
		// existing configs keep working.
		if bundle.admin != nil {
			return bundle.admin
		}
		return bundle.public
	default:
		// kind="public" or no-Kind dev-mode.
		if bundle.public != nil {
			return bundle.public
		}
		return bundle.admin
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

// proxyProtocolListener wraps ln so accepted connections are decoded
// as HAProxy PROXY protocol (v1 and v2) and the decoded client
// address surfaces via Conn.RemoteAddr -- and therefore as
// http.Request.RemoteAddr. The REQUIRE policy rejects any connection
// that does not send a PROXY header: a proxy_protocol listener is
// loopback-bound and reachable only by the reverse proxy, so a
// missing header is a misdirected or spoofing client (issue #106).
func proxyProtocolListener(ln net.Listener) net.Listener {
	return &proxyproto.Listener{
		Listener:          ln,
		ReadHeaderTimeout: 10 * time.Second,
		ConnPolicy: func(proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			return proxyproto.REQUIRE, nil
		},
	}
}

// forwardedHTTPSHandler marks every request as having arrived over
// TLS. A proxy_protocol listener is plaintext on the loopback hop but
// fronted by a TLS-terminating proxy; setting r.TLS non-nil makes the
// handlers that derive an external scheme from r.TLS emit https. No
// handler reads r.TLS sub-fields, so an empty ConnectionState is
// sufficient (issue #106).
func forwardedHTTPSHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			r.TLS = &tls.ConnectionState{}
		}
		next.ServeHTTP(w, r)
	})
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
	jmapSrv         *protojmap.Server
	// emailBulkJobWorker drains whole-mailbox async bulk-mutation jobs
	// created via Email/setByQuery (issue #149/#161, REQ-PROTO-40..48).
	// StartServer runs it under the lifecycle errgroup alongside the
	// other per-principal sweep workers.
	emailBulkJobWorker *jmapemail.BulkJobWorker
}

// composedHandlers is the bundle of HTTP handlers the bind path installs on
// each listener. Since re #58 both public and admin point to the same unified
// handler; admin is retained for the transition period while operators remove
// the kind="admin" listener stanza from their configs.
type composedHandlers struct {
	public http.Handler
	admin  http.Handler
	srvs   suiteServers
}

// composeAdminAndUI assembles the single public HTTP handler that serves
// all herold surfaces (re #58): the suite SPA, JMAP, the admin SPA at
// /admin/, the full protoadmin REST surface at /api/v1/, image proxy,
// chat WS, call credentials, webhook ingress, and the public login flow.
//
// The admin SPA and admin REST routes are gated by ScopeAdmin in the
// session cookie. ScopeAdmin is issued at login only to principals flagged
// as admin AND who completed TOTP step-up (REQ-AUTH-SCOPE-03). Non-admin
// principals receive the end-user scope set; admin routes return 403.
//
// /metrics and /debug/pprof/ remain 404 on the public listener. A
// dedicated MetricsBind listener is the correct scrape endpoint.
//
// The bundle.admin field is set to bundle.public so the binding code can
// still use a kind="admin" listener during the operator config rollout
// period without breaking. Once all operators have removed the admin
// listener stanza, the kind="admin" handling and bundle.admin will be
// removed.
//
// The second return value carries the protocall, protochat, send-server,
// webpush-dispatcher, and jmap-server handles so StartServer can register
// their shutdown hooks against the lifecycle errgroup.
func composeAdminAndUI(
	ctx context.Context,
	cfg *sysconfig.Config,
	cfgPtr *atomic.Pointer[sysconfig.Config],
	st store.Store,
	dir *directory.Directory,
	oidcRP *directoryoidc.RP,
	clk clock.Clock,
	logger *slog.Logger,
	ftsIndex *storefts.Index,
	tlsStore *heroldtls.Store,
	outboundQ *queue.Queue,
	adminServer *protoadmin.Server,
	smtpSrv *protosmtp.Server,
	webhookSigningKey []byte,
	shareSigningKey []byte,
	sharesCfg store.FileSharesConfig,
	health *observe.Health,
	sieveInterp *sieve.Interpreter,
	extSubmitter *extsubmit.Submitter,
	clientEmitter protoadmin.ClientlogEmitter,
	telemetryGate protoadmin.TelemetryGate,
	imapImportDataKey []byte,
	mlistTokenSigner *maillist.TokenSigner,
) (composedHandlers, error) {
	// ftsIndex is the chat-side full-text search backend (Wave 2.9.6
	// Track D, REQ-CHAT-80..82). It is the same Bleve index the mail
	// FTS worker writes to; jmapchat.RegisterWithFTS below uses it so
	// Message/query routes free-text filters through SearchChatMessages.
	adminHandler := adminServer.Handler()
	var bundle composedHandlers

	// Public-listener session resolver (Phase 3c-iii). Built as a
	// closure over authsession.ResolveSession so siblings that need
	// cookie auth (image proxy, chat, call, JMAP) no longer depend on
	// the deleted internal/protoui package. The cookie config is the
	// same one protoadmin uses when issuing the cookie, so HMAC
	// verification succeeds.
	publicCookieCfg := publicSessionCookieConfig(cfg, logger)
	publicSessionResolver := func(r *http.Request) (store.PrincipalID, bool) {
		return authsession.ResolveSession(r, publicCookieCfg, st, clk)
	}
	publicSessionWithScopeResolver := func(r *http.Request) (store.PrincipalID, auth.ScopeSet, bool) {
		return authsession.ResolveSessionWithScope(r, publicCookieCfg, st, clk)
	}

	// ----- Single unified public handler (re #58) -----
	// The admin SPA, admin REST API, and all end-user surfaces live on
	// the same public listener. ScopeAdmin in the session cookie gates
	// the admin surfaces; the session is issued only when the principal
	// has PrincipalFlagAdmin AND has completed TOTP step-up.
	publicMux := http.NewServeMux()

	// Admin Svelte SPA at /admin/ (re #58). Access is gated by ScopeAdmin
	// in the session cookie; the SPA itself redirects to login when the
	// session is absent or lacks ScopeAdmin.
	adminSPA, err := webspa.NewAdmin(webspa.AdminOptions{
		Logger:        logger.With("subsystem", "webspa.admin"),
		AdminAssetDir: cfg.Server.AdminSPA.AssetDir,
		ClientLog:     clientLogBootstrap(cfg),
		BuildSHA:      buildSHA(),
		BuildTime:     buildTime(),
	})
	if err != nil {
		return composedHandlers{}, fmt.Errorf("admin: admin SPA: %w", err)
	}
	publicMux.Handle("/admin/",
		http.StripPrefix("/admin",
			withPanicRecover(logger.With("subsystem", "webspa.admin"),
				"webspa.admin", adminSPA.Handler())))
	// Standalone manual at /admin/manual/ -- public, no session check.
	// Mounted before /admin/ catch-all so longest-prefix routing gives
	// this handler priority over the admin SPA handler.
	manualSPA, err := webspa.NewManual(webspa.ManualOptions{
		Logger: logger.With("subsystem", "webspa.manual"),
	})
	if err != nil {
		return composedHandlers{}, fmt.Errorf("admin: manual SPA: %w", err)
	}
	publicMux.Handle("/admin/manual/",
		http.StripPrefix("/admin/manual",
			withPanicRecover(logger.With("subsystem", "webspa.manual"),
				"webspa.manual", manualSPA.Handler())))
	// Legacy /ui/* paths -> 308 to /admin/ so older bookmarks land on the
	// new SPA without breaking.
	publicMux.HandleFunc("/ui/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
	})
	publicMux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
	})

	// Full protoadmin REST surface (login, logout, auth/me, principals,
	// domains, aliases, queue, certs, TOTP enrollment, API keys, spam,
	// webhooks, OIDC, clientlog, identities, tagged addresses, healthz,
	// verify-identity, ...) -- all at /api/v1/ on the public listener.
	// admin-only routes enforce ScopeAdmin in requireScope. End-user
	// routes (TOTP enrollment, API-key management, settings, clientlog)
	// enforce end-user scopes and are reachable with the normal session.
	taggedAdminHandler := protoadmin.WithListenerTag("public", adminHandler)
	publicMux.Handle("/api/v1/", taggedAdminHandler)
	// /verify-identity sits outside /api/v1/ so the link in the
	// verification email stays short. Route it to the tagged admin handler
	// so the same requireAuth + store logic applies.
	publicMux.Handle("/verify-identity", taggedAdminHandler)

	// /metrics must NOT be served by the public listener. Register an
	// explicit 404 so the suite SPA catch-all does not absorb the path.
	// The dedicated MetricsBind listener is the correct scrape endpoint.
	publicMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	// /debug/pprof/* -- explicit 404 on the public mux so the SPA
	// catch-all does not route a profile request to the SPA shell.
	publicMux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

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
		SessionResolver:       publicSessionWithScopeResolver,
		SessionCookieConfig:   &publicCookieCfg,
		DefaultMethodDeadline: cfg.Performance.DefaultDeadline.AsDuration(),
		MethodDeadlines:       jmapMethodDeadlinesFromConfig(cfg.Performance.MethodDeadline),
		// 0 when unset -> protojmap applies its 50 MiB default.
		MaxSizeUpload: cfg.Server.MaxUploadSize.AsInt64(),
	})
	// JMAP Mail core handlers: Mailbox/* + Email/* + Sieve/* +
	// per-account capability provider (REQ-PROTO-41, REQ-PROTO-53,
	// REQ-PROTO-56). The top-level jmapmail.Register bundles all three;
	// Thread, SearchSnippet, and VacationResponse have separate entry
	// points because jmapmail.Register does not include them.
	jmapmail.RegisterWithOptions(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-mail"), clk,
		jmapmail.RegisterOptions{
			ExtImg:   extimg.FromSysConfig(cfg.ExternalImages, cfg.Server.Hostname),
			Hostname: cfg.Server.Hostname,
		})
	// Whole-mailbox async bulk-mutation drain worker (issue #149/#161,
	// REQ-PROTO-40..48). Email/setByQuery + EmailBulkJob/get are
	// registered above by jmapmail.RegisterWithOptions (via the mail/email
	// subpackage); the worker that actually applies each job's patch runs
	// separately so StartServer can bind it to the lifecycle errgroup like
	// the other per-principal sweep workers.
	bundle.srvs.emailBulkJobWorker = jmapemail.NewBulkJobWorker(jmapemail.BulkJobWorkerOptions{
		Store:  st,
		Logger: logger.With("subsystem", "jmap-email-bulk-job"),
		Clock:  clk,
	})
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
	//
	// The verification-trigger hook (REQ-IDENT-30) is wired below
	// when identity_creation.enabled is true: it generates the
	// per-identity token + 6-digit code, persists their sha256
	// hashes with a 24h expiry, composes the multipart/alternative
	// verification email, and hands it to the outbound queue (DKIM-
	// signed under the canonical domain). A nil hook disables the
	// email round-trip; the row still commits unverified, and the
	// REST resend endpoint (REQ-IDENT-41) can be used later.
	//
	// The external-domain policy hook (REQ-IDENT-20) reads
	// [server.identity_creation].external_domains; the allowlist mode
	// matches the lowercased domain against the configured set.
	var identityVerifyTrigger jmapidentity.VerificationTrigger
	var ivResender *identityverify.Dispatcher
	if cfg.Server.IdentityCreation.Enabled == nil || *cfg.Server.IdentityCreation.Enabled {
		ivDispatcher := identityverify.New(identityverify.Options{
			Store:          st,
			Submitter:      outboundQ,
			Logger:         logger.With("subsystem", "identityverify"),
			Clock:          clk,
			PublicBaseURL:  publicBaseURL(cfg.Server),
			VerifierFrom:   cfg.Server.IdentityCreation.VerifierFrom,
			Auditor:        identityVerifyAuditor{st: st},
			ResendCooldown: time.Duration(cfg.Server.IdentityCreation.ResendCooldownSeconds) * time.Second,
			ResendDailyCap: cfg.Server.IdentityCreation.ResendDailyCap,
		})
		if err := ivDispatcher.Validate(); err != nil {
			logger.Warn("identity verification dispatcher disabled (validation failed)",
				slog.String("subsystem", "identityverify"),
				slog.String("err", err.Error()))
		} else {
			identityVerifyTrigger = func(ctx context.Context, row store.JMAPIdentity) error {
				_, err := ivDispatcher.Trigger(ctx, row)
				return err
			}
			ivResender = ivDispatcher
		}
	}
	// Wire the resend dispatcher into the protoadmin server so the suite
	// SPA can POST /api/v1/identities/{id}/verify-request (REQ-IDENT-36).
	// Nil resender disables the endpoint (503).
	if ivResender != nil {
		cooldown := time.Duration(cfg.Server.IdentityCreation.ResendCooldownSeconds) * time.Second
		cap := cfg.Server.IdentityCreation.ResendDailyCap
		adapter := ivResenderAdapter{d: ivResender}
		adminServer.SetVerificationResender(adapter, cooldown, cap)
	}
	identityOpts := jmapidentity.Options{
		VerificationTrigger: identityVerifyTrigger,
		ExternalDomainPolicy: buildIdentityExternalDomainPolicy(
			cfg.Server.IdentityCreation),
	}
	jmapIdentityStore := jmapidentity.RegisterWithOptions(
		jmapSrv.Registry(), st,
		logger.With("subsystem", "jmap-identity"), clk, identityOpts)
	// REQ-IDENT-11: advertise the herold-namespaced identity-verification
	// capability when [server.identity_creation].enabled is true (default
	// true). The descriptor is empty for v1; the verifiedAt property is
	// always present on Identity objects regardless of advertisement,
	// matching REQ-IDENT-10's "additive, never blocks normal field access".
	if cfg.Server.IdentityCreation.Enabled == nil || *cfg.Server.IdentityCreation.Enabled {
		jmapSrv.Registry().RegisterCapabilityDescriptor(
			protojmap.CapabilityIdentityVerification, struct{}{})
		logger.Info("identity verification enabled",
			slog.String("subsystem", "jmap-identity"))
	}
	// External SMTP submission (REQ-AUTH-EXT-SUBMIT-05): wire the
	// extsubmit.Submitter when [server.external_submission].enabled is true.
	// The Submitter was pre-built in StartServer (Phase-4 fix) so the same
	// instance serves both the JMAP path here and the REST probe in protoadmin.
	var extSub emailsubmission.ExternalSubmitter
	var extRouter emailsubmission.ExternalRouter
	if cfg.Server.ExternalSubmission.Enabled && extSubmitter != nil {
		extSub = extSubmitter
		extRouter = jmapIdentityStore
		jmapSrv.Registry().RegisterCapabilityDescriptor(
			protojmap.CapabilityExternalSubmission, struct{}{})
		logger.Info("external SMTP submission enabled",
			slog.String("subsystem", "jmap-emailsubmission"))
	}
	emailsubmission.Register(jmapSrv.Registry(), st, outboundQ, jmapIdentityStore,
		extSub, extRouter,
		logger.With("subsystem", "jmap-emailsubmission"), clk)
	// Directory autocomplete (compose-window address autocomplete). The
	// capability is advertised when mode != "off". The modeFn closure
	// reads cfgPtr on every request so a SIGHUP reload takes effect
	// without a restart (REQ-PROTO-DA-01, if assigned).
	if cfg.Server.DirectoryAutocomplete.Mode != sysconfig.DirectoryAutocompleteModeOff {
		modeFn := func() sysconfig.DirectoryAutocompleteMode {
			return cfgPtr.Load().Server.DirectoryAutocomplete.Mode
		}
		jmapSrv.Registry().RegisterCapabilityDescriptor(
			protojmap.CapabilityDirectoryAutocomplete,
			struct {
				Mode string `json:"mode"`
			}{Mode: string(cfg.Server.DirectoryAutocomplete.Mode)},
		)
		jmapchat.RegisterDirectorySearch(jmapSrv.Registry(), st, modeFn)
		logger.Info("directory autocomplete enabled",
			slog.String("subsystem", "jmap-directory-autocomplete"),
			slog.String("mode", string(cfg.Server.DirectoryAutocomplete.Mode)))
	}
	// Tagged addresses (REQ-TAG-70). The capability is advertised when
	// [server.tagged_addresses].enabled is true (captured at boot like
	// external-submission: a SIGHUP that flips enabled=false does not
	// retract the capability from already-issued sessions). The
	// per-principal caps live in sysconfig and are read at request time
	// by the future TaggedAddressFilter/set handler via cfgPtr.Load();
	// this Phase wires only the capability emission and the boot-time
	// log line so the SPA can detect support.
	if cfg.Server.TaggedAddresses.TaggedAddressesEnabled() {
		// jmaptaggedaddress.Register installs the TaggedAddressFilter
		// get / set / changes handlers and also (re-)registers the
		// capability descriptor under the same URI the gate-only path
		// previously emitted. The caps closure reads cfgPtr on every
		// request so a SIGHUP-driven cap adjustment takes effect
		// without a restart (REQ-TAG-86).
		jmaptaggedaddress.Register(jmapSrv.Registry(), st,
			logger.With("subsystem", "jmap-tagged-addresses"), clk,
			func() (int, int) {
				live := cfgPtr.Load().Server.TaggedAddresses
				return live.MaxFiltersPerPrincipal, live.MaxDismissalsPerPrincipal
			})
		logger.Info("tagged-addresses enabled",
			slog.String("subsystem", "jmap-tagged-addresses"),
			slog.Int("max_filters", cfg.Server.TaggedAddresses.MaxFiltersPerPrincipal),
			slog.Int("max_dismissals", cfg.Server.TaggedAddresses.MaxDismissalsPerPrincipal))
	}
	// Attachment shares (REQ-SHARE-40). The capability is only advertised
	// when attachment_shares is fully active (enabled=true AND
	// public_base_url is set). A SIGHUP that changes the flag does not
	// retract an already-issued session capability; the operator must
	// restart to remove it. shareSigningKey is non-nil exactly when
	// AttachmentSharesActive() is true (gated at boot by StartServer).
	if cfg.Server.AttachmentShares.AttachmentSharesActive() {
		jmapfileshare.Register(jmapSrv.Registry(), st,
			logger.With("subsystem", "jmap-fileshare"), clk,
			sharesCfg,
			cfg.Server.AttachmentShares.PublicBaseURL)
		logger.Info("attachment-shares enabled",
			slog.String("subsystem", "jmap-fileshare"),
			slog.String("public_base_url", cfg.Server.AttachmentShares.PublicBaseURL))
	}
	// IMAP import (REQ-IMAP-IMP-61, wave 5). Registered unconditionally —
	// no on/off gate; every server advertises the capability. The data key
	// may be nil when data_key_ref is unconfigured; the JMAP set handlers
	// gate-check it themselves (returning serverFail when absent) so boot
	// does not fail. REQ-IMAP-IMP-70.
	jmapimapimport.Register(jmapSrv.Registry(), st,
		logger.With("subsystem", "jmap-imap-import"), clk,
		imapImportDataKey)

	// SeenAddress (REQ-MAIL-11e..m): recipient autocomplete history, exposed
	// under urn:ietf:params:jmap:mail (no new capability URI needed).
	jmapseenaddress.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-seenaddress"), clk)
	// JMAP Contacts (REQ-PROTO-55): AddressBook/* and Contact/* method
	// families. Registering here advertises CapabilityJMAPContacts in the
	// JMAP session's primaryAccounts map so the suite SPA can resolve a
	// contacts accountId for Contact/set ("Add Contact") and Contact/get.
	jmapcontacts.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-contacts"), clk)
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
	// FCM transport (re #200): a second, independent delivery backend
	// for the native Android client, alongside Web Push above. Absent
	// [server.push].fcm_service_account_json_env/_file, fcmSender
	// stays nil and the dispatcher logs-and-skips FCM-kind
	// subscriptions rather than erroring — the same "unconfigured is a
	// valid posture" shape VAPID uses.
	var fcmSender *fcm.Sender
	if ref := cfg.Server.Push.FCMServiceAccountJSONRef(); ref != "" {
		raw, err := sysconfig.ResolveSecretStrict(ref)
		if err != nil {
			logger.Warn("fcm: failed to resolve FCM service-account JSON; FCM push disabled",
				slog.String("err", err.Error()))
		} else if sender, err := fcm.New(fcm.Options{
			ServiceAccountJSON: []byte(raw),
			ProjectID:          cfg.Server.Push.FCMProjectID,
			HTTPDoer:           pushHTTPClient,
		}); err != nil {
			logger.Warn("fcm: failed to construct FCM sender; FCM push disabled",
				slog.String("err", err.Error()))
		} else {
			fcmSender = sender
			logger.Info("fcm: FCM service-account credential loaded; FCM push enabled")
		}
	}
	pushDispatcher, err := webpush.New(webpush.Options{
		Store:              st,
		VAPID:              vapidMgr,
		FCM:                fcmSender,
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
		limits := jmapchat.DefaultLimits()
		if cfg.Server.Chat.MessageTimestampGroupingSeconds > 0 {
			limits.MessageTimestampGroupingSeconds = cfg.Server.Chat.MessageTimestampGroupingSeconds
		}
		// Link-preview fetcher: enabled by default, hardened by
		// netguard at the dialer + pre-flight resolver layers so a
		// crafted URL in chat body cannot reflect off herold into
		// the operator's internal network. See package
		// internal/linkpreview for the trust posture.
		previewer := linkpreview.New(linkpreview.Options{
			Logger: logger.With("subsystem", "linkpreview"),
		})
		jmapchat.RegisterWithFTSAndLinkPreview(jmapSrv.Registry(), st, ftsIndex,
			previewer,
			logger.With("subsystem", "jmap-chat"), clk, limits)
	}
	bundle.srvs.jmapSrv = jmapSrv
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

	// Hosted mailing lists: the public, token-authorised subscriber
	// surface (REQ-MLIST-57/58/59, issue #184) -- the one-click
	// List-Unsubscribe / RFC 8058 endpoint and the GET self-service
	// management page, both unauthenticated (the token IS the auth).
	// mlistTokenSigner is threaded in as a parameter (nil when no
	// deployment data key is configured; PublicServer then refuses every
	// token as invalid rather than failing to construct, matching the
	// Expander's own TokenSigner-nil degrade posture). publicBaseURL()
	// is the same helper the identity-verification email link uses
	// (re #19) -- must match the Expander.PublicBaseURL that generated
	// the token URL in the first place. mlistPublicServer's own inner
	// mux registers the full "/lists/{id}/unsubscribe" pattern, so this
	// subtree mount passes the request through unmodified (no prefix
	// stripping needed).
	mlistPublicServer := maillist.NewPublicServer(st.Meta(), mlistTokenSigner, st.Blobs(), publicBaseURL(cfg.Server), clk, logger.With("subsystem", "maillist-public"))
	publicMux.Handle("/lists/",
		withPanicRecover(logger.With("subsystem", "maillist-public"),
			"maillist.public", mlistPublicServer.Handler()))

	// Attachment-share public download routes (REQ-SHARE-30..32).
	// Mounted only when attachment_shares is fully active. shareSigningKey
	// is non-nil exactly when AttachmentSharesActive() is true.
	if cfg.Server.AttachmentShares.AttachmentSharesActive() {
		shareSrv := protoshare.NewFromStore(st, protoshare.Options{
			SigningKey: shareSigningKey,
			RateLimit: protoshare.RateLimitConfig{
				RequestsPerWindow: cfg.Server.AttachmentShares.DownloadRequestsPerIPPerShare,
				Window:            cfg.Server.AttachmentShares.DownloadRequestsWindow.AsDuration(),
			},
			Clock:  clk,
			Logger: logger.With("subsystem", "protoshare"),
		})
		shareSrv.RegisterRoutes(publicMux)
	}

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

	// Standalone manual at /manual/ on the public listener -- intentionally
	// PUBLIC, no session check. The same manualSPA instance constructed
	// above is reused here; it is safe for concurrent use. Mount BEFORE
	// the suite SPA catch-all so longest-prefix routing gives it priority.
	publicMux.Handle("/manual/",
		http.StripPrefix("/manual",
			withPanicRecover(logger.With("subsystem", "webspa.manual"),
				"webspa.manual", manualSPA.Handler())))

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
			ClientLog:     clientLogBootstrap(cfg),
			BuildSHA:      buildSHA(),
			BuildTime:     buildTime(),
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
	// Alias admin to public so a kind="admin" listener in operator configs
	// serves the same handler during the transition period (re #58).
	// Operators should remove the kind="admin" listener stanza after
	// deploying this binary; until then it routes to the public handler.
	bundle.admin = bundle.public
	return bundle, nil
}

// defaultSessionKeyEnv is the fixed env var name operators set to provide a
// persistent HMAC-SHA256 signing key for session cookies. Using a well-known
// name means operators do not need to read docs to discover the knob: the WARN
// log line emitted when the key is absent names this variable directly.
const defaultSessionKeyEnv = "HEROLD_UI_SESSION_KEY"

// sessionKeyFilename is the basename of the persisted session signing key
// under <data_dir>/secrets/. The file holds 32 raw bytes (mode 0600,
// owned by the run-as user) and is created on first start when no env var
// override is configured.
const sessionKeyFilename = "ui-session-key"

// resolveSessionSigningKey returns the HMAC-SHA256 signing key to use for
// session cookies. Resolution order:
//
//  1. If [server.ui].signing_key_env is set (explicit TOML override), read
//     that env var. This is the back-compat path for operators who wired the
//     old knob; the key must be >= 32 bytes.
//  2. Otherwise read the predefined env var HEROLD_UI_SESSION_KEY.
//  3. Otherwise persist a key under <data_dir>/secrets/ui-session-key and
//     return that on every subsequent boot. This is the production-default
//     path: session cookies survive restarts without the operator needing
//     to wire an env var (issue #14). The file is created on first start
//     with mode 0600 and re-read on every later start.
//  4. Only when persistence itself fails (unwritable data_dir, missing
//     filesystem permissions) does the function fall back to a fresh
//     ephemeral key; this is logged at WARN so the operator notices.
//
// The persisted file is shared between the admin and public listeners.
// Cross-listener isolation is enforced by distinct cookie names (`herold_admin_session`
// vs `herold_public_session`, REQ-AUTH-COOKIE-PATH), not by separate
// signing keys -- the env-var path also shares one key between the two
// listeners, and we keep that shape for consistency.
func resolveSessionSigningKey(cfg *sysconfig.Config, logger *slog.Logger) []byte {
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
	// Step 3: persist + reuse a key under data_dir.
	if key, err := loadOrCreatePersistedSessionKey(cfg.Server.DataDir); err == nil {
		return key
	} else if logger != nil {
		logger.Warn("session-cookie signing key persistence failed; falling back to ephemeral random key (sessions will be invalidated on every restart)",
			"data_dir", cfg.Server.DataDir, "err", err)
	}
	// Step 4: ephemeral fallback.
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		// rand.Read failing is OS-level catastrophic; panic rather than
		// silently issuing cookies signed with a zero key.
		panic("admin: failed to generate ephemeral session signing key: " + err.Error())
	}
	return key[:]
}

// loadOrCreatePersistedSessionKey reads <data_dir>/secrets/ui-session-key
// when present (and >= 32 bytes), or creates it with 32 fresh random bytes
// on first call. The file is 0600 and the parent directory 0700; the
// run-as user owns both because the herold process is the only thing that
// reads or writes inside data_dir.
//
// Returns an error when data_dir is empty or when the file/directory
// cannot be created (filesystem readonly, permission denied, ...);
// callers fall back to an ephemeral key in that case.
func loadOrCreatePersistedSessionKey(dataDir string) ([]byte, error) {
	if dataDir == "" {
		return nil, errors.New("data_dir is empty")
	}
	dir := filepath.Join(dataDir, "secrets")
	path := filepath.Join(dir, sessionKeyFilename)
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return data, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, fmt.Errorf("rand.Read: %w", err)
	}
	if err := os.WriteFile(path, key[:], 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key[:], nil
}

// adminSessionCookieConfig is the retired admin-listener cookie config
// (herold_admin_session, 8h TTL). Retained for reference; no longer called
// in production paths (re #58). The protoadmin server now uses
// publicSessionCookieConfig with a 7-day TTL via herold_public_session.
//
// clientLogBootstrap derives the bootstrap descriptor injected into the
// SPA's <meta name="herold-clientlog"> tag (REQ-CLOG-12) from the resolved
// [clientlog] config block. When the block is absent applyDefaults has
// already set Enabled=true and the documented values from REQ-OPS-219, so
// this helper is a pure projection.
func clientLogBootstrap(cfg *sysconfig.Config) webspa.ClientLogBootstrap {
	cl := cfg.ClientLog
	const (
		batchMaxEvents = 20
		batchMaxAgeMS  = 5000
		queueCap       = 200
	)
	return webspa.ClientLogBootstrap{
		Enabled:                 cl.ClientLogEnabled(),
		BatchMaxEvents:          batchMaxEvents,
		BatchMaxAgeMS:           batchMaxAgeMS,
		QueueCap:                queueCap,
		TelemetryEnabledDefault: cl.TelemetryEnabledDefault(),
	}
}

// buildSHA returns the VCS revision the binary was built from, or "dev"
// when not available (build flags can override via -ldflags later).
func buildSHA() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 12 {
					return s.Value[:12]
				}
				return s.Value
			}
		}
	}
	return "dev"
}

// buildTime returns the RFC3339 commit timestamp of the built revision
// from the embedded VCS metadata (vcs.time from -buildvcs=true). Returns
// the empty string when not available (dev builds, stripped binaries).
func buildTime() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.time" && s.Value != "" {
				return s.Value
			}
		}
	}
	return ""
}

func adminSessionCookieConfig(cfg *sysconfig.Config, logger *slog.Logger) authsession.SessionConfig {
	signingKey := resolveSessionSigningKey(cfg, logger)
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
		// REQ-AUTH-72 (issue #12): the admin listener gets its own
		// absolute + idle TTL pair, separate from the public-listener
		// SessionTTL. applyDefaults has set both to spec values (8h /
		// 1h) when the operator omitted them.
		TTL:           cfg.Server.UI.AdminAbsoluteTTL.AsDuration(),
		IdleTTL:       cfg.Server.UI.AdminIdleTTL.AsDuration(),
		SecureCookies: secure,
	}
}

// publicSessionCookieConfig extracts the public-listener cookie parameters
// from sysconfig and returns an authsession.SessionConfig used for cookie
// issuance (protoadmin handleLogin) and the authsession-based resolvers
// wired into protoimg, protochat, protocall, and protojmap. All consumers
// share the same SigningKey so HMAC verification succeeds (re #58).
//
// When no persistent signing key is configured, resolveSessionSigningKey
// generates an ephemeral 32-byte key (fixes #6, #7).
func publicSessionCookieConfig(cfg *sysconfig.Config, logger *slog.Logger) authsession.SessionConfig {
	signingKey := resolveSessionSigningKey(cfg, logger)
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
		// IdleTTL is the inactivity window for all web sessions. TTL=0
		// disables the absolute cap; idle-only expiry is the sole mechanism
		// (REQ-AUTH-72, REQ-AUTH-73, issue #78). SessionAbsoluteTTL
		// optionally adds a hard cap; default 0 = no cap.
		IdleTTL:       cfg.Server.UI.SessionTTL.AsDuration(),
		TTL:           cfg.Server.UI.SessionAbsoluteTTL.AsDuration(),
		SecureCookies: secure,
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

// retryBlobAdapter adapts store.Blobs to extsubmit.RetryBlobGetter, whose Get
// returns an io.ReadCloser (the narrow surface the Retryer needs) rather than
// the seekable store.BlobReader.
type retryBlobAdapter struct {
	b store.Blobs
}

func (a retryBlobAdapter) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	r, err := a.b.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	return r, nil
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

// telemetryGateAdapter bridges directory.TelemetryGate (IsEnabled takes ctx
// and returns (bool, error)) to the protoadmin.TelemetryGate interface
// (IsEnabled takes only sessionKey and returns bool). The adapter uses
// context.Background() because the clientlog pipeline worker has no
// request context at the gate call-site. Missing sessions (ErrNotFound) are
// treated as telemetry-disabled per REQ-OPS-208 defence-in-depth.
type telemetryGateAdapter struct {
	gate *directory.TelemetryGate
}

func (a *telemetryGateAdapter) IsEnabled(sessionKey string) bool {
	enabled, err := a.gate.IsEnabled(context.Background(), sessionKey)
	if err != nil {
		// ErrNotFound means one of two things:
		//   1. The key is a rate-limit-format string (e.g. "clientlog-auth:1")
		//      used for Bearer API key auth — no session row exists, so the
		//      session-level telemetry setting is not applicable. Allow the
		//      event through (gate open) to preserve backwards compatibility
		//      with API key access.
		//   2. A real session ID that is no longer in the table (expired /
		//      revoked). In this case defence-in-depth would suggest disabling,
		//      but we cannot distinguish case (1) from (2) at this call site
		//      without parsing the key format. We default to allowing (gate open)
		//      so that API key auth is never silently dropped.
		// Any store error other than not-found is treated as gate-open
		// (degrade safely rather than silently drop events).
		return true
	}
	return enabled
}

// notifySystemdReady implements a minimal sd_notify(READY=1) compatible
// with systemd Type=notify without pulling in the coreos/go-systemd
// dependency. If NOTIFY_SOCKET is unset (development, container without
// buildIdentityExternalDomainPolicy returns the DomainPolicy closure
// that the JMAP Identity handler consults when an Identity/set { create }
// payload targets a non-hosted domain (REQ-IDENT-20). The closure is
// pure — it captures the resolved IdentityCreationConfig at boot, so a
// SIGHUP reload that changes the mode requires the existing admin
// reload path to rebuild this closure. Hosted domains never reach this
// hook; the handler always permits them.
//
// Modes:
//   - allow_all (default): permit every external domain.
//   - allowlist: permit only domains in ExternalDomainAllowlist
//     (lowercased, ASCII).
//   - deny_all: refuse every external domain.
//
// identityVerifyAuditor adapts the metadata-store audit-log surface
// to the identityverify.Auditor interface so the dispatcher can emit
// identity.verify.send entries on the queued-message path
// (REQ-IDENT-43 / REQ-IDENT-90) without taking a dependency on the
// store package's verbose signature.
type identityVerifyAuditor struct {
	st store.Store
}

func (a identityVerifyAuditor) Append(ctx context.Context, entry store.AuditLogEntry) error {
	return a.st.Meta().AppendAuditLog(ctx, entry)
}

// ivResenderAdapter narrows the *identityverify.Dispatcher.Resend
// signature down to the (ctx, row) -> error shape that protoadmin's
// VerificationResender interface expects. The dispatcher's full
// signature returns Tokens (for tests); production REST callers only
// need the error.
type ivResenderAdapter struct {
	d *identityverify.Dispatcher
}

func (a ivResenderAdapter) Resend(ctx context.Context, row store.JMAPIdentity) error {
	_, err := a.d.Resend(ctx, row)
	return err
}

// publicBaseURL returns the externally-reachable base URL of the public
// listener. It prefers [server].public_base_url; when that is empty it
// falls back to "https://<hostname>" so single-domain deployments that
// omit public_base_url get a working default. The identity-verification
// email link and Message-ID use this value so they reflect the SPA
// origin, not the internal MTA hostname (re #19).
func publicBaseURL(sc sysconfig.ServerConfig) string {
	if sc.PublicBaseURL != "" {
		return sc.PublicBaseURL
	}
	return "https://" + sc.Hostname
}

func buildIdentityExternalDomainPolicy(ic sysconfig.IdentityCreationConfig) func(string) bool {
	switch ic.ExternalDomains {
	case sysconfig.IdentityCreationExternalDomainsDenyAll:
		return func(string) bool { return false }
	case sysconfig.IdentityCreationExternalDomainsAllowlist:
		allow := make(map[string]struct{}, len(ic.ExternalDomainAllowlist))
		for _, d := range ic.ExternalDomainAllowlist {
			allow[strings.ToLower(d)] = struct{}{}
		}
		return func(dom string) bool {
			_, ok := allow[strings.ToLower(dom)]
			return ok
		}
	default:
		// allow_all (the documented default; also chosen when the
		// section is omitted entirely thanks to applyDefaults).
		return func(string) bool { return true }
	}
}

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
