package storepg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.8 store.Metadata methods for
// the chat subsystem (REQ-CHAT-*) against Postgres. The schema-side
// commentary lives in migrations/0012_chat.sql; the type definitions
// and the shared reaction/attachment validators live in
// internal/store/types_chat.go.

// -- ChatConversation -------------------------------------------------

const chatConversationSelectColumnsPG = `
	id, kind, name, topic, created_by_principal_id,
	created_at_us, updated_at_us, last_message_at_us,
	message_count, is_archived, modseq,
	read_receipts_enabled, retention_seconds, edit_window_seconds`

func scanChatConversationPG(row pgx.Row) (store.ChatConversation, error) {
	var (
		id, createdBy            int64
		createdUs, updatedUs     int64
		lastMsgUs                *int64
		msgCount, modseq         int64
		archived, readReceipts   bool
		retentionSec, editWindow *int64
		kind                     string
		name, topic              *string
	)
	err := row.Scan(&id, &kind, &name, &topic, &createdBy,
		&createdUs, &updatedUs, &lastMsgUs,
		&msgCount, &archived, &modseq,
		&readReceipts, &retentionSec, &editWindow)
	if err != nil {
		return store.ChatConversation{}, mapErr(err)
	}
	c := store.ChatConversation{
		ID:                   store.ConversationID(id),
		Kind:                 kind,
		CreatedByPrincipalID: store.PrincipalID(createdBy),
		CreatedAt:            fromMicros(createdUs),
		UpdatedAt:            fromMicros(updatedUs),
		MessageCount:         int(msgCount),
		IsArchived:           archived,
		ReadReceiptsEnabled:  readReceipts,
		ModSeq:               store.ModSeq(modseq),
	}
	if name != nil {
		c.Name = *name
	}
	if topic != nil {
		c.Topic = *topic
	}
	if lastMsgUs != nil {
		t := fromMicros(*lastMsgUs)
		c.LastMessageAt = &t
	}
	if retentionSec != nil {
		v := *retentionSec
		c.RetentionSeconds = &v
	}
	if editWindow != nil {
		v := *editWindow
		c.EditWindowSeconds = &v
	}
	return c, nil
}

