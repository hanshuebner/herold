package vacation

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

// jmapID is the wire form of a JMAP id (RFC 8620 §1.2).
type jmapID = string

// singletonID is the only id a VacationResponse object carries
// (RFC 8621 §9: "There is only one VacationResponse object per Account,
// with id 'singleton'").
const singletonID = "singleton"

// jmapVacation is the wire-form VacationResponse object (RFC 8621 §9.1).
type jmapVacation struct {
	ID        jmapID  `json:"id"`
	IsEnabled bool    `json:"isEnabled"`
	FromDate  *string `json:"fromDate"`
	ToDate    *string `json:"toDate"`
	Subject   *string `json:"subject"`
	TextBody  *string `json:"textBody"`
	HTMLBody  *string `json:"htmlBody"`
}

func paramsToJMAP(p vacationParams) jmapVacation {
	v := jmapVacation{ID: singletonID, IsEnabled: p.IsEnabled}
	if p.FromDate != nil {
		s := p.FromDate.UTC().Format(time.RFC3339)
		v.FromDate = &s
	}
	if p.ToDate != nil {
		s := p.ToDate.UTC().Format(time.RFC3339)
		v.ToDate = &s
	}
	if p.Subject != "" {
		s := p.Subject
		v.Subject = &s
	}
	if p.TextBody != "" {
		s := p.TextBody
		v.TextBody = &s
	}
	if p.HTMLBody != "" {
		s := p.HTMLBody
		v.HTMLBody = &s
	}
	return v
}

// getRequest is the inbound shape for VacationResponse/get.
type getRequest struct {
	AccountID jmapID    `json:"accountId"`
	IDs       *[]jmapID `json:"ids"`
}

// getResponse mirrors RFC 8620 §5.1.
type getResponse struct {
	AccountID string         `json:"accountId"`
	State     string         `json:"state"`
	List      []jmapVacation `json:"list"`
	NotFound  []jmapID       `json:"notFound"`
}

