package managedrule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// handlerSet bundles the shared dependencies.
type handlerSet struct {
	store  store.Store
	logger *slog.Logger
}

// requirePrincipal pulls the authenticated principal from ctx.
func requirePrincipal(ctx context.Context) (store.PrincipalID, *protojmap.MethodError) {
	p, ok := principalFromTestCtx(ctx)
	if !ok || p.ID == 0 {
		return 0, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	return p.ID, nil
}

// requireAccount validates the inbound accountId.
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

// serverFail wraps an internal error into a JMAP method-error envelope.
func serverFail(err error) *protojmap.MethodError {
	if err == nil {
		return nil
	}
	return protojmap.NewMethodError("serverFail", err.Error())
}

// currentState returns the principal's current ManagedRule state string.
func (h *handlerSet) currentState(ctx context.Context, pid store.PrincipalID) (string, error) {
	st, err := h.store.Meta().GetJMAPStates(ctx, pid)
	if err != nil {
		return "", err
	}
	return stateString(st.ManagedRule), nil
}

// recompileAndPersist fetches all managed rules for pid, compiles them to a
// Sieve preamble, then re-reads the user's hand-written script, splices the
// two together, and persists the effective script. Called after any mutation
// to the managed-rule set.
func (h *handlerSet) recompileAndPersist(ctx context.Context, pid store.PrincipalID) error {
	rules, err := h.store.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
	if err != nil {
		return fmt.Errorf("managed-rule recompile: list: %w", err)
	}
	preamble, err := sieve.CompileRules(rules)
	if err != nil {
		return fmt.Errorf("managed-rule recompile: compile: %w", err)
	}
	userScript, err := h.store.Meta().GetUserSieveScript(ctx, pid)
	if err != nil {
		return fmt.Errorf("managed-rule recompile: load user script: %w", err)
	}
	effective := sieve.EffectiveScript(preamble, userScript)
	if err := h.store.Meta().SetSieveScript(ctx, pid, effective); err != nil {
		return fmt.Errorf("managed-rule recompile: persist: %w", err)
	}
	return nil
}

// -- ManagedRule/get --------------------------------------------------

type getRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type getResponse struct {
	AccountID jmapID            `json:"accountId"`
	State     string            `json:"state"`
	List      []jmapManagedRule `json:"list"`
	NotFound  []jmapID          `json:"notFound"`
}

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "ManagedRule/get" }

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
	state, err := g.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := getResponse{
		AccountID: req.AccountID,
		State:     state,
		List:      []jmapManagedRule{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// Fetch all rules.
		rules, err := g.h.store.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
		if err != nil {
			return nil, serverFail(err)
		}
		for _, r := range rules {
			resp.List = append(resp.List, ruleToWire(r))
		}
		return resp, nil
	}
	// Fetch only the requested ids.
	for _, id := range *req.IDs {
		rid, ok := ruleFromID(id)
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		r, err := g.h.store.Meta().GetManagedRule(ctx, rid, pid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, id)
				continue
			}
			return nil, serverFail(err)
		}
		resp.List = append(resp.List, ruleToWire(r))
	}
	return resp, nil
}

// -- ManagedRule/query ------------------------------------------------

type queryRequest struct {
	AccountID jmapID `json:"accountId"`
	// Phase 1: no filter support; returns all rules.
	Position int `json:"position"`
	Limit    int `json:"limit"`
}

type queryResponse struct {
	AccountID string   `json:"accountId"`
	QueryState string  `json:"queryState"`
	CanCalculateChanges bool `json:"canCalculateChanges"`
	Position  int      `json:"position"`
	IDs       []jmapID `json:"ids"`
	Total     int      `json:"total"`
}

type queryHandler struct{ h *handlerSet }

func (queryHandler) Method() string { return "ManagedRule/query" }

func (q queryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
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
	state, err := q.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	rules, err := q.h.store.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
	if err != nil {
		return nil, serverFail(err)
	}
	ids := make([]jmapID, len(rules))
	for i, r := range rules {
		ids[i] = idForRule(r.ID)
	}
	limit := req.Limit
	if limit <= 0 || limit > len(ids) {
		limit = len(ids)
	}
	start := req.Position
	if start < 0 {
		start = 0
	}
	if start > len(ids) {
		start = len(ids)
	}
	page := ids[start:]
	if limit < len(page) {
		page = page[:limit]
	}
	return queryResponse{
		AccountID:           req.AccountID,
		QueryState:          state,
		CanCalculateChanges: false,
		Position:            start,
		IDs:                 page,
		Total:               len(ids),
	}, nil
}

// -- ManagedRule/set --------------------------------------------------

type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[string]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

