// Package protoadmin implements the admin REST surface under /api/v1/.
// It exposes principal, domain, alias, OIDC, API-key, server-status and
// audit-log endpoints plus a bootstrap path for first-run setup.
//
// Authentication is Bearer-token API keys (phase 1). Session tokens for
// the future browser UI are phase 2; the middleware rejects non-API-key
// bearers today.
//
// Ownership: http-api-implementor.
package protoadmin
