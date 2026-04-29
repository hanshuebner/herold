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

// Wave 2.9 sec audit #9: byte-oriented caps on Conversation.Name /
// .Topic. JMAP server-side limits are byte-counted (the spec allows
// servers to express maxSizeForeignKey-style caps in bytes; we follow
// the same convention here). Names land in headers and small UI
// chrome; topics land in the conversation banner.
const (
	maxConversationNameBytes  = 256
	maxConversationTopicBytes = 4096
)

// -- Conversation/get -------------------------------------------------

type convGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type convGetResponse struct {
	AccountID jmapID             `json:"accountId"`
	State     string             `json:"state"`
	List      []jmapConversation `json:"list"`
	NotFound  []jmapID           `json:"notFound"`
}

type convGetHandler struct{ h *handlerSet }

func (h *convGetHandler) Method() string { return "Conversation/get" }

func (h *convGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req convGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentConversationState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := convGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapConversation{},
		NotFound:  []jmapID{},
	}

	allowed, err := principalConversationIDs(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	var candidates []store.ConversationID
	if req.IDs == nil {
		// Every conversation the principal is a member of.
		for cid := range allowed {
			candidates = append(candidates, cid)
		}
	} else {
		for _, raw := range *req.IDs {
			id, ok := conversationIDFromJMAP(raw)
			if !ok {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			if _, member := allowed[id]; !member {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			candidates = append(candidates, id)
		}
	}

	for _, cid := range candidates {
		c, err := h.h.store.Meta().GetChatConversation(ctx, cid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, jmapIDFromConversation(cid))
				continue
			}
			return nil, serverFail(err)
		}
		members, err := h.h.store.Meta().ListChatMembershipsByConversation(ctx, c.ID)
		if err != nil {
			return nil, serverFail(err)
		}
		resp.List = append(resp.List, renderConversation(ctx, h.h.store.Meta(), c, members, pid))
	}
	return resp, nil
}

// renderConversation projects a store.ChatConversation + its
// memberships into the wire-form jmapConversation, filling in the
// requesting principal's myMembership row, unread count, per-member
// display names, and per-viewer DM name.
//
// DM name projection: the stored Name is set at create time from the
// creator's perspective. On the wire we override it by resolving the
// OTHER member's display name from self's point of view. The stored row
// is intentionally left unchanged; only the wire projection is per-viewer.
//
// Member displayName: populated from the principal's display name so the
// client can label senders without a separate Principal/get round trip.
func renderConversation(ctx context.Context, meta store.Metadata, c store.ChatConversation, members []store.ChatMembership, self store.PrincipalID) jmapConversation {
	// REQ-CHAT-31 / REQ-CHAT-32: DMs always advertise read receipts on,
	// regardless of the underlying column. Spaces project the column
	// value verbatim.
	receipts := c.ReadReceiptsEnabled
	if c.Kind == store.ChatConversationKindDM {
		receipts = true
	}
	out := jmapConversation{
		ID:                  jmapIDFromConversation(c.ID),
		Kind:                c.Kind,
		Name:                c.Name,
		Topic:               c.Topic,
		LastMessageAt:       rfc3339OrNilFromPtr(c.LastMessageAt),
		MessageCount:        c.MessageCount,
		IsArchived:          c.IsArchived,
		ReadReceiptsEnabled: receipts,
		RetentionSeconds:    int64PtrOrNil(c.RetentionSeconds),
		EditWindowSeconds:   int64PtrOrNil(c.EditWindowSeconds),
		Members:             make([]jmapConversationMember, 0, len(members)),
	}
	var unreadAnchor *store.ChatMessageID
	for _, m := range members {
		displayName := resolvePrincipalEmail(ctx, meta, m.PrincipalID)
		out.Members = append(out.Members, jmapConversationMember{
			PrincipalID: jmapIDFromPrincipal(m.PrincipalID),
			DisplayName: displayName,
			Role:        m.Role,
			JoinedAt:    m.JoinedAt.UTC().Format(rfc3339Layout),
			IsMuted:     m.IsMuted,
		})
		if m.PrincipalID == self {
			rendered := renderMembership(m)
			out.MyMembership = &rendered
			unreadAnchor = m.LastReadMessageID
		}
	}
	// For DMs, override the stored Name with the OTHER member's display
	// name from the requesting principal's perspective. The stored row
	// keeps the creator's-perspective name unchanged; only the wire
	// projection is per-viewer (re #47).
	if c.Kind == store.ChatConversationKindDM {
		otherPID := otherDMMember(members, self)
		if otherPID != 0 {
			out.Name = resolvePrincipalEmail(ctx, meta, otherPID)
		}
	}
	// Unread count approximation: the conversation's MessageCount minus
	// the offset of LastReadMessageID. Message ids are monotonic per
	// conversation; using the gap as a proxy keeps the JMAP get path a
	// single round trip. Precise counts come from a Message/query with
	// after = the last-read pointer.
	if c.MessageCount > 0 {
		if unreadAnchor == nil {
			out.UnreadCount = c.MessageCount
		} else if uint64(*unreadAnchor) < uint64(c.MessageCount) {
			gap := c.MessageCount - int(*unreadAnchor)
			if gap < 0 {
				gap = 0
			}
			out.UnreadCount = gap
		}
	}
	return out
}

