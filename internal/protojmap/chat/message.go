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

// -- Message/get ------------------------------------------------------

type msgGetRequest struct {
	AccountID  jmapID    `json:"accountId"`
	IDs        *[]jmapID `json:"ids"`
	Properties *[]string `json:"properties"`
}

type msgGetResponse struct {
	AccountID jmapID        `json:"accountId"`
	State     string        `json:"state"`
	List      []jmapMessage `json:"list"`
	NotFound  []jmapID      `json:"notFound"`
}

type msgGetHandler struct{ h *handlerSet }

func (h *msgGetHandler) Method() string { return "Message/get" }

func (h *msgGetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req msgGetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentMessageState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp := msgGetResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		State:     state,
		List:      []jmapMessage{},
		NotFound:  []jmapID{},
	}
	if req.IDs == nil {
		return nil, protojmap.NewMethodError("invalidArguments",
			"Message/get requires an explicit ids list (server-side enumeration is not supported)")
	}
	allowed, err := principalConversationIDs(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	for _, raw := range *req.IDs {
		id, ok := messageIDFromJMAP(raw)
		if !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		m, err := h.h.store.Meta().GetChatMessage(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotFound = append(resp.NotFound, raw)
				continue
			}
			return nil, serverFail(err)
		}
		if _, ok := allowed[m.ConversationID]; !ok {
			resp.NotFound = append(resp.NotFound, raw)
			continue
		}
		resp.List = append(resp.List, renderMessage(m))
	}
	return resp, nil
}

// renderMessage projects a store.ChatMessage into the wire form. Soft-
// deleted messages return with deletedAt set + the body cleared per
// REQ-CHAT-21 tombstone semantics.
func renderMessage(m store.ChatMessage) jmapMessage {
	out := jmapMessage{
		ID:             jmapIDFromMessage(m.ID),
		ConversationID: jmapIDFromConversation(m.ConversationID),
		IsSystem:       m.IsSystem,
		CreatedAt:      m.CreatedAt.UTC().Format(rfc3339Layout),
		EditedAt:       rfc3339OrNilFromPtr(m.EditedAt),
		DeletedAt:      rfc3339OrNilFromPtr(m.DeletedAt),
		Reactions:      map[string][]string{},
		Attachments:    []jmapAttachment{},
	}
	if m.SenderPrincipalID != nil {
		out.SenderPrincipalID = jmapIDFromPrincipal(*m.SenderPrincipalID)
	}
	if m.ReplyToMessageID != nil {
		out.ReplyToMessageID = jmapIDFromMessage(*m.ReplyToMessageID)
	}
	if m.DeletedAt == nil {
		out.Body = jmapMessageBody{
			Text:   m.BodyText,
			HTML:   m.BodyHTML,
			Format: m.BodyFormat,
		}
	} else {
		out.Body = jmapMessageBody{Format: m.BodyFormat}
	}
	// Decode reactions JSON. The store keeps it canonical (sorted
	// keys + sorted reactor lists); decoding a typed map is enough.
	if len(m.ReactionsJSON) > 0 {
		var raw map[string][]int64
		if err := json.Unmarshal(m.ReactionsJSON, &raw); err == nil {
			for emoji, reactors := range raw {
				ids := make([]string, 0, len(reactors))
				for _, p := range reactors {
					ids = append(ids, jmapIDFromPrincipal(store.PrincipalID(p)))
				}
				out.Reactions[emoji] = ids
			}
		}
	}
	// Decode attachments JSON. Each entry is {blob_hash, content_type,
	// filename, size}.
	if len(m.AttachmentsJSON) > 0 {
		var raw []map[string]any
		if err := json.Unmarshal(m.AttachmentsJSON, &raw); err == nil {
			for _, a := range raw {
				att := jmapAttachment{}
				if v, ok := a["blob_hash"].(string); ok {
					att.BlobID = v
				}
				if v, ok := a["content_type"].(string); ok {
					att.ContentType = v
				}
				if v, ok := a["filename"].(string); ok {
					att.Filename = v
				}
				if v, ok := a["size"].(float64); ok {
					att.Size = int64(v)
				}
				out.Attachments = append(out.Attachments, att)
			}
		}
	}
	if len(m.MetadataJSON) > 0 {
		out.Metadata = json.RawMessage(m.MetadataJSON)
	}
	return out
}

