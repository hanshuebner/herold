package protoimap

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// MailboxRepo is the narrow view of the metadata layer that protoimap
// depends on. It lists the per-mailbox operations we need beyond the
// Phase 1 store.Metadata interface (GetMailboxByName, ListMessages,
// SetMailboxSubscribed, RenameMailbox). Wave 3 is expected to reconcile
// this seam by promoting the methods onto store.Metadata once all backends
// (sqlite, postgres) implement them; until then the test harness wires a
// fakestore-specific adapter that satisfies the interface.
//
// TODO(wave-3): fold into store.Metadata.
type MailboxRepo interface {
	// GetMailboxByName returns the mailbox with the given name owned by
	// principalID. Returns store.ErrNotFound when no such mailbox exists.
	GetMailboxByName(ctx context.Context, principalID store.PrincipalID, name string) (store.Mailbox, error)
	// ListMessages returns every message row in the mailbox ordered by UID
	// ascending. The slice is safe for the caller to mutate; it is a copy
	// of the store's internal state.
	ListMessages(ctx context.Context, mailboxID store.MailboxID) ([]store.Message, error)
	// SetMailboxSubscribed toggles the MailboxAttrSubscribed bit on the
	// mailbox atomically. Returns store.ErrNotFound if the mailbox is gone.
	SetMailboxSubscribed(ctx context.Context, mailboxID store.MailboxID, subscribed bool) error
	// RenameMailbox changes the Name of a mailbox. Returns
	// store.ErrNotFound if the mailbox is missing and store.ErrConflict if
	// the new name collides with an existing mailbox for the same principal.
	RenameMailbox(ctx context.Context, mailboxID store.MailboxID, newName string) error
}