type setResponse struct {
	AccountID    jmapID                        `json:"accountId"`
	OldState     string                        `json:"oldState,omitempty"`
	NewState     string                        `json:"newState"`
	Created      map[string]jmapManagedRule    `json:"created,omitempty"`
	Updated      map[jmapID]*jmapManagedRule   `json:"updated,omitempty"`
	Destroyed    []jmapID                      `json:"destroyed,omitempty"`
	NotCreated   map[string]setError           `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError           `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError           `json:"notDestroyed,omitempty"`
}

func (r *setResponse) notCreated(k string, e setError) {
	if r.NotCreated == nil {
		r.NotCreated = make(map[string]setError)
	}
	r.NotCreated[k] = e
}
func (r *setResponse) notUpdated(k jmapID, e setError) {
	if r.NotUpdated == nil {
		r.NotUpdated = make(map[jmapID]setError)
	}
	r.NotUpdated[k] = e
}
func (r *setResponse) notDestroyed(k jmapID, e setError) {
	if r.NotDestroyed == nil {
		r.NotDestroyed = make(map[jmapID]setError)
	}
	r.NotDestroyed[k] = e
}

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "ManagedRule/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req setRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	oldState, err := s.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: req.AccountID,
		OldState:  oldState,
	}
	mutated := false

	for clientKey, raw := range req.Create {
		var w jmapManagedRule
		if err := json.Unmarshal(raw, &w); err != nil {
			resp.notCreated(clientKey, setError{Type: "invalidProperties", Description: err.Error()})
			continue
		}
		rule := ruleFromWire(w)
		rule.PrincipalID = pid
		inserted, err := s.h.store.Meta().InsertManagedRule(ctx, rule)
		if err != nil {
			if errors.Is(err, store.ErrInvalidArgument) {
				resp.notCreated(clientKey, setError{Type: "invalidProperties", Description: err.Error()})
				continue
			}
			return nil, serverFail(err)
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapManagedRule)
		}
		resp.Created[clientKey] = ruleToWire(inserted)
		mutated = true
	}

	for id, raw := range req.Update {
		rid, ok := ruleFromID(id)
		if !ok {
			resp.notUpdated(id, setError{Type: "notFound"})
			continue
		}
		existing, err := s.h.store.Meta().GetManagedRule(ctx, rid, pid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.notUpdated(id, setError{Type: "notFound"})
				continue
			}
			return nil, serverFail(err)
		}
		// Apply patch: unmarshal only provided fields.
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(raw, &patch); err != nil {
			resp.notUpdated(id, setError{Type: "invalidProperties", Description: err.Error()})
			continue
		}
		updated, patchErr := applyRulePatch(existing, patch)
		if patchErr != nil {
			resp.notUpdated(id, setError{Type: "invalidProperties", Description: patchErr.Error()})
			continue
		}
		saved, err := s.h.store.Meta().UpdateManagedRule(ctx, updated)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.notUpdated(id, setError{Type: "notFound"})
				continue
			}
			return nil, serverFail(err)
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapManagedRule)
		}
		wire := ruleToWire(saved)
		resp.Updated[id] = &wire
		mutated = true
	}

	for _, id := range req.Destroy {
		rid, ok := ruleFromID(id)
		if !ok {
			resp.notDestroyed(id, setError{Type: "notFound"})
			continue
		}
		if err := s.h.store.Meta().DeleteManagedRule(ctx, rid, pid); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.notDestroyed(id, setError{Type: "notFound"})
				continue
			}
			return nil, serverFail(err)
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
	}

	if mutated {
		// Recompile + persist the effective Sieve script.
		if err := s.h.recompileAndPersist(ctx, pid); err != nil {
			s.h.logger.ErrorContext(ctx, "managed-rule: recompile failed",
				slog.String("err", err.Error()))
			// We return the JMAP mutation results even if recompile fails
			// so the client state stays consistent; the error is logged.
		}
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, pid,
			store.JMAPStateKindManagedRule); err != nil {
			return nil, serverFail(fmt.Errorf("managed-rule: bump state: %w", err))
		}
	}
	newState, err := s.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

