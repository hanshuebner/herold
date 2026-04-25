package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// getRequest is the wire-form Mailbox/get request (RFC 8621 §2.4 +
// RFC 8620 §5.1).
type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

// getResponse is the wire-form Mailbox/get response.
type getResponse struct {
	AccountID jmapID        `json:"accountId"`
	State     string        `json:"state"`
	List      []jmapMailbox `json:"list"`
	NotFound  []jmapID      `json:"notFound"`
}

// getHandler implements protojmap.MethodHandler for Mailbox/get.
type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "Mailbox/get" }

func (g *getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}

	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentState(ctx, g.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	all, err := listAccessibleMailboxes(ctx, g.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	resp := getResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapMailbox{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		for _, mb := range all {
			rendered, err := renderMailbox(ctx, g.h.store.Meta(), pid, mb)
			if err != nil {
				return nil, serverFail(err)
			}
			resp.List = append(resp.List, rendered)
		}
		return resp, nil
	}

	byID := make(map[store.MailboxID]store.Mailbox, len(all))
	for _, mb := range all {
		byID[mb.ID] = mb
	}
	for _, raw := range *req.IDs {
		id, ok := mailboxIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		mb, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		rendered, err := renderMailbox(ctx, g.h.store.Meta(), pid, mb)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// errMailboxMissing reports "looks the same as never existed" per
// RFC 8621 §2.5.
var errMailboxMissing = errors.New("mailbox: not found or not visible")

func loadMailboxForPrincipal(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	id store.MailboxID,
) (store.Mailbox, error) {
	mb, err := meta.GetMailboxByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Mailbox{}, errMailboxMissing
		}
		return store.Mailbox{}, fmt.Errorf("mailbox: load: %w", err)
	}
	if mb.PrincipalID == pid {
		return mb, nil
	}
	rows, err := meta.GetMailboxACL(ctx, mb.ID)
	if err != nil {
		return store.Mailbox{}, fmt.Errorf("mailbox: load acl: %w", err)
	}
	for _, r := range rows {
		if r.PrincipalID == nil {
			if r.Rights&store.ACLRightLookup != 0 {
				return mb, nil
			}
			continue
		}
		if *r.PrincipalID == pid && r.Rights&store.ACLRightLookup != 0 {
			return mb, nil
		}
	}
	return store.Mailbox{}, errMailboxMissing
}
