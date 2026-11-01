package email

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hanshuebner/herold/internal/store"
)

// snoozeHolderMembership returns the membership of m that holds the
// reminder (the lowest mailbox id when several do, which only legacy rows
// can produce), or false when none does.
func snoozeHolderMembership(m store.Message) (store.MessageMailbox, bool) {
	var best store.MessageMailbox
	found := false
	for _, mm := range m.Mailboxes {
		if mm.SnoozedUntil == nil {
			continue
		}
		if !found || mm.MailboxID < best.MailboxID {
			best, found = mm, true
		}
	}
	return best, found
}

// applySnoozeHolder makes the mailbox-independent snooze properties of m
// (SnoozedUntil and the "$snoozed" keyword) reflect the membership that
// holds the reminder. The convenience fields otherwise come from the
// lowest-id membership, which is a Trash row when Trash was provisioned
// before the label the reminder lives on; reading it would report the
// message as not snoozed while its reminder still stands.
func applySnoozeHolder(m *store.Message) {
	holder, ok := snoozeHolderMembership(*m)
	if !ok || m.SnoozedUntil != nil {
		return
	}
	m.SnoozedUntil = holder.SnoozedUntil
	if !slices.Contains(m.Keywords, "$snoozed") {
		m.Keywords = append(slices.Clone(m.Keywords), "$snoozed")
	}
}

// snoozeTargetMembership returns the membership a new reminder for m goes
// on: the lowest-id membership whose mailbox is not Trash, the same rule
// the store applies when it hands a reminder over or consolidates it. ok is
// false when every membership is in Trash.
func snoozeTargetMembership(ctx context.Context, meta store.Metadata, m store.Message) (store.MailboxID, bool, error) {
	if len(m.Mailboxes) == 0 {
		return m.MailboxID, true, nil
	}
	var target store.MailboxID
	found := false
	for _, mm := range m.Mailboxes {
		if found && mm.MailboxID > target {
			continue
		}
		mb, err := meta.GetMailboxByID(ctx, mm.MailboxID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return 0, false, fmt.Errorf("email: get mailbox: %w", err)
		}
		if mb.Attributes&store.MailboxAttrTrash != 0 {
			continue
		}
		target, found = mm.MailboxID, true
	}
	return target, found, nil
}
