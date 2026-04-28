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

// getHandler implements protojmap.MethodHandler for Mailbox/get.
type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "Mailbox/get" }

// getResponse is the wire-form Mailbox/get response. List holds either
// []jmapMailbox (all properties) or []map[string]any (subset filtered
// by req.Properties) — both marshal to the correct JSON shape.
type getResponseFiltered struct {
	AccountID jmapID   `json:"accountId"`
	State     string   `json:"state"`
	List      []any    `json:"list"`
	NotFound  []jmapID `json:"notFound"`
}

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

	// Build a property allow-set when the client requested a subset.
	// "id" is always returned per RFC 8620 §5.1.
	var propSet map[string]struct{}
	if req.Properties != nil {
		propSet = make(map[string]struct{}, len(*req.Properties)+1)
		propSet["id"] = struct{}{}
		for _, p := range *req.Properties {
			propSet[p] = struct{}{}
		}
	}

	resp := getResponseFiltered{
		AccountID: req.AccountID,
		State:     state,
		List:      []any{},
		NotFound:  []jmapID{},
	}

	appendRendered := func(mb store.Mailbox) *protojmap.MethodError {
		rendered, err := renderMailbox(ctx, g.h.store.Meta(), pid, mb)
		if err != nil {
			return serverFail(err)
		}
		if propSet == nil {
			resp.List = append(resp.List, rendered)
		} else {
			resp.List = append(resp.List, filterMailboxProperties(rendered, propSet))
		}
		return nil
	}

	if req.IDs == nil {
		for _, mb := range all {
			if merr := appendRendered(mb); merr != nil {
				return nil, merr
			}
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
		if merr := appendRendered(mb); merr != nil {
			return nil, merr
		}
	}
	return resp, nil
}

// filterMailboxProperties projects a fully-rendered jmapMailbox onto a
// map[string]any containing only the keys present in propSet. RFC 8620
// §5.1: "id" is always included regardless of the client's list.
func filterMailboxProperties(mb jmapMailbox, propSet map[string]struct{}) map[string]any {
	m := make(map[string]any, len(propSet))
	if _, ok := propSet["id"]; ok {
		m["id"] = mb.ID
	}
	if _, ok := propSet["name"]; ok {
		m["name"] = mb.Name
	}
	if _, ok := propSet["parentId"]; ok {
		m["parentId"] = mb.ParentID
	}
	if _, ok := propSet["role"]; ok {
		m["role"] = mb.Role
	}
	if _, ok := propSet["sortOrder"]; ok {
		m["sortOrder"] = mb.SortOrder
	}
	if _, ok := propSet["totalEmails"]; ok {
		m["totalEmails"] = mb.TotalEmails
	}
	if _, ok := propSet["unreadEmails"]; ok {
		m["unreadEmails"] = mb.UnreadEmails
	}
	if _, ok := propSet["totalThreads"]; ok {
		m["totalThreads"] = mb.TotalThreads
	}
	if _, ok := propSet["unreadThreads"]; ok {
		m["unreadThreads"] = mb.UnreadThreads
	}
	if _, ok := propSet["myRights"]; ok {
		m["myRights"] = mb.MyRights
	}
	if _, ok := propSet["isSubscribed"]; ok {
		m["isSubscribed"] = mb.IsSubscribed
	}
	if _, ok := propSet["color"]; ok {
		m["color"] = mb.Color
	}
	return m
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
