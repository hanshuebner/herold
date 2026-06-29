package sysconfig

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the parsed representation of /etc/herold/system.toml (REQ-OPS-01..08).
// Unknown keys at parse time are errors.
type Config struct {
	Server         ServerConfig         `toml:"server"`
	Acme           *AcmeConfig          `toml:"acme,omitempty"`
	Listener       []ListenerConfig     `toml:"listener"`
	Plugin         []PluginConfig       `toml:"plugin"`
	SMTP           SMTPConfig           `toml:"smtp,omitempty"`
	Hooks          HooksConfig          `toml:"hooks,omitempty"`
	ExternalImages ExternalImagesConfig `toml:"external_images,omitempty"`
	Observability  ObservabilityConfig  `toml:"observability"`
	// Log holds the multi-sink logging configuration (REQ-OPS-80..86).
	// The legacy [observability] block is translated into a single
	// [[log.sink]] entry at parse time by translateLegacyObservability.
	Log LogConfig `toml:"log,omitempty"`
	// ClientLog holds the client-log ingest configuration (REQ-OPS-219).
	// All values are reloadable via SIGHUP (REQ-OPS-30). When
	// ClientLog.Enabled is false both ingest endpoints return 404 and the
	// bootstrap meta tag carries enabled:false so the SPA wrapper installs
	// no handlers (REQ-CLOG-12).
	ClientLog ClientLogConfig `toml:"clientlog,omitempty"`
	// Performance holds the response-time budget configuration
	// (REQ-PERF-DEADLINE-20..23). Default deadline applies to every
	// JMAP method and IMAP command; per-method/command overrides live
	// under [performance.method_deadline].
	Performance PerformanceConfig `toml:"performance,omitempty"`
	// IMAPImport holds operator-wide configuration for the inbound IMAP
	// live-mirror feature (docs/design/server/requirements/19-imap-import.md).
	// An absent block produces a fully defaulted configuration with
	// allow_password=true, concurrent_accounts=16, and poll_interval=60s.
	// Per-account upstream settings (credentials, folder maps, etc.) live in
	// the DB (REQ-IMAP-IMP-01); only the instance-wide knobs and OAuth
	// application registrations belong here.
	IMAPImport IMAPImportConfig `toml:"imap_import,omitempty"`
	// Translation configures the operator opt-in server-side translation
	// proxy (re #84). The feature is disabled by default; enabling it routes
	// message text to a third-party API so the operator must explicitly
	// opt in. Supported providers: "mymemory" (free tier, default) and
	// "deepl" (paid API requiring an api_key_ref).
	Translation TranslationConfig `toml:"translation,omitempty"`
}

// PerformanceConfig configures the response-time budget enforced by
// the JMAP and IMAP dispatchers (REQ-PERF-RESP). The principal flag
// `bypass_response_deadline` overrides this for trusted accounts; it
// is set per-principal, not in this file.
//
// Example:
//
//	[performance]
//	default_deadline = "1s"
//
//	[performance.method_deadline]
//	"Email/import"     = "30s"
//	"Email/set"        = "5s"
//	"IMAP:APPEND"      = "30s"
//	"IMAP:FETCH"       = "5s"
type PerformanceConfig struct {
	// DefaultDeadline is the wall-clock budget applied to every JMAP
	// method invocation and every IMAP command when no method-specific
	// override is configured. Default 1000ms when zero. Wrapped in
	// the local Duration type so go-toml decodes the TOML string
	// ("1s", "30s") via UnmarshalText.
	DefaultDeadline Duration `toml:"default_deadline,omitempty"`
	// MethodDeadline overrides DefaultDeadline for specific operations.
	// JMAP method names use the RFC 8620 Type/methodName form
	// ("Email/query"). IMAP commands use the prefix "IMAP:" followed by
	// the uppercase verb ("IMAP:FETCH"). Unknown keys at parse time
	// are not rejected so future methods can be configured ahead of
	// their handler landing; the dispatcher ignores entries it does
	// not recognise.
	MethodDeadline map[string]Duration `toml:"method_deadline,omitempty"`
}

// HooksConfig groups ingress-hook subsystems (SES inbound, future
// webhook ingress shapes).
type HooksConfig struct {
	// SESInbound configures the AWS SES inbound path
	// (REQ-HOOK-SES-01..07). Disabled by default.
	SESInbound SESInboundConfig `toml:"ses_inbound,omitempty"`
}

