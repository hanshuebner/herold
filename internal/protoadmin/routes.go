package protoadmin

import (
	"net/http"
)

// registerRoutes registers every /api/v1/... endpoint on mux. Routes are
// declared flat rather than nested because the Go 1.22 stdlib mux
// honours method + path patterns without a separate Router type.
//
// REQ-AUTH-SCOPE-02: every authenticated route is scope-gated. The
// vast majority of protoadmin's surface requires admin elevation (the
// REST surface is mounted on the admin listener and operators are
// the consumers). The self-service handlers (GET /api/v1/api-keys,
// DELETE /api/v1/api-keys/{id}, POST /api/v1/principals/{pid}/api-keys
// against the caller's own pid, the principals/{self} self-service
// flows) are gated only by requireAuth + requireSelfOrAdmin inside
// the handler — a non-admin end-user with a valid cookie should be able
// to manage their own keys without completing step-up elevation.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	auth1 := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAuth(h) }
	// authAdmin requires authentication + a valid admin elevation record
	// (PrincipalFlagAdmin AND an unexpired entry in session_elevations).
	// API-key callers with ScopeAdmin in their credential are permitted
	// without an elevation record (REQ-AUTH-SCOPE-02, REQ-AUTH-74, issue #79).
	authAdmin := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAuth(s.requireElevation(h)) }

	// Health (unauth).
	mux.HandleFunc("GET /api/v1/healthz/live", s.handleHealthLive)
	mux.HandleFunc("GET /api/v1/healthz/ready", s.handleHealthReady)

	// Bootstrap (unauth, rate-limited per remote).
	mux.HandleFunc("POST /api/v1/bootstrap", s.handleBootstrap)

	// JSON login / logout / whoami / me (REQ-AUTH-SESSION-REST). Login and
	// logout are NOT protected by requireAuth -- they are the auth
	// boundary. whoami and me ARE protected: they return 200 + principal info
	// on a valid session or 401 when there is no session, which is the
	// mechanism the SPAs use to probe session state on page load.
	// POST /api/v1/auth/login  returns cookies + {principal_id, scopes,
	//                                              session_expires_at}.
	// POST /api/v1/auth/logout clears the cookies; accepts cookie or
	// Bearer (Bearer-authenticated callers get a 204 with cookie-clear
	// headers that are harmless since they had no cookie to begin with).
	// GET  /api/v1/auth/whoami returns the calling principal's identity plus
	//                          clientlog metadata (admin SPA, re #58).
	// GET  /api/v1/auth/me     returns {principal_id, email, scopes,
	//                          session_expires_at} for the Suite SPA's
	//                          page-reload session probe (re #58).
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	// Bearer-token grant for native clients (issue #199): unauthenticated
	// credential exchange -- {email, password, totp_code?, device_label?}
	// -- that mints a long-lived "hk_..." Bearer token instead of a
	// cookie. Rate-limited per source IP the same way login is.
	mux.HandleFunc("POST /api/v1/auth/device-token", s.handleIssueDeviceToken)
	// OAuth2 authorization-code + PKCE grant for native clients (issue
	// #199, REQ-AND-AUTH-01/02): the system-browser sign-in surface
	// (GET renders the login form, POST submits it and redirects back
	// with a code) and the RFC 6749 token endpoint. All three are
	// unauthenticated -- they are the auth boundary, like /auth/login.
	mux.HandleFunc("/oauth2/authorize", s.handleOAuthAuthorize)
	mux.HandleFunc("POST /oauth2/token", s.handleOAuthToken)
	// Step-up: TOTP verification that creates a server-side elevation record
	// gating admin endpoints (REQ-AUTH-74, issue #79). Requires a cookie session
	// and CSRF check (auth1 enforces the CSRF gate on POST).
	mux.HandleFunc("POST /api/v1/auth/step-up", auth1(s.handleStepUp))
	mux.HandleFunc("POST /api/v1/auth/logout", auth1(s.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/whoami", auth1(s.handleWhoAmI))
	mux.HandleFunc("GET /api/v1/auth/me", auth1(s.handleAuthMe))
	// Session listing and revocation (REQ-AUTH-77, issue #80). Self-service:
	// principals list and revoke their own sessions. Admin variants on the
	// /api/v1/admin/... namespace (require elevation).
	mux.HandleFunc("GET /api/v1/auth/sessions", auth1(s.handleListSessions))
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{session_id}", auth1(s.handleRevokeSession))
	mux.HandleFunc("GET /api/v1/admin/principals/{pid}/sessions", authAdmin(s.handleAdminListSessions))
	mux.HandleFunc("DELETE /api/v1/admin/principals/{pid}/sessions/{session_id}", authAdmin(s.handleAdminRevokeSession))

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

	// Domain-scoped operator management (REQ-ADM-307, re #145).
	// Super-admin only: listing operators and assigning/revoking managed domains.
	mux.HandleFunc("GET /api/v1/admin/operators", authAdmin(s.handleListOperators))
	mux.HandleFunc("GET /api/v1/principals/{pid}/managed-domains", authAdmin(s.handleListManagedDomains))
	mux.HandleFunc("POST /api/v1/principals/{pid}/managed-domains", authAdmin(s.handleAssignManagedDomain))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/managed-domains/{domain}", authAdmin(s.handleRevokeManagedDomain))

	// Domain-scoped DKIM keys (REQ-ADM-11, REQ-ADM-310, REQ-OPS-60, REQ-OPS-62).
	mux.HandleFunc("POST /api/v1/domains/{name}/dkim", authAdmin(s.handleGenerateDKIMKey))
	mux.HandleFunc("GET /api/v1/domains/{name}/dkim", authAdmin(s.handleListDKIMKeys))
	mux.HandleFunc("DELETE /api/v1/domains/{name}/dkim/{selector}", authAdmin(s.handleDeleteDKIMKey))

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

	// System events ring-buffer (REQ-ADM-304, re #142). Admin-scoped;
	// operator results are filtered to managed domains per REQ-ADM-307.
	mux.HandleFunc("GET /api/v1/admin/system-events", authAdmin(s.handleSystemEvents))

	// Message research: retrospective per-message tracer joining received
	// messages, SMTP system events, and outbound queue history
	// (REQ-ADM-306/307, re #143).
	mux.HandleFunc("GET /api/v1/admin/message-research", authAdmin(s.handleMessageResearch))

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
	// (REQ-FILT-220). Config GET/PUT (REQ-FILT-210..212).
	mux.HandleFunc("POST /api/v1/principals/{pid}/recategorise", auth1(s.handleRecategorisePrincipal))
	mux.HandleFunc("GET /api/v1/jobs/{id}", auth1(s.handleGetJob))
	mux.HandleFunc("GET /api/v1/principals/{pid}/categorisation", authAdmin(s.handleGetCategorisationConfig))
	mux.HandleFunc("PUT /api/v1/principals/{pid}/categorisation", authAdmin(s.handlePutCategorisationConfig))

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

	// Hosted mailing lists, Stage 1 (epic #183, REQ-MLIST-40a). CRUD for
	// the list row (mlist.go) plus roster CRUD and bulk import/export
	// (mlist_members.go). Every handler applies requireAdmin here and
	// then narrows further to the caller's domain/list grant scope
	// (REQ-MLIST-05, internal/authz) inside the handler.
	mux.HandleFunc("GET /api/v1/lists", authAdmin(s.handleListMailingLists))
	mux.HandleFunc("POST /api/v1/lists", authAdmin(s.handleCreateMailingList))
	mux.HandleFunc("GET /api/v1/lists/{id}", authAdmin(s.handleGetMailingList))
	mux.HandleFunc("PATCH /api/v1/lists/{id}", authAdmin(s.handlePatchMailingList))
	mux.HandleFunc("DELETE /api/v1/lists/{id}", authAdmin(s.handleDeleteMailingList))
	mux.HandleFunc("GET /api/v1/lists/{id}/members", authAdmin(s.handleListMailingListMembers))
	mux.HandleFunc("POST /api/v1/lists/{id}/members", authAdmin(s.handleAddMailingListMember))
	mux.HandleFunc("PATCH /api/v1/lists/{id}/members/{mid}", authAdmin(s.handlePatchMailingListMember))
	mux.HandleFunc("DELETE /api/v1/lists/{id}/members/{mid}", authAdmin(s.handleRemoveMailingListMember))
	mux.HandleFunc("POST /api/v1/lists/{id}/members/import", authAdmin(s.handleImportMailingListMembers))
	mux.HandleFunc("GET /api/v1/lists/{id}/members/export", authAdmin(s.handleExportMailingListMembers))
	mux.HandleFunc("GET /api/v1/lists/{id}/members/summary", authAdmin(s.handleMailingListMemberSummary))

	// External SMTP submission per-Identity credentials
	// (REQ-AUTH-EXT-SUBMIT-04). Also registered in RegisterSelfServiceRoutes
	// for the public listener. All four endpoints are gated by requireSelfOnly
	// inside each handler — admins cannot read or write another principal's
	// submission credentials (no impersonation in v1).
	mux.HandleFunc("GET /api/v1/identities/{id}/submission", auth1(s.handleGetSubmission))
	mux.HandleFunc("PUT /api/v1/identities/{id}/submission", auth1(s.handlePutSubmission))
	mux.HandleFunc("DELETE /api/v1/identities/{id}/submission", auth1(s.handleDeleteSubmission))
	// On-demand connection test: relays a self-addressed test message through
	// the configured smart host and returns {ok, detail} (re #113).
	mux.HandleFunc("POST /api/v1/identities/{id}/submission/test", auth1(s.handleTestSubmission))
	mux.HandleFunc("POST /api/v1/identities/{id}/submission/oauth/start", auth1(s.handleOAuthStart))
	// The OAuth callback URL is FIXED (no identity id in the path) so
	// operators register one redirect URI with their OAuth provider (Google /
	// Microsoft perform exact-match validation). The identity id travels in
	// the opaque state token. REQ-MAIL-SUBMIT-02, REQ-AUTH-EXT-SUBMIT-03.
	//
	// Unauthenticated: the browser arrives here via a cross-site top-level
	// redirect from the OAuth provider. Session cookies with SameSite=Strict
	// are not sent on cross-site navigations, so requireAuth would reject
	// the request before any code exchange runs (re #95). Authorization
	// comes solely from the opaque state token (CSPRNG, 128 bits,
	// single-use, 5-min TTL, bound to a specific IdentityID) — the same
	// trust model used by the OIDC callback at POST /api/v1/oidc/callback.
	mux.HandleFunc("GET /api/v1/oauth/external-submission/callback", s.handleOAuthCallback)

	// Identity verification (REQ-IDENT-40..43).
	// The code POST is self-only and CSRF-checked. The link callback
	// is mounted at the root path "/verify-identity" so the URL
	// embedded in the verification email stays short; the handler
	// has no auth gate because the token IS the auth. The same route
	// is also registered in RegisterSelfServiceRoutes so the public
	// listener can serve it; this admin-listener registration exists
	// so the route table is symmetric with the rest of the surface
	// (and so unit tests that attach only the admin listener can
	// exercise the link path without a separate public-mux harness).
	mux.HandleFunc("POST /api/v1/identities/{id}/verify", auth1(s.handleVerifyIdentityCode))
	mux.HandleFunc("POST /api/v1/identities/{id}/verify-request", auth1(s.handleVerifyIdentityRequest))
	mux.HandleFunc("GET /verify-identity", s.handleVerifyIdentityLink)

	// Provider detection for the add-identity wizard (re #92).
	// Side-effect free: performs an MX lookup on the email domain and
	// returns {"provider":"google"|"microsoft"|null}. The wizard uses
	// this to route into the existing external-submission OAuth2 flow
	// instead of the verification-code flow. Registered here AND in
	// RegisterSelfServiceRoutes so both listeners expose it.
	mux.HandleFunc("GET /api/v1/identities/detect-provider", auth1(s.handleDetectProvider))

	// Per-user client-log telemetry opt-out (REQ-OPS-208, REQ-CLOG-06).
	// Self-service: the caller may only modify their own flag (enforced
	// inside the handler by using principalFrom, not a {pid} path param).
	mux.HandleFunc("PUT /api/v1/me/clientlog/telemetry_enabled", auth1(s.handlePutTelemetryEnabled))

	// Self-service client-log readback (re #83). Returns the caller's own
	// recent rows from the authenticated slice only; no admin scope or
	// elevation required. The user_id filter is applied server-side from
	// the authenticated context.
	mux.HandleFunc("GET /api/v1/me/clientlog", auth1(s.handleMeClientLog))

	// Tagged-address dismissals + Convert-to-Sieve (REQ-TAG-50..62, REQ-TAG-90).
	// All four endpoints are self-only (gated by principalFrom inside the
	// handler). They are also registered on the public listener via
	// RegisterSelfServiceRoutes so the suite SPA can reach them.
	mux.HandleFunc("POST /api/v1/tagged-address-dismissals", auth1(s.handleCreateTaggedAddressDismissal))
	mux.HandleFunc("GET /api/v1/tagged-address-dismissals", auth1(s.handleListTaggedAddressDismissals))
	mux.HandleFunc("DELETE /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}", auth1(s.handleDeleteTaggedAddressDismissal))
	mux.HandleFunc("POST /api/v1/tagged-address-filters/{id}/convert-to-sieve", auth1(s.handleConvertTaggedAddressFilterToSieve))

	// IMAP import (REQ-IMAP-IMP-60, REQ-IMAP-IMP-65).
	// Admin-only endpoints (operator surface for managing upstream accounts):
	mux.HandleFunc("GET /api/v1/principals/{pid}/imap-imports", authAdmin(s.handleListIMAPImports))
	mux.HandleFunc("POST /api/v1/principals/{pid}/imap-imports", authAdmin(s.handleCreateIMAPImport))
	mux.HandleFunc("PATCH /api/v1/principals/{pid}/imap-imports/{aid}", authAdmin(s.handlePatchIMAPImport))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/imap-imports/{aid}", authAdmin(s.handleDeleteIMAPImport))
	mux.HandleFunc("GET /api/v1/imap-imports/status", authAdmin(s.handleIMAPImportStatus))
	// Principal-scoped live status: authenticated user sees only their own
	// accounts' worker status (re #138, REQ-IMAP-IMP-65).
	mux.HandleFunc("GET /api/v1/me/imap-imports/status", auth1(s.handleIMAPImportMyStatus))

	// Mailbox ACL administration (REQ-PROTO-33, REQ-AUTH-63). The
	// matching IMAP wire surface lives in internal/protoimap/acl.go;
	// the REST endpoints below are the admin-facing way to grant /
	// inspect / revoke ACL rows without driving an IMAP session.
	mux.HandleFunc("GET /api/v1/principals/{pid}/mailboxes", authAdmin(s.handleListPrincipalMailboxes))
	mux.HandleFunc("GET /api/v1/principals/{pid}/mailboxes/{mailbox}/acl", authAdmin(s.handleGetMailboxACL))
	mux.HandleFunc("PUT /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}", authAdmin(s.handlePutMailboxACL))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}", authAdmin(s.handleDeleteMailboxACL))

	// Inbound attachment policy (REQ-FLOW-ATTPOL-01..02).
	mux.HandleFunc("GET /api/v1/mailboxes/{addr}/attachment-policy", authAdmin(s.handleGetMailboxAttPol))
	mux.HandleFunc("PUT /api/v1/mailboxes/{addr}/attachment-policy", authAdmin(s.handlePutMailboxAttPol))
	mux.HandleFunc("GET /api/v1/domains/{name}/attachment-policy", authAdmin(s.handleGetDomainAttPol))
	mux.HandleFunc("PUT /api/v1/domains/{name}/attachment-policy", authAdmin(s.handlePutDomainAttPol))

	// Spam-classifier feedback signal (Wave 3.15). Self-service: the caller
	// must own the referenced email (enforced inside the handler). Registered
	// here so the full handler set (Handler()) exposes it alongside the
	// self-service set (RegisterSelfServiceRoutes).
	mux.HandleFunc("POST /api/v1/spam-feedback", auth1(s.handleSpamFeedback))

	// Translation proxy (re #84). Operator opt-in: when [translation].enabled
	// is false in system.toml the handler returns HTTP 501 with the stable
	// machine-readable code "translation_not_configured" so the Suite SPA can
	// detect support and suppress the translation UI affordance. No store
	// access — the handler is a stateless proxy to the configured third-party
	// translation API. Self-service: any authenticated principal may call it.
	// Registered here AND in RegisterSelfServiceRoutes so both the admin and
	// public listeners expose it.
	mux.HandleFunc("POST /api/v1/translate", auth1(s.handleTranslate))

	// Client-log ingest (REQ-OPS-200..207, REQ-OPS-215..218).
	// Authenticated endpoint: requires valid session/API-key.
	mux.HandleFunc("POST /api/v1/clientlog", auth1(s.handleClientLogAuth))
	mux.HandleFunc("OPTIONS /api/v1/clientlog", s.handleClientLogPreflight)
	// Anonymous endpoint: no auth. CORS check is done inside the handler.
	mux.HandleFunc("POST /api/v1/clientlog/public", s.handleClientLogPublic)
	mux.HandleFunc("OPTIONS /api/v1/clientlog/public", s.handleClientLogPreflight)

	// Client-log admin REST surfaces (REQ-ADM-23, REQ-ADM-230..233).
	mux.HandleFunc("GET /api/v1/admin/clientlog", authAdmin(s.handleAdminListClientLog))
	mux.HandleFunc("GET /api/v1/admin/clientlog/timeline", authAdmin(s.handleAdminClientLogTimeline))
	mux.HandleFunc("POST /api/v1/admin/clientlog/livetail", authAdmin(s.handleAdminClientLogLivetailSet))
	mux.HandleFunc("DELETE /api/v1/admin/clientlog/livetail/{user_id}", authAdmin(s.handleAdminClientLogLivetailClear))
	mux.HandleFunc("GET /api/v1/admin/clientlog/stats", authAdmin(s.handleAdminClientLogStats))
}

