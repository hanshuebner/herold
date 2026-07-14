// Package authz is herold's authorization layer: the single seam that answers
// "may this principal act on this resource at this level" for every protocol
// surface (SMTP, IMAP, JMAP, admin REST, mailing lists). It sits behind the
// authentication and scope/elevation gate and consults the resource-grant
// model (epic #182, docs/design/server/requirements/07-access-control.md,
// REQ-AC-01..05).
//
// Resolve computes a principal's effective level on one resource as the max
// over the superadmin short-circuit and explicit grant rows. Resolution is
// default-deny and fail-closed: a store error yields a deny, never an allow
// (REQ-AC-12). Callers compare the returned level against the level the
// operation requires with AtLeast.
//
// Phase A resolves the server and domain resource kinds. Explicit grant rows
// are honoured for every kind; the structural branches for mailbox (implicit
// admin on own mailboxes, #186) and domain->list containment land with the
// dependent epics. IdP claim reconciliation (#188) adds ReconcileIdP here
// without changing Resolve.
package authz
