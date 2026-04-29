package fakestore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.8 chat subsystem
// (REQ-CHAT-*) against the fakestore. The schema-side commentary lives
// in internal/storesqlite/migrations/0012_chat.sql; the type
// definitions are in internal/store/types_chat.go.

// -- Helpers ----------------------------------------------------------

// appendStateChangeForMembersLocked fans a state-change to every current
// member of convID plus the owner principal (always included). Requires
// s.mu held exclusively (re #47).
func (s *Store) appendStateChangeForMembersLocked(
	owner store.PrincipalID,
	convID store.ConversationID,
	kind store.EntityKind, entityID uint64, parentEntityID uint64,
	op store.ChangeOp, now time.Time,
) {
	seen := make(map[store.PrincipalID]bool)
	ordered := []store.PrincipalID{owner}
	seen[owner] = true
	if s.phase2 != nil {
		// Collect members in ascending membership ID order for
		// deterministic test output.
		type entry struct {
			id  store.MembershipID
			pid store.PrincipalID
		}
		var members []entry
		for _, mb := range s.phase2.chatMemberships {
			if mb.ConversationID == convID {
				members = append(members, entry{mb.ID, mb.PrincipalID})
			}
		}
		sort.Slice(members, func(i, j int) bool { return members[i].id < members[j].id })
		for _, e := range members {
			if !seen[e.pid] {
				seen[e.pid] = true
				ordered = append(ordered, e.pid)
			}
		}
	}
	for _, pid := range ordered {
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID:    pid,
			Kind:           kind,
			EntityID:       entityID,
			ParentEntityID: parentEntityID,
			Op:             op,
			ProducedAt:     now,
		})
	}
}

// -- ChatConversation -------------------------------------------------

func (m *metaFace) InsertChatConversation(ctx context.Context, c store.ChatConversation) (store.ConversationID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	c.ID = s.phase2.nextChatConversation
	s.phase2.nextChatConversation++
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.ModSeq = 1
	// REQ-CHAT-32: new conversations default to read_receipts on. Mirror
	// the SQL backends' schema default; callers opt out via Update.
	c.ReadReceiptsEnabled = true
	if c.LastMessageAt != nil {
		t := c.LastMessageAt.UTC()
		c.LastMessageAt = &t
	}
	if c.RetentionSeconds != nil {
		v := *c.RetentionSeconds
		c.RetentionSeconds = &v
	}
	if c.EditWindowSeconds != nil {
		v := *c.EditWindowSeconds
		c.EditWindowSeconds = &v
	}
	s.phase2.chatConversations[c.ID] = c
	// At creation time the conversation has no members yet; the fan
	// falls back to creator-only (re #47).
	s.appendStateChangeForMembersLocked(c.CreatedByPrincipalID, c.ID,
		store.EntityKindConversation, uint64(c.ID), 0,
		store.ChangeOpCreated, now)
	return c.ID, nil
}

