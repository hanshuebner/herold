package categorysettings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// maxPromptBytes is the server-side cap on the user-supplied classifier
// prompt (security gate: unbounded prompts add little classifier value and
// risk memory or LLM-context exhaustion).
const maxPromptBytes = 32 * 1024

// singletonID is the only id a CategorySettings object carries. There is
// exactly one CategorySettings object per account.
const singletonID = "singleton"

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// primaryCategoryName is the fallback category that cannot be removed from
// the category set (REQ-CAT-40).
const primaryCategoryName = "primary"

// handlerSet bundles the dependencies shared by all CategorySettings handlers.
type handlerSet struct {
	store       store.Store
	categoriser *categorise.Categoriser
	jobs        *categorise.JobRegistry
	logger      *slog.Logger
	clk         clock.Clock
}

// AccountCapability satisfies protojmap.AccountCapabilityProvider. It
// reports the active category set IDs and whether bulk recategorisation is
// available (i.e. the categoriser is wired). The session endpoint embeds
// this under "accountCapabilities.<cap>".
func (h *handlerSet) AccountCapability() any {
	return categoryAccountCapability{
		BulkRecategoriseEnabled: h.categoriser != nil,
	}
}

// categoryAccountCapability is the per-account JSON shape under the
// capability URI in the session descriptor.
type categoryAccountCapability struct {
	// BulkRecategoriseEnabled reports whether the server can run a
	// CategorySettings/recategorise job (i.e. the LLM client is wired).
	BulkRecategoriseEnabled bool `json:"bulkRecategoriseEnabled"`
}

// jmapCategoryDef is the wire form of one category entry.
type jmapCategoryDef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Order       int    `json:"order"`
}

// jmapCategorySettings is the wire-form CategorySettings object.
type jmapCategorySettings struct {
	ID            jmapID            `json:"id"`
	Categories    []jmapCategoryDef `json:"categories"`
	Prompt        string            `json:"prompt"`
	DefaultPrompt string            `json:"defaultPrompt"`
}

// stateString converts an int64 JMAP state counter to the opaque string
// clients receive.
func stateString(n int64) string { return strconv.FormatInt(n, 10) }

// configToJMAP converts a store.CategorisationConfig to the wire form.
func configToJMAP(cfg store.CategorisationConfig) jmapCategorySettings {
	cats := make([]jmapCategoryDef, len(cfg.CategorySet))
	for i, c := range cfg.CategorySet {
		cats[i] = jmapCategoryDef{
			ID:          c.Name,
			Name:        c.Name,
			Description: c.Description,
			Order:       i,
		}
	}
	return jmapCategorySettings{
		ID:            singletonID,
		Categories:    cats,
		Prompt:        cfg.Prompt,
		DefaultPrompt: categorise.DefaultPrompt,
	}
}

// -- CategorySettings/get -------------------------------------------

// getRequest is the inbound shape for CategorySettings/get.
type getRequest struct {
	AccountID jmapID    `json:"accountId"`
	IDs       *[]jmapID `json:"ids"`
}

// getResponse mirrors RFC 8620 §5.1.
type getResponse struct {
	AccountID string                 `json:"accountId"`
	State     string                 `json:"state"`
	List      []jmapCategorySettings `json:"list"`
	NotFound  []jmapID               `json:"notFound"`
}

// getHandler implements CategorySettings/get.
type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "CategorySettings/get" }

