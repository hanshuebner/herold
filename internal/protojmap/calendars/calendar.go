package calendars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- Calendar/get -----------------------------------------------------

type calGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type calGetResponse struct {
	AccountID jmapID         `json:"accountId"`
	State     string         `json:"state"`
	List      []jmapCalendar `json:"list"`
	NotFound  []jmapID       `json:"notFound"`
}

type calGetHandler struct{ h *handlerSet }

func (h *calGetHandler) Method() string { return "Calendar/get" }

func (h *calGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req calGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentCalendarState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	all, err := listOwnedCalendars(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	defID := defaultCalendarID(ctx, h.h.store.Meta(), pid)
	resp := calGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapCalendar{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		for _, c := range all {
			resp.List = append(resp.List, renderCalendar(c, defID))
		}
		return resp, nil
	}
	byID := make(map[store.CalendarID]store.Calendar, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	for _, raw := range *req.IDs {
		id, ok := calendarIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		c, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		resp.List = append(resp.List, renderCalendar(c, defID))
	}
	return resp, nil
}

// listOwnedCalendars returns every calendar owned by the principal.
func listOwnedCalendars(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.Calendar, error) {
	return meta.ListCalendars(ctx, store.CalendarFilter{
		PrincipalID: &pid,
	})
}

// defaultCalendarID returns the principal's default CalendarID, or 0
// when the principal has none.
func defaultCalendarID(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) store.CalendarID {
	c, err := meta.DefaultCalendar(ctx, pid)
	if err != nil {
		return 0
	}
	return c.ID
}

// renderCalendar converts a store.Calendar into wire form, stamping
// the per-principal default flag and the owner rights mask.
func renderCalendar(c store.Calendar, defID store.CalendarID) jmapCalendar {
	isDefault := c.IsDefault
	if defID != 0 {
		isDefault = isDefault || c.ID == defID
	}
	out := jmapCalendar{
		ID:           jmapIDFromCalendar(c.ID),
		Name:         c.Name,
		SortOrder:    c.SortOrder,
		IsSubscribed: c.IsSubscribed,
		IsDefault:    isDefault,
		IsVisible:    c.IsVisible,
		MyRights:     rightsForCalendarOwner(),
	}
	if c.Description != "" {
		s := c.Description
		out.Description = &s
	}
	if c.Color != nil {
		s := *c.Color
		out.Color = &s
	}
	if c.TimeZoneID != "" {
		s := c.TimeZoneID
		out.TimeZone = &s
	}
	return out
}

// -- Calendar/changes -------------------------------------------------

type calChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type calChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type calChangesHandler struct{ h *handlerSet }

func (h *calChangesHandler) Method() string { return "Calendar/changes" }

func (h *calChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req calChangesRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	since, ok := parseState(req.SinceState)
	if !ok {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "unparseable sinceState")
	}
	st, err := h.h.store.Meta().GetJMAPStates(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	newState := stateFromCounter(st.Calendar)
	resp := calChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.Calendar {
		return resp, nil
	}
	if since > st.Calendar {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}
	created, updated, destroyed, ferr := walkChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindCalendar, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromCalendar(store.CalendarID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromCalendar(store.CalendarID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromCalendar(store.CalendarID(id)))
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

// walkChangeFeed reads the principal's change feed for the given
// entity kind, returning the disjoint created/updated/destroyed sets
// produced by entries with seq > since. Mirrors the contacts package
// helper of the same name.
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

// -- Calendar/set -----------------------------------------------------

type calSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type calSetResponse struct {
	AccountID    jmapID                  `json:"accountId"`
	OldState     string                  `json:"oldState"`
	NewState     string                  `json:"newState"`
	Created      map[string]jmapCalendar `json:"created"`
	Updated      map[jmapID]any          `json:"updated"`
	Destroyed    []jmapID                `json:"destroyed"`
	NotCreated   map[string]setError     `json:"notCreated"`
	NotUpdated   map[jmapID]setError     `json:"notUpdated"`
	NotDestroyed map[jmapID]setError     `json:"notDestroyed"`
}

type calCreateInput struct {
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Color        *string `json:"color"`
	SortOrder    *int    `json:"sortOrder"`
	IsSubscribed *bool   `json:"isSubscribed"`
	IsDefault    *bool   `json:"isDefault"`
	IsVisible    *bool   `json:"isVisible"`
	TimeZone     *string `json:"timeZone"`
}

type calUpdateInput struct {
	Name         *string         `json:"name"`
	Description  json.RawMessage `json:"description"`
	Color        json.RawMessage `json:"color"`
	SortOrder    *int            `json:"sortOrder"`
	IsSubscribed *bool           `json:"isSubscribed"`
	IsDefault    *bool           `json:"isDefault"`
	IsVisible    *bool           `json:"isVisible"`
	TimeZone     json.RawMessage `json:"timeZone"`
}

type calSetHandler struct{ h *handlerSet }

func (h *calSetHandler) Method() string { return "Calendar/set" }

func (h *calSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req calSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentCalendarState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}
	resp := calSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapCalendar{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.CalendarID, len(req.Create))

	// Per-account calendar creation cap (binding draft).
	owned, err := listOwnedCalendars(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	currentCount := len(owned)

	for key, raw := range req.Create {
		var in calCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		if h.h.limits.MaxCalendarsPerAccount > 0 && currentCount >= h.h.limits.MaxCalendarsPerAccount {
			resp.NotCreated[key] = setError{
				Type:        "overQuota",
				Description: "calendar count would exceed maxCalendarsPerAccount",
			}
			continue
		}
		c, serr, err := h.h.createCalendar(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = c.ID
		currentCount++
		defID := defaultCalendarID(ctx, h.h.store.Meta(), pid)
		resp.Created[key] = renderCalendar(c, defID)
	}

	for raw, payload := range req.Update {
		id, ok := resolveCalendarID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateCalendar(ctx, pid, id, payload)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotUpdated[raw] = *serr
			continue
		}
		resp.Updated[raw] = nil
	}

	for _, raw := range req.Destroy {
		id, ok := resolveCalendarID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyCalendar(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentCalendarState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveCalendarID(raw jmapID, creationRefs map[string]store.CalendarID) (store.CalendarID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return calendarIDFromJMAP(raw)
}

func (h *handlerSet) createCalendar(
	ctx context.Context,
	pid store.PrincipalID,
	in calCreateInput,
) (store.Calendar, *setError, error) {
	if strings.TrimSpace(in.Name) == "" {
		return store.Calendar{}, &setError{
			Type: "invalidProperties", Properties: []string{"name"},
			Description: "name is required",
		}, nil
	}
	owned, err := listOwnedCalendars(ctx, h.store.Meta(), pid)
	if err != nil {
		return store.Calendar{}, nil, fmt.Errorf("calendars: list calendars: %w", err)
	}
	for _, c := range owned {
		if strings.EqualFold(c.Name, in.Name) {
			return store.Calendar{}, &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "another calendar with this name already exists",
			}, nil
		}
	}
	row := store.Calendar{
		PrincipalID:  pid,
		Name:         in.Name,
		IsSubscribed: true,
		IsVisible:    true,
	}
	if in.Description != nil {
		row.Description = *in.Description
	}
	if in.Color != nil {
		v := *in.Color
		row.Color = &v
	}
	if in.SortOrder != nil {
		row.SortOrder = *in.SortOrder
	}
	if in.IsSubscribed != nil {
		row.IsSubscribed = *in.IsSubscribed
	}
	if in.IsVisible != nil {
		row.IsVisible = *in.IsVisible
	}
	if in.TimeZone != nil {
		row.TimeZoneID = *in.TimeZone
	}
	// Auto-set is_default=true when no other default exists.
	if defID := defaultCalendarID(ctx, h.store.Meta(), pid); defID == 0 {
		row.IsDefault = true
	} else if in.IsDefault != nil && *in.IsDefault {
		row.IsDefault = true
	}
	id, err := h.store.Meta().InsertCalendar(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.Calendar{}, &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "another calendar with this name already exists",
			}, nil
		}
		return store.Calendar{}, nil, fmt.Errorf("calendars: insert: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendar); err != nil {
		return store.Calendar{}, nil, fmt.Errorf("calendars: bump state: %w", err)
	}
	loaded, err := h.store.Meta().GetCalendar(ctx, id)
	if err != nil {
		return store.Calendar{}, nil, fmt.Errorf("calendars: reload: %w", err)
	}
	return loaded, nil, nil
}

func (h *handlerSet) updateCalendar(
	ctx context.Context,
	pid store.PrincipalID,
	id store.CalendarID,
	raw json.RawMessage,
) (*setError, error) {
	c, err := h.store.Meta().GetCalendar(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: load: %w", err)
	}
	if c.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	var in calUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "name must not be empty",
			}, nil
		}
		owned, err := listOwnedCalendars(ctx, h.store.Meta(), pid)
		if err != nil {
			return nil, fmt.Errorf("calendars: list: %w", err)
		}
		for _, other := range owned {
			if other.ID != c.ID && strings.EqualFold(other.Name, *in.Name) {
				return &setError{
					Type: "invalidProperties", Properties: []string{"name"},
					Description: "another calendar with this name already exists",
				}, nil
			}
		}
		c.Name = *in.Name
	}
	if in.SortOrder != nil {
		c.SortOrder = *in.SortOrder
	}
	if in.IsSubscribed != nil {
		c.IsSubscribed = *in.IsSubscribed
	}
	if in.IsDefault != nil {
		c.IsDefault = *in.IsDefault
	}
	if in.IsVisible != nil {
		c.IsVisible = *in.IsVisible
	}
	if len(in.Description) > 0 {
		if string(in.Description) == "null" {
			c.Description = ""
		} else {
			var s string
			if err := json.Unmarshal(in.Description, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"description"},
					Description: "description must be a string or null",
				}, nil
			}
			c.Description = s
		}
	}
	if len(in.Color) > 0 {
		if string(in.Color) == "null" {
			c.Color = nil
		} else {
			var s string
			if err := json.Unmarshal(in.Color, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"color"},
					Description: "color must be a string or null",
				}, nil
			}
			c.Color = &s
		}
	}
	if len(in.TimeZone) > 0 {
		if string(in.TimeZone) == "null" {
			c.TimeZoneID = ""
		} else {
			var s string
			if err := json.Unmarshal(in.TimeZone, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"timeZone"},
					Description: "timeZone must be a string or null",
				}, nil
			}
			c.TimeZoneID = s
		}
	}
	if err := h.store.Meta().UpdateCalendar(ctx, c); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		if errors.Is(err, store.ErrConflict) {
			return &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "name conflicts with existing calendar",
			}, nil
		}
		return nil, fmt.Errorf("calendars: update: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendar); err != nil {
		return nil, fmt.Errorf("calendars: bump state: %w", err)
	}
	return nil, nil
}

