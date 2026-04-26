package sysconfig

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the parsed representation of /etc/herold/system.toml (REQ-OPS-01..08).
// Unknown keys at parse time are errors.
type Config struct {
	Server        ServerConfig        `toml:"server"`
	Acme          *AcmeConfig         `toml:"acme,omitempty"`
	Listener      []ListenerConfig    `toml:"listener"`
	Plugin        []PluginConfig      `toml:"plugin"`
	SMTP          SMTPConfig          `toml:"smtp,omitempty"`
	Observability ObservabilityConfig `toml:"observability"`
}

// SMTPConfig groups SMTP-listener-side knobs that span both the
// inbound (port-25) and submission flows (REQ-DIR-RCPT-* and the
// in-flight Track B / Track C work).
type SMTPConfig struct {
	Inbound SMTPInboundConfig `toml:"inbound,omitempty"`
}

// SMTPInboundConfig carries the per-server inbound SMTP knobs
// configured on the relay-in listener.
type SMTPInboundConfig struct {
	// DirectoryResolveRcptPlugin names the plugin invoked at SMTP
	// RCPT TO time (REQ-DIR-RCPT-02). Empty disables the path.
	DirectoryResolveRcptPlugin string `toml:"directory_resolve_rcpt_plugin,omitempty"`
	// PluginFirstForDomains is the lowercased ASCII recipient-domain
	// set for which the plugin is consulted BEFORE the internal
	// directory (REQ-DIR-RCPT-03 inversion).
	PluginFirstForDomains []string `toml:"plugin_first_for_domains,omitempty"`
	// RcptRateLimitPerIPPerSec is the per-source-IP RCPT cap
	// (REQ-DIR-RCPT-06). Zero applies the directory.DefaultRcptRateLimit
	// default of 50/sec.
	RcptRateLimitPerIPPerSec int `toml:"rcpt_rate_limit_per_ip_per_sec,omitempty"`
	// SpamForSynthetic toggles spam classification on synthetic
	// recipients (REQ-DIR-RCPT-07). Default false: synthetic mail
	// skips the classifier; operators who want classification on
	// transactional intake set this true.
	SpamForSynthetic bool `toml:"spam_for_synthetic,omitempty"`
	// ResolveRcptTimeout overrides the per-method timeout for
	// directory.resolve_rcpt (REQ-PLUG-32). Zero applies the 2s
	// default; values above the 5s hard cap are rejected at Validate.
	ResolveRcptTimeout Duration `toml:"resolve_rcpt_timeout,omitempty"`
}

// ServerConfig carries process-wide settings.
type ServerConfig struct {
	Hostname      string   `toml:"hostname"`
	DataDir       string   `toml:"data_dir"`
	RunAsUser     string   `toml:"run_as_user"`
	RunAsGroup    string   `toml:"run_as_group"`
	ShutdownGrace Duration `toml:"shutdown_grace,omitempty"`
	// DevMode relaxes the production-only validate rules so a single
	// developer config can run the whole suite on one HTTP listener
	// (Wave 3.6). Specifically: when DevMode is true, configs without
	// any kind="admin" listener get a co-mount warn-log instead of a
	// hard validate failure, and a single "admin" listener is allowed
	// to serve both public and admin handlers (the
	// admin handler returns 403 from a public-listener cookie via
	// the scope check; this is the dev-only "trust the operator"
	// posture). Production deployments MUST leave DevMode off and
	// configure both listeners explicitly.
	DevMode    bool             `toml:"dev_mode,omitempty"`
	AdminTLS   AdminTLSConfig   `toml:"admin_tls"`
	Storage    StorageConfig    `toml:"storage"`
	Snooze     SnoozeConfig     `toml:"snooze,omitempty"`
	UI         UIConfig         `toml:"ui,omitempty"`
	ImageProxy ImageProxyConfig `toml:"image_proxy,omitempty"`
	Chat       ChatConfig       `toml:"chat,omitempty"`
	Call       CallConfig       `toml:"call,omitempty"`
	TURN       TURNConfig       `toml:"turn,omitempty"`
	SmartHost  SmartHostConfig  `toml:"smart_host,omitempty"`
	Tabard     TabardConfig     `toml:"tabard,omitempty"`
	Push       PushConfig       `toml:"push,omitempty"`
}

// PushConfig configures the deployment-level VAPID key pair the Web
// Push outbound dispatcher uses (REQ-PROTO-122). The private key is
// referenced via a $VAR or file:/path secret reference per
// STANDARDS §9 / REQ-OPS-04 / REQ-OPS-161; inline values are
// rejected at Validate. When neither field is set the deployment
// has no VAPID configured: the JMAP push capability is still
// advertised but applicationServerKey is omitted, signalling
// clients that Web Push is unavailable.
//
// Operator key generation: see `herold vapid generate`. The
// resulting PEM private key plumbs in via VAPIDPrivateKeyEnv /
// VAPIDPrivateKeyFile.
type PushConfig struct {
	// VAPIDPrivateKeyEnv names the environment variable carrying the
	// PEM-encoded P-256 ECDSA private key (typed as
	// "$HEROLD_VAPID_PRIVATE_KEY"). Mutually exclusive with
	// VAPIDPrivateKeyFile.
	VAPIDPrivateKeyEnv string `toml:"vapid_private_key_env,omitempty"`
	// VAPIDPrivateKeyFile names the file holding the PEM-encoded
	// private key. The file is read once at startup and again on
	// SIGHUP.
	VAPIDPrivateKeyFile string `toml:"vapid_private_key_file,omitempty"`
	// VAPIDSubject is the operator's contact URL or mailto: that the
	// dispatcher embeds in the VAPID JWT's "sub" claim per RFC 8292
	// §2. Empty defaults to "mailto:postmaster@<server.hostname>" at
	// dispatch time. Used in 3.8b; carried here so the operator
	// configures it once.
	VAPIDSubject string `toml:"vapid_subject,omitempty"`
}

// VAPIDPrivateKeyRef returns the operator-supplied secret reference
// for the VAPID private key in the form ResolveSecret accepts:
// "$VAR" when VAPIDPrivateKeyEnv is set, "file:/path" when
// VAPIDPrivateKeyFile is set, and "" when neither — i.e. when Web
// Push is disabled. The two fields are mutually exclusive at
// Validate time so this lookup is deterministic.
func (p PushConfig) VAPIDPrivateKeyRef() string {
	if p.VAPIDPrivateKeyEnv != "" {
		return p.VAPIDPrivateKeyEnv
	}
	if p.VAPIDPrivateKeyFile != "" {
		return "file:" + p.VAPIDPrivateKeyFile
	}
	return ""
}

// TabardConfig configures the embedded tabard SPA mount on the public
// HTTP listener (REQ-DEPLOY-COLOC-01..05). The default packaging
// embeds the tabard build artefacts into the herold binary at release
// time; an explicit AssetDir overrides the embedded FS for development
// hot-reload.
type TabardConfig struct {
	// Enabled selects whether the SPA is mounted on the public
	// listener's catch-all (`/`). Defaults to true. Operators
	// running an admin-only deployment with no consumer suite set
	// this false explicitly; the bare `/` then returns the public
	// listener's default 404.
	Enabled *bool `toml:"enabled,omitempty"`
	// AssetDir, when non-empty, makes the SPA handler serve from
	// this directory instead of the embedded FS. The directory MUST
	// be an absolute path AND contain index.html at startup; the
	// validator refuses to start the server otherwise so a typo is
	// loud at boot rather than at first 404.
	AssetDir string `toml:"asset_dir,omitempty"`
}