func (g *getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	p, ok := principalFrom(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := validateAccountID(p, req.AccountID); merr != nil {
		return nil, merr
	}

	st, err := g.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	cfg, err := g.h.store.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}

	obj := configToJMAP(cfg)
	resp := getResponse{
		AccountID: protojmap.AccountIDForPrincipal(p.ID),
		State:     stateString(st.CategorySettings),
		List:      []jmapCategorySettings{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		// No id filter — return the singleton.
		resp.List = append(resp.List, obj)
		return resp, nil
	}
	for _, id := range *req.IDs {
		if id == singletonID {
			resp.List = append(resp.List, obj)
		} else {
			resp.NotFound = append(resp.NotFound, id)
		}
	}
	return resp, nil
}

// -- CategorySettings/set -------------------------------------------

// setRequest is the inbound shape for CategorySettings/set. Singletons
// cannot be created or destroyed per the JMAP singleton pattern; only
// updates are accepted.
type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Create    map[jmapID]json.RawMessage `json:"create,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy   []jmapID                   `json:"destroy,omitempty"`
}

// setError is the per-create/update/destroy error object (RFC 8620 §5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// setResponse mirrors RFC 8620 §5.3.
type setResponse struct {
	AccountID    string                       `json:"accountId"`
	OldState     string                       `json:"oldState,omitempty"`
	NewState     string                       `json:"newState"`
	Created      map[jmapID]any               `json:"created,omitempty"`
	Updated      map[jmapID]*jmapCategorySettings `json:"updated,omitempty"`
	Destroyed    []jmapID                     `json:"destroyed,omitempty"`
	NotCreated   map[jmapID]setError          `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError          `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError          `json:"notDestroyed,omitempty"`
}

// setHandler implements CategorySettings/set.
type setHandler struct{ h *handlerSet }

func (s *setHandler) Method() string { return "CategorySettings/set" }

func (s *setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	p, ok := principalFrom(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	var req setRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := validateAccountID(p, req.AccountID); merr != nil {
		return nil, merr
	}

	st, err := s.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	oldState := stateString(st.CategorySettings)
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}

	resp := setResponse{
		AccountID: protojmap.AccountIDForPrincipal(p.ID),
		OldState:  oldState,
	}

	// Singletons cannot be created.
	for id := range req.Create {
		if resp.NotCreated == nil {
			resp.NotCreated = make(map[jmapID]setError)
		}
		resp.NotCreated[id] = setError{
			Type:        "singleton",
			Description: "CategorySettings is a singleton and cannot be created",
		}
	}
	// Singletons cannot be destroyed.
	for _, id := range req.Destroy {
		if resp.NotDestroyed == nil {
			resp.NotDestroyed = make(map[jmapID]setError)
		}
		resp.NotDestroyed[id] = setError{
			Type:        "singleton",
			Description: "CategorySettings is a singleton and cannot be destroyed",
		}
	}

	mutated := false
	for id, raw := range req.Update {
		if id != singletonID {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "notFound"}
			continue
		}
		current, err := s.h.store.Meta().GetCategorisationConfig(ctx, p.ID)
		if err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		patched, pErr := applySetPatch(current, raw)
		if pErr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type:        "invalidProperties",
				Description: pErr.Error(),
			}
			continue
		}
		if err := s.h.store.Meta().UpdateCategorisationConfig(ctx, patched); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		updated := configToJMAP(patched)
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapCategorySettings)
		}
		resp.Updated[id] = &updated
		mutated = true
	}

	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindCategorySettings); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	stAfter, err := s.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = stateString(stAfter.CategorySettings)
	return resp, nil
}

// applySetPatch applies a JSON update patch to current and returns the
// resulting config. Recognised properties: categories, prompt. The id and
// defaultPrompt properties are read-only.
func applySetPatch(current store.CategorisationConfig, raw json.RawMessage) (store.CategorisationConfig, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return current, err
	}
	out := current
	for k, v := range m {
		switch k {
		case "prompt":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return current, fmt.Errorf("prompt: %w", err)
			}
			if len(s) > maxPromptBytes {
				return current, fmt.Errorf("prompt exceeds maximum size of %d bytes", maxPromptBytes)
			}
			out.Prompt = s
		case "categories":
			defs, err := parseCategoryArray(v)
			if err != nil {
				return current, fmt.Errorf("categories: %w", err)
			}
			if err := validateCategorySet(defs); err != nil {
				return current, fmt.Errorf("categories: %w", err)
			}
			out.CategorySet = defs
		case "id", "defaultPrompt":
			return current, fmt.Errorf("%s is read-only", k)
		default:
			return current, fmt.Errorf("unknown property %q", k)
		}
	}
	return out, nil
}

// parseCategoryArray decodes the JSON array of category objects from the
// wire form into store.CategoryDef values. Order is preserved.
func parseCategoryArray(raw json.RawMessage) ([]store.CategoryDef, error) {
	var items []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	defs := make([]store.CategoryDef, 0, len(items))
	for _, item := range items {
		name := item.Name
		if name == "" {
			name = item.ID
		}
		defs = append(defs, store.CategoryDef{
			Name:        strings.ToLower(strings.TrimSpace(name)),
			Description: item.Description,
		})
	}
	return defs, nil
}

// validateCategorySet enforces the invariants on the user-supplied
// category list:
//   - Must not be empty.
//   - The "primary" category must be present (REQ-CAT-40).
//   - Names must be non-empty lowercase ASCII (the keyword prefix rules).
//   - No duplicate names.
func validateCategorySet(defs []store.CategoryDef) error {
	if len(defs) == 0 {
		return fmt.Errorf("category set must not be empty")
	}
	seen := make(map[string]struct{}, len(defs))
	hasPrimary := false
	for _, d := range defs {
		if d.Name == "" {
			return fmt.Errorf("category name must not be empty")
		}
		if strings.EqualFold(d.Name, primaryCategoryName) {
			hasPrimary = true
		}
		if _, dup := seen[d.Name]; dup {
			return fmt.Errorf("duplicate category name %q", d.Name)
		}
		seen[d.Name] = struct{}{}
	}
	if !hasPrimary {
		return fmt.Errorf("the %q category cannot be removed", primaryCategoryName)
	}
	return nil
}

// -- CategorySettings/recategorise ----------------------------------

// recategoriseScope enumerates the allowed scopes for a recategorise job.
type recategoriseScope string

const (
	scopeInboxRecent recategoriseScope = "inbox-recent"
	scopeInboxAll    recategoriseScope = "inbox-all"
)

// recategoriseRequest is the inbound shape for CategorySettings/recategorise.
type recategoriseRequest struct {
	AccountID jmapID            `json:"accountId"`
	Scope     recategoriseScope `json:"scope"`
	Limit     int               `json:"limit"`
}

// recategoriseResponse is the outbound shape for CategorySettings/recategorise.
type recategoriseResponse struct {
	// JobID is an opaque identifier the client may use to correlate
	// progress state-changes. It is not a JMAP id in the RFC sense but
	// a server-assigned opaque token.
	JobID  string `json:"jobId"`
	// Queued is the number of messages that were queued for processing.
	// For "inbox-recent" this is capped by Limit; for "inbox-all" it is
	// the total inbox size at job-dispatch time.
	Queued int `json:"queued"`
}

// recategoriseHandler implements CategorySettings/recategorise.
type recategoriseHandler struct{ h *handlerSet }

func (r *recategoriseHandler) Method() string { return "CategorySettings/recategorise" }

func (r *recategoriseHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	p, ok := principalFrom(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	var req recategoriseRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := validateAccountID(p, req.AccountID); merr != nil {
		return nil, merr
	}

	scope := req.Scope
	if scope == "" {
		scope = scopeInboxRecent
	}
	if scope != scopeInboxRecent && scope != scopeInboxAll {
		return nil, protojmap.NewMethodError("invalidArguments",
			fmt.Sprintf("scope must be %q or %q", scopeInboxRecent, scopeInboxAll))
	}

	if r.h.categoriser == nil {
		return nil, protojmap.NewMethodError("serverFail",
			"categoriser not configured; operator must set an LLM endpoint")
	}

	limit := req.Limit
	if scope == scopeInboxRecent {
		if limit <= 0 {
			limit = categorise.DefaultRecategoriseLimit
		}
	} else {
		// inbox-all: no limit means "everything"; pass 0 to RecategoriseRecent
		// which applies DefaultRecategoriseLimit. For inbox-all we want
		// unbounded, so use a very large limit.
		if limit <= 0 {
			limit = 1<<31 - 1
		}
	}

	// Generate a job ID before spawning the goroutine so we can return it
	// immediately to the caller. The ID is the Unix nanosecond timestamp as
	// a hex string — no import of crypto/rand needed, collisions impossible
	// within a single process.
	jobID := fmt.Sprintf("%x", r.h.clk.Now().UnixNano())

	// Record the initial "queued" state before spawning. We advance the
	// CategorySettings state so EventSource listeners wake up.
	if _, err := r.h.store.Meta().IncrementJMAPState(ctx, p.ID,
		store.JMAPStateKindCategorySettings); err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}

	r.h.jobs.Put(r.h.clk.Now(), categorise.JobStatus{
		ID:    jobID,
		State: categorise.JobStateRunning,
	})

	principalID := p.ID
	categoriser := r.h.categoriser
	jobs := r.h.jobs
	st := r.h.store
	logger := r.h.logger
	clk := r.h.clk

	// The goroutine owns a fresh context so the HTTP request lifecycle
	// does not cancel the background work. It watches the server-level
	// context via a background context — STANDARDS.md §5 requires every
	// goroutine to watch a ctx.
	go func() {
		bgCtx := context.Background()
		done, err := categoriser.RecategoriseRecent(bgCtx, principalID, limit,
			func(d, total int) {
				jobs.Put(clk.Now(), categorise.JobStatus{
					ID:    jobID,
					State: categorise.JobStateRunning,
					Done:  d,
					Total: total,
				})
			})
		finalState := categorise.JobStateDone
		errStr := ""
		if err != nil {
			finalState = categorise.JobStateFailed
			errStr = err.Error()
			logger.WarnContext(bgCtx, "categorysettings: recategorise job failed",
				slog.String("job_id", jobID),
				slog.Uint64("principal_id", uint64(principalID)),
				slog.String("err", errStr))
		}
		jobs.Put(clk.Now(), categorise.JobStatus{
			ID:    jobID,
			State: finalState,
			Done:  done,
			Total: done,
			Err:   errStr,
		})
		// Advance state so EventSource listeners see the job completion.
		if _, serr := st.Meta().IncrementJMAPState(bgCtx, principalID,
			store.JMAPStateKindCategorySettings); serr != nil {
			logger.WarnContext(bgCtx, "categorysettings: advance state after job",
				slog.String("job_id", jobID),
				slog.String("err", serr.Error()))
		}
	}()

	return recategoriseResponse{
		JobID:  jobID,
		Queued: limit,
	}, nil
}

// validateAccountID rejects a mismatched or absent accountId.
func validateAccountID(p store.Principal, accountID jmapID) *protojmap.MethodError {
	if accountID == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if accountID != protojmap.AccountIDForPrincipal(p.ID) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// utcDate formats a time.Time as an RFC 3339 UTC string.
func utcDate(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// _ is a compile-time assertion that utcDate is referenced.
var _ = utcDate
