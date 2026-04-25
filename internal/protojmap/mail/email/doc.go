// Package email implements the JMAP Email datatype handlers per
// RFC 8621 §4: Email/get, Email/changes, Email/query, Email/queryChanges,
// Email/set, Email/copy, Email/import, Email/parse.
//
// Email/query routes the text-bearing predicates (text/from/to/cc/bcc/
// subject/body/header) onto the FTS index (storefts / fakestore.FTS) and
// the structured predicates (inMailbox/inMailboxOtherThan/before/after/
// minSize/maxSize/hasKeyword/notKeyword/hasAttachment) onto the typed
// metadata surface (store.Metadata.ListMessages). Mixed predicates
// AND-combine the two result sets. Thread-scoped predicates
// (allInThreadHaveKeyword / someInThreadHaveKeyword /
// noneInThreadHaveKeyword) collapse to per-message keyword predicates
// in v1 because the Thread datatype is not yet wired (the parallel agent
// owns it).
//
// Each *Set / *Import / *Copy mutation increments
// JMAPStateKindEmail via store.Metadata.IncrementJMAPState so the
// per-principal Email state string clients persist as their sync cursor
// stays consistent with the observable mutation.
package email