// SmartHostConfig drives the optional outbound smart-host relay
// (REQ-FLOW-SMARTHOST-01..08). When Enabled is false the queue worker
// uses the direct-MX path (REQ-FLOW-70..76); otherwise outbound mail
// flows through the configured submission endpoint with TLS, optional
// SASL, and a fallback policy.
//
// Per-domain overrides live under PerDomain keyed by lowercase ASCII
// recipient domain (REQ-FLOW-SMARTHOST-02). Overrides reuse the same
// shape as the top-level block but the inner PerDomain map is ignored
// (no nested overrides) so operators cannot accidentally fan out a
// hierarchy of relays.
//
// Secrets MUST come from PasswordEnv ("$VAR") or PasswordFile
// ("file:/path") per STANDARDS §9 / REQ-OPS-04 / REQ-OPS-161; inline
// passwords are rejected at Validate.
type SmartHostConfig struct {
	// Enabled selects whether the queue worker uses the smart-host
	// path for outbound delivery. Defaults to false.
	Enabled bool `toml:"enabled,omitempty"`
	// Host is the relay's SMTP server hostname. Required when Enabled.
	Host string `toml:"host,omitempty"`
	// Port is the TCP port; 587 (STARTTLS submission) or 465 (implicit
	// TLS submission) are the canonical values; 25 is permitted only
	// for dev-mode relays.
	Port int `toml:"port,omitempty"`
	// TLSMode selects the TLS posture: "starttls", "implicit_tls", or
	// "none". When Port is 587 the default is "starttls"; when 465 the
	// default is "implicit_tls". "none" is refused by Validate when
	// AuthMethod is non-none (never send credentials in plaintext).
	TLSMode string `toml:"tls_mode,omitempty"`
	// AuthMethod selects the SASL mechanism: "plain", "login",
	// "scram-sha-256", "xoauth2", or "none".
	AuthMethod string `toml:"auth_method,omitempty"`
	// Username is the SASL authcid; required when AuthMethod != "none".
	Username string `toml:"username,omitempty"`
	// PasswordEnv names the environment variable carrying the SASL
	// password ("$VAR"). Mutually exclusive with PasswordFile.
	PasswordEnv string `toml:"password_env,omitempty"`
	// PasswordFile names the file holding the SASL password. The file
	// content is trimmed of trailing newlines on read.
	PasswordFile string `toml:"password_file,omitempty"`
	// FallbackPolicy controls direct-MX fallback. Defaults to
	// "smart_host_only" (no fallback).
	FallbackPolicy string `toml:"fallback_policy,omitempty"`
	// ConnectTimeoutSeconds bounds the TCP dial. Default 10.
	ConnectTimeoutSeconds int `toml:"connect_timeout_seconds,omitempty"`
	// ReadTimeoutSeconds bounds the SMTP exchange after dial. Default 30.
	ReadTimeoutSeconds int `toml:"read_timeout_seconds,omitempty"`
	// TLSVerifyMode chooses the upstream cert verification posture:
	// "system_roots" (default), "pinned" (PinnedCertPath required), or
	// "insecure_skip_verify" (dev-only).
	TLSVerifyMode string `toml:"tls_verify_mode,omitempty"`
	// PinnedCertPath is the path to a PEM file containing the trusted
	// upstream certificate when TLSVerifyMode == "pinned".
	PinnedCertPath string `toml:"pinned_cert_path,omitempty"`
	// FallbackAfterFailureSeconds is the sustained-outage threshold
	// for "smart_host_then_mx" before the worker falls back to direct
	// MX. Default 300 (5 min).
	FallbackAfterFailureSeconds int `toml:"fallback_after_failure_seconds,omitempty"`
	// PerDomain maps lowercase recipient-domain keys to overrides.
	// Overrides reuse the same struct shape; the inner PerDomain map
	// is ignored (no nested overrides).
	PerDomain map[string]SmartHostConfig `toml:"per_domain,omitempty"`
}

// CallConfig configures the 1:1 video-call signaling surface
// (REQ-CALL-*). The signaling itself rides the chat WebSocket; this
// block toggles the credential-mint endpoint and exposes the
// per-call ring-window knob (REQ-CALL-06).
type CallConfig struct {
	// Enabled selects whether the credential mint endpoint is
	// mounted and the chat protocol's call.signal handler is
	// registered. Defaults to true; set false to disable video
	// calling entirely.
	Enabled *bool `toml:"enabled,omitempty"`
	// RingTimeoutSeconds is the per-call window the offerer waits
	// for an answer before the server emits a synthetic
	// kind="timeout" signal and writes a missed-call sysmsg
	// (REQ-CALL-06). Default 30; capped at 300 (5 min).
	RingTimeoutSeconds int `toml:"ring_timeout_seconds,omitempty"`
}

// TURNConfig configures the operator-deployed coturn (or equivalent)
// the call surface points clients at (REQ-CALL-OPS).
//
// Per STANDARDS §9 the shared secret MUST come from environment via
// SharedSecretEnv ($VAR) or a file (file:/path); inline secrets are
// rejected at config validate. The herold process reads the env once
// at startup and again on SIGHUP; rotating the secret requires a
// matching rotate of coturn's static-auth-secret.
type TURNConfig struct {
	// URIs is the operator-supplied list of "turn:" / "turns:" URIs
	// herold advertises in mint responses. At least one entry is
	// required when [server.call] is enabled.
	URIs []string `toml:"uris,omitempty"`
	// SharedSecretEnv names the environment variable carrying
	// coturn's static-auth-secret (typed as "$HEROLD_TURN_SECRET"
	// in the TOML). The server resolves it via sysconfig.ResolveSecret.
	SharedSecretEnv string `toml:"shared_secret_env,omitempty"`
	// CredentialTTLSeconds is the requested credential lifetime in
	// seconds. Default 300 (REQ-CALL-22); clamped to MaxCredentialTTL
	// inside internal/protocall.
	CredentialTTLSeconds int `toml:"credential_ttl_seconds,omitempty"`
}

// ChatConfig configures the chat ephemeral channel
// (REQ-CHAT-40..46), an optional WebSocket surface mounted on the
// admin HTTP listener at /chat/ws. Defaults match the architecture
// document and produce a working server out of the box; operators
// override only to widen / narrow the connection caps or the
// heartbeat schedule.
type ChatConfig struct {
	// Enabled selects whether the /chat/ws upgrade handler is
	// mounted. Defaults to true; operators who haven't shipped a
	// chat client yet keep the surface unreachable by setting
	// false.
	Enabled *bool `toml:"enabled,omitempty"`
	// MaxConnections caps the number of concurrent WebSocket
	// connections across all principals. Default 4096.
	MaxConnections int `toml:"max_connections,omitempty"`
	// PerPrincipalCap caps connections per principal (one tab per
	// connection in the typical client). Default 8.
	PerPrincipalCap int `toml:"per_principal_cap,omitempty"`
	// PingIntervalSeconds is the server-to-client ping cadence in
	// seconds (REQ-CHAT-42). Default 30.
	PingIntervalSeconds int `toml:"ping_interval_seconds,omitempty"`
	// PongTimeoutSeconds is the budget in seconds for a client to
	// respond to a ping with a pong before the server closes the
	// connection. Default 60.
	PongTimeoutSeconds int `toml:"pong_timeout_seconds,omitempty"`
	// MaxFrameBytes caps the size of a single inbound WebSocket
	// frame; oversize frames close the connection with code 1009.
	// Default 65536 (64 KiB).
	MaxFrameBytes int `toml:"max_frame_bytes,omitempty"`
	// WriteTimeoutSeconds is the per-frame write deadline, in
	// seconds, applied via net.Conn.SetWriteDeadline before each
	// outbound frame write. A slow consumer that stops draining its
	// TCP receive buffer must not pin the writer indefinitely.
	// Default 10.
	WriteTimeoutSeconds int `toml:"write_timeout_seconds,omitempty"`
	// AllowedOrigins is the operator-supplied set of allowed Origin
	// header values for the /chat/ws upgrade. An empty list is
	// interpreted as "same-origin only": the server matches the
	// Request.Host against the Origin's host. Wildcards are not
	// supported; entries must be the full origin including scheme,
	// e.g. "https://mail.example.com". Mismatched origins yield 403
	// + RFC 7807 problem detail before the upgrade hijack.
	AllowedOrigins []string `toml:"allowed_origins,omitempty"`
	// AllowEmptyOrigin lets non-browser clients (e.g. native chat
	// app over a session cookie) connect without an Origin header.
	// Default false: every connection without an Origin header is
	// rejected with 403, matching browser fetch policy.
	AllowEmptyOrigin bool `toml:"allow_empty_origin,omitempty"`
	// Retention configures the chat retention sweeper
	// (REQ-CHAT-92). Defaults match chatretention package
	// constants; operators rarely override either.
	Retention ChatRetentionConfig `toml:"retention,omitempty"`
}

