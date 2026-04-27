package coach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// requirePrincipal pulls the authenticated principal id out of ctx.
func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := principalFromTestCtx(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

// requireAccount validates that accountId matches the calling principal.
func requireAccount(req jmapID, pid store.PrincipalID) *protojmap.MethodError {
	if req == "" {
		return nil
	}
	if req != protojmap.AccountIDForPrincipal(pid) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// serverFail wraps an internal Go error into a JMAP method-error.
func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}

// setError is the per-create/update/destroy error object per RFC 8620 §5.3.
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// -- ShortcutCoachStat/get --------------------------------------------

type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type getResponse struct {
	AccountID jmapID          `json:"accountId"`
	State     string          `json:"state"`
	List      []jmapCoachStat `json:"list"`
	NotFound  []jmapID        `json:"notFound"`
}

type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "ShortcutCoachStat/get" }

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
	now := g.h.clk.Now()

	resp := getResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapCoachStat{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		// Return all stats.
		stats, err := g.h.store.Meta().ListCoachStats(ctx, pid, now)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, s := range stats {
			resp.List = append(resp.List, renderStat(s))
		}
		return resp, nil
	}

	// Fetch only the requested IDs.
	for _, rawID := range *req.IDs {
		action, ok := coachIDFromJMAP(rawID)
		if !ok {
			resp.NotFound = append(resp.NotFound, rawID)
			continue
		}
		stat, err := g.h.store.Meta().GetCoachStat(ctx, pid, action, now)
		if err != nil {
			return nil, serverFail(err)
		}
		// A zero stat (all counters 0, no dismissal) with no underlying
		// data is treated as notFound so clients can distinguish "we
		// have a row" from "no row yet".
		if stat.KeyboardCount90d == 0 && stat.MouseCount90d == 0 &&
			stat.DismissCount == 0 && stat.DismissUntil == nil {
			resp.NotFound = append(resp.NotFound, rawID)
			continue
		}
		resp.List = append(resp.List, renderStat(stat))
	}
	return resp, nil
}

// -- ShortcutCoachStat/query ------------------------------------------

type queryRequest struct {
	AccountID string `json:"accountId"`
}

type queryResponse struct {
	AccountID           string   `json:"accountId"`
	QueryState          string   `json:"queryState"`
	CanCalculateChanges bool     `json:"canCalculateChanges"`
	Position            int      `json:"position"`
	IDs                 []jmapID `json:"ids"`
	Total               int      `json:"total"`
}

type queryHandler struct{ h *handlerSet }

func (q *queryHandler) Method() string { return "ShortcutCoachStat/query" }