func (h *handlerSet) destroyCalendar(
	ctx context.Context,
	pid store.PrincipalID,
	id store.CalendarID,
) (*setError, error) {
	c, err := h.store.Meta().GetCalendar(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: load: %w", err)
	}
	if c.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	// Cascade is handled by ON DELETE CASCADE in the schema.
	if err := h.store.Meta().DeleteCalendar(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: delete: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendar); err != nil {
		return nil, fmt.Errorf("calendars: bump state: %w", err)
	}
	// The cascade may have removed events; bump the event state too.
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return nil, fmt.Errorf("calendars: bump event state: %w", err)
	}
	return nil, nil
}

// -- Calendar/query ---------------------------------------------------

type calFilterCondition struct {
	Name *string `json:"name"`
}

type calQueryRequest struct {
	AccountID      jmapID              `json:"accountId"`
	Filter         *calFilterCondition `json:"filter"`
	Sort           []comparator        `json:"sort"`
	Position       int                 `json:"position"`
	Anchor         *jmapID             `json:"anchor"`
	AnchorOffset   int                 `json:"anchorOffset"`
	Limit          *int                `json:"limit"`
	CalculateTotal bool                `json:"calculateTotal"`
}

type calQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type calQueryHandler struct{ h *handlerSet }

func (h *calQueryHandler) Method() string { return "Calendar/query" }

