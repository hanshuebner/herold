package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the Phase-2 Wave 2.6 store.Metadata methods for
// JMAP for Contacts (REQ-PROTO-55, RFC 9553 JSContact). The schema-side
// commentary lives in migrations/0010_contacts.sql; helpers reused from
// metadata.go (mapErr, runTx, usMicros, fromMicros, appendStateChange)
// keep the surface narrow.

// -- AddressBook ------------------------------------------------------

const addressBookSelectColumns = `
	id, principal_id, name, description, color_hex, sort_order,
	is_subscribed, is_default, rights_mask, created_at_us, updated_at_us, modseq`

func scanAddressBook(row rowLike) (store.AddressBook, error) {
	var (
		id, pid                      int64
		sortOrder, rightsMask        int64
		subscribed, isDefault        int64
		createdUs, updatedUs, modseq int64
		name, description            string
		color                        sql.NullString
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
		IsSubscribed: subscribed != 0,
		IsDefault:    isDefault != 0,
		RightsMask:   store.ACLRights(rightsMask),
		CreatedAt:    fromMicros(createdUs),
		UpdatedAt:    fromMicros(updatedUs),
		ModSeq:       store.ModSeq(modseq),
	}
	if color.Valid {
		v := color.String
		ab.Color = &v
	}
	return ab, nil
}

func (m *metadata) InsertAddressBook(ctx context.Context, ab store.AddressBook) (store.AddressBookID, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// IsDefault enforcement: when the caller asks for default, flip
		// the previous default off in the same tx. The partial unique
		// index would reject the second is_default=1 otherwise.
		if ab.IsDefault {
			if _, err := tx.ExecContext(ctx,
				`UPDATE address_books SET is_default = 0, updated_at_us = ?
				   WHERE principal_id = ? AND is_default = 1`,
				usMicros(now), int64(ab.PrincipalID)); err != nil {
				return mapErr(err)
			}
		}
		var color any
		if ab.Color != nil {
			color = *ab.Color
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO address_books (principal_id, name, description, color_hex,
			  sort_order, is_subscribed, is_default, rights_mask,
			  created_at_us, updated_at_us, modseq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			int64(ab.PrincipalID), ab.Name, ab.Description, color,
			int64(ab.SortOrder), boolToInt(ab.IsSubscribed), boolToInt(ab.IsDefault),
			int64(ab.RightsMask), usMicros(now), usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
		return appendStateChange(ctx, tx, ab.PrincipalID,
			store.EntityKindAddressBook, uint64(id), 0, store.ChangeOpCreated, now)
	})
	if err != nil {
		return 0, err
	}
	return store.AddressBookID(id), nil
}

func (m *metadata) GetAddressBook(ctx context.Context, id store.AddressBookID) (store.AddressBook, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+addressBookSelectColumns+` FROM address_books WHERE id = ?`,
		int64(id))
	return scanAddressBook(row)
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
	if filter.PrincipalID != nil {
		clauses = append(clauses, "principal_id = ?")
		args = append(args, int64(*filter.PrincipalID))
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, "modseq > ?")
		args = append(args, int64(filter.AfterModSeq))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + addressBookSelectColumns + ` FROM address_books`
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
	var out []store.AddressBook
	for rows.Next() {
		ab, err := scanAddressBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ab)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateAddressBook(ctx context.Context, ab store.AddressBook) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Lookup current row to discover the owning principal (for the
		// state-change feed) and to decide whether IsDefault changed.
		var (
			pid        int64
			wasDefault int64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, is_default FROM address_books WHERE id = ?`,
			int64(ab.ID)).Scan(&pid, &wasDefault)
		if err != nil {
			return mapErr(err)
		}
		if ab.IsDefault && wasDefault == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE address_books SET is_default = 0, updated_at_us = ?
				   WHERE principal_id = ? AND is_default = 1 AND id <> ?`,
				usMicros(now), pid, int64(ab.ID)); err != nil {
				return mapErr(err)
			}
		}
		var color any
		if ab.Color != nil {
			color = *ab.Color
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE address_books SET
			  name = ?, description = ?, color_hex = ?, sort_order = ?,
			  is_subscribed = ?, is_default = ?, rights_mask = ?,
			  updated_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`,
			ab.Name, ab.Description, color, int64(ab.SortOrder),
			boolToInt(ab.IsSubscribed), boolToInt(ab.IsDefault), int64(ab.RightsMask),
			usMicros(now), int64(ab.ID))
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
			store.EntityKindAddressBook, uint64(ab.ID), 0, store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteAddressBook(ctx context.Context, id store.AddressBookID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var pid int64
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id FROM address_books WHERE id = ?`,
			int64(id)).Scan(&pid)
		if err != nil {
			return mapErr(err)
		}
		// Capture the contact IDs before the FK cascade wipes them so we
		// can append per-contact destroyed rows.
		contactRows, err := tx.QueryContext(ctx,
			`SELECT id FROM contacts WHERE address_book_id = ?`, int64(id))
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
		res, err := tx.ExecContext(ctx,
			`DELETE FROM address_books WHERE id = ?`, int64(id))
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
		// Per-contact destroyed rows first, then the address book row.
		for _, cid := range contactIDs {
			if err := appendStateChange(ctx, tx, store.PrincipalID(pid),
				store.EntityKindContact, uint64(cid), uint64(id), store.ChangeOpDestroyed, now); err != nil {
				return err
			}
		}
		return appendStateChange(ctx, tx, store.PrincipalID(pid),
			store.EntityKindAddressBook, uint64(id), 0, store.ChangeOpDestroyed, now)
	})
}

