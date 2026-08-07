package store

import "strings"

// ResolveInboxMailbox returns the principal's Inbox mailbox from a
// pre-fetched list of mailboxes (as returned by Metadata.ListMailboxes),
// or nil when the principal has none. It mirrors migration 0101's
// snooze-wake-destination backfill: the MailboxAttrInbox bit is
// authoritative, and a case-insensitive name match on "INBOX" is the
// fallback for a mailbox that predates the attribute being set. When
// more than one mailbox matches by attribute (or, absent that, by
// name), the lowest MailboxID wins so resolution is deterministic.
//
// Used by the JMAP snooze intent (issue #274) to default an unspecified
// snoozeWakeMailboxId to the account's Inbox, and by the snooze wake
// worker to resolve the destination for a pre-migration (NULL
// wake_mailbox_id) row.
func ResolveInboxMailbox(mailboxes []Mailbox) *Mailbox {
	var byAttr, byName *Mailbox
	for i := range mailboxes {
		mb := &mailboxes[i]
		if mb.Attributes&MailboxAttrInbox != 0 {
			if byAttr == nil || mb.ID < byAttr.ID {
				byAttr = mb
			}
			continue
		}
		if strings.EqualFold(mb.Name, "INBOX") {
			if byName == nil || mb.ID < byName.ID {
				byName = mb
			}
		}
	}
	if byAttr != nil {
		return byAttr
	}
	return byName
}