// RegisterSelfServiceRoutes registers the self-service subset of the
// /api/v1/... endpoints on mux. Only routes that a non-admin end-user
// should be able to reach from the public listener are included; all
// admin-only surfaces (queue, certs, domains, aliases, audit, spam policy,
// webhooks, OIDC providers, server status) are deliberately excluded so
// the public-listener REST surface stays minimal.
//
// Each registered route relies on the per-handler requireSelfOrAdmin gate
// already present in the handler implementation — this function does not
// add new authorisation logic. The caller is responsible for mounting the
// returned SelfServiceHandler (or wiring the mux) behind the public
// listener's session cookie + CSRF middleware.
//
// REQ-ADM-203: supports the Suite SPA /settings panel (change password,
// 2FA, app passwords, OIDC identity links, API key management).
func (s *Server) RegisterSelfServiceRoutes(mux *http.ServeMux) {
	auth1 := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAuth(h) }

	// Health (unauth) — useful for public-listener liveness probes.
	mux.HandleFunc("GET /api/v1/healthz/live", s.handleHealthLive)
	mux.HandleFunc("GET /api/v1/healthz/ready", s.handleHealthReady)

	// Session listing and revocation (REQ-AUTH-77, issue #80). Self-service
	// subset only (no admin variants on the public listener).
	mux.HandleFunc("GET /api/v1/auth/sessions", auth1(s.handleListSessions))
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{session_id}", auth1(s.handleRevokeSession))

	// Principal self-service: a non-admin principal may only access their
	// own row; requireSelfOrAdmin inside each handler enforces this.
	mux.HandleFunc("GET /api/v1/principals/{pid}", auth1(s.handleGetPrincipal))
	mux.HandleFunc("PATCH /api/v1/principals/{pid}", auth1(s.handlePatchPrincipal))
	mux.HandleFunc("PUT /api/v1/principals/{pid}/password", auth1(s.handleSetPassword))

	// TOTP enrolment / management.
	mux.HandleFunc("POST /api/v1/principals/{pid}/totp/enroll", auth1(s.handleTOTPEnroll))
	mux.HandleFunc("POST /api/v1/principals/{pid}/totp/confirm", auth1(s.handleTOTPConfirm))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/totp", auth1(s.handleTOTPDisable))

	// API key management (principal-scoped). The flat self-service surface
	// lists and revokes keys belonging to the authenticated caller only.
	// POST creates a new key scoped to the {pid} (gates by self-or-admin).
	mux.HandleFunc("GET /api/v1/api-keys", auth1(s.handleListOwnAPIKeys))
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", auth1(s.handleDeleteAPIKey))
	mux.HandleFunc("POST /api/v1/principals/{pid}/api-keys", auth1(s.handleCreateAPIKey))

	// OIDC identity links (link / unlink an external IdP identity to the
	// principal's account). begin redirects the browser to the IdP;
	// the callback completion is handled on the admin listener (OIDC
	// callback uses admin session state) and is NOT registered here.
	mux.HandleFunc("GET /api/v1/principals/{pid}/oidc-links", auth1(s.handleListOIDCLinks))
	mux.HandleFunc("POST /api/v1/principals/{pid}/oidc-links/begin", auth1(s.handleBeginOIDCLink))
	mux.HandleFunc("DELETE /api/v1/principals/{pid}/oidc-links/{provider_id}", auth1(s.handleUnlinkOIDC))

	// Per-user client-log telemetry opt-out (REQ-OPS-208, REQ-CLOG-06).
	mux.HandleFunc("PUT /api/v1/me/clientlog/telemetry_enabled", auth1(s.handlePutTelemetryEnabled))

	// Self-service client-log readback (re #83). Authenticated slice only;
	// principal-id filter applied server-side from the session context.
	mux.HandleFunc("GET /api/v1/me/clientlog", auth1(s.handleMeClientLog))

	// Spam-classifier feedback signal (Wave 3.15). The Suite SPA's
	// per-message report-spam / report-phishing actions POST here so
	// the operator can surface the signal for tuning. Per-handler
	// ownership check: the caller must own the referenced email.
	mux.HandleFunc("POST /api/v1/spam-feedback", auth1(s.handleSpamFeedback))

	// Translation proxy (re #84). Returns 501 "translation_not_configured"
	// when [translation].enabled is false so the Suite SPA can suppress the
	// translation affordance. Any authenticated principal may call it;
	// registered here so the public listener exposes it alongside the admin
	// listener registration in registerRoutes.
	mux.HandleFunc("POST /api/v1/translate", auth1(s.handleTranslate))

	// External SMTP submission per-Identity credentials
	// (REQ-AUTH-EXT-SUBMIT-04). All four endpoints are scoped to the
	// principal that owns the Identity via requireSelfOnly inside each
	// handler — admins cannot read or write another principal's submission
	// credentials (no impersonation in v1).
	mux.HandleFunc("GET /api/v1/identities/{id}/submission", auth1(s.handleGetSubmission))
	mux.HandleFunc("PUT /api/v1/identities/{id}/submission", auth1(s.handlePutSubmission))
	mux.HandleFunc("DELETE /api/v1/identities/{id}/submission", auth1(s.handleDeleteSubmission))
	// On-demand connection test (re #113).
	mux.HandleFunc("POST /api/v1/identities/{id}/submission/test", auth1(s.handleTestSubmission))

	// Server-mediated OAuth start/callback for external submission
	// (REQ-MAIL-SUBMIT-02, REQ-AUTH-EXT-SUBMIT-03).
	mux.HandleFunc("POST /api/v1/identities/{id}/submission/oauth/start", auth1(s.handleOAuthStart))
	// Fixed callback path — no identity id in URL (identity id in state token).
	// Unauthenticated for the same reason as the admin-listener registration
	// in registerRoutes (re #95): SameSite=Strict cookies are absent on
	// cross-site top-level redirects from the OAuth provider.
	mux.HandleFunc("GET /api/v1/oauth/external-submission/callback", s.handleOAuthCallback)

	// Identity verification (REQ-IDENT-40..43).
	//
	// The code-entry POST is self-only and CSRF-checked: the suite's
	// "have a code" input POSTs a 6-digit code lifted from the
	// verification email body.
	mux.HandleFunc("POST /api/v1/identities/{id}/verify", auth1(s.handleVerifyIdentityCode))
	// User-initiated resend (REQ-IDENT-36): rotates the trio, applies
	// the cooldown / daily-cap rate-limit gate, and enqueues a fresh
	// verification email. Rejected resends surface as 429 with
	// Retry-After.
	mux.HandleFunc("POST /api/v1/identities/{id}/verify-request", auth1(s.handleVerifyIdentityRequest))
	// The link callback is mounted OUTSIDE /api/v1/* so the URL
	// embedded in the verification email stays short. No auth gate:
	// the token IS the auth (CSPRNG-generated, single-use, sha256'd
	// in the store). admin/server.go MUST mount this handler on
	// publicMux at the same path; the /api/v1/* prefix mount does
	// NOT cover it.
	mux.HandleFunc("GET /verify-identity", s.handleVerifyIdentityLink)

	// Provider detection for the add-identity wizard (re #92).
	// Side-effect free: performs an MX lookup on the email domain and
	// returns {"provider":"google"|"microsoft"|null}. The wizard uses
	// this to route into the existing external-submission OAuth2 flow
	// instead of the verification-code flow.
	mux.HandleFunc("GET /api/v1/identities/detect-provider", auth1(s.handleDetectProvider))

	// Client-log ingest from the Suite SPA (public listener), both endpoints
	// (REQ-OPS-200, architecture §Endpoint mounting).
	mux.HandleFunc("POST /api/v1/clientlog", auth1(s.handleClientLogAuth))
	mux.HandleFunc("OPTIONS /api/v1/clientlog", s.handleClientLogPreflight)
	mux.HandleFunc("POST /api/v1/clientlog/public", s.handleClientLogPublic)
	mux.HandleFunc("OPTIONS /api/v1/clientlog/public", s.handleClientLogPreflight)

	// Tagged-address dismissals + Convert-to-Sieve (REQ-TAG-50..62).
	// Self-only by ownership check inside each handler; admin
	// impersonation is not supported (consistent with the JMAP wire
	// surface for TaggedAddressFilter and the rest of the user-state
	// REST surfaces).
	mux.HandleFunc("POST /api/v1/tagged-address-dismissals", auth1(s.handleCreateTaggedAddressDismissal))
	mux.HandleFunc("GET /api/v1/tagged-address-dismissals", auth1(s.handleListTaggedAddressDismissals))
	mux.HandleFunc("DELETE /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}", auth1(s.handleDeleteTaggedAddressDismissal))
	mux.HandleFunc("POST /api/v1/tagged-address-filters/{id}/convert-to-sieve", auth1(s.handleConvertTaggedAddressFilterToSieve))

	// IMAP import principal-scoped live status (re #138, REQ-IMAP-IMP-65).
	// Returns the authenticated caller's own workers; cannot read other
	// principals' status.
	mux.HandleFunc("GET /api/v1/me/imap-imports/status", auth1(s.handleIMAPImportMyStatus))
}

