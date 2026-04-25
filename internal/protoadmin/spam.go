package protoadmin

import (
	"net/http"

	"github.com/hanshuebner/herold/internal/store"
)

func (s *Server) handleGetSpamPolicy(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	if s.opts.SpamPolicyStore == nil {
		writeProblem(w, r, http.StatusNotImplemented, "spam/not_implemented",
			"spam policy store is not configured on this server", "")
		return
	}
	writeJSON(w, http.StatusOK, s.opts.SpamPolicyStore.GetSpamPolicy())
}

func (s *Server) handlePutSpamPolicy(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if !requireAdmin(w, r, caller) {
		return
	}
	if s.opts.SpamPolicyStore == nil {
		writeProblem(w, r, http.StatusNotImplemented, "spam/not_implemented",
			"spam policy store is not configured on this server", "")
		return
	}
	var req SpamPolicy
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.PluginName == "" {
		writeProblem(w, r, http.StatusBadRequest, "spam/validation_failed",
			"plugin_name is required", "")
		return
	}
	if req.Threshold < 0 || req.Threshold > 1 {
		writeProblem(w, r, http.StatusBadRequest, "spam/validation_failed",
			"threshold must be in [0.0, 1.0]", "")
		return
	}
	s.opts.SpamPolicyStore.SetSpamPolicy(req)
	s.appendAudit(r.Context(), "spam.policy.update",
		"spam:policy", store.OutcomeSuccess, "",
		map[string]string{"plugin_name": req.PluginName})
	writeJSON(w, http.StatusOK, s.opts.SpamPolicyStore.GetSpamPolicy())
}
