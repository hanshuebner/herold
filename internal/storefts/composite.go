package storefts

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// Composite implements store.FTS by routing the Bleve-backed surface to
// *Index and the SQL-backed change-feed read to a per-backend delegate
// (the storesqlite / storepg ftsStub). The split exists because the
// change feed lives in the relational state_changes table the backend
// owns, while the search corpus lives in the Bleve index storefts owns.
//
// Production wires this up in admin/server.go after constructing the
// *Index, replacing store.Store.FTS() so the JMAP Email/query and IMAP
// SEARCH paths read from Bleve instead of the substring stub.
type Composite struct {
	idx      *Index
	delegate store.FTS
}

// NewComposite returns a store.FTS that serves IndexMessage,
// RemoveMessage, Query, and Commit from idx and forwards
// ReadChangeFeedForFTS to delegate. delegate is typically the
// per-backend stub returned by store.FTS() before composition.
func NewComposite(idx *Index, delegate store.FTS) *Composite {
	return &Composite{idx: idx, delegate: delegate}
}

// IndexMessage writes the FTS document via the underlying *Index. The
// principal-scoped variant (IndexMessageFull) used by Worker stays on
// *Index directly; this method exists for callers that hold only a
// store.FTS and accepts the principal-less signature the interface
// defines.
func (c *Composite) IndexMessage(ctx context.Context, msg store.Message, text string) error {
	return c.idx.IndexMessage(ctx, msg, text)
}

func (c *Composite) RemoveMessage(ctx context.Context, id store.MessageID) error {
	return c.idx.RemoveMessage(ctx, id)
}

func (c *Composite) Query(ctx context.Context, principalID store.PrincipalID, q store.Query) ([]store.MessageRef, error) {
	return c.idx.Query(ctx, principalID, q)
}

func (c *Composite) ReadChangeFeedForFTS(ctx context.Context, cursor uint64, max int) ([]store.FTSChange, error) {
	return c.delegate.ReadChangeFeedForFTS(ctx, cursor, max)
}

func (c *Composite) Commit(ctx context.Context) error {
	return c.idx.Commit(ctx)
}
