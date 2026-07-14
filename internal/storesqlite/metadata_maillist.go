package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata hosted-mailing-list methods
// (Stage 1 storage foundation, epic #183, REQ-MLIST-01..12). Schema
// commentary lives in migrations/0085_mailing_lists.sql.

const mailingListColumns = `
	id, principal_id, posting_address, domain, display_name, owner_id,
	subject_tag, arc_seal, posting_policy, subscribe_policy,
	bounce_policy_json, archive_mailbox_id, max_message_size_bytes,
	unsubscribe_enabled, archive_retention_days, archive_retention_max_messages,
	created_at_us, updated_at_us`

func scanMailingList(row rowScanner) (store.MailingList, error) {
	var (
		id, principalID, ownerID int64
		postingAddress, domain   string
		displayName              string
		subjectTag               sql.NullString
		arcSeal                  int64
		postingPolicy            string
		subscribePolicy          string
		bouncePolicyJSON         string
		archiveMailboxID         sql.NullInt64
		maxMessageSize           int64
		unsubscribeEnabled       int64
		archiveRetentionDays     int64
		archiveRetentionMaxMsgs  int64
		createdUs, updatedUs     int64
	)
	if err := row.Scan(&id, &principalID, &postingAddress, &domain, &displayName, &ownerID,
		&subjectTag, &arcSeal, &postingPolicy, &subscribePolicy,
		&bouncePolicyJSON, &archiveMailboxID, &maxMessageSize,
		&unsubscribeEnabled, &archiveRetentionDays, &archiveRetentionMaxMsgs,
		&createdUs, &updatedUs); err != nil {
		return store.MailingList{}, mapErr(err)
	}
	l := store.MailingList{
		ID:                          store.MailingListID(id),
		PrincipalID:                 store.PrincipalID(principalID),
		PostingAddress:              postingAddress,
		Domain:                      domain,
		DisplayName:                 displayName,
		OwnerID:                     store.PrincipalID(ownerID),
		ARCSeal:                     arcSeal != 0,
		PostingPolicy:               store.MailingListPostingPolicy(postingPolicy),
		SubscribePolicy:             store.MailingListSubscribePolicy(subscribePolicy),
		BouncePolicyJSON:            bouncePolicyJSON,
		MaxMessageSizeBytes:         maxMessageSize,
		UnsubscribeEnabled:          unsubscribeEnabled != 0,
		ArchiveRetentionDays:        archiveRetentionDays,
		ArchiveRetentionMaxMessages: archiveRetentionMaxMsgs,
		CreatedAt:                   fromMicros(createdUs),
		UpdatedAt:                   fromMicros(updatedUs),
	}
	if subjectTag.Valid {
		v := subjectTag.String
		l.SubjectTag = &v
	}
	if archiveMailboxID.Valid {
		v := store.MailboxID(archiveMailboxID.Int64)
		l.ArchiveMailboxID = &v
	}
	return l, nil
}

func (m *metadata) InsertMailingList(ctx context.Context, l store.MailingList) (store.MailingList, error) {
	address, domain, err := store.NormalizeMailingListAddress(l.PostingAddress)
	if err != nil {
		return store.MailingList{}, err
	}
	now := m.s.clock.Now().UTC()
	var subjectTag any
	if l.SubjectTag != nil {
		subjectTag = *l.SubjectTag
	}
	var archiveMailboxID any
	if l.ArchiveMailboxID != nil {
		archiveMailboxID = int64(*l.ArchiveMailboxID)
	}
	postingPolicy := l.PostingPolicy
	if postingPolicy == "" {
		postingPolicy = store.MailingListPostingOpen
	}
	subscribePolicy := l.SubscribePolicy
	if subscribePolicy == "" {
		subscribePolicy = store.MailingListSubscribeClosed
	}
	bouncePolicyJSON := l.BouncePolicyJSON
	if bouncePolicyJSON == "" {
		bouncePolicyJSON = "{}"
	}
	var id int64
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO mailing_list (principal_id, posting_address, domain, display_name,
				owner_id, subject_tag, arc_seal, posting_policy, subscribe_policy,
				bounce_policy_json, archive_mailbox_id, max_message_size_bytes,
				unsubscribe_enabled, archive_retention_days, archive_retention_max_messages,
				created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(l.PrincipalID), address, domain, l.DisplayName,
			int64(l.OwnerID), subjectTag, boolToInt(l.ARCSeal),
			string(postingPolicy), string(subscribePolicy),
			bouncePolicyJSON, archiveMailboxID, l.MaxMessageSizeBytes,
			boolToInt(l.UnsubscribeEnabled), l.ArchiveRetentionDays, l.ArchiveRetentionMaxMessages,
			usMicros(now), usMicros(now))
		if err != nil {
			return fmt.Errorf("mailing list %q: %w", address, mapErr(err))
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.MailingList{}, err
	}
	return m.GetMailingList(ctx, store.MailingListID(id))
}

