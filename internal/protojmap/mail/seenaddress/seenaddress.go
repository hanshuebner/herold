package seenaddress

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire-form JMAP id type.
type jmapID = string

// handlerSet bundles the dependencies shared by all SeenAddress handlers.
type handlerSet struct {
	store store.Store
	log   *slog.Logger
	clk   clock.Clock
}

// Register installs the SeenAddress/* handlers under the urn:ietf:params:jmap:mail
// capability (no new capability slug per REQ-MAIL-11e spec and the architecture
// doc § "SeenAddress (recipient autocomplete supplement)").
func Register(reg *protojmap.CapabilityRegistry, st store.Store, logger *slog.Logger, clk clock.Clock) {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	h := &handlerSet{store: st, log: logger, clk: clk}
	reg.Register(protojmap.CapabilityMail, &getHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &changesHandler{h: h})
	reg.Register(protojmap.CapabilityMail, &setHandler{h: h})
}

// helpers ----------------------------------------------------------------

func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := principalFor(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

func requireAccount(req jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if req == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if req != protojmap.AccountIDForPrincipal(pid) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}

func seenAddrID(id store.SeenAddressID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

func parseSeenAddrID(s jmapID) (store.SeenAddressID, bool) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.SeenAddressID(v), true
}

func currentState(ctx context.Context, meta store.Metadata, pid store.PrincipalID) (string, error) {
	st, err := meta.GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(st.SeenAddress, 10), nil
}

// jmapSeenAddress is the wire-form SeenAddress object.
type jmapSeenAddress struct {
	ID            jmapID `json:"id"`
	Email         string `json:"email"`
	DisplayName   string `json:"displayName,omitempty"`
	FirstSeenAt   string `json:"firstSeenAt"`
	LastUsedAt    string `json:"lastUsedAt"`
	SendCount     int64  `json:"sendCount"`
	ReceivedCount int64  `json:"receivedCount"`
}

func toJMAP(sa store.SeenAddress) jmapSeenAddress {
	return jmapSeenAddress{
		ID:            seenAddrID(sa.ID),
		Email:         sa.Email,
		DisplayName:   sa.DisplayName,
		FirstSeenAt:   sa.FirstSeenAt.UTC().Format(time.RFC3339),
		LastUsedAt:    sa.LastUsedAt.UTC().Format(time.RFC3339),
		SendCount:     sa.SendCount,
		ReceivedCount: sa.ReceivedCount,
	}
}

// -- SeenAddress/get -----------------------------------------------------

type getRequest struct {
	AccountID jmapID    `json:"accountId"`
	IDs       *[]jmapID `json:"ids"`
}

type getResponse struct {
	AccountID jmapID            `json:"accountId"`
	State     string            `json:"state"`
	List      []jmapSeenAddress `json:"list"`
	NotFound  []jmapID          `json:"notFound"`
}

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "SeenAddress/get" }