// setRequest is the inbound shape for VacationResponse/set. Only
// updates make sense — singletons are not created or destroyed.
type setRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState,omitempty"`
	Update    map[jmapID]json.RawMessage `json:"update,omitempty"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// setResponse mirrors RFC 8620 §5.3 with create/destroy slots elided
// because singletons cannot be created or destroyed.
type setResponse struct {
	AccountID  string                   `json:"accountId"`
	OldState   string                   `json:"oldState,omitempty"`
	NewState   string                   `json:"newState"`
	Updated    map[jmapID]*jmapVacation `json:"updated,omitempty"`
	NotUpdated map[jmapID]setError      `json:"notUpdated,omitempty"`
}

// handlerSet binds the VacationResponse handlers to the store.
type handlerSet struct {
	store store.Store
}

func stateString(seq int64) string { return strconv.FormatInt(seq, 10) }

func accountIDForPrincipal(p store.Principal) string {
	return strconv.FormatUint(uint64(p.ID), 10)
}

func validateAccountID(p store.Principal, requested jmapID) *protojmap.MethodError {
	if requested == "" {
		return nil
	}
	if requested != accountIDForPrincipal(p) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// loadParams reads the principal's Sieve script and extracts the
// vacation parameters. Returns the empty (disabled) params on a
// missing or empty script. A complex script that the round-trip layer
// cannot parse safely returns the disabled defaults — we never
// silently drop content; /set is where the operator hits the
// errComplexScript signal.
func (h *handlerSet) loadParams(ctx context.Context, p store.Principal) (vacationParams, error) {
	script, err := h.store.Meta().GetSieveScript(ctx, p.ID)
	if err != nil {
		return vacationParams{}, fmt.Errorf("vacation: read sieve: %w", err)
	}
	v, err := readVacation(script)
	if err != nil {
		if errors.Is(err, errComplexScript) {
			// Conservative: pretend nothing is set. /set will refuse to
			// rewrite it.
			return vacationParams{IsEnabled: false}, nil
		}
		return vacationParams{}, err
	}
	return v, nil
}

// -- VacationResponse/get --------------------------------------------

type getHandler struct{ h *handlerSet }

func (getHandler) Method() string { return "VacationResponse/get" }

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
	params, err := g.h.loadParams(ctx, p)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp := getResponse{
		AccountID: accountIDForPrincipal(p),
		State:     stateString(st.VacationResponse),
		List:      []jmapVacation{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		resp.List = append(resp.List, paramsToJMAP(params))
		return resp, nil
	}
	for _, id := range *req.IDs {
		if id != singletonID {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		resp.List = append(resp.List, paramsToJMAP(params))
	}
	return resp, nil
}

// -- VacationResponse/set --------------------------------------------

type setHandler struct{ h *handlerSet }

func (setHandler) Method() string { return "VacationResponse/set" }

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
	oldState := stateString(st.VacationResponse)
	if req.IfInState != nil && *req.IfInState != oldState {
		return nil, protojmap.NewMethodError("stateMismatch",
			"server state does not match ifInState")
	}
	resp := setResponse{
		AccountID: accountIDForPrincipal(p),
		OldState:  oldState,
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
		// Refuse if the existing script is too complex to round-trip.
		current, gerr := s.h.store.Meta().GetSieveScript(ctx, p.ID)
		if gerr != nil {
			return nil, protojmap.NewMethodError("serverFail", gerr.Error())
		}
		if _, perr := readVacation(current); perr != nil && errors.Is(perr, errComplexScript) {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{
				Type:        "forbidden",
				Description: "the active Sieve script embeds vacation in a complex structure; edit it via ManageSieve",
			}
			continue
		}
		// Read the existing params and apply the patch.
		current0, _ := readVacation(current)
		patched, perr := applySetPatch(current0, raw)
		if perr != nil {
			if resp.NotUpdated == nil {
				resp.NotUpdated = make(map[jmapID]setError)
			}
			resp.NotUpdated[id] = setError{Type: "invalidProperties", Description: perr.Error()}
			continue
		}
		// Synthesize the new script and persist.
		newScript := synthesizeVacation(patched)
		if err := s.h.store.Meta().SetSieveScript(ctx, p.ID, newScript); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
		v := paramsToJMAP(patched)
		if resp.Updated == nil {
			resp.Updated = make(map[jmapID]*jmapVacation)
		}
		resp.Updated[id] = &v
		mutated = true
	}
	if mutated {
		if _, err := s.h.store.Meta().IncrementJMAPState(ctx, p.ID,
			store.JMAPStateKindVacationResponse); err != nil {
			return nil, protojmap.NewMethodError("serverFail", err.Error())
		}
	}
	stAfter, err := s.h.store.Meta().GetJMAPStates(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	resp.NewState = stateString(stAfter.VacationResponse)
	return resp, nil
}

// applySetPatch applies a JSON update patch to current and returns
// the resulting params. Unknown keys produce errors; isEnabled,
// fromDate, toDate, subject, textBody, htmlBody are recognised.
func applySetPatch(current vacationParams, raw json.RawMessage) (vacationParams, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return current, err
	}
	out := current
	for k, v := range m {
		switch k {
		case "isEnabled":
			if err := json.Unmarshal(v, &out.IsEnabled); err != nil {
				return current, fmt.Errorf("isEnabled: %w", err)
			}
		case "fromDate":
			t, err := decodeOptionalTime(v)
			if err != nil {
				return current, fmt.Errorf("fromDate: %w", err)
			}
			out.FromDate = t
		case "toDate":
			t, err := decodeOptionalTime(v)
			if err != nil {
				return current, fmt.Errorf("toDate: %w", err)
			}
			out.ToDate = t
		case "subject":
			s, err := decodeOptionalString(v)
			if err != nil {
				return current, fmt.Errorf("subject: %w", err)
			}
			out.Subject = s
		case "textBody":
			s, err := decodeOptionalString(v)
			if err != nil {
				return current, fmt.Errorf("textBody: %w", err)
			}
			out.TextBody = s
		case "htmlBody":
			s, err := decodeOptionalString(v)
			if err != nil {
				return current, fmt.Errorf("htmlBody: %w", err)
			}
			out.HTMLBody = s
		case "id":
			return current, fmt.Errorf("id is read-only")
		default:
			return current, fmt.Errorf("unknown property %q", k)
		}
	}
	return out, nil
}

func decodeOptionalString(raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

func decodeOptionalTime(raw json.RawMessage) (*time.Time, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