// ChatRetentionConfig tunes the chat retention sweeper
// (REQ-CHAT-92): how often it scans and how many rows it deletes per
// sweep. The defaults applied at applyDefaults match the package
// constants in internal/chatretention.
type ChatRetentionConfig struct {
	// SweepIntervalSeconds is the cadence at which the sweeper
	// scans for retention-expired chat messages. Default 60 (1
	// minute). Validate rejects values below 10 to avoid pinning a
	// writer transaction; values above 86400 (1 day) are typos.
	SweepIntervalSeconds int `toml:"sweep_interval_seconds,omitempty"`
	// BatchSize is the per-sweep hard-delete ceiling. Default
	// 1000. Validate rejects values below 1 or above 10000 so a
	// typo cannot deadlock the writer or starve other workers.
	BatchSize int `toml:"batch_size,omitempty"`
}

// ImageProxyConfig configures the inbound HTML image proxy
// (REQ-SEND-70..78). Defaults match the requirements; operators
// override only to widen / narrow the byte cap or rate limits.
type ImageProxyConfig struct {
	// Enabled selects whether the /proxy/image handler is mounted.
	// Defaults to true. Operators who want to disable the feature
	// (e.g. behind an upstream proxy that owns image rewriting) set
	// this false.
	Enabled *bool `toml:"enabled,omitempty"`
	// MaxBytes is the per-response upstream byte cap (REQ-SEND-74).
	// Default 25 * 1024 * 1024 (25 MB).
	MaxBytes int64 `toml:"max_bytes,omitempty"`
	// CacheMaxBytes is the total in-memory cache footprint cap
	// (REQ-SEND-75). Default 256 MiB.
	CacheMaxBytes int64 `toml:"cache_max_bytes,omitempty"`
	// CacheMaxEntries is the entry-count cache cap. Default 8192.
	CacheMaxEntries int `toml:"cache_max_entries,omitempty"`
	// CacheMaxAgeSeconds is the upstream-Cache-Control ceiling, in
	// seconds. Default 86400 (24 h).
	CacheMaxAgeSeconds int `toml:"cache_max_age_seconds,omitempty"`
	// PerUserPerMinute caps fetches per principal per minute.
	// Default 200 (REQ-SEND-77).
	PerUserPerMinute int `toml:"per_user_per_minute,omitempty"`
	// PerUserOriginPerMinute caps fetches per (principal,
	// upstream-origin) per minute. Default 10.
	PerUserOriginPerMinute int `toml:"per_user_origin_per_minute,omitempty"`
	// PerUserConcurrent caps in-flight fetches per principal.
	// Default 8.
	PerUserConcurrent int `toml:"per_user_concurrent,omitempty"`
}

// UIConfig configures the operator-facing web UI (internal/protoui).
// The UI mounts as a sibling handler on the existing admin HTTP
// listener; operators may opt out (set Enabled = false) on a host
// where only the API surface is wanted.
//
// SecureCookies defaults to true: cookies issued for sessions and
// CSRF tokens carry the Secure attribute. Operators running behind a
// trusted localhost reverse proxy during development can override
// (set false), but production deployments MUST keep it true.
type UIConfig struct {
	// Enabled selects whether the UI is mounted. Defaults to true.
	Enabled *bool `toml:"enabled,omitempty"`
	// PathPrefix is the URL prefix every UI route lives under
	// (default "/ui"). Leading slash required.
	PathPrefix string `toml:"path_prefix,omitempty"`
	// CookieName overrides the session cookie name (default
	// "herold_ui_session").
	CookieName string `toml:"cookie_name,omitempty"`
	// CSRFCookieName overrides the CSRF cookie name (default
	// "herold_ui_csrf").
	CSRFCookieName string `toml:"csrf_cookie_name,omitempty"`
	// SessionTTL bounds session lifetime; zero applies the default
	// of 24 hours. Sliding renewal extends the deadline on each
	// authenticated request.
	SessionTTL Duration `toml:"session_ttl,omitempty"`
	// SecureCookies, when nil, applies the secure-by-default policy
	// (true). Set explicitly to false only for development.
	SecureCookies *bool `toml:"secure_cookies,omitempty"`
	// SigningKeyEnv names the environment variable holding the
	// HMAC signing key for session cookies. Empty makes the server
	// generate a random per-process key (operators tolerate
	// re-login on restart).
	SigningKeyEnv string `toml:"signing_key_env,omitempty"`
}

// SnoozeConfig tunes the JMAP snooze wake-up worker (REQ-PROTO-49).
// Default poll cadence is 60 s; values below 5 s are rejected at
// Validate so operators do not accidentally hammer the messages table
// trying to gain sub-second snooze precision the protocol does not
// promise.
type SnoozeConfig struct {
	PollInterval Duration `toml:"poll_interval,omitempty"`
}

// StorageConfig selects the metadata-store backend and configures it
// (REQ-OPS-03 extension). The backend is "sqlite" (default) or "postgres".
// SQLite and Postgres sub-blocks are parsed unconditionally but only the
// block matching Backend is consulted; the other is validated for shape
// only so operators can keep both during a migration.
type StorageConfig struct {
	Backend  string                `toml:"backend,omitempty"`
	SQLite   StorageSQLiteConfig   `toml:"sqlite,omitempty"`
	Postgres StoragePostgresConfig `toml:"postgres,omitempty"`
}

// StorageSQLiteConfig holds SQLite-specific knobs.
type StorageSQLiteConfig struct {
	Path string `toml:"path,omitempty"`
}

// StoragePostgresConfig holds Postgres-specific knobs.
type StoragePostgresConfig struct {
	DSN     string `toml:"dsn,omitempty"`
	BlobDir string `toml:"blob_dir,omitempty"`
}

// Duration is a TOML-friendly wrapper around time.Duration: it parses
// Go duration strings ("30s", "5m") from TOML strings. The zero value
// marshals to an empty string so omitempty works.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler for go-toml strict decoding.
func (d *Duration) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*d = 0
		return nil
	}
	dur, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	*d = Duration(dur)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	if d == 0 {
		return []byte{}, nil
	}
	return []byte(time.Duration(d).String()), nil
}

// AsDuration returns the value as a time.Duration.
func (d Duration) AsDuration() time.Duration { return time.Duration(d) }

// AdminTLSConfig controls the cert used for the admin HTTPS surface.
// Phase 1 accepts only source = "file"; "acme" is rejected at Validate.
type AdminTLSConfig struct {
	Source      string `toml:"source"`
	CertFile    string `toml:"cert_file,omitempty"`
	KeyFile     string `toml:"key_file,omitempty"`
	AcmeAccount string `toml:"acme_account,omitempty"`
}

