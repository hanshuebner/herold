package storepg

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata hosted-mailing-list methods
// (Stage 1 storage foundation, epic #183, REQ-MLIST-01..12) against
// Postgres. Mirrors storesqlite/metadata_maillist.go. Schema commentary
// lives in migrations/0085_mailing_lists.sql.

const mailingListColumnsPG = `
	id, principal_id, posting_address, domain, display_name, owner_id,
	subject_tag, arc_seal, posting_policy, subscribe_policy,
	bounce_policy_json, archive_mailbox_id, max_message_size_bytes,
	unsubscribe_enabled, archive_retention_days, archive_retention_max_messages,
	created_at_us, updated_at_us`

func scanMailingListPG(row rowScanner) (store.MailingList, error) {
	var (
		id, principalID, ownerID int64
		postingAddress, domain   string
		displayName              string
		subjectTag               *string
		arcSeal                  bool
		postingPolicy            string
		subscribePolicy          string
		bouncePolicyJSON         string
		archiveMailboxID         *int64
		maxMessageSize           int64
		unsubscribeEnabled       bool
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
		ARCSeal:                     arcSeal,
		PostingPolicy:               store.MailingListPostingPolicy(postingPolicy),
		SubscribePolicy:             store.MailingListSubscribePolicy(subscribePolicy),
		BouncePolicyJSON:            bouncePolicyJSON,
		MaxMessageSizeBytes:         maxMessageSize,
		UnsubscribeEnabled:          unsubscribeEnabled,
		ArchiveRetentionDays:        archiveRetentionDays,
		ArchiveRetentionMaxMessages: archiveRetentionMaxMsgs,
		CreatedAt:                   fromMicros(createdUs),
		UpdatedAt:                   fromMicros(updatedUs),
	}
	if subjectTag != nil {
		v := *subjectTag
		l.SubjectTag = &v
	}
	if archiveMailboxID != nil {
		v := store.MailboxID(*archiveMailboxID)
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
	var subjectTag *string
	if l.SubjectTag != nil {
		v := *l.SubjectTag
		subjectTag = &v
	}
	var archiveMailboxID *int64
	if l.ArchiveMailboxID != nil {
		v := int64(*l.ArchiveMailboxID)
		archiveMailboxID = &v
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
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO mailing_list (principal_id, posting_address, domain, display_name,
				owner_id, subject_tag, arc_seal, posting_policy, subscribe_policy,
				bounce_policy_json, archive_mailbox_id, max_message_size_bytes,
				unsubscribe_enabled, archive_retention_days, archive_retention_max_messages,
				created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			RETURNING id`,
			int64(l.PrincipalID), address, domain, l.DisplayName,
			int64(l.OwnerID), subjectTag, l.ARCSeal,
			string(postingPolicy), string(subscribePolicy),
			bouncePolicyJSON, archiveMailboxID, l.MaxMessageSizeBytes,
			l.UnsubscribeEnabled, l.ArchiveRetentionDays, l.ArchiveRetentionMaxMessages,
			usMicros(now), usMicros(now))
		if err := row.Scan(&id); err != nil {
			return fmt.Errorf("mailing list %q: %w", address, mapErr(err))
		}
		return nil
	})
	if err != nil {
		return store.MailingList{}, err
	}
	return m.GetMailingList(ctx, store.MailingListID(id))
}

func (m *metadata) GetMailingList(ctx context.Context, id store.MailingListID) (store.MailingList, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+mailingListColumnsPG+` FROM mailing_list WHERE id = $1`, int64(id))
	return scanMailingListPG(row)
}

func (m *metadata) GetMailingListByPostingAddress(ctx context.Context, address string) (store.MailingList, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+mailingListColumnsPG+` FROM mailing_list WHERE posting_address = $1`,
		strings.ToLower(strings.TrimSpace(address)))
	return scanMailingListPG(row)
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
	idx := 1
	if filter.Domain != "" {
		clauses = append(clauses, fmt.Sprintf("domain = $%d", idx))
		args = append(args, strings.ToLower(filter.Domain))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + mailingListColumnsPG + ` FROM mailing_list`
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
	out := make([]store.MailingList, 0)
	for rows.Next() {
		l, err := scanMailingListPG(rows)
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
	var subjectTag *string
	if l.SubjectTag != nil {
		v := *l.SubjectTag
		subjectTag = &v
	}
	var archiveMailboxID *int64
	if l.ArchiveMailboxID != nil {
		v := int64(*l.ArchiveMailboxID)
		archiveMailboxID = &v
	}
	bouncePolicyJSON := l.BouncePolicyJSON
	if bouncePolicyJSON == "" {
		bouncePolicyJSON = "{}"
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE mailing_list
			   SET posting_address = $1, domain = $2, display_name = $3, owner_id = $4,
			       subject_tag = $5, arc_seal = $6, posting_policy = $7, subscribe_policy = $8,
			       bounce_policy_json = $9, archive_mailbox_id = $10, max_message_size_bytes = $11,
			       unsubscribe_enabled = $12, archive_retention_days = $13, archive_retention_max_messages = $14,
			       updated_at_us = $15
			 WHERE id = $16`,
			address, domain, l.DisplayName, int64(l.OwnerID),
			subjectTag, l.ARCSeal, string(l.PostingPolicy), string(l.SubscribePolicy),
			bouncePolicyJSON, archiveMailboxID, l.MaxMessageSizeBytes,
			l.UnsubscribeEnabled, l.ArchiveRetentionDays, l.ArchiveRetentionMaxMessages,
			usMicros(now), int64(l.ID))
		if err != nil {
			return fmt.Errorf("mailing list %d: %w", l.ID, mapErr(err))
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) DeleteMailingList(ctx context.Context, id store.MailingListID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM mailing_list WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// -- Roster ------------------------------------------------------------

const mailingListMemberColumnsPG = `
	id, list_id, principal_id, external_address, state, delivery_mode,
	bounce_score, last_bounce_at_us, added_at_us, added_by`

func scanMailingListMemberPG(row rowScanner) (store.MailingListMember, error) {
	var (
		id, listID          int64
		principalID         *int64
		externalAddress     *string
		state, deliveryMode string
		bounceScore         float64
		lastBounceAtUs      *int64
		addedAtUs           int64
		addedBy             *int64
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
	if principalID != nil {
		v := store.PrincipalID(*principalID)
		mem.PrincipalID = &v
	}
	if externalAddress != nil {
		v := *externalAddress
		mem.ExternalAddress = &v
	}
	if lastBounceAtUs != nil {
		v := fromMicros(*lastBounceAtUs)
		mem.LastBounceAt = &v
	}
	if addedBy != nil {
		v := store.PrincipalID(*addedBy)
		mem.AddedBy = &v
	}
	return mem, nil
}

// validateMailingListMember enforces REQ-MLIST-02/04 in Go, ahead of the
// mirroring CHECK constraints in the schema, so a violation returns the
// package's ErrInvalidArgument sentinel with a clear message rather than
// an opaque driver error. Mirrors storesqlite/metadata_maillist.go.
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
	var principalID *int64
	if mem.PrincipalID != nil {
		v := int64(*mem.PrincipalID)
		principalID = &v
	}
	var externalAddress *string
	if mem.ExternalAddress != nil {
		v := store.CanonicalizeExternalAddress(*mem.ExternalAddress)
		externalAddress = &v
	}
	var addedBy *int64
	if mem.AddedBy != nil {
		v := int64(*mem.AddedBy)
		addedBy = &v
	}
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO mailing_list_member (list_id, principal_id, external_address,
				state, delivery_mode, bounce_score, last_bounce_at_us, added_at_us, added_by)
			VALUES ($1, $2, $3, $4, $5, 0, NULL, $6, $7)
			RETURNING id`,
			int64(mem.ListID), principalID, externalAddress,
			string(state), string(mode), usMicros(now), addedBy)
		if err := row.Scan(&id); err != nil {
			return mapErr(err)
		}
		return nil
	})
	if err != nil {
		return store.MailingListMember{}, err
	}
	return m.GetMailingListMember(ctx, store.MailingListMemberID(id))
}

func (m *metadata) GetMailingListMember(ctx context.Context, id store.MailingListMemberID) (store.MailingListMember, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+mailingListMemberColumnsPG+` FROM mailing_list_member WHERE id = $1`, int64(id))
	return scanMailingListMemberPG(row)
}

// GetMailingListMemberByAddress implements the store.Metadata method of
// the same name (Stage 3, REQ-MLIST-61). Mirrors
// storesqlite/metadata_maillist.go.
func (m *metadata) GetMailingListMemberByAddress(ctx context.Context, listID store.MailingListID, address string) (store.MailingListMember, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+mailingListMemberColumnsPG+` FROM mailing_list_member WHERE list_id = $1 AND external_address = $2`,
		int64(listID), store.CanonicalizeExternalAddress(address))
	return scanMailingListMemberPG(row)
}

func (m *metadata) RemoveMailingListMember(ctx context.Context, id store.MailingListMemberID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM mailing_list_member WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) UpdateMailingListMemberState(ctx context.Context, id store.MailingListMemberID, state store.MailingListMemberState) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE mailing_list_member SET state = $1 WHERE id = $2`,
			string(state), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) UpdateMailingListMemberDeliveryMode(ctx context.Context, id store.MailingListMemberID, mode store.MailingListDeliveryMode) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		if mode == store.MailingListDeliveryNoMail {
			var principalID sql.NullInt64
			err := tx.QueryRow(ctx,
				`SELECT principal_id FROM mailing_list_member WHERE id = $1`, int64(id)).Scan(&principalID)
			if err != nil {
				return mapErr(err)
			}
			if !principalID.Valid {
				return fmt.Errorf("%w: nomail delivery requires an internal principal member", store.ErrInvalidArgument)
			}
		}
		ct, err := tx.Exec(ctx,
			`UPDATE mailing_list_member SET delivery_mode = $1 WHERE id = $2`,
			string(mode), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// RecordMailingListMemberBounce implements the store.Metadata method of
// the same name (REQ-MLIST-53, issue #184). Mirrors
// storesqlite/metadata_maillist.go: the decay decision and the score
// write happen inside one runTx call.
func (m *metadata) RecordMailingListMemberBounce(ctx context.Context, id store.MailingListMemberID, now time.Time, weight float64, decayWindow time.Duration) (float64, error) {
	var newScore float64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var score float64
		var lastBounceUs *int64
		if err := tx.QueryRow(ctx,
			`SELECT bounce_score, last_bounce_at_us FROM mailing_list_member WHERE id = $1`,
			int64(id)).Scan(&score, &lastBounceUs); err != nil {
			return mapErr(err)
		}
		decayed := lastBounceUs == nil
		if lastBounceUs != nil && decayWindow > 0 {
			last := fromMicros(*lastBounceUs)
			if now.Sub(last) > decayWindow {
				decayed = true
			}
		}
		if decayed {
			newScore = weight
		} else {
			newScore = score + weight
		}
		ct, err := tx.Exec(ctx,
			`UPDATE mailing_list_member SET bounce_score = $1, last_bounce_at_us = $2 WHERE id = $3`,
			newScore, usMicros(now.UTC()), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var exists int64
		if err := tx.QueryRow(ctx,
			`SELECT 1 FROM mailing_list_member WHERE id = $1`, int64(id)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		ct, err := tx.Exec(ctx,
			`UPDATE mailing_list_member SET state = 'suspended' WHERE id = $1 AND state = 'active'`,
			int64(id))
		if err != nil {
			return mapErr(err)
		}
		transitioned = ct.RowsAffected() > 0
		return nil
	})
	return transitioned, err
}

// ReactivateMailingListMember implements the store.Metadata method of
// the same name (REQ-MLIST-55, issue #184).
func (m *metadata) ReactivateMailingListMember(ctx context.Context, id store.MailingListMemberID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE mailing_list_member SET state = 'active', bounce_score = 0, last_bounce_at_us = NULL WHERE id = $1`,
			int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// CountMailingListMembersByState implements the store.Metadata method of
// the same name (issue #184).
func (m *metadata) CountMailingListMembersByState(ctx context.Context, listID store.MailingListID) (map[store.MailingListMemberState]int, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT state, COUNT(*) FROM mailing_list_member WHERE list_id = $1 GROUP BY state`, int64(listID))
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
	clauses := []string{"list_id = $1"}
	args := []any{int64(filter.ListID)}
	idx := 2
	if filter.State != "" {
		clauses = append(clauses, fmt.Sprintf("state = $%d", idx))
		args = append(args, string(filter.State))
		idx++
	}
	if filter.DeliveryMode != "" {
		clauses = append(clauses, fmt.Sprintf("delivery_mode = $%d", idx))
		args = append(args, string(filter.DeliveryMode))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + mailingListMemberColumnsPG + ` FROM mailing_list_member
		WHERE ` + strings.Join(clauses, " AND ") + fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.MailingListMember, 0)
	for rows.Next() {
		mem, err := scanMailingListMemberPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mem)
	}
	return out, mapErr(rows.Err())
}

// -- Hosted mailing lists, moderation (v2 milestone, issue #189,
// REQ-MLIST-80) --------------------------------------------------------
// Mirrors storesqlite/metadata_maillist.go's held-post methods.

const mailingListHeldPostColumnsPG = `
	id, list_id, blob_hash, blob_size, from_address, subject, message_id,
	auth_results_json, reason, status, held_at_us, decided_at_us,
	decided_by, decision_note`

func scanMailingListHeldPostPG(row rowScanner) (store.MailingListHeldPost, error) {
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
		decidedAtUs     *int64
		decidedBy       *int64
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
	if decidedAtUs != nil {
		v := fromMicros(*decidedAtUs)
		h.DecidedAt = &v
	}
	if decidedBy != nil {
		v := store.PrincipalID(*decidedBy)
		h.DecidedBy = &v
	}
	return h, nil
}

// InsertMailingListHeldPost implements the store.Metadata method of the
// same name. Mirrors storesqlite: the row insert and the blob_refs
// IncRef happen inside one transaction.
func (m *metadata) InsertMailingListHeldPost(ctx context.Context, h store.MailingListHeldPost) (store.MailingListHeldPost, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO mailing_list_held_post
				(list_id, blob_hash, blob_size, from_address, subject, message_id,
				 auth_results_json, reason, status, held_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9)
			RETURNING id`,
			int64(h.ListID), h.BlobHash, h.BlobSize, h.FromAddress, h.Subject, h.MessageID,
			h.AuthResultsJSON, string(h.Reason), usMicros(now))
		if err := row.Scan(&id); err != nil {
			return fmt.Errorf("mailing list held post: %w", mapErr(err))
		}
		return incRef(ctx, tx, h.BlobHash, h.BlobSize, now)
	})
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	return m.GetMailingListHeldPost(ctx, store.MailingListHeldPostID(id))
}

func (m *metadata) GetMailingListHeldPost(ctx context.Context, id store.MailingListHeldPostID) (store.MailingListHeldPost, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+mailingListHeldPostColumnsPG+` FROM mailing_list_held_post WHERE id = $1`, int64(id))
	return scanMailingListHeldPostPG(row)
}

func (m *metadata) ListMailingListHeldPosts(ctx context.Context, filter store.MailingListHeldPostFilter) ([]store.MailingListHeldPost, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	clauses := []string{"list_id = $1"}
	args := []any{int64(filter.ListID)}
	idx := 2
	if filter.Status != "" {
		clauses = append(clauses, fmt.Sprintf("status = $%d", idx))
		args = append(args, string(filter.Status))
		idx++
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, fmt.Sprintf("id > $%d", idx))
		args = append(args, int64(filter.AfterID))
		idx++
	}
	q := `SELECT ` + mailingListHeldPostColumnsPG + ` FROM mailing_list_held_post
		WHERE ` + strings.Join(clauses, " AND ") + fmt.Sprintf(" ORDER BY id ASC LIMIT $%d", idx)
	args = append(args, limit)
	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.MailingListHeldPost, 0)
	for rows.Next() {
		h, err := scanMailingListHeldPostPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, mapErr(rows.Err())
}

// DecideMailingListHeldPost implements the store.Metadata method of the
// same name. Mirrors storesqlite: the UPDATE's own WHERE status =
// 'pending' clause is the compare-and-swap, and the blob_refs DecRef
// happens in the same transaction as the transition.
func (m *metadata) DecideMailingListHeldPost(ctx context.Context, id store.MailingListHeldPostID, status store.MailingListHeldPostStatus, decidedBy store.PrincipalID, note string, now time.Time) (store.MailingListHeldPost, error) {
	if status != store.MailingListHeldPostRejected && status != store.MailingListHeldPostDiscarded {
		return store.MailingListHeldPost{}, fmt.Errorf(
			"%w: DecideMailingListHeldPost only accepts rejected/discarded; approval goes through ClaimMailingListHeldPostForApproval/FinalizeMailingListHeldPostApproval",
			store.ErrInvalidArgument)
	}
	now = now.UTC()
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var blobHash string
		if err := tx.QueryRow(ctx,
			`SELECT blob_hash FROM mailing_list_held_post WHERE id = $1`, int64(id)).Scan(&blobHash); err != nil {
			return mapErr(err)
		}
		ct, err := tx.Exec(ctx, `
			UPDATE mailing_list_held_post
			   SET status = $1, decided_at_us = $2, decided_by = $3, decision_note = $4
			 WHERE id = $5 AND status = 'pending'`,
			string(status), usMicros(now), int64(decidedBy), note, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrConflict
		}
		return decRef(ctx, tx, blobHash, now)
	})
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	return m.GetMailingListHeldPost(ctx, id)
}

// ClaimMailingListHeldPostForApproval implements the store.Metadata
// method of the same name (issue #189 verification fix). Mirrors
// storesqlite: the CAS that makes "a held post is fanned out at most
// once" hold under concurrent ApproveHeldPost calls.
func (m *metadata) ClaimMailingListHeldPostForApproval(ctx context.Context, id store.MailingListHeldPostID, decidedBy store.PrincipalID, now time.Time) (store.MailingListHeldPost, error) {
	now = now.UTC()
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var exists int64
		if err := tx.QueryRow(ctx,
			`SELECT 1 FROM mailing_list_held_post WHERE id = $1`, int64(id)).Scan(&exists); err != nil {
			return mapErr(err)
		}
		ct, err := tx.Exec(ctx, `
			UPDATE mailing_list_held_post
			   SET status = 'approving', decided_at_us = $1, decided_by = $2
			 WHERE id = $3 AND status = 'pending'`,
			usMicros(now), int64(decidedBy), int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrConflict
		}
		return nil
	})
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	return m.GetMailingListHeldPost(ctx, id)
}

// FinalizeMailingListHeldPostApproval implements the store.Metadata
// method of the same name (issue #189 verification fix). Mirrors
// storesqlite: the CAS approving -> approved, with the blob_refs
// release in the same transaction.
func (m *metadata) FinalizeMailingListHeldPostApproval(ctx context.Context, id store.MailingListHeldPostID, now time.Time) (store.MailingListHeldPost, error) {
	now = now.UTC()
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var blobHash string
		if err := tx.QueryRow(ctx,
			`SELECT blob_hash FROM mailing_list_held_post WHERE id = $1`, int64(id)).Scan(&blobHash); err != nil {
			return mapErr(err)
		}
		ct, err := tx.Exec(ctx, `
			UPDATE mailing_list_held_post
			   SET status = 'approved'
			 WHERE id = $1 AND status = 'approving'`,
			int64(id))
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrConflict
		}
		return decRef(ctx, tx, blobHash, now)
	})
	if err != nil {
		return store.MailingListHeldPost{}, err
	}
	return m.GetMailingListHeldPost(ctx, id)
}