// SESInboundConfig is the operator-facing configuration block for the
// SES inbound HTTP handler (REQ-HOOK-SES-03).
//
// Secrets MUST be supplied as secret references ($VAR or file:/path)
// per STANDARDS §9 / REQ-OPS-04 / REQ-OPS-161.  Inline credentials are
// rejected at Validate.
//
// Example:
//
//	[hooks.ses_inbound]
//	enabled = true
//	aws_region = "us-east-1"
//	s3_bucket_allowlist = ["my-ses-mail-bucket"]
//	sns_topic_arn_allowlist = ["arn:aws:sns:us-east-1:123456789012:herold-inbound"]
//	signature_cert_host_allowlist = ["sns.us-east-1.amazonaws.com"]
//	aws_access_key_id_env = "$AWS_ACCESS_KEY_ID"
//	aws_secret_access_key_env = "$AWS_SECRET_ACCESS_KEY"
type SESInboundConfig struct {
	// Enabled activates the handler. When false (default) no handler
	// is mounted and no goroutines are started.
	Enabled bool `toml:"enabled,omitempty"`
	// AWSRegion is the AWS region where the S3 bucket and SNS topic
	// live (e.g. "us-east-1").
	AWSRegion string `toml:"aws_region,omitempty"`
	// S3BucketAllowlist lists the S3 buckets herold is allowed to
	// fetch messages from.  References to any other bucket in an
	// incoming SNS notification are rejected (REQ-HOOK-SES-03).
	S3BucketAllowlist []string `toml:"s3_bucket_allowlist,omitempty"`
	// SNSTopicARNAllowlist lists the SNS topic ARNs from which
	// SubscriptionConfirmation requests are auto-confirmed
	// (REQ-HOOK-SES-03).  Confirmations from other topic ARNs are
	// silently dropped.
	SNSTopicARNAllowlist []string `toml:"sns_topic_arn_allowlist,omitempty"`
	// SignatureCertHostAllowlist lists the hostnames that
	// SigningCertURL may resolve to (REQ-HOOK-SES-06).  Typically
	// ["sns.<region>.amazonaws.com"].  A request whose SigningCertURL
	// hostname is not in this list is rejected before any network
	// activity.
	SignatureCertHostAllowlist []string `toml:"signature_cert_host_allowlist,omitempty"`
	// AWSAccessKeyIDEnv is a secret reference ("$VAR" or
	// "file:/path") for the AWS access key ID.  Required when
	// Enabled is true.
	AWSAccessKeyIDEnv string `toml:"aws_access_key_id_env,omitempty"`
	// AWSSecretAccessKeyEnv is a secret reference for the AWS secret
	// access key.  Required when Enabled is true.
	AWSSecretAccessKeyEnv string `toml:"aws_secret_access_key_env,omitempty"`
	// AWSSessionTokenEnv is an optional secret reference for a
	// temporary session token (for STS-vended credentials).
	AWSSessionTokenEnv string `toml:"aws_session_token_env,omitempty"`
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

// ExternalImagesMode selects the operator-wide policy for handling
// HTML mail's external image references. See docs/design/server/
// requirements/17-external-images.md REQ-EXTIMG-01..05.
type ExternalImagesMode string

const (
	// ExternalImagesModeInternalize is the default: prefetch every
	// external image at delivery, attach as inline parts, rewrite
	// references to cid:. Sender sees one delivery-time fetch from
	// the operator's IP; user sees zero per-open fetches.
	// (REQ-EXTIMG-02 / REQ-EXTIMG-05)
	ExternalImagesModeInternalize ExternalImagesMode = "internalize"
	// ExternalImagesModePassthrough leaves bodies unchanged. The
	// suite's "show images" gating is the user's privacy fence.
	// DKIM signatures are preserved unchanged. (REQ-EXTIMG-03)
	ExternalImagesModePassthrough ExternalImagesMode = "passthrough"
)

// ExternalImagesConfig is the operator-facing configuration for the
// external-image internalization pipeline (17-external-images.md).
// Zero value matches "internalize" with the documented defaults.
type ExternalImagesConfig struct {
	// Mode selects the operator-wide policy (REQ-EXTIMG-01).
	// Empty = internalize.
	Mode ExternalImagesMode `toml:"mode,omitempty"`

	// Limits caps fetcher behaviour (REQ-EXTIMG-21..27).
	Limits ExternalImagesLimits `toml:"limits,omitempty"`

	// Network configures the SSRF guard surface (REQ-EXTIMG-30..37).
	Network ExternalImagesNetwork `toml:"network,omitempty"`

	// DKIM controls disposition of inbound DKIM signatures when the
	// rewriter modifies the body (REQ-EXTIMG-40..47).
	DKIM ExternalImagesDKIM `toml:"dkim,omitempty"`

	// Audit configures observability output (REQ-EXTIMG-80..82).
	Audit ExternalImagesAudit `toml:"audit,omitempty"`
}

// ExternalImagesLimits enforces fetcher resource caps. Zero values
// fall back to the documented defaults applied by the extimg package.
// See REQ-EXTIMG-21..27.
type ExternalImagesLimits struct {
	// MaxPerImageBytes truncates a single fetch beyond this size.
	// Default 5 MiB. (REQ-EXTIMG-21)
	MaxPerImageBytes int64 `toml:"max_per_image_bytes,omitempty"`
	// MaxPerMessageImages caps the number of fetch candidates per
	// message; the rest stay as their original URL. Default 100.
	// (REQ-EXTIMG-22)
	MaxPerMessageImages int `toml:"max_per_message_images,omitempty"`
	// MaxPerMessageBytes caps cumulative fetched bytes per message.
	// Default 50 MiB. (REQ-EXTIMG-23)
	MaxPerMessageBytes int64 `toml:"max_per_message_bytes,omitempty"`
	// PerImageConnectTimeout bounds the dial. Default 5s.
	// (REQ-EXTIMG-24)
	PerImageConnectTimeout Duration `toml:"per_image_connect_timeout,omitempty"`
	// PerImageTotalTimeout bounds dial + headers + body read.
	// Default 30s. (REQ-EXTIMG-24)
	PerImageTotalTimeout Duration `toml:"per_image_total_timeout,omitempty"`
	// PerMessageTimeout caps the wall-clock budget for the whole
	// message rewrite. Default 60s. (REQ-EXTIMG-24)
	PerMessageTimeout Duration `toml:"per_message_timeout,omitempty"`
	// ConcurrentFetches bounds in-flight fetches per message.
	// Default 8. (REQ-EXTIMG-20)
	ConcurrentFetches int `toml:"concurrent_fetches,omitempty"`
	// RequireHTTPS refuses http:// references. Default true.
	// (REQ-EXTIMG-25)
	RequireHTTPS *bool `toml:"require_https,omitempty"`
	// FollowRedirectsMax bounds redirect chain length, validated at
	// every hop (REQ-EXTIMG-26). Default 3.
	FollowRedirectsMax int `toml:"follow_redirects_max,omitempty"`
}

// ExternalImagesNetwork configures the SSRF guard. The defaults
// refuse the full set of IANA special-purpose ranges (IPv4 + IPv6).
// Operators MAY extend the deny list; AllowPrivate is the only
// loosening knob. See REQ-EXTIMG-30..37.
type ExternalImagesNetwork struct {
	// DenyCIDRs extends the built-in deny list with operator-specific
	// blocks. (REQ-EXTIMG-33)
	DenyCIDRs []string `toml:"deny_cidrs,omitempty"`
	// AllowPrivate flips the built-in RFC-1918 / loopback fence off
	// for operators with legitimate internal-fetch needs. Documented
	// as dangerous. (REQ-EXTIMG-33)
	AllowPrivate bool `toml:"allow_private,omitempty"`
}

// ExternalImagesDKIM selects what happens to DKIM signatures on a
// rewritten body. See REQ-EXTIMG-40..47.
type ExternalImagesDKIM struct {
	// OnModification: "strip" (default) drops the now-invalid
	// DKIM-Signature and any inbound Authentication-Results headers,
	// emitting a server-stamped Authentication-Results recording the
	// pre-rewrite verdict. "keep" leaves the broken signature in
	// place; useful only for debugging.
	// (REQ-EXTIMG-41..43)
	OnModification string `toml:"on_modification,omitempty"`
	// ARCSeal is reserved for a future iteration that will emit
	// ARC-Authentication-Results / ARC-Message-Signature / ARC-Seal
	// headers. Currently must remain false; non-false values fail
	// at Validate to surface premature use. (REQ-EXTIMG-47)
	ARCSeal bool `toml:"arc_seal,omitempty"`
}

// ExternalImagesAudit toggles observability output (REQ-EXTIMG-80..82).
type ExternalImagesAudit struct {
	// LogFetches emits one structured record per fetch with URL hash
	// (sha256 prefix; never the URL itself), bytes, status, duration,
	// resolved IP. Off by default. (REQ-EXTIMG-82)
	LogFetches bool `toml:"log_fetches,omitempty"`
}

// ServerConfig carries process-wide settings.
type ServerConfig struct {
	Hostname      string   `toml:"hostname"`
	DataDir       string   `toml:"data_dir"`
	RunAsUser     string   `toml:"run_as_user"`
	RunAsGroup    string   `toml:"run_as_group"`
	ShutdownGrace Duration `toml:"shutdown_grace,omitempty"`
	// MaxUploadSize caps a single JMAP /jmap/upload request body (the
	// Blob/upload endpoint backing attachment, inline-image, and
	// attachment-share uploads), and is advertised to clients as the JMAP
	// core maxSizeUpload capability. The upload streams straight to the
	// content-addressed blob store, so a large value does not buffer in
	// RAM. Accepts IEC/SI byte sizes ("1 GiB", "250 MB"). Unset/0 -> the
	// JMAP server's 50 MiB default.
	MaxUploadSize ByteSize `toml:"max_upload_size,omitempty"`
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
	DevMode            bool                     `toml:"dev_mode,omitempty"`
	AdminTLS           AdminTLSConfig           `toml:"admin_tls"`
	Storage            StorageConfig            `toml:"storage"`
	Snooze             SnoozeConfig             `toml:"snooze,omitempty"`
	UI                 UIConfig                 `toml:"ui,omitempty"`
	ImageProxy         ImageProxyConfig         `toml:"image_proxy,omitempty"`
	Chat               ChatConfig               `toml:"chat,omitempty"`
	Call               CallConfig               `toml:"call,omitempty"`
	TURN               TURNConfig               `toml:"turn,omitempty"`
	SmartHost          SmartHostConfig          `toml:"smart_host,omitempty"`
	Suite              SuiteConfig              `toml:"suite,omitempty"`
	AdminSPA           AdminSPAConfig           `toml:"admin_spa,omitempty"`
	Push               PushConfig               `toml:"push,omitempty"`
	Queue              QueueConfig              `toml:"queue,omitempty"`
	Secrets            SecretsConfig            `toml:"secrets,omitempty"`
	ExternalSubmission ExternalSubmissionConfig `toml:"external_submission,omitempty"`
	// IdentityCreation configures the principal-initiated Identity
	// creation and email verification surface (REQ-IDENT-10..36). An
	// omitted section yields a fully working default-enabled
	// configuration.
	IdentityCreation      IdentityCreationConfig      `toml:"identity_creation,omitempty"`
	DirectoryAutocomplete DirectoryAutocompleteConfig `toml:"directory_autocomplete,omitempty"`
	TaggedAddresses       TaggedAddressesConfig       `toml:"tagged_addresses,omitempty"`
	// TrashRetention configures the email trash retention sweeper
	// (REQ-STORE-90). Defaults match the trashretention package
	// constants: 30 days, 1-hour sweep interval.
	TrashRetention TrashRetentionConfig `toml:"trash_retention,omitempty"`
	// AttachmentShares configures the attachment-share feature
	// (docs/design/server/requirements/25-attachment-shares.md
	// REQ-SHARE-30..52). An omitted section produces a fully functional
	// configuration with the documented defaults. The feature is enabled
	// by default but is inert (the JMAP capability is not advertised and
	// /share routes return 404) until public_base_url is set.
	AttachmentShares AttachmentSharesConfig `toml:"attachment_shares,omitempty"`
	// OAuthProviders maps provider name to per-provider OAuth 2.0 client
	// configuration for server-mediated OAuth flows (REQ-AUTH-EXT-SUBMIT-03).
	// Provider names are normalised to lowercase at parse time. The reserved
	// names "gmail" and "m365" are the canonical Google and Microsoft 365
	// providers; operators may omit them when only a custom provider is used.
	// Each entry requires client_secret_ref (a $VAR or file:/path reference;
	// inline secrets are rejected at Validate per STANDARDS §9), auth_url,
	// token_url, and at least one entry in scopes.
	OAuthProviders map[string]OAuthProviderConfig `toml:"oauth_providers,omitempty"`
	// PublicBaseURL is the externally-reachable base URL of the public
	// HTTP listener (e.g. "https://mail.example.com"). It is used to
	// build signed webhook fetch URLs (REQ-HOOK-30..31) delivered to
	// webhook receivers who then GET the blob back. The default
	// "https://<hostname>" is suitable for single-domain deployments;
	// operators running behind a reverse proxy or split-horizon DNS
	// set this explicitly.
	PublicBaseURL string `toml:"public_base_url,omitempty"`
	// PortReportFile, when set, is the absolute path where herold writes
	// the actual bound addresses of every listener after all listeners are
	// up and before the server marks itself ready. The file uses TOML with
	// one [[listener]] entry per configured listener, each carrying the
	// kernel-assigned address (useful when any listener uses port 0).
	// The file is written atomically (tmp + rename) and removed on graceful
	// shutdown. Required when any [[listener]] address uses port 0.
	PortReportFile string `toml:"port_report_file,omitempty"`
}

// SecretsConfig carries references to operator-supplied secrets used for
// at-rest encryption of per-identity submission credentials
// (REQ-AUTH-EXT-SUBMIT-02). Per STANDARDS §9, the key is never specified
// inline; it must be a $VAR or file:/path reference resolved via
// sysconfig.ResolveSecretStrict.
//
// The referenced value must be 64 hex characters (encoding 32 bytes); the
// internal/secrets package decodes and validates it at boot via LoadDataKey.
//
// Example (system.toml):
//
//	[server.secrets]
//	data_key_ref = "$HEROLD_DATA_KEY"
type SecretsConfig struct {
	// DataKeyRef is a secret reference ("$VAR" or "file:/path") resolving
	// to a 64-hex-character (32-byte) AES-grade key used to seal and open
	// per-identity SMTP submission credentials. Required when
	// [server.external_submission].enabled is true.
	DataKeyRef string `toml:"data_key_ref,omitempty"`
}

// ExternalSubmissionConfig controls the external SMTP submission feature
// (REQ-AUTH-EXT-SUBMIT-01..10). When Enabled is false (the default) the
// submission-credential store methods are available but the HTTP surface
// and background token-refresh sweeper are not started.
//
// Example (system.toml):
//
//	[server.external_submission]
//	enabled = true
type ExternalSubmissionConfig struct {
	// Enabled activates the external SMTP submission surface. When true,
	// [server.secrets].data_key_ref must be configured; the server refuses
	// to start otherwise (boot-time hard-fail per architectural decision 4).
	// Default false.
	Enabled bool `toml:"enabled,omitempty"`
	// SweeperWorkers is the size of the bounded worker pool that the OAuth
	// refresh sweeper dispatches refresh attempts to. Zero or absent defaults
	// to 4 (architectural decision 1, Phase 6). A higher value allows more
	// concurrent refreshes when many OAuth identities are due at once;
	// raising it above 16 provides diminishing returns for typical deployments.
	SweeperWorkers int `toml:"sweeper_workers,omitempty"`
}

// IdentityCreationExternalDomainsMode selects the external-domain policy for
// principal-initiated Identity creation (REQ-IDENT-20). External domains are
// any domain not in Meta().ListLocalDomains; hosted domains are always
// permitted regardless of this knob.
type IdentityCreationExternalDomainsMode string

const (
	// IdentityCreationExternalDomainsAllowAll lets a principal create an
	// Identity for any external domain. The default; matches the most
	// permissive posture and keeps operator-controlled deployments out of
	// the way of users who already own their domain elsewhere.
	IdentityCreationExternalDomainsAllowAll IdentityCreationExternalDomainsMode = "allow_all"
	// IdentityCreationExternalDomainsAllowlist restricts creation to the
	// external domains explicitly listed in ExternalDomainAllowlist. The
	// allowlist MUST be non-empty when this mode is selected.
	IdentityCreationExternalDomainsAllowlist IdentityCreationExternalDomainsMode = "allowlist"
	// IdentityCreationExternalDomainsDenyAll refuses creation of any
	// external-domain Identity. Hosted-domain identities remain permitted.
	IdentityCreationExternalDomainsDenyAll IdentityCreationExternalDomainsMode = "deny_all"
)

// IdentityCreationConfig configures the principal-initiated Identity
// creation and email verification surface (REQ-IDENT-10..36). The whole
// section is optional; an omitted block produces a fully working
// configuration with sensible defaults (enabled, allow-all external
// domains, 7-day unverified purge window, 60s resend cooldown, 5/day
// resend cap).
//
// Example (system.toml):
//
//	[server.identity_creation]
//	enabled = true
//	verifier_from = "postmaster@example.local"
//	external_domains = "allow_all"                # allow_all | allowlist | deny_all
//	external_domain_allowlist = ["gmail.com"]     # only when external_domains = "allowlist"
//	unverified_purge_after = "7d"
//	resend_cooldown_seconds = 60
//	resend_daily_cap = 5
type IdentityCreationConfig struct {
	// Enabled is the master switch for principal-initiated Identity
	// creation and the matching JMAP capability advertisement
	// (REQ-IDENT-11). Defaults to true; set to false explicitly to
	// hide the "Add identity" affordance and refuse Identity/set { create }
	// at the JMAP boundary. The pointer encoding lets us distinguish an
	// omitted key (default-true) from an explicit `enabled = false`.
	Enabled *bool `toml:"enabled,omitempty"`
	// VerifierFrom is the sender address embedded in the envelope MAIL FROM
	// and header From of outbound verification messages (REQ-IDENT-30). MUST
	// be a locally-hosted address so DKIM signs under a key herold controls.
	// When omitted, the dispatcher resolves the sender at runtime: if exactly
	// one local domain is registered it uses "postmaster@<that domain>"; if
	// multiple local domains are registered the dispatch fails with a clear
	// error instructing the operator to set this field explicitly. Set this
	// field to avoid the runtime lookup and to select the right domain in
	// multi-domain deployments.
	VerifierFrom string `toml:"verifier_from,omitempty"`
	// ExternalDomains selects the external-domain Identity creation policy
	// (REQ-IDENT-20). One of "allow_all" (default), "allowlist", or
	// "deny_all". Hosted-domain creation is always permitted independent
	// of this knob.
	ExternalDomains IdentityCreationExternalDomainsMode `toml:"external_domains,omitempty"`
	// ExternalDomainAllowlist enumerates the external domains a principal
	// may create an Identity for when ExternalDomains == "allowlist".
	// Bare domain names ("gmail.com", "company.com"); lowercase ASCII.
	// Ignored in any other mode; required (non-empty) in allowlist mode.
	ExternalDomainAllowlist []string `toml:"external_domain_allowlist,omitempty"`
	// UnverifiedPurgeAfter is the maximum age of an unverified Identity row
	// before the background GC destroys it (REQ-IDENT-35). Accepts standard
	// Go durations plus a "d"-suffix shorthand (e.g. "7d" == 168h). Default
	// "7d".
	UnverifiedPurgeAfter string `toml:"unverified_purge_after,omitempty"`
	// ResendCooldownSeconds is the minimum interval between two consecutive
	// verification-message resends on the same Identity (REQ-IDENT-36).
	// Must be > 0. Default 60.
	ResendCooldownSeconds int `toml:"resend_cooldown_seconds,omitempty"`
	// ResendDailyCap is the hard per-Identity ceiling on verification-message
	// resends in any trailing 24h window (REQ-IDENT-36). Must be > 0.
	// Default 5.
	ResendDailyCap int `toml:"resend_daily_cap,omitempty"`
	// GCInterval is the cadence at which the verification-token GC
	// sweeper runs (REQ-IDENT-35). Accepts standard Go durations.
	// Default "6h" -- the spec's "periodic GC pass (default every 6
	// hours)". Sub-minute values are clamped to one minute in the
	// sweeper to avoid pinning a write transaction.
	GCInterval string `toml:"gc_interval,omitempty"`
}

// DirectoryAutocompleteMode is the typed enum for the compose To-field
// autocomplete behaviour (see DirectoryAutocompleteConfig).
type DirectoryAutocompleteMode string

const (
	// DirectoryAutocompleteModeAll enables cross-domain principal suggestions
	// from the full instance directory. Opt-in; operators who want to allow
	// any authenticated user to discover all principals set this.
	DirectoryAutocompleteModeAll DirectoryAutocompleteMode = "all"
	// DirectoryAutocompleteModeDomain suggests only principals that share the
	// calling user's email domain (privacy-respecting default).
	DirectoryAutocompleteModeDomain DirectoryAutocompleteMode = "domain"
	// DirectoryAutocompleteModeOff disables the feature entirely. The JMAP
	// directory-autocomplete capability is not advertised when mode is "off".
	DirectoryAutocompleteModeOff DirectoryAutocompleteMode = "off"
)

// DirectoryAutocompleteConfig controls the compose To-field autocomplete
// feature that suggests principals known to this herold instance.
//
// When the section is omitted entirely, Mode defaults to "domain" (only
// suggest principals sharing the caller's email domain). Set mode = "all"
// to opt in to cross-domain suggestions, or mode = "off" to disable the
// feature and suppress the JMAP capability advertisement.
//
// Example (system.toml):
//
//	[server.directory_autocomplete]
//	mode = "domain"   # "all" | "domain" (default) | "off"
type DirectoryAutocompleteConfig struct {
	// Mode controls which principals are surfaced as autocomplete candidates.
	// "all": suggest any principal on the instance regardless of domain.
	// "domain": suggest only principals sharing the caller's email domain (default).
	// "off": disable autocomplete; the JMAP capability is not advertised.
	Mode DirectoryAutocompleteMode `toml:"mode,omitempty"`
}

// TaggedAddressesConfig controls the tagged-address (sub-addressing) feature
// described in docs/design/server/requirements/24-tagged-addresses.md.
// The feature lets a user hand out sub-addressed variants of one of their
// email addresses (alice+amazon@..., alice+newsletters@...) and have inbound
// mail to each variant routed to a label of their choice, optionally
// skipping the inbox and/or marked read.
//
// When the section is omitted entirely, the feature is enabled with the
// documented default caps (100 filters, 500 dismissals per principal). Set
// Enabled = false to suppress the JMAP capability advertisement and skip
// the inbound pipeline's tag-filter evaluation; all sub-addressed mail
// falls through to Sieve unchanged in that case (REQ-TAG-20).
//
// The caps are enforced at filter-create / dismissal-create time
// (REQ-TAG-11). They are read at request time from the live cfg via the
// reload-safe sysconfig pointer so a SIGHUP-driven adjustment takes effect
// without a restart. Flipping Enabled at runtime does NOT retract the JMAP
// capability from already-issued sessions until they reconnect — same
// semantics as [server.external_submission].
//
// Example (system.toml):
//
//	[server.tagged_addresses]
//	enabled = true
//	max_filters_per_principal = 100
//	max_dismissals_per_principal = 500
type TaggedAddressesConfig struct {
	// Enabled is the master switch for the tagged-address feature. Pointer
	// so an absent section can be distinguished from an explicit `false`;
	// the default (when nil) is true. When false the JMAP capability is
	// not advertised and the inbound pipeline skips REQ-TAG-20 evaluation.
	Enabled *bool `toml:"enabled,omitempty"`
	// MaxFiltersPerPrincipal caps the total tagged_address_filters rows a
	// single principal may hold across all their identities. Must be > 0;
	// defaults to 100 (REQ-TAG-11).
	MaxFiltersPerPrincipal int `toml:"max_filters_per_principal,omitempty"`
	// MaxDismissalsPerPrincipal caps the total tagged_address_dismissals
	// rows a single principal may hold. Must be > 0; defaults to 500
	// (REQ-TAG-11).
	MaxDismissalsPerPrincipal int `toml:"max_dismissals_per_principal,omitempty"`
}

// TaggedAddressesEnabled reports whether the tagged-address feature is
// enabled, treating an absent Enabled field (nil) as the documented
// default of true.
func (c *TaggedAddressesConfig) TaggedAddressesEnabled() bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// AttachmentSharesConfig controls the attachment-share feature
// (docs/design/server/requirements/25-attachment-shares.md).
//
// The feature lets a sender lift a large or scanner-hostile attachment out of
// a message and replace it with a capability URL the recipient fetches over
// HTTPS. A share is an owner-scoped, TTL-bounded, content-addressed object
// with its own lifecycle, independent of any mailbox.
//
// When Enabled is true but PublicBaseURL is empty (the zero value) the feature
// is inert: the JMAP capability is not advertised and the /share routes return
// 404. Set PublicBaseURL to activate the surface.
//
// Example (system.toml):
//
//	[server.attachment_shares]
//	enabled = true
//	public_base_url = "https://mail.example.com"
//	default_ttl  = "48h"
//	max_ttl      = "90d"
//	pending_ttl  = "48h"
//	revoked_grace = "24h"
//	max_shares_per_principal = 1000
//	share_quota_per_principal = "5 GiB"
//	download_requests_per_ip_per_share = 50
//	download_requests_window = "10m"
type AttachmentSharesConfig struct {
	// Enabled is the master switch for the attachment-share feature. When
	// false the JMAP capability is not advertised, the /share routes return
	// 404, and the composer's offload affordance disappears. Pointer so an
	// absent key (default-true) is distinguishable from an explicit false.
	// The feature is also inert when PublicBaseURL is empty regardless of
	// this flag (REQ-SHARE-30).
	Enabled *bool `toml:"enabled,omitempty"`
	// PublicBaseURL is the externally-reachable HTTPS origin of the public
	// listener (e.g. "https://mail.example.com"). Recipients receive
	// capability URLs of the form {public_base_url}/share/{id}. Required
	// when Enabled; MUST use the https scheme. An http URL is rejected at
	// Validate because capability tokens carried over http are trivially
	// intercepted (REQ-SHARE-11d).
	PublicBaseURL string `toml:"public_base_url,omitempty"`
	// DefaultTTL is the share lifetime applied when a pending share is
	// confirmed to active state (REQ-SHARE-20 / REQ-SHARE-21). Accepts
	// standard Go durations plus a "d" day suffix ("30d" == 720h). Default
	// "48h". Must not exceed MaxTTL. The end user can override per share
	// when offloading (compose expiry picker, REQ-ATT-65).
	DefaultTTL DurationExtended `toml:"default_ttl,omitempty"`
	// MaxTTL is the ceiling the owner cannot exceed when creating or
	// shortening a share's expiry (REQ-SHARE-03 / REQ-SHARE-42). Accepts
	// the same format as DefaultTTL. Default "90d". Must be >= DefaultTTL.
	MaxTTL DurationExtended `toml:"max_ttl,omitempty"`
	// PendingTTL is the lifetime of an unconfirmed (compose-abandoned)
	// share before the sweeper deletes it (REQ-SHARE-20 / REQ-SHARE-23).
	// Accepts the same format as DefaultTTL. Default "48h" -- matched to
	// DefaultTTL so a still-pending share shows the same lifetime it will
	// keep once sent. Must be <= DefaultTTL.
	PendingTTL DurationExtended `toml:"pending_ttl,omitempty"`
	// RevokedGrace is the delay between a revocation and the sweeper
	// removing the row (REQ-SHARE-22 / REQ-SHARE-23). During this window
	// the owner's management view shows the share as "revoked" rather than
	// making it vanish immediately. Accepts the same format as DefaultTTL.
	// Default "24h".
	RevokedGrace DurationExtended `toml:"revoked_grace,omitempty"`
	// MaxSharesPerPrincipal caps the number of active+pending shares a
	// single principal may hold simultaneously (REQ-SHARE-12). Creation
	// past this cap fails with forbidden { too_many_shares }. Default 1000.
	// Must be > 0.
	MaxSharesPerPrincipal int `toml:"max_shares_per_principal,omitempty"`
	// ShareQuotaPerPrincipal is the per-principal share storage quota
	// (REQ-SHARE-50). Accepts human-readable byte sizes ("5 GiB",
	// "512 MiB"). Quota is measured over distinct blobs referenced by the
	// principal's shares; a blob already referenced by a mailbox message
	// adds zero marginal share quota. Default "5 GiB". Must be > 0.
	ShareQuotaPerPrincipal ByteSize `toml:"share_quota_per_principal,omitempty"`
	// DownloadRequestsPerIPPerShare caps how many download requests a
	// single source IP may make against a single share within
	// DownloadRequestsWindow (REQ-SHARE-32). On exceed the server returns
	// 429 with Retry-After. In-process state; restarting the server resets
	// the counters. Default 50. Must be > 0.
	DownloadRequestsPerIPPerShare int `toml:"download_requests_per_ip_per_share,omitempty"`
	// DownloadRequestsWindow is the sliding time window for
	// DownloadRequestsPerIPPerShare (REQ-SHARE-32). Accepts standard Go
	// durations. Default "10m". Must be > 0.
	DownloadRequestsWindow Duration `toml:"download_requests_window,omitempty"`
}

// AttachmentSharesEnabled reports whether the attachment-share feature is
// active. The feature requires both Enabled (default true) and a non-empty
// PublicBaseURL; returning true here does NOT mean the surface is live — the
// caller must also check PublicBaseURL.
func (c *AttachmentSharesConfig) AttachmentSharesEnabled() bool {
	if c == nil || c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// AttachmentSharesActive reports whether the full attachment-share surface is
// operational: Enabled is true (or defaulted) AND PublicBaseURL is set. The
// JMAP capability is only advertised and /share routes only mounted when this
// returns true.
func (c *AttachmentSharesConfig) AttachmentSharesActive() bool {
	return c.AttachmentSharesEnabled() && c.PublicBaseURL != ""
}

// OAuthProviderConfig is the per-provider OAuth 2.0 client configuration for
// the server-mediated external SMTP submission OAuth flow
// (REQ-AUTH-EXT-SUBMIT-03).
//
// Example (system.toml):
//
//	[server.oauth_providers.gmail]
//	client_id     = "123456789012-abc.apps.googleusercontent.com"
//	client_secret_ref = "$HEROLD_GMAIL_CLIENT_SECRET"
//	auth_url      = "https://accounts.google.com/o/oauth2/v2/auth"
//	token_url     = "https://oauth2.googleapis.com/token"
//	scopes        = ["https://mail.google.com/"]
type OAuthProviderConfig struct {
	// ClientID is the OAuth 2.0 client identifier issued by the provider.
	ClientID string `toml:"client_id"`
	// ClientSecretRef is a secret reference ("$VAR" or "file:/path") that
	// resolves to the OAuth 2.0 client secret. Inline values are rejected at
	// Validate per STANDARDS §9.
	ClientSecretRef string `toml:"client_secret_ref"`
	// AuthURL is the provider's authorisation endpoint.
	AuthURL string `toml:"auth_url"`
	// TokenURL is the provider's token endpoint used for code exchange and
	// refresh.
	TokenURL string `toml:"token_url"`
	// Scopes is the set of OAuth scopes requested. Must be non-empty.
	Scopes []string `toml:"scopes"`
}

// IMAPImportConfig is the operator-facing [imap_import] block for the
// inbound IMAP live-mirror feature (docs/design/server/requirements/
// 19-imap-import.md REQ-IMAP-IMP-05, -11, -22, -23, -26).
//
// Per-account upstream settings (host, credentials, folder maps, etc.)
// are stored in the DB; only instance-wide policy knobs and OAuth
// application registrations belong here. An absent block is valid and
// produces a fully defaulted, functional configuration.
//
// Example (system.toml):
//
//	[imap_import]
//	allow_password = true          # REQ-IMAP-IMP-05: set false to forbid plain-password auth
//	concurrent_accounts = 16       # REQ-IMAP-IMP-26: goroutine-pool size
//	poll_interval = "60s"          # REQ-IMAP-IMP-23: NOOP-poll cadence when IDLE unsupported
//
//	[imap_import.oauth.google]
//	client_id         = "123456789012-abc.apps.googleusercontent.com"
//	client_secret_ref = "$HEROLD_GOOGLE_IMAP_CLIENT_SECRET"
//	token_endpoint    = "https://oauth2.googleapis.com/token"
//	scope             = "https://mail.google.com/"
//
//	[imap_import.oauth.microsoft]
//	client_id         = "..."
//	client_secret_ref = "$HEROLD_MICROSOFT_IMAP_CLIENT_SECRET"
//	token_endpoint    = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
//	scope             = "https://outlook.office.com/IMAP.AccessAsUser.All"
type IMAPImportConfig struct {
	// AllowPassword controls whether auth_method=password is accepted for
	// per-account upstream configurations (REQ-IMAP-IMP-05). Defaults to
	// true so existing deployments keep working; operators who want to
	// mandate app-password or xoauth2 set this to false. auth_method=
	// app_password and auth_method=xoauth2 are unaffected by this knob.
	// Pointer so an absent key (default-true) is distinguishable from an
	// explicit `allow_password = false`.
	AllowPassword *bool `toml:"allow_password,omitempty"`
	// ConcurrentAccounts is the size of the goroutine pool that supervises
	// per-account IMAP workers (REQ-IMAP-IMP-26). One slow upstream does not
	// block other accounts on the same herold instance. Default 16; must be >= 1.
	ConcurrentAccounts int `toml:"concurrent_accounts,omitempty"`
	// PollInterval is the cadence at which the worker sends a NOOP command
	// to poll for new mail when the upstream IMAP server does not advertise
	// RFC 2177 IDLE (REQ-IMAP-IMP-23). Default 60s. Must be > 0. Has no
	// effect when the upstream supports IDLE.
	PollInterval Duration `toml:"poll_interval,omitempty"`
	// InsecureSkipTLSVerify disables TLS certificate verification for all
	// upstream IMAP connections. Intended only for development instances
	// with self-signed certificates. Never enable in production.
	InsecureSkipTLSVerify bool `toml:"insecure_skip_tls_verify,omitempty"`
	// OAuth maps provider names (e.g. "google", "microsoft") to their OAuth
	// 2.0 application credentials. Provider names are normalised to lowercase
	// at parse time. These entries are required when any per-account
	// auth_method=xoauth2 configuration references the provider; the per-
	// account record stores a sealed refresh token and the worker exchanges it
	// here before each IMAP connection (REQ-IMAP-IMP-03; decision 3).
	// This is distinct from [server.oauth_providers], which is for outbound
	// SMTP submission OAuth — IMAP OAuth needs the restricted IMAP scope under
	// a separately-registered application (NG11).
	OAuth map[string]IMAPImportOAuthProvider `toml:"oauth,omitempty"`
	// DefaultFolderMap is an operator-supplied, system-wide default folder
	// mapping table keyed by upstream-host pattern (REQ-IMAP-IMP-11). Each
	// entry's mappings apply to every account whose host matches the entry's
	// Host pattern; per-account folder maps (stored in the DB) win over these
	// defaults, which in turn win over the name-equals-name fallback
	// (REQ-IMAP-IMP-10). An absent table leaves the previous behaviour
	// (per-account map or name-equals-name) unchanged.
	//
	// Example (system.toml):
	//
	//	[[imap_import.default_folder_map]]
	//	host = "imap.gmail.com"
	//	[imap_import.default_folder_map.mappings]
	//	"INBOX" = "INBOX"
	//	"[Gmail]/Sent Mail" = "Sent"
	DefaultFolderMap []IMAPImportDefaultFolderMap `toml:"default_folder_map,omitempty"`
}

// IMAPImportDefaultFolderMap is one operator-supplied default folder-map
// rule scoped to an upstream-host pattern (REQ-IMAP-IMP-11).
type IMAPImportDefaultFolderMap struct {
	// Host is the upstream-host pattern this rule applies to. Matching is
	// case-insensitive and supports shell-style wildcards (path.Match
	// semantics, e.g. "imap.gmail.com" or "*.gmail.com").
	Host string `toml:"host"`
	// Mappings maps an upstream IMAP folder name to the herold mailbox name
	// it should be mirrored into. Upstream names are matched verbatim
	// (case-sensitive, as the upstream presents them).
	Mappings map[string]string `toml:"mappings"`
}

// IMAPImportDefaultFolderMapFor returns the merged default folder mapping
// (upstream-folder -> herold-mailbox) for the given upstream host
// (REQ-IMAP-IMP-11). Every DefaultFolderMap entry whose Host pattern matches
// host contributes its mappings; when multiple entries match, later entries
// override earlier ones for the same upstream folder. Returns nil when no
// entry matches, so callers can cheaply skip the merge.
func (ii *IMAPImportConfig) IMAPImportDefaultFolderMapFor(host string) map[string]string {
	if ii == nil || len(ii.DefaultFolderMap) == 0 {
		return nil
	}
	var merged map[string]string
	for _, rule := range ii.DefaultFolderMap {
		if !matchHostPattern(rule.Host, host) {
			continue
		}
		for up, hm := range rule.Mappings {
			if merged == nil {
				merged = make(map[string]string)
			}
			merged[up] = hm
		}
	}
	return merged
}

// matchHostPattern reports whether host matches pattern (case-insensitive,
// path.Match shell-glob semantics). A malformed pattern never matches.
func matchHostPattern(pattern, host string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(host))
	return err == nil && ok
}

// IMAPImportAllowPassword reports whether plain-password auth is permitted for
// upstream IMAP accounts (REQ-IMAP-IMP-05). Returns true when AllowPassword is
// nil (the documented default) or explicitly true; false when the operator set
// allow_password = false.
func (ii *IMAPImportConfig) IMAPImportAllowPassword() bool {
	if ii == nil || ii.AllowPassword == nil {
		return true
	}
	return *ii.AllowPassword
}

// IMAPImportOAuthProvider holds the OAuth 2.0 application credentials an
// operator registers for IMAP xoauth2 access (REQ-IMAP-IMP-03; decision 3).
// This is NOT herold's login-OIDC federation (that is RP-for-login only, NG11):
// IMAP access requires the restricted IMAP scope under an OAuth application the
// operator separately registers with the provider.
//
// Example (system.toml):
//
//	[imap_import.oauth.google]
//	client_id         = "123456789012-abc.apps.googleusercontent.com"
//	client_secret_ref = "$HEROLD_GOOGLE_IMAP_CLIENT_SECRET"
//	token_endpoint    = "https://oauth2.googleapis.com/token"
//	scope             = "https://mail.google.com/"
type IMAPImportOAuthProvider struct {
	// ClientID is the OAuth 2.0 client identifier issued by the provider.
	// Required.
	ClientID string `toml:"client_id"`
	// ClientSecretRef is a secret reference ("$VAR" or "file:/path") resolving
	// to the OAuth 2.0 client secret. Inline values are rejected at Validate
	// per STANDARDS §9 / REQ-OPS-04 / REQ-OPS-161.
	ClientSecretRef string `toml:"client_secret_ref"`
	// TokenEndpoint is the provider's token endpoint URL, used to exchange a
	// refresh token for a short-lived access token before each IMAP connection.
	// Required; must be an absolute HTTPS URL.
	// Canonical values:
	//   google:    "https://oauth2.googleapis.com/token"
	//   microsoft: "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	TokenEndpoint string `toml:"token_endpoint"`
	// Scope is the OAuth scope string passed in the token-refresh request.
	// Optional; when empty the worker omits the scope parameter and relies on
	// the refresh token already carrying the correct scope from the initial
	// authorisation grant.
	// Canonical values:
	//   google:    "https://mail.google.com/"
	//   microsoft: "https://outlook.office.com/IMAP.AccessAsUser.All"
	Scope string `toml:"scope,omitempty"`
}

// TranslationConfig is the [translation] section of system.toml.
//
// It enables the operator opt-in server-side translation proxy (re #84).
// The feature is off by default; enabling it routes message text to a
// configured third-party API so the operator must consent explicitly.
// Two providers are supported: "mymemory" (free tier, no API key required
// though one raises the daily limit) and "deepl" (paid, api_key_ref
// required).
//
// Secrets MUST be supplied as secret references ($VAR or file:/path) per
// STANDARDS §9 / REQ-OPS-04 / REQ-OPS-161. Inline credentials are
// rejected at Validate.
//
// Example (system.toml):
//
//	[translation]
//	enabled       = true
//	provider      = "mymemory"                     # "mymemory" (default) | "deepl"
//	api_key_ref   = "$HEROLD_TRANSLATE_KEY"        # optional for mymemory, required for deepl
//	contact_email = "admin@example.com"            # mymemory: raises anonymous rate limit
//	default_source_lang = "en"                     # ISO-639-1 hint used when caller omits source_lang
type TranslationConfig struct {
	// Enabled is the master switch for the translation proxy. Default false:
	// the feature is off unless the operator explicitly opts in, because
	// enabling it routes user message text through a third-party API.
	Enabled bool `toml:"enabled,omitempty"`
	// Provider selects the backend translation API.
	// "mymemory" (default when enabled) uses the free MyMemory API.
	// "deepl" uses the DeepL API and requires api_key_ref.
	// Validated at Validate time when Enabled is true.
	Provider string `toml:"provider,omitempty"`
	// Endpoint overrides the provider's canonical base URL. Must be an
	// absolute HTTPS URL when set. When omitted, the client uses the
	// provider's documented default:
	//   mymemory: "https://api.mymemory.translated.net/get"
	//   deepl:    "https://api-free.deepl.com/v2/translate"
	Endpoint string `toml:"endpoint,omitempty"`
	// APIKeyRef is a secret reference ("$VAR" or "file:/path") resolving to
	// the translation API key. Required when Provider is "deepl" and Enabled
	// is true. Optional for "mymemory" — when set, it is passed as the "key"
	// query parameter to raise the registered-user daily limit. Inline values
	// are rejected at Validate per STANDARDS §9.
	APIKeyRef string `toml:"api_key_ref,omitempty"`
	// ContactEmail is the operator contact email forwarded to the MyMemory
	// API via the "de" query parameter to raise the anonymous daily request
	// limit. Only meaningful for the "mymemory" provider; ignored for "deepl".
	// This is a plain config value, not a secret reference.
	ContactEmail string `toml:"contact_email,omitempty"`
	// DefaultSourceLang is the ISO-639-1 source language code used when a
	// translate request omits source_lang. For MyMemory, which always needs a
	// langpair source, an empty DefaultSourceLang causes the client to use
	// "autodetect" as the source component of the langpair. For DeepL, an
	// empty DefaultSourceLang (with no per-request source_lang) omits the
	// source_lang parameter so DeepL performs automatic detection.
	DefaultSourceLang string `toml:"default_source_lang,omitempty"`
}

// TranslationEnabled reports whether the translation proxy feature is
// enabled. Returns false for a nil receiver or when Enabled is false.
func (c *TranslationConfig) TranslationEnabled() bool {
	return c != nil && c.Enabled
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

	// DispatcherEnabled toggles the outbound dispatcher. Defaults
	// to true; set false to disable Web Push delivery without
	// dropping the VAPID key (the JMAP capability still advertises
	// applicationServerKey so clients can register, but no pushes
	// fan out — useful for staged migrations or operator
	// debugging). When true and VAPID is unconfigured the
	// dispatcher stays alive but idle (logs "dispatcher idle" once
	// at startup).
	DispatcherEnabled *bool `toml:"dispatcher_enabled,omitempty"`

	// DispatcherPollIntervalSeconds is the change-feed poll cadence
	// when no work is available. Defaults to 5; below 1 is
	// rejected at Validate.
	DispatcherPollIntervalSeconds int `toml:"dispatcher_poll_interval_seconds,omitempty"`

	// HTTPTimeoutSeconds bounds a single outbound POST. Default 30.
	HTTPTimeoutSeconds int `toml:"http_timeout_seconds,omitempty"`

	// JWTExpirySeconds bounds the VAPID JWT exp - iat. Default
	// 43200 (12 h); hard-capped at 86400 (24 h) per push-gateway
	// practice.
	JWTExpirySeconds int `toml:"jwt_expiry_seconds,omitempty"`

	// RateLimitPerMinute caps sustained pushes per subscription
	// (REQ-PROTO-126). Default 60.
	RateLimitPerMinute int `toml:"rate_limit_per_minute,omitempty"`

	// RateLimitPerDay caps daily pushes per subscription. Default 1000.
	RateLimitPerDay int `toml:"rate_limit_per_day,omitempty"`

	// CooldownSeconds is the per-subscription cooldown applied on
	// sustained excess. Default 300 (5 min).
	CooldownSeconds int `toml:"cooldown_seconds,omitempty"`

	// CoalesceWindowSeconds is the per-(subscription, tag) replacement
	// window per REQ-PROTO-124. Default 30; capped at 300 (5 min) at
	// Validate so a misconfigured ceiling cannot defer pushes
	// indefinitely.
	CoalesceWindowSeconds int `toml:"coalesce_window_seconds,omitempty"`
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

// SuiteConfig configures the embedded Suite SPA mount on the public
// HTTP listener (REQ-DEPLOY-COLOC-01..05). The default packaging
// embeds the suite build artefacts into the herold binary at release
// time; an explicit AssetDir overrides the embedded FS for development
// hot-reload.
type SuiteConfig struct {
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

// AdminSPAConfig configures the embedded admin Svelte SPA mount on
// the admin HTTP listener at /admin/. The SPA is the only admin UI
// from Phase 3b of the merge plan onwards; the legacy HTMX UI at
// /ui/ has been retired (REQ-ADM-204; the admin listener now 308-
// redirects every /ui/* request to /admin/*).
type AdminSPAConfig struct {
	// AssetDir, when non-empty, makes the admin handler serve from
	// this directory instead of the embedded FS. The directory MUST
	// be an absolute path AND contain index.html at startup; the
	// validator refuses to start the server otherwise so a typo is
	// loud at boot rather than at first 404. Used in development to
	// hot-reload admin builds without rebuilding herold.
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
	// MessageTimestampGroupingSeconds controls when the chat UI
	// renders a timestamp under a message: only when more than this
	// many seconds elapsed since the previous message in the same
	// day-group. Default 120 (2 minutes). 0 means "always show".
	// Advertised to the client through the chat capability descriptor
	// so a single operator-supplied value drives every Suite session.
	MessageTimestampGroupingSeconds int `toml:"message_timestamp_grouping_seconds,omitempty"`
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

// TrashRetentionConfig tunes the email trash retention sweeper
// (REQ-STORE-90): how long messages sit in Trash before permanent deletion,
// and how often the sweeper scans. The defaults applied at applyDefaults
// match the package constants in internal/trashretention.
type TrashRetentionConfig struct {
	// RetentionDays is the number of days after which messages in the
	// Trash mailbox are permanently deleted. Default 30. Validate rejects
	// values below 1 (cannot be zero) or above 3650 (10 years is a typo).
	RetentionDays int `toml:"retention_days,omitempty"`
	// SweepIntervalSeconds is the cadence at which the sweeper scans
	// Trash mailboxes. Default 3600 (1 hour). Validate rejects values
	// below 60 to avoid unnecessary load and above 86400 (1 day).
	SweepIntervalSeconds int `toml:"sweep_interval_seconds,omitempty"`
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

// UIConfig configures the session-cookie and CSRF substrate shared by the
// JSON login endpoints (/api/v1/auth/login on both listeners) and the admin
// SPA mount at /admin/.
//
// The [server.ui] block was previously the config home for the now-deleted
// internal/protoui HTMX server. The fields that survive (CookieName,
// CSRFCookieName, SigningKeyEnv, SessionTTL, SecureCookies) are consumed by
// internal/protoadmin (admin-listener cookie auth) and internal/protologin
// (public-listener JSON login). The Enabled and PathPrefix fields were
// protoui-specific and have been removed.
//
// SecureCookies defaults to true: cookies issued for sessions and
// CSRF tokens carry the Secure attribute. Operators running behind a
// trusted localhost reverse proxy during development can override
// (set false), but production deployments MUST keep it true.
type UIConfig struct {
	// CookieName overrides the session cookie name (default
	// "herold_ui_session").
	CookieName string `toml:"cookie_name,omitempty"`
	// CSRFCookieName overrides the CSRF cookie name (default
	// "herold_ui_csrf").
	CSRFCookieName string `toml:"csrf_cookie_name,omitempty"`
	// SessionTTL is the idle timeout for all web sessions (Suite and admin
	// UI). A session is rejected when (now - last_seen_at) > SessionTTL.
	// Zero applies the default of one week (168h). The idle window slides
	// forward on every authenticated request so a continuously active
	// session never expires (REQ-AUTH-72, REQ-AUTH-73, issue #78).
	SessionTTL Duration `toml:"session_ttl,omitempty"`
	// SessionAbsoluteTTL is an optional hard cap on session lifetime,
	// measured from first login. Zero (the default) disables the cap so
	// the idle window is the sole expiry mechanism. When non-zero every
	// session issued after the cap exceeds the idle window is also bounded
	// by the absolute TTL (REQ-AUTH-72). Set this only when your security
	// policy requires a forced re-login interval regardless of activity.
	SessionAbsoluteTTL Duration `toml:"session_absolute_ttl,omitempty"`
	// AdminIdleTTL is accepted for backward-compatibility but no longer
	// has any effect. Admin and end-user sessions share the SessionTTL
	// idle window as of issue #78. Validate still enforces the 12-hour
	// ceiling so existing config files do not silently carry surprising
	// values. Remove this key from your config at your next convenience.
	AdminIdleTTL Duration `toml:"admin_idle_ttl,omitempty"`
	// AdminAbsoluteTTL is accepted for backward-compatibility but no
	// longer has any effect. See AdminIdleTTL. Validate still enforces
	// the 12-hour ceiling. Remove this key from your config at your next
	// convenience.
	AdminAbsoluteTTL Duration `toml:"admin_absolute_ttl,omitempty"`
	// SecureCookies, when nil, applies the secure-by-default policy
	// (true). Set explicitly to false only for development.
	SecureCookies *bool `toml:"secure_cookies,omitempty"`
	// SigningKeyEnv overrides the env var name the server reads for
	// the HMAC signing key for session cookies. When empty (the usual
	// case) the server reads the predefined variable HEROLD_UI_SESSION_KEY.
	// Set this only when you cannot use the standard variable name
	// (e.g. a secrets manager that mandates its own naming). If neither
	// this variable nor HEROLD_UI_SESSION_KEY holds a value of at least 32
	// bytes, the server generates a random per-process key (sessions are
	// invalidated on every restart and a WARN is emitted at startup).
	SigningKeyEnv string `toml:"signing_key_env,omitempty"`
	// ElevationTTL is the time-to-live for a successful TOTP step-up
	// elevation record (REQ-AUTH-74, issue #79). After this window the
	// admin-endpoint gate returns 403 step_up_required again.  Default
	// is 15 minutes ("15m"). Operators on high-security deployments may
	// shorten this; operators who need longer admin windows may extend it
	// up to 1 hour. Values outside [1m, 1h] are rejected by Validate.
	ElevationTTL Duration `toml:"elevation_ttl,omitempty"`
}

// QueueConfig exposes operator-facing knobs for the outbound delivery queue.
// Zero values fall back to the queue package's built-in defaults
// (Concurrency = 32, PerHostMax = 4). Setting non-zero values overrides
// the defaults; Validate rejects negative values and a Concurrency above
// the 1024 sanity cap so a config typo cannot OOM the box.
//
// Example:
//
//	[server.queue]
//	concurrency = 64
//	per_host_max = 8
type QueueConfig struct {
	// Concurrency is the maximum number of in-flight outbound SMTP
	// connections. 0 uses the queue default (32). Capped at 1024 at
	// Validate to prevent OOM on misconfiguration.
	Concurrency int `toml:"concurrency,omitempty"`
	// PerHostMax caps per-MX-hostname in-flight connections. 0 uses the
	// queue default (derived from Concurrency). Must be >= 0 and <=
	// Concurrency when both are non-zero.
	PerHostMax int `toml:"per_host_max,omitempty"`
	// DelayDSNThreshold is the wall-clock age at which a still-deferred
	// row generates an RFC 3461 NOTIFY=DELAY notification to the
	// original sender. 0 uses the queue default (4h, matching Postfix
	// and Sendmail operator practice). Idempotency: at most one delay
	// DSN per (queue row, recipient) tuple regardless of how many
	// transient retries follow.
	DelayDSNThreshold Duration `toml:"delay_dsn_threshold,omitempty"`
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
	// CacheSize maps to PRAGMA cache_size. Negative values are KiB;
	// positive values are page counts (SQLite semantics). Zero means
	// "use the built-in default" (-65536 = 64 MiB applied at Open).
	// Accepted range: [-1<<20, 1<<20]. Values outside that range are
	// rejected at Validate as likely typos.
	CacheSize int `toml:"cache_size,omitempty"`
	// WALAutocheckpoint maps to PRAGMA wal_autocheckpoint (default 1000
	// pages in SQLite). Zero leaves the SQLite built-in default.
	// Accepted range: [0, 1<<20].
	WALAutocheckpoint int `toml:"wal_autocheckpoint,omitempty"`
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

// DurationExtended is a TOML-friendly duration that additionally accepts a
// trailing "d" suffix as a 24-hour multiplier ("30d" == 720h). This extends
// the base Duration type for knobs where operators naturally express values in
// days. Standard Go duration strings ("1h", "30m", "10s") are also accepted.
// The zero value marshals to an empty string so omitempty works.
type DurationExtended time.Duration

// parseDurationExtended parses a duration string that may carry a "d" suffix
// for days, or any string accepted by time.ParseDuration. It returns an error
// for empty input or unparseable values.
func parseDurationExtended(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	// "Nd" with an optional decimal: only allow pure-day strings (no mixing
	// "1d2h") — the rest fall through to time.ParseDuration.
	if strings.HasSuffix(s, "d") && !strings.ContainsAny(s[:len(s)-1], "hms") {
		prefix := s[:len(s)-1]
		hours, err := time.ParseDuration(prefix + "h")
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		return hours * 24, nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return dur, nil
}

// UnmarshalText implements encoding.TextUnmarshaler for go-toml strict decoding.
func (d *DurationExtended) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*d = 0
		return nil
	}
	dur, err := parseDurationExtended(string(text))
	if err != nil {
		return err
	}
	*d = DurationExtended(dur)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d DurationExtended) MarshalText() ([]byte, error) {
	if d == 0 {
		return []byte{}, nil
	}
	return []byte(time.Duration(d).String()), nil
}

// AsDuration returns the value as a time.Duration.
func (d DurationExtended) AsDuration() time.Duration { return time.Duration(d) }

// ByteSize is a TOML-friendly byte quantity. It accepts strings in the form
// "N unit" where unit is one of B, KiB, MiB, GiB, TiB (binary units per
// IEC 80000-13), or KB, MB, GB, TB (decimal SI units). Plain integers are
// also accepted as a byte count with no unit. The zero value marshals to an
// empty string so omitempty works.
//
// Examples:
//
//	"5 GiB"  -> 5368709120
//	"512 MiB" -> 536870912
//	"1024"   -> 1024
type ByteSize int64

// parseByteSize parses a human-readable byte size string into a byte count.
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty byte size")
	}
	// Split into numeric and unit parts.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid byte size %q: must start with a number", s)
	}
	numStr := s[:i]
	unit := strings.TrimSpace(s[i:])
	n := int64(0)
	for _, ch := range numStr {
		n = n*10 + int64(ch-'0')
	}
	switch unit {
	case "", "B":
		return n, nil
	case "KiB":
		return n * 1024, nil
	case "MiB":
		return n * 1024 * 1024, nil
	case "GiB":
		return n * 1024 * 1024 * 1024, nil
	case "TiB":
		return n * 1024 * 1024 * 1024 * 1024, nil
	case "KB":
		return n * 1000, nil
	case "MB":
		return n * 1000 * 1000, nil
	case "GB":
		return n * 1000 * 1000 * 1000, nil
	case "TB":
		return n * 1000 * 1000 * 1000 * 1000, nil
	default:
		return 0, fmt.Errorf("invalid byte size %q: unknown unit %q (want B, KiB, MiB, GiB, TiB, KB, MB, GB, TB)", s, unit)
	}
}

// UnmarshalText implements encoding.TextUnmarshaler for go-toml strict decoding.
func (b *ByteSize) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*b = 0
		return nil
	}
	n, err := parseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = ByteSize(n)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (b ByteSize) MarshalText() ([]byte, error) {
	if b == 0 {
		return []byte{}, nil
	}
	return []byte(fmt.Sprintf("%d", int64(b))), nil
}

// AsInt64 returns the byte count as an int64.
func (b ByteSize) AsInt64() int64 { return int64(b) }

// AdminTLSConfig controls the cert used for the admin HTTPS surface.
// source may be:
//   - "none" -- no TLS; all admin/JMAP/public listeners must have tls="none".
//     Use this for the loopback quickstart where every listener runs plaintext.
//   - "file" -- cert_file + key_file required.
//   - "acme" -- uses the deployment-level [acme] account.
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
// validator only reads Kind when Protocol == "http" (which is the
// legacy single-HTTP-listener shape) or when DevMode is on.
type ListenerConfig struct {
	Name          string `toml:"name"`
	Address       string `toml:"address"`
	Protocol      string `toml:"protocol"`
	Kind          string `toml:"kind,omitempty"`
	TLS           string `toml:"tls"`
	AuthRequired  bool   `toml:"auth_required,omitempty"`
	ProxyProtocol bool   `toml:"proxy_protocol,omitempty"`
	// AllowPlainAuth opts a plaintext mail listener into accepting
	// PLAIN / LOGIN SASL authentication without TLS. Default false:
	// PLAIN / LOGIN are refused on cleartext sockets per RFC 8314 /
	// REQ-PROTO-12. Only meaningful for imap, smtp-submission, and
	// managesieve listeners with tls = "none"; the validator rejects
	// the flag in any other combination. Intended for the loopback /
	// container quickstart posture (issue #114), where the listener
	// is reachable only from trusted callers.
	AllowPlainAuth bool   `toml:"allow_plain_auth,omitempty"`
	CertFile       string `toml:"cert_file,omitempty"`
	KeyFile        string `toml:"key_file,omitempty"`
}

// AcmeConfig configures the ACME client (REQ-OPS-50..55).
// One account per deployment; the account key is stored at
// data_dir/acme/account.key with mode 0600 (REQ-OPS-52).
type AcmeConfig struct {
	// Email is the operator contact address registered with the ACME CA.
	// Required when [acme] is present.
	Email string `toml:"email,omitempty"`
	// DirectoryURL is the ACME directory endpoint. Defaults to Let's
	// Encrypt production when empty.
	DirectoryURL string `toml:"directory_url,omitempty"`
	// ChallengeType selects the validation method: "http-01" (default),
	// "tls-alpn-01", or "dns-01".
	ChallengeType string `toml:"challenge_type,omitempty"`
	// DNSPlugin names the [[plugin]] to use for dns-01 challenge publication.
	// Required when challenge_type = "dns-01".
	DNSPlugin string `toml:"dns_plugin,omitempty"`
}

// PluginConfig describes an out-of-process plugin declaration.
type PluginConfig struct {
	Name      string            `toml:"name"`
	Path      string            `toml:"path"`
	Type      string            `toml:"type"`
	Lifecycle string            `toml:"lifecycle"`
	Options   map[string]string `toml:"options,omitempty"`
}

// ObservabilityConfig holds the legacy single-sink fields plus the non-log
// observability knobs (metrics, OTLP). New deployments use [[log.sink]] and
// leave LogFormat/LogLevel/LogModules empty; the legacy fields are accepted as
// a translation shim (one place, translateLegacyObservability) that synthesises
// a single [[log.sink]] entry and emits a deprecation warning.
//
// MetricsBind and OTLPEndpoint remain here; they are not part of the per-sink
// model and require no migration.
type ObservabilityConfig struct {
	// LogFormat is the legacy single-sink format ("json" or "text").
	// Deprecated: use [[log.sink]] format instead.
	LogFormat string `toml:"log_format,omitempty"`
	// LogLevel is the legacy single-sink level.
	// Deprecated: use [[log.sink]] level instead.
	LogLevel string `toml:"log_level,omitempty"`
	// LogModules is the legacy per-module level map.
	// Deprecated: use [[log.sink]] modules instead.
	LogModules   map[string]string `toml:"log_modules,omitempty"`
	MetricsBind  string            `toml:"metrics_bind,omitempty"`
	OTLPEndpoint string            `toml:"otlp_endpoint,omitempty"`
}

// LogConfig is the top-level [log] table that holds the [[log.sink]] array
// (REQ-OPS-80..86). An empty Sinks slice is valid; applyDefaults inserts
// one stderr/auto/info sink so the process always produces some output.
//
// SecretKeys, if non-nil, overrides the default list of log attribute keys
// whose values are redacted (REQ-OPS-84). Matching is case-insensitive exact.
type LogConfig struct {
	Sink []LogSinkConfig `toml:"sink,omitempty"`
	// SecretKeys overrides the redaction key list for all sinks. Nil keeps
	// the observe.DefaultSecretKeys set. Splitting per-sink is not supported;
	// redaction is a single outermost layer applied before fan-out (REQ-OPS-84).
	SecretKeys []string `toml:"secret_keys,omitempty"`
}

// LogSinkConfig describes a single log destination (REQ-OPS-80..86b).
//
// Example (TOML):
//
//	[[log.sink]]
//	target = "stderr"
//	format = "auto"
//	level  = "info"
//	activities = { deny = ["poll", "access"] }
//
//	[[log.sink]]
//	target = "/var/log/herold/herold.jsonl"
//	format = "json"
//	level  = "debug"
type LogSinkConfig struct {
	// Target is one of "stderr", "stdout", or a filesystem path. A
	// path is resolved at parse time: relative paths are joined onto
	// Server.DataDir, then made absolute against the process's CWD,
	// so the same TOML works under systemd / Docker (where CWD may be
	// "/") and a dev launch (where CWD is the repo root). "/dev/null"
	// is rejected.
	Target string `toml:"target"`
	// Format selects the rendering: "json", "console", or "auto" (default).
	// "auto" resolves to "console" when Target is a TTY at process start,
	// "json" otherwise (REQ-OPS-81a).
	Format string `toml:"format,omitempty"`
	// Level is the minimum level for this sink: trace/debug/info/warn/error.
	// Default "info".
	Level string `toml:"level,omitempty"`
	// Modules maps subsystem/module names to per-module level overrides
	// (REQ-OPS-82). Keys match the "subsystem" or "module" slog attribute;
	// values are from the same closed enum as Level.
	Modules map[string]string `toml:"modules,omitempty"`
	// Activities is the optional activity filter for this sink (REQ-OPS-86b).
	// Set either Allow or Deny; setting both is a validation error.
	Activities ActivityFilterConfig `toml:"activities,omitempty"`
}

// ActivityFilterConfig holds the allow/deny lists for the activity filter
// (REQ-OPS-86b). Exactly one of Allow or Deny may be non-nil; both set is
// a validation error. Neither set means "pass all activities".
type ActivityFilterConfig struct {
	// Allow lists the only activities that pass this sink.
	// Mutually exclusive with Deny.
	Allow []string `toml:"allow,omitempty"`
	// Deny lists activities that are dropped by this sink.
	// Mutually exclusive with Allow.
	Deny []string `toml:"deny,omitempty"`
}

// RateLimit is a TOML-friendly rate specification of the form "N/unit"
// where unit is one of "s" (second), "m" (minute), "h" (hour), or a
// multi-unit window like "5m" (five minutes). Examples:
//
//   - "10/m"    — ten events per minute
//   - "1000/5m" — one thousand events per five minutes
//   - "10/s"    — ten events per second
//
// The zero value (empty string) means "no limit configured; use the
// built-in default". RateLimit is comparable: two RateLimits are equal
// when their raw text representations are identical.
type RateLimit struct {
	// raw is the original TOML string; preserved verbatim for
	// round-tripping and equality comparisons.
	raw string
	// Count is the number of events per window.
	Count int
	// Window is the duration of the rate window.
	Window time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler so go-toml strict
// decoding can populate a RateLimit from a TOML string value.
func (r *RateLimit) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		r.raw = ""
		return nil
	}
	count, window, err := parseRateLimit(s)
	if err != nil {
		return err
	}
	r.raw = s
	r.Count = count
	r.Window = window
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (r RateLimit) MarshalText() ([]byte, error) {
	return []byte(r.raw), nil
}

// IsZero reports whether the RateLimit is unset (empty string in TOML).
func (r RateLimit) IsZero() bool { return r.raw == "" }

// String returns the original TOML string representation.
func (r RateLimit) String() string { return r.raw }

// parseRateLimit parses "N/unit" or "N/Xunit" where unit is s/m/h.
// Examples: "10/m", "1000/5m", "10/s", "100/1h".
func parseRateLimit(s string) (count int, window time.Duration, err error) {
	slash := strings.LastIndex(s, "/")
	if slash < 1 || slash == len(s)-1 {
		return 0, 0, fmt.Errorf("invalid rate limit %q: want \"N/unit\" (e.g. \"10/m\", \"1000/5m\")", s)
	}
	countStr := s[:slash]
	windowStr := s[slash+1:]

	// Parse count.
	n := 0
	for _, ch := range countStr {
		if ch < '0' || ch > '9' {
			return 0, 0, fmt.Errorf("invalid rate limit %q: count %q is not a positive integer", s, countStr)
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return 0, 0, fmt.Errorf("invalid rate limit %q: count must be > 0", s)
	}

	// The window may be a bare unit letter ("m", "s", "h") or a
	// multiplied form ("5m", "12h"). We normalise by prepending "1"
	// when the string starts with a letter.
	var windowInput string
	if len(windowStr) > 0 && (windowStr[0] >= 'a' && windowStr[0] <= 'z') {
		windowInput = "1" + windowStr
	} else {
		windowInput = windowStr
	}
	// Validate the window string: optional digits + exactly one unit letter.
	if len(windowInput) < 2 {
		return 0, 0, fmt.Errorf("invalid rate limit %q: window %q too short", s, windowStr)
	}
	// Allow standard go duration syntax with only s/m/h units.
	w, durErr := time.ParseDuration(windowInput)
	if durErr != nil {
		return 0, 0, fmt.Errorf("invalid rate limit %q: window %q: %w", s, windowStr, durErr)
	}
	if w <= 0 {
		return 0, 0, fmt.Errorf("invalid rate limit %q: window must be > 0", s)
	}
	return n, w, nil
}

// ClientLogConfig is the [clientlog] section of system.toml (REQ-OPS-219).
// All fields are reloadable via SIGHUP (REQ-OPS-30).
//
// Example (system.toml):
//
//	[clientlog]
//	enabled = true
//	reorder_window_ms = 1000
//	livetail_default_duration = "15m"
//	livetail_max_duration    = "60m"
//
//	[clientlog.defaults]
//	telemetry_enabled = true
//
//	[clientlog.auth]
//	ring_buffer_rows = 100000
//	ring_buffer_age  = "168h"
//	rate_per_session = "1000/5m"
//	body_max_bytes   = 262144
//
//	[clientlog.public]
//	enabled          = true
//	otlp_egress      = false
//	ring_buffer_rows = 10000
//	ring_buffer_age  = "24h"
//	rate_per_ip      = "10/m"
//	body_max_bytes   = 8192
type ClientLogConfig struct {
	// Enabled is the master switch for the client-log pipeline. Default
	// true. When false, both ingest endpoints return 404 and the
	// bootstrap meta tag carries enabled:false so the SPA wrapper
	// installs no handlers (REQ-CLOG-12).
	Enabled *bool `toml:"enabled,omitempty"`
	// ReorderWindowMS is the per-session console reorder buffer window
	// in milliseconds (REQ-OPS-210). Default 1000.
	ReorderWindowMS int `toml:"reorder_window_ms,omitempty"`
	// LivetailDefaultDuration is the default live-tail session length
	// when the operator does not specify a duration (REQ-OPS-219).
	// Default "15m".
	LivetailDefaultDuration Duration `toml:"livetail_default_duration,omitempty"`
	// LivetailMaxDuration caps the duration an operator may request for
	// a live-tail session (REQ-OPS-219). Default "60m".
	LivetailMaxDuration Duration `toml:"livetail_max_duration,omitempty"`
	// Defaults holds deployment-wide default values for per-user
	// opt-in flags (REQ-OPS-208).
	Defaults ClientLogDefaultsConfig `toml:"defaults,omitempty"`
	// Auth configures the authenticated ingest endpoint
	// (REQ-OPS-200, REQ-OPS-216).
	Auth ClientLogAuthConfig `toml:"auth,omitempty"`
	// Public configures the anonymous ingest endpoint
	// (REQ-OPS-200, REQ-OPS-216, REQ-OPS-217).
	Public ClientLogPublicConfig `toml:"public,omitempty"`
}

// ClientLogDefaultsConfig holds deployment-wide default values for
// per-user client-log settings (REQ-OPS-208).
type ClientLogDefaultsConfig struct {
	// TelemetryEnabled is the default value of the per-user
	// telemetry opt-in (REQ-OPS-208). When true, kind=log and
	// kind=vital events are emitted for all users unless they have
	// explicitly opted out. Errors are always sent regardless.
	// Default true.
	TelemetryEnabled *bool `toml:"telemetry_enabled,omitempty"`
}

// ClientLogAuthConfig configures the authenticated client-log ingest
// endpoint (REQ-OPS-200, REQ-OPS-216).
type ClientLogAuthConfig struct {
	// RingBufferRows is the maximum number of rows retained in the
	// authenticated ring-buffer slice. Default 100000.
	RingBufferRows int `toml:"ring_buffer_rows,omitempty"`
	// RingBufferAge is the maximum age of rows in the authenticated
	// ring-buffer slice. Default "168h" (7 days).
	RingBufferAge Duration `toml:"ring_buffer_age,omitempty"`
	// RatePerSession is the per-session token-bucket rate for the
	// authenticated endpoint. Default "1000/5m". Empty means no limit.
	RatePerSession RateLimit `toml:"rate_per_session,omitempty"`
	// BodyMaxBytes is the maximum accepted request body size for the
	// authenticated endpoint. Default 262144 (256 KiB).
	BodyMaxBytes int `toml:"body_max_bytes,omitempty"`
}

// ClientLogPublicConfig configures the anonymous client-log ingest
// endpoint (REQ-OPS-200, REQ-OPS-216, REQ-OPS-217).
type ClientLogPublicConfig struct {
	// Enabled controls whether the anonymous endpoint is mounted.
	// When false the endpoint returns 404. Default true.
	Enabled *bool `toml:"enabled,omitempty"`
	// OTLPEgress controls whether anonymous events are forwarded to
	// the OTLP exporter (REQ-OPS-205). Default false to prevent
	// arbitrary internet traffic from inflating the operator's
	// observability bill.
	OTLPEgress bool `toml:"otlp_egress,omitempty"`
	// RingBufferRows is the maximum number of rows retained in the
	// public ring-buffer slice. Default 10000.
	RingBufferRows int `toml:"ring_buffer_rows,omitempty"`
	// RingBufferAge is the maximum age of rows in the public
	// ring-buffer slice. Default "24h".
	RingBufferAge Duration `toml:"ring_buffer_age,omitempty"`
	// RatePerIP is the per-IP token-bucket rate for the anonymous
	// endpoint. Default "10/m". Empty means no limit.
	RatePerIP RateLimit `toml:"rate_per_ip,omitempty"`
	// BodyMaxBytes is the maximum accepted request body size for the
	// anonymous endpoint. Default 8192 (8 KiB).
	BodyMaxBytes int `toml:"body_max_bytes,omitempty"`
}

// ClientLogEnabled returns true when the ClientLog pipeline is enabled.
// It applies the default (true) when Enabled is nil.
func (c *ClientLogConfig) ClientLogEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// ClientLogPublicEnabled returns true when the anonymous endpoint is
// mounted. It applies the default (true) when Public.Enabled is nil.
func (c *ClientLogConfig) ClientLogPublicEnabled() bool {
	return c.Public.Enabled == nil || *c.Public.Enabled
}

// TelemetryEnabledDefault returns the deployment default for per-user
// telemetry opt-in. It applies the default (true) when
// Defaults.TelemetryEnabled is nil.
func (c *ClientLogConfig) TelemetryEnabledDefault() bool {
	return c.Defaults.TelemetryEnabled == nil || *c.Defaults.TelemetryEnabled
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

// isValidModuleIdent reports whether s is a non-empty lowercase ASCII
// identifier (letters, digits, underscore, hyphen). Used to validate
// log_modules keys (REQ-OPS-82).
func isValidModuleIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
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
// (Protocol == "http") carry a Kind; non-HTTP listeners (smtp / imap
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
	validProtocols     = map[string]struct{}{"smtp": {}, "smtp-submission": {}, "imap": {}, "http": {}, "managesieve": {}}
	validListenerKinds = map[string]struct{}{
		ListenerKindPublic: {},
		ListenerKindAdmin:  {},
	}
	validTLSModes    = map[string]struct{}{"none": {}, "starttls": {}, "implicit": {}}
	validLifecycles  = map[string]struct{}{"long-running": {}, "on-demand": {}}
	validPluginType  = map[string]struct{}{"dns": {}, "spam": {}, "events": {}, "directory": {}, "delivery": {}}
	validLogLevels   = map[string]struct{}{"trace": {}, "debug": {}, "info": {}, "warn": {}, "error": {}}
	validLogFormats  = map[string]struct{}{"json": {}, "text": {}, "console": {}, "auto": {}}
	validSinkFormats = map[string]struct{}{"json": {}, "console": {}, "auto": {}}
	validActivities  = map[string]struct{}{"user": {}, "audit": {}, "system": {}, "poll": {}, "access": {}, "internal": {}}
	validBackends    = map[string]struct{}{"sqlite": {}, "postgres": {}}

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

	// Directory autocomplete modes.
	validDirectoryAutocompleteModes = map[DirectoryAutocompleteMode]struct{}{
		DirectoryAutocompleteModeAll:    {},
		DirectoryAutocompleteModeDomain: {},
		DirectoryAutocompleteModeOff:    {},
	}

	// Identity-creation external-domain policy modes (REQ-IDENT-20).
	validIdentityCreationExternalDomainsModes = map[IdentityCreationExternalDomainsMode]struct{}{
		IdentityCreationExternalDomainsAllowAll:  {},
		IdentityCreationExternalDomainsAllowlist: {},
		IdentityCreationExternalDomainsDenyAll:   {},
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
func Parse(raw []byte) (cfg *Config, err error) {
	// go-toml/v2 panics on certain malformed inputs instead of returning an
	// error (upstream issue, current as of v2.3.0). Recover so that Parse
	// always returns an error rather than crashing the process — required by
	// the FuzzLoad invariant and safe-parser posture for operator-supplied
	// config files.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sysconfig: parse: internal decoder panic: %v", r)
		}
	}()

	var c Config
	dec := toml.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("sysconfig: parse: %w", enrichDecodeError(err))
	}
	applyDefaults(&c)
	if err := Validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
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
	// Multi-sink defaults (REQ-OPS-80): if no [[log.sink]] entries were
	// configured AND the legacy [observability] block has no log fields,
	// insert a single stderr/auto/info sink so the process always emits
	// something. The legacy translation runs below in translateLegacyObservability
	// and may populate Log.Sink first.
	translateLegacyObservability(c)
	if len(c.Log.Sink) == 0 {
		c.Log.Sink = []LogSinkConfig{
			{
				Target: "stderr",
				Format: "auto",
				Level:  "info",
				Activities: ActivityFilterConfig{
					Deny: []string{"poll", "access"},
				},
			},
		}
	}
	// Apply per-sink defaults.
	for i := range c.Log.Sink {
		if c.Log.Sink[i].Format == "" {
			c.Log.Sink[i].Format = "auto"
		}
		if c.Log.Sink[i].Level == "" {
			c.Log.Sink[i].Level = "info"
		}
	}
	// Resolve relative file targets against data_dir, then bake in the
	// current working directory if data_dir is itself relative. This
	// freezes the log path at parse time so the same TOML works under
	// systemd (CWD=/), Docker, and a dev invocation that launches the
	// binary from a parent directory with --system-config
	// data/system.toml. The validator below still rejects any target
	// that is neither stderr/stdout nor absolute -- a path that stays
	// relative after this pass means data_dir was empty (Validate also
	// rejects that, with a clearer message) or filepath.Abs failed.
	if c.Server.DataDir != "" {
		for i := range c.Log.Sink {
			t := c.Log.Sink[i].Target
			if t == "" || t == "stderr" || t == "stdout" {
				continue
			}
			if filepath.IsAbs(t) {
				continue
			}
			joined := filepath.Join(c.Server.DataDir, t)
			if abs, err := filepath.Abs(joined); err == nil {
				c.Log.Sink[i].Target = abs
			} else {
				c.Log.Sink[i].Target = joined
			}
		}
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
	// UI session-cookie defaults: 24-hour session TTL, secure cookies.
	// CookieName / CSRFCookieName serve as the root from which listener-
	// specific names are derived (see adminSessionCookieConfig and
	// publicSessionCookieConfig in internal/admin/server.go).
	if c.Server.UI.CookieName == "" {
		c.Server.UI.CookieName = "herold_ui_session"
	}
	if c.Server.UI.CSRFCookieName == "" {
		c.Server.UI.CSRFCookieName = "herold_ui_csrf"
	}
	if c.Server.UI.SessionTTL == 0 {
		c.Server.UI.SessionTTL = Duration(7 * 24 * time.Hour)
	}
	// Admin-listener session TTL defaults (REQ-AUTH-72, issue #12).
	if c.Server.UI.AdminIdleTTL == 0 {
		c.Server.UI.AdminIdleTTL = Duration(1 * time.Hour)
	}
	if c.Server.UI.AdminAbsoluteTTL == 0 {
		c.Server.UI.AdminAbsoluteTTL = Duration(8 * time.Hour)
	}
	if c.Server.UI.SecureCookies == nil {
		t := true
		c.Server.UI.SecureCookies = &t
	}
	if c.Server.UI.ElevationTTL == 0 {
		c.Server.UI.ElevationTTL = Duration(15 * time.Minute)
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
	// External-image internalization defaults
	// (17-external-images.md REQ-EXTIMG-05/-20..27/-46). The
	// rewriter package consults these knobs at runtime so an empty
	// [external_images] block ships the documented defaults.
	if c.ExternalImages.Mode == "" {
		c.ExternalImages.Mode = ExternalImagesModeInternalize
	}
	el := &c.ExternalImages.Limits
	if el.MaxPerImageBytes == 0 {
		el.MaxPerImageBytes = 5 * 1024 * 1024
	}
	if el.MaxPerMessageImages == 0 {
		el.MaxPerMessageImages = 100
	}
	if el.MaxPerMessageBytes == 0 {
		el.MaxPerMessageBytes = 50 * 1024 * 1024
	}
	if el.PerImageConnectTimeout == 0 {
		el.PerImageConnectTimeout = Duration(5 * time.Second)
	}
	if el.PerImageTotalTimeout == 0 {
		el.PerImageTotalTimeout = Duration(30 * time.Second)
	}
	if el.PerMessageTimeout == 0 {
		el.PerMessageTimeout = Duration(60 * time.Second)
	}
	if el.ConcurrentFetches == 0 {
		el.ConcurrentFetches = 8
	}
	if el.RequireHTTPS == nil {
		t := true
		el.RequireHTTPS = &t
	}
	if el.FollowRedirectsMax == 0 {
		el.FollowRedirectsMax = 3
	}
	if c.ExternalImages.DKIM.OnModification == "" {
		c.ExternalImages.DKIM.OnModification = "strip"
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
	if c.Server.Chat.MessageTimestampGroupingSeconds == 0 {
		c.Server.Chat.MessageTimestampGroupingSeconds = 120
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
	// Suite SPA (REQ-DEPLOY-COLOC-01..05). Default-enabled so a
	// fresh install boots with the consumer suite mounted on the
	// public listener; admin-only deployments set Enabled=false.
	if c.Server.Suite.Enabled == nil {
		t := true
		c.Server.Suite.Enabled = &t
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
	// OAuth providers: normalise provider names to lowercase
	// (REQ-AUTH-EXT-SUBMIT-03). TOML map keys are case-sensitive; the
	// operator may write "Gmail" or "GMAIL" — we normalise so lookups are
	// always lowercase.
	if len(c.Server.OAuthProviders) > 0 {
		normalised := make(map[string]OAuthProviderConfig, len(c.Server.OAuthProviders))
		for k, v := range c.Server.OAuthProviders {
			normalised[strings.ToLower(k)] = v
		}
		c.Server.OAuthProviders = normalised
	}
	// Directory autocomplete default: "domain" (privacy-respecting).
	if c.Server.DirectoryAutocomplete.Mode == "" {
		c.Server.DirectoryAutocomplete.Mode = DirectoryAutocompleteModeDomain
	}
	// Identity creation (REQ-IDENT-10..36). Defaults are applied here so
	// a missing [server.identity_creation] block and an empty block behave
	// identically: enabled, allow-all external domains, 7-day purge,
	// 60s resend cooldown, 5/day cap.
	applyIdentityCreationDefaults(&c.Server.IdentityCreation, c.Server.Hostname)
	// Tagged addresses (REQ-TAG-*): default-enabled with the documented
	// per-principal caps. An absent section produces a fully-functional
	// configuration (enabled=true, caps=100/500); an empty section behaves
	// the same.
	if c.Server.TaggedAddresses.Enabled == nil {
		t := true
		c.Server.TaggedAddresses.Enabled = &t
	}
	if c.Server.TaggedAddresses.MaxFiltersPerPrincipal == 0 {
		c.Server.TaggedAddresses.MaxFiltersPerPrincipal = 100
	}
	if c.Server.TaggedAddresses.MaxDismissalsPerPrincipal == 0 {
		c.Server.TaggedAddresses.MaxDismissalsPerPrincipal = 500
	}
	// Trash retention sweeper (REQ-STORE-90). Defaults mirror the
	// trashretention package constants so a missing block and an empty
	// block behave the same: 30-day retention, 1-hour sweep interval.
	if c.Server.TrashRetention.RetentionDays == 0 {
		c.Server.TrashRetention.RetentionDays = 30
	}
	if c.Server.TrashRetention.SweepIntervalSeconds == 0 {
		c.Server.TrashRetention.SweepIntervalSeconds = 3600
	}
	// Client-log ingest (REQ-OPS-219). Defaults match the table in
	// REQ-OPS-216. A missing [clientlog] block produces a fully
	// functional configuration with the documented default values.
	applyClientLogDefaults(&c.ClientLog)
	// Attachment shares (REQ-SHARE-30..52). Defaults match the
	// configuration knobs in 25-attachment-shares.md. A missing section
	// produces a fully defaulted (but inert) configuration; the feature
	// only becomes active when public_base_url is also set.
	applyAttachmentSharesDefaults(&c.Server.AttachmentShares)
	// IMAP import (REQ-IMAP-IMP-05, -23, -26). A missing [imap_import]
	// block produces a fully defaulted configuration: allow_password=true,
	// concurrent_accounts=16, poll_interval=60s.
	applyIMAPImportDefaults(&c.IMAPImport)
	// Translation proxy (re #84). When enabled without an explicit provider,
	// default to "mymemory" so Validate can check provider-specific rules.
	if c.Translation.Enabled && c.Translation.Provider == "" {
		c.Translation.Provider = "mymemory"
	}
}

// applyClientLogDefaults fills in the documented default values for
// the [clientlog] block (REQ-OPS-219) when fields are absent.
func applyClientLogDefaults(cl *ClientLogConfig) {
	if cl.Enabled == nil {
		t := true
		cl.Enabled = &t
	}
	if cl.ReorderWindowMS == 0 {
		cl.ReorderWindowMS = 1000
	}
	if cl.LivetailDefaultDuration == 0 {
		cl.LivetailDefaultDuration = Duration(15 * time.Minute)
	}
	if cl.LivetailMaxDuration == 0 {
		cl.LivetailMaxDuration = Duration(60 * time.Minute)
	}
	if cl.Defaults.TelemetryEnabled == nil {
		t := true
		cl.Defaults.TelemetryEnabled = &t
	}
	// Auth endpoint defaults (REQ-OPS-216).
	if cl.Auth.RingBufferRows == 0 {
		cl.Auth.RingBufferRows = 100000
	}
	if cl.Auth.RingBufferAge == 0 {
		cl.Auth.RingBufferAge = Duration(168 * time.Hour) // 7 days
	}
	if cl.Auth.RatePerSession.IsZero() {
		_ = cl.Auth.RatePerSession.UnmarshalText([]byte("1000/5m"))
	}
	if cl.Auth.BodyMaxBytes == 0 {
		cl.Auth.BodyMaxBytes = 262144 // 256 KiB
	}
	// Public endpoint defaults (REQ-OPS-216).
	if cl.Public.Enabled == nil {
		t := true
		cl.Public.Enabled = &t
	}
	if cl.Public.RingBufferRows == 0 {
		cl.Public.RingBufferRows = 10000
	}
	if cl.Public.RingBufferAge == 0 {
		cl.Public.RingBufferAge = Duration(24 * time.Hour)
	}
	if cl.Public.RatePerIP.IsZero() {
		_ = cl.Public.RatePerIP.UnmarshalText([]byte("10/m"))
	}
	if cl.Public.BodyMaxBytes == 0 {
		cl.Public.BodyMaxBytes = 8192 // 8 KiB
	}
}

// applyAttachmentSharesDefaults populates the documented default values for
// the [server.attachment_shares] block (REQ-SHARE-30..52) when fields are
// absent. The feature defaults to enabled but is inert until public_base_url
// is also set.
func applyAttachmentSharesDefaults(as *AttachmentSharesConfig) {
	if as.Enabled == nil {
		t := true
		as.Enabled = &t
	}
	if as.DefaultTTL == 0 {
		as.DefaultTTL = DurationExtended(48 * time.Hour) // 48h
	}
	if as.MaxTTL == 0 {
		as.MaxTTL = DurationExtended(90 * 24 * time.Hour) // 90d
	}
	if as.PendingTTL == 0 {
		// Match DefaultTTL so a freshly-offloaded (still pending) share
		// shows the same 48h lifetime it will keep once the message is
		// sent, rather than a misleading short window.
		as.PendingTTL = DurationExtended(48 * time.Hour) // 48h
	}
	if as.RevokedGrace == 0 {
		as.RevokedGrace = DurationExtended(24 * time.Hour) // 24h
	}
	if as.MaxSharesPerPrincipal == 0 {
		as.MaxSharesPerPrincipal = 1000
	}
	if as.ShareQuotaPerPrincipal == 0 {
		as.ShareQuotaPerPrincipal = ByteSize(5 * 1024 * 1024 * 1024) // 5 GiB
	}
	if as.DownloadRequestsPerIPPerShare == 0 {
		as.DownloadRequestsPerIPPerShare = 50
	}
	if as.DownloadRequestsWindow == 0 {
		as.DownloadRequestsWindow = Duration(10 * time.Minute) // 10m
	}
}

// applyIMAPImportDefaults fills in the documented default values for the
// [imap_import] block (REQ-IMAP-IMP-05, -23, -26) when fields are absent.
func applyIMAPImportDefaults(ii *IMAPImportConfig) {
	// AllowPassword defaults to true (REQ-IMAP-IMP-05). Pointer so an absent
	// key is distinguishable from an explicit `allow_password = false`; same
	// pattern as TaggedAddressesConfig.Enabled and CallConfig.Enabled.
	if ii.AllowPassword == nil {
		t := true
		ii.AllowPassword = &t
	}
	if ii.ConcurrentAccounts == 0 {
		ii.ConcurrentAccounts = 16
	}
	if ii.PollInterval == 0 {
		ii.PollInterval = Duration(60 * time.Second)
	}
	// Normalise OAuth provider names to lowercase (same convention as
	// [server.oauth_providers]).
	if len(ii.OAuth) > 0 {
		normalised := make(map[string]IMAPImportOAuthProvider, len(ii.OAuth))
		for k, v := range ii.OAuth {
			normalised[strings.ToLower(k)] = v
		}
		ii.OAuth = normalised
	}
}

// applyIdentityCreationDefaults populates the documented default values for
// the [server.identity_creation] block (REQ-IDENT-10..36) when fields are
// absent. Called once after parse; idempotent. hostname is accepted for
// signature compatibility but is no longer used to build a VerifierFrom
// default — that default is now resolved at dispatch time from the hosted
// domain list (REQ-IDENT-30): when exactly one local domain is registered,
// the dispatcher synthesises "postmaster@<domain>"; when multiple local
// domains exist the operator must set verifier_from explicitly.
func applyIdentityCreationDefaults(ic *IdentityCreationConfig, hostname string) {
	_ = hostname // reserved for future use; default is resolved at dispatch time
	if ic.Enabled == nil {
		t := true
		ic.Enabled = &t
	}
	if ic.ExternalDomains == "" {
		ic.ExternalDomains = IdentityCreationExternalDomainsAllowAll
	}
	if ic.UnverifiedPurgeAfter == "" {
		ic.UnverifiedPurgeAfter = "7d"
	}
	if ic.ResendCooldownSeconds == 0 {
		ic.ResendCooldownSeconds = 60
	}
	if ic.ResendDailyCap == 0 {
		ic.ResendDailyCap = 5
	}
	if ic.GCInterval == "" {
		ic.GCInterval = "6h"
	}
}

// parseIdentityCreationDuration parses the unverified_purge_after duration
// value (REQ-IDENT-35). Accepts standard Go durations (time.ParseDuration)
// plus a single trailing "d" suffix as a 24-hour multiplier ("7d" == 168h),
// since operators write retention windows in days and time.ParseDuration
// does not natively understand "d". Returns an error for unparseable input
// or non-positive durations.
func parseIdentityCreationDuration(s string) (time.Duration, error) {
	v := strings.TrimSpace(s)
	if v == "" {
		return 0, errors.New("empty duration")
	}
	if strings.HasSuffix(v, "d") && !strings.ContainsAny(v[:len(v)-1], "hms") {
		days, err := time.ParseDuration(v[:len(v)-1] + "h")
		if err != nil {
			return 0, err
		}
		return days * 24, nil
	}
	return time.ParseDuration(v)
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

// validateLogSinks checks the [[log.sink]] array (REQ-OPS-80..86b).
// Called from Validate after applyDefaults has run, so each sink has
// non-empty Format and Level.
func validateLogSinks(sinks []LogSinkConfig) error {
	fileSeen := make(map[string]struct{})
	for i, s := range sinks {
		label := fmt.Sprintf("[[log.sink]] #%d (target=%q)", i, s.Target)
		// Target validation: must be "stderr", "stdout", or an absolute path.
		// Relative paths and "/dev/null" are rejected (REQ-OPS-81).
		if s.Target == "" {
			return fmt.Errorf("sysconfig: %s: target is required", label)
		}
		if s.Target != "stderr" && s.Target != "stdout" {
			// applyDefaults resolved relative paths against data_dir
			// and then to absolute via filepath.Abs; a relative target
			// reaching the validator means data_dir was empty (caught
			// separately) or filepath.Abs failed.
			if !filepath.IsAbs(s.Target) {
				return fmt.Errorf("sysconfig: %s: target must be \"stderr\", \"stdout\", or a filesystem path (relative paths resolve against [server] data_dir)", label)
			}
			if s.Target == "/dev/null" {
				return fmt.Errorf("sysconfig: %s: target \"/dev/null\" is not permitted", label)
			}
			// Duplicate file targets (REQ-OPS-80): one sink per path.
			if _, dup := fileSeen[s.Target]; dup {
				return fmt.Errorf("sysconfig: %s: duplicate file target %q (one sink per path)", label, s.Target)
			}
			fileSeen[s.Target] = struct{}{}
		}
		// Format.
		if _, ok := validSinkFormats[s.Format]; !ok {
			return fmt.Errorf("sysconfig: %s: format %q not recognised (want \"json\", \"console\", or \"auto\")", label, s.Format)
		}
		// Level.
		if _, ok := validLogLevels[s.Level]; !ok {
			return fmt.Errorf("sysconfig: %s: level %q not recognised", label, s.Level)
		}
		// Per-module level overrides.
		for mod, lvl := range s.Modules {
			if !isValidModuleIdent(mod) {
				return fmt.Errorf("sysconfig: %s: modules key %q must be a non-empty lowercase ASCII identifier", label, mod)
			}
			if _, ok := validLogLevels[lvl]; !ok {
				return fmt.Errorf("sysconfig: %s: modules[%q] level %q not recognised", label, mod, lvl)
			}
		}
		// Activities filter (REQ-OPS-86b).
		act := s.Activities
		if len(act.Allow) > 0 && len(act.Deny) > 0 {
			return fmt.Errorf("sysconfig: %s: activities.allow and activities.deny are mutually exclusive", label)
		}
		for _, a := range act.Allow {
			if _, ok := validActivities[a]; !ok {
				return fmt.Errorf("sysconfig: %s: activities.allow value %q not in enum {user,audit,system,poll,access,internal}", label, a)
			}
		}
		for _, a := range act.Deny {
			if _, ok := validActivities[a]; !ok {
				return fmt.Errorf("sysconfig: %s: activities.deny value %q not in enum {user,audit,system,poll,access,internal}", label, a)
			}
		}
	}
	return nil
}

// translateLegacyObservability checks whether the operator used the old
// [observability] log_format / log_level / log_modules fields. If so — and if
// no [[log.sink]] entries were explicitly configured — it synthesises a single
// [[log.sink]] entry from those fields and records a deprecation warning via
// slog (printed at runtime, so it appears after the logger is bootstrapped).
//
// This shim is the single place where legacy-to-new translation lives;
// no other code path scatters legacy logic.
func translateLegacyObservability(c *Config) {
	obs := &c.Observability
	hasLegacyLog := obs.LogFormat != "" && obs.LogFormat != "json" ||
		obs.LogLevel != "" && obs.LogLevel != "info" ||
		len(obs.LogModules) > 0
	if !hasLegacyLog {
		return
	}
	if len(c.Log.Sink) > 0 {
		// Operator mixed legacy and new forms. We ignore the legacy fields;
		// the validator will later reject if needed.
		return
	}
	// Build a synthetic sink. Map the old "text" format to "console".
	format := obs.LogFormat
	switch format {
	case "text":
		format = "console"
	case "":
		format = "auto"
	}
	level := obs.LogLevel
	if level == "" {
		level = "info"
	}
	// Clone modules map to avoid aliasing.
	var modules map[string]string
	if len(obs.LogModules) > 0 {
		modules = make(map[string]string, len(obs.LogModules))
		for k, v := range obs.LogModules {
			modules[k] = v
		}
	}
	c.Log.Sink = []LogSinkConfig{
		{
			Target:  "stderr",
			Format:  format,
			Level:   level,
			Modules: modules,
		},
	}
	// Emit a deprecation warning. At the time applyDefaults runs the
	// configured logger may not be up yet; slog.Default() falls back to the
	// stdlib default JSON logger which is fine — the message will still reach
	// the operator via stderr.
	slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
		"sysconfig: [observability] log_format/log_level/log_modules are deprecated; migrate to [[log.sink]] (REQ-OPS-80)",
		slog.String("action", "synthesised single [[log.sink]] from legacy fields"),
	)
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
	// Admin-listener session TTLs (REQ-AUTH-72, issue #12). Both knobs
	// have a 12-hour hard ceiling; the idle TTL must not exceed the
	// absolute TTL or the idle gate could never trip before the
	// absolute deadline arrives. Both fields have been defaulted by
	// applyDefaults at this point, so a zero here would be a coding
	// bug rather than operator config.
	const adminSessionTTLCeiling = 12 * time.Hour
	if dur := c.Server.UI.AdminIdleTTL.AsDuration(); dur > adminSessionTTLCeiling {
		return fmt.Errorf("sysconfig: [server.ui] admin_idle_ttl %s exceeds the 12h ceiling", dur)
	}
	if dur := c.Server.UI.AdminAbsoluteTTL.AsDuration(); dur > adminSessionTTLCeiling {
		return fmt.Errorf("sysconfig: [server.ui] admin_absolute_ttl %s exceeds the 12h ceiling", dur)
	}
	if idle, abs := c.Server.UI.AdminIdleTTL.AsDuration(), c.Server.UI.AdminAbsoluteTTL.AsDuration(); idle > abs {
		return fmt.Errorf("sysconfig: [server.ui] admin_idle_ttl %s exceeds admin_absolute_ttl %s", idle, abs)
	}
	// Elevation TTL (REQ-AUTH-74, issue #79). Bounded to [1m, 1h] to
	// prevent operator misconfiguration that would either lock admins out
	// (< 1 min gives no window) or leave the admin surface open
	// indefinitely (> 1 h weakens the step-up protection).
	if dur := c.Server.UI.ElevationTTL.AsDuration(); dur < time.Minute || dur > time.Hour {
		return fmt.Errorf("sysconfig: [server.ui] elevation_ttl %s must be between 1m and 1h", dur)
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
	// External-image internalization (17-external-images.md
	// REQ-EXTIMG-01/-21..27/-33/-41/-47).
	ei := &c.ExternalImages
	switch ei.Mode {
	case "", ExternalImagesModeInternalize, ExternalImagesModePassthrough:
		// ok
	default:
		return fmt.Errorf("sysconfig: [external_images] mode %q must be %q or %q",
			ei.Mode, ExternalImagesModeInternalize, ExternalImagesModePassthrough)
	}
	if ei.Limits.MaxPerImageBytes < 0 ||
		ei.Limits.MaxPerMessageImages < 0 ||
		ei.Limits.MaxPerMessageBytes < 0 ||
		ei.Limits.ConcurrentFetches < 0 ||
		ei.Limits.FollowRedirectsMax < 0 {
		return errors.New("sysconfig: [external_images.limits] knobs must be >= 0")
	}
	if ei.Limits.MaxPerMessageBytes > 0 && ei.Limits.MaxPerImageBytes > 0 &&
		ei.Limits.MaxPerImageBytes > ei.Limits.MaxPerMessageBytes {
		return fmt.Errorf("sysconfig: [external_images.limits] max_per_image_bytes %d exceeds max_per_message_bytes %d",
			ei.Limits.MaxPerImageBytes, ei.Limits.MaxPerMessageBytes)
	}
	for _, raw := range ei.Network.DenyCIDRs {
		if _, _, err := net.ParseCIDR(raw); err != nil {
			return fmt.Errorf("sysconfig: [external_images.network] deny_cidrs %q: %w", raw, err)
		}
	}
	switch ei.DKIM.OnModification {
	case "", "strip", "keep":
		// ok
	default:
		return fmt.Errorf("sysconfig: [external_images.dkim] on_modification %q must be \"strip\" or \"keep\"",
			ei.DKIM.OnModification)
	}
	// REQ-EXTIMG-47: ARC sealing reserved; refuse premature use so an
	// operator who flips the knob does not get silent no-op.
	if ei.DKIM.ARCSeal {
		return errors.New("sysconfig: [external_images.dkim] arc_seal is reserved for a future iteration; must be false")
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
	// Trash retention sweeper (REQ-STORE-90). retention_days must be in
	// [1, 3650]; sweep_interval_seconds in [60, 86400].
	tr := c.Server.TrashRetention
	if tr.RetentionDays < 1 {
		return fmt.Errorf("sysconfig: [server.trash_retention] retention_days %d must be >= 1",
			tr.RetentionDays)
	}
	if tr.RetentionDays > 3650 {
		return fmt.Errorf("sysconfig: [server.trash_retention] retention_days %d exceeds 3650 (10-year) ceiling",
			tr.RetentionDays)
	}
	if tr.SweepIntervalSeconds < 60 {
		return fmt.Errorf("sysconfig: [server.trash_retention] sweep_interval_seconds %d below 60s floor",
			tr.SweepIntervalSeconds)
	}
	if tr.SweepIntervalSeconds > 86400 {
		return fmt.Errorf("sysconfig: [server.trash_retention] sweep_interval_seconds %d exceeds 1d ceiling",
			tr.SweepIntervalSeconds)
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
	// Dispatcher knobs (Wave 3.8b). Negative or zero values mean
	// "use default" at dispatcher construction; only out-of-range
	// positives are rejected here.
	if push.DispatcherPollIntervalSeconds < 0 {
		return fmt.Errorf("sysconfig: [server.push] dispatcher_poll_interval_seconds %d must be >= 0",
			push.DispatcherPollIntervalSeconds)
	}
	if push.DispatcherPollIntervalSeconds > 3600 {
		return fmt.Errorf("sysconfig: [server.push] dispatcher_poll_interval_seconds %d exceeds 1h ceiling",
			push.DispatcherPollIntervalSeconds)
	}
	if push.HTTPTimeoutSeconds < 0 || push.HTTPTimeoutSeconds > 600 {
		return fmt.Errorf("sysconfig: [server.push] http_timeout_seconds %d out of range (0..600)",
			push.HTTPTimeoutSeconds)
	}
	if push.JWTExpirySeconds < 0 || push.JWTExpirySeconds > 86400 {
		return fmt.Errorf("sysconfig: [server.push] jwt_expiry_seconds %d out of range (0..86400)",
			push.JWTExpirySeconds)
	}
	if push.RateLimitPerMinute < 0 || push.RateLimitPerMinute > 10000 {
		return fmt.Errorf("sysconfig: [server.push] rate_limit_per_minute %d out of range (0..10000)",
			push.RateLimitPerMinute)
	}
	if push.RateLimitPerDay < 0 || push.RateLimitPerDay > 1_000_000 {
		return fmt.Errorf("sysconfig: [server.push] rate_limit_per_day %d out of range (0..1000000)",
			push.RateLimitPerDay)
	}
	if push.CooldownSeconds < 0 || push.CooldownSeconds > 86400 {
		return fmt.Errorf("sysconfig: [server.push] cooldown_seconds %d out of range (0..86400)",
			push.CooldownSeconds)
	}
	if push.CoalesceWindowSeconds < 0 || push.CoalesceWindowSeconds > 300 {
		return fmt.Errorf("sysconfig: [server.push] coalesce_window_seconds %d out of range (0..300)",
			push.CoalesceWindowSeconds)
	}
	// Suite SPA (REQ-DEPLOY-COLOC-01..05). The asset_dir override is
	// validated at parse time so a missing path fails the load rather
	// than at first 404; the actual content (index.html presence) is
	// re-checked by webspa.New at server boot for the embedded path
	// too. Relative paths are accepted and resolved against the current
	// working directory at server start, matching the convention used
	// by data_dir, cert_file, and the SQLite path -- the quickstart
	// system.toml may set asset_dir to a developer-built dist tree
	// (e.g. web/apps/suite/dist) for hot-reload during frontend
	// development.
	if dir := c.Server.Suite.AssetDir; dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("sysconfig: [server.suite] asset_dir %q: %v", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("sysconfig: [server.suite] asset_dir %q is not a directory", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			return fmt.Errorf("sysconfig: [server.suite] asset_dir %q missing index.html: %v", dir, err)
		}
	}
	if dir := c.Server.AdminSPA.AssetDir; dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("sysconfig: [server.admin_spa] asset_dir %q: %v", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("sysconfig: [server.admin_spa] asset_dir %q is not a directory", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			return fmt.Errorf("sysconfig: [server.admin_spa] asset_dir %q missing index.html: %v", dir, err)
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
	// Outbound queue concurrency (REQ-OPS). Negative is always a typo;
	// cap Concurrency at 1024 so a misconfigured value cannot exhaust
	// file descriptors or OOM the box.
	const queueConcurrencyMax = 1024
	qc := c.Server.Queue
	if qc.Concurrency < 0 {
		return fmt.Errorf("sysconfig: [server.queue] concurrency %d must be >= 0", qc.Concurrency)
	}
	if qc.Concurrency > queueConcurrencyMax {
		return fmt.Errorf("sysconfig: [server.queue] concurrency %d exceeds %d ceiling", qc.Concurrency, queueConcurrencyMax)
	}
	if qc.PerHostMax < 0 {
		return fmt.Errorf("sysconfig: [server.queue] per_host_max %d must be >= 0", qc.PerHostMax)
	}
	if qc.Concurrency > 0 && qc.PerHostMax > qc.Concurrency {
		return fmt.Errorf("sysconfig: [server.queue] per_host_max %d exceeds concurrency %d", qc.PerHostMax, qc.Concurrency)
	}
	// Admin TLS
	switch c.Server.AdminTLS.Source {
	case "":
		return errors.New("sysconfig: [server.admin_tls] source is required (use \"none\", \"file\", or \"acme\")")
	case "none":
		// All listeners run plaintext. No cert needed. Valid for the loopback
		// quickstart where tls = "none" on every listener (ADR-0001 section 2).
	case "file":
		if c.Server.AdminTLS.CertFile == "" || c.Server.AdminTLS.KeyFile == "" {
			return errors.New("sysconfig: [server.admin_tls] source=\"file\" requires cert_file and key_file")
		}
	case "acme":
		if c.Acme == nil {
			return errors.New("sysconfig: [server.admin_tls] source=\"acme\" requires an [acme] block")
		}
	default:
		return fmt.Errorf("sysconfig: [server.admin_tls] source %q not recognised (want \"none\", \"file\", or \"acme\")", c.Server.AdminTLS.Source)
	}
	// [acme] block validation.
	if c.Acme != nil {
		if c.Acme.Email == "" {
			return errors.New("sysconfig: [acme] email is required")
		}
		switch c.Acme.ChallengeType {
		case "", "http-01", "tls-alpn-01":
			// ok
		case "dns-01":
			if c.Acme.DNSPlugin == "" {
				return errors.New("sysconfig: [acme] challenge_type=\"dns-01\" requires dns_plugin")
			}
		default:
			return fmt.Errorf("sysconfig: [acme] challenge_type %q not recognised (want \"http-01\", \"tls-alpn-01\", or \"dns-01\")", c.Acme.ChallengeType)
		}
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
		// port 0 asks the kernel to pick a free port; port_report_file
		// is required in that case so callers can discover the actual port.
		if _, portStr, splitErr := net.SplitHostPort(l.Address); splitErr == nil && portStr == "0" {
			if c.Server.PortReportFile == "" {
				return fmt.Errorf("sysconfig: listener %q uses port 0 but [server].port_report_file is unset", l.Name)
			}
		}
		if _, ok := validProtocols[l.Protocol]; !ok {
			if l.Protocol == "admin" {
				return fmt.Errorf("sysconfig: [[listener]] %q: protocol \"admin\" was renamed to \"http\" for HTTP listeners; update the [[listener]] block", l.Name)
			}
			if l.Protocol == "imaps" {
				return fmt.Errorf("sysconfig: [[listener]] %q: protocol \"imaps\" was removed; use protocol = \"imap\" with tls = \"implicit\"", l.Name)
			}
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
		// proxy_protocol is supported on HTTP, SMTP, IMAP, and
		// ManageSieve listeners (issues #106, #111). The listener
		// wrapper makes conn.RemoteAddr() return the decoded client
		// address transparently, so no protocol-specific change is
		// needed. Any other protocol value is rejected.
		if l.ProxyProtocol {
			switch l.Protocol {
			case "http", "smtp", "smtp-submission", "imap", "managesieve":
				// allowed
			default:
				return fmt.Errorf("sysconfig: [[listener]] %q: proxy_protocol is not supported on protocol %q", l.Name, l.Protocol)
			}
		}
		// allow_plain_auth opts a plaintext mail listener into accepting
		// SASL PLAIN / LOGIN over cleartext. Only meaningful on imap,
		// smtp-submission, and managesieve listeners with tls = "none"
		// (issue #114). Any other combination is operator error and is
		// rejected.
		if l.AllowPlainAuth {
			switch l.Protocol {
			case "imap", "smtp-submission", "managesieve":
				// allowed
			default:
				return fmt.Errorf("sysconfig: [[listener]] %q: allow_plain_auth is not supported on protocol %q", l.Name, l.Protocol)
			}
			if l.TLS != "none" {
				return fmt.Errorf("sysconfig: [[listener]] %q: allow_plain_auth requires tls = \"none\"", l.Name)
			}
			// The bind address is intentionally not constrained. allow_plain_auth
			// is an explicit operator opt-in for a trusted-network posture, and
			// the container quickstart binds 0.0.0.0 inside the container by
			// necessity (Docker's -p 127.0.0.1:host:container port-forward only
			// reaches a process listening on the container's wildcard address);
			// the loopback boundary there lives at the host-side port mapping,
			// which herold cannot observe. Restricting the bind would reject that
			// supported deployment, so the operator owns the network exposure.
		}
		// REQ-OPS-ADMIN-LISTENER-01: HTTP listeners (Protocol=="http")
		// carry a Kind in {public, admin}. Non-HTTP listeners must
		// leave Kind empty.
		if l.Protocol == "http" {
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
	// re #58: the admin SPA and REST surface are now on the public listener,
	// gated by ScopeAdmin. A separate kind="admin" listener is no longer
	// required; operators should remove it from their configs. During the
	// transition period kind="admin" is accepted and aliased to the public
	// handler. DevMode still uses the no-kind co-mount path (sawPublic /
	// sawAdmin are only inspected for diagnostics below, not for hard errors).
	_ = sawPublic
	_ = sawAdmin
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
	// Observability legacy fields (validated only when present; the
	// new [[log.sink]] model is validated below).
	if _, ok := validLogFormats[c.Observability.LogFormat]; !ok {
		return fmt.Errorf("sysconfig: [observability] log_format %q not recognised", c.Observability.LogFormat)
	}
	if _, ok := validLogLevels[c.Observability.LogLevel]; !ok {
		return fmt.Errorf("sysconfig: [observability] log_level %q not recognised", c.Observability.LogLevel)
	}
	for mod, lvl := range c.Observability.LogModules {
		if !isValidModuleIdent(mod) {
			return fmt.Errorf("sysconfig: [observability] log_modules key %q must be a non-empty lowercase ASCII identifier", mod)
		}
		if _, ok := validLogLevels[lvl]; !ok {
			return fmt.Errorf("sysconfig: [observability] log_modules[%q] level %q not recognised", mod, lvl)
		}
	}
	// [[log.sink]] validation (REQ-OPS-80..86b).
	if err := validateLogSinks(c.Log.Sink); err != nil {
		return err
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
		sc := c.Server.Storage.SQLite
		const cacheSizeLimit = 1 << 20
		if sc.CacheSize < -cacheSizeLimit || sc.CacheSize > cacheSizeLimit {
			return fmt.Errorf("sysconfig: [server.storage.sqlite] cache_size %d out of range [-%d, %d]",
				sc.CacheSize, cacheSizeLimit, cacheSizeLimit)
		}
		const walACPLimit = 1 << 20
		if sc.WALAutocheckpoint < 0 || sc.WALAutocheckpoint > walACPLimit {
			return fmt.Errorf("sysconfig: [server.storage.sqlite] wal_autocheckpoint %d out of range [0, %d]",
				sc.WALAutocheckpoint, walACPLimit)
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
	// SES inbound (REQ-HOOK-SES-03).
	if err := validateSESInbound(&c.Hooks.SESInbound); err != nil {
		return err
	}
	// External SMTP submission (REQ-AUTH-EXT-SUBMIT-01..10).
	// When enabled, a data key must be configured; the server refuses to
	// start without one (boot-time hard-fail, architectural decision 4).
	if c.Server.ExternalSubmission.Enabled && c.Server.Secrets.DataKeyRef == "" {
		return errors.New("sysconfig: [server.external_submission] enabled requires [server.secrets].data_key_ref; " +
			"set [server.secrets].data_key_ref to enable [server.external_submission]")
	}
	if c.Server.Secrets.DataKeyRef != "" && !IsSecretReference(c.Server.Secrets.DataKeyRef) {
		return fmt.Errorf("sysconfig: [server.secrets] data_key_ref %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)",
			c.Server.Secrets.DataKeyRef)
	}
	// OAuth providers (REQ-AUTH-EXT-SUBMIT-03).
	if err := validateOAuthProviders(c); err != nil {
		return err
	}
	// Directory autocomplete mode.
	if _, ok := validDirectoryAutocompleteModes[c.Server.DirectoryAutocomplete.Mode]; !ok {
		return fmt.Errorf("sysconfig: [server.directory_autocomplete] mode %q not recognised (want \"all\", \"domain\", or \"off\")",
			c.Server.DirectoryAutocomplete.Mode)
	}
	// Identity creation (REQ-IDENT-10..36). Verifier_from is checked for
	// shape only here; the "must be locally hosted" check (REQ-IDENT-30) is
	// deferred to runtime first-use because ListLocalDomains lives in the
	// store and is not available during config parse.
	if err := validateIdentityCreation(&c.Server.IdentityCreation); err != nil {
		return err
	}
	// Tagged-address caps (REQ-TAG-11). Both caps must be positive even
	// when enabled=false so a future flip to enabled=true does not surface
	// a silent zero-cap configuration; the feature being off does not
	// excuse a malformed cap.
	if c.Server.TaggedAddresses.MaxFiltersPerPrincipal <= 0 {
		return fmt.Errorf("sysconfig: [server.tagged_addresses] max_filters_per_principal %d must be > 0",
			c.Server.TaggedAddresses.MaxFiltersPerPrincipal)
	}
	if c.Server.TaggedAddresses.MaxDismissalsPerPrincipal <= 0 {
		return fmt.Errorf("sysconfig: [server.tagged_addresses] max_dismissals_per_principal %d must be > 0",
			c.Server.TaggedAddresses.MaxDismissalsPerPrincipal)
	}
	// Client-log ingest (REQ-OPS-219).
	if err := validateClientLog(&c.ClientLog); err != nil {
		return err
	}
	// Attachment shares (REQ-SHARE-30..52).
	if err := validateAttachmentShares(&c.Server.AttachmentShares); err != nil {
		return err
	}
	// IMAP import (REQ-IMAP-IMP-03, -05, -23, -26).
	if err := validateIMAPImport(&c.IMAPImport); err != nil {
		return err
	}
	// Translation proxy (re #84).
	if err := validateTranslation(&c.Translation); err != nil {
		return err
	}
	return nil
}

// validateTranslation checks semantic constraints on the [translation]
// block (re #84). applyDefaults has already set the default provider so
// Validate can check provider-specific rules unconditionally.
func validateTranslation(tc *TranslationConfig) error {
	// Validate the api_key_ref format even when the feature is disabled, so
	// a typo in the reference string is caught at config-load time rather
	// than at first request.
	if tc.APIKeyRef != "" && !IsSecretReference(tc.APIKeyRef) {
		return fmt.Errorf("sysconfig: [translation] api_key_ref %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)",
			tc.APIKeyRef)
	}
	if !tc.Enabled {
		return nil
	}
	switch tc.Provider {
	case "mymemory", "deepl":
		// ok
	default:
		return fmt.Errorf("sysconfig: [translation] provider %q not recognised (want \"mymemory\" or \"deepl\")",
			tc.Provider)
	}
	if tc.Endpoint != "" {
		u, err := url.ParseRequestURI(tc.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("sysconfig: [translation] endpoint %q must be an absolute https URL",
				tc.Endpoint)
		}
	}
	if tc.Provider == "deepl" && tc.APIKeyRef == "" {
		return errors.New("sysconfig: [translation] provider \"deepl\" requires api_key_ref (STANDARDS §9)")
	}
	return nil
}

// validateIdentityCreation checks semantic constraints on the
// [server.identity_creation] block (REQ-IDENT-10..36). applyDefaults has
// already run, so every knob has a non-zero value when the operator
// omitted it.
//
// The "verifier_from MUST be a locally-hosted address" check
// (REQ-IDENT-30) is deferred to a runtime sanity-check at first use:
// ListLocalDomains lives in the store and is not available during config
// parse, and operators legitimately bring up the server before the
// domain list has been seeded. The runtime check logs a startup warning
// and refuses to advertise the identity-verification capability if the
// verifier_from domain is not hosted.
func validateIdentityCreation(ic *IdentityCreationConfig) error {
	// VerifierFrom shape: must contain exactly one "@" and a non-empty
	// localpart and domain. The locally-hosted-domain check happens at
	// runtime.
	if ic.VerifierFrom != "" {
		at := strings.IndexByte(ic.VerifierFrom, '@')
		if at <= 0 || at == len(ic.VerifierFrom)-1 || strings.IndexByte(ic.VerifierFrom[at+1:], '@') >= 0 {
			return fmt.Errorf("sysconfig: [server.identity_creation] verifier_from %q is not a valid email address",
				ic.VerifierFrom)
		}
	}
	// External domain policy enum.
	if _, ok := validIdentityCreationExternalDomainsModes[ic.ExternalDomains]; !ok {
		return fmt.Errorf("sysconfig: [server.identity_creation] external_domains %q not recognised (want \"allow_all\", \"allowlist\", or \"deny_all\")",
			ic.ExternalDomains)
	}
	// Allowlist mode requires at least one entry; entries must be
	// lowercase ASCII bare domain names.
	if ic.ExternalDomains == IdentityCreationExternalDomainsAllowlist {
		if len(ic.ExternalDomainAllowlist) == 0 {
			return errors.New("sysconfig: [server.identity_creation] external_domains=\"allowlist\" requires external_domain_allowlist to be non-empty")
		}
		for _, d := range ic.ExternalDomainAllowlist {
			if d == "" {
				return errors.New("sysconfig: [server.identity_creation] external_domain_allowlist contains an empty entry")
			}
			if d != strings.ToLower(d) {
				return fmt.Errorf("sysconfig: [server.identity_creation] external_domain_allowlist entry %q must be lowercase ASCII",
					d)
			}
			if strings.ContainsAny(d, " \t\r\n@/") {
				return fmt.Errorf("sysconfig: [server.identity_creation] external_domain_allowlist entry %q is not a bare domain name",
					d)
			}
		}
	}
	// Unverified-purge duration: required to parse as a strictly positive
	// duration. Accept the "d"-suffix shorthand.
	dur, err := parseIdentityCreationDuration(ic.UnverifiedPurgeAfter)
	if err != nil {
		return fmt.Errorf("sysconfig: [server.identity_creation] unverified_purge_after %q: %v",
			ic.UnverifiedPurgeAfter, err)
	}
	if dur <= 0 {
		return fmt.Errorf("sysconfig: [server.identity_creation] unverified_purge_after %q must be a positive duration",
			ic.UnverifiedPurgeAfter)
	}
	// Resend rate-limit knobs must be strictly positive: a zero cooldown
	// or daily cap collapses the limiter and would let an unverified
	// Identity flood the outbound queue.
	if ic.ResendCooldownSeconds <= 0 {
		return fmt.Errorf("sysconfig: [server.identity_creation] resend_cooldown_seconds %d must be > 0",
			ic.ResendCooldownSeconds)
	}
	if ic.ResendDailyCap <= 0 {
		return fmt.Errorf("sysconfig: [server.identity_creation] resend_daily_cap %d must be > 0",
			ic.ResendDailyCap)
	}
	// GC interval: optional but must parse as a positive duration when
	// non-empty (defaultsApplied substitutes "6h" so a parsed value is
	// guaranteed here).
	if ic.GCInterval != "" {
		gd, err := time.ParseDuration(ic.GCInterval)
		if err != nil {
			return fmt.Errorf("sysconfig: [server.identity_creation] gc_interval %q: %v",
				ic.GCInterval, err)
		}
		if gd <= 0 {
			return fmt.Errorf("sysconfig: [server.identity_creation] gc_interval %q must be a positive duration",
				ic.GCInterval)
		}
	}
	return nil
}

// UnverifiedPurgeAfterDuration returns the parsed UnverifiedPurgeAfter
// value as a time.Duration. Callers MUST invoke this only on a config
// that has already passed Validate; Validate guarantees the duration
// parses as a positive value.
func (ic IdentityCreationConfig) UnverifiedPurgeAfterDuration() time.Duration {
	d, _ := parseIdentityCreationDuration(ic.UnverifiedPurgeAfter)
	return d
}

// GCIntervalDuration returns the parsed GCInterval as a time.Duration.
// Falls back to 6h when unparseable; the sweeper itself clamps
// pathologically small values up to a minimum to protect the store.
func (ic IdentityCreationConfig) GCIntervalDuration() time.Duration {
	if ic.GCInterval == "" {
		return 6 * time.Hour
	}
	d, err := time.ParseDuration(ic.GCInterval)
	if err != nil || d <= 0 {
		return 6 * time.Hour
	}
	return d
}

// IsEnabled reports whether identity creation is on. Encapsulates the
// pointer-default behaviour: a nil Enabled means the operator omitted
// the key, which defaults to true (REQ-IDENT-11). Use after applyDefaults
// has run; in that case Enabled is non-nil and the explicit dereference
// is fine, but the helper makes call sites resilient to a hypothetical
// caller that constructs an IdentityCreationConfig directly.
func (ic IdentityCreationConfig) IsEnabled() bool {
	return ic.Enabled == nil || *ic.Enabled
}

// validateClientLog checks semantic constraints on the [clientlog] block.
func validateClientLog(cl *ClientLogConfig) error {
	if cl.ReorderWindowMS < 0 {
		return fmt.Errorf("sysconfig: [clientlog] reorder_window_ms %d must be >= 0", cl.ReorderWindowMS)
	}
	if cl.LivetailDefaultDuration < 0 {
		return fmt.Errorf("sysconfig: [clientlog] livetail_default_duration must be >= 0")
	}
	if cl.LivetailMaxDuration < 0 {
		return fmt.Errorf("sysconfig: [clientlog] livetail_max_duration must be >= 0")
	}
	if cl.LivetailDefaultDuration > 0 && cl.LivetailMaxDuration > 0 &&
		cl.LivetailDefaultDuration > cl.LivetailMaxDuration {
		return fmt.Errorf("sysconfig: [clientlog] livetail_default_duration %s exceeds livetail_max_duration %s",
			cl.LivetailDefaultDuration.AsDuration(), cl.LivetailMaxDuration.AsDuration())
	}
	// Auth endpoint.
	if cl.Auth.RingBufferRows < 0 {
		return fmt.Errorf("sysconfig: [clientlog.auth] ring_buffer_rows %d must be >= 0", cl.Auth.RingBufferRows)
	}
	if cl.Auth.RingBufferAge < 0 {
		return fmt.Errorf("sysconfig: [clientlog.auth] ring_buffer_age must be >= 0")
	}
	if cl.Auth.BodyMaxBytes < 0 {
		return fmt.Errorf("sysconfig: [clientlog.auth] body_max_bytes %d must be >= 0", cl.Auth.BodyMaxBytes)
	}
	// Public endpoint.
	if cl.Public.RingBufferRows < 0 {
		return fmt.Errorf("sysconfig: [clientlog.public] ring_buffer_rows %d must be >= 0", cl.Public.RingBufferRows)
	}
	if cl.Public.RingBufferAge < 0 {
		return fmt.Errorf("sysconfig: [clientlog.public] ring_buffer_age must be >= 0")
	}
	if cl.Public.BodyMaxBytes < 0 {
		return fmt.Errorf("sysconfig: [clientlog.public] body_max_bytes %d must be >= 0", cl.Public.BodyMaxBytes)
	}
	return nil
}

// validateAttachmentShares checks semantic constraints on the
// [server.attachment_shares] block (REQ-SHARE-30..52). applyDefaults has
// already run, so every knob has a non-zero value.
func validateAttachmentShares(as *AttachmentSharesConfig) error {
	// When enabled, public_base_url is required and MUST be https.
	// The feature is inert (but not an error) when public_base_url is absent.
	if as.AttachmentSharesEnabled() && as.PublicBaseURL != "" {
		u, err := url.ParseRequestURI(as.PublicBaseURL)
		if err != nil || !u.IsAbs() {
			return fmt.Errorf("sysconfig: [server.attachment_shares] public_base_url %q is not a valid absolute URL",
				as.PublicBaseURL)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("sysconfig: [server.attachment_shares] public_base_url %q must use https scheme (REQ-SHARE-11d)",
				as.PublicBaseURL)
		}
	}
	// TTL ordering: max_ttl >= default_ttl >= pending_ttl (REQ-SHARE-20..21).
	if as.MaxTTL > 0 && as.DefaultTTL > 0 && as.DefaultTTL > as.MaxTTL {
		return fmt.Errorf("sysconfig: [server.attachment_shares] default_ttl %s exceeds max_ttl %s",
			as.DefaultTTL.AsDuration(), as.MaxTTL.AsDuration())
	}
	if as.DefaultTTL > 0 && as.PendingTTL > 0 && as.PendingTTL > as.DefaultTTL {
		return fmt.Errorf("sysconfig: [server.attachment_shares] pending_ttl %s exceeds default_ttl %s",
			as.PendingTTL.AsDuration(), as.DefaultTTL.AsDuration())
	}
	// All durations must be positive.
	if as.DefaultTTL <= 0 {
		return errors.New("sysconfig: [server.attachment_shares] default_ttl must be > 0")
	}
	if as.MaxTTL <= 0 {
		return errors.New("sysconfig: [server.attachment_shares] max_ttl must be > 0")
	}
	if as.PendingTTL <= 0 {
		return errors.New("sysconfig: [server.attachment_shares] pending_ttl must be > 0")
	}
	if as.RevokedGrace <= 0 {
		return errors.New("sysconfig: [server.attachment_shares] revoked_grace must be > 0")
	}
	if as.DownloadRequestsWindow <= 0 {
		return errors.New("sysconfig: [server.attachment_shares] download_requests_window must be > 0")
	}
	// Caps must be positive.
	if as.MaxSharesPerPrincipal <= 0 {
		return fmt.Errorf("sysconfig: [server.attachment_shares] max_shares_per_principal %d must be > 0",
			as.MaxSharesPerPrincipal)
	}
	if as.ShareQuotaPerPrincipal <= 0 {
		return fmt.Errorf("sysconfig: [server.attachment_shares] share_quota_per_principal %d must be > 0",
			as.ShareQuotaPerPrincipal.AsInt64())
	}
	if as.DownloadRequestsPerIPPerShare <= 0 {
		return fmt.Errorf("sysconfig: [server.attachment_shares] download_requests_per_ip_per_share %d must be > 0",
			as.DownloadRequestsPerIPPerShare)
	}
	return nil
}

// validateIMAPImport checks semantic constraints on the [imap_import] block
// (REQ-IMAP-IMP-03, -05, -23, -26). applyDefaults has already run so the
// numeric fields carry their defaults when the operator left them absent.
func validateIMAPImport(ii *IMAPImportConfig) error {
	// concurrent_accounts must be >= 1 (REQ-IMAP-IMP-26). applyDefaults
	// sets 16 for an absent field; only an explicit 0/negative is an error.
	if ii.ConcurrentAccounts < 1 {
		return fmt.Errorf("sysconfig: [imap_import] concurrent_accounts %d must be >= 1",
			ii.ConcurrentAccounts)
	}
	// poll_interval must be > 0 (REQ-IMAP-IMP-23). applyDefaults sets 60s
	// for an absent field.
	if ii.PollInterval <= 0 {
		return errors.New("sysconfig: [imap_import] poll_interval must be > 0")
	}
	// OAuth providers: each entry requires client_id, client_secret_ref (as
	// a secret reference), and token_endpoint (as an absolute URL). Scope is
	// optional. Provider names are already normalised to lowercase by
	// applyDefaults. (REQ-IMAP-IMP-03; STANDARDS §9.)
	for name, p := range ii.OAuth {
		if p.ClientID == "" {
			return fmt.Errorf("sysconfig: [imap_import.oauth.%s] client_id is required", name)
		}
		if p.ClientSecretRef == "" {
			return fmt.Errorf("sysconfig: [imap_import.oauth.%s] client_secret_ref is required (STANDARDS §9)", name)
		}
		if !IsSecretReference(p.ClientSecretRef) {
			return fmt.Errorf("sysconfig: [imap_import.oauth.%s] client_secret_ref %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)",
				name, p.ClientSecretRef)
		}
		if p.TokenEndpoint == "" {
			return fmt.Errorf("sysconfig: [imap_import.oauth.%s] token_endpoint is required", name)
		}
		if u, err := url.ParseRequestURI(p.TokenEndpoint); err != nil || !u.IsAbs() {
			return fmt.Errorf("sysconfig: [imap_import.oauth.%s] token_endpoint %q is not a valid absolute URL",
				name, p.TokenEndpoint)
		}
	}
	// Default folder-map rules: each entry requires a host pattern that is a
	// valid path.Match glob and at least one non-empty mapping
	// (REQ-IMAP-IMP-11).
	for i := range ii.DefaultFolderMap {
		rule := &ii.DefaultFolderMap[i]
		if rule.Host == "" {
			return fmt.Errorf("sysconfig: [[imap_import.default_folder_map]] #%d host is required", i)
		}
		if _, err := path.Match(rule.Host, ""); err != nil {
			return fmt.Errorf("sysconfig: [[imap_import.default_folder_map]] host %q is not a valid pattern: %v",
				rule.Host, err)
		}
		if len(rule.Mappings) == 0 {
			return fmt.Errorf("sysconfig: [[imap_import.default_folder_map]] host %q must define at least one mapping",
				rule.Host)
		}
		for up, hm := range rule.Mappings {
			if up == "" || hm == "" {
				return fmt.Errorf("sysconfig: [[imap_import.default_folder_map]] host %q has an empty upstream-folder or herold-mailbox name",
					rule.Host)
			}
		}
	}
	return nil
}

// validateOAuthProviders checks each entry in [server.oauth_providers.<name>].
// Provider names are normalised to lowercase at parse time (in applyDefaults);
// this function validates the provider values.
// Rules (REQ-AUTH-EXT-SUBMIT-03, STANDARDS §9):
//   - client_secret_ref must be a $VAR or file:/path secret reference (no
//     inline secrets).
//   - auth_url and token_url must parse as absolute URLs.
//   - scopes must be non-empty.
//
// Empty OAuthProviders maps are always valid (the feature is optional).
func validateOAuthProviders(c *Config) error {
	for name, p := range c.Server.OAuthProviders {
		if p.ClientID == "" {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] client_id is required", name)
		}
		if p.ClientSecretRef == "" {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] client_secret_ref is required (STANDARDS §9)", name)
		}
		if !IsSecretReference(p.ClientSecretRef) {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] client_secret_ref %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)", name, p.ClientSecretRef)
		}
		if p.AuthURL == "" {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] auth_url is required", name)
		}
		if u, err := url.ParseRequestURI(p.AuthURL); err != nil || !u.IsAbs() {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] auth_url %q is not a valid absolute URL", name, p.AuthURL)
		}
		if p.TokenURL == "" {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] token_url is required", name)
		}
		if u, err := url.ParseRequestURI(p.TokenURL); err != nil || !u.IsAbs() {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] token_url %q is not a valid absolute URL", name, p.TokenURL)
		}
		if len(p.Scopes) == 0 {
			return fmt.Errorf("sysconfig: [server.oauth_providers.%s] scopes must be non-empty", name)
		}
	}
	return nil
}

// validateSESInbound checks the [hooks.ses_inbound] block.
// When enabled is false, we only verify that no partial / conflicting
// configuration is present. When enabled is true, all required fields
// must be set and credential references must be secret references.
func validateSESInbound(ses *SESInboundConfig) error {
	if !ses.Enabled {
		return nil
	}
	if ses.AWSRegion == "" {
		return errors.New("sysconfig: [hooks.ses_inbound] aws_region is required when enabled")
	}
	if len(ses.S3BucketAllowlist) == 0 {
		return errors.New("sysconfig: [hooks.ses_inbound] s3_bucket_allowlist must not be empty when enabled")
	}
	if len(ses.SNSTopicARNAllowlist) == 0 {
		return errors.New("sysconfig: [hooks.ses_inbound] sns_topic_arn_allowlist must not be empty when enabled")
	}
	if len(ses.SignatureCertHostAllowlist) == 0 {
		return errors.New("sysconfig: [hooks.ses_inbound] signature_cert_host_allowlist must not be empty when enabled")
	}
	if ses.AWSAccessKeyIDEnv == "" {
		return errors.New("sysconfig: [hooks.ses_inbound] aws_access_key_id_env is required when enabled")
	}
	if !IsSecretReference(ses.AWSAccessKeyIDEnv) {
		return fmt.Errorf("sysconfig: [hooks.ses_inbound] aws_access_key_id_env %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)", ses.AWSAccessKeyIDEnv)
	}
	if ses.AWSSecretAccessKeyEnv == "" {
		return errors.New("sysconfig: [hooks.ses_inbound] aws_secret_access_key_env is required when enabled")
	}
	if !IsSecretReference(ses.AWSSecretAccessKeyEnv) {
		return fmt.Errorf("sysconfig: [hooks.ses_inbound] aws_secret_access_key_env %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)", ses.AWSSecretAccessKeyEnv)
	}
	if ses.AWSSessionTokenEnv != "" && !IsSecretReference(ses.AWSSessionTokenEnv) {
		return fmt.Errorf("sysconfig: [hooks.ses_inbound] aws_session_token_env %q must be \"$VAR\" or \"file:/path\" (STANDARDS §9)", ses.AWSSessionTokenEnv)
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

// AdminRESTURL derives the admin REST base URL from cfg by inspecting the
// first [[listener]] with kind = "admin". The returned string is suitable for
// writing to ~/.herold/credentials.toml as server_url.
//
// Scheme selection:
//   - "https" when the listener's tls field is "starttls" or "implicit" (any
//     TLS-producing value). The caller should still trust the cert via the
//     normal OS trust store or an explicit --tls-ca flag.
//   - "http" when tls = "none" (the quickstart default).
//
// Bind-address translation:
//   - "0.0.0.0" -> "127.0.0.1"  (wildcard IPv4 -> loopback)
//   - "::"      -> "[::1]"       (wildcard IPv6 -> loopback)
//   - Any other address is written through verbatim.
//
// The second return value carries operator warnings (non-fatal). Currently
// the only warning is emitted when tls="none" and the bind address is not
// loopback, which means the API key flows in cleartext to a non-local
// endpoint.
//
// If no admin-kind listener exists AdminRESTURL returns ("", nil, false). The
// caller decides whether to warn; Bootstrap treats this as a non-fatal
// condition that still allows the key to be written.
func AdminRESTURL(cfg *Config) (string, []string, bool) {
	for _, l := range cfg.Listener {
		if l.Kind != "admin" {
			continue
		}
		scheme := "http"
		if l.TLS == "starttls" || l.TLS == "implicit" {
			scheme = "https"
		}
		host, port, err := net.SplitHostPort(l.Address)
		if err != nil {
			// Malformed address; skip rather than panic — validation
			// would have caught this already in a real startup path.
			return "", nil, false
		}
		originalBind := l.Address
		switch {
		case host == "0.0.0.0":
			host = "127.0.0.1"
		case host == "::":
			host = "[::1]"
		case strings.EqualFold(host, "localhost"):
			// `localhost` expands to two listening sockets (IPv4 +
			// IPv6) at bind time. Pin the saved server_url to the IPv4
			// loopback so the value is deterministic across operator
			// machines and works regardless of how the local resolver
			// orders A vs AAAA records.
			host = "127.0.0.1"
		}
		// If the host contains a colon (bare IPv6 literal) wrap it in
		// brackets so the URL is well-formed.
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		var warnings []string
		if scheme == "http" && !isLoopbackBindAddr(originalBind) {
			warnings = append(warnings, fmt.Sprintf(
				"admin listener has tls=none and a non-loopback bind (%s); "+
					"the API key will flow in cleartext to that endpoint — "+
					"only use this for local development",
				originalBind,
			))
		}
		return fmt.Sprintf("%s://%s:%s", scheme, host, port), warnings, true
	}
	return "", nil, false
}

// ResolveBindAddresses turns a single listener address into the concrete
// host:port pairs that should be bound. The expansion exists because
// macOS resolves the literal hostname "localhost" to ::1 first (an
// IPv6 loopback) but Apple Mail and other CFNetwork-based clients do
// not Happy-Eyeballs back to IPv4 reliably; an operator who writes
// `address = "127.0.0.1:1143"` and tells a client `localhost:1143`
// thus hits a refused IPv6 connect with no IPv4 fallback. Writing
// `address = "localhost:1143"` instead expands here into both
// `127.0.0.1:1143` and `[::1]:1143`, so both stacks accept.
//
// Rules:
//   - host == "localhost" (case-insensitive): expand to 127.0.0.1 and ::1.
//   - everything else (literal IP, "0.0.0.0", "::", explicit hostname):
//     return the address as-is. We deliberately do not DNS-resolve
//     non-loopback hostnames at bind time; an operator who writes
//     `mail.example.com:443` wants the listener to follow whatever
//     the kernel binds for that name and to fail loudly if it does
//     not exist, not to silently bind on N IPs.
//
// An empty address is returned as a single empty string so the caller
// surfaces sysconfig.Validate's existing "address is required" error.
func ResolveBindAddresses(address string) ([]string, error) {
	if address == "" {
		return []string{""}, nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("sysconfig: address %q: %w", address, err)
	}
	if strings.EqualFold(host, "localhost") {
		return []string{
			net.JoinHostPort("127.0.0.1", port),
			net.JoinHostPort("::1", port),
		}, nil
	}
	return []string{address}, nil
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
