package protoadmin

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// MXResolver is the minimal DNS interface the detect-provider endpoint
// uses to classify a domain's mail provider by its MX records.
// It is satisfied by *mailauth.SystemResolver and by in-test fakes.
type MXResolver interface {
	// MXLookup returns MX records for domain sorted by preference
	// ascending. Any error (NXDOMAIN, no MX, network failure) is
	// treated as "provider unknown" by the handler — the wizard falls
	// back to the verification-code flow.
	MXLookup(ctx context.Context, domain string) ([]*net.MX, error)
}

// netMXResolver wraps net.DefaultResolver to satisfy MXResolver.
// Used as the production default when Options.MXResolver is nil.
type netMXResolver struct{}

func (netMXResolver) MXLookup(ctx context.Context, domain string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, domain)
}

// detectProviderResponse is the wire form of the detect-provider response.
// Provider is nil when the domain's mail provider is not recognised.
type detectProviderResponse struct {
	Provider *string `json:"provider"`
}

// handleDetectProvider implements
//
//	GET /api/v1/identities/detect-provider?email=<address>
//
// It parses the domain from the email query parameter, performs an MX
// lookup, and classifies the result as "google", "microsoft", or null.
// Errors from the MX lookup (NXDOMAIN, no records, transient failure)
// return {"provider":null} with 200 — the caller falls back to the
// verification-code flow rather than surfacing a DNS error to the user.
// Malformed / missing email returns 400.
//
// Side-effect free: no writes, no store access.
func (s *Server) handleDetectProvider(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	if email == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"email query parameter is required", "")
		return
	}

	atIdx := strings.LastIndex(email, "@")
	if atIdx < 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"email must contain @", "")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(email[atIdx+1:]))
	if domain == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"email has empty domain part", "")
		return
	}

	mxs, err := s.opts.MXResolver.MXLookup(r.Context(), domain)
	if err != nil {
		// Any lookup failure (NXDOMAIN, no MX, transient) is non-fatal;
		// the wizard falls back to the verification-code flow.
		writeJSON(w, http.StatusOK, detectProviderResponse{Provider: nil})
		return
	}

	hosts := make([]string, 0, len(mxs))
	for _, mx := range mxs {
		hosts = append(hosts, mx.Host)
	}

	provider := classifyMXProvider(hosts)
	resp := detectProviderResponse{}
	if provider != "" {
		resp.Provider = &provider
	}
	writeJSON(w, http.StatusOK, resp)
}

// classifyMXProvider inspects a slice of MX target hostnames and returns
// the first recognised provider: "google", "microsoft", or "" (unknown).
//
// Matching is suffix-anchored on dot boundaries — the suffix string starts
// with a literal dot (e.g. ".google.com"), so the check is equivalent to
// a full DNS-label boundary match. This prevents spoofable substring
// matches: "notgoogle.com.evil.com" does not end with ".google.com",
// and "google.com.evil.com" does not end with ".google.com" either.
//
// Hostnames are canonicalised to lowercase and have a trailing dot
// stripped before comparison, matching RFC 1035 normalisation.
func classifyMXProvider(hosts []string) string {
	for _, raw := range hosts {
		h := strings.ToLower(strings.TrimSuffix(raw, "."))
		switch {
		case isGoogleMXHost(h):
			return "google"
		case isMicrosoftMXHost(h):
			return "microsoft"
		}
	}
	return ""
}

// isGoogleMXHost reports whether host is a Google mail MX target.
// Covers Gmail (aspmx.l.google.com family) and Google Workspace
// (*.googlemail.com legacy) MX endpoints.
func isGoogleMXHost(host string) bool {
	return host == "google.com" ||
		strings.HasSuffix(host, ".google.com") ||
		host == "googlemail.com" ||
		strings.HasSuffix(host, ".googlemail.com")
}

// isMicrosoftMXHost reports whether host is a Microsoft mail MX target.
// Covers Exchange Online / M365 (*.mail.protection.outlook.com) and
// consumer Outlook.com (*.outlook.com) MX endpoints.
func isMicrosoftMXHost(host string) bool {
	return host == "mail.protection.outlook.com" ||
		strings.HasSuffix(host, ".mail.protection.outlook.com") ||
		host == "outlook.com" ||
		strings.HasSuffix(host, ".outlook.com")
}
