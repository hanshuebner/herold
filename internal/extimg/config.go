package extimg

import (
	"fmt"
	"net"
	"net/netip"
	"time"
)

// Mode is the operator-wide external-image policy (REQ-EXTIMG-01).
type Mode string

const (
	// ModeInternalize rewrites the body at delivery, fetching every
	// external image and attaching as inline parts (REQ-EXTIMG-02,
	// the default — REQ-EXTIMG-05).
	ModeInternalize Mode = "internalize"
	// ModePassthrough leaves the body unchanged. The package's
	// Internalize entry point becomes a no-op in this mode
	// (REQ-EXTIMG-03).
	ModePassthrough Mode = "passthrough"
)

// DKIMHandling selects what happens to inbound DKIM signatures when
// the rewriter modifies the body (REQ-EXTIMG-41..43).
type DKIMHandling string

const (
	// DKIMStrip removes the now-invalid DKIM-Signature plus inbound
	// Authentication-Results headers and emits a server-stamped
	// replacement carrying the verdict captured before rewriting.
	DKIMStrip DKIMHandling = "strip"
	// DKIMKeep leaves the broken DKIM signature in the message.
	// Useful only for debugging — recipients will report dkim=fail.
	DKIMKeep DKIMHandling = "keep"
)

// Config is the runtime configuration consumed by Internalize. Build
// it from sysconfig.ExternalImagesConfig via FromSysConfig at startup
// and on SIGHUP reload; the package never reads sysconfig directly so
// it is testable in isolation.
type Config struct {
	Mode Mode

	// Limits (REQ-EXTIMG-20..27).
	MaxPerImageBytes       int64
	MaxPerMessageImages    int
	MaxPerMessageBytes     int64
	PerImageConnectTimeout time.Duration
	PerImageTotalTimeout   time.Duration
	PerMessageTimeout      time.Duration
	ConcurrentFetches      int
	RequireHTTPS           bool
	FollowRedirectsMax     int

	// Network / SSRF (REQ-EXTIMG-30..37).
	DenyCIDRs    []*net.IPNet
	AllowPrivate bool
	// AllowedPorts extends the default 80/443 allowlist. Empty leaves
	// only the defaults active. Used by the test suite (httptest binds
	// to a random high port) and by operators who legitimately fetch
	// from non-standard ports (e.g. an internal CDN on :8443 with
	// AllowPrivate enabled).
	AllowedPorts []int

	// DKIM disposition (REQ-EXTIMG-40..47).
	DKIM DKIMHandling

	// Audit (REQ-EXTIMG-80..82).
	AuditLogFetches bool

	// HostHeader names this server in the server-stamped
	// Authentication-Results header (REQ-EXTIMG-43).
	HostHeader string
}

// validate is the lightweight sanity check for a hand-constructed
// Config (tests use it; the sysconfig path already validates upstream).
func (c Config) validate() error {
	switch c.Mode {
	case "", ModeInternalize, ModePassthrough:
	default:
		return fmt.Errorf("extimg: unknown mode %q", c.Mode)
	}
	switch c.DKIM {
	case "", DKIMStrip, DKIMKeep:
	default:
		return fmt.Errorf("extimg: unknown dkim handling %q", c.DKIM)
	}
	if c.MaxPerImageBytes < 0 || c.MaxPerMessageBytes < 0 ||
		c.MaxPerMessageImages < 0 || c.ConcurrentFetches < 0 ||
		c.FollowRedirectsMax < 0 {
		return fmt.Errorf("extimg: limits must be >= 0")
	}
	return nil
}

// resolveOptional fills zero-valued fields with the documented
// defaults (REQ-EXTIMG-21..27). FromSysConfig already does this; the
// helper is kept for tests that hand-build a partial Config.
func (c *Config) resolveOptional() {
	if c.Mode == "" {
		c.Mode = ModeInternalize
	}
	if c.MaxPerImageBytes == 0 {
		c.MaxPerImageBytes = 5 * 1024 * 1024
	}
	if c.MaxPerMessageImages == 0 {
		c.MaxPerMessageImages = 100
	}
	if c.MaxPerMessageBytes == 0 {
		c.MaxPerMessageBytes = 50 * 1024 * 1024
	}
	if c.PerImageConnectTimeout == 0 {
		c.PerImageConnectTimeout = 5 * time.Second
	}
	if c.PerImageTotalTimeout == 0 {
		c.PerImageTotalTimeout = 30 * time.Second
	}
	if c.PerMessageTimeout == 0 {
		c.PerMessageTimeout = 60 * time.Second
	}
	if c.ConcurrentFetches == 0 {
		c.ConcurrentFetches = 8
	}
	if c.FollowRedirectsMax == 0 {
		c.FollowRedirectsMax = 3
	}
	if c.DKIM == "" {
		c.DKIM = DKIMStrip
	}
	// RequireHTTPS defaults to false; operators opt into https-only
	// internalization via Limits.RequireHTTPS=true.
}

// MustParseCIDRs is a small helper used by FromSysConfig to convert
// the operator's TOML deny_cidrs entries into *net.IPNet. Bad entries
// are caught at Validate; this helper assumes pre-validated input.
func MustParseCIDRs(raws []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(raws))
	for _, raw := range raws {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// netipFrom4 / netipFrom6 are tiny helpers used by ssrf.go's prefix
// constructors so the long built-in deny list reads as a flat slice.
func netipFrom4(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func netipFrom6(s string) netip.Prefix { return netip.MustParsePrefix(s) }
