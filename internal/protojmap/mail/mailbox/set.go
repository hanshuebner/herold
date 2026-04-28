package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// setRequest is the wire-form Mailbox/set request (RFC 8620 §5.3).
type setRequest struct {
	AccountID    jmapID                     `json:"accountId"`
	IfInState    *string                    `json:"ifInState"`
	Create       map[string]json.RawMessage `json:"create"`
	Update       map[jmapID]json.RawMessage `json:"update"`
	Destroy      []jmapID                   `json:"destroy"`
	OnDestroyRem *bool                      `json:"onDestroyRemoveEmails"`
}

// setError is the wire-form SetError (RFC 8620 §5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

// setResponse is the wire-form Mailbox/set response.
type setResponse struct {
	AccountID    jmapID                 `json:"accountId"`
	OldState     string                 `json:"oldState"`
	NewState     string                 `json:"newState"`
	Created      map[string]jmapMailbox `json:"created"`
	Updated      map[jmapID]any         `json:"updated"`
	Destroyed    []jmapID               `json:"destroyed"`
	NotCreated   map[string]setError    `json:"notCreated"`
	NotUpdated   map[jmapID]setError    `json:"notUpdated"`
	NotDestroyed map[jmapID]setError    `json:"notDestroyed"`
}

type mailboxCreateInput struct {
	Name         string  `json:"name"`
	ParentID     *jmapID `json:"parentId"`
	Role         *string `json:"role"`
	IsSubscribed *bool   `json:"isSubscribed"`
	SortOrder    *int    `json:"sortOrder"`
	// Color is the JMAP-only extension property (REQ-PROTO-56 /
	// REQ-STORE-34). Hex literal "#RRGGBB"; null/absent means unset.
	Color *string `json:"color,omitempty"`
}

// mailboxUpdateInput uses json.RawMessage for fields where we must
// distinguish "absent" (no change) from explicit null (clear) per
// JMAP /set update semantics.
// - Color: null clears the colour; absent means no change.
// - ParentID: null moves to top-level; absent means no change.
type mailboxUpdateInput struct {
	Name         *string         `json:"name"`
	ParentID     json.RawMessage `json:"parentId"`
	Role         *string         `json:"role"`
	IsSubscribed *bool           `json:"isSubscribed"`
	SortOrder    *int            `json:"sortOrder"`
	Color        json.RawMessage `json:"color"`
}

// setHandler implements Mailbox/set.
type setHandler struct{ h *handlerSet }

