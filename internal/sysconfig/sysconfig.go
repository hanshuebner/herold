package sysconfig

import (
	"errors"
	"fmt"
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
	Observability ObservabilityConfig `toml:"observability"`
}

// ServerConfig carries process-wide settings.
type ServerConfig struct {
	Hostname      string           `toml:"hostname"`
	DataDir       string           `toml:"data_dir"`
	RunAsUser     string           `toml:"run_as_user"`
	RunAsGroup    string           `toml:"run_as_group"`
	ShutdownGrace Duration         `toml:"shutdown_grace,omitempty"`
	AdminTLS      AdminTLSConfig   `toml:"admin_tls"`
	Storage       StorageConfig    `toml:"storage"`
	Snooze        SnoozeConfig     `toml:"snooze,omitempty"`
	UI            UIConfig         `toml:"ui,omitempty"`
	ImageProxy    ImageProxyConfig `toml:"image_proxy,omitempty"`
	Chat          ChatConfig       `toml:"chat,omitempty"`
	Call          CallConfig       `toml:"call,omitempty"`
	TURN          TURNConfig       `toml:"turn,omitempty"`
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
type ListenerConfig struct {
	Name          string `toml:"name"`
	Address       string `toml:"address"`
	Protocol      string `toml:"protocol"`
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

// Valid protocol / tls / lifecycle / plugin-type / log level sets.
var (
	validProtocols  = map[string]struct{}{"smtp": {}, "smtp-submission": {}, "imap": {}, "imaps": {}, "admin": {}}
	validTLSModes   = map[string]struct{}{"none": {}, "starttls": {}, "implicit": {}}
	validLifecycles = map[string]struct{}{"long-running": {}, "on-demand": {}}
	validPluginType = map[string]struct{}{"dns": {}, "spam": {}, "events": {}, "directory": {}, "delivery": {}}
	validLogLevels  = map[string]struct{}{"debug": {}, "info": {}, "warn": {}, "error": {}}
	validLogFormats = map[string]struct{}{"json": {}, "text": {}}
	validBackends   = map[string]struct{}{"sqlite": {}, "postgres": {}}
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
		ch.MaxFrameBytes < 0 {
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
	return nil
}