func (m *metadata) GetMailingList(ctx context.Context, id store.MailingListID) (store.MailingList, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+mailingListColumns+` FROM mailing_list WHERE id = ?`, int64(id))
	return scanMailingList(row)
}

func (m *metadata) GetMailingListByPostingAddress(ctx context.Context, address string) (store.MailingList, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+mailingListColumns+` FROM mailing_list WHERE posting_address = ?`,
		strings.ToLower(strings.TrimSpace(address)))
	return scanMailingList(row)
}

func (m *metadata) ListMailingLists(ctx context.Context, filter store.MailingListFilter) ([]store.MailingList, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	if filter.Domain != "" {
		clauses = append(clauses, "domain = ?")
		args = append(args, strings.ToLower(filter.Domain))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + mailingListColumns + ` FROM mailing_list`
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
	out := make([]store.MailingList, 0)
	for rows.Next() {
		l, err := scanMailingList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, mapErr(rows.Err())
}

func (m *metadata) UpdateMailingList(ctx context.Context, l store.MailingList) error {
	address, domain, err := store.NormalizeMailingListAddress(l.PostingAddress)
	if err != nil {
		return err
	}
	now := m.s.clock.Now().UTC()
	var subjectTag any
	if l.SubjectTag != nil {
		subjectTag = *l.SubjectTag
	}
	var archiveMailboxID any
	if l.ArchiveMailboxID != nil {
		archiveMailboxID = int64(*l.ArchiveMailboxID)
	}
	bouncePolicyJSON := l.BouncePolicyJSON
	if bouncePolicyJSON == "" {
		bouncePolicyJSON = "{}"
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE mailing_list
			   SET posting_address = ?, domain = ?, display_name = ?, owner_id = ?,
			       subject_tag = ?, arc_seal = ?, posting_policy = ?, subscribe_policy = ?,
			       bounce_policy_json = ?, archive_mailbox_id = ?, max_message_size_bytes = ?,
			       unsubscribe_enabled = ?, archive_retention_days = ?, archive_retention_max_messages = ?,
			       updated_at_us = ?
			 WHERE id = ?`,
			address, domain, l.DisplayName, int64(l.OwnerID),
			subjectTag, boolToInt(l.ARCSeal), string(l.PostingPolicy), string(l.SubscribePolicy),
			bouncePolicyJSON, archiveMailboxID, l.MaxMessageSizeBytes,
			boolToInt(l.UnsubscribeEnabled), l.ArchiveRetentionDays, l.ArchiveRetentionMaxMessages,
			usMicros(now), int64(l.ID))
		if err != nil {
			return fmt.Errorf("mailing list %d: %w", l.ID, mapErr(err))
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

func (m *metadata) DeleteMailingList(ctx context.Context, id store.MailingListID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM mailing_list WHERE id = ?`, int64(id))
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

// -- Roster ------------------------------------------------------------

const mailingListMemberColumns = `
	id, list_id, principal_id, external_address, state, delivery_mode,
	bounce_score, last_bounce_at_us, added_at_us, added_by`

func scanMailingListMember(row rowScanner) (store.MailingListMember, error) {
	var (
		id, listID          int64
		principalID         sql.NullInt64
		externalAddress     sql.NullString
		state, deliveryMode string
		bounceScore         float64
		lastBounceAtUs      sql.NullInt64
		addedAtUs           int64
		addedBy             sql.NullInt64
	)
	if err := row.Scan(&id, &listID, &principalID, &externalAddress, &state, &deliveryMode,
		&bounceScore, &lastBounceAtUs, &addedAtUs, &addedBy); err != nil {
		return store.MailingListMember{}, mapErr(err)
	}
	mem := store.MailingListMember{
		ID:           store.MailingListMemberID(id),
		ListID:       store.MailingListID(listID),
		State:        store.MailingListMemberState(state),
		DeliveryMode: store.MailingListDeliveryMode(deliveryMode),
		BounceScore:  bounceScore,
		AddedAt:      fromMicros(addedAtUs),
	}
	if principalID.Valid {
		v := store.PrincipalID(principalID.Int64)
		mem.PrincipalID = &v
	}
	if externalAddress.Valid {
		v := externalAddress.String
		mem.ExternalAddress = &v
	}
	if lastBounceAtUs.Valid {
		v := fromMicros(lastBounceAtUs.Int64)
		mem.LastBounceAt = &v
	}
	if addedBy.Valid {
		v := store.PrincipalID(addedBy.Int64)
		mem.AddedBy = &v
	}
	return mem, nil
}

// validateMailingListMember enforces REQ-MLIST-02/04 in Go, ahead of the
// mirroring CHECK constraints in the schema, so a violation returns the
// package's ErrInvalidArgument sentinel with a clear message rather than
// an opaque driver error.
func validateMailingListMember(principalID *store.PrincipalID, externalAddress *string, mode store.MailingListDeliveryMode) error {
	switch {
	case principalID == nil && externalAddress == nil:
		return fmt.Errorf("%w: mailing list member must set PrincipalID or ExternalAddress", store.ErrInvalidArgument)
	case principalID != nil && externalAddress != nil:
		return fmt.Errorf("%w: mailing list member must set exactly one of PrincipalID / ExternalAddress", store.ErrInvalidArgument)
	}
	if mode == store.MailingListDeliveryNoMail && principalID == nil {
		return fmt.Errorf("%w: nomail delivery requires an internal principal member", store.ErrInvalidArgument)
	}
	return nil
}

func (m *metadata) AddMailingListMember(ctx context.Context, mem store.MailingListMember) (store.MailingListMember, error) {
	if err := validateMailingListMember(mem.PrincipalID, mem.ExternalAddress, mem.DeliveryMode); err != nil {
		return store.MailingListMember{}, err
	}
	state := mem.State
	if state == "" {
		state = store.MailingListMemberActive
	}
	mode := mem.DeliveryMode
	if mode == "" {
		mode = store.MailingListDeliveryEach
	}
	now := m.s.clock.Now().UTC()
	var principalID, addedBy any
	if mem.PrincipalID != nil {
		principalID = int64(*mem.PrincipalID)
	}
	var externalAddress any
	if mem.ExternalAddress != nil {
		v := store.CanonicalizeExternalAddress(*mem.ExternalAddress)
		externalAddress = v
	}
	if mem.AddedBy != nil {
		addedBy = int64(*mem.AddedBy)
	}
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO mailing_list_member (list_id, principal_id, external_address,
				state, delivery_mode, bounce_score, last_bounce_at_us, added_at_us, added_by)
			VALUES (?, ?, ?, ?, ?, 0, NULL, ?, ?)`,
			int64(mem.ListID), principalID, externalAddress,
			string(state), string(mode), usMicros(now), addedBy)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return nil
	})
	if err != nil {
		return store.MailingListMember{}, err
	}
	return m.GetMailingListMember(ctx, store.MailingListMemberID(id))
}

func (m *metadata) GetMailingListMember(ctx context.Context, id store.MailingListMemberID) (store.MailingListMember, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+mailingListMemberColumns+` FROM mailing_list_member WHERE id = ?`, int64(id))
	return scanMailingListMember(row)
}

// GetMailingListMemberByAddress implements the store.Metadata method of
// the same name (Stage 3, REQ-MLIST-61): looks up an ExternalAddress
// roster row by (list_id, external_address), canonicalising address the
// same way AddMailingListMember does before comparing.
func (m *metadata) GetMailingListMemberByAddress(ctx context.Context, listID store.MailingListID, address string) (store.MailingListMember, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+mailingListMemberColumns+` FROM mailing_list_member WHERE list_id = ? AND external_address = ?`,
		int64(listID), store.CanonicalizeExternalAddress(address))
	return scanMailingListMember(row)
}

func (m *metadata) RemoveMailingListMember(ctx context.Context, id store.MailingListMemberID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM mailing_list_member WHERE id = ?`, int64(id))
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

func (m *metadata) UpdateMailingListMemberState(ctx context.Context, id store.MailingListMemberID, state store.MailingListMemberState) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE mailing_list_member SET state = ? WHERE id = ?`,
			string(state), int64(id))
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

func (m *metadata) UpdateMailingListMemberDeliveryMode(ctx context.Context, id store.MailingListMemberID, mode store.MailingListDeliveryMode) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		if mode == store.MailingListDeliveryNoMail {
			var principalID sql.NullInt64
			err := tx.QueryRowContext(ctx,
				`SELECT principal_id FROM mailing_list_member WHERE id = ?`, int64(id)).Scan(&principalID)
			if err != nil {
				return mapErr(err)
			}
			if !principalID.Valid {
				return fmt.Errorf("%w: nomail delivery requires an internal principal member", store.ErrInvalidArgument)
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailing_list_member SET delivery_mode = ? WHERE id = ?`,
			string(mode), int64(id))
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

// RecordMailingListMemberBounce implements the store.Metadata method of
// the same name (REQ-MLIST-53, issue #184). The decay decision and the
// score write happen inside one runTx call, wrapped in the store's own
// writer-serializing transaction, so a concurrent bounce for the same
// member cannot read stale state.
func (m *metadata) RecordMailingListMemberBounce(ctx context.Context, id store.MailingListMemberID, now time.Time, weight float64, decayWindow time.Duration) (float64, error) {
	var newScore float64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var score float64
		var lastBounceUs sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT bounce_score, last_bounce_at_us FROM mailing_list_member WHERE id = ?`,
			int64(id)).Scan(&score, &lastBounceUs); err != nil {
			return mapErr(err)
		}
		decayed := !lastBounceUs.Valid
		if lastBounceUs.Valid && decayWindow > 0 {
			last := fromMicros(lastBounceUs.Int64)
			if now.Sub(last) > decayWindow {
				decayed = true
			}
		}
		if decayed {
			newScore = weight
		} else {
			newScore = score + weight
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailing_list_member SET bounce_score = ?, last_bounce_at_us = ? WHERE id = ?`,
			newScore, usMicros(now.UTC()), int64(id))
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
	return newScore, err
}

// SuspendMailingListMemberIfActive implements the store.Metadata method
// of the same name (REQ-MLIST-54, issue #184): the UPDATE's own WHERE
// clause is the compare-and-swap.
func (m *metadata) SuspendMailingListMemberIfActive(ctx context.Context, id store.MailingListMemberID) (bool, error) {
	var transitioned bool
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var exists int64
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM mailing_list_member WHERE id = ?`, int64(id)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE mailing_list_member SET state = 'suspended' WHERE id = ? AND state = 'active'`,
			int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		transitioned = n > 0
		return nil
	})
	return transitioned, err
}

