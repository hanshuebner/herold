package protoui

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

type domainsListData struct {
	Items []store.Domain
}

type domainDetailData struct {
	Domain  store.Domain
	Aliases []store.Alias
}

func (s *Server) handleDomainsList(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	rows, err := s.store.Meta().ListLocalDomains(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "List domains failed: "+err.Error())
		return
	}
	flash := flashFromQuery(r)
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Domains",
		Active:   "domains",
		Flash:    flash,
		BodyTmpl: "domains_list_body",
		Body:     domainsListData{Items: rows},
	})
}

func (s *Server) handleDomainsCreate(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PostForm.Get("name")))
	if name == "" {
		s.renderError(w, r, http.StatusBadRequest, "Domain name required.")
		return
	}
	if err := s.store.Meta().InsertDomain(r.Context(), store.Domain{Name: name, IsLocal: true}); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Insert failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.pathPrefix+"/domains?flash=domain_created", http.StatusSeeOther)
}

func (s *Server) handleDomainDetail(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	dom, err := s.store.Meta().GetDomain(r.Context(), name)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "Domain not found.")
		return
	}
	body := domainDetailData{Domain: dom}
	if aliases, err := s.store.Meta().ListAliases(r.Context(), name); err == nil {
		body.Aliases = aliases
	}
	flash := flashFromQuery(r)
	s.renderPage(w, r, http.StatusOK, &pageData{
		Title:    "Domain " + name,
		Active:   "domains",
		Flash:    flash,
		BodyTmpl: "domains_detail_body",
		Body:     body,
	})
}

func (s *Server) handleDomainDelete(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	if r.PostForm.Get("confirm") != name {
		s.renderError(w, r, http.StatusBadRequest, "Type the domain name to confirm.")
		return
	}
	if err := s.store.Meta().DeleteDomain(r.Context(), name); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Delete failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.pathPrefix+"/domains?flash=domain_deleted", http.StatusSeeOther)
}

func (s *Server) handleAliasCreate(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	local := strings.ToLower(strings.TrimSpace(r.PostForm.Get("local")))
	targetRaw := strings.TrimSpace(r.PostForm.Get("target_principal_id"))
	if local == "" || targetRaw == "" {
		s.renderError(w, r, http.StatusBadRequest, "Local part and target principal id required.")
		return
	}
	target, err := strconv.ParseUint(targetRaw, 10, 64)
	if err != nil || target == 0 {
		s.renderError(w, r, http.StatusBadRequest, "Invalid target principal id.")
		return
	}
	alias := store.Alias{
		LocalPart:       local,
		Domain:          domain,
		TargetPrincipal: store.PrincipalID(target),
	}
	if raw := strings.TrimSpace(r.PostForm.Get("expires_at")); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			alias.ExpiresAt = &t
		}
	}
	if _, err := s.store.Meta().InsertAlias(r.Context(), alias); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Insert failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.pathPrefix+"/domains/"+domain+"?flash=alias_created", http.StatusSeeOther)
}

func (s *Server) handleAliasDelete(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFromCtx(r.Context())
	if !caller.Flags.Has(store.PrincipalFlagAdmin) {
		s.renderError(w, r, http.StatusForbidden, "Admin privileges required.")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PathValue("name")))
	idRaw := r.PathValue("id")
	id, err := strconv.ParseUint(idRaw, 10, 64)
	if err != nil || id == 0 {
		s.renderError(w, r, http.StatusBadRequest, "Invalid alias id.")
		return
	}
	if err := s.store.Meta().DeleteAlias(r.Context(), store.AliasID(id)); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Delete failed: "+err.Error())
		return
	}
	http.Redirect(w, r, s.pathPrefix+"/domains/"+domain+"?flash=alias_deleted", http.StatusSeeOther)
}
