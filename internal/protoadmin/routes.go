package protoadmin

import "net/http"

// registerRoutes registers every /api/v1/... endpoint on mux. Routes are
// declared flat rather than nested because the Go 1.22 stdlib mux
// honours method + path patterns without a separate Router type.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Health (unauth).
	mux.HandleFunc("GET /api/v1/healthz/live", s.handleHealthLive)
	mux.HandleFunc("GET /api/v1/healthz/ready", s.handleHealthReady)

	// Bootstrap (unauth, rate-limited per remote).
	mux.HandleFunc("POST /api/v1/bootstrap", s.handleBootstrap)

	// OIDC callback (unauth).
	mux.HandleFunc("POST /api/v1/oidc/callback", s.handleOIDCCallback)

	// Principals.
	mux.HandleFunc("GET /api/v1/principals", s.requireAuth(s.handleListPrincipals))
	mux.HandleFunc("POST /api/v1/principals", s.requireAuth(s.handleCreatePrincipal))
	mux.HandleFunc("GET /api/v1/principals/{pid}", s.requireAuth(s.handleGetPrincipal))
	mux.HandleFunc("PATCH /api/v1/principals/{pid}", s.requireAuth(s.handlePatchPrincipal))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}", s.requireAuth(s.handleDeletePrincipal))
	mux.HandleFunc("PUT /api/v1/principals/{pid}/password", s.requireAuth(s.handleSetPassword))
	mux.HandleFunc("POST /api/v1/principals/{pid}/totp/enroll", s.requireAuth(s.handleTOTPEnroll))
	mux.HandleFunc("POST /api/v1/principals/{pid}/totp/confirm", s.requireAuth(s.handleTOTPConfirm))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/totp", s.requireAuth(s.handleTOTPDisable))

	// Principal-scoped API keys.
	mux.HandleFunc("GET /api/v1/principals/{pid}/api-keys", s.requireAuth(s.handleListPrincipalAPIKeys))
	mux.HandleFunc("POST /api/v1/principals/{pid}/api-keys", s.requireAuth(s.handleCreateAPIKey))

	// Principal-scoped OIDC links.
	mux.HandleFunc("GET /api/v1/principals/{pid}/oidc-links", s.requireAuth(s.handleListOIDCLinks))
	mux.HandleFunc("POST /api/v1/principals/{pid}/oidc-links/begin", s.requireAuth(s.handleBeginOIDCLink))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/oidc-links/{provider_id}", s.requireAuth(s.handleUnlinkOIDC))

	// Domains.
	mux.HandleFunc("GET /api/v1/domains", s.requireAuth(s.handleListDomains))
	mux.HandleFunc("POST /api/v1/domains", s.requireAuth(s.handleCreateDomain))
	mux.HandleFunc("DELETE /api/v1/domains/{name}", s.requireAuth(s.handleDeleteDomain))

	// Aliases.
	mux.HandleFunc("GET /api/v1/aliases", s.requireAuth(s.handleListAliases))
	mux.HandleFunc("POST /api/v1/aliases", s.requireAuth(s.handleCreateAlias))
	mux.HandleFunc("DELETE /api/v1/aliases/{id}", s.requireAuth(s.handleDeleteAlias))

	// API keys (flat surface).
	mux.HandleFunc("GET /api/v1/api-keys", s.requireAuth(s.handleListOwnAPIKeys))
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", s.requireAuth(s.handleDeleteAPIKey))

	// OIDC providers.
	mux.HandleFunc("GET /api/v1/oidc/providers", s.requireAuth(s.handleListOIDCProviders))
	mux.HandleFunc("POST /api/v1/oidc/providers", s.requireAuth(s.handleCreateOIDCProvider))
	mux.HandleFunc("DELETE /api/v1/oidc/providers/{id}", s.requireAuth(s.handleDeleteOIDCProvider))

	// Server.
	mux.HandleFunc("GET /api/v1/server/status", s.requireAuth(s.handleServerStatus))
	mux.HandleFunc("GET /api/v1/server/config-check", s.requireAuth(s.handleServerConfigCheck))

	// Audit log.
	mux.HandleFunc("GET /api/v1/audit", s.requireAuth(s.handleAuditLog))

	// Outbound queue.
	mux.HandleFunc("GET /api/v1/queue", s.requireAuth(s.handleListQueue))
	mux.HandleFunc("GET /api/v1/queue/stats", s.requireAuth(s.handleQueueStats))
	mux.HandleFunc("POST /api/v1/queue/flush", s.requireAuth(s.handleQueueFlush))
	mux.HandleFunc("GET /api/v1/queue/{id}", s.requireAuth(s.handleGetQueueItem))
	mux.HandleFunc("POST /api/v1/queue/{id}/retry", s.requireAuth(s.handleRetryQueueItem))
	mux.HandleFunc("POST /api/v1/queue/{id}/hold", s.requireAuth(s.handleHoldQueueItem))
	mux.HandleFunc("POST /api/v1/queue/{id}/release", s.requireAuth(s.handleReleaseQueueItem))
	mux.HandleFunc("DELETE /api/v1/queue/{id}", s.requireAuth(s.handleDeleteQueueItem))

	// ACME certs.
	mux.HandleFunc("GET /api/v1/certs", s.requireAuth(s.handleListACMECerts))
	mux.HandleFunc("GET /api/v1/certs/{hostname}", s.requireAuth(s.handleGetACMECert))
	mux.HandleFunc("POST /api/v1/certs/{hostname}/renew", s.requireAuth(s.handleRenewACMECert))

	// Spam policy.
	mux.HandleFunc("GET /api/v1/spam/policy", s.requireAuth(s.handleGetSpamPolicy))
	mux.HandleFunc("PUT /api/v1/spam/policy", s.requireAuth(s.handlePutSpamPolicy))

	// LLM categorisation: per-principal recategorise + job poll
	// (REQ-FILT-220).
	mux.HandleFunc("POST /api/v1/principals/{pid}/recategorise", s.requireAuth(s.handleRecategorisePrincipal))
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.requireAuth(s.handleGetJob))

	// Webhooks.
	mux.HandleFunc("GET /api/v1/webhooks", s.requireAuth(s.handleListWebhooks))
	mux.HandleFunc("POST /api/v1/webhooks", s.requireAuth(s.handleCreateWebhook))
	mux.HandleFunc("GET /api/v1/webhooks/{id}", s.requireAuth(s.handleGetWebhook))
	mux.HandleFunc("PATCH /api/v1/webhooks/{id}", s.requireAuth(s.handlePatchWebhook))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", s.requireAuth(s.handleDeleteWebhook))

	// OIDC provider extensions (show / update).
	mux.HandleFunc("GET /api/v1/oidc/providers/{id}", s.requireAuth(s.handleGetOIDCProvider))
	mux.HandleFunc("PATCH /api/v1/oidc/providers/{id}", s.requireAuth(s.handlePatchOIDCProvider))

	// Diag (DNS check). Backup/restore/migrate live in a sibling file
	// owned by the parallel agent.
	mux.HandleFunc("GET /api/v1/diag/dns-check/{domain}", s.requireAuth(s.handleDiagDNSCheck))

	// Inbound attachment policy (REQ-FLOW-ATTPOL-01..02).
	mux.HandleFunc("GET /api/v1/mailboxes/{addr}/attachment-policy", s.requireAuth(s.handleGetMailboxAttPol))
	mux.HandleFunc("PUT /api/v1/mailboxes/{addr}/attachment-policy", s.requireAuth(s.handlePutMailboxAttPol))
	mux.HandleFunc("GET /api/v1/domains/{name}/attachment-policy", s.requireAuth(s.handleGetDomainAttPol))
	mux.HandleFunc("PUT /api/v1/domains/{name}/attachment-policy", s.requireAuth(s.handlePutDomainAttPol))
}
