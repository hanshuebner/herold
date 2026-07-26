package extimg

import (
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// FromSysConfig adapts the operator-facing TOML config block into the
// runtime Config consumed by Fetcher / Internalize. Defaults applied
// upstream by sysconfig.applyDefaults are honoured here; the only
// transformation is the *net.IPNet ↔ netip.Prefix bridge in the SSRF
// guard (see ssrf.go).
//
// hostname is rendered into server-stamped Authentication-Results
// headers (REQ-EXTIMG-43); pass cfg.Server.Hostname.
func FromSysConfig(c sysconfig.ExternalImagesConfig, hostname string) Config {
	out := Config{
		Mode:                   Mode(c.Mode),
		MaxPerImageBytes:       c.Limits.MaxPerImageBytes,
		MaxPerMessageImages:    c.Limits.MaxPerMessageImages,
		MaxPerMessageBytes:     c.Limits.MaxPerMessageBytes,
		PerImageConnectTimeout: c.Limits.PerImageConnectTimeout.AsDuration(),
		PerImageTotalTimeout:   c.Limits.PerImageTotalTimeout.AsDuration(),
		PerMessageTimeout:      c.Limits.PerMessageTimeout.AsDuration(),
		ConcurrentFetches:      c.Limits.ConcurrentFetches,
		FollowRedirectsMax:     c.Limits.FollowRedirectsMax,
		DenyCIDRs:              MustParseCIDRs(c.Network.DenyCIDRs),
		AllowPrivate:           c.Network.AllowPrivate,
		DKIM:                   DKIMHandling(c.DKIM.OnModification),
		AuditLogFetches:        c.Audit.LogFetches,
		HostHeader:             hostname,
	}
	if c.Limits.RequireHTTPS != nil {
		out.RequireHTTPS = *c.Limits.RequireHTTPS
	} else {
		out.RequireHTTPS = false
	}
	out.resolveOptional()
	return out
}
