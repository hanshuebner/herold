package contacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- Contact/get ------------------------------------------------------

type contactGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type contactGetResponse struct {
	AccountID jmapID            `json:"accountId"`
	State     string            `json:"state"`
	List      []json.RawMessage `json:"list"`
	NotFound  []jmapID          `json:"notFound"`
}

type contactGetHandler struct{ h *handlerSet }

func (h *contactGetHandler) Method() string { return "Contact/get" }

func (h *contactGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req contactGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentContactState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := contactGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []json.RawMessage{},
		NotFound:  []jmapID{},
	}

	if req.IDs == nil {
		// "ids: null" returns every contact accessible to the principal.
		rows, err := h.h.store.Meta().ListContacts(ctx, store.ContactFilter{
			PrincipalID: &pid,
		})
		if err != nil {
			return nil, serverFail(err)
		}
		for _, c := range rows {
			rendered, rerr := renderContact(c)
			if rerr != nil {
				return nil, serverFail(rerr)
			}
			resp.List = append(resp.List, rendered)
		}
		return resp, nil
	}

	for _, raw := range *req.IDs {
		id, ok := contactIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		c, err := h.h.store.Meta().GetContact(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			return nil, serverFail(err)
		}
		if c.PrincipalID != pid {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		rendered, rerr := renderContact(c)
		if rerr != nil {
			return nil, serverFail(rerr)
		}
		resp.List = append(resp.List, rendered)
	}
	return resp, nil
}

// renderContact produces the wire-form Contact JSON: the JSContact
// Card body merged with the JMAP-projected properties (id,
// addressBookId, myRights). The merge happens at the JSON level so we
// preserve any RawJSON keys the typed Card does not model.
func renderContact(c store.Contact) (json.RawMessage, error) {
	// Decode the stored JSContact body, then re-encode with our id /
	// addressBookId / myRights overlays so the result is one JSON
	// object.
	out := map[string]json.RawMessage{}
	if len(c.JSContactJSON) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(c.JSContactJSON, &raw); err != nil {
			return nil, fmt.Errorf("contacts: parse stored card: %w", err)
		}
		for k, v := range raw {
			out[k] = v
		}
	}
	idBytes, _ := json.Marshal(jmapIDFromContact(c.ID))
	out["id"] = idBytes
	abBytes, _ := json.Marshal(jmapIDFromAddressBook(c.AddressBookID))
	out["addressBookId"] = abBytes
	rightsBytes, _ := json.Marshal(rightsForAddressBookOwner())
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

// -- Contact/changes --------------------------------------------------

type contactChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type contactChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type contactChangesHandler struct{ h *handlerSet }

func (h *contactChangesHandler) Method() string { return "Contact/changes" }

