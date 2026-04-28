package emailsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/auth/sendpolicy"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// IdentityResolver resolves a JMAP Identity id back to its email
// address. Production wires the identity package's Store via a small
// adapter; tests inject a stub. The resolver returns ok=false when the
// id is not owned by the principal — EmailSubmission/set then refuses
// the submission with "invalidProperties".
type IdentityResolver interface {
	IdentityEmail(ctx context.Context, p store.Principal, id jmapID) (email string, ok bool)
}

// handlerSet binds the EmailSubmission handlers to their dependencies.
type handlerSet struct {
	store    store.Store
	queue    Submitter
	clk      clock.Clock
	identity IdentityResolver
}

func stateString(seq int64) string { return strconv.FormatInt(seq, 10) }

// accountIDForPrincipal returns the canonical wire-form accountId for p
// (e.g. "a42") matching the session descriptor's primaryAccounts entry.
// Sharing protojmap.AccountIDForPrincipal keeps the format in sync with
// the rest of the JMAP surface.
func accountIDForPrincipal(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// validateAccountID checks the inbound accountId against the
// authenticated principal. An absent accountId is rejected with
// "invalidArguments" per RFC 8620 §5.1; a mismatched one returns
// "accountNotFound".
func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if requested != accountIDForPrincipal(p) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// listSubmissions returns every EmailSubmission row for the principal
// ordered by SendAt ascending. Joins the persisted EmailSubmission rows
// to the queue rows that share their EnvelopeID for live undoStatus /
// deliveryStatus rendering.
func (h *handlerSet) listSubmissions(ctx context.Context, p store.Principal) ([]jmapEmailSubmission, error) {
	rows, err := h.store.Meta().ListEmailSubmissions(ctx, p.ID, store.EmailSubmissionFilter{Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make([]jmapEmailSubmission, 0, len(rows))
	for _, r := range rows {
		j, err := h.renderSubmission(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SendAt < out[j].SendAt })
	return out, nil
}

// renderSubmission joins an EmailSubmissionRow to its queue rows and
// projects the result into the wire-form jmapEmailSubmission. When the
// queue rows have already been GC'd (terminal-state destroy path),
// rowToJMAP receives an empty slice — the submission renders with no
// recipients and a synthesised "final" undoStatus.
func (h *handlerSet) renderSubmission(ctx context.Context, r store.EmailSubmissionRow) (jmapEmailSubmission, error) {
	qrows, err := h.store.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: r.EnvelopeID})
	if err != nil {
		return jmapEmailSubmission{}, err
	}
	return rowToJMAP(qrows, r.IdentityID, renderEmailID(r.EmailID), r.ThreadID), nil
}

// -- EmailSubmission/get ---------------------------------------------

type getRequest struct {
	AccountID jmapID    `json:"accountId"`
	IDs       *[]jmapID `json:"ids"`
}

type getResponse struct {
	AccountID string                `json:"accountId"`
	State     string                `json:"state"`
	List      []jmapEmailSubmission `json:"list"`
	NotFound  []jmapID              `json:"notFound"`
}

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "EmailSubmission/get" }

