package admin

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// devAppThreadSubject is the subject of the thread seeded for the fake
// app (REQ-APP-71).
const devAppThreadSubject = "Old computers to give away"

// devAppThreadMessageIDs are the Message-IDs of the two seeded
// messages; the first one's presence makes the command a no-op.
var devAppThreadMessageIDs = [2]string{"fakeapp-thread-1@example.local", "fakeapp-thread-2@example.local"}

// newDevSeedAppThreadCmd seeds a two-message thread into a principal's
// INBOX for exercising app actions against the fake app (REQ-APP-71).
func newDevSeedAppThreadCmd() *cobra.Command {
	var principalEmail string
	c := &cobra.Command{
		Use:   "seed-app-thread",
		Short: "seed a two-message thread for app-action testing (not for production)",
		Long: "Inserts a donation-offer style two-message thread (\"" + devAppThreadSubject + "\")\n" +
			"into the named principal's INBOX, unless it is already there. Opens\n" +
			"the store directly; the principal must already exist.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDevSeedAppThread(cmd, principalEmail)
		},
	}
	c.Flags().StringVar(&principalEmail, "principal", "alice@example.local",
		"email of the principal that will own the seeded thread")
	return c
}

func runDevSeedAppThread(cmd *cobra.Command, principalEmail string) error {
	g := globals(cmd.Context())
	cfg, err := requireConfig(g)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		return fmt.Errorf("dev seed-app-thread: open store: %w", err)
	}
	defer st.Close()

	principal, err := st.Meta().GetPrincipalByEmail(ctx, principalEmail)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("dev seed-app-thread: principal %q not found; create it first", principalEmail)
		}
		return fmt.Errorf("dev seed-app-thread: lookup principal: %w", err)
	}
	boxes, err := st.Meta().ListMailboxes(ctx, principal.ID)
	if err != nil {
		return fmt.Errorf("dev seed-app-thread: list mailboxes: %w", err)
	}
	var inboxID store.MailboxID
	for _, mb := range boxes {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			inboxID = mb.ID
			break
		}
	}
	if inboxID == 0 {
		return fmt.Errorf("dev seed-app-thread: principal %q has no INBOX", principalEmail)
	}
	findSeed := func() (store.Message, bool, error) {
		msgs, err := st.Meta().ListMessages(ctx, inboxID, store.MessageFilter{Limit: 1000})
		if err != nil {
			return store.Message{}, false, err
		}
		for _, m := range msgs {
			if m.Envelope.MessageID == devAppThreadMessageIDs[0] {
				return m, true, nil
			}
		}
		return store.Message{}, false, nil
	}
	if _, ok, err := findSeed(); err != nil {
		return fmt.Errorf("dev seed-app-thread: list messages: %w", err)
	} else if ok {
		fmt.Fprintf(cmd.OutOrStdout(), "dev-seed: app thread %q already present for %s\n", devAppThreadSubject, principalEmail)
		return nil
	}

	donor := "Dora Donor <dora@donor.example>"
	start := clk.Now().Add(-2 * time.Hour)
	bodies := [2]string{
		"Hello,\r\n\r\nwe are clearing out our office and have two old computers\r\n" +
			"(an Atari ST and a Commodore 128) to give away. Would your club\r\n" +
			"be interested?\r\n\r\nDora\r\n",
		"Hello Dora,\r\n\r\nyes, we would love to have them. Could you send us your\r\n" +
			"address and a time that suits you for the pickup?\r\n\r\nAlice\r\n",
	}
	var threadKey uint64
	for i := 0; i < 2; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		from, to, subject := donor, principal.CanonicalEmail, devAppThreadSubject
		refs := ""
		if i == 1 {
			from, to, subject = principal.CanonicalEmail, donor, "Re: "+devAppThreadSubject
			refs = "In-Reply-To: <" + devAppThreadMessageIDs[0] + ">\r\nReferences: <" + devAppThreadMessageIDs[0] + ">\r\n"
		}
		raw := "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\n" +
			"Date: " + at.Format(time.RFC1123Z) + "\r\nMessage-ID: <" + devAppThreadMessageIDs[i] + ">\r\n" + refs +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" + bodies[i]
		ref, err := st.Blobs().Put(ctx, bytes.NewReader([]byte(raw)))
		if err != nil {
			return fmt.Errorf("dev seed-app-thread: put blob: %w", err)
		}
		env := store.Envelope{Subject: subject, From: from, To: to, Date: at, MessageID: devAppThreadMessageIDs[i]}
		if i == 1 {
			env.InReplyTo = devAppThreadMessageIDs[0]
			env.References = "<" + devAppThreadMessageIDs[0] + ">"
		}
		msg := store.Message{
			PrincipalID: principal.ID, MailboxID: inboxID, InternalDate: at, ReceivedAt: at,
			Size: ref.Size, Blob: ref, ThreadID: threadKey, Envelope: env,
		}
		if _, _, err := st.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: inboxID}}); err != nil {
			return fmt.Errorf("dev seed-app-thread: insert message: %w", err)
		}
		if i == 0 {
			first, ok, err := findSeed()
			if err != nil || !ok {
				return fmt.Errorf("dev seed-app-thread: locate first message: %v", err)
			}
			threadKey = uint64(first.ID)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dev-seed: seeded app thread %q (t%d, 2 messages) for %s\n",
		devAppThreadSubject, threadKey, principalEmail)
	return nil
}
