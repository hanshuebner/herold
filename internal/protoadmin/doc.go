// Package protoadmin implements the admin REST surface served under
// /api/v1/ per docs/requirements/08-admin-and-management.md. It covers
// the JSON-in/JSON-out shape (REQ-ADM-01..06), bearer-token API-key
// authentication (REQ-ADM-03), the principal/domain/alias/OIDC/API-key
// CRUD endpoints (REQ-ADM-10..11), the audit-log read endpoint
// (REQ-ADM-19), the server config / reload / health / stats surface
// (REQ-ADM-20..22), the canonical problem-details error envelope
// (REQ-ADM-30..31), cursor-based list pagination (REQ-ADM-40..41),
// per-key rate-limiting (REQ-ADM-50), and the audit-record emission
// contract (REQ-ADM-300..303). Session tokens for the future browser
// UI (REQ-ADM-200) are deferred to Phase 2; the middleware rejects
// non-API-key bearers today.
//
// Ownership: http-api-implementor.
package protoadmin
