package storepg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.6 store.Metadata methods for
// JMAP for Contacts (REQ-PROTO-55, RFC 9553 JSContact) against
// Postgres. The schema-side commentary lives in
// migrations/0010_contacts.sql.

// -- AddressBook ------------------------------------------------------

const addressBookSelectColumnsPG = `
	id, principal_id, name, description, color_hex, sort_order,
	is_subscribed, is_default, rights_mask, created_at_us, updated_at_us, modseq`

func scanAddressBookPG(row pgx.Row) (store.AddressBook, error) {
	var (
		id, pid                      int64
		sortOrder, rightsMask        int64
		subscribed, isDefault        bool
		createdUs, updatedUs, modseq int64
		name, description            string
		color                        *string
	)
	err := row.Scan(&id, &pid, &name, &description, &color, &sortOrder,
		&subscribed, &isDefault, &rightsMask, &createdUs, &updatedUs, &modseq)
	if err != nil {
		return store.AddressBook{}, mapErr(err)
	}
	ab := store.AddressBook{
		ID:           store.AddressBookID(id),
		PrincipalID:  store.PrincipalID(pid),
		Name:         name,
		Description:  description,
		SortOrder:    int(sortOrder),
		IsSubscribed: subscribed,
		IsDefault:    isDefault,
		RightsMask:   store.ACLRights(rightsMask),
		CreatedAt:    fromMicros(createdUs),
		UpdatedAt:    fromMicros(updatedUs),
		ModSeq:       store.ModSeq(modseq),
	}
	if color != nil {
		v := *color
		ab.Color = &v
	}
	return ab, nil
}

func (m *metadata) InsertAddressBook(ctx context.Context, ab store.AddressBook) (store.AddressBookID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		if ab.IsDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE address_books SET is_default = FALSE, updated_at_us = $1
				   WHERE principal_id = $2 AND is_default = TRUE`,
				usMicros(now), int64(ab.PrincipalID)); err != nil {
				return mapErr(err)
			}
		}
		var color *string
		if ab.Color != nil {
			v := *ab.Color
			color = &v
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO address_books (principal_id, name, description, color_hex,
			  sort_order, is_subscribed, is_default, rights_mask,
			  created_at_us, updated_at_us, modseq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1)
			RETURNING id`,
			int64(ab.PrincipalID), ab.Name, ab.Description, color,
			int64(ab.SortOrder), ab.IsSubscribed, ab.IsDefault,
			int64(ab.RightsMask), usMicros(now), usMicros(now)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, ab.PrincipalID,
			store.EntityKindAddressBook, uint64(id), 0, store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.AddressBookID(id), nil
}

func (m *metadata) GetAddressBook(ctx context.Context, id store.AddressBookID) (store.AddressBook, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+addressBookSelectColumnsPG+` FROM address_books WHERE id = $1`,
		int64(id))
	return scanAddressBookPG(row)
}

