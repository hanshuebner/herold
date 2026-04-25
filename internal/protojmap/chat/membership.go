package chat

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// -- Membership/get ---------------------------------------------------

type memGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type memGetResponse struct {
	AccountID jmapID           `json:"accountId"`
	State     string           `json:"state"`
	List      []jmapMembership `json:"list"`
	NotFound  []jmapID         `json:"notFound"`
}

type memGetHandler struct{ h *handlerSet }

func (h *memGetHandler) Method() string { return "Membership/get" }

func (h *memGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req memGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentMembershipState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := memGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapMembership{},
		NotFound:  []jmapID{},
	}

	// We resolve by id by scanning every membership in conversations
	// the caller participates in; the store API exposes lookups by
	// (convID, pid) but not by membership ID alone, so the JMAP layer
	// performs the join. The set of conversations is bounded by the
	// caller's own membership list (REQ-CHAT-101), so the scan is
	// scoped tightly.
	mine, err := h.h.store.Meta().ListChatMembershipsByPrincipal(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	convs := make(map[store.ConversationID]struct{}, len(mine))
	for _, m := range mine {
		convs[m.ConversationID] = struct{}{}
	}
	byID := map[store.MembershipID]store.ChatMembership{}
	for cid := range convs {
		rows, err := h.h.store.Meta().ListChatMembershipsByConversation(ctx, cid)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, m := range rows {
			byID[m.ID] = m
		}
	}
	// Resolve the per-conversation read-receipt policy once so the
	// REQ-CHAT-32 suppression below does not re-fetch on every row.
	convCache := map[store.ConversationID]store.ChatConversation{}
	getConv := func(cid store.ConversationID) (store.ChatConversation, error) {
		if c, ok := convCache[cid]; ok {
			return c, nil
		}
		c, err := h.h.store.Meta().GetChatConversation(ctx, cid)
		if err != nil {
			return store.ChatConversation{}, err
		}
		convCache[cid] = c
		return c, nil
	}

	if req.IDs == nil {
		// Default scope: only the caller's own membership rows. The
		// caller always sees their own lastReadMessageId regardless of
		// the Space's readReceiptsEnabled flag (REQ-CHAT-32).
		for _, m := range mine {
			resp.List = append(resp.List, renderMembership(m))
		}
		return resp, nil
	}
	for _, raw := range *req.IDs {
		id, ok := membershipIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		m, ok := byID[id]
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		// REQ-CHAT-32: when a Space has readReceiptsEnabled=false,
		// suppress lastReadMessageId for OTHER members. The requester's
		// own row is always rendered with the pointer intact.
		conv, err := getConv(m.ConversationID)
		if err != nil {
			return nil, serverFail(err)
		}
		suppress := conv.Kind == store.ChatConversationKindSpace &&
			!conv.ReadReceiptsEnabled && m.PrincipalID != pid
		if suppress {
			resp.List = append(resp.List, renderMembershipMasked(m))
		} else {
			resp.List = append(resp.List, renderMembership(m))
		}
	}
	return resp, nil
}

// renderMembership projects a store.ChatMembership into the wire form.
func renderMembership(m store.ChatMembership) jmapMembership {
	out := jmapMembership{
		ID:                   jmapIDFromMembership(m.ID),
		ConversationID:       jmapIDFromConversation(m.ConversationID),
		PrincipalID:          jmapIDFromPrincipal(m.PrincipalID),
		Role:                 m.Role,
		JoinedAt:             m.JoinedAt.UTC().Format(rfc3339Layout),
		IsMuted:              m.IsMuted,
		MuteUntil:            rfc3339OrNilFromPtr(m.MuteUntil),
		NotificationsSetting: m.NotificationsSetting,
	}
	if m.LastReadMessageID == nil {
		out.LastReadMessageID = ""
	} else {
		out.LastReadMessageID = jmapIDFromMessage(*m.LastReadMessageID)
	}
	return out
}

// renderMembershipMasked is renderMembership with lastReadMessageId
// elided. Used by Membership/get when REQ-CHAT-32 mandates that the
// caller not see another member's read pointer (Space with
// readReceiptsEnabled=false). The server still tracks readThrough
// internally for unread-count maths; only the wire projection is
// masked.
func renderMembershipMasked(m store.ChatMembership) jmapMembership {
	out := renderMembership(m)
	out.LastReadMessageID = ""
	return out
}

