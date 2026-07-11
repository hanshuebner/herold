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

// GrantLevel is an access tier. Tiers are per-kind and totally ordered
// (REQ-AC-03); the ordering is defined in internal/authz.
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
	// GrantLevelRead is the lowest mailbox-kind level (REQ-AC-50).
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
