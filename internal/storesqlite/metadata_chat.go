package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.8 store.Metadata methods for
// the chat subsystem (REQ-CHAT-*). The schema-side commentary lives in
// migrations/0012_chat.sql; the type definitions and the shared
// reaction/attachment validators live in
// internal/store/types_chat.go.

// -- ChatConversation -------------------------------------------------

const chatConversationSelectColumns = `
	id, kind, name, topic, created_by_principal_id,
	created_at_us, updated_at_us, last_message_at_us,
	message_count, is_archived, modseq,
	read_receipts_enabled, retention_seconds, edit_window_seconds`

func scanChatConversation(row rowLike) (store.ChatConversation, error) {
	var (
		id, createdBy              int64
		createdUs, updatedUs       int64
		lastMsgUs                  sql.NullInt64
		msgCount, archived, modseq int64
		readReceipts               int64
		retentionSec, editWindow   sql.NullInt64
		kind                       string
		name, topic                sql.NullString
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
		IsArchived:           archived != 0,
		ReadReceiptsEnabled:  readReceipts != 0,
		ModSeq:               store.ModSeq(modseq),
	}
	if name.Valid {
		c.Name = name.String
	}
	if topic.Valid {
		c.Topic = topic.String
	}
	if lastMsgUs.Valid {
		t := fromMicros(lastMsgUs.Int64)
		c.LastMessageAt = &t
	}
	if retentionSec.Valid {
		v := retentionSec.Int64
		c.RetentionSeconds = &v
	}
	if editWindow.Valid {
		v := editWindow.Int64
		c.EditWindowSeconds = &v
	}
	return c, nil
}

