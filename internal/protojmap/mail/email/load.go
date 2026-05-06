package email

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// listAccessibleMailboxes returns the mailboxes owned by pid. Cross-
// account JMAP routing scopes message visibility per-accountId; the
// cross-account branch of each Email/* handler resolves a foreign
// targetPID and calls this with that PID, so we never need to mix
// owned + shared into a single result. See mailbox/render.go for the
// matching mailbox-side comment.
func listAccessibleMailboxes(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.Mailbox, error) {
	owned, err := meta.ListMailboxes(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("email: list mailboxes: %w", err)
	}
	return owned, nil
}

// loadMessageForPrincipal returns the message if it lives in a mailbox
// the principal can access. ErrNotFound is mapped to errMessageMissing
// so the JMAP wire form can render "notFound" without leaking the
// existence of out-of-scope mailboxes.
func loadMessageForPrincipal(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	id store.MessageID,
) (store.Message, error) {
	m, err := meta.GetMessage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Message{}, errMessageMissing
		}
		return store.Message{}, fmt.Errorf("email: get message: %w", err)
	}
	mb, err := meta.GetMailboxByID(ctx, m.MailboxID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Message{}, errMessageMissing
		}
		return store.Message{}, fmt.Errorf("email: get mailbox: %w", err)
	}
	if mb.PrincipalID == pid {
		return m, nil
	}
	rows, err := meta.GetMailboxACL(ctx, mb.ID)
	if err != nil {
		return store.Message{}, fmt.Errorf("email: get mailbox acl: %w", err)
	}
	for _, r := range rows {
		if r.PrincipalID == nil {
			if r.Rights&store.ACLRightLookup != 0 {
				return m, nil
			}
			continue
		}
		if *r.PrincipalID == pid && r.Rights&store.ACLRightLookup != 0 {
			return m, nil
		}
	}
	return store.Message{}, errMessageMissing
}

// errMessageMissing is the unified "looks like never existed" error.
var errMessageMissing = errors.New("email: not found or not visible")

// listPrincipalMessages returns every message in every mailbox the
// principal can see. Primarily used by Email/changes and the
// metadata-fallback Email/query path. The implementation is keyset-
// paged per mailbox so a principal with millions of messages does not
// hold the whole list in memory at once at the storage layer; the
// returned slice is bounded only by the caller.
func listPrincipalMessages(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.Message, error) {
	mailboxes, err := listAccessibleMailboxes(ctx, meta, pid)
	if err != nil {
		return nil, err
	}
	const page = 1000
	var out []store.Message
	for _, mb := range mailboxes {
		var cursor store.UID
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			batch, ferr := meta.ListMessages(ctx, mb.ID, store.MessageFilter{
				AfterUID:     cursor,
				Limit:        page,
				WithEnvelope: true,
			})
			if ferr != nil {
				return nil, fmt.Errorf("email: list messages: %w", ferr)
			}
			out = append(out, batch...)
			if len(batch) < page {
				break
			}
			cursor = batch[len(batch)-1].UID
		}
	}
	return out, nil
}
