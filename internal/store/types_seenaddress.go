package store

import "time"

// This file declares the seen-addresses history entity (REQ-MAIL-11e..m).
// The seen-addresses history is a per-principal sliding window of recently-used
// email addresses. It supplements JMAP Contacts as an autocomplete source for
// addresses the user has corresponded with but not saved as a contact.

// SeenAddressID uniquely identifies a row in the seen_addresses table.
type SeenAddressID uint64

// SeenAddress is one entry in the per-principal seen-addresses history
// (REQ-MAIL-11e..m). The window is server-enforced at 500 entries per
// principal; on insert when the cap would be exceeded the oldest-by-LastUsedAt
// row is evicted in the same transaction.
type SeenAddress struct {
	// ID is the assigned primary key.
	ID SeenAddressID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Email is the lower-cased canonical email address. Unique per principal.
	Email string
	// DisplayName is the most recently observed display name from a parsed
	// address header. May be empty.
	DisplayName string
	// FirstSeenAt is the timestamp of the first upsert for this (principal, email)
	// pair.
	FirstSeenAt time.Time
	// LastUsedAt is the timestamp of the most recent upsert. Used as the eviction
	// sort key when the 500-entry cap is reached.
	LastUsedAt time.Time
	// SendCount is the number of times this address was an outbound recipient
	// (seed-on-send path).
	SendCount int64
	// ReceivedCount is the number of times this address was the From: on an
	// inbound message (seed-on-receive path).
	ReceivedCount int64
}
