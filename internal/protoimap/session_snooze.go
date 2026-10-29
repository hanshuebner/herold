package protoimap

import (
	"context"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

const snoozedKeyword = "$snoozed"

// withoutSnoozedKeyword returns kws minus "$snoozed" (case-insensitive).
// The keyword and the membership's snoozed-until deadline form an
// atomic pair (REQ-PROTO-49); an insert path that cannot supply the
// deadline must not store the keyword either.
func withoutSnoozedKeyword(kws []string) []string {
	var out []string
	for _, k := range kws {
		if strings.EqualFold(k, snoozedKeyword) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// copyTarget builds the destination membership for an IMAP COPY or
// MOVE of m out of srcMailboxID into dest.
//
// COPY creates a new message, so it never carries a reminder. MOVE
// relocates the one membership, so the reminder (keyword, deadline and
// wake destination) travels with it, exactly as the store's
// MoveMessage does; a move into Trash ends the reminder, again as
// MoveMessage does (re #460). Whenever the pair cannot be carried
// whole, both halves are dropped.
func (ses *session) copyTarget(ctx context.Context, m store.Message, srcMailboxID store.MailboxID, dest store.Mailbox, isMove bool) store.MessageMailbox {
	mm := store.MessageMailbox{MailboxID: dest.ID, Flags: m.Flags, Keywords: m.Keywords}
	if !containsCI(m.Keywords, snoozedKeyword) {
		return mm
	}
	mm.Keywords = withoutSnoozedKeyword(m.Keywords)
	if !isMove || dest.Attributes&store.MailboxAttrTrash != 0 {
		return mm
	}
	full, err := ses.s.store.Meta().GetMessage(ctx, m.ID)
	if err != nil {
		return mm
	}
	for _, src := range full.Mailboxes {
		if src.MailboxID != srcMailboxID || src.SnoozedUntil == nil {
			continue
		}
		mm.Keywords = m.Keywords
		mm.SnoozedUntil = src.SnoozedUntil
		mm.WakeMailboxID = src.WakeMailboxID
	}
	return mm
}