func (h *calQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req calQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentCalendarState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	all, err := listOwnedCalendars(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := make([]store.Calendar, 0, len(all))
	for _, c := range all {
		if matchCalendarFilter(c, req.Filter) {
			matched = append(matched, c)
		}
	}
	sortCalendars(matched, req.Sort)
	resp := calQueryResponse{
		AccountID:  string(protojmap.AccountIDForPrincipal(pid)),
		QueryState: state,
		IDs:        []jmapID{},
	}
	total := len(matched)
	if req.CalculateTotal {
		t := total
		resp.Total = &t
	}
	start := req.Position
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if req.Limit != nil && *req.Limit >= 0 {
		l := *req.Limit
		if start+l < end {
			end = start + l
		}
		resp.Limit = req.Limit
	}
	resp.Position = start
	for _, c := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromCalendar(c.ID))
	}
	return resp, nil
}

func matchCalendarFilter(c store.Calendar, f *calFilterCondition) bool {
	if f == nil {
		return true
	}
	if f.Name != nil {
		if !strings.EqualFold(c.Name, *f.Name) {
			return false
		}
	}
	return true
}

func sortCalendars(xs []store.Calendar, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "name"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareCalendar(xs[i], xs[j], c.Property)
			if cmp == 0 {
				continue
			}
			if asc {
				return cmp < 0
			}
			return cmp > 0
		}
		return xs[i].ID < xs[j].ID
	})
}

func compareCalendar(a, b store.Calendar, property string) int {
	switch property {
	case "name":
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	case "sortOrder":
		switch {
		case a.SortOrder < b.SortOrder:
			return -1
		case a.SortOrder > b.SortOrder:
			return 1
		}
		return 0
	}
	return 0
}

// -- Calendar/queryChanges --------------------------------------------

type calQueryChangesHandler struct{ h *handlerSet }

func (calQueryChangesHandler) Method() string { return "Calendar/queryChanges" }

func (calQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Calendar/queryChanges is unsupported; clients re-issue Calendar/query")
}