func (g getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	st, err := g.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	all, err := g.h.listSubmissions(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		State:     stateString(st.EmailSubmission),
		List:      []jmapEmailSubmission{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		resp.List = append(resp.List, all...)
		return resp, nil
	}
	byID := make(map[jmapID]jmapEmailSubmission, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	for _, id := range *req.IDs {
		s, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		resp.List = append(resp.List, s)
	}
	return resp, nil
}

// -- EmailSubmission/changes -----------------------------------------

type changesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges,omitempty"`
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

func (changesHandler) Method() string { return "EmailSubmission/changes" }

func (c changesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req changesRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	st, err := c.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	now := stateString(st.EmailSubmission)
	resp := changesResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  req.SinceState,
		NewState:  now,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if req.SinceState == now {
		return resp, nil
	}
	all, err := c.h.listSubmissions(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	for _, s := range all {
		resp.Updated = append(resp.Updated, s.ID)
	}
	return resp, nil
}

// -- EmailSubmission/query -------------------------------------------

type queryFilter struct {
	IdentityIDs []jmapID `json:"identityIds,omitempty"`
	EmailIDs    []jmapID `json:"emailIds,omitempty"`
	ThreadIDs   []jmapID `json:"threadIds,omitempty"`
	UndoStatus  string   `json:"undoStatus,omitempty"`
	Before      string   `json:"before,omitempty"`
	After       string   `json:"after,omitempty"`
}

type queryRequest struct {
	AccountID      jmapID           `json:"accountId"`
	Filter         *queryFilter     `json:"filter,omitempty"`
	Sort           []sortComparator `json:"sort,omitempty"`
	Position       int              `json:"position,omitempty"`
	Limit          *int             `json:"limit,omitempty"`
	CalculateTotal bool             `json:"calculateTotal,omitempty"`
}

type sortComparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending,omitempty"`
}

type queryResponse struct {
	AccountID      string   `json:"accountId"`
	QueryState     string   `json:"queryState"`
	CanCalcChanges bool     `json:"canCalculateChanges"`
	Position       int      `json:"position"`
	IDs            []jmapID `json:"ids"`
	Total          *int     `json:"total,omitempty"`
}

type queryHandler struct{ h *handlerSet }

func (queryHandler) Method() string { return "EmailSubmission/query" }

func (q queryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req queryRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	st, err := q.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	all, err := q.h.listSubmissions(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	filtered := all[:0]
	for _, s := range all {
		if req.Filter != nil && !filterMatches(req.Filter, s) {
			continue
		}
		filtered = append(filtered, s)
	}
	// Default sort: sendAt ascending.
	sortSubmissions(filtered, req.Sort)
	resp := queryResponse{
		AccountID:  accountIDForPrincipal(p),
		QueryState: stateString(st.EmailSubmission),
		Position:   req.Position,
		IDs:        []jmapID{},
	}
	limit := len(filtered)
	if req.Limit != nil && *req.Limit < limit {
		limit = *req.Limit
	}
	for i := req.Position; i < limit && i < len(filtered); i++ {
		resp.IDs = append(resp.IDs, filtered[i].ID)
	}
	if req.CalculateTotal {
		t := len(filtered)
		resp.Total = &t
	}
	return resp, nil
}

func filterMatches(f *queryFilter, s jmapEmailSubmission) bool {
	if len(f.IdentityIDs) > 0 && !contains(f.IdentityIDs, s.IdentityID) {
		return false
	}
	if len(f.EmailIDs) > 0 && !contains(f.EmailIDs, s.EmailID) {
		return false
	}
	if len(f.ThreadIDs) > 0 && !contains(f.ThreadIDs, s.ThreadID) {
		return false
	}
	if f.UndoStatus != "" && string(s.UndoStatus) != f.UndoStatus {
		return false
	}
	if f.After != "" && s.SendAt < f.After {
		return false
	}
	if f.Before != "" && s.SendAt >= f.Before {
		return false
	}
	return true
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sortSubmissions(list []jmapEmailSubmission, comparators []sortComparator) {
	if len(comparators) == 0 {
		sort.Slice(list, func(i, j int) bool { return list[i].SendAt < list[j].SendAt })
		return
	}
	sort.Slice(list, func(i, j int) bool {
		for _, c := range comparators {
			cmp := compareSubmission(list[i], list[j], c.Property)
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			if cmp == 0 {
				continue
			}
			if !asc {
				cmp = -cmp
			}
			return cmp < 0
		}
		return false
	})
}

func compareSubmission(a, b jmapEmailSubmission, property string) int {
	switch property {
	case "emailId":
		return strings.Compare(a.EmailID, b.EmailID)
	case "threadId":
		return strings.Compare(a.ThreadID, b.ThreadID)
	case "sentAt", "sendAt":
		return strings.Compare(a.SendAt, b.SendAt)
	}
	return 0
}

// -- EmailSubmission/queryChanges ------------------------------------

type queryChangesRequest struct {
	AccountID       jmapID           `json:"accountId"`
	Filter          *queryFilter     `json:"filter,omitempty"`
	Sort            []sortComparator `json:"sort,omitempty"`
	SinceQueryState string           `json:"sinceQueryState"`
	MaxChanges      *int             `json:"maxChanges,omitempty"`
}

type queryChangesResponse struct {
	AccountID     string      `json:"accountId"`
	OldQueryState string      `json:"oldQueryState"`
	NewQueryState string      `json:"newQueryState"`
	Total         *int        `json:"total,omitempty"`
	Removed       []jmapID    `json:"removed"`
	Added         []addedItem `json:"added"`
}

type addedItem struct {
	ID    jmapID `json:"id"`
	Index int    `json:"index"`
}

type queryChangesHandler struct{ h *handlerSet }

func (queryChangesHandler) Method() string { return "EmailSubmission/queryChanges" }

func (q queryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req queryChangesRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	st, err := q.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	now := stateString(st.EmailSubmission)
	resp := queryChangesResponse{
		AccountID:     accountIDForPrincipal(p),
		OldQueryState: req.SinceQueryState,
		NewQueryState: now,
		Removed:       []jmapID{},
		Added:         []addedItem{},
	}
	if req.SinceQueryState == now {
		return resp, nil
	}
	// Without a per-row history we conservatively report
	// cannotCalculateChanges; clients re-issue Email/query.
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"EmailSubmission/queryChanges is not supported for arbitrary intervals")
}

// -- EmailSubmission/set ---------------------------------------------

type setRequest struct {
	AccountID             jmapID                     `json:"accountId"`
	IfInState             *string                    `json:"ifInState,omitempty"`
	Create                map[string]json.RawMessage `json:"create,omitempty"`
	Update                map[jmapID]json.RawMessage `json:"update,omitempty"`
	Destroy               []jmapID                   `json:"destroy,omitempty"`
	OnSuccessUpdateEmail  map[jmapID]json.RawMessage `json:"onSuccessUpdateEmail,omitempty"`
	OnSuccessDestroyEmail []jmapID                   `json:"onSuccessDestroyEmail,omitempty"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type setResponse struct {
	AccountID    string                          `json:"accountId"`
	OldState     string                          `json:"oldState,omitempty"`
	NewState     string                          `json:"newState"`
	Created      map[string]jmapEmailSubmission  `json:"created,omitempty"`
	Updated      map[jmapID]*jmapEmailSubmission `json:"updated,omitempty"`
	Destroyed    []jmapID                        `json:"destroyed,omitempty"`
	NotCreated   map[string]setError             `json:"notCreated,omitempty"`
	NotUpdated   map[jmapID]setError             `json:"notUpdated,omitempty"`
	NotDestroyed map[jmapID]setError             `json:"notDestroyed,omitempty"`
}

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "EmailSubmission/set" }

func (s setHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	var req setRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	p, ok := principalFor(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	if e := validateAccountID(p, req.AccountID); e != nil {
		return nil, e
	}
	st, err := s.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	oldState := stateString(st.EmailSubmission)
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  oldState,
	}
	mutated := false
	for clientID, raw := range req.Create {
		created, perr := s.processCreate(ctx, p, raw)
		if perr != nil {
			if resp.NotCreated == nil {
				resp.NotCreated = make(map[string]setError)
			}
			resp.NotCreated[clientID] = *perr
			continue
		}
		if resp.Created == nil {
			resp.Created = make(map[string]jmapEmailSubmission)
		}
		resp.Created[clientID] = created
		mutated = true
	}
	// EmailSubmission/set update is constrained per RFC 8621 §5.4: only
	// undoStatus may transition to "canceled". We surface a forbidden
	// SetError for any other key.
	for id, raw := range req.Update {
		uerr := s.processUpdate(ctx, p, id, raw)
		if uerr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = *uerr
			continue
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapEmailSubmission)
		}
		resp.Updated[id] = nil
		mutated = true
	}
	// Destroys delete the EmailSubmission row but DO NOT recall the
	// already-delivered mail. We only allow destroying rows that have
	// reached a terminal state (RFC 8621 §5.5).
	for _, id := range req.Destroy {
		derr := s.processDestroy(ctx, p, id)
		if derr != nil {
			if resp.NotDestroyed == nil {
				resp.NotDestroyed = make(map[jmapID]setError)
			}
			resp.NotDestroyed[id] = *derr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, id)
		mutated = true
	}
	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindEmailSubmission); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	stAfter, err := s.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = stateString(stAfter.EmailSubmission)

	// onSuccessUpdateEmail: RFC 8621 §7.5 implicit Email/set.
	// For each successful create entry referenced in onSuccessUpdateEmail,
	// apply the given patch to the associated email and append an implicit
	// Email/set response.
	if len(req.OnSuccessUpdateEmail) > 0 && len(resp.Created) > 0 {
		implicitResp, mErr := s.h.applyOnSuccessUpdateEmail(ctx, p, req.OnSuccessUpdateEmail, resp.Created)
		if mErr != nil {
			// Treat as serverFail rather than rolling back the submission.
			return resp, nil
		}
		if implicitResp != nil {
			primaryBytes, err := json.Marshal(resp)
			if err != nil {
				return resp, nil
			}
			extraBytes, err := json.Marshal(implicitResp)
			if err != nil {
				return resp, nil
			}
			return protojmap.MultipleInvocations{
				Primary: primaryBytes,
				Extra: []protojmap.Invocation{
					{Name: "Email/set", Args: extraBytes},
				},
			}, nil
		}
	}
	return resp, nil
}

// implicitEmailSetResponse is the wire form of the implicit Email/set
// triggered by onSuccessUpdateEmail (RFC 8621 §7.5).
type implicitEmailSetResponse struct {
	AccountID    string                     `json:"accountId"`
	OldState     string                     `json:"oldState,omitempty"`
	NewState     string                     `json:"newState"`
	Updated      map[jmapID]*implicitUpdate `json:"updated,omitempty"`
	NotUpdated   map[jmapID]setError        `json:"notUpdated,omitempty"`
	Created      map[jmapID]any             `json:"created,omitempty"`
	Destroyed    []jmapID                   `json:"destroyed,omitempty"`
	NotCreated   map[jmapID]setError        `json:"notCreated,omitempty"`
	NotDestroyed map[jmapID]setError        `json:"notDestroyed,omitempty"`
}

type implicitUpdate struct{} // null in JSON — update returned as null per RFC 8620

func (u *implicitUpdate) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// applyOnSuccessUpdateEmail applies the onSuccessUpdateEmail patch to each
// successfully created submission's email. Returns the implicit Email/set
// response or nil if nothing to do.
func (h *handlerSet) applyOnSuccessUpdateEmail(
	ctx context.Context,
	p store.Principal,
	patches map[jmapID]json.RawMessage,
	created map[string]jmapEmailSubmission,
) (*implicitEmailSetResponse, *protojmap.MethodError) {
	st, err := h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	oldEmailState := strconv.FormatInt(st.Email, 10)
	resp := &implicitEmailSetResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  oldEmailState,
	}
	mutated := false
	for ref, patch := range patches {
		// Resolve "#clientID" reference to the created submission.
		clientID := strings.TrimPrefix(ref, "#")
		sub, ok := created[clientID]
		if !ok {
			// Reference doesn't match any created submission; skip.
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[ref] = setError{Type: "notFound",
				Description: "creation reference " + ref + " not found in this request's created submissions"}
			continue
		}
		emailID := sub.EmailID
		mid, ok := parseEmailID(emailID)
		if !ok {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[emailID] = setError{Type: "notFound"}
			continue
		}
		msg, err := h.store.Meta().GetMessage(ctx, mid)
		if err != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[emailID] = setError{Type: "notFound"}
			continue
		}
		// Parse the patch as a flat JSON object and apply.
		if perr := h.applyEmailPatch(ctx, p, msg, patch); perr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[emailID] = *perr
			continue
		}
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*implicitUpdate)
		}
		resp.Updated[emailID] = nil
		mutated = true
	}
	if mutated {
		if _, err := h.store.Meta().IncrementJMAPState(ctx, p.ID, store.JMAPStateKindEmail); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	stAfter, err := h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = strconv.FormatInt(stAfter.Email, 10)
	return resp, nil
}

// applyEmailPatch applies a flat Email/set patch to a message, handling
// mailboxIds and keywords partial patches per RFC 8621 §4.2.
func (h *handlerSet) applyEmailPatch(
	ctx context.Context,
	p store.Principal,
	msg store.Message,
	patch json.RawMessage,
) *setError {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(patch, &m); err != nil {
		return &setError{Type: "invalidProperties", Description: err.Error()}
	}
	var addFlags, clearFlags store.MessageFlags
	var addKW, clearKW []string
	targetMailboxID := store.MailboxID(0) // 0 = no move
	for k, v := range m {
		switch {
		case k == "keywords/$seen":
			if string(v) == "null" {
				clearFlags |= store.MessageFlagSeen
			} else {
				addFlags |= store.MessageFlagSeen
			}
		case k == "keywords/$answered":
			if string(v) == "null" {
				clearFlags |= store.MessageFlagAnswered
			} else {
				addFlags |= store.MessageFlagAnswered
			}
		case k == "keywords/$flagged":
			if string(v) == "null" {
				clearFlags |= store.MessageFlagFlagged
			} else {
				addFlags |= store.MessageFlagFlagged
			}
		case k == "keywords/$draft":
			if string(v) == "null" {
				clearFlags |= store.MessageFlagDraft
			} else {
				addFlags |= store.MessageFlagDraft
			}
		case strings.HasPrefix(k, "keywords/"):
			kw := k[len("keywords/"):]
			if string(v) == "null" {
				clearKW = append(clearKW, kw)
			} else {
				addKW = append(addKW, kw)
			}
		case strings.HasPrefix(k, "mailboxIds/"):
			mbID := k[len("mailboxIds/"):]
			id, err := strconv.ParseUint(mbID, 10, 64)
			if err != nil || id == 0 {
				return &setError{Type: "invalidProperties",
					Description: "invalid mailboxId in patch: " + mbID}
			}
			if string(v) == "null" {
				// Remove from this mailbox — in v1 single-mailbox model,
				// this is a no-op if the message is not in this mailbox.
			} else {
				// Add to this mailbox — in v1 single-mailbox model, this is
				// a move.
				targetMailboxID = store.MailboxID(id)
			}
		}
	}
	if targetMailboxID != 0 && targetMailboxID != msg.MailboxID {
		// Verify the principal owns the target mailbox.
		mb, err := h.store.Meta().GetMailboxByID(ctx, targetMailboxID)
		if err != nil || mb.PrincipalID != p.ID {
			return &setError{Type: "notFound", Description: "target mailbox not found"}
		}
		if err := h.store.Meta().MoveMessage(ctx, msg.ID, targetMailboxID); err != nil {
			return &setError{Type: "serverFail", Description: err.Error()}
		}
	}
	if addFlags != 0 || clearFlags != 0 || len(addKW) > 0 || len(clearKW) > 0 {
		if _, err := h.store.Meta().UpdateMessageFlags(ctx, msg.ID,
			addFlags, clearFlags, addKW, clearKW, 0); err != nil {
			return &setError{Type: "serverFail", Description: err.Error()}
		}
	}
	return nil
}

func (s setHandler) processCreate(ctx context.Context, p store.Principal, raw json.RawMessage) (jmapEmailSubmission, *setError) {
	var in struct {
		IdentityID jmapID        `json:"identityId"`
		EmailID    jmapID        `json:"emailId"`
		Envelope   *jmapEnvelope `json:"envelope,omitempty"`
		SendAt     *string       `json:"sendAt,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return jmapEmailSubmission{}, &setError{Type: "invalidProperties", Description: err.Error()}
	}
	// REQ-PROTO-58: sendAt (RFC 8621 §7.5) is an optional UTCDate;
	// when present and parseable the queue holds the submission until
	// that instant. A malformed value is rejected at the JMAP layer
	// rather than silently coerced to "now".
	var sendAt time.Time
	if in.SendAt != nil && *in.SendAt != "" {
		t, err := time.Parse(time.RFC3339, *in.SendAt)
		if err != nil {
			return jmapEmailSubmission{}, &setError{Type: "invalidProperties",
				Properties:  []string{"sendAt"},
				Description: "sendAt must be an RFC 3339 UTC date"}
		}
		sendAt = t.UTC()
	}
	if in.EmailID == "" {
		return jmapEmailSubmission{}, &setError{Type: "invalidProperties", Properties: []string{"emailId"},
			Description: "emailId is required"}
	}
	mid, ok := parseEmailID(in.EmailID)
	if !ok {
		return jmapEmailSubmission{}, &setError{Type: "invalidProperties", Properties: []string{"emailId"},
			Description: "emailId is malformed"}
	}
	msg, err := s.h.store.Meta().GetMessage(ctx, mid)
	if err != nil {
		return jmapEmailSubmission{}, &setError{Type: "invalidProperties", Properties: []string{"emailId"},
			Description: "no such email"}
	}
	mb, err := s.h.store.Meta().GetMailboxByID(ctx, msg.MailboxID)
	if err != nil || mb.PrincipalID != p.ID {
		return jmapEmailSubmission{}, &setError{Type: "invalidProperties", Properties: []string{"emailId"},
			Description: "email is not visible to the caller"}
	}
	identityEmail, ok := s.h.identity.IdentityEmail(ctx, p, in.IdentityID)
	if !ok {
		return jmapEmailSubmission{}, &setError{Type: "invalidProperties", Properties: []string{"identityId"},
			Description: "no such identity"}
	}
	mailFrom, recipients, perr := buildEnvelope(in.Envelope, identityEmail, msg)
	if perr != nil {
		return jmapEmailSubmission{}, perr
	}
	// REQ-SEND-12 / REQ-FLOW-41: verify the principal owns the from address.
	observe.RegisterSendPolicyMetrics()
	{
		chk := sendpolicy.StoreChecker{Meta: s.h.store.Meta()}
		var keyPtr *store.APIKey
		if k, ok := protojmap.APIKeyFromContext(ctx); ok {
			keyPtr = &k
		}
		dec, decErr := sendpolicy.CheckFrom(ctx, chk, p, keyPtr, strings.ToLower(mailFrom))
		if decErr != nil {
			return jmapEmailSubmission{}, &setError{Type: "serverFail",
				Description: fmt.Sprintf("from-address ownership check: %s", decErr)}
		}
		if !dec.Allowed {
			observe.SendForbiddenFromTotal.WithLabelValues(string(sendpolicy.SourceJMAP)).Inc()
			_ = s.h.store.Meta().AppendAuditLog(ctx, store.AuditLogEntry{
				ActorKind: store.ActorPrincipal,
				ActorID:   strconv.FormatUint(uint64(p.ID), 10),
				Action:    "mail.send.forbidden_from",
				Subject:   mailFrom,
				Outcome:   store.OutcomeFailure,
				Message:   "from address not owned by principal",
				Metadata:  map[string]string{"from": mailFrom, "source": string(sendpolicy.SourceJMAP), "reason": string(dec.Reason)},
			})
			return jmapEmailSubmission{}, &setError{
				Type:        "forbiddenFrom",
				Description: fmt.Sprintf("from address %q is not owned by the authenticated principal", mailFrom),
				Properties:  []string{"envelope.mailFrom"},
			}
		}
	}
	// Read the body blob for handing to the queue.
	rc, err := s.h.store.Blobs().Get(ctx, msg.Blob.Hash)
	if err != nil {
		return jmapEmailSubmission{}, &setError{Type: "serverFail",
			Description: fmt.Sprintf("blob read: %s", err)}
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return jmapEmailSubmission{}, &setError{Type: "serverFail",
			Description: fmt.Sprintf("blob read: %s", err)}
	}
	signingDomain := domainOf(identityEmail)
	pid := p.ID
	envID, err := s.h.queue.Submit(ctx, queue.Submission{
		PrincipalID:   &pid,
		MailFrom:      mailFrom,
		Recipients:    recipients,
		Body:          bytes.NewReader(body),
		Sign:          true,
		SigningDomain: signingDomain,
		SendAt:        sendAt,
	})
	if err != nil {
		return jmapEmailSubmission{}, &setError{Type: "serverFail",
			Description: fmt.Sprintf("queue submit: %s", err)}
	}
	threadID := strconv.FormatUint(msg.ThreadID, 10)
	now := s.h.clk.Now().UTC()
	// SendAtUs records the user-visible sendAt for /get rendering. When
	// the request supplied a future sendAt we persist it verbatim so
	// the UTCDate the client provided is what /get returns; otherwise
	// the row was an "immediate" send and SendAtUs == CreatedAtUs.
	sendAtUs := now.UnixMicro()
	if !sendAt.IsZero() {
		sendAtUs = sendAt.UnixMicro()
	}
	row := store.EmailSubmissionRow{
		ID:          renderSubmissionID(envID),
		EnvelopeID:  envID,
		PrincipalID: p.ID,
		IdentityID:  in.IdentityID,
		EmailID:     mid,
		ThreadID:    threadID,
		SendAtUs:    sendAtUs,
		CreatedAtUs: now.UnixMicro(),
		UndoStatus:  string(undoStatusPending),
	}
	if err := s.h.store.Meta().InsertEmailSubmission(ctx, row); err != nil {
		return jmapEmailSubmission{}, &setError{Type: "serverFail",
			Description: fmt.Sprintf("persist submission: %s", err)}
	}
	rows, _ := s.h.store.Meta().ListQueueItems(ctx, store.QueueFilter{EnvelopeID: envID})
	return rowToJMAP(rows, in.IdentityID, in.EmailID, threadID), nil
}

func (s setHandler) processUpdate(ctx context.Context, p store.Principal, id jmapID, raw json.RawMessage) *setError {
	env, ok := parseSubmissionID(id)
	if !ok {
		return &setError{Type: "notFound"}
	}
	rows, err := s.h.store.Meta().ListQueueItems(ctx, store.QueueFilter{
		EnvelopeID:  env,
		PrincipalID: p.ID,
	})
	if err != nil {
		return &setError{Type: "serverFail", Description: err.Error()}
	}
	if len(rows) == 0 {
		return &setError{Type: "notFound"}
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(raw, &patch); err != nil {
		return &setError{Type: "invalidProperties", Description: err.Error()}
	}
	for k, v := range patch {
		if k != "undoStatus" {
			return &setError{Type: "invalidProperties",
				Properties:  []string{k},
				Description: "only undoStatus is updatable"}
		}
		var us undoStatus
		if err := json.Unmarshal(v, &us); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}
		}
		if us != undoStatusCanceled {
			return &setError{Type: "invalidProperties",
				Properties:  []string{"undoStatus"},
				Description: "only canceled is permitted"}
		}
	}
	// Cancel: hold every row that is still in flight.
	for _, r := range rows {
		switch r.State {
		case store.QueueStateQueued, store.QueueStateDeferred:
			if err := s.h.store.Meta().HoldQueueItem(ctx, r.ID); err != nil {
				return &setError{Type: "serverFail", Description: err.Error()}
			}
		case store.QueueStateInflight:
			// Inflight rows cannot be undone — the deliverer has the
			// wire. RFC 8621 §5.4 permits a SetError here.
			return &setError{Type: "cannotUnsend",
				Description: "submission is already being delivered"}
		}
	}
	// Reflect the canceled status onto the persisted row so /get and
	// /query observe the transition after restart.
	if err := s.h.store.Meta().UpdateEmailSubmissionUndoStatus(ctx, id, string(undoStatusCanceled)); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return &setError{Type: "serverFail", Description: err.Error()}
	}
	return nil
}

