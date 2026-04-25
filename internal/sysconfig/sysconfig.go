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
	Hostname      string         `toml:"hostname"`
	DataDir       string         `toml:"data_dir"`
	RunAsUser     string         `toml:"run_as_user"`
	RunAsGroup    string         `toml:"run_as_group"`
	ShutdownGrace Duration       `toml:"shutdown_grace,omitempty"`
	AdminTLS      AdminTLSConfig `toml:"admin_tls"`
	Storage       StorageConfig  `toml:"storage"`
	Snooze        SnoozeConfig   `toml:"snooze,omitempty"`
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
