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
	callerPID, merr := requirePrincipal(ctx)
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

	newSeq, err := c.h.store.Meta().GetMaxChangeSeqForKind(ctx, ownerPID, store.EntityKindMailbox)
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

	maxChanges := 0
	if req.MaxChanges != nil && *req.MaxChanges > 0 {
		maxChanges = *req.MaxChanges
	}
	createdRaw, updatedRaw, destroyedRaw, cutoff, hasMore, ferr := protojmap.WalkChangesBySeq(
		ctx, c.h.store.Meta(), ownerPID, store.EntityKindMailbox, since, maxChanges)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	created := make(map[store.MailboxID]struct{}, len(createdRaw))
	for id := range createdRaw {
		created[store.MailboxID(id)] = struct{}{}
	}
	updated := make(map[store.MailboxID]struct{}, len(updatedRaw))
	for id := range updatedRaw {
		updated[store.MailboxID(id)] = struct{}{}
	}
	destroyed := make(map[store.MailboxID]struct{}, len(destroyedRaw))
	for id := range destroyedRaw {
		destroyed[store.MailboxID(id)] = struct{}{}
	}
	if hasMore {
		resp.HasMoreChanges = true
		resp.NewState = stateFromSeq(cutoff)
	}

	// Cross-account: filter Created/Updated to mailboxes the caller can
	// currently see via ACL. Destroyed entries pass through so the
	// caller can drop stale ids from its cache. Same-account: no filter.
	visible := map[store.MailboxID]struct{}{}
	if callerPID != ownerPID {
		visibleSet, lerr := listMailboxesForAccount(ctx, c.h.store.Meta(), callerPID, ownerPID)
		if lerr != nil {
			return nil, serverFail(lerr)
		}
		for _, mb := range visibleSet {
			visible[mb.ID] = struct{}{}
		}
	}
	keep := func(id store.MailboxID) bool {
		if callerPID == ownerPID {
			return true
		}
		_, ok := visible[id]
		return ok
	}
	for id := range created {
		if keep(id) {
			resp.Created = append(resp.Created, jmapIDFromMailbox(id))
		}
	}
	for id := range updated {
		if keep(id) {
			resp.Updated = append(resp.Updated, jmapIDFromMailbox(id))
		}
	}
	for id := range destroyed {
		// Destroyed entries pass through unconditionally: the caller
		// must be able to drop a stale id once it leaves the visible set
		// (revoked ACL, deleted mailbox).
		resp.Destroyed = append(resp.Destroyed, jmapIDFromMailbox(id))
	}

	return resp, nil
}
