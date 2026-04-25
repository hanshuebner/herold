package email

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// getRequest is the wire-form Email/get request (RFC 8621 §4.2).
type getRequest struct {
	AccountID           jmapID    `json:"accountId"`
	IDs                 *[]jmapID `json:"ids"`
	Properties          *[]string `json:"properties"`
	BodyProperties      *[]string `json:"bodyProperties"`
	FetchTextBodyValues bool      `json:"fetchTextBodyValues"`
	FetchHTMLBodyValues bool      `json:"fetchHTMLBodyValues"`
	FetchAllBodyValues  bool      `json:"fetchAllBodyValues"`
	MaxBodyValueBytes   int       `json:"maxBodyValueBytes"`
}

// getResponse is the wire-form response.
type getResponse struct {
	AccountID jmapID      `json:"accountId"`
	State     string      `json:"state"`
	List      []jmapEmail `json:"list"`
	NotFound  []jmapID    `json:"notFound"`
}

// getHandler implements Email/get.
type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "Email/get" }

func (g *getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := principalFromCtx(ctx)
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

	resp := getResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapEmail{},
		NotFound:  []jmapID{},
	}

	wantBodies := req.FetchTextBodyValues || req.FetchHTMLBodyValues || req.FetchAllBodyValues

	if req.IDs == nil {
		// Fetch all (rarely useful — RFC 8621 recommends always-listing
		// ids — but we honour it because the spec permits it).
		all, err := listPrincipalMessages(ctx, g.h.store.Meta(), pid)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, m := range all {
			rendered, err := g.renderOne(ctx, m, wantBodies, req.MaxBodyValueBytes)
			if err != nil {
				return nil, serverFail(err)
			}
			resp.List = append(resp.List, rendered)
		}
		return resp, nil
	}

	for _, raw := range *req.IDs {
		mid, ok := emailIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		m, err := loadMessageForPrincipal(ctx, g.h.store.Meta(), pid, mid)
		if err != nil {
			if errors.Is(err, errMessageMissing) {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			return nil, serverFail(err)
		}
		rendered, err := g.renderOne(ctx, m, wantBodies, req.MaxBodyValueBytes)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// renderOne produces the wire-form Email object. When the request asks
// for body values we round-trip through the blob store and parser.
func (g *getHandler) renderOne(
	ctx context.Context,
	m store.Message,
	wantBodies bool,
	truncateAt int,
) (jmapEmail, error) {
	if !wantBodies {
		return renderEmailMetadata(m), nil
	}
	parser := g.h.parseFn
	if parser == nil {
		parser = defaultParseFn
	}
	return renderFull(ctx, g.h.store.Blobs(), m, truncateAt, parser)
}

// principalFromCtx is a thin wrapper around protojmap.PrincipalFromContext
// used by every handler in this package.
func principalFromCtx(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	return requirePrincipal(func() (store.PrincipalID, bool) {
		p, ok := protojmap.PrincipalFromContext(ctx)
		if !ok {
			return 0, false
		}
		return p.ID, true
	})
}
