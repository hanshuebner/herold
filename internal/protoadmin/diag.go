package protoadmin

import (
	"net/http"
	"strings"
)

// handleDiagDNSCheck dispatches to the configured DNSVerifier and returns
// the resulting report as JSON. Returns 501 when no verifier is wired
// in.
func (s *Server) handleDiagDNSCheck(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("domain")))
	if domain == "" {
		writeProblem(w, r, http.StatusBadRequest, "diag/invalid_domain",
			"domain is required", "")
		return
	}
	if s.opts.DNSVerifier == nil {
		writeProblem(w, r, http.StatusNotImplemented, "diag/not_implemented",
			"DNS verifier is not configured on this server", "")
		return
	}
	report, err := s.opts.DNSVerifier.VerifyDomain(r.Context(), domain)
	if err != nil {
		writeProblem(w, r, http.StatusBadGateway, "diag/dns_check_failed",
			err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, report)
}
