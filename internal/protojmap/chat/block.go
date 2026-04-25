package chat

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- Block/get --------------------------------------------------------

type blockGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type blockGetResponse struct {
	AccountID jmapID      `json:"accountId"`
	State     string      `json:"state"`
	List      []jmapBlock `json:"list"`
	NotFound  []jmapID    `json:"notFound"`
}

type blockGetHandler struct{ h *handlerSet }

func (h *blockGetHandler) Method() string { return "Block/get" }

func (h *blockGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req blockGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	rows, err := h.h.store.Meta().ListChatBlocksBy(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	// Block lists are private: the caller only ever sees their own
	// blocks. The state string is intentionally not exposed via
	// JMAPStates per the prompt; we use a tag derived from the block
	// count + the most recent CreatedAt so clients can detect change.
	state := blockStateString(rows)
	resp := blockGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapBlock{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		for _, b := range rows {
			resp.List = append(resp.List, renderBlock(b))
		}
		return resp, nil
	}
	byID := make(map[jmapID]store.ChatBlock, len(rows))
	for _, b := range rows {
		byID[blockIDFromBlocked(b.BlockedPrincipalID)] = b
	}
	for _, raw := range *req.IDs {
		if b, ok := byID[raw]; ok {
			resp.List = append(resp.List, renderBlock(b))
		} else {
			resp.NotFound = append(resp.NotFound, raw)
		}
	}
	return resp, nil
}

func renderBlock(b store.ChatBlock) jmapBlock {
	return jmapBlock{
		ID:                 blockIDFromBlocked(b.BlockedPrincipalID),
		BlockedPrincipalID: jmapIDFromPrincipal(b.BlockedPrincipalID),
		CreatedAt:          b.CreatedAt.UTC().Format(rfc3339Layout),
	}
}

// blockStateString synthesises a state token from the caller's block
// list. Block state is per-caller and infrequent (REQ-CHAT-71); we
// avoid plumbing a JMAPStates counter and let the client refresh on
// demand. The token is `<count>-<latest-rfc3339>` so any add/remove
// changes the state.
func blockStateString(rows []store.ChatBlock) string {
	if len(rows) == 0 {
		return "0-"
	}
	var latest string
	for _, b := range rows {
		ts := b.CreatedAt.UTC().Format(rfc3339Layout)
		if ts > latest {
			latest = ts
		}
	}
	return strings.Join([]string{strconv.Itoa(len(rows)), latest}, "-")
}

// -- Block/set --------------------------------------------------------

type blockSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	Create    map[string]json.RawMessage `json:"create"`
	Destroy   []jmapID                   `json:"destroy"`
}

type blockSetResponse struct {
	AccountID    jmapID               `json:"accountId"`
	OldState     string               `json:"oldState"`
	NewState     string               `json:"newState"`
	Created      map[string]jmapBlock `json:"created"`
	Destroyed    []jmapID             `json:"destroyed"`
	NotCreated   map[string]setError  `json:"notCreated"`
	NotDestroyed map[jmapID]setError  `json:"notDestroyed"`
	Updated      map[jmapID]any       `json:"updated"`
	NotUpdated   map[jmapID]setError  `json:"notUpdated"`
}

type blockCreateInput struct {
	BlockedPrincipalID jmapID `json:"blockedPrincipalId"`
	Reason             string `json:"reason"`
}

type blockSetHandler struct{ h *handlerSet }

func (h *blockSetHandler) Method() string { return "Block/set" }

func (h *blockSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req blockSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	rows, err := h.h.store.Meta().ListChatBlocksBy(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	oldState := blockStateString(rows)
	resp := blockSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     oldState,
		NewState:     oldState,
		Created:      map[string]jmapBlock{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}

	for key, raw := range req.Create {
		var in blockCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		blocked, ok := blockIDToBlocked(in.BlockedPrincipalID)
		if !ok {
			resp.NotCreated[key] = setError{
				Type: "invalidProperties", Properties: []string{"blockedPrincipalId"},
				Description: "blockedPrincipalId is required",
			}
			continue
		}
		if blocked == pid {
			resp.NotCreated[key] = setError{
				Type: "invalidProperties", Properties: []string{"blockedPrincipalId"},
				Description: "cannot block yourself",
			}
			continue
		}
		row := store.ChatBlock{
			BlockerPrincipalID: pid,
			BlockedPrincipalID: blocked,
			CreatedAt:          h.h.clk.Now(),
			Reason:             in.Reason,
		}
		if err := h.h.store.Meta().InsertChatBlock(ctx, row); err != nil {
			if errors.Is(err, store.ErrConflict) {
				// Already blocked; surface the existing row so the
				// client renders consistent state.
				resp.Created[key] = renderBlock(row)
				continue
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
			return nil, serverFail(err)
		}
		resp.Created[key] = renderBlock(row)
	}

	for _, raw := range req.Destroy {
		blocked, ok := blockIDToBlocked(raw)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		if err := h.h.store.Meta().DeleteChatBlock(ctx, pid, blocked); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotDestroyed[raw] = setError{Type: "notFound"}
				continue
			}
			return nil, serverFail(err)
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	rows, err = h.h.store.Meta().ListChatBlocksBy(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = blockStateString(rows)
	sort.Strings(resp.Destroyed)
	return resp, nil
}
