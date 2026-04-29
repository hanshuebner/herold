package chat

// Principal/get and Principal/query — the two JMAP methods that support
// the new-chat picker (docs/design/web/architecture/07-chat-protocol.md
// § "Principal directory (chat-only)").
//
// Both methods are gated on the chat capability so that unauthenticated
// or non-chat clients cannot enumerate the directory (REQ-CHAT-01b).
//
// Privacy contract: only id, email, and displayName are ever returned.
// No password hashes, OIDC links, creation timestamps, or role flags.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// principalQueryMaxLimit is the server-side hard cap on Principal/query
// results per the spec ("server enforces a max of 25").
const principalQueryMaxLimit = 25

// principalQueryDefaultLimit is the default when the caller omits limit.
const principalQueryDefaultLimit = 10

// -- Wire types -------------------------------------------------------

// jmapPrincipal is the three-field public view of a Principal
// (REQ-CHAT-01b, REQ-CHAT-15).
type jmapPrincipal struct {
	ID          jmapID `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// principalFilter is the union type for Principal/query filter. Exactly
// one of EmailExact or TextPrefix may be set; sending both is an error.
type principalFilter struct {
	EmailExact *string `json:"emailExact"`
	TextPrefix *string `json:"textPrefix"`
}

// -- Principal/get ----------------------------------------------------

type principalGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type principalGetResponse struct {
	AccountID string          `json:"accountId"`
	State     string          `json:"state"`
	List      []jmapPrincipal `json:"list"`
	NotFound  []jmapID        `json:"notFound"`
}

type principalGetHandler struct{ h *handlerSet }

func (h *principalGetHandler) Method() string { return "Principal/get" }

func (h *principalGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req principalGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentConversationState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	resp := principalGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapPrincipal{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		// RFC 8620 §5.1: when ids is null the server MAY return all
		// records or an error. We return empty for safety — enumerating
		// all principals without a query is not a supported operation.
		return resp, nil
	}

	for _, raw := range *req.IDs {
		storeID, ok := principalIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		p, err := h.h.store.Meta().GetPrincipalByID(ctx, storeID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			return nil, serverFail(err)
		}
		resp.List = append(resp.List, renderPrincipal(p, req.Properties))
	}
	return resp, nil
}

// -- Principal/query --------------------------------------------------

type principalQueryRequest struct {
	AccountID jmapID           `json:"accountId"`
	Filter    *principalFilter `json:"filter"`
	Limit     *int             `json:"limit"`
}

type principalQueryResponse struct {
	AccountID string   `json:"accountId"`
	State     string   `json:"state"`
	IDs       []jmapID `json:"ids"`
	Total     int      `json:"total"`
}

type principalQueryHandler struct{ h *handlerSet }

func (h *principalQueryHandler) Method() string { return "Principal/query" }

func (h *principalQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req principalQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	// Validate limit before touching the filter so limit errors surface
	// independently of filter errors.
	limit := principalQueryDefaultLimit
	if req.Limit != nil {
		if *req.Limit <= 0 {
			return nil, protojmap.NewMethodError("invalidArguments", "limit must be a positive integer")
		}
		limit = *req.Limit
		if limit > principalQueryMaxLimit {
			limit = principalQueryMaxLimit
		}
	}

	if req.Filter == nil {
		return nil, protojmap.NewMethodError("invalidArguments", "filter is required for Principal/query")
	}

	// Mutually-exclusive filter shapes.
	if req.Filter.EmailExact != nil && req.Filter.TextPrefix != nil {
		return nil, protojmap.NewMethodError("invalidArguments",
			"emailExact and textPrefix are mutually exclusive")
	}
	if req.Filter.EmailExact == nil && req.Filter.TextPrefix == nil {
		return nil, protojmap.NewMethodError("invalidArguments",
			"filter must contain emailExact or textPrefix")
	}

	state, err := currentConversationState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	resp := principalQueryResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		IDs:       []jmapID{},
	}

	switch {
	case req.Filter.EmailExact != nil:
		email := strings.ToLower(strings.TrimSpace(*req.Filter.EmailExact))
		p, err := h.h.store.Meta().GetPrincipalByEmail(ctx, email)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Miss — return empty ids, total 0.
				return resp, nil
			}
			return nil, serverFail(err)
		}
		resp.IDs = append(resp.IDs, jmapIDFromPrincipal(p.ID))
		resp.Total = 1

	case req.Filter.TextPrefix != nil:
		prefix := strings.TrimSpace(*req.Filter.TextPrefix)
		principals, err := h.h.store.Meta().SearchPrincipalsByText(ctx, prefix, limit)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.Total = len(principals)
		for _, p := range principals {
			resp.IDs = append(resp.IDs, jmapIDFromPrincipal(p.ID))
		}
	}

	return resp, nil
}

// -- rendering --------------------------------------------------------

// renderPrincipal projects a store.Principal into the three-field wire
// form. properties, when non-nil, restricts to the named subset; an
// unrecognised property name is silently ignored (RFC 8620 §5.1).
func renderPrincipal(p store.Principal, properties *[]string) jmapPrincipal {
	out := jmapPrincipal{
		ID:          jmapIDFromPrincipal(p.ID),
		Email:       p.CanonicalEmail,
		DisplayName: principalDisplayName(p),
	}
	if properties == nil {
		return out
	}
	// Apply property mask: zero out fields not requested.
	wantID, wantEmail, wantDisplayName := false, false, false
	for _, prop := range *properties {
		switch prop {
		case "id":
			wantID = true
		case "email":
			wantEmail = true
		case "displayName":
			wantDisplayName = true
		}
	}
	if !wantID {
		out.ID = ""
	}
	if !wantEmail {
		out.Email = ""
	}
	if !wantDisplayName {
		out.DisplayName = ""
	}
	return out
}
