package protoimap

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// listAllPageSize is the per-page row count used by listAllMessages. It
// matches the store's hard-coded ceiling (see internal/store/types.go
// MessageFilter docs); a full page is therefore the signal that another
// page may exist.
const listAllPageSize = 1000

// listAllMessages reads every message in the mailbox by paginating the
// store's MessageFilter.AfterUID + Limit cursor. The store caps a single
// read at 1000 rows; this helper accumulates pages until the backend
// returns fewer than listAllPageSize rows.
//
// Without this, IMAP SELECT/FETCH/STATUS/LIST-STATUS truncate to the first
// 1000 messages on mailboxes that exceed that bound, breaking REQ-NFR-01
// large-mailbox support (re #99).
func listAllMessages(ctx context.Context, meta store.Metadata, mailboxID store.MailboxID, withEnvelope bool) ([]store.Message, error) {
	var (
		all  []store.Message
		last store.UID
	)
	for {
		page, err := meta.ListMessages(ctx, mailboxID, store.MessageFilter{
			WithEnvelope: withEnvelope,
			AfterUID:     last,
			Limit:        listAllPageSize,
		})
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		if len(page) < listAllPageSize {
			return all, nil
		}
		// Page was full -> there may be more. Advance the cursor by the
		// largest UID we just saw. The store returns rows ordered by UID
		// ascending, so the last element holds the max.
		last = page[len(page)-1].UID
	}
}