func (m *metadata) InsertChatConversation(ctx context.Context, c store.ChatConversation) (store.ConversationID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var nameArg, topicArg any
		if c.Name != "" {
			nameArg = c.Name
		}
		if c.Topic != "" {
			topicArg = c.Topic
		}
		var lastMsgArg any
		if c.LastMessageAt != nil {
			lastMsgArg = usMicros(c.LastMessageAt.UTC())
		}
		// REQ-CHAT-32: new conversations default to read_receipts on.
		// The Go-side bool zero-value is false, so we pin it to true at
		// insert time and let callers opt out via UpdateChatConversation
		// once the row exists. (DMs ignore the flag at the JMAP layer
		// per REQ-CHAT-31; Spaces consult it.)
		readReceiptsArg := int64(1)
		var retentionArg, editWindowArg any
		if c.RetentionSeconds != nil {
			retentionArg = *c.RetentionSeconds
		}
		if c.EditWindowSeconds != nil {
			editWindowArg = *c.EditWindowSeconds
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chat_conversations (kind, name, topic,
			  created_by_principal_id, created_at_us, updated_at_us,
			  last_message_at_us, message_count, is_archived, modseq,
			  read_receipts_enabled, retention_seconds, edit_window_seconds)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
			c.Kind, nameArg, topicArg, int64(c.CreatedByPrincipalID),
			usMicros(now), usMicros(now), lastMsgArg,
			int64(c.MessageCount), boolToInt(c.IsArchived),
			readReceiptsArg, retentionArg, editWindowArg)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return appendStateChange(ctx, tx, c.CreatedByPrincipalID,
			store.EntityKindConversation, uint64(id), 0,
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.ConversationID(id), nil
}

func (m *metadata) GetChatConversation(ctx context.Context, id store.ConversationID) (store.ChatConversation, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+chatConversationSelectColumns+` FROM chat_conversations WHERE id = ?`,
		int64(id))
	return scanChatConversation(row)
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
	if filter.Kind != nil {
		clauses = append(clauses, "kind = ?")
		args = append(args, *filter.Kind)
	}
	if filter.CreatedByPrincipalID != nil {
		clauses = append(clauses, "created_by_principal_id = ?")
		args = append(args, int64(*filter.CreatedByPrincipalID))
	}
	if !filter.IncludeArchived {
		clauses = append(clauses, "is_archived = 0")
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, "modseq > ?")
		args = append(args, int64(filter.AfterModSeq))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + chatConversationSelectColumns + ` FROM chat_conversations`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatConversation
	for rows.Next() {
		c, err := scanChatConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateChatConversation(ctx context.Context, c store.ChatConversation) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var owner int64
		err := tx.QueryRowContext(ctx,
			`SELECT created_by_principal_id FROM chat_conversations WHERE id = ?`,
			int64(c.ID)).Scan(&owner)
		if err != nil {
			return mapErr(err)
		}
		var nameArg, topicArg any
		if c.Name != "" {
			nameArg = c.Name
		}
		if c.Topic != "" {
			topicArg = c.Topic
		}
		var lastMsgArg any
		if c.LastMessageAt != nil {
			lastMsgArg = usMicros(c.LastMessageAt.UTC())
		}
		var retentionArg, editWindowArg any
		if c.RetentionSeconds != nil {
			retentionArg = *c.RetentionSeconds
		}
		if c.EditWindowSeconds != nil {
			editWindowArg = *c.EditWindowSeconds
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE chat_conversations SET
			  name = ?, topic = ?, last_message_at_us = ?,
			  message_count = ?, is_archived = ?, updated_at_us = ?,
			  read_receipts_enabled = ?, retention_seconds = ?,
			  edit_window_seconds = ?, modseq = modseq + 1
			 WHERE id = ?`,
			nameArg, topicArg, lastMsgArg,
			int64(c.MessageCount), boolToInt(c.IsArchived),
			usMicros(now),
			boolToInt(c.ReadReceiptsEnabled),
			retentionArg, editWindowArg,
			int64(c.ID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
			store.EntityKindConversation, uint64(c.ID), 0,
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteChatConversation(ctx context.Context, id store.ConversationID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var owner int64
		err := tx.QueryRowContext(ctx,
			`SELECT created_by_principal_id FROM chat_conversations WHERE id = ?`,
			int64(id)).Scan(&owner)
		if err != nil {
			return mapErr(err)
		}
		// Capture child IDs before the FK cascade wipes them so we
		// can append per-child destroyed state-change rows.
		msgRows, err := tx.QueryContext(ctx,
			`SELECT id FROM chat_messages WHERE conversation_id = ?`, int64(id))
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
		memRows, err := tx.QueryContext(ctx,
			`SELECT id FROM chat_memberships WHERE conversation_id = ?`, int64(id))
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
		res, err := tx.ExecContext(ctx,
			`DELETE FROM chat_conversations WHERE id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		for _, mid := range msgIDs {
			if err := appendStateChange(ctx, tx, store.PrincipalID(owner),
				store.EntityKindChatMessage, uint64(mid), uint64(id),
				store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		for _, mid := range memIDs {
			if err := appendStateChange(ctx, tx, store.PrincipalID(owner),
				store.EntityKindMembership, uint64(mid), uint64(id),
				store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
			store.EntityKindConversation, uint64(id), 0,
			store.ChangeOpDestroyed, now)
	})
}

// -- ChatMembership ---------------------------------------------------

const chatMembershipSelectColumns = `
	id, conversation_id, principal_id, role, joined_at_us,
	last_read_message_id, is_muted, mute_until_us,
	notifications_setting, modseq`

func scanChatMembership(row rowLike) (store.ChatMembership, error) {
	var (
		id, convID, pid int64
		role, notif     string
		joinedUs        int64
		lastRead        sql.NullInt64
		muted           int64
		muteUntil       sql.NullInt64
		modseq          int64
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
		IsMuted:              muted != 0,
		NotificationsSetting: notif,
		ModSeq:               store.ModSeq(modseq),
	}
	if lastRead.Valid {
		v := store.ChatMessageID(lastRead.Int64)
		mem.LastReadMessageID = &v
	}
	if muteUntil.Valid {
		t := fromMicros(muteUntil.Int64)
		mem.MuteUntil = &t
	}
	return mem, nil
}

func (m *metadata) InsertChatMembership(ctx context.Context, mb store.ChatMembership) (store.MembershipID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var lastReadArg, muteUntilArg any
		if mb.LastReadMessageID != nil {
			lastReadArg = int64(*mb.LastReadMessageID)
		}
		if mb.MuteUntil != nil {
			muteUntilArg = usMicros(mb.MuteUntil.UTC())
		}
		notif := mb.NotificationsSetting
		if notif == "" {
			notif = store.ChatNotificationsAll
		}
		joined := mb.JoinedAt
		if joined.IsZero() {
			joined = now
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chat_memberships (conversation_id, principal_id, role,
			  joined_at_us, last_read_message_id, is_muted, mute_until_us,
			  notifications_setting, modseq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			int64(mb.ConversationID), int64(mb.PrincipalID), mb.Role,
			usMicros(joined.UTC()), lastReadArg, boolToInt(mb.IsMuted),
			muteUntilArg, notif)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return appendStateChange(ctx, tx, mb.PrincipalID,
			store.EntityKindMembership, uint64(id), uint64(mb.ConversationID),
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.MembershipID(id), nil
}

func (m *metadata) GetChatMembership(ctx context.Context, conversationID store.ConversationID, principalID store.PrincipalID) (store.ChatMembership, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+chatMembershipSelectColumns+`
		   FROM chat_memberships
		  WHERE conversation_id = ? AND principal_id = ?`,
		int64(conversationID), int64(principalID))
	return scanChatMembership(row)
}

func (m *metadata) ListChatMembershipsByConversation(ctx context.Context, conversationID store.ConversationID) ([]store.ChatMembership, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+chatMembershipSelectColumns+`
		   FROM chat_memberships
		  WHERE conversation_id = ? ORDER BY id ASC`,
		int64(conversationID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatMembership
	for rows.Next() {
		m, err := scanChatMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (m *metadata) ListChatMembershipsByPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.ChatMembership, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+chatMembershipSelectColumns+`
		   FROM chat_memberships
		  WHERE principal_id = ? ORDER BY id ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatMembership
	for rows.Next() {
		m, err := scanChatMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateChatMembership(ctx context.Context, mb store.ChatMembership) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid, convID int64
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, conversation_id FROM chat_memberships WHERE id = ?`,
			int64(mb.ID)).Scan(&pid, &convID)
		if err != nil {
			return mapErr(err)
		}
		var lastReadArg, muteUntilArg any
		if mb.LastReadMessageID != nil {
			lastReadArg = int64(*mb.LastReadMessageID)
		}
		if mb.MuteUntil != nil {
			muteUntilArg = usMicros(mb.MuteUntil.UTC())
		}
		notif := mb.NotificationsSetting
		if notif == "" {
			notif = store.ChatNotificationsAll
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE chat_memberships SET
			  role = ?, last_read_message_id = ?, is_muted = ?,
			  mute_until_us = ?, notifications_setting = ?,
			  modseq = modseq + 1
			 WHERE id = ?`,
			mb.Role, lastReadArg, boolToInt(mb.IsMuted),
			muteUntilArg, notif, int64(mb.ID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindMembership, uint64(mb.ID), uint64(convID),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteChatMembership(ctx context.Context, id store.MembershipID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid, convID int64
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, conversation_id FROM chat_memberships WHERE id = ?`,
			int64(id)).Scan(&pid, &convID)
		if err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM chat_memberships WHERE id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindMembership, uint64(id), uint64(convID),
			store.ChangeOpDestroyed, now)
	})
}

// -- ChatMessage ------------------------------------------------------

const chatMessageSelectColumns = `
	id, conversation_id, sender_principal_id, is_system,
	body_text, body_html, body_format, reply_to_message_id,
	reactions_json, attachments_json, metadata_json,
	edited_at_us, deleted_at_us, created_at_us, modseq`

func scanChatMessage(row rowLike) (store.ChatMessage, error) {
	var (
		id, convID                   int64
		sender                       sql.NullInt64
		isSystem                     int64
		bodyText, bodyHTML, bodyFmt  sql.NullString
		replyTo                      sql.NullInt64
		reactions, attachments, meta []byte
		editedUs, deletedUs          sql.NullInt64
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
		IsSystem:        isSystem != 0,
		ReactionsJSON:   reactions,
		AttachmentsJSON: attachments,
		MetadataJSON:    meta,
		CreatedAt:       fromMicros(createdUs),
		ModSeq:          store.ModSeq(modseq),
	}
	if sender.Valid {
		p := store.PrincipalID(sender.Int64)
		msg.SenderPrincipalID = &p
	}
	if bodyText.Valid {
		msg.BodyText = bodyText.String
	}
	if bodyHTML.Valid {
		msg.BodyHTML = bodyHTML.String
	}
	if bodyFmt.Valid {
		msg.BodyFormat = bodyFmt.String
	}
	if replyTo.Valid {
		v := store.ChatMessageID(replyTo.Int64)
		msg.ReplyToMessageID = &v
	}
	if editedUs.Valid {
		t := fromMicros(editedUs.Int64)
		msg.EditedAt = &t
	}
	if deletedUs.Valid {
		t := fromMicros(deletedUs.Int64)
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
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		// Resolve the conversation owner so the state-change row is
		// attributed to the conversation creator (the system-message
		// case has no SenderPrincipalID and we still need a principal
		// to attribute the change feed to).
		var owner int64
		err := tx.QueryRowContext(ctx,
			`SELECT created_by_principal_id FROM chat_conversations WHERE id = ?`,
			int64(msg.ConversationID)).Scan(&owner)
		if err != nil {
			return mapErr(err)
		}
		var senderArg, replyArg, editedArg, deletedArg any
		if msg.SenderPrincipalID != nil {
			senderArg = int64(*msg.SenderPrincipalID)
		}
		if msg.ReplyToMessageID != nil {
			replyArg = int64(*msg.ReplyToMessageID)
		}
		if msg.EditedAt != nil {
			editedArg = usMicros(msg.EditedAt.UTC())
		}
		if msg.DeletedAt != nil {
			deletedArg = usMicros(msg.DeletedAt.UTC())
		}
		bodyFmt := msg.BodyFormat
		if bodyFmt == "" {
			bodyFmt = store.ChatBodyFormatText
		}
		var bodyTextArg, bodyHTMLArg any
		if msg.BodyText != "" {
			bodyTextArg = msg.BodyText
		}
		if msg.BodyHTML != "" {
			bodyHTMLArg = msg.BodyHTML
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chat_messages (conversation_id, sender_principal_id,
			  is_system, body_text, body_html, body_format,
			  reply_to_message_id, reactions_json, attachments_json,
			  metadata_json, edited_at_us, deleted_at_us, created_at_us,
			  modseq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			int64(msg.ConversationID), senderArg, boolToInt(msg.IsSystem),
			bodyTextArg, bodyHTMLArg, bodyFmt, replyArg,
			nilIfEmpty(msg.ReactionsJSON), nilIfEmpty(msg.AttachmentsJSON),
			nilIfEmpty(msg.MetadataJSON), editedArg, deletedArg,
			usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		// Advance the conversation's denormalised counters: only count
		// non-deleted messages, only advance LastMessageAt for live
		// rows.
		if msg.DeletedAt == nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE chat_conversations SET
				  last_message_at_us = ?, message_count = message_count + 1,
				  updated_at_us = ?, modseq = modseq + 1
				 WHERE id = ?`,
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
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
			store.EntityKindChatMessage, uint64(id), uint64(msg.ConversationID),
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.ChatMessageID(id), nil
}

func (m *metadata) GetChatMessage(ctx context.Context, id store.ChatMessageID) (store.ChatMessage, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+chatMessageSelectColumns+` FROM chat_messages WHERE id = ?`,
		int64(id))
	return scanChatMessage(row)
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
	if filter.ConversationID != nil {
		clauses = append(clauses, "conversation_id = ?")
		args = append(args, int64(*filter.ConversationID))
	}
	if filter.SenderPrincipalID != nil {
		clauses = append(clauses, "sender_principal_id = ?")
		args = append(args, int64(*filter.SenderPrincipalID))
	}
	if !filter.IncludeDeleted {
		clauses = append(clauses, "deleted_at_us IS NULL")
	}
	if filter.CreatedAfter != nil {
		clauses = append(clauses, "created_at_us > ?")
		args = append(args, usMicros(filter.CreatedAfter.UTC()))
	}
	if filter.CreatedBefore != nil {
		clauses = append(clauses, "created_at_us < ?")
		args = append(args, usMicros(filter.CreatedBefore.UTC()))
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, "modseq > ?")
		args = append(args, int64(filter.AfterModSeq))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + chatMessageSelectColumns + ` FROM chat_messages`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatMessage
	for rows.Next() {
		msg, err := scanChatMessage(rows)
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
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var convID int64
		var owner int64
		err := tx.QueryRowContext(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = ?`, int64(msg.ID)).Scan(&convID, &owner)
		if err != nil {
			return mapErr(err)
		}
		var editedArg, deletedArg any
		if msg.EditedAt != nil {
			editedArg = usMicros(msg.EditedAt.UTC())
		}
		if msg.DeletedAt != nil {
			deletedArg = usMicros(msg.DeletedAt.UTC())
		}
		bodyFmt := msg.BodyFormat
		if bodyFmt == "" {
			bodyFmt = store.ChatBodyFormatText
		}
		var bodyTextArg, bodyHTMLArg any
		if msg.BodyText != "" {
			bodyTextArg = msg.BodyText
		}
		if msg.BodyHTML != "" {
			bodyHTMLArg = msg.BodyHTML
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE chat_messages SET
			  body_text = ?, body_html = ?, body_format = ?,
			  reactions_json = ?, attachments_json = ?, metadata_json = ?,
			  edited_at_us = ?, deleted_at_us = ?,
			  modseq = modseq + 1
			 WHERE id = ?`,
			bodyTextArg, bodyHTMLArg, bodyFmt,
			nilIfEmpty(msg.ReactionsJSON), nilIfEmpty(msg.AttachmentsJSON),
			nilIfEmpty(msg.MetadataJSON), editedArg, deletedArg,
			int64(msg.ID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
			store.EntityKindChatMessage, uint64(msg.ID), uint64(convID),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) SoftDeleteChatMessage(ctx context.Context, id store.ChatMessageID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var convID, owner int64
		var alreadyDeleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id, cm.deleted_at_us
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = ?`, int64(id)).Scan(&convID, &owner, &alreadyDeleted)
		if err != nil {
			return mapErr(err)
		}
		// Idempotent on a row already soft-deleted.
		if alreadyDeleted.Valid {
			return nil
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE chat_messages SET
			  body_text = NULL, body_html = NULL,
			  deleted_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`, usMicros(now), int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		// Decrement the denormalised conversation message_count so
		// "live" totals stay accurate.
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_conversations SET
			  message_count = MAX(message_count - 1, 0),
			  updated_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`, usMicros(now), convID); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
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
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var convID, owner int64
		var reactions []byte
		err := tx.QueryRowContext(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id, cm.reactions_json
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = ?`, int64(msgID)).Scan(&convID, &owner, &reactions)
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
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_messages SET reactions_json = ?, modseq = modseq + 1
			 WHERE id = ?`, nilIfEmpty(updated), int64(msgID)); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
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
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var reason any
		if b.Reason != "" {
			reason = b.Reason
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO chat_blocks (blocker_principal_id, blocked_principal_id,
			  created_at_us, reason)
			VALUES (?, ?, ?, ?)`,
			int64(b.BlockerPrincipalID), int64(b.BlockedPrincipalID),
			usMicros(now), reason)
		return mapErr(err)
	})
}

func (m *metadata) DeleteChatBlock(ctx context.Context, blocker, blocked store.PrincipalID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM chat_blocks
			 WHERE blocker_principal_id = ? AND blocked_principal_id = ?`,
			int64(blocker), int64(blocked))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListChatBlocksBy(ctx context.Context, blocker store.PrincipalID) ([]store.ChatBlock, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT blocker_principal_id, blocked_principal_id, created_at_us, reason
		  FROM chat_blocks WHERE blocker_principal_id = ?
		 ORDER BY blocked_principal_id ASC`, int64(blocker))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatBlock
	for rows.Next() {
		var (
			blockerID, blockedID, createdUs int64
			reason                          sql.NullString
		)
		if err := rows.Scan(&blockerID, &blockedID, &createdUs, &reason); err != nil {
			return nil, mapErr(err)
		}
		b := store.ChatBlock{
			BlockerPrincipalID: store.PrincipalID(blockerID),
			BlockedPrincipalID: store.PrincipalID(blockedID),
			CreatedAt:          fromMicros(createdUs),
		}
		if reason.Valid {
			b.Reason = reason.String
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (m *metadata) IsBlocked(ctx context.Context, blocker, blocked store.PrincipalID) (bool, error) {
	var one int64
	err := m.s.db.QueryRowContext(ctx, `
		SELECT 1 FROM chat_blocks
		 WHERE blocker_principal_id = ? AND blocked_principal_id = ?
		 LIMIT 1`, int64(blocker), int64(blocked)).Scan(&one)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, mapErr(err)
	}
	return true, nil
}

// -- Read pointer -----------------------------------------------------

func (m *metadata) LastReadAt(ctx context.Context, principalID store.PrincipalID, conversationID store.ConversationID) (*store.ChatMessageID, time.Time, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT last_read_message_id, joined_at_us
		  FROM chat_memberships
		 WHERE conversation_id = ? AND principal_id = ?`,
		int64(conversationID), int64(principalID))
	var (
		lastRead sql.NullInt64
		joinedUs int64
	)
	if err := row.Scan(&lastRead, &joinedUs); err != nil {
		return nil, time.Time{}, mapErr(err)
	}
	var out *store.ChatMessageID
	if lastRead.Valid {
		v := store.ChatMessageID(lastRead.Int64)
		out = &v
	}
	return out, fromMicros(joinedUs), nil
}

func (m *metadata) SetLastRead(ctx context.Context, principalID store.PrincipalID, conversationID store.ConversationID, msgID store.ChatMessageID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var memID int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM chat_memberships
			 WHERE conversation_id = ? AND principal_id = ?`,
			int64(conversationID), int64(principalID)).Scan(&memID)
		if err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE chat_memberships SET
			  last_read_message_id = ?, modseq = modseq + 1
			 WHERE id = ?`, int64(msgID), memID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, principalID,
			store.EntityKindMembership, uint64(memID), uint64(conversationID),
			store.ChangeOpUpdated, now)
	})
}

// -- Account-default settings (Phase 2 Wave 2.9.6 REQ-CHAT-20/92) ---

func (m *metadata) GetChatAccountSettings(ctx context.Context, principalID store.PrincipalID) (store.ChatAccountSettings, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT principal_id, default_retention_seconds, default_edit_window_seconds,
		       created_at_us, updated_at_us
		  FROM chat_account_settings WHERE principal_id = ?`, int64(principalID))
	var (
		pid                  int64
		retention, editWin   int64
		createdUs, updatedUs int64
	)
	if err := row.Scan(&pid, &retention, &editWin, &createdUs, &updatedUs); err != nil {
		if err == sql.ErrNoRows {
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
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO chat_account_settings (principal_id,
			  default_retention_seconds, default_edit_window_seconds,
			  created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(principal_id) DO UPDATE SET
			  default_retention_seconds = excluded.default_retention_seconds,
			  default_edit_window_seconds = excluded.default_edit_window_seconds,
			  updated_at_us = excluded.updated_at_us`,
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
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT principal_id, default_retention_seconds, default_edit_window_seconds,
		       created_at_us, updated_at_us
		  FROM chat_account_settings
		 WHERE default_retention_seconds > 0 AND principal_id > ?
		 ORDER BY principal_id ASC LIMIT ?`,
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
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+chatConversationSelectColumns+`
		   FROM chat_conversations
		  WHERE retention_seconds IS NOT NULL AND retention_seconds > 0
		    AND id > ?
		  ORDER BY id ASC LIMIT ?`,
		int64(afterID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ChatConversation
	for rows.Next() {
		c, err := scanChatConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) HardDeleteChatMessage(ctx context.Context, id store.ChatMessageID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var convID, owner int64
		var atts []byte
		err := tx.QueryRowContext(ctx, `
			SELECT cm.conversation_id, cc.created_by_principal_id, cm.attachments_json
			  FROM chat_messages cm
			  JOIN chat_conversations cc ON cc.id = cm.conversation_id
			 WHERE cm.id = ?`, int64(id)).Scan(&convID, &owner, &atts)
		if err != nil {
			return mapErr(err)
		}
		// Decrement blob_refs.ref_count for each distinct attachment
		// hash atomically with the row delete. The blob-store sweeper
		// evicts blob_refs rows whose ref_count <= 0 out-of-band
		// (REQ-STORE-12 grace window). Mirrors the mail-side
		// ExpungeMessages / DeleteMailbox path.
		hashes, err := store.ChatAttachmentHashes(atts)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM chat_messages WHERE id = ?`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		for _, a := range hashes {
			if err := decRef(ctx, tx, a.Hash, now); err != nil {
				return err
			}
		}
		// Recompute the conversation's denormalised counters from the
		// surviving live rows so retention sweeps leave the row in a
		// consistent state.
		var (
			liveCount int64
			lastUs    sql.NullInt64
		)
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*), MAX(created_at_us)
			  FROM chat_messages
			 WHERE conversation_id = ? AND deleted_at_us IS NULL`,
			convID).Scan(&liveCount, &lastUs); err != nil {
			return mapErr(err)
		}
		var lastArg any
		if lastUs.Valid {
			lastArg = lastUs.Int64
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_conversations SET
			  message_count = ?, last_message_at_us = ?,
			  updated_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`,
			liveCount, lastArg, usMicros(now), convID); err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, store.PrincipalID(owner),
			store.EntityKindChatMessage, uint64(id), uint64(convID),
			store.ChangeOpDestroyed, now)
	})
}

// -- Helpers ----------------------------------------------------------

// nilIfEmpty returns nil for a zero-length byte slice so the binding
// layer writes SQL NULL instead of a zero-length BLOB. Postgres
// distinguishes empty from null bytea; SQLite is lax here but we
// keep the semantics consistent across backends.
func nilIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
