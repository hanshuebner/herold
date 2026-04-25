package store

import "time"

// This file declares the Phase-2 Wave 2.6 entities backing JMAP for
// Contacts (REQ-PROTO-55, RFC 9553 JSContact + the JMAP-Contacts binding
// draft). The schema-side comments live in
// internal/storesqlite/migrations/0010_contacts.sql; this file is the
// Go-side companion.
//
// Storage strategy. RFC 9553 specifies a deeply-nested object model (the
// JSContact object) that we persist verbatim as JSON in the
// contacts.jscontact_json BLOB. A small set of denormalised columns
// (DisplayName, GivenName, Surname, OrgName, PrimaryEmail, SearchBlob)
// carries the values JMAP queries filter and sort on so the read path
// does not have to JSON-parse every row. The JMAP serializer (the
// internal/protojmap agent) is the sole producer of those columns: it
// parses the JSContact, derives the denormalised fields, and hands the
// store a fully-populated Contact. New RFC 9553 fields land additively
// in the JSON blob; only when a field needs to be filterable does it
// earn a column.

// AddressBookID identifies one row in the address_books table.
type AddressBookID uint64

// AddressBook is one JMAP AddressBook owned by a principal. Mirrors the
// container shape of Mailbox: per-principal, with name + colour +
// is_subscribed + RFC 4314-style rights mask. RightsMask reuses the
// existing ACLRights bitfield; the JMAP-Contacts myRights vocabulary
// maps cleanly onto Lookup/Read/Write/Insert/DeleteMessage/Admin for v1
// — the JSContact-specific extras (mayReadFreeBusy, …) come later, when
// REQ-PROTO-55 deems them necessary.
type AddressBook struct {
	// ID is the assigned primary key.
	ID AddressBookID
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// Name is the display name.
	Name string
	// Description is an optional free-text description; empty when the
	// caller did not provide one.
	Description string
	// Color is the optional "#RRGGBB" colour hex, mirroring Mailbox.Color.
	// nil means "unset"; clients render their own default.
	Color *string
	// SortOrder is the JMAP sort hint (smaller sorts first); 0 by
	// default.
	SortOrder int
	// IsSubscribed mirrors the IMAP subscribed bit / JMAP isSubscribed.
	// True on insert by default.
	IsSubscribed bool
	// IsDefault marks at most one default address book per principal.
	// The store fences "at most one" with a partial unique index; the
	// metadata layer auto-flips the previous default when a new row
	// arrives with IsDefault = true.
	IsDefault bool
	// RightsMask packs the JMAP myRights flags for v1 using the existing
	// ACLRights vocabulary. 0 means "no extra rights beyond ownership".
	RightsMask ACLRights
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
	// ModSeq is the per-row monotonic counter used for /changes
	// pagination at the JMAP layer (orthogonal to the per-principal
	// state-change feed).
	ModSeq ModSeq
}

// AddressBookFilter narrows a ListAddressBooks read.
type AddressBookFilter struct {
	// PrincipalID, when non-nil, restricts to address books owned by
	// that principal.
	PrincipalID *PrincipalID
	// AfterModSeq, when non-zero, returns rows whose ModSeq > the
	// supplied value (used by AddressBook/changes).
	AfterModSeq ModSeq
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID > AfterID. Zero
	// starts at the first row.
	AfterID AddressBookID
}

// ContactID identifies one row in the contacts table.
type ContactID uint64

// Contact is one JSContact object owned by an address book. The full
// RFC 9553 shape lives in JSContactJSON; the denormalised columns the
// store filters and sorts on are populated by the JMAP serializer on
// every Insert / Update.
type Contact struct {
	// ID is the assigned primary key.
	ID ContactID
	// AddressBookID is the containing address book.
	AddressBookID AddressBookID
	// PrincipalID is the owning principal (denormalised so /query can
	// filter without joining address_books).
	PrincipalID PrincipalID
	// UID is the RFC 9553 uid: client-supplied or server-minted; unique
	// per address book.
	UID string
	// JSContactJSON is the full JSContact object serialised as JSON.
	// The store treats the bytes as opaque; the protojmap serializer
	// parses on read and re-serialises on write.
	JSContactJSON []byte
	// DisplayName is the denormalised name shown in list / sort views.
	// Populated by the serializer from the JSContact; never edited
	// directly by the store.
	DisplayName string
	// GivenName / Surname / OrgName are the JSContact name + org
	// fragments split for filter / sort.
	GivenName string
	Surname   string
	OrgName   string
	// PrimaryEmail is the marked-primary email from the JSContact, if
	// any. Empty when the JSContact carries no email entries.
	PrimaryEmail string
	// SearchBlob is the pre-flattened lower-cased concatenation of
	// names, organisations, emails, and phone numbers used by the
	// substring-text filter. The serializer constructs it on write.
	SearchBlob string
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
	// ModSeq is the per-row monotonic counter used by Contact/changes.
	ModSeq ModSeq
}

// ContactFilter narrows a ListContacts read. Zero values mean
// "no constraint". Limit caps at 1000 server-side.
type ContactFilter struct {
	// AddressBookID, when non-nil, restricts to one address book.
	AddressBookID *AddressBookID
	// PrincipalID, when non-nil, restricts to one principal (useful
	// when the caller wants every contact across all books they own).
	PrincipalID *PrincipalID
	// Text is a case-insensitive substring matched against SearchBlob.
	Text string
	// HasEmail, when non-nil, restricts to contacts whose JSContact
	// carries a matching email entry. The store uses PrimaryEmail or
	// the JSON blob to resolve the predicate; case-insensitive.
	HasEmail *string
	// UID, when non-nil, restricts to a specific uid (rarely used
	// directly; AddressBookID + UID is the natural key).
	UID *string
	// AfterModSeq, when non-zero, returns rows whose ModSeq > the
	// supplied value (used by Contact/changes).
	AfterModSeq ModSeq
	// Limit caps the result set (default 1000, max 1000).
	Limit int
	// AfterID is the keyset cursor; rows whose ID > AfterID.
	AfterID ContactID
}