// ListenerConfig describes a single bound listener (REQ-OPS-03).
//
// Kind partitions HTTP listeners into two roles per
// REQ-OPS-ADMIN-LISTENER-01:
//   - "public": serves the SPA mount point, JMAP, chat WS, send API,
//     call credentials, image proxy, public webhook ingress, and the
//     public /login flow. Default bind 0.0.0.0:443 in production.
//   - "admin": serves the protoadmin REST surface, admin UI, /metrics,
//     and the admin /login flow with TOTP step-up. Default bind
//     127.0.0.1:9443; the loopback-by-default makes the surface
//     invisible to internet scanners (REQ-OPS-ADMIN-LISTENER-02).
//
// For non-HTTP protocols (smtp, imap, etc.) Kind is left empty; the
// validator only reads Kind when Protocol == "admin" (which is the
// legacy single-HTTP-listener shape) or when DevMode is on.
type ListenerConfig struct {
	Name          string `toml:"name"`
	Address       string `toml:"address"`
	Protocol      string `toml:"protocol"`
	Kind          string `toml:"kind,omitempty"`
	TLS           string `toml:"tls"`
	AuthRequired  bool   `toml:"auth_required,omitempty"`
	ProxyProtocol bool   `toml:"proxy_protocol,omitempty"`
	CertFile      string `toml:"cert_file,omitempty"`
	KeyFile       string `toml:"key_file,omitempty"`
}

// AcmeConfig is parsed but explicitly rejected in Phase 1.
type AcmeConfig struct {
	Email        string `toml:"email,omitempty"`
	DirectoryURL string `toml:"directory_url,omitempty"`
}

// PluginConfig describes an out-of-process plugin declaration.
type PluginConfig struct {
	Name      string            `toml:"name"`
	Path      string            `toml:"path"`
	Type      string            `toml:"type"`
	Lifecycle string            `toml:"lifecycle"`
	Options   map[string]string `toml:"options,omitempty"`
}

// ObservabilityConfig controls log format, level, metrics bind, and OTLP export.
type ObservabilityConfig struct {
	LogFormat    string `toml:"log_format,omitempty"`
	LogLevel     string `toml:"log_level,omitempty"`
	MetricsBind  string `toml:"metrics_bind,omitempty"`
	OTLPEndpoint string `toml:"otlp_endpoint,omitempty"`
}

// secretKeySubstrings is the closed-vocabulary list of substrings the
// strict secret-validation pass treats as secret-bearing in plugin
// option keys. Matched case-insensitively. Operators who genuinely
// have a non-secret value whose key contains one of these tokens
// (e.g. a public "api_key_url") can prefix-rename or use the
// reference form to satisfy the check.
var secretKeySubstrings = []string{
	"secret", "token", "password", "passwd", "api_key", "apikey", "credential",
}

// looksLikeSecretKey reports whether k matches any well-known secret-
// bearing key convention. Used by Validate to enforce STANDARDS §9.
func looksLikeSecretKey(k string) bool {
	lk := strings.ToLower(k)
	for _, sub := range secretKeySubstrings {
		if strings.Contains(lk, sub) {
			return true
		}
	}
	return false
}

// Listener kinds (REQ-OPS-ADMIN-LISTENER-01..03). Only HTTP listeners
// (Protocol == "admin") carry a Kind; non-HTTP listeners (smtp / imap
// etc.) leave Kind empty.
const (
	// ListenerKindPublic serves the SPA mount + JMAP + chat WS + send
	// API + call credentials + image proxy + public webhook ingress +
	// public /login (suite-login flow).
	ListenerKindPublic = "public"
	// ListenerKindAdmin serves protoadmin REST + admin UI + /metrics +
	// admin /login (TOTP step-up flow). Default bind loopback.
	ListenerKindAdmin = "admin"
)

// Valid protocol / tls / lifecycle / plugin-type / log level sets.
var (
	validProtocols     = map[string]struct{}{"smtp": {}, "smtp-submission": {}, "imap": {}, "imaps": {}, "admin": {}}
	validListenerKinds = map[string]struct{}{
		ListenerKindPublic: {},
		ListenerKindAdmin:  {},
	}
	validTLSModes   = map[string]struct{}{"none": {}, "starttls": {}, "implicit": {}}
	validLifecycles = map[string]struct{}{"long-running": {}, "on-demand": {}}
	validPluginType = map[string]struct{}{"dns": {}, "spam": {}, "events": {}, "directory": {}, "delivery": {}}
	validLogLevels  = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}
	validLogFormats = map[string]struct{}{"json": {}, "text": {}}
	validBackends   = map[string]struct{}{"sqlite": {}, "postgres": {}}

	// Smart-host enums (REQ-FLOW-SMARTHOST-01..06).
	validSmartHostTLSModes = map[string]struct{}{
		"starttls":     {},
		"implicit_tls": {},
		"none":         {},
	}
	validSmartHostAuthMethods = map[string]struct{}{
		"plain":         {},
		"login":         {},
		"scram-sha-256": {},
		"xoauth2":       {},
		"none":          {},
	}
	validSmartHostFallback = map[string]struct{}{
		"smart_host_only":    {},
		"smart_host_then_mx": {},
		"mx_then_smart_host": {},
	}
	validSmartHostTLSVerify = map[string]struct{}{
		"system_roots":         {},
		"pinned":               {},
		"insecure_skip_verify": {},
	}
)

// Load reads path, parses it strictly, applies defaults, and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sysconfig: read %q: %w", path, err)
	}
	return Parse(raw)
}

// Parse parses the given TOML bytes and validates them.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	dec := toml.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("sysconfig: parse: %w", enrichDecodeError(err))
	}
	applyDefaults(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// enrichDecodeError turns go-toml's generic "fields in the document are
// missing in the target struct" into a message that names the offending keys.
func enrichDecodeError(err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		keys := make([]string, 0, len(strict.Errors))
		for i := range strict.Errors {
			de := &strict.Errors[i]
			keys = append(keys, strings.Join(de.Key(), "."))
		}
		return fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}
	return err
}