func (m *metaFace) GetChatConversation(ctx context.Context, id store.ConversationID) (store.ChatConversation, error) {
	if err := ctx.Err(); err != nil {
		return store.ChatConversation{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ChatConversation{}, fmt.Errorf("conversation %d: %w", id, store.ErrNotFound)
	}
	c, ok := s.phase2.chatConversations[id]
	if !ok {
		return store.ChatConversation{}, fmt.Errorf("conversation %d: %w", id, store.ErrNotFound)
	}
	return cloneChatConversation(c), nil
}

func (m *metaFace) ListChatConversations(ctx context.Context, filter store.ChatConversationFilter) ([]store.ChatConversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatConversation
	for _, c := range s.phase2.chatConversations {
		if filter.Kind != nil && c.Kind != *filter.Kind {
			continue
		}
		if filter.CreatedByPrincipalID != nil && c.CreatedByPrincipalID != *filter.CreatedByPrincipalID {
			continue
		}
		if !filter.IncludeArchived && c.IsArchived {
			continue
		}
		if filter.AfterModSeq != 0 && c.ModSeq <= filter.AfterModSeq {
			continue
		}
		if filter.AfterID != 0 && c.ID <= filter.AfterID {
			continue
		}
		out = append(out, cloneChatConversation(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateChatConversation(ctx context.Context, c store.ChatConversation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatConversations[c.ID]
	if !ok {
		return fmt.Errorf("conversation %d: %w", c.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	cur.Name = c.Name
	cur.Topic = c.Topic
	if c.LastMessageAt != nil {
		t := c.LastMessageAt.UTC()
		cur.LastMessageAt = &t
	} else {
		cur.LastMessageAt = nil
	}
	cur.MessageCount = c.MessageCount
	cur.IsArchived = c.IsArchived
	cur.ReadReceiptsEnabled = c.ReadReceiptsEnabled
	if c.RetentionSeconds != nil {
		v := *c.RetentionSeconds
		cur.RetentionSeconds = &v
	} else {
		cur.RetentionSeconds = nil
	}
	if c.EditWindowSeconds != nil {
		v := *c.EditWindowSeconds
		cur.EditWindowSeconds = &v
	} else {
		cur.EditWindowSeconds = nil
	}
	cur.UpdatedAt = now
	cur.ModSeq++
	s.phase2.chatConversations[c.ID] = cur
	s.appendStateChangeForMembersLocked(cur.CreatedByPrincipalID, c.ID,
		store.EntityKindConversation, uint64(c.ID), 0,
		store.ChangeOpUpdated, now)
	return nil
}

func (m *metaFace) DeleteChatConversation(ctx context.Context, id store.ConversationID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatConversations[id]
	if !ok {
		return fmt.Errorf("conversation %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	// Cascade: drop every membership and message owned by the
	// conversation and append per-row destroyed state-change rows fanned
	// to all members (re #47). Build the audience snapshot first so the
	// fan helper does not read partially-deleted membership rows.
	seen := make(map[store.PrincipalID]bool)
	audience := []store.PrincipalID{cur.CreatedByPrincipalID}
	seen[cur.CreatedByPrincipalID] = true
	type memEntry struct {
		id store.MembershipID
		mb store.ChatMembership
	}
	var memEntries []memEntry
	for memID, mb := range s.phase2.chatMemberships {
		if mb.ConversationID == id {
			memEntries = append(memEntries, memEntry{memID, mb})
			if !seen[mb.PrincipalID] {
				seen[mb.PrincipalID] = true
				audience = append(audience, mb.PrincipalID)
			}
		}
	}
	sort.Slice(memEntries, func(i, j int) bool { return memEntries[i].id < memEntries[j].id })
	// appFan emits a state-change row for every principal in audience.
	appFan := func(kind store.EntityKind, entityID, parentEntityID uint64, op store.ChangeOp) {
		for _, pid := range audience {
			s.appendStateChangeLocked(store.StateChange{
				PrincipalID:    pid,
				Kind:           kind,
				EntityID:       entityID,
				ParentEntityID: parentEntityID,
				Op:             op,
				ProducedAt:     now,
			})
		}
	}
	var msgIDs []store.ChatMessageID
	for mid, msg := range s.phase2.chatMessages {
		if msg.ConversationID == id {
			msgIDs = append(msgIDs, mid)
		}
	}
	sort.Slice(msgIDs, func(i, j int) bool { return msgIDs[i] < msgIDs[j] })
	for _, mid := range msgIDs {
		delete(s.phase2.chatMessages, mid)
		appFan(store.EntityKindChatMessage, uint64(mid), uint64(id), store.ChangeOpDestroyed)
	}
	for _, e := range memEntries {
		delete(s.phase2.chatMemberships, e.id)
		appFan(store.EntityKindMembership, uint64(e.id), uint64(id), store.ChangeOpDestroyed)
	}
	delete(s.phase2.chatConversations, id)
	appFan(store.EntityKindConversation, uint64(id), 0, store.ChangeOpDestroyed)
	return nil
}

// -- ChatMembership ---------------------------------------------------

func (m *metaFace) InsertChatMembership(ctx context.Context, mb store.ChatMembership) (store.MembershipID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for _, ex := range s.phase2.chatMemberships {
		if ex.ConversationID == mb.ConversationID && ex.PrincipalID == mb.PrincipalID {
			return 0, fmt.Errorf("membership (%d, %d): %w", mb.ConversationID, mb.PrincipalID, store.ErrConflict)
		}
	}
	now := s.clk.Now()
	mb.ID = s.phase2.nextChatMembership
	s.phase2.nextChatMembership++
	if mb.JoinedAt.IsZero() {
		mb.JoinedAt = now
	}
	if mb.NotificationsSetting == "" {
		mb.NotificationsSetting = store.ChatNotificationsAll
	}
	mb.ModSeq = 1
	if mb.LastReadMessageID != nil {
		v := *mb.LastReadMessageID
		mb.LastReadMessageID = &v
	}
	if mb.MuteUntil != nil {
		t := mb.MuteUntil.UTC()
		mb.MuteUntil = &t
	}
	s.phase2.chatMemberships[mb.ID] = mb
	// The newly inserted membership is now in the map, so
	// appendStateChangeForMembersLocked will include the new member as
	// well as all pre-existing members (re #47).
	s.appendStateChangeForMembersLocked(mb.PrincipalID, mb.ConversationID,
		store.EntityKindMembership, uint64(mb.ID), uint64(mb.ConversationID),
		store.ChangeOpCreated, now)
	return mb.ID, nil
}

func (m *metaFace) GetChatMembership(ctx context.Context, conversationID store.ConversationID, principalID store.PrincipalID) (store.ChatMembership, error) {
	if err := ctx.Err(); err != nil {
		return store.ChatMembership{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ChatMembership{}, fmt.Errorf("membership (%d, %d): %w", conversationID, principalID, store.ErrNotFound)
	}
	for _, mb := range s.phase2.chatMemberships {
		if mb.ConversationID == conversationID && mb.PrincipalID == principalID {
			return cloneChatMembership(mb), nil
		}
	}
	return store.ChatMembership{}, fmt.Errorf("membership (%d, %d): %w", conversationID, principalID, store.ErrNotFound)
}

func (m *metaFace) ListChatMembershipsByConversation(ctx context.Context, conversationID store.ConversationID) ([]store.ChatMembership, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatMembership
	for _, mb := range s.phase2.chatMemberships {
		if mb.ConversationID != conversationID {
			continue
		}
		out = append(out, cloneChatMembership(mb))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *metaFace) ListChatMembershipsByPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.ChatMembership, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatMembership
	for _, mb := range s.phase2.chatMemberships {
		if mb.PrincipalID != principalID {
			continue
		}
		out = append(out, cloneChatMembership(mb))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *metaFace) UpdateChatMembership(ctx context.Context, mb store.ChatMembership) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatMemberships[mb.ID]
	if !ok {
		return fmt.Errorf("membership %d: %w", mb.ID, store.ErrNotFound)
	}
	now := s.clk.Now()
	cur.Role = mb.Role
	if mb.LastReadMessageID != nil {
		v := *mb.LastReadMessageID
		cur.LastReadMessageID = &v
	} else {
		cur.LastReadMessageID = nil
	}
	cur.IsMuted = mb.IsMuted
	if mb.MuteUntil != nil {
		t := mb.MuteUntil.UTC()
		cur.MuteUntil = &t
	} else {
		cur.MuteUntil = nil
	}
	if mb.NotificationsSetting != "" {
		cur.NotificationsSetting = mb.NotificationsSetting
	}
	cur.ModSeq++
	s.phase2.chatMemberships[mb.ID] = cur
	s.appendStateChangeLocked(store.StateChange{
		PrincipalID:    cur.PrincipalID,
		Kind:           store.EntityKindMembership,
		EntityID:       uint64(cur.ID),
		ParentEntityID: uint64(cur.ConversationID),
		Op:             store.ChangeOpUpdated,
		ProducedAt:     now,
	})
	return nil
}

func (m *metaFace) DeleteChatMembership(ctx context.Context, id store.MembershipID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatMemberships[id]
	if !ok {
		return fmt.Errorf("membership %d: %w", id, store.ErrNotFound)
	}
	now := s.clk.Now()
	// Fan the destroy notification before deleting the row so all current
	// members (including the departing one) receive the change (re #47).
	s.appendStateChangeForMembersLocked(cur.PrincipalID, cur.ConversationID,
		store.EntityKindMembership, uint64(id), uint64(cur.ConversationID),
		store.ChangeOpDestroyed, now)
	delete(s.phase2.chatMemberships, id)
	return nil
}

// -- ChatMessage ------------------------------------------------------

func (m *metaFace) InsertChatMessage(ctx context.Context, msg store.ChatMessage) (store.ChatMessageID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := store.ChatValidateReactions(msg.ReactionsJSON); err != nil {
		return 0, err
	}
	if err := store.ChatValidateAttachments(msg.AttachmentsJSON); err != nil {
		return 0, err
	}
	attHashes, err := store.ChatAttachmentHashes(msg.AttachmentsJSON)
	if err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	conv, ok := s.phase2.chatConversations[msg.ConversationID]
	if !ok {
		return 0, fmt.Errorf("conversation %d: %w", msg.ConversationID, store.ErrNotFound)
	}
	now := s.clk.Now()
	msg.ID = s.phase2.nextChatMessage
	s.phase2.nextChatMessage++
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = now
	}
	if msg.BodyFormat == "" {
		msg.BodyFormat = store.ChatBodyFormatText
	}
	if msg.SenderPrincipalID != nil {
		v := *msg.SenderPrincipalID
		msg.SenderPrincipalID = &v
	}
	if msg.ReplyToMessageID != nil {
		v := *msg.ReplyToMessageID
		msg.ReplyToMessageID = &v
	}
	if msg.EditedAt != nil {
		t := msg.EditedAt.UTC()
		msg.EditedAt = &t
	}
	if msg.DeletedAt != nil {
		t := msg.DeletedAt.UTC()
		msg.DeletedAt = &t
	}
	msg.ReactionsJSON = cloneBytes(msg.ReactionsJSON)
	msg.AttachmentsJSON = cloneBytes(msg.AttachmentsJSON)
	msg.MetadataJSON = cloneBytes(msg.MetadataJSON)
	msg.ModSeq = 1
	s.phase2.chatMessages[msg.ID] = msg
	if msg.DeletedAt == nil {
		t := now
		conv.LastMessageAt = &t
		conv.MessageCount++
		conv.UpdatedAt = now
		conv.ModSeq++
		s.phase2.chatConversations[msg.ConversationID] = conv
	}
	// Increment refcounts for each distinct attachment hash. The
	// blob_refs row for chat attachments is created lazily on first
	// reference (mirrors the SQL incRef path); blobSize is recorded for
	// rows that did not already exist so a Stat read after the first
	// incRef returns the declared size.
	for _, a := range attHashes {
		s.blobRefs[a.Hash]++
		if _, ok := s.blobSize[a.Hash]; !ok {
			s.blobSize[a.Hash] = a.Size
		}
	}
	// Fan the state-change to every current member (re #47).
	s.appendStateChangeForMembersLocked(conv.CreatedByPrincipalID, msg.ConversationID,
		store.EntityKindChatMessage, uint64(msg.ID), uint64(msg.ConversationID),
		store.ChangeOpCreated, now)
	s.appendFTSChangeLocked(store.FTSChange{
		PrincipalID:    conv.CreatedByPrincipalID,
		Kind:           store.EntityKindChatMessage,
		EntityID:       uint64(msg.ID),
		ParentEntityID: uint64(msg.ConversationID),
		Op:             store.ChangeOpCreated,
		ProducedAt:     now,
	})
	return msg.ID, nil
}

func (m *metaFace) GetChatMessage(ctx context.Context, id store.ChatMessageID) (store.ChatMessage, error) {
	if err := ctx.Err(); err != nil {
		return store.ChatMessage{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ChatMessage{}, fmt.Errorf("chat message %d: %w", id, store.ErrNotFound)
	}
	msg, ok := s.phase2.chatMessages[id]
	if !ok {
		return store.ChatMessage{}, fmt.Errorf("chat message %d: %w", id, store.ErrNotFound)
	}
	return cloneChatMessage(msg), nil
}

func (m *metaFace) ListChatMessages(ctx context.Context, filter store.ChatMessageFilter) ([]store.ChatMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatMessage
	for _, msg := range s.phase2.chatMessages {
		if filter.ConversationID != nil && msg.ConversationID != *filter.ConversationID {
			continue
		}
		if filter.SenderPrincipalID != nil {
			if msg.SenderPrincipalID == nil || *msg.SenderPrincipalID != *filter.SenderPrincipalID {
				continue
			}
		}
		if !filter.IncludeDeleted && msg.DeletedAt != nil {
			continue
		}
		if filter.CreatedAfter != nil && !msg.CreatedAt.After(filter.CreatedAfter.UTC()) {
			continue
		}
		if filter.CreatedBefore != nil && !msg.CreatedAt.Before(filter.CreatedBefore.UTC()) {
			continue
		}
		if filter.AfterModSeq != 0 && msg.ModSeq <= filter.AfterModSeq {
			continue
		}
		if filter.AfterID != 0 && msg.ID <= filter.AfterID {
			continue
		}
		out = append(out, cloneChatMessage(msg))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) UpdateChatMessage(ctx context.Context, msg store.ChatMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.ChatValidateReactions(msg.ReactionsJSON); err != nil {
		return err
	}
	if err := store.ChatValidateAttachments(msg.AttachmentsJSON); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatMessages[msg.ID]
	if !ok {
		return fmt.Errorf("chat message %d: %w", msg.ID, store.ErrNotFound)
	}
	conv, ok := s.phase2.chatConversations[cur.ConversationID]
	if !ok {
		return fmt.Errorf("conversation %d: %w", cur.ConversationID, store.ErrNotFound)
	}
	now := s.clk.Now()
	cur.BodyText = msg.BodyText
	cur.BodyHTML = msg.BodyHTML
	if msg.BodyFormat != "" {
		cur.BodyFormat = msg.BodyFormat
	}
	cur.ReactionsJSON = cloneBytes(msg.ReactionsJSON)
	cur.AttachmentsJSON = cloneBytes(msg.AttachmentsJSON)
	cur.MetadataJSON = cloneBytes(msg.MetadataJSON)
	if msg.EditedAt != nil {
		t := msg.EditedAt.UTC()
		cur.EditedAt = &t
	} else {
		cur.EditedAt = nil
	}
	if msg.DeletedAt != nil {
		t := msg.DeletedAt.UTC()
		cur.DeletedAt = &t
	} else {
		cur.DeletedAt = nil
	}
	cur.ModSeq++
	s.phase2.chatMessages[msg.ID] = cur
	s.appendStateChangeForMembersLocked(conv.CreatedByPrincipalID, cur.ConversationID,
		store.EntityKindChatMessage, uint64(cur.ID), uint64(cur.ConversationID),
		store.ChangeOpUpdated, now)
	s.appendFTSChangeLocked(store.FTSChange{
		PrincipalID:    conv.CreatedByPrincipalID,
		Kind:           store.EntityKindChatMessage,
		EntityID:       uint64(cur.ID),
		ParentEntityID: uint64(cur.ConversationID),
		Op:             store.ChangeOpUpdated,
		ProducedAt:     now,
	})
	return nil
}

func (m *metaFace) SoftDeleteChatMessage(ctx context.Context, id store.ChatMessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatMessages[id]
	if !ok {
		return fmt.Errorf("chat message %d: %w", id, store.ErrNotFound)
	}
	if cur.DeletedAt != nil {
		// Idempotent.
		return nil
	}
	conv, ok := s.phase2.chatConversations[cur.ConversationID]
	if !ok {
		return fmt.Errorf("conversation %d: %w", cur.ConversationID, store.ErrNotFound)
	}
	now := s.clk.Now()
	t := now
	cur.DeletedAt = &t
	cur.BodyText = ""
	cur.BodyHTML = ""
	cur.ModSeq++
	s.phase2.chatMessages[id] = cur
	if conv.MessageCount > 0 {
		conv.MessageCount--
	}
	conv.UpdatedAt = now
	conv.ModSeq++
	s.phase2.chatConversations[cur.ConversationID] = conv
	s.appendStateChangeForMembersLocked(conv.CreatedByPrincipalID, cur.ConversationID,
		store.EntityKindChatMessage, uint64(id), uint64(cur.ConversationID),
		store.ChangeOpUpdated, now)
	s.appendFTSChangeLocked(store.FTSChange{
		PrincipalID:    conv.CreatedByPrincipalID,
		Kind:           store.EntityKindChatMessage,
		EntityID:       uint64(id),
		ParentEntityID: uint64(cur.ConversationID),
		Op:             store.ChangeOpUpdated,
		ProducedAt:     now,
	})
	return nil
}

// -- Reactions --------------------------------------------------------

func (m *metaFace) SetChatReaction(ctx context.Context, msgID store.ChatMessageID, emoji string, principalID store.PrincipalID, present bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.ChatValidateEmoji(emoji); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatMessages[msgID]
	if !ok {
		return fmt.Errorf("chat message %d: %w", msgID, store.ErrNotFound)
	}
	conv, ok := s.phase2.chatConversations[cur.ConversationID]
	if !ok {
		return fmt.Errorf("conversation %d: %w", cur.ConversationID, store.ErrNotFound)
	}
	updated, changed, err := store.ChatApplyReaction(cur.ReactionsJSON, emoji, principalID, present)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	cur.ReactionsJSON = cloneBytes(updated)
	cur.ModSeq++
	s.phase2.chatMessages[msgID] = cur
	now := s.clk.Now()
	s.appendStateChangeForMembersLocked(conv.CreatedByPrincipalID, cur.ConversationID,
		store.EntityKindChatMessage, uint64(msgID), uint64(cur.ConversationID),
		store.ChangeOpUpdated, now)
	return nil
}

// -- ChatBlock --------------------------------------------------------

func (m *metaFace) InsertChatBlock(ctx context.Context, b store.ChatBlock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.BlockerPrincipalID == b.BlockedPrincipalID {
		return fmt.Errorf("%w: blocker and blocked principals must differ", store.ErrInvalidArgument)
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	key := chatBlockKey{Blocker: b.BlockerPrincipalID, Blocked: b.BlockedPrincipalID}
	if _, dup := s.phase2.chatBlocks[key]; dup {
		return fmt.Errorf("block (%d, %d): %w", b.BlockerPrincipalID, b.BlockedPrincipalID, store.ErrConflict)
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = s.clk.Now()
	}
	s.phase2.chatBlocks[key] = b
	return nil
}

func (m *metaFace) DeleteChatBlock(ctx context.Context, blocker, blocked store.PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	key := chatBlockKey{Blocker: blocker, Blocked: blocked}
	if _, ok := s.phase2.chatBlocks[key]; !ok {
		return fmt.Errorf("block (%d, %d): %w", blocker, blocked, store.ErrNotFound)
	}
	delete(s.phase2.chatBlocks, key)
	return nil
}

func (m *metaFace) ListChatBlocksBy(ctx context.Context, blocker store.PrincipalID) ([]store.ChatBlock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatBlock
	for k, b := range s.phase2.chatBlocks {
		if k.Blocker != blocker {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BlockedPrincipalID < out[j].BlockedPrincipalID })
	return out, nil
}

func (m *metaFace) IsBlocked(ctx context.Context, blocker, blocked store.PrincipalID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return false, nil
	}
	_, ok := s.phase2.chatBlocks[chatBlockKey{Blocker: blocker, Blocked: blocked}]
	return ok, nil
}

// -- Read pointer -----------------------------------------------------

func (m *metaFace) LastReadAt(ctx context.Context, principalID store.PrincipalID, conversationID store.ConversationID) (*store.ChatMessageID, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, time.Time{}, fmt.Errorf("membership (%d, %d): %w", conversationID, principalID, store.ErrNotFound)
	}
	for _, mb := range s.phase2.chatMemberships {
		if mb.ConversationID == conversationID && mb.PrincipalID == principalID {
			var out *store.ChatMessageID
			if mb.LastReadMessageID != nil {
				v := *mb.LastReadMessageID
				out = &v
			}
			return out, mb.JoinedAt, nil
		}
	}
	return nil, time.Time{}, fmt.Errorf("membership (%d, %d): %w", conversationID, principalID, store.ErrNotFound)
}

func (m *metaFace) ChatPrincipalCanReadBlob(ctx context.Context, principalID store.PrincipalID, blobHash string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return false, nil
	}
	conversations := make(map[store.ConversationID]struct{})
	for _, mb := range s.phase2.chatMemberships {
		if mb.PrincipalID == principalID {
			conversations[mb.ConversationID] = struct{}{}
		}
	}
	if len(conversations) == 0 {
		return false, nil
	}
	needleAttachments := `"blob_hash":"` + blobHash + `"`
	for _, msg := range s.phase2.chatMessages {
		if msg.DeletedAt != nil {
			continue
		}
		if _, ok := conversations[msg.ConversationID]; !ok {
			continue
		}
		if len(msg.AttachmentsJSON) > 0 && strings.Contains(string(msg.AttachmentsJSON), needleAttachments) {
			return true, nil
		}
		if msg.BodyHTML != "" && strings.Contains(msg.BodyHTML, blobHash) {
			return true, nil
		}
	}
	return false, nil
}

func (m *metaFace) CountChatMessagesUnread(ctx context.Context, conversationID store.ConversationID, viewerPID store.PrincipalID, lastReadMessageID *store.ChatMessageID) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return 0, nil
	}
	n := 0
	for _, msg := range s.phase2.chatMessages {
		if msg.ConversationID != conversationID {
			continue
		}
		if msg.DeletedAt != nil {
			continue
		}
		if msg.SenderPrincipalID == nil {
			continue
		}
		if *msg.SenderPrincipalID == viewerPID {
			continue
		}
		if lastReadMessageID != nil && msg.ID <= *lastReadMessageID {
			continue
		}
		n++
	}
	return n, nil
}

func (m *metaFace) SetLastRead(ctx context.Context, principalID store.PrincipalID, conversationID store.ConversationID, msgID store.ChatMessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	for memID, mb := range s.phase2.chatMemberships {
		if mb.ConversationID != conversationID || mb.PrincipalID != principalID {
			continue
		}
		v := msgID
		mb.LastReadMessageID = &v
		mb.ModSeq++
		s.phase2.chatMemberships[memID] = mb
		now := s.clk.Now()
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID:    principalID,
			Kind:           store.EntityKindMembership,
			EntityID:       uint64(memID),
			ParentEntityID: uint64(conversationID),
			Op:             store.ChangeOpUpdated,
			ProducedAt:     now,
		})
		return nil
	}
	return fmt.Errorf("membership (%d, %d): %w", conversationID, principalID, store.ErrNotFound)
}

// -- Account-default settings (Phase 2 Wave 2.9.6 REQ-CHAT-20/92) ---

func (m *metaFace) GetChatAccountSettings(ctx context.Context, principalID store.PrincipalID) (store.ChatAccountSettings, error) {
	if err := ctx.Err(); err != nil {
		return store.ChatAccountSettings{}, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ChatAccountSettings{
			PrincipalID:              principalID,
			DefaultRetentionSeconds:  store.ChatDefaultRetentionSeconds,
			DefaultEditWindowSeconds: store.ChatDefaultEditWindowSeconds,
		}, nil
	}
	if cur, ok := s.phase2.chatAccountSettings[principalID]; ok {
		return cur, nil
	}
	return store.ChatAccountSettings{
		PrincipalID:              principalID,
		DefaultRetentionSeconds:  store.ChatDefaultRetentionSeconds,
		DefaultEditWindowSeconds: store.ChatDefaultEditWindowSeconds,
	}, nil
}

func (m *metaFace) UpsertChatAccountSettings(ctx context.Context, settings store.ChatAccountSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	now := s.clk.Now()
	cur, ok := s.phase2.chatAccountSettings[settings.PrincipalID]
	if !ok {
		settings.CreatedAt = now
	} else {
		settings.CreatedAt = cur.CreatedAt
	}
	settings.UpdatedAt = now
	s.phase2.chatAccountSettings[settings.PrincipalID] = settings
	return nil
}

func (m *metaFace) ListChatAccountSettingsForRetention(ctx context.Context, afterID store.PrincipalID, limit int) ([]store.ChatAccountSettings, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatAccountSettings
	for pid, settings := range s.phase2.chatAccountSettings {
		if settings.DefaultRetentionSeconds <= 0 {
			continue
		}
		if pid <= afterID {
			continue
		}
		out = append(out, settings)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrincipalID < out[j].PrincipalID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// -- Retention helpers (Phase 2 Wave 2.9.6 REQ-CHAT-92) -------------

func (m *metaFace) ListChatConversationsForRetention(ctx context.Context, afterID store.ConversationID, limit int) ([]store.ChatConversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return nil, nil
	}
	var out []store.ChatConversation
	for _, c := range s.phase2.chatConversations {
		if c.RetentionSeconds == nil || *c.RetentionSeconds <= 0 {
			continue
		}
		if c.ID <= afterID {
			continue
		}
		out = append(out, cloneChatConversation(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *metaFace) HardDeleteChatMessage(ctx context.Context, id store.ChatMessageID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	cur, ok := s.phase2.chatMessages[id]
	if !ok {
		return fmt.Errorf("chat message %d: %w", id, store.ErrNotFound)
	}
	conv, ok := s.phase2.chatConversations[cur.ConversationID]
	if !ok {
		return fmt.Errorf("conversation %d: %w", cur.ConversationID, store.ErrNotFound)
	}
	// Resolve the attachments' distinct blob hashes so we can decrement
	// refcounts atomically with the row delete.
	attHashes, err := store.ChatAttachmentHashes(cur.AttachmentsJSON)
	if err != nil {
		return err
	}
	now := s.clk.Now()
	delete(s.phase2.chatMessages, id)
	// Decrement blob_refs for each distinct attachment hash; the
	// blob-store sweeper evicts rows whose count drops to zero
	// out-of-band (REQ-STORE-12 grace window). Mirrors the mail-side
	// expunge / mailbox-delete path.
	for _, a := range attHashes {
		s.blobRefs[a.Hash]--
		if s.blobRefs[a.Hash] < 0 {
			s.blobRefs[a.Hash] = 0
		}
	}
	// Recompute conversation denormalised counters.
	var (
		liveCount int
		lastAt    *time.Time
	)
	for _, msg := range s.phase2.chatMessages {
		if msg.ConversationID != cur.ConversationID || msg.DeletedAt != nil {
			continue
		}
		liveCount++
		if lastAt == nil || msg.CreatedAt.After(*lastAt) {
			t := msg.CreatedAt
			lastAt = &t
		}
	}
	conv.MessageCount = liveCount
	conv.LastMessageAt = lastAt
	conv.UpdatedAt = now
	conv.ModSeq++
	s.phase2.chatConversations[cur.ConversationID] = conv
	s.appendStateChangeForMembersLocked(conv.CreatedByPrincipalID, cur.ConversationID,
		store.EntityKindChatMessage, uint64(id), uint64(cur.ConversationID),
		store.ChangeOpDestroyed, now)
	s.appendFTSChangeLocked(store.FTSChange{
		PrincipalID:    conv.CreatedByPrincipalID,
		Kind:           store.EntityKindChatMessage,
		EntityID:       uint64(id),
		ParentEntityID: uint64(cur.ConversationID),
		Op:             store.ChangeOpDestroyed,
		ProducedAt:     now,
	})
	return nil
}

// -- DM deduplication (re #47) ----------------------------------------

func (m *metaFace) FindDMBetween(ctx context.Context, a, b store.PrincipalID) (store.ChatConversation, []store.ChatMembership, bool, error) {
	if err := ctx.Err(); err != nil {
		return store.ChatConversation{}, nil, false, err
	}
	if a == b {
		return store.ChatConversation{}, nil, false, nil
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.phase2 == nil {
		return store.ChatConversation{}, nil, false, nil
	}
	// Scan conversations for a DM where both a and b are members and
	// there are exactly two members. Return the one with the smallest id.
	var best *store.ChatConversation
	for _, c := range s.phase2.chatConversations {
		if c.Kind != store.ChatConversationKindDM {
			continue
		}
		var hasA, hasB bool
		var count int
		for _, mb := range s.phase2.chatMemberships {
			if mb.ConversationID != c.ID {
				continue
			}
			count++
			if mb.PrincipalID == a {
				hasA = true
			}
			if mb.PrincipalID == b {
				hasB = true
			}
		}
		if !hasA || !hasB || count != 2 {
			continue
		}
		cc := c
		if best == nil || cc.ID < best.ID {
			best = &cc
		}
	}
	if best == nil {
		return store.ChatConversation{}, nil, false, nil
	}
	clone := cloneChatConversation(*best)
	var members []store.ChatMembership
	for _, mb := range s.phase2.chatMemberships {
		if mb.ConversationID == best.ID {
			members = append(members, cloneChatMembership(mb))
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return clone, members, true, nil
}

func (m *metaFace) InsertDMConversation(ctx context.Context, creator, other store.PrincipalID, name string, now time.Time) (store.ChatConversation, []store.ChatMembership, error) {
	if err := ctx.Err(); err != nil {
		return store.ChatConversation{}, nil, err
	}
	if creator == other {
		return store.ChatConversation{}, nil, fmt.Errorf("%w: cannot create a DM with yourself", store.ErrInvalidArgument)
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensurePhase2()
	// Under the write lock there is no real race window: the fakestore
	// serialises all writes, so this check-then-insert is atomic.
	for _, c := range s.phase2.chatConversations {
		if c.Kind != store.ChatConversationKindDM {
			continue
		}
		var hasCreator, hasOther bool
		var count int
		for _, mb := range s.phase2.chatMemberships {
			if mb.ConversationID != c.ID {
				continue
			}
			count++
			if mb.PrincipalID == creator {
				hasCreator = true
			}
			if mb.PrincipalID == other {
				hasOther = true
			}
		}
		if hasCreator && hasOther && count == 2 {
			// DM already exists; return ErrConflict so the handler falls
			// through to FindDMBetween.
			return store.ChatConversation{}, nil, store.ErrConflict
		}
	}
	convID := s.phase2.nextChatConversation
	s.phase2.nextChatConversation++
	c := store.ChatConversation{
		ID:                   convID,
		Kind:                 store.ChatConversationKindDM,
		Name:                 name,
		CreatedByPrincipalID: creator,
		CreatedAt:            now,
		UpdatedAt:            now,
		ReadReceiptsEnabled:  true,
		ModSeq:               1,
	}
	s.phase2.chatConversations[convID] = c
	creatorMemID := s.phase2.nextChatMembership
	s.phase2.nextChatMembership++
	creatorMem := store.ChatMembership{
		ID:                   creatorMemID,
		ConversationID:       convID,
		PrincipalID:          creator,
		Role:                 store.ChatRoleOwner,
		JoinedAt:             now,
		NotificationsSetting: store.ChatNotificationsAll,
		ModSeq:               1,
	}
	s.phase2.chatMemberships[creatorMemID] = creatorMem
	otherMemID := s.phase2.nextChatMembership
	s.phase2.nextChatMembership++
	otherMem := store.ChatMembership{
		ID:                   otherMemID,
		ConversationID:       convID,
		PrincipalID:          other,
		Role:                 store.ChatRoleMember,
		JoinedAt:             now,
		NotificationsSetting: store.ChatNotificationsAll,
		ModSeq:               1,
	}
	s.phase2.chatMemberships[otherMemID] = otherMem
	// Fan state-change rows to both principals.
	for _, pid := range []store.PrincipalID{creator, other} {
		s.appendStateChangeLocked(store.StateChange{
			PrincipalID:    pid,
			Kind:           store.EntityKindConversation,
			EntityID:       uint64(convID),
			ParentEntityID: 0,
			Op:             store.ChangeOpCreated,
			ProducedAt:     now,
		})
	}
	for _, memID := range []store.MembershipID{creatorMemID, otherMemID} {
		for _, pid := range []store.PrincipalID{creator, other} {
			s.appendStateChangeLocked(store.StateChange{
				PrincipalID:    pid,
				Kind:           store.EntityKindMembership,
				EntityID:       uint64(memID),
				ParentEntityID: uint64(convID),
				Op:             store.ChangeOpCreated,
				ProducedAt:     now,
			})
		}
	}
	result := cloneChatConversation(c)
	members := []store.ChatMembership{cloneChatMembership(creatorMem), cloneChatMembership(otherMem)}
	return result, members, nil
}

// -- clone helpers ----------------------------------------------------

func cloneChatConversation(c store.ChatConversation) store.ChatConversation {
	if c.LastMessageAt != nil {
		t := *c.LastMessageAt
		c.LastMessageAt = &t
	}
	if c.RetentionSeconds != nil {
		v := *c.RetentionSeconds
		c.RetentionSeconds = &v
	}
	if c.EditWindowSeconds != nil {
		v := *c.EditWindowSeconds
		c.EditWindowSeconds = &v
	}
	return c
}

func cloneChatMembership(mb store.ChatMembership) store.ChatMembership {
	if mb.LastReadMessageID != nil {
		v := *mb.LastReadMessageID
		mb.LastReadMessageID = &v
	}
	if mb.MuteUntil != nil {
		t := *mb.MuteUntil
		mb.MuteUntil = &t
	}
	return mb
}

func cloneChatMessage(msg store.ChatMessage) store.ChatMessage {
	if msg.SenderPrincipalID != nil {
		v := *msg.SenderPrincipalID
		msg.SenderPrincipalID = &v
	}
	if msg.ReplyToMessageID != nil {
		v := *msg.ReplyToMessageID
		msg.ReplyToMessageID = &v
	}
	if msg.EditedAt != nil {
		t := *msg.EditedAt
		msg.EditedAt = &t
	}
	if msg.DeletedAt != nil {
		t := *msg.DeletedAt
		msg.DeletedAt = &t
	}
	msg.ReactionsJSON = cloneBytes(msg.ReactionsJSON)
	msg.AttachmentsJSON = cloneBytes(msg.AttachmentsJSON)
	msg.MetadataJSON = cloneBytes(msg.MetadataJSON)
	return msg
}
