// Package ghostrows finds and removes IMAP-import message_state "ghost"
// rows (re #558): rows whose upstream UID cannot exist in their folder
// because it is at or above the folder's last-known UIDNEXT. The write-back
// move path used to record a moved message under the destination folder
// with the SOURCE folder's UID, leaving such rows behind. A ghost row names
// no upstream message, so deleting it loses nothing; it only stops a later
// write-back or reconcile from acting on a UID that may one day belong to
// an unrelated message.
package ghostrows

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hanshuebner/herold/internal/store"
)

// Ghost is one ghost message_state row.
type Ghost struct {
	State store.IMAPImportMessageState
	// UIDNext is the folder cursor's UIDNEXT the row's UID is not below.
	UIDNext uint64
}

// FolderCount is the number of ghost rows in one (account, folder).
type FolderCount struct {
	AccountID string
	Folder    string
	Rows      int
	// LiveMessages counts distinct herold messages that still exist and
	// own at least one ghost row in the folder.
	LiveMessages int
}

// Find returns every ghost row across all import accounts. Folders without a
// cursor (or with UIDNEXT 0) are never reported: without an upstream bound
// no row is provably a ghost.
func Find(ctx context.Context, st store.Store) ([]Ghost, error) {
	meta := st.Meta()
	var out []Ghost
	var after store.PrincipalID
	for {
		ps, err := meta.ListPrincipals(ctx, after, 200)
		if err != nil {
			return nil, fmt.Errorf("ghostrows: ListPrincipals: %w", err)
		}
		if len(ps) == 0 {
			break
		}
		for _, p := range ps {
			after = p.ID
			accts, err := meta.ListIMAPImportAccountsByPrincipal(ctx, p.ID)
			if err != nil {
				return nil, fmt.Errorf("ghostrows: ListIMAPImportAccountsByPrincipal(%d): %w", p.ID, err)
			}
			for _, a := range accts {
				rows, err := meta.ListIMAPImportMessageStatesByAccount(ctx, a.ID)
				if err != nil {
					return nil, fmt.Errorf("ghostrows: ListIMAPImportMessageStatesByAccount: %w", err)
				}
				uidNext := map[string]uint64{}
				for _, r := range rows {
					next, seen := uidNext[r.UpstreamFolder]
					if !seen {
						cur, found, err := meta.GetIMAPImportFolderCursor(ctx, a.ID, r.UpstreamFolder)
						if err != nil {
							return nil, fmt.Errorf("ghostrows: GetIMAPImportFolderCursor: %w", err)
						}
						if found {
							next = cur.UIDNext
						}
						uidNext[r.UpstreamFolder] = next
					}
					if next > 0 && uint64(r.UpstreamUID) >= next {
						out = append(out, Ghost{State: r, UIDNext: next})
					}
				}
			}
		}
	}
	return out, nil
}

// Summarize groups ghosts per (account, folder), ordered by account then
// folder.
func Summarize(ctx context.Context, st store.Store, ghosts []Ghost) ([]FolderCount, error) {
	type key struct{ acct, folder string }
	counts := map[key]*FolderCount{}
	live := map[key]map[store.MessageID]bool{}
	for _, g := range ghosts {
		k := key{g.State.AccountID, g.State.UpstreamFolder}
		c := counts[k]
		if c == nil {
			c = &FolderCount{AccountID: k.acct, Folder: k.folder}
			counts[k] = c
			live[k] = map[store.MessageID]bool{}
		}
		c.Rows++
		if _, err := st.Meta().GetMessage(ctx, g.State.HeroldMessageID); err == nil {
			live[k][g.State.HeroldMessageID] = true
		}
	}
	out := make([]FolderCount, 0, len(counts))
	for k, c := range counts {
		c.LiveMessages = len(live[k])
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AccountID != out[j].AccountID {
			return out[i].AccountID < out[j].AccountID
		}
		return out[i].Folder < out[j].Folder
	})
	return out, nil
}

// Delete removes the given ghost rows and returns how many were removed.
func Delete(ctx context.Context, st store.Store, ghosts []Ghost) (int, error) {
	n := 0
	for _, g := range ghosts {
		err := st.Meta().DeleteIMAPImportMessageState(ctx, g.State.AccountID, g.State.UpstreamFolder, g.State.UpstreamUID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return n, fmt.Errorf("ghostrows: delete (%s, %d): %w", g.State.UpstreamFolder, g.State.UpstreamUID, err)
		}
		n++
	}
	return n, nil
}