func (q *queryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req queryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentState(ctx, q.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	now := q.h.clk.Now()
	stats, err := q.h.store.Meta().ListCoachStats(ctx, pid, now)
	if err != nil {
		return nil, serverFail(err)
	}
	ids := make([]jmapID, 0, len(stats))
	for _, s := range stats {
		ids = append(ids, s.Action)
	}
	return queryResponse{
		AccountID:           req.AccountID,
		QueryState:          state,
		CanCalculateChanges: false,
		Position:            0,
		IDs:                 ids,
		Total:               len(ids),
	}, nil
}

// -- ShortcutCoachStat/changes ----------------------------------------

type changesRequest struct {
	AccountID  string `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []jmapID `json:"created"`
	Updated        []jmapID `json:"updated"`
	Destroyed      []jmapID `json:"destroyed"`
}

type changesHandler struct{ h *handlerSet }

func (c *changesHandler) Method() string { return "ShortcutCoachStat/changes" }

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
	state, err := currentState(ctx, c.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	// Per REQ-PROTO-113 the suite does not subscribe to coach changes. We
	// honour the method (RFC 8620 §5.6) but return cannotCalculateChanges
	// when sinceState != currentState to keep the implementation simple
	// and correct without building a full change log.
	if req.SinceState != state {
		return nil, protojmap.NewMethodError("cannotCalculateChanges",
			"ShortcutCoachStat does not maintain a detailed change log; re-fetch with /get")
	}
	return changesResponse{
		AccountID:      req.AccountID,
		OldState:       state,
		NewState:       state,
		HasMoreChanges: false,
		Created:        []jmapID{},
		Updated:        []jmapID{},
		Destroyed:      []jmapID{},
	}, nil
}

// -- ShortcutCoachStat/set --------------------------------------------

type setRequest struct {
	AccountID string                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type setResponse struct {
	AccountID    string                   `json:"accountId"`
	OldState     string                   `json:"oldState"`
	NewState     string                   `json:"newState"`
	Created      map[string]jmapCoachStat `json:"created"`
	Updated      map[jmapID]any           `json:"updated"`
	Destroyed    []jmapID                 `json:"destroyed"`
	NotCreated   map[string]setError      `json:"notCreated"`
	NotUpdated   map[jmapID]setError      `json:"notUpdated"`
	NotDestroyed map[jmapID]setError      `json:"notDestroyed"`
}

// coachCreateInput is the wire-form per-create object. Creating a
// ShortcutCoachStat row sets its initial event and dismiss values.
type coachCreateInput struct {
	Action         string  `json:"action"`
	Keyboard       int     `json:"keyboard"`
	Mouse          int     `json:"mouse"`
	LastKeyboardAt *string `json:"lastKeyboardAt"`
	LastMouseAt    *string `json:"lastMouseAt"`
	DismissCount   int     `json:"dismissCount"`
	DismissUntil   *string `json:"dismissUntil"`
}

// coachUpdateInput is the wire-form per-update incremental patch.
// the suite sends positive integer deltas; herold accumulates events.
type coachUpdateInput struct {
	Keyboard       *int            `json:"keyboard"`
	Mouse          *int            `json:"mouse"`
	LastKeyboardAt *string         `json:"lastKeyboardAt"`
	LastMouseAt    *string         `json:"lastMouseAt"`
	DismissCount   *int            `json:"dismissCount"`
	DismissUntil   json.RawMessage `json:"dismissUntil"`
}

type setHandler struct{ h *handlerSet }

func (s *setHandler) Method() string { return "ShortcutCoachStat/set" }

func (s *setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
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
		AccountID:    req.AccountID,
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapCoachStat{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	mutated := false

	for key, raw := range req.Create {
		var in coachCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		if in.Action == "" {
			resp.NotCreated[key] = setError{Type: "invalidProperties", Properties: []string{"action"},
				Description: "action is required"}
			continue
		}
		serr, err := s.h.applyCreate(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		now := s.h.clk.Now()
		stat, err := s.h.store.Meta().GetCoachStat(ctx, pid, in.Action, now)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.Created[key] = renderStat(stat)
		mutated = true
	}

	for rawID, raw := range req.Update {
		action, ok := coachIDFromJMAP(rawID)
		if !ok {
			resp.NotUpdated[rawID] = setError{Type: "notFound"}
			continue
		}
		serr, err := s.h.applyUpdate(ctx, pid, action, raw)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotUpdated[rawID] = *serr
			continue
		}
		resp.Updated[rawID] = nil
		mutated = true
	}

	for _, rawID := range req.Destroy {
		action, ok := coachIDFromJMAP(rawID)
		if !ok {
			resp.NotDestroyed[rawID] = setError{Type: "notFound"}
			continue
		}
		if err := s.h.store.Meta().DestroyCoachStat(ctx, pid, action); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotDestroyed[rawID] = setError{Type: "notFound"}
				continue
			}
			return nil, serverFail(err)
		}
		resp.Destroyed = append(resp.Destroyed, rawID)
		mutated = true
	}

	if mutated {
		newState, err := s.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindShortcutCoach)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.NewState = strconv.FormatInt(newState, 10)
	}

	return resp, nil
}

// applyCreate handles one /set { create } entry. It appends events for
// the initial keyboard/mouse counts and upserts the dismiss row.
func (h *handlerSet) applyCreate(ctx context.Context, pid store.PrincipalID, in coachCreateInput) (*setError, error) {
	now := h.clk.Now()
	var evts []store.CoachEvent
	if in.Keyboard > 0 {
		occurredAt := now
		if in.LastKeyboardAt != nil {
			if t, err := parseUTCDate(*in.LastKeyboardAt); err == nil {
				occurredAt = t
			}
		}
		evts = append(evts, store.CoachEvent{
			PrincipalID: pid,
			Action:      in.Action,
			Method:      store.CoachInputMethodKeyboard,
			Count:       in.Keyboard,
			OccurredAt:  occurredAt,
		})
	}
	if in.Mouse > 0 {
		occurredAt := now
		if in.LastMouseAt != nil {
			if t, err := parseUTCDate(*in.LastMouseAt); err == nil {
				occurredAt = t
			}
		}
		evts = append(evts, store.CoachEvent{
			PrincipalID: pid,
			Action:      in.Action,
			Method:      store.CoachInputMethodMouse,
			Count:       in.Mouse,
			OccurredAt:  occurredAt,
		})
	}
	if len(evts) > 0 {
		if err := h.store.Meta().AppendCoachEvents(ctx, evts); err != nil {
			return nil, fmt.Errorf("coach: append events on create: %w", err)
		}
	}
	if in.DismissCount > 0 || in.DismissUntil != nil {
		d := store.CoachDismiss{
			PrincipalID:  pid,
			Action:       in.Action,
			DismissCount: in.DismissCount,
		}
		if in.DismissUntil != nil {
			if t, err := parseUTCDate(*in.DismissUntil); err == nil {
				d.DismissUntil = &t
			} else {
				return &setError{Type: "invalidProperties", Properties: []string{"dismissUntil"},
					Description: "dismissUntil must be a UTCDate string"}, nil
			}
		}
		if err := h.store.Meta().UpsertCoachDismiss(ctx, d); err != nil {
			return nil, fmt.Errorf("coach: upsert dismiss on create: %w", err)
		}
	}
	return nil, nil
}

// applyUpdate handles one /set { update } entry. Accepts incremental
// keyboard/mouse deltas and optional dismiss-field replacements.
func (h *handlerSet) applyUpdate(ctx context.Context, pid store.PrincipalID, action string, raw json.RawMessage) (*setError, error) {
	var in coachUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}
	now := h.clk.Now()
	var evts []store.CoachEvent
	if in.Keyboard != nil && *in.Keyboard > 0 {
		occurredAt := now
		if in.LastKeyboardAt != nil {
			if t, err := parseUTCDate(*in.LastKeyboardAt); err == nil {
				occurredAt = t
			}
		}
		evts = append(evts, store.CoachEvent{
			PrincipalID: pid,
			Action:      action,
			Method:      store.CoachInputMethodKeyboard,
			Count:       *in.Keyboard,
			OccurredAt:  occurredAt,
		})
	}
	if in.Mouse != nil && *in.Mouse > 0 {
		occurredAt := now
		if in.LastMouseAt != nil {
			if t, err := parseUTCDate(*in.LastMouseAt); err == nil {
				occurredAt = t
			}
		}
		evts = append(evts, store.CoachEvent{
			PrincipalID: pid,
			Action:      action,
			Method:      store.CoachInputMethodMouse,
			Count:       *in.Mouse,
			OccurredAt:  occurredAt,
		})
	}
	if len(evts) > 0 {
		if err := h.store.Meta().AppendCoachEvents(ctx, evts); err != nil {
			return nil, fmt.Errorf("coach: append events on update: %w", err)
		}
	}

	// Handle dismiss fields: if either is present, read-modify-write the
	// dismiss row.
	hasDismissUpdate := in.DismissCount != nil || len(in.DismissUntil) > 0
	if hasDismissUpdate {
		existing, err := h.store.Meta().GetCoachStat(ctx, pid, action, now)
		if err != nil {
			return nil, fmt.Errorf("coach: read stat for dismiss update: %w", err)
		}
		d := store.CoachDismiss{
			PrincipalID:  pid,
			Action:       action,
			DismissCount: existing.DismissCount,
			DismissUntil: existing.DismissUntil,
		}
		if in.DismissCount != nil {
			d.DismissCount = *in.DismissCount
		}
		if len(in.DismissUntil) > 0 {
			switch string(in.DismissUntil) {
			case "null":
				d.DismissUntil = nil
			default:
				var s string
				if err := json.Unmarshal(in.DismissUntil, &s); err != nil {
					return &setError{Type: "invalidProperties", Properties: []string{"dismissUntil"},
						Description: "dismissUntil must be a UTCDate string or null"}, nil
				}
				t, err := parseUTCDate(s)
				if err != nil {
					return &setError{Type: "invalidProperties", Properties: []string{"dismissUntil"},
						Description: "dismissUntil must be a UTCDate string"}, nil
				}
				d.DismissUntil = &t
			}
		}
		if err := h.store.Meta().UpsertCoachDismiss(ctx, d); err != nil {
			return nil, fmt.Errorf("coach: upsert dismiss on update: %w", err)
		}
	}
	return nil, nil
}

// parseUTCDate parses a JMAP UTCDate string (RFC 3339 / ISO 8601 UTC).
func parseUTCDate(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