func (s *setHandler) Method() string { return "Mailbox/set" }

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
		Created:      map[string]jmapMailbox{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.MailboxID, len(req.Create))

	for key, raw := range req.Create {
		var in mailboxCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{
					Type: "invalidProperties", Description: err.Error(),
				}
				continue
			}
		}
		mb, serr, err := s.h.createMailbox(ctx, pid, in, creationRefs)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = mb.ID
		rendered, err := renderMailbox(ctx, s.h.store.Meta(), pid, mb)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.Created[key] = rendered
	}

	for raw, payload := range req.Update {
		mid, ok := resolveID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := s.h.updateMailbox(ctx, pid, mid, payload)
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
		mid, ok := resolveID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := s.h.destroyMailbox(ctx, pid, mid, req.OnDestroyRem)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentState(ctx, s.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveID(raw jmapID, creationRefs map[string]store.MailboxID) (store.MailboxID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return mailboxIDFromJMAP(raw)
}

// createMailbox — handlerSet method so other files can reach it.
func (h *handlerSet) createMailbox(
	ctx context.Context,
	pid store.PrincipalID,
	in mailboxCreateInput,
	creationRefs map[string]store.MailboxID,
) (store.Mailbox, *setError, error) {
	if strings.TrimSpace(in.Name) == "" {
		return store.Mailbox{}, &setError{
			Type: "invalidProperties", Properties: []string{"name"},
			Description: "name is required",
		}, nil
	}

	parentID := store.MailboxID(0)
	if in.ParentID != nil && *in.ParentID != "" {
		pid2, ok := resolveID(*in.ParentID, creationRefs)
		if !ok {
			return store.Mailbox{}, &setError{
				Type: "invalidProperties", Properties: []string{"parentId"},
				Description: "parentId references unknown mailbox",
			}, nil
		}
		parent, err := loadMailboxForPrincipal(ctx, h.store.Meta(), pid, pid2)
		if err != nil {
			if errors.Is(err, errMailboxMissing) {
				return store.Mailbox{}, &setError{
					Type: "invalidProperties", Properties: []string{"parentId"},
					Description: "parent mailbox does not exist",
				}, nil
			}
			return store.Mailbox{}, nil, err
		}
		rights, err := rightsForPrincipal(ctx, h.store.Meta(), pid, parent)
		if err != nil {
			return store.Mailbox{}, nil, err
		}
		if !rights.MayCreateChild {
			return store.Mailbox{}, &setError{
				Type:        "forbidden",
				Description: "principal lacks mayCreateChild on parent mailbox",
			}, nil
		}
		parentID = parent.ID
	}

	attrs := store.MailboxAttributes(0)
	if in.Role != nil {
		got, ok := attributesFromRole(*in.Role)
		if !ok {
			return store.Mailbox{}, &setError{
				Type: "invalidProperties", Properties: []string{"role"},
				Description: "unknown role",
			}, nil
		}
		owned, err := h.store.Meta().ListMailboxes(ctx, pid)
		if err != nil {
			return store.Mailbox{}, nil, err
		}
		for _, mb := range owned {
			if got != 0 && mb.Attributes&got != 0 {
				return store.Mailbox{}, &setError{
					Type: "invalidProperties", Properties: []string{"role"},
					Description: "another mailbox already holds this role",
				}, nil
			}
		}
		attrs |= got
	}
	if in.IsSubscribed != nil && *in.IsSubscribed {
		attrs |= store.MailboxAttrSubscribed
	}

	var color *string
	if in.Color != nil {
		v := *in.Color
		if !validJMAPColor(v) {
			return store.Mailbox{}, &setError{
				Type: "invalidProperties", Properties: []string{"color"},
				Description: "color must be the hex literal #RRGGBB",
			}, nil
		}
		color = &v
	}

	sortOrder := uint32(0)
	if in.SortOrder != nil {
		sortOrder = uint32(*in.SortOrder)
	}

	mb, err := h.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: pid,
		ParentID:    parentID,
		Name:        in.Name,
		Attributes:  attrs,
		Color:       color,
		SortOrder:   sortOrder,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// RFC 8621 §2.5: duplicate name under same parent is alreadyExists.
			return store.Mailbox{}, &setError{
				Type:        "alreadyExists",
				Description: "a mailbox with this name already exists under the same parent",
			}, nil
		}
		return store.Mailbox{}, nil, fmt.Errorf("mailbox: insert: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindMailbox); err != nil {
		return store.Mailbox{}, nil, fmt.Errorf("mailbox: bump state: %w", err)
	}
	return mb, nil, nil
}

func (h *handlerSet) updateMailbox(
	ctx context.Context,
	pid store.PrincipalID,
	id store.MailboxID,
	raw json.RawMessage,
) (*setError, error) {
	mb, err := loadMailboxForPrincipal(ctx, h.store.Meta(), pid, id)
	if err != nil {
		if errors.Is(err, errMailboxMissing) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	rights, err := rightsForPrincipal(ctx, h.store.Meta(), pid, mb)
	if err != nil {
		return nil, err
	}

	var in mailboxUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{
				Type: "invalidProperties", Description: err.Error(),
			}, nil
		}
	}

	if in.Name != nil {
		if !rights.MayRename {
			return &setError{
				Type:        "forbidden",
				Description: "principal lacks mayRename",
			}, nil
		}
		if strings.TrimSpace(*in.Name) == "" {
			return &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "name must not be empty",
			}, nil
		}
		if err := h.store.Meta().RenameMailbox(ctx, mb.ID, *in.Name); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return &setError{
					Type: "invalidProperties", Properties: []string{"name"},
					Description: "name conflicts with existing mailbox",
				}, nil
			}
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, fmt.Errorf("mailbox: rename: %w", err)
		}
	}

	if in.IsSubscribed != nil {
		if err := h.store.Meta().SetMailboxSubscribed(ctx, mb.ID, *in.IsSubscribed); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, fmt.Errorf("mailbox: subscribed: %w", err)
		}
	}

	// ParentID is json.RawMessage so we can distinguish three cases:
	//   absent (nil/empty): no change to parent
	//   JSON null: move to top-level (parentId == 0)
	//   JSON string: move under the named parent
	if len(in.ParentID) > 0 {
		rawParent := strings.TrimSpace(string(in.ParentID))
		newParentID := store.MailboxID(0)
		if rawParent != "null" {
			var idStr string
			if err := json.Unmarshal(in.ParentID, &idStr); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"parentId"},
					Description: "parentId must be a string or null",
				}, nil
			}
			targetID, ok := mailboxIDFromJMAP(idStr)
			if !ok {
				return &setError{
					Type: "invalidProperties", Properties: []string{"parentId"},
					Description: "parentId references an unknown mailbox",
				}, nil
			}
			parent, err := loadMailboxForPrincipal(ctx, h.store.Meta(), pid, targetID)
			if err != nil {
				if errors.Is(err, errMailboxMissing) {
					return &setError{
						Type: "invalidProperties", Properties: []string{"parentId"},
						Description: "parent mailbox does not exist",
					}, nil
				}
				return nil, fmt.Errorf("mailbox: load parent: %w", err)
			}
			parentRights, err := rightsForPrincipal(ctx, h.store.Meta(), pid, parent)
			if err != nil {
				return nil, err
			}
			if !parentRights.MayCreateChild {
				return &setError{
					Type:        "forbidden",
					Description: "principal lacks mayCreateChild on the target parent",
				}, nil
			}
			newParentID = targetID
		}
		if err := h.store.Meta().MoveMailbox(ctx, mb.ID, newParentID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, fmt.Errorf("mailbox: move: %w", err)
		}
	}

	if in.Role != nil {
		want := *in.Role
		current := roleFromAttributes(mb.Attributes)
		if want == "" {
			if current != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"role"},
					Description: "clearing role is not supported in v1",
				}, nil
			}
		} else {
			if current == nil || *current != want {
				return &setError{
					Type: "invalidProperties", Properties: []string{"role"},
					Description: "changing role is not supported in v1",
				}, nil
			}
		}
	}

	if in.SortOrder != nil {
		so := uint32(*in.SortOrder)
		if err := h.store.Meta().SetMailboxSortOrder(ctx, mb.ID, so); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, fmt.Errorf("mailbox: sort order: %w", err)
		}
	}

	if len(in.Color) > 0 {
		// JSON null clears; a string value sets after format validation.
		// JMAP property absence = no change; the json.RawMessage decoder
		// only populates this branch when the client sent the key.
		var v *string
		switch string(in.Color) {
		case "null":
			v = nil
		default:
			var s string
			if err := json.Unmarshal(in.Color, &s); err != nil {
				return &setError{
					Type: "invalidProperties", Properties: []string{"color"},
					Description: "color must be a string or null",
				}, nil
			}
			if !validJMAPColor(s) {
				return &setError{
					Type: "invalidProperties", Properties: []string{"color"},
					Description: "color must be the hex literal #RRGGBB",
				}, nil
			}
			v = &s
		}
		if err := h.store.Meta().SetMailboxColor(ctx, mb.ID, v); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			if errors.Is(err, store.ErrInvalidArgument) {
				return &setError{
					Type: "invalidProperties", Properties: []string{"color"},
					Description: err.Error(),
				}, nil
			}
			return nil, fmt.Errorf("mailbox: set color: %w", err)
		}
	}

	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindMailbox); err != nil {
		return nil, fmt.Errorf("mailbox: bump state: %w", err)
	}
	return nil, nil
}

