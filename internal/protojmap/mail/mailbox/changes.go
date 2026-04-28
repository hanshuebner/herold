package mailbox

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// changesRequest is the wire-form Mailbox/changes request (RFC 8620 §5.2
// + RFC 8621 §2.5).
type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

// changesResponse is the wire-form response.
type changesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

// changesHandler implements protojmap.MethodHandler for Mailbox/changes.
type changesHandler struct{ h *handlerSet }

func (c *changesHandler) Method() string { return "Mailbox/changes" }

// Execute walks the per-principal change feed for mailbox-kind entries
// with seq > sinceState. The state string is the max change-feed seq for
// EntityKindMailbox so any mailbox mutation — including ones made outside
// the JMAP layer (IMAP renames, provisioning) — advances the state
// and is reflected in the changes response.
func (c *changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}

	var req changesRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	since, ok := parseState(req.SinceState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceState")
	}

	newSeq, err := c.h.store.Meta().GetMaxChangeSeqForKind(ctx, pid, store.EntityKindMailbox)
	if err != nil {
		return nil, serverFail(err)
	}
	newState := stateFromSeq(newSeq)

	resp := changesResponse{
		AccountID: req.AccountID,
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == newSeq {
		return resp, nil
	}
	if since > newSeq {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}

	const page = 1000
	var cursor store.ChangeSeq = since
	created := map[store.MailboxID]struct{}{}
	updated := map[store.MailboxID]struct{}{}
	destroyed := map[store.MailboxID]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, serverFail(err)
		}
		batch, ferr := c.h.store.Meta().ReadChangeFeed(ctx, pid, cursor, page)
		if ferr != nil {
			return nil, serverFail(ferr)
		}
		for _, entry := range batch {
			cursor = entry.Seq
			if entry.Kind != store.EntityKindMailbox {
				continue
			}
			id := store.MailboxID(entry.EntityID)
			switch entry.Op {
			case store.ChangeOpCreated:
				delete(destroyed, id)
				created[id] = struct{}{}
			case store.ChangeOpUpdated:
				if _, isCreated := created[id]; isCreated {
					continue
				}
				if _, gone := destroyed[id]; gone {
					continue
				}
				updated[id] = struct{}{}
			case store.ChangeOpDestroyed:
				if _, isCreated := created[id]; isCreated {
					delete(created, id)
					continue
				}
				delete(updated, id)
				destroyed[id] = struct{}{}
			}
		}
		if len(batch) < page {
			break
		}
	}

	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromMailbox(id))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromMailbox(id))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromMailbox(id))
	}

	if req.MaxChanges != nil && *req.MaxChanges > 0 {
		total := len(resp.Created) + len(resp.Updated) + len(resp.Destroyed)
		if total > *req.MaxChanges {
			resp.HasMoreChanges = true
			resp.NewState = req.SinceState
			over := total - *req.MaxChanges
			over, resp.Updated = trimIDs(over, resp.Updated)
			over, resp.Destroyed = trimIDs(over, resp.Destroyed)
			_, resp.Created = trimIDs(over, resp.Created)
		}
	}

	return resp, nil
}

// trimIDs drops up to `over` entries from xs's tail, returning the
// remaining `over` budget after the trim and the shrunk slice.
func trimIDs(over int, xs []jmapID) (int, []jmapID) {
	if over <= 0 || len(xs) == 0 {
		return over, xs
	}
	if over >= len(xs) {
		return over - len(xs), xs[:0]
	}
	return 0, xs[:len(xs)-over]
}