// ReactivateMailingListMember implements the store.Metadata method of
// the same name (REQ-MLIST-55, issue #184).
func (m *metadata) ReactivateMailingListMember(ctx context.Context, id store.MailingListMemberID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE mailing_list_member SET state = 'active', bounce_score = 0, last_bounce_at_us = NULL WHERE id = ?`,
			int64(id))
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

// CountMailingListMembersByState implements the store.Metadata method of
// the same name (issue #184).
func (m *metadata) CountMailingListMembersByState(ctx context.Context, listID store.MailingListID) (map[store.MailingListMemberState]int, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT state, COUNT(*) FROM mailing_list_member WHERE list_id = ? GROUP BY state`, int64(listID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make(map[store.MailingListMemberState]int)
	for rows.Next() {
		var state string
		var n int64
		if err := rows.Scan(&state, &n); err != nil {
			return nil, mapErr(err)
		}
		out[store.MailingListMemberState(state)] = int(n)
	}
	return out, mapErr(rows.Err())
}

func (m *metadata) ListMailingListMembers(ctx context.Context, filter store.MailingListRosterFilter) ([]store.MailingListMember, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	clauses := []string{"list_id = ?"}
	args := []any{int64(filter.ListID)}
	if filter.State != "" {
		clauses = append(clauses, "state = ?")
		args = append(args, string(filter.State))
	}
	if filter.DeliveryMode != "" {
		clauses = append(clauses, "delivery_mode = ?")
		args = append(args, string(filter.DeliveryMode))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + mailingListMemberColumns + ` FROM mailing_list_member
		WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.MailingListMember, 0)
	for rows.Next() {
		mem, err := scanMailingListMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mem)
	}
	return out, mapErr(rows.Err())
}

// -- Hosted mailing lists, moderation (v2 milestone, issue #189,
// REQ-MLIST-80) --------------------------------------------------------

const mailingListHeldPostColumns = `
	id, list_id, blob_hash, blob_size, from_address, subject, message_id,
	auth_results_json, reason, status, held_at_us, decided_at_us,
	decided_by, decision_note`

func scanMailingListHeldPost(row rowScanner) (store.MailingListHeldPost, error) {
	var (
		id, listID      int64
		blobHash        string
		blobSize        int64
		fromAddress     string
		subject         string
		messageID       string
		authResultsJSON string
		reason          string
		status          string
		heldAtUs        int64
		decidedAtUs     sql.NullInt64
		decidedBy       sql.NullInt64
		decisionNote    string
	)
	if err := row.Scan(&id, &listID, &blobHash, &blobSize, &fromAddress, &subject, &messageID,
		&authResultsJSON, &reason, &status, &heldAtUs, &decidedAtUs, &decidedBy, &decisionNote); err != nil {
		return store.MailingListHeldPost{}, mapErr(err)
	}
	h := store.MailingListHeldPost{
		ID:              store.MailingListHeldPostID(id),
		ListID:          store.MailingListID(listID),
		BlobHash:        blobHash,
		BlobSize:        blobSize,
		FromAddress:     fromAddress,
		Subject:         subject,
		MessageID:       messageID,
		AuthResultsJSON: authResultsJSON,
		Reason:          store.MailingListHeldPostReason(reason),
		Status:          store.MailingListHeldPostStatus(status),
		HeldAt:          fromMicros(heldAtUs),
		DecisionNote:    decisionNote,
	}
	if decidedAtUs.Valid {
		v := fromMicros(decidedAtUs.Int64)
		h.DecidedAt = &v
	}
	if decidedBy.Valid {
		v := store.PrincipalID(decidedBy.Int64)
		h.DecidedBy = &v
	}
	return h, nil
}

// InsertMailingListHeldPost implements the store.Metadata method of the
// same name: the row insert and the blob_refs IncRef happen inside one
// transaction (mirrors the identity-avatar-blob caller-managed
// refcounting pattern), so a crash between the two can never leave the
// row without its blob kept alive.
func (m *metadata) InsertMailingListHeldPost(ctx context.Context, h store.MailingListHeldPost) (store.MailingListHeldPost, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO mailing_list_held_post
				(list_id, blob_hash, blob_size, from_address, subject, message_id,
				 auth_results_json, reason, status, held_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
			int64(h.ListID), h.BlobHash, h.BlobSize, h.FromAddress, h.Subject, h.MessageID,
			h.AuthResultsJSON, string(h.Reason), usMicros(now))
		if err != nil {
			return fmt.Errorf("mailing list held post: %w", mapErr(err))
		}
		lastID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = lastID
		return incRef(ctx, tx, h.BlobHash, h.BlobSize, now)
	})
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	return m.GetMailingListHeldPost(ctx, store.MailingListHeldPostID(id))
}

func (m *metadata) GetMailingListHeldPost(ctx context.Context, id store.MailingListHeldPostID) (store.MailingListHeldPost, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+mailingListHeldPostColumns+` FROM mailing_list_held_post WHERE id = ?`, int64(id))
	return scanMailingListHeldPost(row)
}

func (m *metadata) ListMailingListHeldPosts(ctx context.Context, filter store.MailingListHeldPostFilter) ([]store.MailingListHeldPost, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	clauses := []string{"list_id = ?"}
	args := []any{int64(filter.ListID)}
	if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + mailingListHeldPostColumns + ` FROM mailing_list_held_post
		WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.MailingListHeldPost, 0)
	for rows.Next() {
		h, err := scanMailingListHeldPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, mapErr(rows.Err())
}

// DecideMailingListHeldPost implements the store.Metadata method of the
// same name: the UPDATE's own WHERE status = 'pending' clause is the
// compare-and-swap (mirroring SuspendMailingListMemberIfActive above),
// and the blob_refs DecRef happens in the same transaction as the
// transition so the two can never observably diverge.
func (m *metadata) DecideMailingListHeldPost(ctx context.Context, id store.MailingListHeldPostID, status store.MailingListHeldPostStatus, decidedBy store.PrincipalID, note string, now time.Time) (store.MailingListHeldPost, error) {
	now = now.UTC()
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		var blobHash string
		if err := tx.QueryRowContext(ctx,
			`SELECT blob_hash FROM mailing_list_held_post WHERE id = ?`, int64(id)).Scan(&blobHash); err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE mailing_list_held_post
			   SET status = ?, decided_at_us = ?, decided_by = ?, decision_note = ?
			 WHERE id = ? AND status = 'pending'`,
			string(status), usMicros(now), int64(decidedBy), note, int64(id))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: rows affected: %w", err)
		}
		if n == 0 {
			return store.ErrConflict
		}
		return decRef(ctx, tx, blobHash, now)
	})
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	return m.GetMailingListHeldPost(ctx, id)
}