// applyRulePatch applies a partial JSON update to an existing ManagedRule.
func applyRulePatch(r store.ManagedRule, patch map[string]json.RawMessage) (store.ManagedRule, error) {
	out := r
	for k, v := range patch {
		switch k {
		case "name":
			if err := json.Unmarshal(v, &out.Name); err != nil {
				return r, fmt.Errorf("name: %w", err)
			}
		case "enabled":
			if err := json.Unmarshal(v, &out.Enabled); err != nil {
				return r, fmt.Errorf("enabled: %w", err)
			}
		case "order":
			if err := json.Unmarshal(v, &out.SortOrder); err != nil {
				return r, fmt.Errorf("order: %w", err)
			}
		case "conditions":
			var wConds []jmapCondition
			if err := json.Unmarshal(v, &wConds); err != nil {
				return r, fmt.Errorf("conditions: %w", err)
			}
			out.Conditions = make([]store.RuleCondition, len(wConds))
			for i, c := range wConds {
				out.Conditions[i] = store.RuleCondition{Field: c.Field, Op: c.Op, Value: c.Value}
			}
		case "actions":
			var wActs []jmapAction
			if err := json.Unmarshal(v, &wActs); err != nil {
				return r, fmt.Errorf("actions: %w", err)
			}
			out.Actions = make([]store.RuleAction, len(wActs))
			for i, a := range wActs {
				out.Actions[i] = store.RuleAction{Kind: a.Kind, Params: a.Params}
			}
		case "id":
			return r, fmt.Errorf("id is read-only")
		default:
			return r, fmt.Errorf("unknown property %q", k)
		}
	}
	return out, nil
}

// -- ManagedRule/changes ----------------------------------------------

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

func (changesHandler) Method() string { return "ManagedRule/changes" }

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
	newState, err := c.h.currentState(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	// Validate sinceState is a parseable integer.
	if _, err := strconv.ParseInt(req.SinceState, 10, 64); req.SinceState != "" && err != nil {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceState")
	}
	// Phase 1: we do not store per-rule change history; when the state has
	// advanced we return the full current list as "updated" and the client
	// refreshes via /get. This is conservative but correct per RFC 8620 §5.6.
	resp := changesResponse{
		AccountID: req.AccountID,
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if req.SinceState == newState {
		return resp, nil
	}
	// State has changed: surface all current rules as "updated".
	rules, err := c.h.store.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
	if err != nil {
		return nil, serverFail(err)
	}
	for _, r := range rules {
		resp.Updated = append(resp.Updated, idForRule(r.ID))
	}
	return resp, nil
}

// -- Thread/mute  / Thread/unmute ------------------------------------
// These are convenience methods that auto-generate a ManagedRule whose
// condition matches a specific thread id.

type threadMuteRequest struct {
	AccountID jmapID `json:"accountId"`
	ThreadID  string `json:"threadId"`
}

type threadMuteResponse struct {
	AccountID  jmapID `json:"accountId"`
	ManagedRuleID jmapID `json:"managedRuleId"`
}

type threadMuteHandler struct{ h *handlerSet }

func (threadMuteHandler) Method() string { return "Thread/mute" }

func (t threadMuteHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req threadMuteRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	if req.ThreadID == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "threadId is required")
	}
	// Idempotent: check if a mute rule for this thread already exists.
	existing, err := findThreadMuteRule(ctx, t.h.store, pid, req.ThreadID)
	if err != nil {
		return nil, serverFail(err)
	}
	if existing != nil {
		// Already muted — ensure it is enabled.
		if !existing.Enabled {
			existing.Enabled = true
			if _, err := t.h.store.Meta().UpdateManagedRule(ctx, *existing); err != nil {
				return nil, serverFail(err)
			}
			if err := t.h.recompileAndPersist(ctx, pid); err != nil {
				t.h.logger.ErrorContext(ctx, "Thread/mute: recompile failed", slog.String("err", err.Error()))
			}
			if _, err := t.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindManagedRule); err != nil {
				return nil, serverFail(err)
			}
		}
		return threadMuteResponse{AccountID: req.AccountID, ManagedRuleID: idForRule(existing.ID)}, nil
	}
	// Create a new mute rule.
	rule := store.ManagedRule{
		PrincipalID: pid,
		Name:        "Mute thread " + req.ThreadID,
		Enabled:     true,
		SortOrder:   0,
		Conditions: []store.RuleCondition{
			{Field: "thread-id", Op: "equals", Value: req.ThreadID},
		},
		Actions: []store.RuleAction{
			{Kind: "skip-inbox"},
			{Kind: "mark-read"},
		},
	}
	inserted, err := t.h.store.Meta().InsertManagedRule(ctx, rule)
	if err != nil {
		return nil, serverFail(err)
	}
	if err := t.h.recompileAndPersist(ctx, pid); err != nil {
		t.h.logger.ErrorContext(ctx, "Thread/mute: recompile failed", slog.String("err", err.Error()))
	}
	if _, err := t.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindManagedRule); err != nil {
		return nil, serverFail(fmt.Errorf("Thread/mute: bump state: %w", err))
	}
	return threadMuteResponse{AccountID: req.AccountID, ManagedRuleID: idForRule(inserted.ID)}, nil
}

type threadUnmuteHandler struct{ h *handlerSet }

func (threadUnmuteHandler) Method() string { return "Thread/unmute" }