func (s setHandler) processDestroy(ctx context.Context, p store.Principal, id jmapID) *setError {
	env, ok := parseSubmissionID(id)
	if !ok {
		return &setError{Type: "notFound"}
	}
	rows, err := s.h.store.Meta().ListQueueItems(ctx, store.QueueFilter{
		EnvelopeID:  env,
		PrincipalID: p.ID,
	})
	if err != nil {
		return &setError{Type: "serverFail", Description: err.Error()}
	}
	// REQ-PROTO-58 / REQ-FLOW-63: destroy before sendAt MUST atomically
	// remove the queue rows; destroy after delivery began is a
	// no-op-with-diagnostic. The branch is selected by Cancel's
	// inflight count: zero means every still-pending row was removed
	// (including the all-rows-already-gone path that followed a prior
	// in-flight delivery to completion); a non-zero count means at
	// least one recipient is on the wire and we surface the
	// "alreadyInflight" notDestroyed entry to the client. Rows that
	// already reached done/failed do not block the destroy.
	cancelled, inflight, cErr := s.h.queue.Cancel(ctx, env)
	if cErr != nil {
		return &setError{Type: "serverFail", Description: cErr.Error()}
	}
	if inflight > 0 {
		// Best-effort cancellation refused. RFC 8621 §5.4 permits the
		// server to report a setError on destroy; we encode the
		// in-flight count so the client can tell the user how many
		// recipients may still receive the message.
		return &setError{Type: "alreadyInflight",
			Properties: []string{
				fmt.Sprintf("deliveredCount=%d", inflight),
			},
			Description: "submission already handed off to remote SMTP for one or more recipients"}
	}
	// Sweep any rows the queue did not touch (terminal done/failed).
	// These are normal post-delivery destroys: the queue rows linger
	// for a short window so /get can still observe the deliveryStatus,
	// and destroy is the operator-visible cleanup. ErrNotFound here is
	// benign (a concurrent destroy raced us).
	if cancelled == 0 {
		for _, r := range rows {
			if r.State == store.QueueStateDone || r.State == store.QueueStateFailed {
				if dErr := s.h.store.Meta().DeleteQueueItem(ctx, r.ID); dErr != nil &&
					!errors.Is(dErr, store.ErrNotFound) {
					return &setError{Type: "serverFail", Description: dErr.Error()}
				}
			}
		}
	}
	if err := s.h.store.Meta().DeleteEmailSubmission(ctx, id); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return &setError{Type: "serverFail", Description: err.Error()}
	}
	// Both no rows existed and Cancel had nothing to report → the
	// submission is unknown to the server. RFC 8621: notFound.
	if len(rows) == 0 {
		return &setError{Type: "notFound"}
	}
	return nil
}

