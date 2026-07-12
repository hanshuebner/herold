package store

import "time"

// Resource-grant authorization types (epic #182, REQ-AC-01..05). A grant
// binds a subject (a principal today; the schema's subject_kind column also
// admits 'group' for a later step) to an access level on a typed resource.
// A principal's effective authority is the union of its grants plus the
// structural implicit ownership resolved in internal/authz. Schema commentary
// lives in storesqlite/migrations/0079_grants.sql; the total ordering of
// levels within a kind (REQ-AC-03) lives in internal/authz.

// GrantID identifies a grant row.
type GrantID uint64

// GrantSubjectKind labels what a grant's subject is.
type GrantSubjectKind string

const (
	// GrantSubjectPrincipal is a grant held directly by a principal.
	GrantSubjectPrincipal GrantSubjectKind = "principal"
	// GrantSubjectGroup is a grant held by an authorization group
	// (REQ-AC-80). Not written in Phase A; the value exists so the schema
	// and types admit groups without a further migration.
	GrantSubjectGroup GrantSubjectKind = "group"
	// GrantSubjectAnyone is the RFC 4314 "anyone" pseudo-identifier (epic
	// #210, migration 0084): a mailbox grant row with this subject kind
	// applies to every authenticated principal. SubjectID carries no
	// meaning for this kind (stored as 0). Only ever used with
	// GrantResourceMailbox; introduced to retire the legacy mailbox_acl
	// table's NULL-principal_id row without inventing a numeric sentinel
	// principal id.
	GrantSubjectAnyone GrantSubjectKind = "anyone"
)

// GrantResourceKind is the kind of resource a grant is scoped to.
type GrantResourceKind string

const (
	// GrantResourceServer is the whole node; its resource id is "".
	GrantResourceServer GrantResourceKind = "server"
	// GrantResourceDomain is a hosted domain; its resource id is the domain name.
	GrantResourceDomain GrantResourceKind = "domain"
	// GrantResourceList is a mailing list; its resource id is the list id.
	GrantResourceList GrantResourceKind = "list"
	// GrantResourceMailbox is a mailbox; its resource id is the mailbox id.
	GrantResourceMailbox GrantResourceKind = "mailbox"
)

// GrantLevel is a grant's stored capability value. For server/domain/list
// kinds it is one of a small per-kind, totally ordered set of access tiers
// (REQ-AC-03); the ordering is defined in internal/authz. For the mailbox
// kind (epic #210) it instead carries the full RFC 4314 rights letter-set
// the grant conveys, in the canonical order internal/aclcodec defines (e.g.
// "lrs", "lrswiktea") -- letter-exact, not collapsed to a tier -- so a
// mailbox grant round-trips SETACL/GETACL without fidelity loss.
// GrantLevelRead/Write/Admin remain valid mailbox-kind values too: a
// non-ACL-wire grant (e.g. a future IdP claim-mapping rule targeting a
// mailbox) may still assert one of the three coarse tiers, which
// internal/aclcodec.DecodeGrantLevel expands to the rights it implies. The
// coarse tiers are always a convenience projection, never the sole
// representation, for the mailbox kind.
type GrantLevel string

const (
	// GrantLevelSuperadmin is the only server-kind level (REQ-AC-04).
	GrantLevelSuperadmin GrantLevel = "superadmin"
	// GrantLevelOperator is the lower domain-kind level (REQ-AC-30).
	GrantLevelOperator GrantLevel = "operator"
	// GrantLevelOwner is the higher domain-kind and list-kind level
	// (REQ-AC-31 / REQ-AC-40).
	GrantLevelOwner GrantLevel = "owner"
	// GrantLevelModerator is the lower list-kind level (REQ-AC-41).
	GrantLevelModerator GrantLevel = "moderator"
	// GrantLevelRead is the coarse "read" mailbox tier -- a convenience
	// projection (REQ-AC-50); the mailbox grant's Level column normally
	// carries the full RFC 4314 letter-set instead (see GrantLevel doc).
	GrantLevelRead GrantLevel = "read"
	// GrantLevelWrite is the middle mailbox-kind level; implies read.
	GrantLevelWrite GrantLevel = "write"
	// GrantLevelAdmin is the highest mailbox-kind level; implies write.
	GrantLevelAdmin GrantLevel = "admin"
)

// GrantProvenanceLocal marks an operator-assigned grant. IdP-derived grants
// carry provenance "idp:<provider>" (REQ-AC-61); the two are distinct rows
// for one (subject, resource) so reconciliation of IdP grants never disturbs
// a local grant.
const GrantProvenanceLocal = "local"

// GrantProvenanceACLMigration marks a mailbox grant row produced by the
// one-time migration of the legacy mailbox_acl table into the grants
// substrate (epic #210, migration 0084). Distinct from GrantProvenanceLocal
// so a migrated row is identifiable in an audit or reconciliation pass; a
// subsequent SETACL/admin-REST write on the same (mailbox, grantee) row
// normalises its provenance to GrantProvenanceLocal (an explicit re-set
// supersedes the historical migration tag -- see SetMailboxACL).
const GrantProvenanceACLMigration = "acl-migration"

// Grant is one authorization grant row.
type Grant struct {
	ID           GrantID
	SubjectKind  GrantSubjectKind
	SubjectID    uint64
	ResourceKind GrantResourceKind
	ResourceID   string
	Level        GrantLevel
	// Provenance is GrantProvenanceLocal or "idp:<provider>".
	Provenance string
	// GrantedBy is the acting principal for a local grant, or nil for a
	// migration-derived or IdP-derived grant.
	GrantedBy *PrincipalID
	// GrantedAt is set by the store from its clock on insert.
	GrantedAt time.Time
	// LastAssertedAt is set only on idp:<provider> grants and drives the
	// staleness sweep (#188); nil for local grants.
	LastAssertedAt *time.Time
}