func (t threadUnmuteHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req threadMuteRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	if req.ThreadID == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "threadId is required")
	}
	existing, err := findThreadMuteRule(ctx, t.h.store, pid, req.ThreadID)
	if err != nil {
		return nil, serverFail(err)
	}
	if existing == nil || !existing.Enabled {
		// Already unmuted — idempotent.
		return struct {
			AccountID jmapID `json:"accountId"`
		}{AccountID: req.AccountID}, nil
	}
	existing.Enabled = false
	if _, err := t.h.store.Meta().UpdateManagedRule(ctx, *existing); err != nil {
		return nil, serverFail(err)
	}
	if err := t.h.recompileAndPersist(ctx, pid); err != nil {
		t.h.logger.ErrorContext(ctx, "Thread/unmute: recompile failed", slog.String("err", err.Error()))
	}
	if _, err := t.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindManagedRule); err != nil {
		return nil, serverFail(err)
	}
	return struct {
		AccountID jmapID `json:"accountId"`
	}{AccountID: req.AccountID}, nil
}

// findThreadMuteRule scans the principal's rules for a thread-mute rule
// targeting the given thread id. Returns nil when none exists.
func findThreadMuteRule(ctx context.Context, st store.Store, pid store.PrincipalID, threadID string) (*store.ManagedRule, error) {
	rules, err := st.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
	if err != nil {
		return nil, err
	}
	for i := range rules {
		r := &rules[i]
		if len(r.Conditions) == 1 &&
			r.Conditions[0].Field == "thread-id" &&
			r.Conditions[0].Value == threadID {
			return r, nil
		}
	}
	return nil, nil
}

// -- BlockedSender/set -----------------------------------------------

type blockedSenderSetRequest struct {
	AccountID jmapID `json:"accountId"`
	Address   string `json:"address"`
}

type blockedSenderSetResponse struct {
	AccountID     jmapID `json:"accountId"`
	ManagedRuleID jmapID `json:"managedRuleId"`
}

type blockedSenderSetHandler struct{ h *handlerSet }

func (blockedSenderSetHandler) Method() string { return "BlockedSender/set" }

func (b blockedSenderSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req blockedSenderSetRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	if req.Address == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "address is required")
	}
	// Idempotent: if a block rule for this address already exists, return it.
	existing, err := findBlockedSenderRule(ctx, b.h.store, pid, req.Address)
	if err != nil {
		return nil, serverFail(err)
	}
	if existing != nil {
		if !existing.Enabled {
			existing.Enabled = true
			if _, err := b.h.store.Meta().UpdateManagedRule(ctx, *existing); err != nil {
				return nil, serverFail(err)
			}
			if err := b.h.recompileAndPersist(ctx, pid); err != nil {
				b.h.logger.ErrorContext(ctx, "BlockedSender/set: recompile failed", slog.String("err", err.Error()))
			}
			if _, err := b.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindManagedRule); err != nil {
				return nil, serverFail(err)
			}
		}
		return blockedSenderSetResponse{AccountID: req.AccountID, ManagedRuleID: idForRule(existing.ID)}, nil
	}
	rule := store.ManagedRule{
		PrincipalID: pid,
		Name:        "Block " + req.Address,
		Enabled:     true,
		SortOrder:   0,
		Conditions: []store.RuleCondition{
			{Field: "from", Op: "equals", Value: req.Address},
		},
		Actions: []store.RuleAction{
			{Kind: "delete"},
		},
	}
	inserted, err := b.h.store.Meta().InsertManagedRule(ctx, rule)
	if err != nil {
		return nil, serverFail(err)
	}
	if err := b.h.recompileAndPersist(ctx, pid); err != nil {
		b.h.logger.ErrorContext(ctx, "BlockedSender/set: recompile failed", slog.String("err", err.Error()))
	}
	if _, err := b.h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindManagedRule); err != nil {
		return nil, serverFail(fmt.Errorf("BlockedSender/set: bump state: %w", err))
	}
	return blockedSenderSetResponse{AccountID: req.AccountID, ManagedRuleID: idForRule(inserted.ID)}, nil
}

// findBlockedSenderRule scans for a block-sender rule for the given address.
func findBlockedSenderRule(ctx context.Context, st store.Store, pid store.PrincipalID, addr string) (*store.ManagedRule, error) {
	rules, err := st.Meta().ListManagedRules(ctx, pid, store.ManagedRuleFilter{})
	if err != nil {
		return nil, err
	}
	for i := range rules {
		r := &rules[i]
		if len(r.Conditions) == 1 &&
			r.Conditions[0].Field == "from" &&
			r.Conditions[0].Value == addr &&
			len(r.Actions) == 1 &&
			r.Actions[0].Kind == "delete" {
			return r, nil
		}
	}
	return nil, nil
}
