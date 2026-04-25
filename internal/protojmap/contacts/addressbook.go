package contacts

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

// -- AddressBook/get --------------------------------------------------

// abGetRequest is the wire-form AddressBook/get request.
type abGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

// abGetResponse is the wire-form AddressBook/get response.
type abGetResponse struct {
	AccountID jmapID            `json:"accountId"`
	State     string            `json:"state"`
	List      []jmapAddressBook `json:"list"`
	NotFound  []jmapID          `json:"notFound"`
}

type abGetHandler struct{ h *handlerSet }

func (h *abGetHandler) Method() string { return "AddressBook/get" }

func (h *abGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req abGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentAddressBookState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	all, err := listOwnedAddressBooks(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	defID := defaultAddressBookID(ctx, h.h.store.Meta(), pid)

	resp := abGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapAddressBook{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		for _, ab := range all {
			resp.List = append(resp.List, renderAddressBook(ab, defID))
		}
		return resp, nil
	}

	byID := make(map[store.AddressBookID]store.AddressBook, len(all))
	for _, ab := range all {
		byID[ab.ID] = ab
	}
	for _, raw := range *req.IDs {
		id, ok := addressBookIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		ab, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		resp.List = append(resp.List, renderAddressBook(ab, defID))
	}
	return resp, nil
}

// listOwnedAddressBooks returns every address book owned by the
// principal. The store filter is keyed on PrincipalID; we wrap that here
// so handlers don't repeat the filter assembly.
func listOwnedAddressBooks(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) ([]store.AddressBook, error) {
	return meta.ListAddressBooks(ctx, store.AddressBookFilter{
		PrincipalID: &pid,
	})
}

// defaultAddressBookID returns the principal's default AddressBookID,
// or 0 when the principal has none. We swallow ErrNotFound since the
// "no default yet" case is normal (e.g. fresh principal).
func defaultAddressBookID(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
) store.AddressBookID {
	ab, err := meta.DefaultAddressBook(ctx, pid)
	if err != nil {
		return 0
	}
	return ab.ID
}

// renderAddressBook converts a store.AddressBook into wire form,
// stamping the per-principal default flag and the owner rights mask.
func renderAddressBook(ab store.AddressBook, defID store.AddressBookID) jmapAddressBook {
	isDefault := ab.IsDefault
	if defID != 0 {
		isDefault = isDefault || ab.ID == defID
	}
	out := jmapAddressBook{
		ID:           jmapIDFromAddressBook(ab.ID),
		Name:         ab.Name,
		SortOrder:    ab.SortOrder,
		IsSubscribed: ab.IsSubscribed,
		IsDefault:    isDefault,
		MyRights:     rightsForAddressBookOwner(),
	}
	if ab.Description != "" {
		s := ab.Description
		out.Description = &s
	}
	if ab.Color != nil {
		s := *ab.Color
		out.Color = &s
	}
	return out
}

// -- AddressBook/changes ----------------------------------------------

type abChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type abChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type abChangesHandler struct{ h *handlerSet }

func (h *abChangesHandler) Method() string { return "AddressBook/changes" }

func (h *abChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req abChangesRequest
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
	newState := stateFromCounter(st.AddressBook)

	resp := abChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.AddressBook {
		return resp, nil
	}
	if since > st.AddressBook {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}

	created, updated, destroyed, ferr := walkChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindAddressBook, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromAddressBook(store.AddressBookID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromAddressBook(store.AddressBookID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromAddressBook(store.AddressBookID(id)))
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
// produced by entries with seq > since.
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

// -- AddressBook/set --------------------------------------------------

type abSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

type abSetResponse struct {
	AccountID    jmapID                     `json:"accountId"`
	OldState     string                     `json:"oldState"`
	NewState     string                     `json:"newState"`
	Created      map[string]jmapAddressBook `json:"created"`
	Updated      map[jmapID]any             `json:"updated"`
	Destroyed    []jmapID                   `json:"destroyed"`
	NotCreated   map[string]setError        `json:"notCreated"`
	NotUpdated   map[jmapID]setError        `json:"notUpdated"`
	NotDestroyed map[jmapID]setError        `json:"notDestroyed"`
}

type abCreateInput struct {
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	SortOrder    *int    `json:"sortOrder"`
	IsSubscribed *bool   `json:"isSubscribed"`
	IsDefault    *bool   `json:"isDefault"`
	Color        *string `json:"color"`
}

type abUpdateInput struct {
	Name         *string         `json:"name"`
	Description  json.RawMessage `json:"description"`
	SortOrder    *int            `json:"sortOrder"`
	IsSubscribed *bool           `json:"isSubscribed"`
	IsDefault    *bool           `json:"isDefault"`
	Color        json.RawMessage `json:"color"`
}

type abSetHandler struct{ h *handlerSet }

func (h *abSetHandler) Method() string { return "AddressBook/set" }

func (h *abSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req abSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentAddressBookState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}

	resp := abSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapAddressBook{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.AddressBookID, len(req.Create))

	for key, raw := range req.Create {
		var in abCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		ab, serr, err := h.h.createAddressBook(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = ab.ID
		defID := defaultAddressBookID(ctx, h.h.store.Meta(), pid)
		resp.Created[key] = renderAddressBook(ab, defID)
	}

	for raw, payload := range req.Update {
		id, ok := resolveAddressBookID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateAddressBook(ctx, pid, id, payload)
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
		id, ok := resolveAddressBookID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyAddressBook(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentAddressBookState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveAddressBookID(raw jmapID, creationRefs map[string]store.AddressBookID) (store.AddressBookID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return addressBookIDFromJMAP(raw)
}

func (h *handlerSet) createAddressBook(
	ctx context.Context,
	pid store.PrincipalID,
	in abCreateInput,
) (store.AddressBook, *setError, error) {
	if strings.TrimSpace(in.Name) == "" {
		return store.AddressBook{}, &setError{
			Type: "invalidProperties", Properties: []string{"name"},
			Description: "name is required",
		}, nil
	}
	// Per the binding draft, name must be unique within the principal.
	owned, err := listOwnedAddressBooks(ctx, h.store.Meta(), pid)
	if err != nil {
		return store.AddressBook{}, nil, fmt.Errorf("contacts: list books: %w", err)
	}
	for _, ab := range owned {
		if strings.EqualFold(ab.Name, in.Name) {
			return store.AddressBook{}, &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "another address book with this name already exists",
			}, nil
		}
	}

	row := store.AddressBook{
		PrincipalID:  pid,
		Name:         in.Name,
		IsSubscribed: true,
	}
	if in.Description != nil {
		row.Description = *in.Description
	}
	if in.SortOrder != nil {
		row.SortOrder = *in.SortOrder
	}
	if in.IsSubscribed != nil {
		row.IsSubscribed = *in.IsSubscribed
	}
	if in.Color != nil {
		v := *in.Color
		row.Color = &v
	}
	// Auto-set is_default=true when no other default exists.
	if defID := defaultAddressBookID(ctx, h.store.Meta(), pid); defID == 0 {
		row.IsDefault = true
	} else if in.IsDefault != nil && *in.IsDefault {
		row.IsDefault = true
	}

	id, err := h.store.Meta().InsertAddressBook(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.AddressBook{}, &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "another address book with this name already exists",
			}, nil
		}
		return store.AddressBook{}, nil, fmt.Errorf("contacts: insert book: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindAddressBook); err != nil {
		return store.AddressBook{}, nil, fmt.Errorf("contacts: bump book state: %w", err)
	}
	loaded, err := h.store.Meta().GetAddressBook(ctx, id)
	if err != nil {
		return store.AddressBook{}, nil, fmt.Errorf("contacts: reload book: %w", err)
	}
	return loaded, nil, nil
}

func (h *handlerSet) updateAddressBook(
	ctx context.Context,
	pid store.PrincipalID,
	id store.AddressBookID,
	raw json.RawMessage,
) (*setError, error) {
	ab, err := h.store.Meta().GetAddressBook(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: load book: %w", err)
	}
	if ab.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}

	var in abUpdateInput
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
		owned, err := listOwnedAddressBooks(ctx, h.store.Meta(), pid)
		if err != nil {
			return nil, fmt.Errorf("contacts: list books: %w", err)
		}
		for _, other := range owned {
			if other.ID != ab.ID && strings.EqualFold(other.Name, *in.Name) {
				return &setError{
					Type: "invalidProperties", Properties: []string{"name"},
					Description: "another address book with this name already exists",
				}, nil
			}
		}
		ab.Name = *in.Name
	}
	if in.SortOrder != nil {
		ab.SortOrder = *in.SortOrder
	}
	if in.IsSubscribed != nil {
		ab.IsSubscribed = *in.IsSubscribed
	}
	if in.IsDefault != nil {
		ab.IsDefault = *in.IsDefault
	}
	if len(in.Description) > 0 {
		if string(in.Description) == "null" {
			ab.Description = ""
		} else {
			var s string
			if err := json.Unmarshal(in.Description, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"description"},
					Description: "description must be a string or null",
				}, nil
			}
			ab.Description = s
		}
	}
	if len(in.Color) > 0 {
		if string(in.Color) == "null" {
			ab.Color = nil
		} else {
			var s string
			if err := json.Unmarshal(in.Color, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"color"},
					Description: "color must be a string or null",
				}, nil
			}
			ab.Color = &s
		}
	}

	if err := h.store.Meta().UpdateAddressBook(ctx, ab); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		if errors.Is(err, store.ErrConflict) {
			return &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "name conflicts with existing address book",
			}, nil
		}
		return nil, fmt.Errorf("contacts: update book: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindAddressBook); err != nil {
		return nil, fmt.Errorf("contacts: bump book state: %w", err)
	}
	return nil, nil
}

