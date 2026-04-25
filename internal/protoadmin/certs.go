package protoadmin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// acmeCertDTO is the wire representation of a stored ACME cert. The
// PrivateKeyPEM column is intentionally omitted so listing/showing certs
// over the admin REST does not expose key material.
type acmeCertDTO struct {
	Hostname  string    `json:"hostname"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Issuer    string    `json:"issuer"`
	OrderID   uint64    `json:"order_id,omitempty"`
	ChainPEM  string    `json:"chain_pem,omitempty"` // populated only by the detail endpoint
}

func toACMECertDTO(c store.ACMECert, includeChain bool) acmeCertDTO {
	d := acmeCertDTO{
		Hostname:  c.Hostname,
		NotBefore: c.NotBefore,
		NotAfter:  c.NotAfter,
		Issuer:    c.Issuer,
		OrderID:   uint64(c.OrderID),
	}
	if includeChain {
		d.ChainPEM = c.ChainPEM
	}
	return d
}

// handleListACMECerts lists every cert with NotAfter no further than
// 100 years from now (i.e. essentially everything). The admin client
// renders an "expiring soon" view by filtering NotAfter < now+30d
// client-side.
func (s *Server) handleListACMECerts(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	cutoff := s.clk.Now().Add(100 * 365 * 24 * time.Hour)
	rows, err := s.store.Meta().ListACMECertsExpiringBefore(r.Context(), cutoff)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	items := make([]acmeCertDTO, 0, len(rows))
	for _, c := range rows {
		items = append(items, toACMECertDTO(c, false))
	}
	writeJSON(w, http.StatusOK, pageDTO[acmeCertDTO]{Items: items, Next: nil})
}

func (s *Server) handleGetACMECert(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	hostname := strings.ToLower(strings.TrimSpace(r.PathValue("hostname")))
	if hostname == "" {
		writeProblem(w, r, http.StatusBadRequest, "certs/invalid_hostname",
			"hostname is required", "")
		return
	}
	c, err := s.store.Meta().GetACMECert(r.Context(), hostname)
	if err != nil {
		s.writeCertsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toACMECertDTO(c, true))
}

func (s *Server) handleRenewACMECert(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	hostname := strings.ToLower(strings.TrimSpace(r.PathValue("hostname")))
	if hostname == "" {
		writeProblem(w, r, http.StatusBadRequest, "certs/invalid_hostname",
			"hostname is required", "")
		return
	}
	if s.opts.CertRenewer == nil {
		writeProblem(w, r, http.StatusNotImplemented, "certs/not_implemented",
			"certificate renewal is not configured on this server", "")
		return
	}
	if err := s.opts.CertRenewer.RenewCert(r.Context(), hostname); err != nil {
		s.appendAudit(r.Context(), "cert.renew",
			fmt.Sprintf("cert:%s", hostname),
			store.OutcomeFailure, err.Error(), nil)
		writeProblem(w, r, http.StatusBadGateway, "certs/renewal_failed",
			err.Error(), "")
		return
	}
	c, err := s.store.Meta().GetACMECert(r.Context(), hostname)
	if err != nil {
		s.writeCertsError(w, r, err)
		return
	}
	s.appendAudit(r.Context(), "cert.renew",
		fmt.Sprintf("cert:%s", hostname),
		store.OutcomeSuccess, "", nil)
	writeJSON(w, http.StatusOK, toACMECertDTO(c, false))
}

func (s *Server) writeCertsError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "certs/not_found", err.Error(), "")
	default:
		s.writeStoreError(w, r, err)
	}
}
