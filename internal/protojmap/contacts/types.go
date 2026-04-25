package contacts

import (
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2: 1..255 printable
// ASCII). AddressBook and Contact ids are stringified store ids;
// clients echo them back unchanged on subsequent calls.
type jmapID = string

// addressBookIDFromJMAP parses a wire-form id into a store.AddressBookID.
// Empty / unparseable / zero values return (0, false); callers translate
// to a "notFound" SetError per the JMAP-Contacts binding draft.
func addressBookIDFromJMAP(id jmapID) (store.AddressBookID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.AddressBookID(v), true
}

// jmapIDFromAddressBook renders an AddressBookID as the wire id form.
func jmapIDFromAddressBook(id store.AddressBookID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// contactIDFromJMAP parses a wire-form id into a store.ContactID.
func contactIDFromJMAP(id jmapID) (store.ContactID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.ContactID(v), true
}

// jmapIDFromContact renders a ContactID as the wire id form.
func jmapIDFromContact(id store.ContactID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// abMyRights is the per-AddressBook capability mask returned in
// AddressBook/get (per the JMAP-Contacts binding draft, mirroring the
// JMAP Calendars myRights envelope).
type abMyRights struct {
	MayRead   bool `json:"mayRead"`
	MayWrite  bool `json:"mayWrite"`
	MayAdmin  bool `json:"mayAdmin"`
	MayDelete bool `json:"mayDelete"`
}

// rightsForAddressBookOwner is the mask the owning principal sees on
// its own address books — every JMAP mutation is permitted.
func rightsForAddressBookOwner() abMyRights {
	return abMyRights{
		MayRead:   true,
		MayWrite:  true,
		MayAdmin:  true,
		MayDelete: true,
	}
}

// jmapAddressBook is the wire-form AddressBook object per the
// JMAP-Contacts binding draft.
type jmapAddressBook struct {
	ID           jmapID     `json:"id"`
	Name         string     `json:"name"`
	Description  *string    `json:"description"`
	SortOrder    int        `json:"sortOrder"`
	IsSubscribed bool       `json:"isSubscribed"`
	IsDefault    bool       `json:"isDefault"`
	MyRights     abMyRights `json:"myRights"`
	Color        *string    `json:"color"`
}
