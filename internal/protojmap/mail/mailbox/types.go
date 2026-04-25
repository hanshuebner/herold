package mailbox

import (
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2: opaque string of
// printable ASCII, length 1..255). Mailbox ids are stringified
// MailboxID; clients echo them back unchanged on subsequent calls.
type jmapID = string

// mailboxIDFromJMAP parses a wire-form id into a MailboxID. Empty
// strings and unparseable values return (0, false); callers translate
// that to a "notFound" SetError per RFC 8621 §2.5.
func mailboxIDFromJMAP(id jmapID) (store.MailboxID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.MailboxID(v), true
}

// jmapIDFromMailbox renders a MailboxID into the wire id form.
func jmapIDFromMailbox(id store.MailboxID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// myRights is the JMAP per-mailbox capability mask returned in
// Mailbox/get (RFC 8621 §2.1.1). Each field maps to one IMAP RFC 4314
// right plus a thin layer of derived flags.
type myRights struct {
	MayReadItems   bool `json:"mayReadItems"`
	MayAddItems    bool `json:"mayAddItems"`
	MayRemoveItems bool `json:"mayRemoveItems"`
	MaySetSeen     bool `json:"maySetSeen"`
	MaySetKeywords bool `json:"maySetKeywords"`
	MayCreateChild bool `json:"mayCreateChild"`
	MayRename      bool `json:"mayRename"`
	MayDelete      bool `json:"mayDelete"`
	MaySubmit      bool `json:"maySubmit"`
}

// rightsForOwner is the right mask the owning principal sees on its own
// mailboxes — every JMAP mutation is permitted.
func rightsForOwner() myRights {
	return myRights{
		MayReadItems:   true,
		MayAddItems:    true,
		MayRemoveItems: true,
		MaySetSeen:     true,
		MaySetKeywords: true,
		MayCreateChild: true,
		MayRename:      true,
		MayDelete:      true,
		MaySubmit:      true,
	}
}

// rightsFromACL maps an RFC 4314 rights mask onto the JMAP myRights
// envelope. Used when the principal is not the mailbox owner; an
// "anyone" ACL row is folded in by the caller before this call.
func rightsFromACL(r store.ACLRights) myRights {
	return myRights{
		MayReadItems:   r&store.ACLRightRead != 0,
		MayAddItems:    r&store.ACLRightInsert != 0,
		MayRemoveItems: r&(store.ACLRightDeleteMessage|store.ACLRightExpunge) != 0,
		MaySetSeen:     r&store.ACLRightSeen != 0,
		MaySetKeywords: r&store.ACLRightWrite != 0,
		MayCreateChild: r&store.ACLRightCreateMailbox != 0,
		MayRename:      r&store.ACLRightWrite != 0,
		MayDelete:      r&store.ACLRightDeleteMailbox != 0,
		MaySubmit:      r&store.ACLRightPost != 0,
	}
}

// jmapMailbox is the wire-form Mailbox object (RFC 8621 §2.1) plus the
// "color" extension property defined by REQ-PROTO-56 / REQ-STORE-34. The
// color is null when unset; clients render their own default.
type jmapMailbox struct {
	ID            jmapID   `json:"id"`
	Name          string   `json:"name"`
	ParentID      *jmapID  `json:"parentId"`
	Role          *string  `json:"role"`
	SortOrder     uint32   `json:"sortOrder"`
	TotalEmails   int64    `json:"totalEmails"`
	UnreadEmails  int64    `json:"unreadEmails"`
	TotalThreads  int64    `json:"totalThreads"`
	UnreadThreads int64    `json:"unreadThreads"`
	MyRights      myRights `json:"myRights"`
	IsSubscribed  bool     `json:"isSubscribed"`
	Color         *string  `json:"color"`
}

// roleFromAttributes maps the SPECIAL-USE attribute bits to the JMAP
// role string per RFC 8621 §2.1. Returns nil when no role applies.
func roleFromAttributes(attrs store.MailboxAttributes) *string {
	switch {
	case attrs&store.MailboxAttrInbox != 0:
		s := "inbox"
		return &s
	case attrs&store.MailboxAttrSent != 0:
		s := "sent"
		return &s
	case attrs&store.MailboxAttrDrafts != 0:
		s := "drafts"
		return &s
	case attrs&store.MailboxAttrTrash != 0:
		s := "trash"
		return &s
	case attrs&store.MailboxAttrJunk != 0:
		s := "junk"
		return &s
	case attrs&store.MailboxAttrArchive != 0:
		s := "archive"
		return &s
	case attrs&store.MailboxAttrFlagged != 0:
		s := "flagged"
		return &s
	}
	return nil
}

// attributesFromRole inverts roleFromAttributes for the create / update
// path. An empty role string produces zero attributes; an unknown role
// returns ok=false so the caller can emit "invalidProperties" per
// RFC 8621 §2.5.
func attributesFromRole(role string) (store.MailboxAttributes, bool) {
	switch role {
	case "":
		return 0, true
	case "inbox":
		return store.MailboxAttrInbox, true
	case "sent":
		return store.MailboxAttrSent, true
	case "drafts":
		return store.MailboxAttrDrafts, true
	case "trash":
		return store.MailboxAttrTrash, true
	case "junk":
		return store.MailboxAttrJunk, true
	case "archive":
		return store.MailboxAttrArchive, true
	case "flagged":
		return store.MailboxAttrFlagged, true
	}
	return 0, false
}