// buildEnvelope derives the (mailFrom, recipients) tuple from the
// JMAP create object's optional envelope, falling back to the
// identity's email and the message's To/Cc/Bcc when no envelope is
// supplied. Returns a setError when neither source yields recipients.
func buildEnvelope(env *jmapEnvelope, identityEmail string, msg store.Message) (string, []string, *setError) {
	if env != nil {
		mailFrom := env.MailFrom.Email
		if mailFrom == "" {
			mailFrom = identityEmail
		}
		recipients := make([]string, 0, len(env.RcptTo))
		for _, r := range env.RcptTo {
			if r.Email != "" {
				recipients = append(recipients, r.Email)
			}
		}
		if len(recipients) == 0 {
			return "", nil, &setError{Type: "invalidProperties",
				Properties: []string{"envelope"}, Description: "envelope has no recipients"}
		}
		return mailFrom, recipients, nil
	}
	// Derive from headers.
	recipients := extractAddrs(msg.Envelope.To)
	recipients = append(recipients, extractAddrs(msg.Envelope.Cc)...)
	recipients = append(recipients, extractAddrs(msg.Envelope.Bcc)...)
	if len(recipients) == 0 {
		return "", nil, &setError{Type: "invalidProperties",
			Properties: []string{"envelope"}, Description: "no recipients in headers"}
	}
	return identityEmail, recipients, nil
}

// extractAddrs pulls bare addr-spec values out of a comma-separated
// header. The Phase-1 Envelope carries the raw header verbatim; we
// extract the first <addr> in each comma-separated chunk, falling back
// to the trimmed token when no angle-brackets appear.
func extractAddrs(header string) []string {
	if header == "" {
		return nil
	}
	var out []string
	for _, part := range splitOnComma(header) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if l := strings.Index(part, "<"); l >= 0 {
			if r := strings.Index(part[l:], ">"); r > 0 {
				addr := strings.TrimSpace(part[l+1 : l+r])
				if addr != "" {
					out = append(out, addr)
				}
				continue
			}
		}
		// No brackets — accept the trimmed token if it looks like an
		// addr-spec.
		if strings.Contains(part, "@") {
			out = append(out, part)
		}
	}
	return out
}

// splitOnComma is a tiny CSV splitter respecting double-quoted runs.
// RFC 5322 group syntax is not supported — we accept the common form.
func splitOnComma(s string) []string {
	var out []string
	cur := strings.Builder{}
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func domainOf(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}