func (g getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
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
	resp := getResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapSeenAddress{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		// ids:null — return all entries.
		rows, err := g.h.store.Meta().ListSeenAddressesByPrincipal(ctx, pid, 0)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, sa := range rows {
			resp.List = append(resp.List, toJMAP(sa))
		}
		return resp, nil
	}

	for _, raw := range *req.IDs {
		id, ok := parseSeenAddrID(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		rows, err := g.h.store.Meta().ListSeenAddressesByPrincipal(ctx, pid, 0)
		if err != nil {
			return nil, serverFail(err)
		}
		found := false
		for _, sa := range rows {
			if sa.ID == id {
				resp.List = append(resp.List, toJMAP(sa))
				found = true
				break
			}
		}
		if !found {
			resp.NotFound = append(resp.NotFound, raw)
		}
	}
	return resp, nil
}

// -- SeenAddress/changes -------------------------------------------------

type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type changesResponse struct {
	AccountID      jmapID   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

type changesHandler struct{ h *handlerSet }

func (changesHandler) Method() string { return "SeenAddress/changes" }

func (c changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
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
	since, ok := parseStateString(req.SinceState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceState")
	}
	st, err := c.h.store.Meta().GetJMAPStates(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	newState := strconv.FormatInt(st.SeenAddress, 10)
	resp := changesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.SeenAddress {
		return resp, nil
	}
	if since > st.SeenAddress {
		return nil, protojmap.NewMethodError("cannotCalculateChanges",
			"sinceState is in the future")
	}
	createdIDs, updatedIDs, destroyedIDs, ferr := walkChangeFeed(
		ctx, c.h.store.Meta(), pid, store.EntityKindSeenAddress, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range createdIDs {
		resp.Created = append(resp.Created, seenAddrID(store.SeenAddressID(id)))
	}
	for id := range updatedIDs {
		resp.Updated = append(resp.Updated, seenAddrID(store.SeenAddressID(id)))
	}
	for id := range destroyedIDs {
		resp.Destroyed = append(resp.Destroyed, seenAddrID(store.SeenAddressID(id)))
	}
	if req.MaxChanges != nil && *req.MaxChanges > 0 {
		total := len(resp.Created) + len(resp.Updated) + len(resp.Destroyed)
		if total > *req.MaxChanges {
			resp.HasMoreChanges = true
			resp.NewState = req.SinceState
		}
	}
	return resp, nil
}

// walkChangeFeed reads the principal's change feed for SeenAddress entries,
// returning the disjoint created/updated/destroyed sets produced by entries
// with op-count > since.
func walkChangeFeed(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	kind store.EntityKind,
	since int64,
) (created, updated, destroyed map[uint64]struct{}, err error) {
	created = map[uint64]struct{}{}
	updated = map[uint64]struct{}{}
	destroyed = map[uint64]struct{}{}
	const page = 1000
	var cursor store.ChangeSeq
	opsAfter := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		batch, ferr := meta.ReadChangeFeed(ctx, pid, cursor, page)
		if ferr != nil {
			return nil, nil, nil, ferr
		}
		for _, entry := range batch {
			cursor = entry.Seq
			if entry.Kind != kind {
				continue
			}
			opsAfter++
			if opsAfter <= since {
				continue
			}
			id := entry.EntityID
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
			return created, updated, destroyed, nil
		}
	}
}

func parseStateString(s string) (int64, bool) {
	if s == "" {
		return 0, true // treat empty as "0"
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// -- SeenAddress/set -----------------------------------------------------

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type setResponse struct {
	AccountID    jmapID              `json:"accountId"`
	OldState     string              `json:"oldState"`
	NewState     string              `json:"newState"`
	Created      map[string]any      `json:"created"`
	Updated      map[jmapID]any      `json:"updated"`
	Destroyed    []jmapID            `json:"destroyed"`
	NotCreated   map[string]setError `json:"notCreated"`
	NotUpdated   map[jmapID]setError `json:"notUpdated"`
	NotDestroyed map[jmapID]setError `json:"notDestroyed"`
}

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "SeenAddress/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req setRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch",
			"ifInState does not match current state")
	}
	resp := setResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]any{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}

	// create and update are forbidden — server-only paths.
	for k := range req.Create {
		resp.NotCreated[k] = setError{
			Type:        "forbidden",
			Description: "SeenAddress rows are created server-side only",
		}
	}
	for k := range req.Update {
		resp.NotUpdated[k] = setError{
			Type:        "forbidden",
			Description: "SeenAddress rows are updated server-side only",
		}
	}

	mutated := false
	for _, rawID := range req.Destroy {
		id, ok := parseSeenAddrID(rawID)
		if !ok {
			resp.NotDestroyed[rawID] = setError{Type: "notFound"}
			continue
		}
		if err := s.h.store.Meta().DestroySeenAddress(ctx, pid, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotDestroyed[rawID] = setError{Type: "notFound"}
				continue
			}
			return nil, serverFail(err)
		}
		resp.Destroyed = append(resp.Destroyed, rawID)
		mutated = true
	}
	_ = mutated

	newState, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}
