package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata hosted-mailing-list methods
// (Stage 1 storage foundation, epic #183, REQ-MLIST-01..12). Schema
// commentary lives in migrations/0085_mailing_lists.sql.

const mailingListColumns = `
	id, principal_id, posting_address, domain, display_name, owner_id,
	subject_tag, arc_seal, posting_policy, subscribe_policy,
	bounce_policy_json, archive_mailbox_id, max_message_size_bytes,
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
		createdUs, updatedUs     int64
	)
	if err := row.Scan(&id, &principalID, &postingAddress, &domain, &displayName, &ownerID,
		&subjectTag, &arcSeal, &postingPolicy, &subscribePolicy,
		&bouncePolicyJSON, &archiveMailboxID, &maxMessageSize,
		&createdUs, &updatedUs); err != nil {
		return store.MailingList{}, mapErr(err)
	}
	l := store.MailingList{
		ID:                  store.MailingListID(id),
		PrincipalID:         store.PrincipalID(principalID),
		PostingAddress:      postingAddress,
		Domain:              domain,
		DisplayName:         displayName,
		OwnerID:             store.PrincipalID(ownerID),
		ARCSeal:             arcSeal != 0,
		PostingPolicy:       store.MailingListPostingPolicy(postingPolicy),
		SubscribePolicy:     store.MailingListSubscribePolicy(subscribePolicy),
		BouncePolicyJSON:    bouncePolicyJSON,
		MaxMessageSizeBytes: maxMessageSize,
		CreatedAt:           fromMicros(createdUs),
		UpdatedAt:           fromMicros(updatedUs),
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
				created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(l.PrincipalID), address, domain, l.DisplayName,
			int64(l.OwnerID), subjectTag, boolToInt(l.ARCSeal),
			string(postingPolicy), string(subscribePolicy),
			bouncePolicyJSON, archiveMailboxID, l.MaxMessageSizeBytes,
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
			       updated_at_us = ?
			 WHERE id = ?`,
			address, domain, l.DisplayName, int64(l.OwnerID),
			subjectTag, boolToInt(l.ARCSeal), string(l.PostingPolicy), string(l.SubscribePolicy),
			bouncePolicyJSON, archiveMailboxID, l.MaxMessageSizeBytes,
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
