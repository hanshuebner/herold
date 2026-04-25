// Package identity implements the JMAP Identity datatype handlers per
// RFC 8621 §7: Identity/get, Identity/changes, Identity/set.
//
// An Identity is a sender persona — a (display name, email, replyTo,
// signature, bcc) bundle a client picks when composing a new mail. v1
// derives one default Identity per Principal from the principal's
// CanonicalEmail; clients may add or override identities via
// Identity/set. Phase-1 stored none of this; Wave 2.2 ships the JMAP
// surface and an in-process overlay store. Persistent identity rows are
// scheduled for a follow-up storage extension; the in-process overlay
// loses overrides on restart but keeps the JMAP API surface stable for
// clients.
//
// All handlers extract the authenticated principal via
// protojmap.PrincipalFromContext and scope reads/writes to that
// principal's account. Identity/set rejects creates whose email is not
// hosted by a domain we serve (RFC 8621 §7.5: "forbiddenFrom").
package identity