func (m *metadata) ListAddressBooks(ctx context.Context, filter store.AddressBookFilter) ([]store.AddressBook, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	idx := 1
	if filter.PrincipalID != nil {
		clauses = append(clauses, fmt.Sprintf("principal_id = $%d", idx))
		args = append(args, int64(*filter.PrincipalID))
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
	q := `SELECT ` + addressBookSelectColumnsPG + ` FROM address_books`
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
	var out []store.AddressBook
	for rows.Next() {
		ab, err := scanAddressBookPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ab)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateAddressBook(ctx context.Context, ab store.AddressBook) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			pid        int64
			wasDefault bool
		)
		err := tx.QueryRow(ctx,
			`SELECT principal_id, is_default FROM address_books WHERE id = $1`,
			int64(ab.ID)).Scan(&pid, &wasDefault)
		if err != nil {
			return mapErr(err)
		}
		if ab.IsDefault && !wasDefault {
			if _, err := tx.Exec(ctx,
				`UPDATE address_books SET is_default = FALSE, updated_at_us = $1
				   WHERE principal_id = $2 AND is_default = TRUE AND id <> $3`,
				usMicros(now), pid, int64(ab.ID)); err != nil {
				return mapErr(err)
			}
		}
		var color *string
		if ab.Color != nil {
			v := *ab.Color
			color = &v
		}
		tag, err := tx.Exec(ctx, `
			UPDATE address_books SET
			  name = $1, description = $2, color_hex = $3, sort_order = $4,
			  is_subscribed = $5, is_default = $6, rights_mask = $7,
			  updated_at_us = $8, modseq = modseq + 1
			 WHERE id = $9`,
			ab.Name, ab.Description, color, int64(ab.SortOrder),
			ab.IsSubscribed, ab.IsDefault, int64(ab.RightsMask),
			usMicros(now), int64(ab.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindAddressBook, uint64(ab.ID), 0, store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteAddressBook(ctx context.Context, id store.AddressBookID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var pid int64
		err := tx.QueryRow(ctx,
			`SELECT principal_id FROM address_books WHERE id = $1`,
			int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		contactRows, err := tx.Query(ctx,
			`SELECT id FROM contacts WHERE address_book_id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		var contactIDs []int64
		for contactRows.Next() {
			var cid int64
			if err := contactRows.Scan(&cid); err != nil {
				contactRows.Close()
				return mapErr(err)
			}
			contactIDs = append(contactIDs, cid)
		}
		contactRows.Close()
		tag, err := tx.Exec(ctx,
			`DELETE FROM address_books WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		for _, cid := range contactIDs {
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindContact, uint64(cid), uint64(id),
				store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindAddressBook, uint64(id), 0,
			store.ChangeOpDestroyed, now)
	})
}

func (m *metadata) DefaultAddressBook(ctx context.Context, principalID store.PrincipalID) (store.AddressBook, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+addressBookSelectColumnsPG+`
		   FROM address_books WHERE principal_id = $1 AND is_default = TRUE
		  LIMIT 1`,
		int64(principalID))
	return scanAddressBookPG(row)
}

// -- Contact ----------------------------------------------------------

const contactSelectColumnsPG = `
	id, address_book_id, principal_id, uid, jscontact_json,
	display_name, given_name, surname, org_name, primary_email, search_blob,
	created_at_us, updated_at_us, modseq`

func scanContactPG(row pgx.Row) (store.Contact, error) {
	var (
		id, abID, pid                int64
		uid, dn, gn, sn, org         string
		email, searchBlob            string
		js                           []byte
		createdUs, updatedUs, modseq int64
	)
	err := row.Scan(&id, &abID, &pid, &uid, &js,
		&dn, &gn, &sn, &org, &email, &searchBlob,
		&createdUs, &updatedUs, &modseq)
	if err != nil {
		return store.Contact{}, mapErr(err)
	}
	return store.Contact{
		ID:            store.ContactID(id),
		AddressBookID: store.AddressBookID(abID),
		PrincipalID:   store.PrincipalID(pid),
		UID:           uid,
		JSContactJSON: js,
		DisplayName:   dn,
		GivenName:     gn,
		Surname:       sn,
		OrgName:       org,
		PrimaryEmail:  email,
		SearchBlob:    searchBlob,
		CreatedAt:     fromMicros(createdUs),
		UpdatedAt:     fromMicros(updatedUs),
		ModSeq:        store.ModSeq(modseq),
	}, nil
}

func (m *metadata) InsertContact(ctx context.Context, c store.Contact) (store.ContactID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO contacts (address_book_id, principal_id, uid, jscontact_json,
			  display_name, given_name, surname, org_name, primary_email, search_blob,
			  created_at_us, updated_at_us, modseq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1)
			RETURNING id`,
			int64(c.AddressBookID), int64(c.PrincipalID), c.UID, c.JSContactJSON,
			c.DisplayName, c.GivenName, c.Surname, c.OrgName,
			strings.ToLower(c.PrimaryEmail), strings.ToLower(c.SearchBlob),
			usMicros(now), usMicros(now)).Scan(&id)
		if err != nil {
			return mapErr(err)
		}
		return appendStateChange(ctx, tx, c.PrincipalID,
			store.EntityKindContact, uint64(id), uint64(c.AddressBookID),
			store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.ContactID(id), nil
}

func (m *metadata) GetContact(ctx context.Context, id store.ContactID) (store.Contact, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+contactSelectColumnsPG+` FROM contacts WHERE id = $1`,
		int64(id))
	return scanContactPG(row)
}

func (m *metadata) ListContacts(ctx context.Context, filter store.ContactFilter) ([]store.Contact, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var (
		clauses []string
		args    []any
	)
	idx := 1
	if filter.AddressBookID != nil {
		clauses = append(clauses, fmt.Sprintf("address_book_id = $%d", idx))
		args = append(args, int64(*filter.AddressBookID))
		idx++
	}
	if filter.PrincipalID != nil {
		clauses = append(clauses, fmt.Sprintf("principal_id = $%d", idx))
		args = append(args, int64(*filter.PrincipalID))
		idx++
	}
	if filter.Text != "" {
		clauses = append(clauses, fmt.Sprintf("search_blob LIKE $%d", idx))
		args = append(args, "%"+strings.ToLower(filter.Text)+"%")
		idx++
	}
	if filter.HasEmail != nil {
		clauses = append(clauses, fmt.Sprintf("primary_email = $%d", idx))
		args = append(args, strings.ToLower(*filter.HasEmail))
		idx++
	}
	if filter.UID != nil {
		clauses = append(clauses, fmt.Sprintf("uid = $%d", idx))
		args = append(args, *filter.UID)
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
	q := `SELECT ` + contactSelectColumnsPG + ` FROM contacts`
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
	var out []store.Contact
	for rows.Next() {
		c, err := scanContactPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateContact(ctx context.Context, c store.Contact) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			pid int64
			ab  int64
		)
		err := tx.QueryRow(ctx,
			`SELECT principal_id, address_book_id FROM contacts WHERE id = $1`,
			int64(c.ID)).Scan(&pid, &ab)
		if err != nil {
			return mapErr(err)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE contacts SET
			  jscontact_json = $1, display_name = $2, given_name = $3, surname = $4,
			  org_name = $5, primary_email = $6, search_blob = $7,
			  updated_at_us = $8, modseq = modseq + 1
			 WHERE id = $9`,
			c.JSContactJSON, c.DisplayName, c.GivenName, c.Surname,
			c.OrgName, strings.ToLower(c.PrimaryEmail), strings.ToLower(c.SearchBlob),
			usMicros(now), int64(c.ID))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindContact, uint64(c.ID), uint64(ab),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteContact(ctx context.Context, id store.ContactID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var (
			pid int64
			ab  int64
		)
		err := tx.QueryRow(ctx,
			`SELECT principal_id, address_book_id FROM contacts WHERE id = $1`,
			int64(id)).Scan(&pid, &ab)
		if err != nil {
			return mapErr(err)
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM contacts WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindContact, uint64(id), uint64(ab),
			store.ChangeOpDestroyed, now)
	})
}
