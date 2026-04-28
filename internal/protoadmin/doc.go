// Package protoadmin implements the admin REST surface served under
// /api/v1/ per docs/design/server/requirements/08-admin-and-management.md. It covers
// the JSON-in/JSON-out shape (REQ-ADM-01..06), bearer-token API-key
// authentication (REQ-ADM-03), the principal/domain/alias/OIDC/API-key
// CRUD endpoints (REQ-ADM-10..11), the audit-log read endpoint
// (REQ-ADM-19), the server config / reload / health / stats surface
// (REQ-ADM-20..22), the canonical problem-details error envelope
// (REQ-ADM-30..31), cursor-based list pagination (REQ-ADM-40..41),
// per-key rate-limiting (REQ-ADM-50), and the audit-record emission
// contract (REQ-ADM-300..303). Session-cookie auth for the admin SPA
// (REQ-ADM-200) is provided by Options.Session; the same mechanism
// drives the end-user self-service surface on the public listener.
//
// Public-listener self-service (REQ-ADM-203): RegisterSelfServiceRoutes
// mounts only the non-admin subset of /api/v1/ routes on a caller-
// supplied mux. SelfServiceHandler wraps that mux in the same middleware
// chain as Handler() (concurrency limit, panic recover, request log,
// metrics). The caller constructs a second Server configured with the
// public-listener session cookie (publicCookieCfg) and mounts
// SelfServiceHandler() at the specific path prefixes on the public mux.
//
// Ownership: http-api-implementor.
package protoadmin