func (m *metadata) DefaultAddressBook(ctx context.Context, principalID store.PrincipalID) (store.AddressBook, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+addressBookSelectColumns+`
		   FROM address_books WHERE principal_id = ? AND is_default = 1
		  LIMIT 1`,
		int64(principalID))
	return scanAddressBook(row)
}

// -- Contact ----------------------------------------------------------

const contactSelectColumns = `
	id, address_book_id, principal_id, uid, jscontact_json,
	display_name, given_name, surname, org_name, primary_email, search_blob,
	created_at_us, updated_at_us, modseq`

func scanContact(row rowLike) (store.Contact, error) {
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
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO contacts (address_book_id, principal_id, uid, jscontact_json,
			  display_name, given_name, surname, org_name, primary_email, search_blob,
			  created_at_us, updated_at_us, modseq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			int64(c.AddressBookID), int64(c.PrincipalID), c.UID, c.JSContactJSON,
			c.DisplayName, c.GivenName, c.Surname, c.OrgName,
			strings.ToLower(c.PrimaryEmail), strings.ToLower(c.SearchBlob),
			usMicros(now), usMicros(now))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("storesqlite: last insert id: %w", err)
		}
		id = n
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
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+contactSelectColumns+` FROM contacts WHERE id = ?`,
		int64(id))
	return scanContact(row)
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
	if filter.AddressBookID != nil {
		clauses = append(clauses, "address_book_id = ?")
		args = append(args, int64(*filter.AddressBookID))
	}
	if filter.PrincipalID != nil {
		clauses = append(clauses, "principal_id = ?")
		args = append(args, int64(*filter.PrincipalID))
	}
	if filter.Text != "" {
		clauses = append(clauses, "search_blob LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.Text)+"%")
	}
	if filter.HasEmail != nil {
		clauses = append(clauses, "primary_email = ?")
		args = append(args, strings.ToLower(*filter.HasEmail))
	}
	if filter.UID != nil {
		clauses = append(clauses, "uid = ?")
		args = append(args, *filter.UID)
	}
	if filter.AfterModSeq != 0 {
		clauses = append(clauses, "modseq > ?")
		args = append(args, int64(filter.AfterModSeq))
	}
	if filter.AfterID != 0 {
		clauses = append(clauses, "id > ?")
		args = append(args, int64(filter.AfterID))
	}
	q := `SELECT ` + contactSelectColumns + ` FROM contacts`
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
	var out []store.Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *metadata) UpdateContact(ctx context.Context, c store.Contact) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			pid int64
			ab  int64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, address_book_id FROM contacts WHERE id = ?`,
			int64(c.ID)).Scan(&pid, &ab)
		if err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE contacts SET
			  jscontact_json = ?, display_name = ?, given_name = ?, surname = ?,
			  org_name = ?, primary_email = ?, search_blob = ?,
			  updated_at_us = ?, modseq = modseq + 1
			 WHERE id = ?`,
			c.JSContactJSON, c.DisplayName, c.GivenName, c.Surname,
			c.OrgName, strings.ToLower(c.PrimaryEmail), strings.ToLower(c.SearchBlob),
			usMicros(now), int64(c.ID))
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
			store.EntityKindContact, uint64(c.ID), uint64(ab),
			store.ChangeOpUpdated, now)
	})
}

func (m *metadata) DeleteContact(ctx context.Context, id store.ContactID) error {
	now := m.s.clock.Now().UTC()
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var (
			pid int64
			ab  int64
		)
		err := tx.QueryRowContext(ctx,
			`SELECT principal_id, address_book_id FROM contacts WHERE id = ?`,
			int64(id)).Scan(&pid, &ab)
		if err != nil {
			return mapErr(err)
		}
		res, err := tx.ExecContext(ctx,
			`DELETE FROM contacts WHERE id = ?`, int64(id))
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
			store.EntityKindContact, uint64(id), uint64(ab),
			store.ChangeOpDestroyed, now)
	})
}

// silence unused-import warnings in case time is removed by future edits
var _ = time.Time{}
