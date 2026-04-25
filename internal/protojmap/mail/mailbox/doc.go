// Package mailbox implements the JMAP Mailbox datatype handlers per
// RFC 8621 §2: Mailbox/get, Mailbox/changes, Mailbox/query,
// Mailbox/queryChanges, Mailbox/set.
//
// Handlers register against the shared protojmap.CapabilityRegistry via
// Register. Each *Set mutation wraps the store transaction with an
// IncrementJMAPState(JMAPStateKindMailbox) call so the JMAP state
// string clients persist as their sync cursor stays consistent with the
// observable mutation.
//
// The package depends on the store.Metadata surface only for typed
// reads/writes; mailbox ACL handling reads MailboxACL rows to derive
// the per-call myRights mask. v1 carries one Account per Principal so
// the JMAP accountId and the principal id collapse to the same value.
package mailbox
