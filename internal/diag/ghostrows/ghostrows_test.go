package ghostrows_test

// ghostrows_test.go covers the #558 cleanup: rows whose UID is at or above
// the folder cursor's UIDNEXT are found and removed; rows below it, rows in
// folders without a cursor and the messages themselves are left alone.
// Runs on sqlite always and postgres when HEROLD_PG_DSN is set.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/ghostrows"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

type backend struct {
	name string
	open func(t *testing.T) store.Store
}

func backends() []backend {
	out := []backend{{
		name: "sqlite",
		open: func(t *testing.T) store.Store {
			st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clock.NewReal())
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	}}
	if dsn := os.Getenv("HEROLD_PG_DSN"); dsn != "" {
		out = append(out, backend{
			name: "postgres",
			open: func(t *testing.T) store.Store {
				st, err := storepg.Open(context.Background(), dsn, filepath.Join(t.TempDir(), "blobs"), nil, clock.NewReal())
				if err != nil {
					t.Fatalf("storepg.Open: %v", err)
				}
				if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
					if err := tr.TruncateAll(context.Background()); err != nil {
						t.Fatalf("TruncateAll: %v", err)
					}
				}
				t.Cleanup(func() { _ = st.Close() })
				return st
			},
		})
	}
	return out
}

func TestFindAndDelete(t *testing.T) {
	for _, be := range backends() {
		t.Run(be.name, func(t *testing.T) {
			st := be.open(t)
			ctx := context.Background()
			meta := st.Meta()

			p, err := meta.InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser, CanonicalEmail: "ghost@example.test"})
			if err != nil {
				t.Fatalf("InsertPrincipal: %v", err)
			}
			acc, err := meta.CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
				PrincipalID: p.ID, AccountName: "A", Host: "127.0.0.1", Port: 993,
				TLSMode: store.IMAPImportTLSModeImplicit, Username: "u",
				AuthMethod: store.IMAPImportAuthMethodPassword, CredentialCT: []byte("v1:x"),
				State: store.IMAPImportAccountStateEnabled,
			})
			if err != nil {
				t.Fatalf("CreateIMAPImportAccount: %v", err)
			}
			inbox, err := meta.InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
			if err != nil {
				t.Fatalf("InsertMailbox: %v", err)
			}
			ref, err := st.Blobs().Put(ctx, strings.NewReader("ghost row probe"))
			if err != nil {
				t.Fatalf("Blobs.Put: %v", err)
			}
			_, _, err = meta.InsertMessage(ctx, store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size}, []store.MessageMailbox{{MailboxID: inbox.ID}})
			if err != nil {
				t.Fatalf("InsertMessage: %v", err)
			}
			rows, err := meta.ListMessages(ctx, inbox.ID, store.MessageFilter{})
			if err != nil || len(rows) != 1 {
				t.Fatalf("ListMessages: %v (%d)", err, len(rows))
			}
			msgID := rows[0].ID

			// Folder "Archive" has UIDNEXT 237: uid 229 is real, 29138 is a ghost.
			// Folder "NoCursor" has no cursor: its row is never reported.
			if err := meta.UpsertIMAPImportFolderCursor(ctx, store.IMAPImportFolderCursor{
				AccountID: acc.ID, UpstreamFolder: "Archive", UIDValidity: 1, UIDNext: 237, HighWaterUID: 236,
			}); err != nil {
				t.Fatalf("UpsertIMAPImportFolderCursor: %v", err)
			}
			for _, r := range []store.IMAPImportMessageState{
				{AccountID: acc.ID, UpstreamFolder: "Archive", UpstreamUID: 229, HeroldMessageID: msgID, HeroldMailboxID: inbox.ID},
				{AccountID: acc.ID, UpstreamFolder: "Archive", UpstreamUID: 29138, HeroldMessageID: msgID, HeroldMailboxID: inbox.ID, MappedMailboxID: inbox.ID},
				{AccountID: acc.ID, UpstreamFolder: "NoCursor", UpstreamUID: 99999, HeroldMessageID: msgID, HeroldMailboxID: inbox.ID},
			} {
				if err := meta.UpsertIMAPImportMessageState(ctx, r); err != nil {
					t.Fatalf("Upsert state: %v", err)
				}
			}

			ghosts, err := ghostrows.Find(ctx, st)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if len(ghosts) != 1 || ghosts[0].State.UpstreamUID != 29138 {
				t.Fatalf("Find = %+v, want exactly the Archive uid 29138 row", ghosts)
			}
			counts, err := ghostrows.Summarize(ctx, st, ghosts)
			if err != nil {
				t.Fatalf("Summarize: %v", err)
			}
			if len(counts) != 1 || counts[0].Folder != "Archive" || counts[0].Rows != 1 || counts[0].LiveMessages != 1 {
				t.Fatalf("Summarize = %+v", counts)
			}

			// Find alone (the dry run) writes nothing.
			if again, _ := ghostrows.Find(ctx, st); len(again) != 1 {
				t.Fatalf("dry run changed state: %+v", again)
			}

			n, err := ghostrows.Delete(ctx, st, ghosts)
			if err != nil || n != 1 {
				t.Fatalf("Delete = %d, %v", n, err)
			}
			left, err := meta.ListIMAPImportMessageStatesByAccount(ctx, acc.ID)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(left) != 2 {
				t.Fatalf("rows after delete = %+v, want the two non-ghost rows", left)
			}
			if _, err := meta.GetMessage(ctx, msgID); err != nil {
				t.Fatalf("message must survive the cleanup: %v", err)
			}
		})
	}
}
