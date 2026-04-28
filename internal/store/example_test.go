package store_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// ExampleStore shows the expected usage pattern for the Store contract
// Herold subsystems consume. The illustration uses a nil Store handle
// purely for the shape; real code obtains one from a backend package
// (e.g. storesqlite.Open or storepg.Open) — those land in Wave 1.
func ExampleStore() {
	var s store.Store // obtained from a backend package in real code
	ctx := context.Background()

	deliver := func(principalEmail string, mailboxID store.MailboxID, body string) error {
		if s == nil {
			return nil // example-only guard; real callers always have a Store
		}

		// Resolve the recipient.
		p, err := s.Meta().GetPrincipalByEmail(ctx, principalEmail)
		if err != nil {
			return fmt.Errorf("resolve principal: %w", err)
		}

		// Content-address the message body.
		ref, err := s.Blobs().Put(ctx, strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("put blob: %w", err)
		}

		// Insert the mailbox row; InsertMessage also bumps UIDNext,
		// HighestModSeq, and appends a StateChange atomically.
		msg := store.Message{
			PrincipalID: p.ID,
			Size:        ref.Size,
			Blob:        ref,
		}
		_, _, err = s.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mailboxID}})
		if err != nil {
			return fmt.Errorf("insert message for %d: %w", p.ID, err)
		}
		return nil
	}

	_ = deliver("alice@example.com", 1, "From: bob\r\n\r\nhi")
	// Output:
}