// SelfServiceHandler returns the self-service route set wrapped in the
// same middleware chain as Handler() (concurrency limit, panic recover,
// request logging, metrics). It is intended for mounting on the public
// listener at the specific path prefixes below so the end-user /settings
// panel in the Suite SPA can reach the relevant REST endpoints without
// exposing the full admin surface.
//
// NOTE: as of commit 782fd73 (re #58), admin/server.go mounts Handler()
// — the full unified handler — directly on the public listener at "/".
// SelfServiceHandler is no longer called from production paths. It is
// retained for potential use by embedded deployments or test harnesses
// that want the narrower self-service surface without the full admin
// REST. RegisterSelfServiceRoutes is the underlying registrar; callers
// that need only route registration can use it directly.
//
// Recommended mount points (longest-prefix wins in Go's stdlib mux):
//
//	publicMux.Handle("/api/v1/principals/",            selfServiceHandler)
//	publicMux.Handle("/api/v1/api-keys",               selfServiceHandler)
//	publicMux.Handle("/api/v1/api-keys/",              selfServiceHandler)
//	publicMux.Handle("/api/v1/healthz/",               selfServiceHandler)
//	publicMux.Handle("/api/v1/tagged-address-dismissals", selfServiceHandler)
//	publicMux.Handle("/api/v1/tagged-address-dismissals/", selfServiceHandler)
//	publicMux.Handle("/api/v1/tagged-address-filters/", selfServiceHandler)
func (s *Server) SelfServiceHandler() http.Handler {
	mux := http.NewServeMux()
	s.RegisterSelfServiceRoutes(mux)
	sem := make(chan struct{}, s.opts.MaxConcurrentRequests)
	return s.withConcurrencyLimit(sem,
		s.withPanicRecover(
			s.withRequestLog(
				s.withListenerTag(
					s.withMetrics(mux)))))
}