func (m *metadata) InsertChatConversation(ctx context.Context, c store.ChatConversation) (store.ConversationID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var nameArg, topicArg *string
		if c.Name != "" {
			v := c.Name
			nameArg = &v
		}
		if c.Topic != "" {
			v := c.Topic
			topicArg = &v
		}
		var lastMsgArg *int64
		if c.LastMessageAt != nil {
			v := usMicros(c.LastMessageAt.UTC())
			lastMsgArg = &v
		}
		// REQ-CHAT-32: new conversations default to read_receipts on.
		// See storesqlite InsertChatConversation for the rationale (Go
		// bool zero-value vs schema default TRUE).
		var retentionArg, editWindowArg *int64
		if c.RetentionSeconds != nil {
			v := *c.RetentionSeconds
			retentionArg = &v
		}
		if c.EditWindowSeconds != nil {
			v := *c.EditWindowSeconds
			editWindowArg = &v
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO chat_conversations (kind, name, topic,
			  created_by_principal_id, created_at_us, updated_at_us,
			  last_message_at_us, message_count, is_archived, modseq,
			  read_receipts_enabled, retention_seconds, edit_window_seconds)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, TRUE, $10, $11)
			RETURNING id`,
			c.Kind, nameArg, topicArg, int64(c.CreatedByPrincipalID),
			usMicros(now), usMicros(now), lastMsgArg,
			int64(c.MessageCount), c.IsArchived,
			retentionArg, editWindowArg).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		// Fan to all members; at creation time the membership table for
		// this conversation is empty so this falls back to the creator
		// alone (re #47).
		return appendStateChangeForMembersPG(ctx, tx, c.CreatedByPrincipalID,
			store.ConversationID(id),
			store.EntityKindConversation, uint64(id), 0,
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.ConversationID(id), nil
}

func (m *metadata) GetChatConversation(ctx context.Context, id store.ConversationID) (store.ChatConversation, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+chatConversationSelectColumnsPG+` FROM chat_conversations WHERE id = $1`,
		int64(id))
	return scanChatConversationPG(row)
}

func (m *metadata) ListChatConversations(ctx context.Context, filter store.ChatConversationFilter) ([]store.ChatConversation, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	idx := 1
	if filter.Kind != nil {
		clauses = append(clauses, fmt.Sprintf("kind = $%d", idx))
		args = append(args, *filter.Kind)
		idx++
	}
	if filter.CreatedByPrincipalID != nil {
		clauses = append(clauses, fmt.Sprintf("created_by_principal_id = $%d", idx))
		args = append(args, int64(*filter.CreatedByPrincipalID))
		idx++
	}
	if !filter.IncludeArchived {
		clauses = append(clauses, "is_archived = FALSE")
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, fmt.Sprintf("modseq > $%d", idx))
		args = append(args, int64(filter.AfterModSeq))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + chatConversationSelectColumnsPG + ` FROM chat_conversations`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatConversation
	for rows.Next() {
		c, err := scanChatConversationPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateChatConversation(ctx context.Context, c store.ChatConversation) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var owner int64
		err := tx.QueryRow(ctx,
			`SELECT created_by_principal_id FROM chat_conversations WHERE id = $1`,
			int64(c.ID)).Scan(&owner)
		if err != nil {
			return mapErr(err)
		}
		var nameArg, topicArg *string
		if c.Name != "" {
			v := c.Name
			nameArg = &v
		}
		if c.Topic != "" {
			v := c.Topic
			topicArg = &v
		}
		var lastMsgArg *int64
		if c.LastMessageAt != nil {
			v := usMicros(c.LastMessageAt.UTC())
			lastMsgArg = &v
		}
		var retentionArg, editWindowArg *int64
		if c.RetentionSeconds != nil {
			v := *c.RetentionSeconds
			retentionArg = &v
		}
		if c.EditWindowSeconds != nil {
			v := *c.EditWindowSeconds
			editWindowArg = &v
		}
		tag, err := tx.Exec(ctx, `
			UPDATE chat_conversations SET
			  name = $1, topic = $2, last_message_at_us = $3,
			  message_count = $4, is_archived = $5, updated_at_us = $6,
			  read_receipts_enabled = $7, retention_seconds = $8,
			  edit_window_seconds = $9, modseq = modseq + 1
			 WHERE id = $10`,
			nameArg, topicArg, lastMsgArg,
			int64(c.MessageCount), c.IsArchived,
			usMicros(now),
			c.ReadReceiptsEnabled, retentionArg, editWindowArg,
			int64(c.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			c.ID,
			store.EntityKindConversation, uint64(c.ID), 0,
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteChatConversation(ctx context.Context, id store.ConversationID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var owner int64
		err := tx.QueryRow(ctx,
			`SELECT created_by_principal_id FROM chat_conversations WHERE id = $1`,
			int64(id)).Scan(&owner)
		if err != nil {
			return mapErr(err)
		}
		// Capture member principal IDs and child row IDs before the FK
		// cascade wipes them so we can fan out per-member destroyed
		// state-change rows (re #47).
		memberPIDs, err := conversationMemberIDsPG(ctx, tx, id)
		if err != nil {
			return err
		}
		msgRows, err := tx.Query(ctx,
			`SELECT id FROM chat_messages WHERE conversation_id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		var msgIDs []int64
		for msgRows.Next() {
			var v int64
			if err := msgRows.Scan(&v); err != nil {
				msgRows.Close()
				return mapErr(err)
			}
			msgIDs = append(msgIDs, v)
		}
		msgRows.Close()
		memRows, err := tx.Query(ctx,
			`SELECT id FROM chat_memberships WHERE conversation_id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		var memIDs []int64
		for memRows.Next() {
			var v int64
			if err := memRows.Scan(&v); err != nil {
				memRows.Close()
				return mapErr(err)
			}
			memIDs = append(memIDs, v)
		}
		memRows.Close()
		tag, err := tx.Exec(ctx,
			`DELETE FROM chat_conversations WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		// Build the deduplicated audience for all destroy notifications.
		seen := make(map[store.PrincipalID]bool, len(memberPIDs)+1)
		audience := []store.PrincipalID{store.PrincipalID(owner)}
		seen[store.PrincipalID(owner)] = true
		for _, pid := range memberPIDs {
			if !seen[pid] {
				seen[pid] = true
				audience = append(audience, pid)
			}
		}
		for _, mid := range msgIDs {
			for _, pid := range audience {
				if err := appendStateChange(ctx, tx, pid,
					store.EntityKindChatMessage, uint64(mid), uint64(id),
					store.ChangeOpDestroyed, now); err != nil {
					return err
				}
			}
		}
		for _, mid := range memIDs {
			for _, pid := range audience {
				if err := appendStateChange(ctx, tx, pid,
					store.EntityKindMembership, uint64(mid), uint64(id),
					store.ChangeOpDestroyed, now); err != nil {
					return err
				}
			}
		}
		for _, pid := range audience {
			if err := appendStateChange(ctx, tx, pid,
				store.EntityKindConversation, uint64(id), 0,
				store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// -- ChatMembership ---------------------------------------------------

const chatMembershipSelectColumnsPG = `
	id, conversation_id, principal_id, role, joined_at_us,
	last_read_message_id, is_muted, mute_until_us,
	notifications_setting, modseq`

func scanChatMembershipPG(row pgx.Row) (store.ChatMembership, error) {
	var (
		id, convID, pid     int64
		role, notif         string
		joinedUs            int64
		lastRead, muteUntil *int64
		muted               bool
		modseq              int64
	)
	err := row.Scan(&id, &convID, &pid, &role, &joinedUs,
		&lastRead, &muted, &muteUntil, &notif, &modseq)
	if err != nil {
		return store.ChatMembership{}, mapErr(err)
	}
	mem := store.ChatMembership{
		ID:                   store.MembershipID(id),
		ConversationID:       store.ConversationID(convID),
		PrincipalID:          store.PrincipalID(pid),
		Role:                 role,
		JoinedAt:             fromMicros(joinedUs),
		IsMuted:              muted,
		NotificationsSetting: notif,
		ModSeq:               store.ModSeq(modseq),
	}
	if lastRead != nil {
		v := store.ChatMessageID(*lastRead)
		mem.LastReadMessageID = &v
	}
	if muteUntil != nil {
		t := fromMicros(*muteUntil)
		mem.MuteUntil = &t
	}
	return mem, nil
}

func (m *metadata) InsertChatMembership(ctx context.Context, mb store.ChatMembership) (store.MembershipID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var lastReadArg, muteUntilArg *int64
		if mb.LastReadMessageID != nil {
			v := int64(*mb.LastReadMessageID)
			lastReadArg = &v
		}
		if mb.MuteUntil != nil {
			v := usMicros(mb.MuteUntil.UTC())
			muteUntilArg = &v
		}
		notif := mb.NotificationsSetting
		if notif == "" {
			notif = store.ChatNotificationsAll
		}
		joined := mb.JoinedAt
		if joined.IsZero() {
			joined = now
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO chat_memberships (conversation_id, principal_id, role,
			  joined_at_us, last_read_message_id, is_muted, mute_until_us,
			  notifications_setting, modseq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1)
			RETURNING id`,
			int64(mb.ConversationID), int64(mb.PrincipalID), mb.Role,
			usMicros(joined.UTC()), lastReadArg, mb.IsMuted,
			muteUntilArg, notif).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		// Notify the new member via their own membership row and all
		// pre-existing members so their conversation sidebar refreshes
		// without a manual reload (re #47).
		return appendStateChangeForMembersPG(ctx, tx, mb.PrincipalID,
			mb.ConversationID,
			store.EntityKindMembership, uint64(id), uint64(mb.ConversationID),
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.MembershipID(id), nil
}

func (m *metadata) GetChatMembership(ctx context.Context, conversationID store.ConversationID, principalID store.PrincipalID) (store.ChatMembership, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+chatMembershipSelectColumnsPG+`
		   FROM chat_memberships
		  WHERE conversation_id = $1 AND principal_id = $2`,
		int64(conversationID), int64(principalID))
	return scanChatMembershipPG(row)
}

func (m *metadata) ListChatMembershipsByConversation(ctx context.Context, conversationID store.ConversationID) ([]store.ChatMembership, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+chatMembershipSelectColumnsPG+`
		   FROM chat_memberships
		  WHERE conversation_id = $1 ORDER BY id ASC`,
		int64(conversationID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatMembership
	for rows.Next() {
		m, err := scanChatMembershipPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (m *metadata) ListChatMembershipsByPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.ChatMembership, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+chatMembershipSelectColumnsPG+`
		   FROM chat_memberships
		  WHERE principal_id = $1 ORDER BY id ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatMembership
	for rows.Next() {
		m, err := scanChatMembershipPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateChatMembership(ctx context.Context, mb store.ChatMembership) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid, convID int64
		err := tx.QueryRow(ctx,
			`SELECT principal_id, conversation_id FROM chat_memberships WHERE id = $1`,
			int64(mb.ID)).Scan(&pid, &convID)
		if err != nil {
			return mapErr(err)
		}
		var lastReadArg, muteUntilArg *int64
		if mb.LastReadMessageID != nil {
			v := int64(*mb.LastReadMessageID)
			lastReadArg = &v
		}
		if mb.MuteUntil != nil {
			v := usMicros(mb.MuteUntil.UTC())
			muteUntilArg = &v
		}
		notif := mb.NotificationsSetting
		if notif == "" {
			notif = store.ChatNotificationsAll
		}
		tag, err := tx.Exec(ctx, `
			UPDATE chat_memberships SET
			  role = $1, last_read_message_id = $2, is_muted = $3,
			  mute_until_us = $4, notifications_setting = $5,
			  modseq = modseq + 1
			 WHERE id = $6`,
			mb.Role, lastReadArg, mb.IsMuted,
			muteUntilArg, notif, int64(mb.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindMembership, uint64(mb.ID), uint64(convID),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteChatMembership(ctx context.Context, id store.MembershipID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid, convID int64
		err := tx.QueryRow(ctx,
			`SELECT principal_id, conversation_id FROM chat_memberships WHERE id = $1`,
			int64(id)).Scan(&pid, &convID)
		if err != nil {
			return mapErr(err)
		}
		// Capture all current members (including the leaving principal)
		// before the row is removed so we can notify everyone (re #47).
		memberPIDs, err := conversationMemberIDsPG(ctx, tx, store.ConversationID(convID))
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM chat_memberships WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		// Fan the destroy notification to every member that existed before
		// the delete (includes the departing member).
		seen := make(map[store.PrincipalID]bool, len(memberPIDs)+1)
		audience := []store.PrincipalID{store.PrincipalID(pid)}
		seen[store.PrincipalID(pid)] = true
		for _, p := range memberPIDs {
			if !seen[p] {
				seen[p] = true
				audience = append(audience, p)
			}
		}
		for _, p := range audience {
			if err := appendStateChange(ctx, tx, p,
				store.EntityKindMembership, uint64(id), uint64(convID),
				store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// -- ChatMessage ------------------------------------------------------

const chatMessageSelectColumnsPG = `
	id, conversation_id, sender_principal_id, is_system,
	body_text, body_html, body_format, reply_to_message_id,
	reactions_json, attachments_json, metadata_json,
	edited_at_us, deleted_at_us, created_at_us, modseq`

func scanChatMessagePG(row pgx.Row) (store.ChatMessage, error) {
	var (
		id, convID                   int64
		sender                       *int64
		isSystem                     bool
		bodyText, bodyHTML, bodyFmt  *string
		replyTo                      *int64
		reactions, attachments, meta []byte
		editedUs, deletedUs          *int64
		createdUs, modseq            int64
	)
	err := row.Scan(&id, &convID, &sender, &isSystem,
		&bodyText, &bodyHTML, &bodyFmt, &replyTo,
		&reactions, &attachments, &meta,
		&editedUs, &deletedUs, &createdUs, &modseq)
	if err != nil {
		return store.ChatMessage{}, mapErr(err)
	}
	msg := store.ChatMessage{
		ID:              store.ChatMessageID(id),
		ConversationID:  store.ConversationID(convID),
		IsSystem:        isSystem,
		ReactionsJSON:   reactions,
		AttachmentsJSON: attachments,
		MetadataJSON:    meta,
		CreatedAt:       fromMicros(createdUs),
		ModSeq:          store.ModSeq(modseq),
	}
	if sender != nil {
		p := store.PrincipalID(*sender)
		msg.SenderPrincipalID = &p
	}
	if bodyText != nil {
		msg.BodyText = *bodyText
	}
	if bodyHTML != nil {
		msg.BodyHTML = *bodyHTML
	}
	if bodyFmt != nil {
		msg.BodyFormat = *bodyFmt
	}
	if replyTo != nil {
		v := store.ChatMessageID(*replyTo)
		msg.ReplyToMessageID = &v
	}
	if editedUs != nil {
		t := fromMicros(*editedUs)
		msg.EditedAt = &t
	}
	if deletedUs != nil {
		t := fromMicros(*deletedUs)
		msg.DeletedAt = &t
	}
	return msg, nil
}

func (m *metadata) InsertChatMessage(ctx context.Context, msg store.ChatMessage) (store.ChatMessageID, error) {
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
	now := m.s.clock.Now().UTC()
	var id int64
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		var owner int64
		err := tx.QueryRow(ctx,
			`SELECT created_by_principal_id FROM chat_conversations WHERE id = $1`,
			int64(msg.ConversationID)).Scan(&owner)
		if err != nil {
			return mapErr(err)
		}
		var senderArg, replyArg, editedArg, deletedArg *int64
		if msg.SenderPrincipalID != nil {
			v := int64(*msg.SenderPrincipalID)
			senderArg = &v
		}
		if msg.ReplyToMessageID != nil {
			v := int64(*msg.ReplyToMessageID)
			replyArg = &v
		}
		if msg.EditedAt != nil {
			v := usMicros(msg.EditedAt.UTC())
			editedArg = &v
		}
		if msg.DeletedAt != nil {
			v := usMicros(msg.DeletedAt.UTC())
			deletedArg = &v
		}
		bodyFmt := msg.BodyFormat
		if bodyFmt == "" {
			bodyFmt = store.ChatBodyFormatText
		}
		var bodyTextArg, bodyHTMLArg *string
		if msg.BodyText != "" {
			v := msg.BodyText
			bodyTextArg = &v
		}
		if msg.BodyHTML != "" {
			v := msg.BodyHTML
			bodyHTMLArg = &v
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO chat_messages (conversation_id, sender_principal_id,
			  is_system, body_text, body_html, body_format,
			  reply_to_message_id, reactions_json, attachments_json,
			  metadata_json, edited_at_us, deleted_at_us, created_at_us,
			  modseq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1)
			RETURNING id`,
			int64(msg.ConversationID), senderArg, msg.IsSystem,
			bodyTextArg, bodyHTMLArg, bodyFmt, replyArg,
			pgBytesOrNil(msg.ReactionsJSON), pgBytesOrNil(msg.AttachmentsJSON),
			pgBytesOrNil(msg.MetadataJSON), editedArg, deletedArg,
			usMicros(now)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		if msg.DeletedAt == nil {
			if _, err := tx.Exec(ctx, `
				UPDATE chat_conversations SET
				  last_message_at_us = $1, message_count = message_count + 1,
				  updated_at_us = $2, modseq = modseq + 1
				 WHERE id = $3`,
				usMicros(now), usMicros(now), int64(msg.ConversationID)); err != nil {
				return mapErr(err)
			}
		}
		// Increment blob_refs.ref_count for each distinct attachment
		// hash atomically with the chat_messages insert. Mirrors the
		// mail-side InsertMessage path.
		for _, a := range attHashes {
			if err := incRef(ctx, tx, a.Hash, a.Size, now); err != nil {
				return err
			}
		}
		// Fan the state-change to every current member so all participants
		// see the new message without a manual reload (re #47).
		if err := appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			msg.ConversationID,
			store.EntityKindChatMessage, uint64(id), uint64(msg.ConversationID),
			store.ChangeOpCreated, now); err != nil {
			return err
		}
		// Also fan a Conversation Updated event so the unreadCount and
		// lastMessageAt visible to each member move in lockstep with the
		// new message (re #47).
		return appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			msg.ConversationID,
			store.EntityKindConversation, uint64(msg.ConversationID), uint64(msg.ConversationID),
			store.ChangeOpUpdated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.ChatMessageID(id), nil
}

func (m *metadata) GetChatMessage(ctx context.Context, id store.ChatMessageID) (store.ChatMessage, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+chatMessageSelectColumnsPG+` FROM chat_messages WHERE id = $1`,
		int64(id))
	return scanChatMessagePG(row)
}

func (m *metadata) ListChatMessages(ctx context.Context, filter store.ChatMessageFilter) ([]store.ChatMessage, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	idx := 1
	if filter.ConversationID != nil {
		clauses = append(clauses, fmt.Sprintf("conversation_id = $%d", idx))
		args = append(args, int64(*filter.ConversationID))
		idx++
	}
	if filter.SenderPrincipalID != nil {
		clauses = append(clauses, fmt.Sprintf("sender_principal_id = $%d", idx))
		args = append(args, int64(*filter.SenderPrincipalID))
		idx++
	}
	if !filter.IncludeDeleted {
		clauses = append(clauses, "deleted_at_us IS NULL")
	}
	if filter.CreatedAfter != nil {
		clauses = append(clauses, fmt.Sprintf("created_at_us > $%d", idx))
		args = append(args, usMicros(filter.CreatedAfter.UTC()))
		idx++
	}
	if filter.CreatedBefore != nil {
		clauses = append(clauses, fmt.Sprintf("created_at_us < $%d", idx))
		args = append(args, usMicros(filter.CreatedBefore.UTC()))
		idx++
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, fmt.Sprintf("modseq > $%d", idx))
		args = append(args, int64(filter.AfterModSeq))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + chatMessageSelectColumnsPG + ` FROM chat_messages`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatMessage
	for rows.Next() {
		msg, err := scanChatMessagePG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateChatMessage(ctx context.Context, msg store.ChatMessage) error {
	if err := store.ChatValidateReactions(msg.ReactionsJSON); err != nil {
		return err
	}
	if err := store.ChatValidateAttachments(msg.AttachmentsJSON); err != nil {
		return err
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var convID, owner int64
		err := tx.QueryRow(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = $1`, int64(msg.ID)).Scan(&convID, &owner)
		if err != nil {
			return mapErr(err)
		}
		var editedArg, deletedArg *int64
		if msg.EditedAt != nil {
			v := usMicros(msg.EditedAt.UTC())
			editedArg = &v
		}
		if msg.DeletedAt != nil {
			v := usMicros(msg.DeletedAt.UTC())
			deletedArg = &v
		}
		bodyFmt := msg.BodyFormat
		if bodyFmt == "" {
			bodyFmt = store.ChatBodyFormatText
		}
		var bodyTextArg, bodyHTMLArg *string
		if msg.BodyText != "" {
			v := msg.BodyText
			bodyTextArg = &v
		}
		if msg.BodyHTML != "" {
			v := msg.BodyHTML
			bodyHTMLArg = &v
		}
		tag, err := tx.Exec(ctx, `
			UPDATE chat_messages SET
			  body_text = $1, body_html = $2, body_format = $3,
			  reactions_json = $4, attachments_json = $5, metadata_json = $6,
			  edited_at_us = $7, deleted_at_us = $8,
			  modseq = modseq + 1
			 WHERE id = $9`,
			bodyTextArg, bodyHTMLArg, bodyFmt,
			pgBytesOrNil(msg.ReactionsJSON), pgBytesOrNil(msg.AttachmentsJSON),
			pgBytesOrNil(msg.MetadataJSON), editedArg, deletedArg,
			int64(msg.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			store.ConversationID(convID),
			store.EntityKindChatMessage, uint64(msg.ID), uint64(convID),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) SoftDeleteChatMessage(ctx context.Context, id store.ChatMessageID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var convID, owner int64
		var alreadyDeleted *int64
		err := tx.QueryRow(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id, cm.deleted_at_us
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = $1`, int64(id)).Scan(&convID, &owner, &alreadyDeleted)
		if err != nil {
			return mapErr(err)
		}
		if alreadyDeleted != nil {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE chat_messages SET
			  body_text = NULL, body_html = NULL,
			  deleted_at_us = $1, modseq = modseq + 1
			 WHERE id = $2`, usMicros(now), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `
			UPDATE chat_conversations SET
			  message_count = GREATEST(message_count - 1, 0),
			  updated_at_us = $1, modseq = modseq + 1
			 WHERE id = $2`, usMicros(now), convID); err != nil {
			return mapErr(err)
		}
		return appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			store.ConversationID(convID),
			store.EntityKindChatMessage, uint64(id), uint64(convID),
			store.ChangeOpUpdated, now)
	})
}

// -- Reactions --------------------------------------------------------

func (m *metadata) SetChatReaction(ctx context.Context, msgID store.ChatMessageID, emoji string, principalID store.PrincipalID, present bool) error {
	if err := store.ChatValidateEmoji(emoji); err != nil {
		return err
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var convID, owner int64
		var reactions []byte
		err := tx.QueryRow(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id, cm.reactions_json
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = $1`, int64(msgID)).Scan(&convID, &owner, &reactions)
		if err != nil {
			return mapErr(err)
		}
		updated, changed, err := store.ChatApplyReaction(reactions, emoji, principalID, present)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE chat_messages SET reactions_json = $1, modseq = modseq + 1
			 WHERE id = $2`, pgBytesOrNil(updated), int64(msgID)); err != nil {
			return mapErr(err)
		}
		return appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			store.ConversationID(convID),
			store.EntityKindChatMessage, uint64(msgID), uint64(convID),
			store.ChangeOpUpdated, now)
	})
}

// -- ChatBlock --------------------------------------------------------

func (m *metadata) InsertChatBlock(ctx context.Context, b store.ChatBlock) error {
	if b.BlockerPrincipalID == b.BlockedPrincipalID {
		return fmt.Errorf("%w: blocker and blocked principals must differ", store.ErrInvalidArgument)
	}
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var reason *string
		if b.Reason != "" {
			v := b.Reason
			reason = &v
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO chat_blocks (blocker_principal_id, blocked_principal_id,
			  created_at_us, reason)
			VALUES ($1, $2, $3, $4)`,
			int64(b.BlockerPrincipalID), int64(b.BlockedPrincipalID),
			usMicros(now), reason)
		return mapErr(err)
	})
}

func (m *metadata) DeleteChatBlock(ctx context.Context, blocker, blocked store.PrincipalID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM chat_blocks
			 WHERE blocker_principal_id = $1 AND blocked_principal_id = $2`,
			int64(blocker), int64(blocked))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListChatBlocksBy(ctx context.Context, blocker store.PrincipalID) ([]store.ChatBlock, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT blocker_principal_id, blocked_principal_id, created_at_us, reason
		  FROM chat_blocks WHERE blocker_principal_id = $1
		 ORDER BY blocked_principal_id ASC`, int64(blocker))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatBlock
	for rows.Next() {
		var (
			blockerID, blockedID, createdUs int64
			reason                          *string
		)
		if err := rows.Scan(&blockerID, &blockedID, &createdUs, &reason); err != nil {
			return nil, mapErr(err)
		}
		b := store.ChatBlock{
			BlockerPrincipalID: store.PrincipalID(blockerID),
			BlockedPrincipalID: store.PrincipalID(blockedID),
			CreatedAt:          fromMicros(createdUs),
		}
		if reason != nil {
			b.Reason = *reason
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (m *metadata) IsBlocked(ctx context.Context, blocker, blocked store.PrincipalID) (bool, error) {
	var one int64
	err := m.s.pool.QueryRow(ctx, `
		SELECT 1 FROM chat_blocks
		 WHERE blocker_principal_id = $1 AND blocked_principal_id = $2
		 LIMIT 1`, int64(blocker), int64(blocked)).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return true, nil
}

// -- Read pointer -----------------------------------------------------

func (m *metadata) LastReadAt(ctx context.Context, principalID store.PrincipalID, conversationID store.ConversationID) (*store.ChatMessageID, time.Time, error) {
	var (
		lastRead *int64
		joinedUs int64
	)
	err := m.s.pool.QueryRow(ctx, `
		SELECT last_read_message_id, joined_at_us
		  FROM chat_memberships
		 WHERE conversation_id = $1 AND principal_id = $2`,
		int64(conversationID), int64(principalID)).Scan(&lastRead, &joinedUs)
	if err != nil {
		return nil, time.Time{}, mapErr(err)
	}
	var out *store.ChatMessageID
	if lastRead != nil {
		v := store.ChatMessageID(*lastRead)
		out = &v
	}
	return out, fromMicros(joinedUs), nil
}

func (m *metadata) SetLastRead(ctx context.Context, principalID store.PrincipalID, conversationID store.ConversationID, msgID store.ChatMessageID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var memID int64
		err := tx.QueryRow(ctx, `
			SELECT id FROM chat_memberships
			 WHERE conversation_id = $1 AND principal_id = $2`,
			int64(conversationID), int64(principalID)).Scan(&memID)
		if err != nil {
			return mapErr(err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE chat_memberships SET
			  last_read_message_id = $1, modseq = modseq + 1
			 WHERE id = $2`, int64(msgID), memID)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, principalID,
			store.EntityKindMembership, uint64(memID), uint64(conversationID),
			store.ChangeOpUpdated, now)
	})
}

// -- Account-default settings (Phase 2 Wave 2.9.6 REQ-CHAT-20/92) ---

func (m *metadata) GetChatAccountSettings(ctx context.Context, principalID store.PrincipalID) (store.ChatAccountSettings, error) {
	var (
		pid                  int64
		retention, editWin   int64
		createdUs, updatedUs int64
	)
	err := m.s.pool.QueryRow(ctx, `
		SELECT principal_id, default_retention_seconds, default_edit_window_seconds,
		       created_at_us, updated_at_us
		  FROM chat_account_settings WHERE principal_id = $1`, int64(principalID)).
		Scan(&pid, &retention, &editWin, &createdUs, &updatedUs)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// REQ-CHAT-20 / REQ-CHAT-92: implicit defaults when no row
			// has been persisted yet.
			return store.ChatAccountSettings{
				PrincipalID:              principalID,
				DefaultRetentionSeconds:  store.ChatDefaultRetentionSeconds,
				DefaultEditWindowSeconds: store.ChatDefaultEditWindowSeconds,
			}, nil
		}
		return store.ChatAccountSettings{}, mapErr(err)
	}
	return store.ChatAccountSettings{
		PrincipalID:              store.PrincipalID(pid),
		DefaultRetentionSeconds:  retention,
		DefaultEditWindowSeconds: editWin,
		CreatedAt:                fromMicros(createdUs),
		UpdatedAt:                fromMicros(updatedUs),
	}, nil
}

func (m *metadata) UpsertChatAccountSettings(ctx context.Context, settings store.ChatAccountSettings) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO chat_account_settings (principal_id,
			  default_retention_seconds, default_edit_window_seconds,
			  created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (principal_id) DO UPDATE SET
			  default_retention_seconds = EXCLUDED.default_retention_seconds,
			  default_edit_window_seconds = EXCLUDED.default_edit_window_seconds,
			  updated_at_us = EXCLUDED.updated_at_us`,
			int64(settings.PrincipalID),
			settings.DefaultRetentionSeconds,
			settings.DefaultEditWindowSeconds,
			usMicros(now), usMicros(now))
		return mapErr(err)
	})
}

func (m *metadata) ListChatAccountSettingsForRetention(ctx context.Context, afterID store.PrincipalID, limit int) ([]store.ChatAccountSettings, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT principal_id, default_retention_seconds, default_edit_window_seconds,
		       created_at_us, updated_at_us
		  FROM chat_account_settings
		 WHERE default_retention_seconds > 0 AND principal_id > $1
		 ORDER BY principal_id ASC LIMIT $2`,
		int64(afterID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatAccountSettings
	for rows.Next() {
		var (
			pid                  int64
			retention, editWin   int64
			createdUs, updatedUs int64
		)
		if err := rows.Scan(&pid, &retention, &editWin, &createdUs, &updatedUs); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.ChatAccountSettings{
			PrincipalID:              store.PrincipalID(pid),
			DefaultRetentionSeconds:  retention,
			DefaultEditWindowSeconds: editWin,
			CreatedAt:                fromMicros(createdUs),
			UpdatedAt:                fromMicros(updatedUs),
		})
	}
	return out, rows.Err()
}

// -- Retention helpers (Phase 2 Wave 2.9.6 REQ-CHAT-92) -------------

func (m *metadata) ListChatConversationsForRetention(ctx context.Context, afterID store.ConversationID, limit int) ([]store.ChatConversation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+chatConversationSelectColumnsPG+`
		   FROM chat_conversations
		  WHERE retention_seconds IS NOT NULL AND retention_seconds > 0
		    AND id > $1
		  ORDER BY id ASC LIMIT $2`,
		int64(afterID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatConversation
	for rows.Next() {
		c, err := scanChatConversationPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) HardDeleteChatMessage(ctx context.Context, id store.ChatMessageID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var convID, owner int64
		var atts []byte
		err := tx.QueryRow(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id, cm.attachments_json
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = $1`, int64(id)).Scan(&convID, &owner, &atts)
		if err != nil {
			return mapErr(err)
		}
		// Decrement blob_refs.ref_count for each distinct attachment
		// hash atomically with the row delete. The blob-store sweeper
		// evicts blob_refs rows whose ref_count <= 0 out-of-band
		// (REQ-STORE-12 grace window).
		hashes, err := store.ChatAttachmentHashes(atts)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM chat_messages WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		for _, a := range hashes {
			if err := decRef(ctx, tx, a.Hash, now); err != nil {
				return err
			}
		}
		// Recompute the conversation's denormalised counters from the
		// surviving live rows.
		var (
			liveCount int64
			lastUs    *int64
		)
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*), MAX(created_at_us)
			  FROM chat_messages
			 WHERE conversation_id = $1 AND deleted_at_us IS NULL`,
			convID).Scan(&liveCount, &lastUs); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE chat_conversations SET
			  message_count = $1, last_message_at_us = $2,
			  updated_at_us = $3, modseq = modseq + 1
			 WHERE id = $4`,
			liveCount, lastUs, usMicros(now), convID); err != nil {
			return mapErr(err)
		}
		return appendStateChangeForMembersPG(ctx, tx, store.PrincipalID(owner),
			store.ConversationID(convID),
			store.EntityKindChatMessage, uint64(id), uint64(convID),
			store.ChangeOpDestroyed, now)
	})
}

// -- DM deduplication (re #47) ----------------------------------------

func (m *metadata) FindDMBetween(ctx context.Context, a, b store.PrincipalID) (store.ChatConversation, []store.ChatMembership, bool, error) {
	if a == b {
		return store.ChatConversation{}, nil, false, nil
	}
	pidLo, pidHi := dmPairNormalizePG(a, b)
	// Fast path: the chat_dm_pairs index (populated for DMs created after
	// migration 0034). O(1) primary key lookup.
	var convID int64
	err := m.s.pool.QueryRow(ctx,
		`SELECT conversation_id FROM chat_dm_pairs WHERE pid_lo = $1 AND pid_hi = $2`,
		pidLo, pidHi).Scan(&convID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return store.ChatConversation{}, nil, false, mapErr(err)
	}
	if err == nil {
		c, merr := m.GetChatConversation(ctx, store.ConversationID(convID))
		if merr != nil {
			return store.ChatConversation{}, nil, false, merr
		}
		members, merr := m.ListChatMembershipsByConversation(ctx, c.ID)
		if merr != nil {
			return store.ChatConversation{}, nil, false, merr
		}
		return c, members, true, nil
	}
	// Slow path: membership JOIN for pre-migration DM rows.
	row := m.s.pool.QueryRow(ctx, `
		SELECT `+chatConversationSelectColumnsPG+`
		  FROM chat_conversations c
		 WHERE c.kind = 'dm'
		   AND EXISTS (SELECT 1 FROM chat_memberships WHERE conversation_id = c.id AND principal_id = $1)
		   AND EXISTS (SELECT 1 FROM chat_memberships WHERE conversation_id = c.id AND principal_id = $2)
		   AND (SELECT COUNT(*) FROM chat_memberships WHERE conversation_id = c.id) = 2
		 ORDER BY c.id ASC
		 LIMIT 1`,
		int64(a), int64(b))
	c, err := scanChatConversationPG(row)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ChatConversation{}, nil, false, nil
		}
		return store.ChatConversation{}, nil, false, err
	}
	members, err := m.ListChatMembershipsByConversation(ctx, c.ID)
	if err != nil {
		return store.ChatConversation{}, nil, false, err
	}
	return c, members, true, nil
}

func (m *metadata) InsertDMConversation(ctx context.Context, creator, other store.PrincipalID, name string, now time.Time) (store.ChatConversation, []store.ChatMembership, error) {
	if creator == other {
		return store.ChatConversation{}, nil, fmt.Errorf("%w: cannot create a DM with yourself", store.ErrInvalidArgument)
	}
	var convID int64
	var creatorMemID, otherMemID int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		pidLo, pidHi := dmPairNormalizePG(creator, other)
		// Check for existing pair in the same tx (holds a row-level lock
		// in Postgres REPEATABLE READ / SERIALIZABLE; in READ COMMITTED
		// the subsequent INSERT on chat_dm_pairs fires the unique
		// constraint as the safety net).
		var existingConvID int64
		chkErr := tx.QueryRow(ctx,
			`SELECT conversation_id FROM chat_dm_pairs WHERE pid_lo = $1 AND pid_hi = $2`,
			pidLo, pidHi).Scan(&existingConvID)
		if chkErr != nil && !errors.Is(chkErr, pgx.ErrNoRows) {
			return mapErr(chkErr)
		}
		if chkErr == nil {
			return store.ErrConflict
		}
		var nameArg *string
		if name != "" {
			v := name
			nameArg = &v
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO chat_conversations (kind, name, topic,
			  created_by_principal_id, created_at_us, updated_at_us,
			  last_message_at_us, message_count, is_archived, modseq,
			  read_receipts_enabled, retention_seconds, edit_window_seconds)
			VALUES ('dm', $1, NULL, $2, $3, $4, NULL, 0, FALSE, 1, TRUE, NULL, NULL)
			RETURNING id`,
			nameArg, int64(creator), usMicros(now), usMicros(now)).Scan(&convID)
		if err != nil {
			return mapErr(err)
		}
		// Insert chat_dm_pairs uniqueness row; unique constraint fires on race.
		if _, err := tx.Exec(ctx,
			`INSERT INTO chat_dm_pairs (pid_lo, pid_hi, conversation_id) VALUES ($1, $2, $3)`,
			pidLo, pidHi, convID); err != nil {
			return mapErr(err)
		}
		// Creator membership (owner).
		err = tx.QueryRow(ctx, `
			INSERT INTO chat_memberships (conversation_id, principal_id, role,
			  joined_at_us, last_read_message_id, is_muted, mute_until_us,
			  notifications_setting, modseq)
			VALUES ($1, $2, 'owner', $3, NULL, FALSE, NULL, 'all', 1)
			RETURNING id`,
			convID, int64(creator), usMicros(now)).Scan(&creatorMemID)
		if err != nil {
			return mapErr(err)
		}
		// Other member membership.
		err = tx.QueryRow(ctx, `
			INSERT INTO chat_memberships (conversation_id, principal_id, role,
			  joined_at_us, last_read_message_id, is_muted, mute_until_us,
			  notifications_setting, modseq)
			VALUES ($1, $2, 'member', $3, NULL, FALSE, NULL, 'all', 1)
			RETURNING id`,
			convID, int64(other), usMicros(now)).Scan(&otherMemID)
		if err != nil {
			return mapErr(err)
		}
		// Fan state-change rows to both principals.
		for _, pid := range []store.PrincipalID{creator, other} {
			if err := appendStateChange(ctx, tx, pid,
				store.EntityKindConversation, uint64(convID), 0,
				store.ChangeOpCreated, now); err != nil {
				return err
			}
		}
		for _, memID := range []int64{creatorMemID, otherMemID} {
			for _, pid := range []store.PrincipalID{creator, other} {
				if err := appendStateChange(ctx, tx, pid,
					store.EntityKindMembership, uint64(memID), uint64(convID),
					store.ChangeOpCreated, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return store.ChatConversation{}, nil, err
	}
	c, err := m.GetChatConversation(ctx, store.ConversationID(convID))
	if err != nil {
		return store.ChatConversation{}, nil, err
	}
	members, err := m.ListChatMembershipsByConversation(ctx, c.ID)
	if err != nil {
		return store.ChatConversation{}, nil, err
	}
	return c, members, nil
}

// dmPairNormalizePG returns (pidLo, pidHi) with pidLo < pidHi.
func dmPairNormalizePG(a, b store.PrincipalID) (pidLo, pidHi int64) {
	if a < b {
		return int64(a), int64(b)
	}
	return int64(b), int64(a)
}

// -- Helpers ----------------------------------------------------------

// conversationMemberIDsPG returns the principal_id of every current member
// of a conversation, in ascending ID order, within the same tx. Used to
// fan out state-change rows to all members rather than just the
// conversation creator (re #47).
func conversationMemberIDsPG(ctx context.Context, tx pgx.Tx, convID store.ConversationID) ([]store.PrincipalID, error) {
	rows, err := tx.Query(ctx,
		`SELECT principal_id FROM chat_memberships WHERE conversation_id = $1 ORDER BY id ASC`,
		int64(convID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.PrincipalID
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.PrincipalID(pid))
	}
	return out, rows.Err()
}

// appendStateChangeForMembersPG fans a single logical state-change out to
// every current member of the conversation. The owner principal always
// receives a row regardless of membership (re #47).
func appendStateChangeForMembersPG(
	ctx context.Context, tx pgx.Tx, owner store.PrincipalID,
	convID store.ConversationID,
	kind store.EntityKind, entityID uint64, parentEntityID uint64,
	op store.ChangeOp, now time.Time,
) error {
	members, err := conversationMemberIDsPG(ctx, tx, convID)
	if err != nil {
		return err
	}
	seen := make(map[store.PrincipalID]bool, len(members)+1)
	ordered := []store.PrincipalID{owner}
	seen[owner] = true
	for _, pid := range members {
		if !seen[pid] {
			seen[pid] = true
			ordered = append(ordered, pid)
		}
	}
	for _, pid := range ordered {
		if err := appendStateChange(ctx, tx, pid, kind, entityID, parentEntityID, op, now); err != nil {
			return err
		}
	}
	return nil
}

// pgBytesOrNil returns nil for an empty byte slice so the binding
// layer writes SQL NULL into the bytea column.
func pgBytesOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
