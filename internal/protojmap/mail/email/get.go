package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

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
		ids := make([]store.MessageID, len(all))
		for i, m := range all {
			ids[i] = m.ID
		}
		batchReactions, err := g.h.store.Meta().BatchListEmailReactions(ctx, ids)
		if err != nil {
			return nil, serverFail(fmt.Errorf("email: load reactions: %w", err))
		}
		for _, m := range all {
			rendered, err := g.renderOne(ctx, m, wantBodies, req.MaxBodyValueBytes)
			if err != nil {
				return nil, serverFail(err)
			}
			rendered.Reactions = reactionsToWire(batchReactions[m.ID])
			resp.List = append(resp.List, rendered)
		}
		return resp, nil
	}

	// Collect valid MessageIDs first so we can batch-fetch reactions.
	type entry struct {
		raw string
		mid store.MessageID
		msg store.Message
		ok  bool
	}
	entries := make([]entry, 0, len(*req.IDs))
	var validIDs []store.MessageID
	for _, raw := range *req.IDs {
		mid, ok := emailIDFromJMAP(raw)
		if !ok {
			entries = append(entries, entry{raw: raw})
			continue
		}
		m, err := loadMessageForPrincipal(ctx, g.h.store.Meta(), pid, mid)
		if err != nil {
			if errors.Is(err, errMessageMissing) {
				entries = append(entries, entry{raw: raw})
				continue
			}
			return nil, serverFail(err)
		}
		entries = append(entries, entry{raw: raw, mid: mid, msg: m, ok: true})
		validIDs = append(validIDs, mid)
	}

	batchReactions, err := g.h.store.Meta().BatchListEmailReactions(ctx, validIDs)
	if err != nil {
		return nil, serverFail(fmt.Errorf("email: load reactions: %w", err))
	}

	for _, e := range entries {
		if !e.ok {
			resp.NotFound = append(resp.NotFound, e.raw)
			continue
		}
		rendered, err := g.renderOne(ctx, e.msg, wantBodies, req.MaxBodyValueBytes)
		if err != nil {
			return nil, serverFail(err)
		}
		rendered.Reactions = reactionsToWire(batchReactions[e.mid])
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// reactionsToWire converts the store's map[emoji]map[PrincipalID]struct{}
// into the JMAP wire form map[emoji][]principalID. Returns nil when the
// input is empty so the field is omitted from JSON (sparse by design).
func reactionsToWire(r map[string]map[store.PrincipalID]struct{}) map[string][]string {
	if len(r) == 0 {
		return nil
	}
	out := make(map[string][]string, len(r))
	for emoji, pids := range r {
		list := make([]string, 0, len(pids))
		for pid := range pids {
			list = append(list, strconv.FormatUint(uint64(pid), 10))
		}
		sort.Strings(list) // deterministic order for tests
		out[emoji] = list
	}
	return out
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
