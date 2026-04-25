package calendars

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/calendars/jscalendar"
	"github.com/hanshuebner/herold/internal/store"
)

// -- CalendarEvent/get ------------------------------------------------

type evGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type evGetResponse struct {
	AccountID jmapID            `json:"accountId"`
	State     string            `json:"state"`
	List      []json.RawMessage `json:"list"`
	NotFound  []jmapID          `json:"notFound"`
}

type evGetHandler struct{ h *handlerSet }

func (h *evGetHandler) Method() string { return "CalendarEvent/get" }

func (h *evGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req evGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentCalendarEventState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := evGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []json.RawMessage{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		rows, err := h.h.store.Meta().ListCalendarEvents(ctx, store.CalendarEventFilter{
			PrincipalID: &pid,
		})
		if err != nil {
			return nil, serverFail(err)
		}
		for _, ev := range rows {
			rendered, rerr := renderEvent(ev)
			if rerr != nil {
				return nil, serverFail(rerr)
			}
			resp.List = append(resp.List, rendered)
		}
		return resp, nil
	}
	for _, raw := range *req.IDs {
		id, ok := eventIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		ev, err := h.h.store.Meta().GetCalendarEvent(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			return nil, serverFail(err)
		}
		if ev.PrincipalID != pid {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		rendered, rerr := renderEvent(ev)
		if rerr != nil {
			return nil, serverFail(rerr)
		}
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// renderEvent produces the wire-form CalendarEvent JSON: the
// JSCalendar Event body merged with the JMAP-projected properties
// (id, calendarId, myRights). The merge happens at the JSON level so
// any RawJSON keys the typed Event does not model are preserved.
func renderEvent(ev store.CalendarEvent) (json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(ev.JSCalendarJSON) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(ev.JSCalendarJSON, &raw); err != nil {
			return nil, fmt.Errorf("calendars: parse stored event: %w", err)
		}
		for k, v := range raw {
			out[k] = v
		}
	}
	idBytes, _ := json.Marshal(jmapIDFromEvent(ev.ID))
	out["id"] = idBytes
	calBytes, _ := json.Marshal(jmapIDFromCalendar(ev.CalendarID))
	out["calendarId"] = calBytes
	rightsBytes, _ := json.Marshal(rightsForCalendarOwner())
	out["myRights"] = rightsBytes
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(out[k])
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

// -- CalendarEvent/changes --------------------------------------------

type evChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type evChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type evChangesHandler struct{ h *handlerSet }

func (h *evChangesHandler) Method() string { return "CalendarEvent/changes" }

func (h *evChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req evChangesRequest
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
	newState := stateFromCounter(st.CalendarEvent)
	resp := evChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.CalendarEvent {
		return resp, nil
	}
	if since > st.CalendarEvent {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}
	created, updated, destroyed, ferr := walkChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindCalendarEvent, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromEvent(store.CalendarEventID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromEvent(store.CalendarEventID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromEvent(store.CalendarEventID(id)))
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

// -- CalendarEvent/set ------------------------------------------------

type evSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type evSetResponse struct {
	AccountID    jmapID                     `json:"accountId"`
	OldState     string                     `json:"oldState"`
	NewState     string                     `json:"newState"`
	Created      map[string]json.RawMessage `json:"created"`
	Updated      map[jmapID]any             `json:"updated"`
	Destroyed    []jmapID                   `json:"destroyed"`
	NotCreated   map[string]setError        `json:"notCreated"`
	NotUpdated   map[jmapID]setError        `json:"notUpdated"`
	NotDestroyed map[jmapID]setError        `json:"notDestroyed"`
}

type evSetHandler struct{ h *handlerSet }

func (h *evSetHandler) Method() string { return "CalendarEvent/set" }

func (h *evSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req evSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentCalendarEventState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}
	resp := evSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]json.RawMessage{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.CalendarEventID, len(req.Create))

	for key, raw := range req.Create {
		ev, serr, err := h.h.createEvent(ctx, pid, raw)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = ev.ID
		rendered, rerr := renderEvent(ev)
		if rerr != nil {
			return nil, serverFail(rerr)
		}
		resp.Created[key] = rendered
	}

	for raw, payload := range req.Update {
		id, ok := resolveEventID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateEvent(ctx, pid, id, payload)
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
		id, ok := resolveEventID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyEvent(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentCalendarEventState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveEventID(raw jmapID, creationRefs map[string]store.CalendarEventID) (store.CalendarEventID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return eventIDFromJMAP(raw)
}

// createEvent validates the JSCalendar body, mints a UID when absent,
// resolves the parent calendar, derives denormalised columns via the
// jscalendar bridge, and persists.
func (h *handlerSet) createEvent(
	ctx context.Context,
	pid store.PrincipalID,
	raw json.RawMessage,
) (store.CalendarEvent, *setError, error) {
	if len(raw) == 0 {
		return store.CalendarEvent{}, &setError{Type: "invalidProperties", Description: "empty body"}, nil
	}
	if h.limits.MaxSizePerEventBlob > 0 && len(raw) > h.limits.MaxSizePerEventBlob {
		return store.CalendarEvent{}, &setError{
			Type: "tooLarge", Description: "event blob exceeds maxSizePerEventBlob",
		}, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return store.CalendarEvent{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	calRaw, hasCal := probe["calendarId"]
	if !hasCal {
		return store.CalendarEvent{}, &setError{
			Type: "invalidProperties", Properties: []string{"calendarId"},
			Description: "calendarId is required",
		}, nil
	}
	var calIDStr string
	if err := json.Unmarshal(calRaw, &calIDStr); err != nil {
		return store.CalendarEvent{}, &setError{
			Type: "invalidProperties", Properties: []string{"calendarId"},
			Description: "calendarId must be a string",
		}, nil
	}
	calID, ok := calendarIDFromJMAP(calIDStr)
	if !ok {
		return store.CalendarEvent{}, &setError{
			Type: "invalidProperties", Properties: []string{"calendarId"},
			Description: "unknown calendarId",
		}, nil
	}
	cal, err := h.store.Meta().GetCalendar(ctx, calID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.CalendarEvent{}, &setError{
				Type: "invalidProperties", Properties: []string{"calendarId"},
				Description: "calendarId references unknown calendar",
			}, nil
		}
		return store.CalendarEvent{}, nil, fmt.Errorf("calendars: load calendar: %w", err)
	}
	if cal.PrincipalID != pid {
		return store.CalendarEvent{}, &setError{
			Type: "invalidProperties", Properties: []string{"calendarId"},
			Description: "calendarId is not accessible to this principal",
		}, nil
	}
	// Strip JMAP-projected properties before deriving the JSCalendar
	// body.
	delete(probe, "calendarId")
	delete(probe, "id")
	delete(probe, "myRights")

	body, err := json.Marshal(probe)
	if err != nil {
		return store.CalendarEvent{}, nil, fmt.Errorf("calendars: re-serialise body: %w", err)
	}
	ev, serr, err := h.parseAndDeriveEvent(body)
	if err != nil {
		return store.CalendarEvent{}, nil, err
	}
	if serr != nil {
		return store.CalendarEvent{}, serr, nil
	}
	row := store.CalendarEvent{
		PrincipalID:    pid,
		CalendarID:     calID,
		UID:            ev.UID,
		JSCalendarJSON: ev.JSON,
		Summary:        ev.Summary,
		Start:          microsToTime(ev.StartUS),
		End:            microsToTime(ev.EndUS),
		IsRecurring:    ev.IsRecurring,
		RRuleJSON:      ev.RRuleJSON,
		OrganizerEmail: ev.OrganizerEmail,
		Status:         ev.Status,
	}
	id, err := h.store.Meta().InsertCalendarEvent(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.CalendarEvent{}, &setError{
				Type:        "invalidProperties",
				Description: "event uid conflicts with existing event in this calendar",
			}, nil
		}
		return store.CalendarEvent{}, nil, fmt.Errorf("calendars: insert event: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return store.CalendarEvent{}, nil, fmt.Errorf("calendars: bump event state: %w", err)
	}
	loaded, err := h.store.Meta().GetCalendarEvent(ctx, id)
	if err != nil {
		return store.CalendarEvent{}, nil, fmt.Errorf("calendars: reload event: %w", err)
	}
	return loaded, nil, nil
}

// parsedEvent is the shape parseAndDeriveEvent returns: the validated
// JSON body plus the denormalised columns the store needs.
type parsedEvent struct {
	JSON           []byte
	UID            string
	Summary        string
	StartUS        int64
	EndUS          int64
	IsRecurring    bool
	RRuleJSON      []byte
	OrganizerEmail string
	Status         string
	Sequence       int64
}

// parseAndDeriveEvent unmarshals the JSON body through the jscalendar
// package, validates, mints a UID when absent, and produces the
// denormalised columns the store filters on.
func (h *handlerSet) parseAndDeriveEvent(body []byte) (parsedEvent, *setError, error) {
	var jev jscalendar.Event
	if err := jev.UnmarshalJSON(body); err != nil {
		return parsedEvent{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	if jev.UID == "" {
		uid, uerr := mintUID()
		if uerr != nil {
			return parsedEvent{}, nil, uerr
		}
		jev.UID = uid
	}
	if err := jev.Validate(); err != nil {
		return parsedEvent{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	finalBody, err := jev.MarshalJSON()
	if err != nil {
		return parsedEvent{}, nil, fmt.Errorf("calendars: marshal event: %w", err)
	}
	startUS, endUS := eventStartEndMicros(&jev)
	rrJSON := eventRRuleJSON(&jev)
	return parsedEvent{
		JSON:           finalBody,
		UID:            jev.UID,
		Summary:        jev.Title,
		StartUS:        startUS,
		EndUS:          endUS,
		IsRecurring:    jev.IsRecurring(),
		RRuleJSON:      rrJSON,
		OrganizerEmail: jev.OrganizerEmail(),
		Status:         jev.Status(),
		Sequence:       int64(jev.Sequence),
	}, nil, nil
}

// eventStartEndMicros derives the UTC microsecond start/end stamps the
// store uses for window queries. RFC 8984's Start is a LocalDateTime;
// Unmarshal already resolved it to UTC. End is Start + Duration; for
// all-day events with no Duration the end equals start (a single-day
// span).
func eventStartEndMicros(e *jscalendar.Event) (int64, int64) {
	if e.Start.IsZero() {
		return 0, 0
	}
	startUS := e.Start.UnixMicro()
	endUS := startUS
	if d := e.Duration.Value; d > 0 {
		endUS = e.Start.Add(d).UnixMicro()
	}
	return startUS, endUS
}

// eventRRuleJSON renders the master event's recurrence rules as JSON
// for the store's denormalised rrule_json column. Returns nil for
// non-recurring events.
func eventRRuleJSON(e *jscalendar.Event) []byte {
	if !e.IsRecurring() {
		return nil
	}
	body, err := json.Marshal(e.RecurrenceRules)
	if err != nil {
		return nil
	}
	return body
}

// updateEvent applies a JSON Merge Patch to the stored event body,
// re-derives the denormalised columns, and persists.
func (h *handlerSet) updateEvent(
	ctx context.Context,
	pid store.PrincipalID,
	id store.CalendarEventID,
	raw json.RawMessage,
) (*setError, error) {
	ev, err := h.store.Meta().GetCalendarEvent(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: load event: %w", err)
	}
	if ev.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	var patch map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &patch); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}
	var stored map[string]json.RawMessage
	if len(ev.JSCalendarJSON) > 0 {
		if err := json.Unmarshal(ev.JSCalendarJSON, &stored); err != nil {
			return nil, fmt.Errorf("calendars: parse stored event: %w", err)
		}
	} else {
		stored = map[string]json.RawMessage{}
	}
	for k, v := range patch {
		if k == "id" || k == "myRights" {
			continue
		}
		if k == "calendarId" {
			var calIDStr string
			if err := json.Unmarshal(v, &calIDStr); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"calendarId"},
					Description: "calendarId must be a string",
				}, nil
			}
			newCalID, ok := calendarIDFromJMAP(calIDStr)
			if !ok {
				return &setError{
					Type: "invalidProperties", Properties: []string{"calendarId"},
					Description: "unknown calendarId",
				}, nil
			}
			cal, gerr := h.store.Meta().GetCalendar(ctx, newCalID)
			if gerr != nil || cal.PrincipalID != pid {
				return &setError{
					Type: "invalidProperties", Properties: []string{"calendarId"},
					Description: "calendarId is not accessible to this principal",
				}, nil
			}
			ev.CalendarID = newCalID
			continue
		}
		if string(v) == "null" {
			delete(stored, k)
			continue
		}
		stored[k] = v
	}
	body, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("calendars: marshal merged body: %w", err)
	}
	if h.limits.MaxSizePerEventBlob > 0 && len(body) > h.limits.MaxSizePerEventBlob {
		return &setError{Type: "tooLarge", Description: "merged body exceeds maxSizePerEventBlob"}, nil
	}
	parsed, serr, perr := h.parseAndDeriveEvent(body)
	if perr != nil {
		return nil, perr
	}
	if serr != nil {
		return serr, nil
	}
	if parsed.UID == "" {
		parsed.UID = ev.UID
	}
	ev.UID = parsed.UID
	ev.JSCalendarJSON = parsed.JSON
	ev.Summary = parsed.Summary
	ev.Start = microsToTime(parsed.StartUS)
	ev.End = microsToTime(parsed.EndUS)
	ev.IsRecurring = parsed.IsRecurring
	ev.RRuleJSON = parsed.RRuleJSON
	ev.OrganizerEmail = parsed.OrganizerEmail
	ev.Status = parsed.Status
	if err := h.store.Meta().UpdateCalendarEvent(ctx, ev); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: update event: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return nil, fmt.Errorf("calendars: bump event state: %w", err)
	}
	return nil, nil
}

func (h *handlerSet) destroyEvent(
	ctx context.Context,
	pid store.PrincipalID,
	id store.CalendarEventID,
) (*setError, error) {
	ev, err := h.store.Meta().GetCalendarEvent(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: load: %w", err)
	}
	if ev.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	if err := h.store.Meta().DeleteCalendarEvent(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("calendars: delete: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindCalendarEvent); err != nil {
		return nil, fmt.Errorf("calendars: bump event state: %w", err)
	}
	return nil, nil
}

// -- CalendarEvent/query ----------------------------------------------

type evFilterCondition struct {
	CalendarID        *jmapID    `json:"calendarId"`
	Text              *string    `json:"text"`
	UID               *string    `json:"uid"`
	StartAfter        *time.Time `json:"startAfter"`
	StartBefore       *time.Time `json:"startBefore"`
	Status            *string    `json:"status"`
	ExpandRecurrences *bool      `json:"expandRecurrences"`
}

type evQueryRequest struct {
	AccountID      jmapID             `json:"accountId"`
	Filter         *evFilterCondition `json:"filter"`
	Sort           []comparator       `json:"sort"`
	Position       int                `json:"position"`
	Anchor         *jmapID            `json:"anchor"`
	AnchorOffset   int                `json:"anchorOffset"`
	Limit          *int               `json:"limit"`
	CalculateTotal bool               `json:"calculateTotal"`
	// ExpandRecurrences mirrors the per-filter flag at the top level —
	// the binding draft accepts it on the top-level args object, and
	// we honour either form so client implementers don't have to
	// special-case our shape.
	ExpandRecurrences bool `json:"expandRecurrences"`
}

type evQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type evQueryHandler struct{ h *handlerSet }

func (h *evQueryHandler) Method() string { return "CalendarEvent/query" }

func (h *evQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req evQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentCalendarEventState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	filter := store.CalendarEventFilter{PrincipalID: &pid}
	expand := req.ExpandRecurrences
	var startAfter, startBefore *time.Time
	if req.Filter != nil {
		if req.Filter.CalendarID != nil {
			calID, ok := calendarIDFromJMAP(*req.Filter.CalendarID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments", "calendarId is malformed")
			}
			filter.CalendarID = &calID
		}
		if req.Filter.UID != nil {
			uid := *req.Filter.UID
			filter.UID = &uid
		}
		if req.Filter.StartAfter != nil {
			t := *req.Filter.StartAfter
			startAfter = &t
		}
		if req.Filter.StartBefore != nil {
			t := *req.Filter.StartBefore
			startBefore = &t
		}
		if req.Filter.Status != nil {
			s := *req.Filter.Status
			filter.Status = &s
		}
		if req.Filter.ExpandRecurrences != nil && *req.Filter.ExpandRecurrences {
			expand = true
		}
	}
	rows, err := h.h.store.Meta().ListCalendarEvents(ctx, filter)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := make([]store.CalendarEvent, 0, len(rows))
	for _, ev := range rows {
		if !matchEventFilter(ev, req.Filter, startAfter, startBefore, expand) {
			continue
		}
		matched = append(matched, ev)
	}
	sortEvents(matched, req.Sort)

	resp := evQueryResponse{
		AccountID:  string(protojmap.AccountIDForPrincipal(pid)),
		QueryState: state,
		IDs:        []jmapID{},
	}

	// Recurrence expansion: when the client asked to expand and we
	// have a window, emit one id per occurrence (the master id is
	// repeated; clients use start/end from the rendered occurrence,
	// per the binding draft's expanded view).
	if expand && startAfter != nil && startBefore != nil {
		ids := h.h.expandMatched(matched, *startAfter, *startBefore)
		total := len(ids)
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
		resp.IDs = append(resp.IDs, ids[start:end]...)
		return resp, nil
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
	for _, ev := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromEvent(ev.ID))
	}
	return resp, nil
}

// expandMatched runs jscalendar.Expand on every recurring matched
// event within [from, until], capping per-master at
// h.limits.MaxOccurrencesPerExpansion. Non-recurring rows pass through
// once; recurring rows produce one id per occurrence (the master id
// repeated; the client correlates occurrences via the rendered
// recurrenceId per the binding draft).
func (h *handlerSet) expandMatched(events []store.CalendarEvent, from, until time.Time) []jmapID {
	cap := h.limits.MaxOccurrencesPerExpansion
	if cap <= 0 {
		cap = DefaultMaxOccurrencesPerExpansion
	}
	out := make([]jmapID, 0, len(events))
	for _, ev := range events {
		if !ev.IsRecurring {
			out = append(out, jmapIDFromEvent(ev.ID))
			continue
		}
		var jev jscalendar.Event
		if err := jev.UnmarshalJSON(ev.JSCalendarJSON); err != nil {
			// Bad blob: surface the master once and skip expansion.
			out = append(out, jmapIDFromEvent(ev.ID))
			continue
		}
		occs, eerr := jev.Expand(from, until, cap)
		if eerr != nil || len(occs) == 0 {
			out = append(out, jmapIDFromEvent(ev.ID))
			continue
		}
		id := jmapIDFromEvent(ev.ID)
		for range occs {
			out = append(out, id)
		}
	}
	return out
}

func matchEventFilter(
	ev store.CalendarEvent,
	f *evFilterCondition,
	startAfter, startBefore *time.Time,
	expand bool,
) bool {
	if f == nil {
		return true
	}
	if f.Text != nil && *f.Text != "" {
		if !strings.Contains(strings.ToLower(ev.Summary), strings.ToLower(*f.Text)) {
			return false
		}
	}
	if !expand {
		// Non-expanded mode: gate the master row on the [startAfter,
		// startBefore) window. Recurring masters always pass; the
		// /query expansion path handles the per-occurrence window.
		if startAfter != nil && ev.Start.Before(*startAfter) && !ev.IsRecurring {
			return false
		}
		if startBefore != nil && !ev.Start.Before(*startBefore) && !ev.IsRecurring {
			return false
		}
	}
	return true
}

// microsToTime is a small UTC-microsecond -> time.Time projection,
// used by the JSCalendar bridge so the JMAP layer can populate
// store.CalendarEvent.Start / End from the typed Event helpers.
func microsToTime(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.Unix(us/1_000_000, (us%1_000_000)*1_000).UTC()
}

func sortEvents(xs []store.CalendarEvent, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "start"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareEvent(xs[i], xs[j], c.Property)
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

func compareEvent(a, b store.CalendarEvent, property string) int {
	switch property {
	case "start":
		switch {
		case a.Start.Before(b.Start):
			return -1
		case a.Start.After(b.Start):
			return 1
		}
		return 0
	case "updated":
		switch {
		case a.UpdatedAt.Before(b.UpdatedAt):
			return -1
		case a.UpdatedAt.After(b.UpdatedAt):
			return 1
		}
		return 0
	case "uid":
		return strings.Compare(a.UID, b.UID)
	}
	return 0
}

// -- CalendarEvent/queryChanges ---------------------------------------

type evQueryChangesHandler struct{ h *handlerSet }

func (evQueryChangesHandler) Method() string { return "CalendarEvent/queryChanges" }

func (evQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"CalendarEvent/queryChanges is unsupported; clients re-issue CalendarEvent/query")
}