func applyDefaults(c *Config) {
	if c.Observability.LogFormat == "" {
		c.Observability.LogFormat = "json"
	}
	if c.Observability.LogLevel == "" {
		c.Observability.LogLevel = "info"
	}
	if c.Observability.MetricsBind == "" {
		c.Observability.MetricsBind = "127.0.0.1:9090"
	}
	if c.Server.Storage.Backend == "" {
		c.Server.Storage.Backend = "sqlite"
	}
	if c.Server.Storage.Backend == "sqlite" && c.Server.Storage.SQLite.Path == "" && c.Server.DataDir != "" {
		c.Server.Storage.SQLite.Path = c.Server.DataDir + "/herold.sqlite"
	}
	if c.Server.ShutdownGrace == 0 {
		c.Server.ShutdownGrace = Duration(30 * time.Second)
	}
	if c.Server.Snooze.PollInterval == 0 {
		c.Server.Snooze.PollInterval = Duration(60 * time.Second)
	}
	// UI defaults: enabled, /ui prefix, 24-hour session TTL, secure
	// cookies. Strict TOML parsing keeps unknown keys an error so
	// operators see typos.
	if c.Server.UI.Enabled == nil {
		t := true
		c.Server.UI.Enabled = &t
	}
	if c.Server.UI.PathPrefix == "" {
		c.Server.UI.PathPrefix = "/ui"
	}
	if c.Server.UI.CookieName == "" {
		c.Server.UI.CookieName = "herold_ui_session"
	}
	if c.Server.UI.CSRFCookieName == "" {
		c.Server.UI.CSRFCookieName = "herold_ui_csrf"
	}
	if c.Server.UI.SessionTTL == 0 {
		c.Server.UI.SessionTTL = Duration(24 * time.Hour)
	}
	if c.Server.UI.SecureCookies == nil {
		t := true
		c.Server.UI.SecureCookies = &t
	}
	// Image proxy defaults (REQ-SEND-70..78). Operators get a working
	// proxy without any TOML; the constants here mirror protoimg's
	// own defaults so a missing block and an empty block behave the
	// same.
	if c.Server.ImageProxy.Enabled == nil {
		t := true
		c.Server.ImageProxy.Enabled = &t
	}
	if c.Server.ImageProxy.MaxBytes == 0 {
		c.Server.ImageProxy.MaxBytes = 25 * 1024 * 1024
	}
	if c.Server.ImageProxy.CacheMaxBytes == 0 {
		c.Server.ImageProxy.CacheMaxBytes = 256 * 1024 * 1024
	}
	if c.Server.ImageProxy.CacheMaxEntries == 0 {
		c.Server.ImageProxy.CacheMaxEntries = 8192
	}
	if c.Server.ImageProxy.CacheMaxAgeSeconds == 0 {
		c.Server.ImageProxy.CacheMaxAgeSeconds = 86400
	}
	if c.Server.ImageProxy.PerUserPerMinute == 0 {
		c.Server.ImageProxy.PerUserPerMinute = 200
	}
	if c.Server.ImageProxy.PerUserOriginPerMinute == 0 {
		c.Server.ImageProxy.PerUserOriginPerMinute = 10
	}
	if c.Server.ImageProxy.PerUserConcurrent == 0 {
		c.Server.ImageProxy.PerUserConcurrent = 8
	}
	// Chat ephemeral channel (REQ-CHAT-40..46). Defaults match the
	// architecture document; operators get a working server without
	// any TOML. Toggling Enabled to false unmounts the upgrade
	// handler — useful before a chat client has shipped.
	if c.Server.Chat.Enabled == nil {
		t := true
		c.Server.Chat.Enabled = &t
	}
	if c.Server.Chat.MaxConnections == 0 {
		c.Server.Chat.MaxConnections = 4096
	}
	if c.Server.Chat.PerPrincipalCap == 0 {
		c.Server.Chat.PerPrincipalCap = 8
	}
	if c.Server.Chat.PingIntervalSeconds == 0 {
		c.Server.Chat.PingIntervalSeconds = 30
	}
	if c.Server.Chat.PongTimeoutSeconds == 0 {
		c.Server.Chat.PongTimeoutSeconds = 60
	}
	if c.Server.Chat.MaxFrameBytes == 0 {
		c.Server.Chat.MaxFrameBytes = 65536
	}
	if c.Server.Chat.WriteTimeoutSeconds == 0 {
		c.Server.Chat.WriteTimeoutSeconds = 10
	}
	// Chat retention sweeper (REQ-CHAT-92). Defaults mirror the
	// chatretention package constants so a missing block and an
	// empty block behave the same.
	if c.Server.Chat.Retention.SweepIntervalSeconds == 0 {
		c.Server.Chat.Retention.SweepIntervalSeconds = 60
	}
	if c.Server.Chat.Retention.BatchSize == 0 {
		c.Server.Chat.Retention.BatchSize = 1000
	}
	// Video calls (REQ-CALL-*). Defaults to enabled with a five-
	// minute credential TTL (REQ-CALL-22) and a 30-second ring
	// timeout (REQ-CALL-06); operators who haven't deployed coturn
	// keep the surface unreachable simply by leaving uris empty (the
	// mint endpoint then returns 503).
	if c.Server.Call.Enabled == nil {
		t := true
		c.Server.Call.Enabled = &t
	}
	if c.Server.Call.RingTimeoutSeconds == 0 {
		c.Server.Call.RingTimeoutSeconds = 30
	}
	if c.Server.TURN.CredentialTTLSeconds == 0 {
		c.Server.TURN.CredentialTTLSeconds = 300
	}
	// Tabard SPA (REQ-DEPLOY-COLOC-01..05). Default-enabled so a
	// fresh install boots with the consumer suite mounted on the
	// public listener; admin-only deployments set Enabled=false.
	if c.Server.Tabard.Enabled == nil {
		t := true
		c.Server.Tabard.Enabled = &t
	}
	// Smart host (REQ-FLOW-SMARTHOST-01..08). Defaults are applied to
	// the top-level block AND every per-domain override so a sparsely-
	// keyed override picks up the same timeout / fallback floor as the
	// global block.
	applySmartHostDefaults(&c.Server.SmartHost)
	for k, ov := range c.Server.SmartHost.PerDomain {
		applySmartHostDefaults(&ov)
		c.Server.SmartHost.PerDomain[k] = ov
	}
}

// applySmartHostDefaults populates the smart-host knobs that have a
// canonical default. Called once for the top-level block and once per
// PerDomain override.
func applySmartHostDefaults(sh *SmartHostConfig) {
	if sh.FallbackPolicy == "" {
		sh.FallbackPolicy = "smart_host_only"
	}
	if sh.TLSVerifyMode == "" {
		sh.TLSVerifyMode = "system_roots"
	}
	if sh.ConnectTimeoutSeconds == 0 {
		sh.ConnectTimeoutSeconds = 10
	}
	if sh.ReadTimeoutSeconds == 0 {
		sh.ReadTimeoutSeconds = 30
	}
	if sh.FallbackAfterFailureSeconds == 0 {
		sh.FallbackAfterFailureSeconds = 300
	}
	// TLSMode auto-default: 587 -> starttls, 465 -> implicit_tls.
	if sh.TLSMode == "" {
		switch sh.Port {
		case 465:
			sh.TLSMode = "implicit_tls"
		case 587, 25:
			sh.TLSMode = "starttls"
		default:
			sh.TLSMode = "starttls"
		}
	}
}