// validJMAPColor reports whether s is the "#RRGGBB" hex literal accepted
// by the JMAP Mailbox.color extension. Mirror of the store-side helper;
// duplicated so the JMAP layer can reject malformed inputs with a
// "invalidProperties" set-error before reaching SQL.
func validJMAPColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func (h *handlerSet) destroyMailbox(
	ctx context.Context,
	pid store.PrincipalID,
	id store.MailboxID,
	onDestroyRem *bool,
) (*setError, error) {
	mb, err := loadMailboxForPrincipal(ctx, h.store.Meta(), pid, id)
	if err != nil {
		if errors.Is(err, errMailboxMissing) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	rights, err := rightsForPrincipal(ctx, h.store.Meta(), pid, mb)
	if err != nil {
		return nil, err
	}
	if !rights.MayDelete {
		return &setError{Type: "forbidden", Description: "principal lacks mayDelete"}, nil
	}

	// RFC 8621 §2.5: a mailbox with children MUST be refused unless the
	// client also destroys all children in the same /set call. We always
	// refuse here — the caller is responsible for ordering destroys so
	// that children are destroyed before parents. The error type is
	// "mailboxHasChild" per the RFC.
	allMailboxes, err := h.store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("mailbox: list for children check: %w", err)
	}
	for _, candidate := range allMailboxes {
		if candidate.ParentID == mb.ID {
			return &setError{
				Type:        "mailboxHasChild",
				Description: "mailbox has child mailboxes; destroy children first",
			}, nil
		}
	}

	removeEmails := onDestroyRem != nil && *onDestroyRem
	if !removeEmails {
		total, _, cerr := countMessages(ctx, h.store.Meta(), mb.ID)
		if cerr != nil {
			return nil, cerr
		}
		if total > 0 {
			return &setError{
				Type:        "mailboxHasEmail",
				Description: "mailbox is non-empty and onDestroyRemoveEmails is false",
			}, nil
		}
	}

	if err := h.store.Meta().DeleteMailbox(ctx, mb.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, fmt.Errorf("mailbox: delete: %w", err)
	}
	if _, err := h.store.Meta().IncrementJMAPState(ctx, pid, store.JMAPStateKindMailbox); err != nil {
		return nil, fmt.Errorf("mailbox: bump state: %w", err)
	}
	return nil, nil
}
