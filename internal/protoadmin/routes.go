package protoadmin

import (
	"net/http"

	"github.com/hanshuebner/herold/internal/auth"
)

// registerRoutes registers every /api/v1/... endpoint on mux. Routes are
// declared flat rather than nested because the Go 1.22 stdlib mux
// honours method + path patterns without a separate Router type.
//
// REQ-AUTH-SCOPE-02: every authenticated route is scope-gated. The
// vast majority of protoadmin's surface requires admin scope (the
// REST surface is mounted on the admin listener and operators are
// the consumers). The scope-self handlers (GET /api/v1/api-keys,
// DELETE /api/v1/api-keys/{id}, POST /api/v1/principals/{pid}/api-keys
// against the caller's own pid, the principals/{self} self-service
// flows) are gated only by requireAuth + requireSelfOrAdmin inside
// the handler — the scope check would over-match because a non-
// admin end-user with a valid cookie should be able to manage their
// own keys. Those handlers therefore retain their existing in-handler
// authorisation gates and skip the admin requireScope wrapper.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	admin := s.requireScope(auth.ScopeAdmin)
	auth1 := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAuth(h) }
	authAdmin := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAuth(admin(h)) }

	// Health (unauth).
	mux.HandleFunc("GET /api/v1/healthz/live", s.handleHealthLive)
	mux.HandleFunc("GET /api/v1/healthz/ready", s.handleHealthReady)

	// Bootstrap (unauth, rate-limited per remote).
	mux.HandleFunc("POST /api/v1/bootstrap", s.handleBootstrap)

	// OIDC callback (unauth).
	mux.HandleFunc("POST /api/v1/oidc/callback", s.handleOIDCCallback)

	// Principals.
	mux.HandleFunc("GET /api/v1/principals", authAdmin(s.handleListPrincipals))
	mux.HandleFunc("POST /api/v1/principals", authAdmin(s.handleCreatePrincipal))
	mux.HandleFunc("GET /api/v1/principals/{pid}", auth1(s.handleGetPrincipal))
	mux.HandleFunc("PATCH /api/v1/principals/{pid}", auth1(s.handlePatchPrincipal))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}", authAdmin(s.handleDeletePrincipal))
	mux.HandleFunc("PUT /api/v1/principals/{pid}/password", auth1(s.handleSetPassword))
	mux.HandleFunc("POST /api/v1/principals/{pid}/totp/enroll", auth1(s.handleTOTPEnroll))
	mux.HandleFunc("POST /api/v1/principals/{pid}/totp/confirm", auth1(s.handleTOTPConfirm))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/totp", auth1(s.handleTOTPDisable))

	// Principal-scoped API keys.
	mux.HandleFunc("GET /api/v1/principals/{pid}/api-keys", authAdmin(s.handleListPrincipalAPIKeys))
	mux.HandleFunc("POST /api/v1/principals/{pid}/api-keys", auth1(s.handleCreateAPIKey))

	// Principal-scoped OIDC links.
	mux.HandleFunc("GET /api/v1/principals/{pid}/oidc-links", auth1(s.handleListOIDCLinks))
	mux.HandleFunc("POST /api/v1/principals/{pid}/oidc-links/begin", auth1(s.handleBeginOIDCLink))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/oidc-links/{provider_id}", auth1(s.handleUnlinkOIDC))

	// Domains.
	mux.HandleFunc("GET /api/v1/domains", authAdmin(s.handleListDomains))
	mux.HandleFunc("POST /api/v1/domains", authAdmin(s.handleCreateDomain))
	mux.HandleFunc("DELETE /api/v1/domains/{name}", authAdmin(s.handleDeleteDomain))

	// Aliases.
	mux.HandleFunc("GET /api/v1/aliases", authAdmin(s.handleListAliases))
	mux.HandleFunc("POST /api/v1/aliases", authAdmin(s.handleCreateAlias))
	mux.HandleFunc("DELETE /api/v1/aliases/{id}", authAdmin(s.handleDeleteAlias))

	// API keys (flat surface). Self-service: a non-admin principal
	// uses these to inspect / revoke their own keys.
	mux.HandleFunc("GET /api/v1/api-keys", auth1(s.handleListOwnAPIKeys))
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", auth1(s.handleDeleteAPIKey))

	// OIDC providers.
	mux.HandleFunc("GET /api/v1/oidc/providers", authAdmin(s.handleListOIDCProviders))
	mux.HandleFunc("POST /api/v1/oidc/providers", authAdmin(s.handleCreateOIDCProvider))
	mux.HandleFunc("DELETE /api/v1/oidc/providers/{id}", authAdmin(s.handleDeleteOIDCProvider))

	// Server.
	mux.HandleFunc("GET /api/v1/server/status", authAdmin(s.handleServerStatus))
	mux.HandleFunc("GET /api/v1/server/config-check", authAdmin(s.handleServerConfigCheck))

	// Audit log.
	mux.HandleFunc("GET /api/v1/audit", authAdmin(s.handleAuditLog))

	// Outbound queue.
	mux.HandleFunc("GET /api/v1/queue", authAdmin(s.handleListQueue))
	mux.HandleFunc("GET /api/v1/queue/stats", authAdmin(s.handleQueueStats))
	mux.HandleFunc("POST /api/v1/queue/flush", authAdmin(s.handleQueueFlush))
	mux.HandleFunc("GET /api/v1/queue/{id}", authAdmin(s.handleGetQueueItem))
	mux.HandleFunc("POST /api/v1/queue/{id}/retry", authAdmin(s.handleRetryQueueItem))
	mux.HandleFunc("POST /api/v1/queue/{id}/hold", authAdmin(s.handleHoldQueueItem))
	mux.HandleFunc("POST /api/v1/queue/{id}/release", authAdmin(s.handleReleaseQueueItem))
	mux.HandleFunc("DELETE /api/v1/queue/{id}", authAdmin(s.handleDeleteQueueItem))

	// ACME certs.
	mux.HandleFunc("GET /api/v1/certs", authAdmin(s.handleListACMECerts))
	mux.HandleFunc("GET /api/v1/certs/{hostname}", authAdmin(s.handleGetACMECert))
	mux.HandleFunc("POST /api/v1/certs/{hostname}/renew", authAdmin(s.handleRenewACMECert))

	// Spam policy.
	mux.HandleFunc("GET /api/v1/spam/policy", authAdmin(s.handleGetSpamPolicy))
	mux.HandleFunc("PUT /api/v1/spam/policy", authAdmin(s.handlePutSpamPolicy))

	// LLM categorisation: per-principal recategorise + job poll
	// (REQ-FILT-220).
	mux.HandleFunc("POST /api/v1/principals/{pid}/recategorise", auth1(s.handleRecategorisePrincipal))
	mux.HandleFunc("GET /api/v1/jobs/{id}", auth1(s.handleGetJob))

	// Webhooks.
	mux.HandleFunc("GET /api/v1/webhooks", authAdmin(s.handleListWebhooks))
	mux.HandleFunc("POST /api/v1/webhooks", authAdmin(s.handleCreateWebhook))
	mux.HandleFunc("GET /api/v1/webhooks/{id}", authAdmin(s.handleGetWebhook))
	mux.HandleFunc("PATCH /api/v1/webhooks/{id}", authAdmin(s.handlePatchWebhook))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", authAdmin(s.handleDeleteWebhook))

	// OIDC provider extensions (show / update).
	mux.HandleFunc("GET /api/v1/oidc/providers/{id}", authAdmin(s.handleGetOIDCProvider))
	mux.HandleFunc("PATCH /api/v1/oidc/providers/{id}", authAdmin(s.handlePatchOIDCProvider))

	// Diag (DNS check). Backup/restore/migrate live in a sibling file
	// owned by the parallel agent.
	mux.HandleFunc("GET /api/v1/diag/dns-check/{domain}", authAdmin(s.handleDiagDNSCheck))

	// Inbound attachment policy (REQ-FLOW-ATTPOL-01..02).
	mux.HandleFunc("GET /api/v1/mailboxes/{addr}/attachment-policy", authAdmin(s.handleGetMailboxAttPol))
	mux.HandleFunc("PUT /api/v1/mailboxes/{addr}/attachment-policy", authAdmin(s.handlePutMailboxAttPol))
	mux.HandleFunc("GET /api/v1/domains/{name}/attachment-policy", authAdmin(s.handleGetDomainAttPol))
	mux.HandleFunc("PUT /api/v1/domains/{name}/attachment-policy", authAdmin(s.handlePutDomainAttPol))
}