// Validate performs cross-field and semantic checks that go-toml cannot express.
func Validate(c *Config) error {
	if c == nil {
		return errors.New("sysconfig: nil config")
	}
	if c.Server.Hostname == "" {
		return errors.New("sysconfig: [server] hostname is required")
	}
	if c.Server.DataDir == "" {
		return errors.New("sysconfig: [server] data_dir is required")
	}
	// Snooze worker (REQ-PROTO-49). PollInterval below 5 s is a
	// configuration mistake; defaults already land at 60 s after
	// applyDefaults.
	if c.Server.Snooze.PollInterval > 0 && c.Server.Snooze.PollInterval < Duration(5*time.Second) {
		return fmt.Errorf("sysconfig: [server.snooze] poll_interval %s below 5s floor", c.Server.Snooze.PollInterval.AsDuration())
	}
	// Image proxy (REQ-SEND-70..78). Catch operator typos that would
	// otherwise produce a silently-disabled feature: negative budgets
	// are nonsense and would collapse to "no fetches" / "no cache".
	ip := c.Server.ImageProxy
	if ip.MaxBytes < 0 {
		return fmt.Errorf("sysconfig: [server.image_proxy] max_bytes %d must be >= 0", ip.MaxBytes)
	}
	if ip.CacheMaxBytes < 0 {
		return fmt.Errorf("sysconfig: [server.image_proxy] cache_max_bytes %d must be >= 0", ip.CacheMaxBytes)
	}
	if ip.CacheMaxEntries < 0 {
		return fmt.Errorf("sysconfig: [server.image_proxy] cache_max_entries %d must be >= 0", ip.CacheMaxEntries)
	}
	if ip.CacheMaxAgeSeconds < 0 {
		return fmt.Errorf("sysconfig: [server.image_proxy] cache_max_age_seconds %d must be >= 0", ip.CacheMaxAgeSeconds)
	}
	if ip.PerUserPerMinute < 0 || ip.PerUserOriginPerMinute < 0 || ip.PerUserConcurrent < 0 {
		return errors.New("sysconfig: [server.image_proxy] rate-limit knobs must be >= 0")
	}
	// Chat ephemeral channel (REQ-CHAT-40..46). Sanity-bound the
	// knobs so an operator typo doesn't produce a non-functional
	// server.
	ch := c.Server.Chat
	if ch.MaxConnections < 0 || ch.PerPrincipalCap < 0 ||
		ch.PingIntervalSeconds < 0 || ch.PongTimeoutSeconds < 0 ||
		ch.MaxFrameBytes < 0 || ch.WriteTimeoutSeconds < 0 {
		return errors.New("sysconfig: [server.chat] knobs must be >= 0")
	}
	if ch.PerPrincipalCap > 0 && ch.MaxConnections > 0 && ch.PerPrincipalCap > ch.MaxConnections {
		return fmt.Errorf("sysconfig: [server.chat] per_principal_cap %d exceeds max_connections %d",
			ch.PerPrincipalCap, ch.MaxConnections)
	}
	if ch.PongTimeoutSeconds > 0 && ch.PingIntervalSeconds > 0 && ch.PongTimeoutSeconds < ch.PingIntervalSeconds {
		return fmt.Errorf("sysconfig: [server.chat] pong_timeout_seconds %d below ping_interval_seconds %d",
			ch.PongTimeoutSeconds, ch.PingIntervalSeconds)
	}
	// Chat retention sweeper (REQ-CHAT-92). sweep_interval_seconds
	// floor of 10 avoids pinning a writer; ceiling of 1 day catches
	// typos that would silently disable the sweeper. batch_size is
	// bounded [1, 10000] to match the chatretention worker's
	// MaxBatchSize ceiling.
	cr := c.Server.Chat.Retention
	if cr.SweepIntervalSeconds < 10 {
		return fmt.Errorf("sysconfig: [server.chat.retention] sweep_interval_seconds %d below 10s floor",
			cr.SweepIntervalSeconds)
	}
	if cr.SweepIntervalSeconds > 86400 {
		return fmt.Errorf("sysconfig: [server.chat.retention] sweep_interval_seconds %d exceeds 1d ceiling",
			cr.SweepIntervalSeconds)
	}
	if cr.BatchSize < 1 {
		return fmt.Errorf("sysconfig: [server.chat.retention] batch_size %d must be >= 1", cr.BatchSize)
	}
	if cr.BatchSize > 10000 {
		return fmt.Errorf("sysconfig: [server.chat.retention] batch_size %d exceeds 10000 ceiling", cr.BatchSize)
	}
	// Video calls (REQ-CALL-*). When the operator supplies TURN URIs
	// they MUST also point us at the shared secret via env / file
	// (STANDARDS §9: no inline secrets); the credential TTL must be
	// in (0, 12h]. Empty URIs disables the credential mint endpoint
	// entirely; the chat call.signal handler still runs (signaling
	// works without TURN when STUN succeeds).
	tu := c.Server.TURN
	if c.Server.TURN.CredentialTTLSeconds < 0 {
		return fmt.Errorf("sysconfig: [server.turn] credential_ttl_seconds %d must be >= 0", tu.CredentialTTLSeconds)
	}
	if c.Server.TURN.CredentialTTLSeconds > 12*3600 {
		return fmt.Errorf("sysconfig: [server.turn] credential_ttl_seconds %d exceeds 12h ceiling",
			tu.CredentialTTLSeconds)
	}
	// Ring window cap (REQ-CALL-06): negative is a typo, anything
	// over 5 min defeats the purpose of the timeout.
	if c.Server.Call.RingTimeoutSeconds < 0 {
		return fmt.Errorf("sysconfig: [server.call] ring_timeout_seconds %d must be >= 0",
			c.Server.Call.RingTimeoutSeconds)
	}
	if c.Server.Call.RingTimeoutSeconds > 300 {
		return fmt.Errorf("sysconfig: [server.call] ring_timeout_seconds %d exceeds 5min ceiling",
			c.Server.Call.RingTimeoutSeconds)
	}
	if len(tu.URIs) > 0 {
		if tu.SharedSecretEnv == "" {
			return errors.New("sysconfig: [server.turn] shared_secret_env required when uris is set")
		}
		if !IsSecretReference(tu.SharedSecretEnv) {
			return fmt.Errorf("sysconfig: [server.turn] shared_secret_env %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)",
				tu.SharedSecretEnv)
		}
	}
	// Web Push VAPID (REQ-PROTO-122). Both fields optional; when one
	// is set, exactly one must be set (env XOR file) and it must be a
	// secret reference (no inline PEM in system.toml). When neither
	// is set the deployment has no VAPID and Web Push is disabled —
	// that's a valid posture; the capability handler omits
	// applicationServerKey at runtime.
	push := c.Server.Push
	envSet := push.VAPIDPrivateKeyEnv != ""
	fileSet := push.VAPIDPrivateKeyFile != ""
	if envSet && fileSet {
		return errors.New("sysconfig: [server.push] vapid_private_key_env and vapid_private_key_file are mutually exclusive")
	}
	if envSet {
		// Env values are typed as "$VAR" — IsSecretReference is the
		// canonical check used elsewhere in the file.
		if !strings.HasPrefix(push.VAPIDPrivateKeyEnv, "$") {
			return fmt.Errorf("sysconfig: [server.push] vapid_private_key_env %q must start with \"$\" (STANDARDS §9)",
				push.VAPIDPrivateKeyEnv)
		}
	}
	if fileSet {
		if !strings.HasPrefix(push.VAPIDPrivateKeyFile, "/") {
			return fmt.Errorf("sysconfig: [server.push] vapid_private_key_file %q must be an absolute path",
				push.VAPIDPrivateKeyFile)
		}
	}
	// Tabard SPA (REQ-DEPLOY-COLOC-01..05). The asset_dir override is
	// validated at parse time so a missing or relative path fails the
	// load rather than at first 404; the actual content (index.html
	// presence) is re-checked by tabardspa.New at server boot for the
	// embedded path too.
	if dir := c.Server.Tabard.AssetDir; dir != "" {
		if !strings.HasPrefix(dir, "/") {
			return fmt.Errorf("sysconfig: [server.tabard] asset_dir %q must be an absolute path", dir)
		}
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("sysconfig: [server.tabard] asset_dir %q: %w", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("sysconfig: [server.tabard] asset_dir %q is not a directory", dir)
		}
		if _, err := os.Stat(dir + "/index.html"); err != nil {
			return fmt.Errorf("sysconfig: [server.tabard] asset_dir %q missing index.html: %w", dir, err)
		}
	}
	// Smart host (REQ-FLOW-SMARTHOST-01..08).
	if err := validateSmartHost("smart_host", &c.Server.SmartHost, true); err != nil {
		return err
	}
	for domain, ov := range c.Server.SmartHost.PerDomain {
		if domain != strings.ToLower(domain) {
			return fmt.Errorf("sysconfig: [smart_host.per_domain.%s]: domain key must be lowercase ASCII", domain)
		}
		if domain == "" || strings.ContainsAny(domain, " \t\r\n") {
			return fmt.Errorf("sysconfig: [smart_host.per_domain.%s]: domain key invalid", domain)
		}
		if len(ov.PerDomain) > 0 {
			return fmt.Errorf("sysconfig: [smart_host.per_domain.%s]: nested per_domain not allowed", domain)
		}
		ovCopy := ov
		// PerDomain overrides force Enabled to true at validate-time:
		// the operator wrote a per-domain block to route specific
		// traffic, so the smart-host posture must apply for those
		// recipients regardless of the global Enabled flag.
		ovCopy.Enabled = true
		if err := validateSmartHost(fmt.Sprintf("smart_host.per_domain.%s", domain), &ovCopy, false); err != nil {
			return err
		}
	}
	// Admin TLS
	switch c.Server.AdminTLS.Source {
	case "":
		return errors.New("sysconfig: [server.admin_tls] source is required (Phase 1: only \"file\" is supported)")
	case "file":
		if c.Server.AdminTLS.CertFile == "" || c.Server.AdminTLS.KeyFile == "" {
			return errors.New("sysconfig: [server.admin_tls] source=\"file\" requires cert_file and key_file")
		}
	case "acme":
		return errors.New("sysconfig: [server.admin_tls] source=\"acme\" not supported in Phase 1 (Phase 2: ACME lands in queue-delivery-implementor's surface)")
	default:
		return fmt.Errorf("sysconfig: [server.admin_tls] source %q not recognised (want \"file\" or \"acme\")", c.Server.AdminTLS.Source)
	}
	// [acme] block is parsed (so operators can follow future examples without
	// hitting unknown-key errors) but rejected as a hard error in Phase 1.
	if c.Acme != nil {
		return errors.New("sysconfig: [acme] block not supported in Phase 1 (Phase 2: ACME lands in queue-delivery-implementor's surface)")
	}
	// Listeners.
	if len(c.Listener) == 0 {
		return errors.New("sysconfig: at least one [[listener]] is required")
	}
	seen := make(map[string]struct{}, len(c.Listener))
	var sawPublic, sawAdmin bool
	for i, l := range c.Listener {
		if l.Name == "" {
			return fmt.Errorf("sysconfig: [[listener]] #%d: name is required", i)
		}
		if _, dup := seen[l.Name]; dup {
			return fmt.Errorf("sysconfig: [[listener]] %q: duplicate name", l.Name)
		}
		seen[l.Name] = struct{}{}
		if l.Address == "" {
			return fmt.Errorf("sysconfig: [[listener]] %q: address is required", l.Name)
		}
		if _, ok := validProtocols[l.Protocol]; !ok {
			return fmt.Errorf("sysconfig: [[listener]] %q: protocol %q not recognised", l.Name, l.Protocol)
		}
		if _, ok := validTLSModes[l.TLS]; !ok {
			return fmt.Errorf("sysconfig: [[listener]] %q: tls %q not recognised (want \"none\", \"starttls\", or \"implicit\")", l.Name, l.TLS)
		}
		// per-listener cert override: both or neither
		if (l.CertFile == "") != (l.KeyFile == "") {
			return fmt.Errorf("sysconfig: [[listener]] %q: cert_file and key_file must both be set or both empty", l.Name)
		}
		if l.TLS == "none" && (l.CertFile != "" || l.KeyFile != "") {
			return fmt.Errorf("sysconfig: [[listener]] %q: cert_file/key_file set but tls=\"none\"", l.Name)
		}
		// REQ-OPS-ADMIN-LISTENER-01: HTTP listeners (Protocol=="admin")
		// carry a Kind in {public, admin}. Non-HTTP listeners must
		// leave Kind empty.
		if l.Protocol == "admin" {
			if l.Kind == "" {
				// Wave 3.6 compatibility: an HTTP listener without an
				// explicit kind is treated as the legacy single-mount
				// shape; we accept it ONLY when DevMode is on or when
				// no other HTTP listener carries a kind. Otherwise the
				// validate rejects with a migration message.
				if !c.Server.DevMode {
					return fmt.Errorf(
						"sysconfig: [[listener]] %q: HTTP listener requires kind = \"public\" or \"admin\" (REQ-OPS-ADMIN-LISTENER-01); set [server.dev_mode] = true to co-mount in development",
						l.Name)
				}
				// Dev-mode co-mount: serve both handlers on this
				// single listener. Treat as both public+admin so
				// downstream binding code wires both routers.
				continue
			}
			if _, ok := validListenerKinds[l.Kind]; !ok {
				return fmt.Errorf("sysconfig: [[listener]] %q: kind %q not recognised (want \"public\" or \"admin\")", l.Name, l.Kind)
			}
			switch l.Kind {
			case ListenerKindPublic:
				sawPublic = true
			case ListenerKindAdmin:
				sawAdmin = true
			}
		} else if l.Kind != "" {
			return fmt.Errorf("sysconfig: [[listener]] %q: kind=%q only valid on protocol=\"admin\" listeners", l.Name, l.Kind)
		}
	}
	// REQ-OPS-ADMIN-LISTENER-01..03: a production config MUST declare
	// at least one admin-kind listener so admin surfaces are not
	// co-mounted with public surfaces. DevMode bypasses this rule for
	// developer convenience.
	if !c.Server.DevMode {
		// At least one HTTP listener must exist. If any HTTP listener
		// carries a kind we require both kinds to be present (no
		// silent admin co-mount on the public listener). If no HTTP
		// listener carries a kind we already errored above.
		if sawPublic && !sawAdmin {
			return errors.New(
				"sysconfig: at least one HTTP listener with kind=\"admin\" is required (REQ-OPS-ADMIN-LISTENER-01); set [server.dev_mode] = true to co-mount in development")
		}
		if sawAdmin && !sawPublic {
			return errors.New(
				"sysconfig: at least one HTTP listener with kind=\"public\" is required (REQ-OPS-ADMIN-LISTENER-01); set [server.dev_mode] = true to co-mount in development")
		}
	}
	// SMTP inbound (REQ-DIR-RCPT-*).
	if err := validateSMTPInbound(c); err != nil {
		return err
	}
	// Plugins.
	pseen := make(map[string]struct{}, len(c.Plugin))
	for i, p := range c.Plugin {
		if p.Name == "" {
			return fmt.Errorf("sysconfig: [[plugin]] #%d: name is required", i)
		}
		if _, dup := pseen[p.Name]; dup {
			return fmt.Errorf("sysconfig: [[plugin]] %q: duplicate name", p.Name)
		}
		pseen[p.Name] = struct{}{}
		if p.Path == "" {
			return fmt.Errorf("sysconfig: [[plugin]] %q: path is required", p.Name)
		}
		if _, ok := validPluginType[p.Type]; !ok {
			return fmt.Errorf("sysconfig: [[plugin]] %q: type %q not recognised", p.Name, p.Type)
		}
		if _, ok := validLifecycles[p.Lifecycle]; !ok {
			return fmt.Errorf("sysconfig: [[plugin]] %q: lifecycle %q not recognised", p.Name, p.Lifecycle)
		}
		// STANDARDS §9: no inline secrets in system.toml. Reject any
		// plugin option whose key looks like a secret unless its value
		// is "$ENV" or "file:/path". This catches the common
		// `api_token = "literal"`, `client_secret = "shhh"` shapes
		// without forcing operators to wrap every value in a reference.
		for k, v := range p.Options {
			if !looksLikeSecretKey(k) {
				continue
			}
			if !IsSecretReference(v) {
				return fmt.Errorf("sysconfig: [[plugin]] %q: option %q must be \"$ENV\" or \"file:/path\" (no inline secrets; STANDARDS §9)", p.Name, k)
			}
		}
	}
	// Observability.
	if _, ok := validLogFormats[c.Observability.LogFormat]; !ok {
		return fmt.Errorf("sysconfig: [observability] log_format %q not recognised", c.Observability.LogFormat)
	}
	if _, ok := validLogLevels[c.Observability.LogLevel]; !ok {
		return fmt.Errorf("sysconfig: [observability] log_level %q not recognised", c.Observability.LogLevel)
	}
	// Storage.
	if _, ok := validBackends[c.Server.Storage.Backend]; !ok {
		return fmt.Errorf("sysconfig: [server.storage] backend %q not recognised (want \"sqlite\" or \"postgres\")", c.Server.Storage.Backend)
	}
	switch c.Server.Storage.Backend {
	case "sqlite":
		if c.Server.Storage.SQLite.Path == "" {
			return errors.New("sysconfig: [server.storage.sqlite] path is required (or set server.data_dir)")
		}
	case "postgres":
		if c.Server.Storage.Postgres.DSN == "" {
			return errors.New("sysconfig: [server.storage.postgres] dsn is required")
		}
	}
	// Non-loopback metrics_bind: warn rather than error. STANDARDS §7
	// documents the operator obligation to front a public /metrics
	// with TLS + auth at a reverse proxy; this is a deliberate choice
	// some operators make, so we surface it loudly without breaking
	// startup. The warn fires at parse / Load time; SIGHUP-driven
	// reloads see it again.
	if !isLoopbackBindAddr(c.Observability.MetricsBind) {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
			"sysconfig: metrics_bind is non-loopback; front /metrics with TLS + auth (STANDARDS §7)",
			slog.String("metrics_bind", c.Observability.MetricsBind),
		)
	}
	return nil
}