// -- Message/changes --------------------------------------------------

type msgChangesRequest struct {
	AccountID  jmapID `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type msgChangesResponse struct {
	AccountID         jmapID   `json:"accountId"`
	OldState          string   `json:"oldState"`
	NewState          string   `json:"newState"`
	HasMoreChanges    bool     `json:"hasMoreChanges"`
	Created           []jmapID `json:"created"`
	Updated           []jmapID `json:"updated"`
	Destroyed         []jmapID `json:"destroyed"`
	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

type msgChangesHandler struct{ h *handlerSet }

func (h *msgChangesHandler) Method() string { return "Message/changes" }

func (h *msgChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req msgChangesRequest
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
	currentSeq, err := h.h.store.Meta().GetMaxChangeSeqForKind(ctx, pid, store.EntityKindChatMessage)
	if err != nil {
		return nil, serverFail(err)
	}
	current := int64(currentSeq)
	newState := stateFromCounter(current)
	resp := msgChangesResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		OldState:  req.SinceState,
		NewState:  newState,
		Created:   []jmapID{},
		Updated:   []jmapID{},
		Destroyed: []jmapID{},
	}
	if since == current {
		return resp, nil
	}
	if since > current {
		return nil, protojmap.NewMethodError("cannotCalculateChanges", "sinceState is in the future")
	}
	created, updated, destroyed, ferr := walkChatChangeFeed(ctx, h.h.store.Meta(), pid, store.EntityKindChatMessage, since)
	if ferr != nil {
		return nil, serverFail(ferr)
	}
	for id := range created {
		resp.Created = append(resp.Created, jmapIDFromMessage(store.ChatMessageID(id)))
	}
	for id := range updated {
		resp.Updated = append(resp.Updated, jmapIDFromMessage(store.ChatMessageID(id)))
	}
	for id := range destroyed {
		resp.Destroyed = append(resp.Destroyed, jmapIDFromMessage(store.ChatMessageID(id)))
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

// -- Message/set ------------------------------------------------------

type msgSetRequest struct {
	AccountID jmapID                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[jmapID]json.RawMessage `json:"update"`
	Destroy   []jmapID                   `json:"destroy"`
}

type msgSetResponse struct {
	AccountID    jmapID                 `json:"accountId"`
	OldState     string                 `json:"oldState"`
	NewState     string                 `json:"newState"`
	Created      map[string]jmapMessage `json:"created"`
	Updated      map[jmapID]any         `json:"updated"`
	Destroyed    []jmapID               `json:"destroyed"`
	NotCreated   map[string]setError    `json:"notCreated"`
	NotUpdated   map[jmapID]setError    `json:"notUpdated"`
	NotDestroyed map[jmapID]setError    `json:"notDestroyed"`
}

type msgCreateInput struct {
	ConversationID   jmapID           `json:"conversationId"`
	Body             jmapMessageBody  `json:"body"`
	ReplyToMessageID jmapID           `json:"replyToMessageId"`
	Attachments      []jmapAttachment `json:"attachments"`
}

type msgUpdateInput struct {
	Body *jmapMessageBody `json:"body"`
}

type msgSetHandler struct{ h *handlerSet }

func (h *msgSetHandler) Method() string { return "Message/set" }

func (h *msgSetHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req msgSetRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentMessageState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	if req.IfInState != nil && *req.IfInState != state {
		return nil, protojmap.NewMethodError("stateMismatch", "ifInState does not match current state")
	}
	resp := msgSetResponse{
		AccountID:    string(protojmap.AccountIDForPrincipal(pid)),
		OldState:     state,
		NewState:     state,
		Created:      map[string]jmapMessage{},
		Updated:      map[jmapID]any{},
		Destroyed:    []jmapID{},
		NotCreated:   map[string]setError{},
		NotUpdated:   map[jmapID]setError{},
		NotDestroyed: map[jmapID]setError{},
	}
	creationRefs := make(map[string]store.ChatMessageID, len(req.Create))

	for key, raw := range req.Create {
		var in msgCreateInput
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				resp.NotCreated[key] = setError{Type: "invalidProperties", Description: err.Error()}
				continue
			}
		}
		m, serr, err := h.h.createMessage(ctx, pid, in)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotCreated[key] = *serr
			continue
		}
		creationRefs[key] = m.ID
		resp.Created[key] = renderMessage(m)
	}

	for raw, payload := range req.Update {
		id, ok := resolveMessageID(raw, creationRefs)
		if !ok {
			resp.NotUpdated[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.updateMessage(ctx, pid, id, payload)
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
		id, ok := resolveMessageID(raw, creationRefs)
		if !ok {
			resp.NotDestroyed[raw] = setError{Type: "notFound"}
			continue
		}
		serr, err := h.h.destroyMessage(ctx, pid, id)
		if err != nil {
			return nil, serverFail(err)
		}
		if serr != nil {
			resp.NotDestroyed[raw] = *serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, raw)
	}

	newState, err := currentMessageState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	resp.NewState = newState
	return resp, nil
}

func resolveMessageID(raw jmapID, creationRefs map[string]store.ChatMessageID) (store.ChatMessageID, bool) {
	if strings.HasPrefix(raw, "#") {
		if id, ok := creationRefs[strings.TrimPrefix(raw, "#")]; ok {
			return id, true
		}
		return 0, false
	}
	return messageIDFromJMAP(raw)
}

func (h *handlerSet) createMessage(
	ctx context.Context,
	sender store.PrincipalID,
	in msgCreateInput,
) (store.ChatMessage, *setError, error) {
	cid, ok := conversationIDFromJMAP(in.ConversationID)
	if !ok {
		return store.ChatMessage{}, &setError{
			Type: "invalidProperties", Properties: []string{"conversationId"},
			Description: "conversationId is required",
		}, nil
	}
	conv, err := h.store.Meta().GetChatConversation(ctx, cid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ChatMessage{}, &setError{Type: "notFound"}, nil
		}
		return store.ChatMessage{}, nil, err
	}
	if conv.IsArchived {
		return store.ChatMessage{}, &setError{Type: "forbidden", Description: "conversation is archived"}, nil
	}
	members, err := h.store.Meta().ListChatMembershipsByConversation(ctx, cid)
	if err != nil {
		return store.ChatMessage{}, nil, err
	}
	if !principalIsMember(members, sender) {
		return store.ChatMessage{}, &setError{Type: "forbidden", Description: "you are not a member of this conversation"}, nil
	}

	// REQ-CHAT-71: a sender blocked by any other DM member is rejected.
	if conv.Kind == store.ChatConversationKindDM {
		other := otherDMMember(members, sender)
		if other != 0 {
			blocked, err := h.store.Meta().IsBlocked(ctx, other, sender)
			if err != nil {
				return store.ChatMessage{}, nil, err
			}
			if blocked {
				return store.ChatMessage{}, &setError{
					Type:        "forbidden",
					Description: "the recipient has blocked this sender",
				}, nil
			}
		}
	}

	// Validate body shape and size.
	body := in.Body
	if body.Format == "" {
		body.Format = store.ChatBodyFormatText
	}
	if body.Format != store.ChatBodyFormatText && body.Format != store.ChatBodyFormatHTML && body.Format != store.ChatBodyFormatMarkdown {
		return store.ChatMessage{}, &setError{
			Type: "invalidProperties", Properties: []string{"body.format"},
			Description: "body.format must be 'text', 'markdown', or 'html'",
		}, nil
	}
	if body.Format == store.ChatBodyFormatText && body.Text == "" {
		return store.ChatMessage{}, &setError{
			Type: "invalidProperties", Properties: []string{"body.text"},
			Description: "body.text must be non-empty for plain messages",
		}, nil
	}
	if body.Format == store.ChatBodyFormatHTML && body.HTML == "" {
		return store.ChatMessage{}, &setError{
			Type: "invalidProperties", Properties: []string{"body.html"},
			Description: "body.html must be non-empty for html messages",
		}, nil
	}
	if h.limits.MaxMessageBodyBytes > 0 {
		size := len(body.Text) + len(body.HTML)
		if size > h.limits.MaxMessageBodyBytes {
			return store.ChatMessage{}, &setError{
				Type: "overLimit", Properties: []string{"body"},
				Description: "message body exceeds maxMessageBodyBytes",
			}, nil
		}
	}
	if h.limits.MaxAttachmentsPerMessage > 0 && len(in.Attachments) > h.limits.MaxAttachmentsPerMessage {
		return store.ChatMessage{}, &setError{
			Type: "overLimit", Properties: []string{"attachments"},
			Description: "too many attachments",
		}, nil
	}

	var replyTo *store.ChatMessageID
	if in.ReplyToMessageID != "" {
		v, ok := messageIDFromJMAP(in.ReplyToMessageID)
		if !ok {
			return store.ChatMessage{}, &setError{
				Type: "invalidProperties", Properties: []string{"replyToMessageId"},
				Description: "replyToMessageId must be a numeric id",
			}, nil
		}
		replyTo = &v
	}

	// Encode attachments as the canonical JSON shape the store
	// expects (ChatValidateAttachments: blob_hash + content_type +
	// filename + size).
	var attsJSON []byte
	if len(in.Attachments) > 0 {
		raw := make([]map[string]any, 0, len(in.Attachments))
		for _, a := range in.Attachments {
			raw = append(raw, map[string]any{
				"blob_hash":    a.BlobID,
				"content_type": a.ContentType,
				"filename":     a.Filename,
				"size":         a.Size,
			})
		}
		var err error
		attsJSON, err = json.Marshal(raw)
		if err != nil {
			return store.ChatMessage{}, nil, err
		}
	}

	senderPID := sender
	row := store.ChatMessage{
		ConversationID:    cid,
		SenderPrincipalID: &senderPID,
		BodyText:          body.Text,
		BodyHTML:          body.HTML,
		BodyFormat:        body.Format,
		ReplyToMessageID:  replyTo,
		AttachmentsJSON:   attsJSON,
		CreatedAt:         h.clk.Now(),
	}
	id, err := h.store.Meta().InsertChatMessage(ctx, row)
	if err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			return store.ChatMessage{}, &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
		return store.ChatMessage{}, nil, err
	}
	loaded, err := h.store.Meta().GetChatMessage(ctx, id)
	if err != nil {
		return store.ChatMessage{}, nil, err
	}
	return loaded, nil, nil
}

func (h *handlerSet) updateMessage(
	ctx context.Context,
	caller store.PrincipalID,
	id store.ChatMessageID,
	raw json.RawMessage,
) (*setError, error) {
	m, err := h.store.Meta().GetChatMessage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	// REQ-CHAT-21 / Wave 2.9 sec audit: system messages (call.started,
	// call.ended, membership join/leave) are server-emitted and must
	// not be mutable through Message/set, regardless of caller — even
	// the nominal "sender" of a system row cannot edit it.
	if m.IsSystem {
		return &setError{Type: "forbidden", Description: "system messages are immutable"}, nil
	}
	if m.DeletedAt != nil {
		return &setError{Type: "forbidden", Description: "message has been deleted"}, nil
	}
	if m.SenderPrincipalID == nil || *m.SenderPrincipalID != caller {
		return &setError{Type: "forbidden", Description: "only the original sender may edit a message"}, nil
	}
	var in msgUpdateInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return &setError{Type: "invalidProperties", Description: err.Error()}, nil
		}
	}
	// REQ-CHAT-20: enforce the per-account / per-conversation edit
	// window before applying any body update. The window is consulted
	// only for body edits; reaction toggles and read-receipt updates
	// flow through other handlers and are not subject to this rule.
	if in.Body != nil {
		conv, err := h.store.Meta().GetChatConversation(ctx, m.ConversationID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return &setError{Type: "notFound"}, nil
			}
			return nil, err
		}
		windowSecs, err := h.effectiveEditWindow(ctx, conv, caller)
		if err != nil {
			return nil, err
		}
		if windowSecs > 0 {
			elapsed := h.clk.Now().Sub(m.CreatedAt)
			if elapsed > time.Duration(windowSecs)*time.Second {
				return &setError{
					Type:        "forbidden",
					Description: "edit window has expired for this message",
				}, nil
			}
		}
	}
	if in.Body != nil {
		body := *in.Body
		if body.Format == "" {
			body.Format = m.BodyFormat
		}
		if body.Format != store.ChatBodyFormatText && body.Format != store.ChatBodyFormatHTML && body.Format != store.ChatBodyFormatMarkdown {
			return &setError{
				Type: "invalidProperties", Properties: []string{"body.format"},
				Description: "body.format must be 'text', 'markdown', or 'html'",
			}, nil
		}
		if h.limits.MaxMessageBodyBytes > 0 {
			size := len(body.Text) + len(body.HTML)
			if size > h.limits.MaxMessageBodyBytes {
				return &setError{
					Type: "overLimit", Properties: []string{"body"},
					Description: "message body exceeds maxMessageBodyBytes",
				}, nil
			}
		}
		m.BodyText = body.Text
		m.BodyHTML = body.HTML
		m.BodyFormat = body.Format
		now := h.clk.Now()
		m.EditedAt = &now
	}
	if err := h.store.Meta().UpdateChatMessage(ctx, m); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	return nil, nil
}

func (h *handlerSet) destroyMessage(
	ctx context.Context,
	caller store.PrincipalID,
	id store.ChatMessageID,
) (*setError, error) {
	m, err := h.store.Meta().GetChatMessage(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	// REQ-CHAT-21 / Wave 2.9 sec audit: system messages cannot be
	// destroyed through Message/set; clients that want to hide a
	// call.started row do so client-side.
	if m.IsSystem {
		return &setError{Type: "forbidden", Description: "system messages are immutable"}, nil
	}
	convMembers, err := h.store.Meta().ListChatMembershipsByConversation(ctx, m.ConversationID)
	if err != nil {
		return nil, err
	}
	if !principalIsMember(convMembers, caller) {
		return &setError{Type: "notFound"}, nil
	}
	isSender := m.SenderPrincipalID != nil && *m.SenderPrincipalID == caller
	if !isSender && !canManageConversation(convMembers, caller) {
		return &setError{Type: "forbidden", Description: "only the sender or an admin may delete a message"}, nil
	}
	if err := h.store.Meta().SoftDeleteChatMessage(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &setError{Type: "notFound"}, nil
		}
		return nil, err
	}
	return nil, nil
}

// -- Message/query ----------------------------------------------------

type msgFilterCondition struct {
	ConversationID    *jmapID `json:"conversationId"`
	SenderPrincipalID *jmapID `json:"senderPrincipalId"`
	Before            *string `json:"before"`
	After             *string `json:"after"`
	Text              *string `json:"text"`
}

type msgQueryRequest struct {
	AccountID      jmapID              `json:"accountId"`
	Filter         *msgFilterCondition `json:"filter"`
	Sort           []comparator        `json:"sort"`
	Position       int                 `json:"position"`
	Anchor         *jmapID             `json:"anchor"`
	AnchorOffset   int                 `json:"anchorOffset"`
	Limit          *int                `json:"limit"`
	CalculateTotal bool                `json:"calculateTotal"`
}

type msgQueryResponse struct {
	AccountID  jmapID   `json:"accountId"`
	QueryState string   `json:"queryState"`
	CanCalcCh  bool     `json:"canCalculateChanges"`
	Position   int      `json:"position"`
	IDs        []jmapID `json:"ids"`
	Total      *int     `json:"total,omitempty"`
	Limit      *int     `json:"limit,omitempty"`
}

type msgQueryHandler struct{ h *handlerSet }

func (h *msgQueryHandler) Method() string { return "Message/query" }

// Execute services Message/query.
//
// Sort and search behaviour (Wave 2.9.6 Track D):
//
//   - When filter.text is empty, the existing chronological path runs:
//     ListChatMessages is filtered server-side by conversationId,
//     senderPrincipalId, before, and after, and the default sort is
//     createdAt asc (overrideable via the sort comparator list).
//   - When filter.text is non-empty, the handler routes through the
//     FTS index (REQ-CHAT-80..82): SearchChatMessages returns ids in
//     descending relevance order scoped to the caller's memberships,
//     each id is round-tripped through GetChatMessage to apply the
//     remaining filters (before, after, senderPrincipalId), and the
//     default sort becomes FTS relevance descending. An explicit
//     comparator list still wins. If the handler was constructed
//     without an FTS backend, a substring scan over ListChatMessages
//     stands in and a one-time warn log is emitted; production wires a
//     real index via RegisterWithFTS.
//
// Membership scoping (REQ-CHAT-82, REQ-CHAT-101) is enforced at both
// layers: the FTS query restricts hits to the caller's conversation
// ids, and the substring fallback drops rows in non-member
// conversations before returning.
func (h *msgQueryHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req msgQueryRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	state, err := currentMessageState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	allowed, err := principalConversationIDs(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}

	filter := store.ChatMessageFilter{IncludeDeleted: true}
	if req.Filter != nil {
		if req.Filter.ConversationID != nil {
			id, ok := conversationIDFromJMAP(*req.Filter.ConversationID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments", "conversationId is malformed")
			}
			if _, ok := allowed[id]; !ok {
				return nil, protojmap.NewMethodError("notFound", "conversation is not accessible")
			}
			cid := id
			filter.ConversationID = &cid
		}
		if req.Filter.SenderPrincipalID != nil {
			v, ok := principalIDFromJMAP(*req.Filter.SenderPrincipalID)
			if !ok {
				return nil, protojmap.NewMethodError("invalidArguments", "senderPrincipalId is malformed")
			}
			s := v
			filter.SenderPrincipalID = &s
		}
		if req.Filter.Before != nil {
			t, err := parseRFC3339(*req.Filter.Before)
			if err != nil {
				return nil, protojmap.NewMethodError("invalidArguments", "before: "+err.Error())
			}
			b := t
			filter.CreatedBefore = &b
		}
		if req.Filter.After != nil {
			t, err := parseRFC3339(*req.Filter.After)
			if err != nil {
				return nil, protojmap.NewMethodError("invalidArguments", "after: "+err.Error())
			}
			a := t
			filter.CreatedAfter = &a
		}
	}

	hasText := req.Filter != nil && req.Filter.Text != nil && *req.Filter.Text != ""
	useFTS := hasText && h.h.fts != nil

	var matched []store.ChatMessage
	var ftsOrder map[store.ChatMessageID]int
	if useFTS {
		convIDs := conversationIDsToFilter(filter.ConversationID, allowed)
		hits, err := h.h.fts.SearchChatMessages(ctx, *req.Filter.Text, convIDs, 0)
		if err != nil {
			return nil, serverFail(err)
		}
		ftsOrder = make(map[store.ChatMessageID]int, len(hits))
		matched = make([]store.ChatMessage, 0, len(hits))
		for rank, id := range hits {
			m, err := h.h.store.Meta().GetChatMessage(ctx, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return nil, serverFail(err)
			}
			// Re-check membership: the FTS index is restricted to
			// the caller's conversations at query time, but a
			// race against a Membership/set destroy could still
			// leak a stale hit. Defence in depth (REQ-CHAT-101).
			if _, ok := allowed[m.ConversationID]; !ok {
				continue
			}
			if m.IsSystem || m.DeletedAt != nil {
				// IsSystem and soft-deleted rows are excluded from
				// the index; this branch protects against an in-
				// flight state change between the search and the
				// load.
				continue
			}
			if !chatMessageMatchesFilter(m, filter) {
				continue
			}
			ftsOrder[m.ID] = rank
			matched = append(matched, m)
		}
	} else {
		if hasText {
			h.h.ftsFallbackWarnOnce.Do(func() {
				h.h.logger.Warn("chat FTS not wired; substring fallback in use",
					"req", "Message/query",
				)
			})
		}
		rows, err := h.h.store.Meta().ListChatMessages(ctx, filter)
		if err != nil {
			return nil, serverFail(err)
		}
		matched = make([]store.ChatMessage, 0, len(rows))
		for _, m := range rows {
			if _, ok := allowed[m.ConversationID]; !ok {
				continue
			}
			if hasText {
				needle := strings.ToLower(*req.Filter.Text)
				if !strings.Contains(strings.ToLower(m.BodyText), needle) {
					continue
				}
			}
			matched = append(matched, m)
		}
	}
	sortChatQueryResults(matched, req.Sort, useFTS, ftsOrder)

	resp := msgQueryResponse{
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
		resp.IDs = append(resp.IDs, jmapIDFromMessage(m.ID))
	}
	return resp, nil
}

func sortMessages(xs []store.ChatMessage, comps []comparator) {
	if len(comps) == 0 {
		comps = []comparator{{Property: "createdAt"}}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		for _, c := range comps {
			asc := true
			if c.IsAscending != nil {
				asc = *c.IsAscending
			}
			cmp := compareMessage(xs[i], xs[j], c.Property)
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

// sortChatQueryResults orders Message/query hits.
//
// When the caller supplies an explicit sort comparator list, that list
// wins regardless of whether FTS produced the candidate set — the JMAP
// contract is that the sort spec is authoritative.
//
// When the comparator list is empty and FTS produced the candidates,
// the default sort is FTS relevance (descending) — ftsRank carries the
// rank assigned by SearchChatMessages (lower rank = higher relevance).
//
// When the comparator list is empty and the path was the chronological
// fallback, the default is createdAt asc, matching the pre-Wave-2.9.6
// behaviour.
func sortChatQueryResults(
	xs []store.ChatMessage,
	comps []comparator,
	useFTS bool,
	ftsRank map[store.ChatMessageID]int,
) {
	if len(comps) > 0 {
		sortMessages(xs, comps)
		return
	}
	if useFTS {
		sort.SliceStable(xs, func(i, j int) bool {
			ri, oi := ftsRank[xs[i].ID]
			rj, oj := ftsRank[xs[j].ID]
			switch {
			case oi && oj:
				return ri < rj
			case oi:
				return true
			case oj:
				return false
			default:
				return xs[i].ID < xs[j].ID
			}
		})
		return
	}
	sortMessages(xs, comps)
}

// conversationIDsToFilter resolves the set of conversation ids the FTS
// query should be scoped to. When the caller specified an explicit
// filter.conversationId the search runs against just that one (the
// membership check above already verified the caller is a member);
// otherwise every conversation the caller is a member of is in scope.
// Order is irrelevant for the disjunction the index builds, but
// ranging over a map yields a non-deterministic slice — fine for the
// query, intentionally not relied on by tests.
func conversationIDsToFilter(
	scoped *store.ConversationID,
	allowed map[store.ConversationID]struct{},
) []store.ConversationID {
	if scoped != nil {
		return []store.ConversationID{*scoped}
	}
	out := make([]store.ConversationID, 0, len(allowed))
	for id := range allowed {
		out = append(out, id)
	}
	return out
}

// chatMessageMatchesFilter applies the non-text predicates to a single
// row. Used by the FTS path because the index does not enforce
// senderPrincipalId / before / after — those are still in-process
// after the relevance-sorted hit list comes back.
func chatMessageMatchesFilter(m store.ChatMessage, f store.ChatMessageFilter) bool {
	if f.ConversationID != nil && m.ConversationID != *f.ConversationID {
		return false
	}
	if f.SenderPrincipalID != nil {
		if m.SenderPrincipalID == nil || *m.SenderPrincipalID != *f.SenderPrincipalID {
			return false
		}
	}
	if f.CreatedAfter != nil && !m.CreatedAt.After(*f.CreatedAfter) {
		return false
	}
	if f.CreatedBefore != nil && !m.CreatedAt.Before(*f.CreatedBefore) {
		return false
	}
	return true
}

func compareMessage(a, b store.ChatMessage, property string) int {
	switch property {
	case "createdAt":
		switch {
		case a.CreatedAt.Before(b.CreatedAt):
			return -1
		case a.CreatedAt.After(b.CreatedAt):
			return 1
		}
		return 0
	case "modseq", "id":
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	}
	return 0
}

// -- Message/queryChanges --------------------------------------------

type msgQueryChangesHandler struct{ h *handlerSet }

func (msgQueryChangesHandler) Method() string { return "Message/queryChanges" }

func (msgQueryChangesHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	_ = ctx
	_ = args
	return nil, protojmap.NewMethodError("cannotCalculateChanges",
		"Message/queryChanges is unsupported; clients re-issue Message/query")
}

// -- Message/react ----------------------------------------------------

// msgReactHandler implements the herold-namespaced custom method that
// toggles a reaction emoji on a message. REQ-CHAT-23: the requester
// must be a member of the conversation; otherwise 403.
type msgReactRequest struct {
	AccountID jmapID `json:"accountId"`
	MessageID jmapID `json:"messageId"`
	Emoji     string `json:"emoji"`
	Present   bool   `json:"present"`
}

type msgReactResponse struct {
	AccountID jmapID              `json:"accountId"`
	MessageID jmapID              `json:"messageId"`
	Reactions map[string][]string `json:"reactions"`
	State     string              `json:"state"`
}

type msgReactHandler struct{ h *handlerSet }

func (h *msgReactHandler) Method() string { return "Message/react" }

func (h *msgReactHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	pid, merr := requirePrincipal(ctx)
	if merr != nil {
		return nil, merr
	}
	var req msgReactRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := requireAccount(req.AccountID, pid); merr != nil {
		return nil, merr
	}
	mid, ok := messageIDFromJMAP(req.MessageID)
	if !ok {
		return nil, protojmap.NewMethodError("invalidArguments", "messageId is required")
	}
	if strings.TrimSpace(req.Emoji) == "" {
		return nil, protojmap.NewMethodError("invalidArguments", "emoji is required")
	}
	m, err := h.h.store.Meta().GetChatMessage(ctx, mid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, protojmap.NewMethodError("notFound", "message not found")
		}
		return nil, serverFail(err)
	}
	members, err := h.h.store.Meta().ListChatMembershipsByConversation(ctx, m.ConversationID)
	if err != nil {
		return nil, serverFail(err)
	}
	if !principalIsMember(members, pid) {
		return nil, protojmap.NewMethodError("forbidden", "you are not a member of this conversation")
	}
	if err := h.h.store.Meta().SetChatReaction(ctx, mid, req.Emoji, pid, req.Present); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, protojmap.NewMethodError("notFound", "message not found")
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
		return nil, serverFail(err)
	}
	updated, err := h.h.store.Meta().GetChatMessage(ctx, mid)
	if err != nil {
		return nil, serverFail(err)
	}
	state, err := currentMessageState(ctx, h.h.store.Meta(), pid)
	if err != nil {
		return nil, serverFail(err)
	}
	rendered := renderMessage(updated)
	return msgReactResponse{
		AccountID: string(protojmap.AccountIDForPrincipal(pid)),
		MessageID: req.MessageID,
		Reactions: rendered.Reactions,
		State:     state,
	}, nil
}

// effectiveEditWindow returns the edit-window duration in seconds that
// applies to a message in conv when sent by sender (REQ-CHAT-20):
//
//   - if conv.EditWindowSeconds is non-nil, use it directly;
//   - otherwise fall back to the sender's
//     ChatAccountSettings.DefaultEditWindowSeconds.
//
// Returns 0 when the effective policy is "no time limit"; positive
// when the caller must compare elapsed time against the value.
func (h *handlerSet) effectiveEditWindow(ctx context.Context, conv store.ChatConversation, sender store.PrincipalID) (int64, error) {
	if conv.EditWindowSeconds != nil {
		return *conv.EditWindowSeconds, nil
	}
	settings, err := h.store.Meta().GetChatAccountSettings(ctx, sender)
	if err != nil {
		return 0, err
	}
	return settings.DefaultEditWindowSeconds, nil
}

// silence unused import warnings during in-flight rewrites.
var _ = time.Now