func (h *contactChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req contactChangesRequest
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
	newState := stateFromCounter(st.Contact)
	resp := contactChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.Contact {
		return resp, nil
	}
	if since > st.Contact {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}
	created, updated, destroyed, ferr := walkChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindContact, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromContact(store.ContactID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromContact(store.ContactID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromContact(store.ContactID(id)))
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

// -- Contact/set ------------------------------------------------------

type contactSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type contactSetResponse struct {
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

type contactSetHandler struct{ h *handlerSet }

func (h *contactSetHandler) Method() string { return "Contact/set" }

func (h *contactSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req contactSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentContactState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}
	resp := contactSetResponse{
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
	creationRefs := make(map[string]store.ContactID, len(req.Create))

	for key, raw := range req.Create {
		c, serr, err := h.h.createContact(ctx, pid, raw)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = c.ID
		rendered, rerr := renderContact(c)
		if rerr != nil {
			return nil, serverFail(rerr)
		}
		resp.Created[key] = rendered
	}

	for raw, payload := range req.Update {
		id, ok := resolveContactID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateContact(ctx, pid, id, payload)
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
		id, ok := resolveContactID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyContact(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentContactState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveContactID(raw jmapID, creationRefs map[string]store.ContactID) (store.ContactID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return contactIDFromJMAP(raw)
}

// createContact validates the JSContact body, mints a UID when absent,
// resolves the parent address book, and persists.
func (h *handlerSet) createContact(
	ctx context.Context,
	pid store.PrincipalID,
	raw json.RawMessage,
) (store.Contact, *setError, error) {
	if len(raw) == 0 {
		return store.Contact{}, &setError{Type: "invalidProperties", Description: "empty body"}, nil
	}
	// Wave 2.9 sec audit #4: enforce maxSizePerContactBlob on the raw
	// inbound JSON before any further parsing. Mirrors the calendars
	// event-blob check in internal/protojmap/calendars/event.go:368.
	if h.limits.MaxSizePerContactBlob > 0 && len(raw) > h.limits.MaxSizePerContactBlob {
		return store.Contact{}, &setError{
			Type: "tooLarge", Description: "contact blob exceeds maxSizePerContactBlob",
		}, nil
	}
	// Two passes: extract addressBookId (a JMAP-projected wrapper
	// property), then strip it before re-serialising the JSContact body.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return store.Contact{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	abRaw, hasAB := probe["addressBookId"]
	if !hasAB {
		return store.Contact{}, &setError{
			Type: "invalidProperties", Properties: []string{"addressBookId"},
			Description: "addressBookId is required",
		}, nil
	}
	var abIDStr string
	if err := json.Unmarshal(abRaw, &abIDStr); err != nil {
		return store.Contact{}, &setError{
			Type: "invalidProperties", Properties: []string{"addressBookId"},
			Description: "addressBookId must be a string",
		}, nil
	}
	abID, ok := addressBookIDFromJMAP(abIDStr)
	if !ok {
		return store.Contact{}, &setError{
			Type: "invalidProperties", Properties: []string{"addressBookId"},
			Description: "unknown addressBookId",
		}, nil
	}
	ab, err := h.store.Meta().GetAddressBook(ctx, abID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Contact{}, &setError{
				Type: "invalidProperties", Properties: []string{"addressBookId"},
				Description: "addressBookId references unknown address book",
			}, nil
		}
		return store.Contact{}, nil, fmt.Errorf("contacts: load book: %w", err)
	}
	if ab.PrincipalID != pid {
		return store.Contact{}, &setError{
			Type: "invalidProperties", Properties: []string{"addressBookId"},
			Description: "addressBookId is not accessible to this principal",
		}, nil
	}

	// Strip JMAP-projected properties from the body before storing.
	delete(probe, "addressBookId")
	delete(probe, "id")
	delete(probe, "myRights")

	// Round-trip to a Card to populate the typed fields, mint a UID if
	// the client omitted one, default the version to "1.0" so the v1
	// validator passes, and validate.
	body, err := json.Marshal(probe)
	if err != nil {
		return store.Contact{}, nil, fmt.Errorf("contacts: re-serialise body: %w", err)
	}
	var card Card
	if err := card.UnmarshalJSON(body); err != nil {
		return store.Contact{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	if card.Version == "" {
		card.Version = "1.0"
	}
	if card.UID == "" {
		uid, err := mintUID()
		if err != nil {
			return store.Contact{}, nil, err
		}
		card.UID = uid
	}
	if err := card.Validate(); err != nil {
		return store.Contact{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	finalBody, err := json.Marshal(card)
	if err != nil {
		return store.Contact{}, nil, fmt.Errorf("contacts: marshal card: %w", err)
	}

	row := store.Contact{
		PrincipalID:   pid,
		AddressBookID: abID,
		UID:           card.UID,
		DisplayName:   card.DisplayName(),
		PrimaryEmail:  card.PrimaryEmail(),
		GivenName:     card.GivenName(),
		Surname:       card.Surname(),
		OrgName:       card.OrgName(),
		SearchBlob:    card.SearchBlob(),
		JSContactJSON: finalBody,
	}
	cid, err := h.store.Meta().InsertContact(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.Contact{}, &setError{
				Type:        "invalidProperties",
				Description: "contact uid conflicts with existing contact",
			}, nil
		}
		return store.Contact{}, nil, fmt.Errorf("contacts: insert: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindContact); err != nil {
		return store.Contact{}, nil, fmt.Errorf("contacts: bump state: %w", err)
	}
	loaded, err := h.store.Meta().GetContact(ctx, cid)
	if err != nil {
		return store.Contact{}, nil, fmt.Errorf("contacts: reload: %w", err)
	}

	// Auto-promotion (REQ-MAIL-11l): when a new contact carries a primary
	// email, remove any SeenAddress row for that address. The contact is
	// now the authoritative entry; keeping both would cause stale
	// duplication in the autocomplete surface. Best-effort — failure here
	// is non-fatal for the Contact/set response.
	if loaded.PrimaryEmail != "" {
		_ = h.store.Meta().DestroySeenAddressByEmail(ctx, pid, loaded.PrimaryEmail)
	}

	return loaded, nil, nil
}

// updateContact applies a JSON Merge Patch to the stored Card body,
// re-derives the denormalised columns, and persists. The merge is
// shallow: top-level keys in the patch overwrite the stored value;
// json null deletes the key.
func (h *handlerSet) updateContact(
	ctx context.Context,
	pid store.PrincipalID,
	id store.ContactID,
	raw json.RawMessage,
) (*setError, error) {
	c, err := h.store.Meta().GetContact(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: load contact: %w", err)
	}
	if c.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}

	var patch map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &patch); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}

	// Merge patch onto the stored body.
	var stored map[string]json.RawMessage
	if len(c.JSContactJSON) > 0 {
		if err := json.Unmarshal(c.JSContactJSON, &stored); err != nil {
			return nil, fmt.Errorf("contacts: parse stored body: %w", err)
		}
	} else {
		stored = map[string]json.RawMessage{}
	}
	for k, v := range patch {
		// JMAP-projected fields aren't stored in the body; honour
		// addressBookId moves explicitly.
		if k == "id" || k == "myRights" {
			continue
		}
		if k == "addressBookId" {
			var abIDStr string
			if err := json.Unmarshal(v, &abIDStr); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"addressBookId"},
					Description: "addressBookId must be a string",
				}, nil
			}
			newABID, ok := addressBookIDFromJMAP(abIDStr)
			if !ok {
				return &setError{
					Type: "invalidProperties", Properties: []string{"addressBookId"},
					Description: "unknown addressBookId",
				}, nil
			}
			ab, gerr := h.store.Meta().GetAddressBook(ctx, newABID)
			if gerr != nil || ab.PrincipalID != pid {
				return &setError{
					Type: "invalidProperties", Properties: []string{"addressBookId"},
					Description: "addressBookId is not accessible to this principal",
				}, nil
			}
			c.AddressBookID = newABID
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
		return nil, fmt.Errorf("contacts: marshal merged body: %w", err)
	}
	// Wave 2.9 sec audit #4: enforce maxSizePerContactBlob on the
	// merged body. Mirrors the calendar-event update path.
	if h.limits.MaxSizePerContactBlob > 0 && len(body) > h.limits.MaxSizePerContactBlob {
		return &setError{Type: "tooLarge", Description: "merged contact blob exceeds maxSizePerContactBlob"}, nil
	}
	var card Card
	if err := card.UnmarshalJSON(body); err != nil {
		return &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	if card.Version == "" {
		card.Version = "1.0"
	}
	if card.UID == "" {
		card.UID = c.UID
	}
	if err := card.Validate(); err != nil {
		return &setError{Type: "invalidProperties", Description: err.Error()}, nil
	}
	finalBody, err := json.Marshal(card)
	if err != nil {
		return nil, fmt.Errorf("contacts: marshal card: %w", err)
	}

	c.UID = card.UID
	c.DisplayName = card.DisplayName()
	c.PrimaryEmail = card.PrimaryEmail()
	c.GivenName = card.GivenName()
	c.Surname = card.Surname()
	c.OrgName = card.OrgName()
	c.SearchBlob = card.SearchBlob()
	c.JSContactJSON = finalBody

	if err := h.store.Meta().UpdateContact(ctx, c); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: update: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindContact); err != nil {
		return nil, fmt.Errorf("contacts: bump state: %w", err)
	}
	return nil, nil
}

func (h *handlerSet) destroyContact(
	ctx context.Context,
	pid store.PrincipalID,
	id store.ContactID,
) (*setError, error) {
	c, err := h.store.Meta().GetContact(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: load: %w", err)
	}
	if c.PrincipalID != pid {
		return &setError{Type: "notFound"}, nil
	}
	if err := h.store.Meta().DeleteContact(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("contacts: delete: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindContact); err != nil {
		return nil, fmt.Errorf("contacts: bump state: %w", err)
	}
	return nil, nil
}

// -- Contact/query ----------------------------------------------------

type contactFilterCondition struct {
	AddressBookID *jmapID `json:"addressBookId"`
	Text          *string `json:"text"`
	HasEmail      *bool   `json:"hasEmail"`
	UID           *string `json:"uid"`
}

type contactQueryRequest struct {
	AccountID      jmapID                  `json:"accountId"`
	Filter         *contactFilterCondition `json:"filter"`
	Sort           []comparator            `json:"sort"`
	Position       int                     `json:"position"`
	Anchor         *jmapID                 `json:"anchor"`
	AnchorOffset   int                     `json:"anchorOffset"`
	Limit          *int                    `json:"limit"`
	CalculateTotal bool                    `json:"calculateTotal"`
}

type contactQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type contactQueryHandler struct{ h *handlerSet }

func (h *contactQueryHandler) Method() string { return "Contact/query" }

func (h *contactQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req contactQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentContactState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	filter := store.ContactFilter{PrincipalID: &pid}
	if req.Filter != nil {
		if req.Filter.AddressBookID != nil {
			abID, ok := addressBookIDFromJMAP(*req.Filter.AddressBookID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments", "addressBookId is malformed")
			}
			filter.AddressBookID = &abID
		}
		if req.Filter.UID != nil {
			uid := *req.Filter.UID
			filter.UID = &uid
		}
	}
	rows, err := h.h.store.Meta().ListContacts(ctx, filter)
	if err != nil {
		return nil, serverFail(err)
	}

	// Apply text and hasEmail filters in-process; the store filter
	// surface is pre-keyed per the prompt's described shape and we keep
	// the JMAP-side filter expressive without forcing every predicate
	// into the store.
	matched := make([]store.Contact, 0, len(rows))
	for _, c := range rows {
		if !matchContactFilter(c, req.Filter) {
			continue
		}
		matched = append(matched, c)
	}
	sortContacts(matched, req.Sort)

	resp := contactQueryResponse{
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
		resp.IDs = append(resp.IDs, jmapIDFromContact(c.ID))
	}
	return resp, nil
}

func matchContactFilter(c store.Contact, f *contactFilterCondition) bool {
	if f == nil {
		return true
	}
	if f.Text != nil && *f.Text != "" {
		needle := strings.ToLower(*f.Text)
		if !strings.Contains(strings.ToLower(c.SearchBlob), needle) {
			return false
		}
	}
	if f.HasEmail != nil {
		has := c.PrimaryEmail != ""
		if *f.HasEmail != has {
			return false
		}
	}
	if f.UID != nil {
		if c.UID != *f.UID {
			return false
		}
	}
	return true
}

func sortContacts(xs []store.Contact, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "displayName"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareContact(xs[i], xs[j], c.Property)
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

func compareContact(a, b store.Contact, property string) int {
	switch property {
	case "displayName":
		return strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName))
	case "created":
		switch {
		case a.CreatedAt.Before(b.CreatedAt):
			return -1
		case a.CreatedAt.After(b.CreatedAt):
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
	}
	return 0
}

// -- Contact/queryChanges ---------------------------------------------

type contactQueryChangesHandler struct{ h *handlerSet }

func (contactQueryChangesHandler) Method() string { return "Contact/queryChanges" }

func (contactQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Contact/queryChanges is unsupported; clients re-issue Contact/query")
}