// resolveRcptHardCap is the upper bound the SMTP RCPT phase can wait
// for the directory.resolve_rcpt plugin (REQ-PLUG-32 / REQ-DIR-RCPT-04).
// Mirrored by internal/plugin.ResolveRcptHardCapTimeout; kept as a
// duplicate constant here so sysconfig has no inbound dependency on
// the plugin package.
const resolveRcptHardCap = 5 * time.Second

// validateSMTPInbound enforces REQ-DIR-RCPT-* configuration rules on
// the [smtp.inbound] block. It runs against the parsed config after
// applyDefaults; structural / TOML errors are caught upstream.
func validateSMTPInbound(c *Config) error {
	in := c.SMTP.Inbound
	if in.RcptRateLimitPerIPPerSec < 0 {
		return fmt.Errorf("sysconfig: [smtp.inbound] rcpt_rate_limit_per_ip_per_sec %d must be >= 0", in.RcptRateLimitPerIPPerSec)
	}
	if in.ResolveRcptTimeout < 0 {
		return fmt.Errorf("sysconfig: [smtp.inbound] resolve_rcpt_timeout %s must be >= 0", in.ResolveRcptTimeout.AsDuration())
	}
	if in.ResolveRcptTimeout.AsDuration() > resolveRcptHardCap {
		return fmt.Errorf("sysconfig: [smtp.inbound] resolve_rcpt_timeout %s exceeds hard cap %s (REQ-PLUG-32)",
			in.ResolveRcptTimeout.AsDuration(), resolveRcptHardCap)
	}
	for _, d := range in.PluginFirstForDomains {
		if d == "" {
			return errors.New("sysconfig: [smtp.inbound] plugin_first_for_domains contains empty entry")
		}
		if d != strings.ToLower(d) {
			return fmt.Errorf("sysconfig: [smtp.inbound] plugin_first_for_domains entry %q must be lowercase ASCII", d)
		}
	}
	if in.DirectoryResolveRcptPlugin == "" {
		return nil
	}
	// Refuse-to-start: the named plugin must exist in [[plugin]] blocks
	// AND declare type = "directory". The supports[] check happens at
	// plugin-start time (the manifest is a runtime artefact).
	for _, p := range c.Plugin {
		if p.Name != in.DirectoryResolveRcptPlugin {
			continue
		}
		if p.Type != "directory" {
			return fmt.Errorf("sysconfig: [smtp.inbound] directory_resolve_rcpt_plugin %q must be type=\"directory\" (got %q)",
				p.Name, p.Type)
		}
		return nil
	}
	return fmt.Errorf("sysconfig: [smtp.inbound] directory_resolve_rcpt_plugin %q not declared in any [[plugin]] block",
		in.DirectoryResolveRcptPlugin)
}