func (h *handlerSet) destroyAddressBook(
	ctx context.Context,
	pid store.PrincipalID,
	id store.AddressBookID,
) (*setError, error) {
	ab, err := h.store.Meta().GetAddressBook(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: load book: %w", err)
	}
	if ab.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	// Cascade is handled by ON DELETE CASCADE in the schema; the
	// in-process consumer simply calls DeleteAddressBook.
	if err := h.store.Meta().DeleteAddressBook(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: delete book: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindAddressBook); err != nil {
		return nil, fmt.Errorf("contacts: bump book state: %w", err)
	}
	// The cascade may have removed contacts; bump the contact state too
	// so clients re-sync. (It is harmless to bump even when the book
	// was empty.)
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindContact); err != nil {
		return nil, fmt.Errorf("contacts: bump contact state: %w", err)
	}
	return nil, nil
}

// -- AddressBook/query ------------------------------------------------

type abFilterCondition struct {
	Name *string `json:"name"`
}

type comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation,omitempty"`
}

type abQueryRequest struct {
	AccountID      jmapID             `json:"accountId"`
	Filter         *abFilterCondition `json:"filter"`
	Sort           []comparator       `json:"sort"`
	Position       int                `json:"position"`
	Anchor         *jmapID            `json:"anchor"`
	AnchorOffset   int                `json:"anchorOffset"`
	Limit          *int               `json:"limit"`
	CalculateTotal bool               `json:"calculateTotal"`
}

type abQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type abQueryHandler struct{ h *handlerSet }

func (h *abQueryHandler) Method() string { return "AddressBook/query" }

func (h *abQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req abQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentAddressBookState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	all, err := listOwnedAddressBooks(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	matched := make([]store.AddressBook, 0, len(all))
	for _, ab := range all {
		if matchAddressBookFilter(ab, req.Filter) {
			matched = append(matched, ab)
		}
	}
	sortAddressBooks(matched, req.Sort)
	resp := abQueryResponse{
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
	for _, ab := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromAddressBook(ab.ID))
	}
	return resp, nil
}

func matchAddressBookFilter(ab store.AddressBook, f *abFilterCondition) bool {
	if f == nil {
		return true
	}
	if f.Name != nil {
		if !strings.EqualFold(ab.Name, *f.Name) {
			return false
		}
	}
	return true
}

func sortAddressBooks(xs []store.AddressBook, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "name"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareAddressBook(xs[i], xs[j], c.Property)
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

func compareAddressBook(a, b store.AddressBook, property string) int {
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

// -- AddressBook/queryChanges -----------------------------------------

type abQueryChangesHandler struct{ h *handlerSet }

func (abQueryChangesHandler) Method() string { return "AddressBook/queryChanges" }

func (abQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"AddressBook/queryChanges is unsupported; clients re-issue AddressBook/query")
}