// -- Conversation/changes ---------------------------------------------

type convChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type convChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type convChangesHandler struct{ h *handlerSet }

func (h *convChangesHandler) Method() string { return "Conversation/changes" }

func (h *convChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req convChangesRequest
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
	newState := stateFromCounter(st.Conversation)
	resp := convChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == st.Conversation {
		return resp, nil
	}
	if since > st.Conversation {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}
	created, updated, destroyed, ferr := walkChatChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindConversation, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromConversation(store.ConversationID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromConversation(store.ConversationID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromConversation(store.ConversationID(id)))
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

// walkChatChangeFeed reads the principal's change feed, classifying
// entries by kind. Mirrors the contacts package helper of the same
// shape; chat datatypes flow through additively per the entity-kind-
// agnostic dispatch pattern.
func walkChatChangeFeed(
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

// -- Conversation/set -------------------------------------------------

type convSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type convSetResponse struct {
	AccountID    jmapID                      `json:"accountId"`
	OldState     string                      `json:"oldState"`
	NewState     string                      `json:"newState"`
	Created      map[string]jmapConversation `json:"created"`
	Updated      map[jmapID]any              `json:"updated"`
	Destroyed    []jmapID                    `json:"destroyed"`
	NotCreated   map[string]setError         `json:"notCreated"`
	NotUpdated   map[jmapID]setError         `json:"notUpdated"`
	NotDestroyed map[jmapID]setError         `json:"notDestroyed"`
}

type convCreateInput struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Topic   string   `json:"topic"`
	Members []jmapID `json:"members"`
}

type convUpdateInput struct {
	Name                *string         `json:"name"`
	Topic               *string         `json:"topic"`
	IsArchived          *bool           `json:"isArchived"`
	ReadReceiptsEnabled *bool           `json:"readReceiptsEnabled"`
	RetentionSeconds    json.RawMessage `json:"retentionSeconds"`
	EditWindowSeconds   json.RawMessage `json:"editWindowSeconds"`
}

type convSetHandler struct{ h *handlerSet }

func (h *convSetHandler) Method() string { return "Conversation/set" }

func (h *convSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req convSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}

	state, err := currentConversationState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}
	resp := convSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapConversation{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.ConversationID, len(req.Create))

	for key, raw := range req.Create {
		var in convCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		c, members, serr, err := h.h.createConversation(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = c.ID
		resp.Created[key] = renderConversation(ctx, h.h.store.Meta(), c, members, pid)
	}

	for raw, payload := range req.Update {
		id, ok := resolveConversationID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateConversation(ctx, pid, id, payload)
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
		id, ok := resolveConversationID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyConversation(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentConversationState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveConversationID(raw jmapID, creationRefs map[string]store.ConversationID) (store.ConversationID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return conversationIDFromJMAP(raw)
}

func (h *handlerSet) createConversation(
	ctx context.Context,
	creator store.PrincipalID,
	in convCreateInput,
) (store.ChatConversation, []store.ChatMembership, *setError, error) {
	kind := in.Kind
	if kind != store.ChatConversationKindDM && kind != store.ChatConversationKindSpace {
		return store.ChatConversation{}, nil, &setError{
			Type: "invalidProperties", Properties: []string{"kind"},
			Description: "kind must be 'dm' or 'space'",
		}, nil
	}

	memberIDs := make([]store.PrincipalID, 0, len(in.Members))
	for _, raw := range in.Members {
		v, ok := principalIDFromJMAP(raw)
		if !ok {
			return store.ChatConversation{}, nil, &setError{
				Type: "invalidProperties", Properties: []string{"members"},
				Description: "member ids must be non-zero numeric strings",
			}, nil
		}
		memberIDs = append(memberIDs, v)
	}
	memberIDs = dedupePrincipals(memberIDs)

	switch kind {
	case store.ChatConversationKindDM:
		// DM: members must be exactly one other principal (the creator
		// is auto-added). A self-DM is rejected.
		if len(memberIDs) != 1 {
			return store.ChatConversation{}, nil, &setError{
				Type: "invalidProperties", Properties: []string{"members"},
				Description: "DM requires exactly one other member",
			}, nil
		}
		if memberIDs[0] == creator {
			return store.ChatConversation{}, nil, &setError{
				Type: "invalidProperties", Properties: []string{"members"},
				Description: "cannot create a DM with yourself",
			}, nil
		}
	case store.ChatConversationKindSpace:
		if h.limits.MaxMembersPerSpace > 0 && 1+len(memberIDs) > h.limits.MaxMembersPerSpace {
			return store.ChatConversation{}, nil, &setError{
				Type: "overLimit", Properties: []string{"members"},
				Description: "Space exceeds maxMembersPerSpace",
			}, nil
		}
	}

	name := strings.TrimSpace(in.Name)
	if kind == store.ChatConversationKindDM && name == "" {
		// Auto-name from the other member's display name (REQ-CHAT-02
		// allows omission for DMs).
		name = resolvePrincipalEmail(ctx, h.store.Meta(), memberIDs[0])
	}
	// JMAP server-side caps are byte-oriented per the spec (clients
	// using non-ASCII names see fewer "characters" but the wire-form
	// limit is bytes). Wave 2.9 sec audit #9.
	if len(name) > maxConversationNameBytes {
		return store.ChatConversation{}, nil, &setError{
			Type: "invalidProperties", Properties: []string{"name"},
			Description: "name exceeds 256 bytes",
		}, nil
	}
	topic := strings.TrimSpace(in.Topic)
	if len(topic) > maxConversationTopicBytes {
		return store.ChatConversation{}, nil, &setError{
			Type: "invalidProperties", Properties: []string{"topic"},
			Description: "topic exceeds 4096 bytes",
		}, nil
	}

	now := h.clk.Now()
	cid, err := h.store.Meta().InsertChatConversation(ctx, store.ChatConversation{
		Kind:                 kind,
		Name:                 name,
		Topic:                topic,
		CreatedByPrincipalID: creator,
		CreatedAt:            now,
		UpdatedAt:            now,
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			return store.ChatConversation{}, nil, &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
		return store.ChatConversation{}, nil, nil, err
	}

	// Insert the creator's membership as owner first, then every
	// supplied member as a regular member. Failures inside the loop
	// surface as serverFail per the contacts/calendars convention; the
	// store enforces the unique (cid, pid) constraint.
	creatorRole := store.ChatRoleOwner
	if _, err := h.store.Meta().InsertChatMembership(ctx, store.ChatMembership{
		ConversationID:       cid,
		PrincipalID:          creator,
		Role:                 creatorRole,
		JoinedAt:             now,
		NotificationsSetting: store.ChatNotificationsAll,
	}); err != nil {
		return store.ChatConversation{}, nil, nil, err
	}
	for _, mid := range memberIDs {
		role := store.ChatRoleMember
		if _, err := h.store.Meta().InsertChatMembership(ctx, store.ChatMembership{
			ConversationID:       cid,
			PrincipalID:          mid,
			Role:                 role,
			JoinedAt:             now,
			NotificationsSetting: store.ChatNotificationsAll,
		}); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			return store.ChatConversation{}, nil, nil, err
		}
	}

	c, err := h.store.Meta().GetChatConversation(ctx, cid)
	if err != nil {
		return store.ChatConversation{}, nil, nil, err
	}
	members, err := h.store.Meta().ListChatMembershipsByConversation(ctx, cid)
	if err != nil {
		return store.ChatConversation{}, nil, nil, err
	}
	return c, members, nil, nil
}

func (h *handlerSet) updateConversation(
	ctx context.Context,
	pid store.PrincipalID,
	id store.ConversationID,
	raw json.RawMessage,
) (*setError, error) {
	c, err := h.store.Meta().GetChatConversation(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	members, err := h.store.Meta().ListChatMembershipsByConversation(ctx, id)
	if err != nil {
		return nil, err
	}
	if !principalIsMember(members, pid) {
		return &setError{Type: "notFound"}, nil
	}
	if !canManageConversation(members, pid) {
		return &setError{Type: "forbidden", Description: "only an admin or owner may update a conversation"}, nil
	}

	var in convUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}
	if in.Name != nil {
		newName := strings.TrimSpace(*in.Name)
		if len(newName) > maxConversationNameBytes {
			return &setError{
				Type: "invalidProperties", Properties: []string{"name"},
				Description: "name exceeds 256 bytes",
			}, nil
		}
		c.Name = newName
	}
	if in.Topic != nil {
		newTopic := strings.TrimSpace(*in.Topic)
		if len(newTopic) > maxConversationTopicBytes {
			return &setError{
				Type: "invalidProperties", Properties: []string{"topic"},
				Description: "topic exceeds 4096 bytes",
			}, nil
		}
		c.Topic = newTopic
	}
	if in.IsArchived != nil {
		c.IsArchived = *in.IsArchived
	}
	// REQ-CHAT-32: only Spaces honour readReceiptsEnabled. DMs always
	// have receipts on (REQ-CHAT-31); rejecting an explicit false on a
	// DM is the cleanest signal that the property is not configurable
	// there. true on a DM is a no-op (already true on the wire) and is
	// accepted silently for forward-compatible client convenience.
	if in.ReadReceiptsEnabled != nil {
		if c.Kind == store.ChatConversationKindDM {
			if !*in.ReadReceiptsEnabled {
				return &setError{
					Type: "invalidProperties", Properties: []string{"readReceiptsEnabled"},
					Description: "DMs always have read receipts on; cannot disable for a DM",
				}, nil
			}
		} else {
			c.ReadReceiptsEnabled = *in.ReadReceiptsEnabled
		}
	}
	// REQ-CHAT-92: retentionSeconds is a nullable int64 — JSON null
	// clears the override (use account default), 0 means "never expire",
	// positive values are seconds since CreatedAt at which the sweeper
	// hard-deletes a message.
	if len(in.RetentionSeconds) > 0 {
		serr := applyNullableInt64(in.RetentionSeconds, "retentionSeconds", &c.RetentionSeconds)
		if serr != nil {
			return serr, nil
		}
	}
	// REQ-CHAT-20: editWindowSeconds — same null/0/positive semantics.
	if len(in.EditWindowSeconds) > 0 {
		serr := applyNullableInt64(in.EditWindowSeconds, "editWindowSeconds", &c.EditWindowSeconds)
		if serr != nil {
			return serr, nil
		}
	}
	c.UpdatedAt = h.clk.Now()
	if err := h.store.Meta().UpdateChatConversation(ctx, c); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	return nil, nil
}

func (h *handlerSet) destroyConversation(
	ctx context.Context,
	pid store.PrincipalID,
	id store.ConversationID,
) (*setError, error) {
	c, err := h.store.Meta().GetChatConversation(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	members, err := h.store.Meta().ListChatMembershipsByConversation(ctx, id)
	if err != nil {
		return nil, err
	}
	if !principalIsMember(members, pid) {
		return &setError{Type: "notFound"}, nil
	}
	if !canDestroyConversation(c, pid) {
		return &setError{Type: "forbidden", Description: "only the conversation creator may destroy it"}, nil
	}
	if err := h.store.Meta().DeleteChatConversation(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	return nil, nil
}

// -- Conversation/query -----------------------------------------------

type convFilterCondition struct {
	Kind       *string `json:"kind"`
	IsArchived *bool   `json:"isArchived"`
	Text       *string `json:"text"`
	HasUnread  *bool   `json:"hasUnread"`
}

type convQueryRequest struct {
	AccountID      jmapID               `json:"accountId"`
	Filter         *convFilterCondition `json:"filter"`
	Sort           []comparator         `json:"sort"`
	Position       int                  `json:"position"`
	Anchor         *jmapID              `json:"anchor"`
	AnchorOffset   int                  `json:"anchorOffset"`
	Limit          *int                 `json:"limit"`
	CalculateTotal bool                 `json:"calculateTotal"`
}

type convQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type convQueryHandler struct{ h *handlerSet }

func (h *convQueryHandler) Method() string { return "Conversation/query" }

func (h *convQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req convQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentConversationState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	allowed, err := principalConversationIDs(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	includeArchived := false
	if req.Filter != nil && req.Filter.IsArchived != nil && *req.Filter.IsArchived {
		// Caller asked for archived rows specifically.
		includeArchived = true
	}
	filter := store.ChatConversationFilter{IncludeArchived: includeArchived}
	if req.Filter != nil && req.Filter.Kind != nil {
		k := *req.Filter.Kind
		if k != store.ChatConversationKindDM && k != store.ChatConversationKindSpace {
			return nil, protojmap.NewMethodError("invalidArguments", "kind must be 'dm' or 'space'")
		}
		filter.Kind = &k
	}

	all, err := h.h.store.Meta().ListChatConversations(ctx, filter)
	if err != nil {
		return nil, serverFail(err)
	}

	matched := make([]store.ChatConversation, 0, len(all))
	for _, c := range all {
		if _, ok := allowed[c.ID]; !ok {
			continue
		}
		if req.Filter != nil {
			if req.Filter.IsArchived != nil && c.IsArchived != *req.Filter.IsArchived {
				continue
			}
			if req.Filter.Text != nil && *req.Filter.Text != "" {
				needle := strings.ToLower(*req.Filter.Text)
				hay := strings.ToLower(c.Name + " " + c.Topic)
				if !strings.Contains(hay, needle) {
					continue
				}
			}
			if req.Filter.HasUnread != nil {
				members, mErr := h.h.store.Meta().ListChatMembershipsByConversation(ctx, c.ID)
				if mErr != nil {
					return nil, serverFail(mErr)
				}
				my, ok := findMembership(members, pid)
				has := false
				if ok && c.MessageCount > 0 {
					if my.LastReadMessageID == nil {
						has = true
					} else if uint64(*my.LastReadMessageID) < uint64(c.MessageCount) {
						has = true
					}
				}
				if has != *req.Filter.HasUnread {
					continue
				}
			}
		}
		matched = append(matched, c)
	}
	sortConversations(matched, req.Sort)

	resp := convQueryResponse{
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
		resp.IDs = append(resp.IDs, jmapIDFromConversation(c.ID))
	}
	return resp, nil
}

func sortConversations(xs []store.ChatConversation, comps []comparator) {
	if len(comps) == 0 {
		// Default: lastMessageAt descending.
		desc := false
		comps = []comparator{{Property: "lastMessageAt", IsAscending: &desc}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareConversation(xs[i], xs[j], c.Property)
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

func compareConversation(a, b store.ChatConversation, property string) int {
	switch property {
	case "lastMessageAt":
		ai := timeOrZero(a.LastMessageAt)
		bi := timeOrZero(b.LastMessageAt)
		switch {
		case ai.Before(bi):
			return -1
		case ai.After(bi):
			return 1
		}
		return 0
	case "name":
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	case "messageCount":
		switch {
		case a.MessageCount < b.MessageCount:
			return -1
		case a.MessageCount > b.MessageCount:
			return 1
		}
		return 0
	}
	return 0
}

// timeOrZero dereferences an optional time.Time, returning the zero
// value when nil. Used by the conversation comparator on
// LastMessageAt.
func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// -- Conversation/queryChanges ---------------------------------------

type convQueryChangesHandler struct{ h *handlerSet }

func (convQueryChangesHandler) Method() string { return "Conversation/queryChanges" }

func (convQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Conversation/queryChanges is unsupported; clients re-issue Conversation/query")
}