// validateSmartHost enforces REQ-FLOW-SMARTHOST-01..08 on a single
// SmartHostConfig (the top-level block or a PerDomain override). label
// is the TOML path used in error messages. global is true for the
// top-level block; per-domain overrides skip the "Enabled gates the
// rest" rule because their presence already implies operator intent.
func validateSmartHost(label string, sh *SmartHostConfig, global bool) error {
	// When the global block is disabled and there are no per-domain
	// overrides we accept the zero shape outright. Per-domain
	// overrides come through with global=false and Enabled forced to
	// true at the call site, so this branch only fires for the
	// top-level block.
	if global && !sh.Enabled {
		return nil
	}
	if sh.Host == "" {
		return fmt.Errorf("sysconfig: [%s] host is required", label)
	}
	if sh.Port < 1 || sh.Port > 65535 {
		return fmt.Errorf("sysconfig: [%s] port %d out of range [1,65535]", label, sh.Port)
	}
	if _, ok := validSmartHostTLSModes[sh.TLSMode]; !ok {
		return fmt.Errorf("sysconfig: [%s] tls_mode %q not recognised (want \"starttls\", \"implicit_tls\", or \"none\")", label, sh.TLSMode)
	}
	if _, ok := validSmartHostAuthMethods[sh.AuthMethod]; !ok {
		return fmt.Errorf("sysconfig: [%s] auth_method %q not recognised (want \"plain\", \"login\", \"scram-sha-256\", \"xoauth2\", or \"none\")", label, sh.AuthMethod)
	}
	if _, ok := validSmartHostFallback[sh.FallbackPolicy]; !ok {
		return fmt.Errorf("sysconfig: [%s] fallback_policy %q not recognised (want \"smart_host_only\", \"smart_host_then_mx\", or \"mx_then_smart_host\")", label, sh.FallbackPolicy)
	}
	if _, ok := validSmartHostTLSVerify[sh.TLSVerifyMode]; !ok {
		return fmt.Errorf("sysconfig: [%s] tls_verify_mode %q not recognised (want \"system_roots\", \"pinned\", or \"insecure_skip_verify\")", label, sh.TLSVerifyMode)
	}
	if sh.TLSVerifyMode == "pinned" && sh.PinnedCertPath == "" {
		return fmt.Errorf("sysconfig: [%s] pinned_cert_path required when tls_verify_mode = \"pinned\"", label)
	}
	if sh.TLSVerifyMode == "insecure_skip_verify" {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
			"sysconfig: smart-host tls_verify_mode = \"insecure_skip_verify\" — dev only",
			slog.String("label", label),
			slog.String("host", sh.Host),
		)
	}
	if sh.ConnectTimeoutSeconds < 0 || sh.ReadTimeoutSeconds < 0 || sh.FallbackAfterFailureSeconds < 0 {
		return fmt.Errorf("sysconfig: [%s] timeout knobs must be >= 0", label)
	}
	if sh.AuthMethod == "none" {
		// Username and credentials must be empty when auth is off.
		if sh.Username != "" || sh.PasswordEnv != "" || sh.PasswordFile != "" {
			return fmt.Errorf("sysconfig: [%s] username/password_env/password_file set but auth_method = \"none\"", label)
		}
	} else {
		if sh.Username == "" {
			return fmt.Errorf("sysconfig: [%s] username required when auth_method != \"none\"", label)
		}
		if (sh.PasswordEnv == "") == (sh.PasswordFile == "") {
			return fmt.Errorf("sysconfig: [%s] exactly one of password_env / password_file required", label)
		}
		if sh.PasswordEnv != "" && !IsSecretReference(sh.PasswordEnv) {
			return fmt.Errorf("sysconfig: [%s] password_env %q must be \"$VAR\" (no inline secrets; STANDARDS §9)", label, sh.PasswordEnv)
		}
		if sh.PasswordFile != "" && !strings.HasPrefix(sh.PasswordFile, "/") {
			return fmt.Errorf("sysconfig: [%s] password_file must be an absolute path", label)
		}
		// Refuse plaintext credentials over plaintext transport.
		if sh.TLSMode == "none" {
			return fmt.Errorf("sysconfig: [%s] tls_mode = \"none\" with auth_method = %q would send credentials in plaintext (refused)", label, sh.AuthMethod)
		}
	}
	return nil
}

// isLoopbackBindAddr reports whether bind is a host:port style address
// whose host resolves to a loopback IP. An empty bind (the default
// after applyDefaults is "127.0.0.1:9090") and any unparseable shape
// is treated as loopback so we do not log a misleading warning while
// the operator is still typing.
func isLoopbackBindAddr(bind string) bool {
	if bind == "" {
		return true
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		// Unparseable; the listener will fail later. Don't warn here.
		return true
	}
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A hostname we can't resolve at validate time. Be
		// conservative: treat as non-loopback so the operator sees
		// the warning if they typed an external DNS name.
		return false
	}
	return ip.IsLoopback()
}
