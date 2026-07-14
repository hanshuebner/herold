package email

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// changesRequest is the wire-form Email/changes request.
type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

// changesResponse is the wire-form Email/changes response.
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

// changesHandler implements Email/changes.
type changesHandler struct{ h *handlerSet }

func (c *changesHandler) Method() string { return "Email/changes" }

// Execute walks the principal's change feed for email-kind entries
// since the supplied state. Mirrors Mailbox/changes: the state string
// is the max change-feed seq for EntityKindEmail entries, so mutations
// from IMAP STORE / delivery are included without a separate
// bookkeeping pass.
func (c *changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	callerPID, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req changesRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	ownerPID, merr := resolveAccount(ctx, c.h.store.Meta(), callerPID, req.AccountID)
	if merr != nil {
		return nil, merr
	}
	since, ok := parseState(req.SinceState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceState")
	}

	newSeq, err := c.h.store.Meta().GetMaxChangeSeqForKind(ctx, ownerPID, store.EntityKindEmail)
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
	created := map[store.MessageID]struct{}{}
	updated := map[store.MessageID]struct{}{}
	destroyed := map[store.MessageID]struct{}{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, serverFail(err)
		}
		batch, ferr := c.h.store.Meta().ReadChangeFeed(ctx, ownerPID, cursor, page)
		if ferr != nil {
			return nil, serverFail(ferr)
		}
		for _, entry := range batch {
			cursor = entry.Seq
			if entry.Kind != store.EntityKindEmail {
				continue
			}
			id := store.MessageID(entry.EntityID)
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

	// Cross-account: filter Created/Updated to messages currently
	// visible to the caller (REQ-PROTO-33). Destroyed entries pass
	// through unconditionally so the caller can drop stale ids from
	// its cache (the original mailbox may have been outside the caller
	// before the destroy too, but conservative pass-through keeps the
	// state machine honest).
	keep := func(id store.MessageID) bool {
		m, lerr := loadMessageForPrincipal(ctx, c.h.store.Meta(), callerPID, id)
		if lerr != nil {
			return false
		}
		// Unconditional account guard (not just when callerPID !=
		// ownerPID): loadMessageForPrincipal also grants a sub-account's
		// parent access to the sub-account's mail (REQ-SUBACCT-04), so a
		// caller polling changes on her OWN account must still not see a
		// message that in fact lives in her sub-account, and vice versa.
		return m.PrincipalID == ownerPID
	}
	for id := range created {
		if keep(id) {
			resp.Created = append(resp.Created, jmapIDFromMessage(id))
		}
	}
	for id := range updated {
		if keep(id) {
			resp.Updated = append(resp.Updated, jmapIDFromMessage(id))
		}
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromMessage(id))
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

func trimIDs(over int, xs []jmapID) (int, []jmapID) {
	if over <= 0 || len(xs) == 0 {
		return over, xs
	}
	if over >= len(xs) {
		return over - len(xs), xs[:0]
	}
	return 0, xs[:len(xs)-over]
}