// -- Membership/changes -----------------------------------------------

type memChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type memChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type memChangesHandler struct{ h *handlerSet }

func (h *memChangesHandler) Method() string { return "Membership/changes" }

func (h *memChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req memChangesRequest
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
	newState := stateFromCounter(st.Membership)
	resp := memChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.Membership {
		return resp, nil
	}
	if since > st.Membership {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}
	created, updated, destroyed, ferr := walkChatChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindMembership, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromMembership(store.MembershipID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromMembership(store.MembershipID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromMembership(store.MembershipID(id)))
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

// -- Membership/set ---------------------------------------------------

type memSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type memSetResponse struct {
	AccountID    jmapID                    `json:"accountId"`
	OldState     string                    `json:"oldState"`
	NewState     string                    `json:"newState"`
	Created      map[string]jmapMembership `json:"created"`
	Updated      map[jmapID]any            `json:"updated"`
	Destroyed    []jmapID                  `json:"destroyed"`
	NotCreated   map[string]setError       `json:"notCreated"`
	NotUpdated   map[jmapID]setError       `json:"notUpdated"`
	NotDestroyed map[jmapID]setError       `json:"notDestroyed"`
}

type memCreateInput struct {
	ConversationID jmapID `json:"conversationId"`
	PrincipalID    jmapID `json:"principalId"`
	Role           string `json:"role"`
}

type memUpdateInput struct {
	Role                 *string         `json:"role"`
	LastReadMessageID    *jmapID         `json:"lastReadMessageId"`
	IsMuted              *bool           `json:"isMuted"`
	MuteUntil            json.RawMessage `json:"muteUntil"`
	NotificationsSetting *string         `json:"notificationsSetting"`
}

type memSetHandler struct{ h *handlerSet }

func (h *memSetHandler) Method() string { return "Membership/set" }

func (h *memSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req memSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentMembershipState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}
	resp := memSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapMembership{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.MembershipID, len(req.Create))

	for key, raw := range req.Create {
		var in memCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		m, serr, err := h.h.createMembership(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = m.ID
		resp.Created[key] = renderMembership(m)
	}

	for raw, payload := range req.Update {
		id, ok := resolveMembershipID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateMembership(ctx, pid, id, payload)
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
		id, ok := resolveMembershipID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyMembership(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentMembershipState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveMembershipID(raw jmapID, creationRefs map[string]store.MembershipID) (store.MembershipID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return membershipIDFromJMAP(raw)
}

// lookupMembershipByID fetches a membership row by its id by walking
// the caller's accessible conversations. The store does not expose a
// direct GetChatMembershipByID method; instead we look up via
// (conversationID, principalID) once we know the conversation. The
// scan is bounded by the caller's own conversation list and is small
// in practice (single-digit count for typical users).
func (h *handlerSet) lookupMembershipByID(ctx context.Context, caller store.PrincipalID, id store.MembershipID) (store.ChatMembership, bool, error) {
	mine, err := h.store.Meta().ListChatMembershipsByPrincipal(ctx, caller)
	if err != nil {
		return store.ChatMembership{}, false, err
	}
	convs := map[store.ConversationID]struct{}{}
	for _, m := range mine {
		convs[m.ConversationID] = struct{}{}
		if m.ID == id {
			return m, true, nil
		}
	}
	for cid := range convs {
		rows, err := h.store.Meta().ListChatMembershipsByConversation(ctx, cid)
		if err != nil {
			return store.ChatMembership{}, false, err
		}
		for _, m := range rows {
			if m.ID == id {
				return m, true, nil
			}
		}
	}
	return store.ChatMembership{}, false, nil
}

func (h *handlerSet) createMembership(
	ctx context.Context,
	caller store.PrincipalID,
	in memCreateInput,
) (store.ChatMembership, *setError, error) {
	cid, ok := conversationIDFromJMAP(in.ConversationID)
	if !ok {
		return store.ChatMembership{}, &setError{
			Type: "invalidProperties", Properties: []string{"conversationId"},
			Description: "conversationId is required",
		}, nil
	}
	target, ok := principalIDFromJMAP(in.PrincipalID)
	if !ok {
		return store.ChatMembership{}, &setError{
			Type: "invalidProperties", Properties: []string{"principalId"},
			Description: "principalId is required",
		}, nil
	}

	conv, err := h.store.Meta().GetChatConversation(ctx, cid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ChatMembership{}, &setError{Type: "notFound"}, nil
		}
		return store.ChatMembership{}, nil, err
	}
	if conv.Kind == store.ChatConversationKindDM {
		return store.ChatMembership{}, &setError{
			Type: "forbidden", Description: "DM membership is fixed at creation time",
		}, nil
	}
	members, err := h.store.Meta().ListChatMembershipsByConversation(ctx, cid)
	if err != nil {
		return store.ChatMembership{}, nil, err
	}
	if !canManageConversation(members, caller) {
		return store.ChatMembership{}, &setError{
			Type: "forbidden", Description: "only an admin or owner may add a member to a Space",
		}, nil
	}

	role := in.Role
	switch role {
	case "":
		role = store.ChatRoleMember
	case store.ChatRoleMember, store.ChatRoleAdmin:
		// ok
	case store.ChatRoleOwner:
		return store.ChatMembership{}, &setError{
			Type: "invalidProperties", Properties: []string{"role"},
			Description: "owner role is reserved for the conversation creator",
		}, nil
	default:
		return store.ChatMembership{}, &setError{
			Type: "invalidProperties", Properties: []string{"role"},
			Description: "role must be one of member / admin",
		}, nil
	}

	row := store.ChatMembership{
		ConversationID:       cid,
		PrincipalID:          target,
		Role:                 role,
		JoinedAt:             h.clk.Now(),
		NotificationsSetting: store.ChatNotificationsAll,
	}
	mid, err := h.store.Meta().InsertChatMembership(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.ChatMembership{}, &setError{
				Type: "alreadyExists", Description: "principal is already a member",
			}, nil
		}
		return store.ChatMembership{}, nil, err
	}
	row.ID = mid
	// Reload via the store's GetChatMembership (keyed by convID, pid)
	// so the returned row carries store-resolved timestamps.
	loaded, err := h.store.Meta().GetChatMembership(ctx, cid, target)
	if err == nil {
		return loaded, nil, nil
	}
	return row, nil, nil
}

func (h *handlerSet) updateMembership(
	ctx context.Context,
	caller store.PrincipalID,
	id store.MembershipID,
	raw json.RawMessage,
) (*setError, error) {
	m, ok, err := h.lookupMembershipByID(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &setError{Type: "notFound"}, nil
	}
	conv, err := h.store.Meta().ListChatMembershipsByConversation(ctx, m.ConversationID)
	if err != nil {
		return nil, err
	}
	if !principalIsMember(conv, caller) {
		return &setError{Type: "notFound"}, nil
	}
	isSelf := m.PrincipalID == caller
	isAdmin := canManageConversation(conv, caller)
	if !isSelf && !isAdmin {
		return &setError{Type: "forbidden", Description: "only an admin or owner may update another member"}, nil
	}

	var in memUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}

	if in.Role != nil {
		if !isAdmin {
			return &setError{Type: "forbidden", Properties: []string{"role"},
				Description: "only an admin or owner may change a member's role"}, nil
		}
		role := *in.Role
		if role != store.ChatRoleMember && role != store.ChatRoleAdmin && role != store.ChatRoleOwner {
			return &setError{Type: "invalidProperties", Properties: []string{"role"},
				Description: "role must be one of owner / admin / member"}, nil
		}
		m.Role = role
	}
	if in.LastReadMessageID != nil {
		if !isSelf {
			return &setError{Type: "forbidden", Properties: []string{"lastReadMessageId"},
				Description: "only the member themselves may set lastReadMessageId"}, nil
		}
		if *in.LastReadMessageID == "" {
			m.LastReadMessageID = nil
		} else {
			id, ok := messageIDFromJMAP(*in.LastReadMessageID)
			if !ok {
				return &setError{Type: "invalidProperties", Properties: []string{"lastReadMessageId"},
					Description: "lastReadMessageId must be a numeric id"}, nil
			}
			lid := id
			m.LastReadMessageID = &lid
		}
	}
	if in.IsMuted != nil {
		if !isSelf {
			return &setError{Type: "forbidden", Properties: []string{"isMuted"},
				Description: "only the member themselves may toggle mute"}, nil
		}
		m.IsMuted = *in.IsMuted
	}
	if len(in.MuteUntil) > 0 {
		if !isSelf {
			return &setError{Type: "forbidden", Properties: []string{"muteUntil"},
				Description: "only the member themselves may set muteUntil"}, nil
		}
		if string(in.MuteUntil) == "null" {
			m.MuteUntil = nil
		} else {
			var ts string
			if err := json.Unmarshal(in.MuteUntil, &ts); err != nil {
				return &setError{Type: "invalidProperties", Properties: []string{"muteUntil"},
					Description: "muteUntil must be an RFC 3339 string or null"}, nil
			}
			parsed, err := parseRFC3339(ts)
			if err != nil {
				return &setError{Type: "invalidProperties", Properties: []string{"muteUntil"},
					Description: err.Error()}, nil
			}
			t := parsed
			m.MuteUntil = &t
		}
	}
	if in.NotificationsSetting != nil {
		if !isSelf {
			return &setError{Type: "forbidden", Properties: []string{"notificationsSetting"},
				Description: "only the member themselves may set notificationsSetting"}, nil
		}
		v := *in.NotificationsSetting
		if v != store.ChatNotificationsAll && v != store.ChatNotificationsMentions && v != store.ChatNotificationsNone {
			return &setError{Type: "invalidProperties", Properties: []string{"notificationsSetting"},
				Description: "notificationsSetting must be one of all / mentions / none"}, nil
		}
		m.NotificationsSetting = v
	}

	if err := h.store.Meta().UpdateChatMembership(ctx, m); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	return nil, nil
}

func (h *handlerSet) destroyMembership(
	ctx context.Context,
	caller store.PrincipalID,
	id store.MembershipID,
) (*setError, error) {
	m, ok, err := h.lookupMembershipByID(ctx, caller, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &setError{Type: "notFound"}, nil
	}
	conv, err := h.store.Meta().GetChatConversation(ctx, m.ConversationID)
	if err != nil {
		return nil, err
	}
	if conv.Kind == store.ChatConversationKindDM {
		return &setError{Type: "forbidden", Description: "you cannot leave a DM in v1; use Block instead"}, nil
	}
	convMembers, err := h.store.Meta().ListChatMembershipsByConversation(ctx, m.ConversationID)
	if err != nil {
		return nil, err
	}
	if !principalIsMember(convMembers, caller) {
		return &setError{Type: "notFound"}, nil
	}
	if m.PrincipalID != caller && !canManageConversation(convMembers, caller) {
		return &setError{Type: "forbidden", Description: "only an admin or owner may remove another member"}, nil
	}
	if err := h.store.Meta().DeleteChatMembership(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	return nil, nil
}

// -- Membership/query -------------------------------------------------

type memFilterCondition struct {
	ConversationID *jmapID `json:"conversationId"`
	PrincipalID    *jmapID `json:"principalId"`
}

type memQueryRequest struct {
	AccountID      jmapID              `json:"accountId"`
	Filter         *memFilterCondition `json:"filter"`
	Sort           []comparator        `json:"sort"`
	Position       int                 `json:"position"`
	Anchor         *jmapID             `json:"anchor"`
	AnchorOffset   int                 `json:"anchorOffset"`
	Limit          *int                `json:"limit"`
	CalculateTotal bool                `json:"calculateTotal"`
}

type memQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type memQueryHandler struct{ h *handlerSet }

func (h *memQueryHandler) Method() string { return "Membership/query" }

func (h *memQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req memQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentMembershipState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	mine, err := h.h.store.Meta().ListChatMembershipsByPrincipal(ctx, pid)
	if err != nil {
		return nil, serverFail(err)
	}
	convIDs := make(map[store.ConversationID]struct{}, len(mine))
	for _, m := range mine {
		convIDs[m.ConversationID] = struct{}{}
	}

	var filterConvID *store.ConversationID
	var filterPID *store.PrincipalID
	if req.Filter != nil {
		if req.Filter.ConversationID != nil {
			id, ok := conversationIDFromJMAP(*req.Filter.ConversationID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments", "conversationId is malformed")
			}
			if _, allowed := convIDs[id]; !allowed {
				return nil, protojmap.NewMethodError("notFound", "conversation is not accessible")
			}
			cid := id
			filterConvID = &cid
		}
		if req.Filter.PrincipalID != nil {
			v, ok := principalIDFromJMAP(*req.Filter.PrincipalID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments", "principalId is malformed")
			}
			t := v
			filterPID = &t
		}
	}

	matched := make([]store.ChatMembership, 0)
	if filterConvID != nil {
		rows, err := h.h.store.Meta().ListChatMembershipsByConversation(ctx, *filterConvID)
		if err != nil {
			return nil, serverFail(err)
		}
		for _, m := range rows {
			if filterPID != nil && m.PrincipalID != *filterPID {
				continue
			}
			matched = append(matched, m)
		}
	} else {
		for cid := range convIDs {
			rows, err := h.h.store.Meta().ListChatMembershipsByConversation(ctx, cid)
			if err != nil {
				return nil, serverFail(err)
			}
			for _, m := range rows {
				if filterPID != nil && m.PrincipalID != *filterPID {
					continue
				}
				matched = append(matched, m)
			}
		}
	}

	sort.SliceStable(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })

	resp := memQueryResponse{
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
	for _, m := range matched[start:end] {
		resp.IDs = append(resp.IDs, jmapIDFromMembership(m.ID))
	}
	return resp, nil
}

// -- Membership/queryChanges ------------------------------------------

type memQueryChangesHandler struct{ h *handlerSet }

func (memQueryChangesHandler) Method() string { return "Membership/queryChanges" }

func (memQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Membership/queryChanges is unsupported; clients re-issue Membership/query")
}

// -- Membership/setLastRead -------------------------------------------

// memSetLastReadHandler implements the herold-namespaced custom method
// that atomically advances the caller's last-read pointer in a single
// conversation. Wraps store.Metadata.SetLastRead and emits the matching
// state-change row so other clients see the read receipt (REQ-CHAT-30).
type memSetLastReadRequest struct {
	AccountID      jmapID `json:"accountId"`
	ConversationID jmapID `json:"conversationId"`
	MessageID      jmapID `json:"messageId"`
}

type memSetLastReadResponse struct {
	AccountID      jmapID `json:"accountId"`
	ConversationID jmapID `json:"conversationId"`
	State          string `json:"state"`
}

type memSetLastReadHandler struct{ h *handlerSet }

func (h *memSetLastReadHandler) Method() string { return "Membership/setLastRead" }

func (h *memSetLastReadHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req memSetLastReadRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	cid, ok := conversationIDFromJMAP(req.ConversationID)
	if !ok {
		return nil, protojmap.NewMethodError("invalidArguments", "conversationId is required")
	}
	mid, ok := messageIDFromJMAP(req.MessageID)
	if !ok {
		return nil, protojmap.NewMethodError("invalidArguments", "messageId is required")
	}
	// Membership existence check before the SetLastRead call so the
	// caller sees a clear 404 rather than a backend error.
	if _, err := h.h.store.Meta().GetChatMembership(ctx, cid, pid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, protojmap.NewMethodError("notFound", "you are not a member of this conversation")
		}
		return nil, serverFail(err)
	}
	if err := h.h.store.Meta().SetLastRead(ctx, pid, cid, mid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, protojmap.NewMethodError("notFound", "membership has been removed")
		}
		return nil, serverFail(err)
	}
	state, err := currentMembershipState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	return memSetLastReadResponse{
		AccountID:      string(protojmap.AccountIDForPrincipal(pid)),
		ConversationID: req.ConversationID,
		State:          state,
	}, nil
}

// quietTime is a small helper to silence the time import unused warning
// in tests when this file is compiled in isolation.
var _ = time.Now
