package storesqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the tagged-address store methods for the SQLite
// backend (REQ-TAG-10..11, REQ-TAG-30..32). Schema lives in
// migrations/0050_tagged_address_filters.sql and
// migrations/0051_tagged_address_dismissals.sql.

const taggedAddressFilterSelectCols = `
	id, principal_id, base_identity_id, suffix, action, label_name,
	created_at_us, updated_at_us`

// scanTaggedAddressFilter scans one row of the tagged_address_filters
// table into a TaggedAddressFilter. Column order must match
// taggedAddressFilterSelectCols verbatim.
func scanTaggedAddressFilter(row rowLike) (store.TaggedAddressFilter, error) {
	var (
		id, baseIdentityID, suffix, action, labelName string
		pid                                           int64
		createdUs, updatedUs                          int64
	)
	if err := row.Scan(&id, &pid, &baseIdentityID, &suffix, &action, &labelName,
		&createdUs, &updatedUs); err != nil {
		return store.TaggedAddressFilter{}, mapErr(err)
	}
	return store.TaggedAddressFilter{
		ID:             id,
		PrincipalID:    store.PrincipalID(pid),
		BaseIdentityID: baseIdentityID,
		Suffix:         suffix,
		Action:         action,
		LabelName:      labelName,
		CreatedAt:      fromMicros(createdUs),
		UpdatedAt:      fromMicros(updatedUs),
	}, nil
}

// validateTaggedAddressAction rejects unknown action tokens before any
// DB mutation. The CHECK constraint on the table is a backstop; doing
// the check in Go produces a clean ErrInvalidArgument the caller can
// surface as a typed wire error.
func validateTaggedAddressAction(action string) error {
	switch action {
	case store.TaggedAddressActionLabel,
		store.TaggedAddressActionLabelArchive,
		store.TaggedAddressActionLabelArchiveRead:
		return nil
	}
	return fmt.Errorf("%w: tagged address action %q not one of {label, label_archive, label_archive_read}",
		store.ErrInvalidArgument, action)
}

// newOpaqueID returns a 32-hex-char random opaque identifier. The
// caller's randReader is used when set; otherwise crypto/rand. The
// store's Open path wires crypto/rand by default; tests inject a
// deterministic reader so IDs are reproducible across runs.
func newOpaqueID(rs io.Reader) (string, error) {
	if rs == nil {
		rs = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(rs, b[:]); err != nil {
		return "", fmt.Errorf("storesqlite: opaque id entropy: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (m *metadata) GetTaggedAddressFilter(ctx context.Context, principalID store.PrincipalID, baseIdentityID, suffix string) (store.TaggedAddressFilter, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT `+taggedAddressFilterSelectCols+`
		   FROM tagged_address_filters
		  WHERE principal_id = ? AND base_identity_id = ? AND suffix = ?`,
		int64(principalID), baseIdentityID, strings.ToLower(suffix))
	return scanTaggedAddressFilter(row)
}

func (m *metadata) ListTaggedAddressFiltersForPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.TaggedAddressFilter, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT `+taggedAddressFilterSelectCols+`
		   FROM tagged_address_filters
		  WHERE principal_id = ?
		  ORDER BY base_identity_id ASC, suffix ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.TaggedAddressFilter
	for rows.Next() {
		f, err := scanTaggedAddressFilter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (m *metadata) InsertTaggedAddressFilter(ctx context.Context, f store.TaggedAddressFilter) error {
	if err := validateTaggedAddressAction(f.Action); err != nil {
		return err
	}
	if f.BaseIdentityID == "" {
		return fmt.Errorf("%w: tagged address filter: empty base_identity_id", store.ErrInvalidArgument)
	}
	if f.LabelName == "" {
		return fmt.Errorf("%w: tagged address filter: empty label_name", store.ErrInvalidArgument)
	}
	suffix := strings.ToLower(f.Suffix)
	if suffix == "" {
		return fmt.Errorf("%w: tagged address filter: empty suffix", store.ErrInvalidArgument)
	}
	if f.ID == "" {
		id, err := newOpaqueID(m.s.randReader)
		if err != nil {
			return err
		}
		f.ID = id
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Cap enforcement (REQ-TAG-11): refuse insertion when the
		// principal already holds MaxTaggedAddressFiltersPerPrincipal
		// rows. The SELECT runs inside the writer transaction so two
		// concurrent inserts cannot both race past the cap.
		var count int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tagged_address_filters WHERE principal_id = ?`,
			int64(f.PrincipalID)).Scan(&count); err != nil {
			return mapErr(err)
		}
		if count >= int64(store.MaxTaggedAddressFiltersPerPrincipal) {
			return fmt.Errorf("principal %d already holds %d tagged-address filters: %w",
				int64(f.PrincipalID), count, store.ErrTooManyFilters)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tagged_address_filters
			  (id, principal_id, base_identity_id, suffix, action, label_name,
			   created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, int64(f.PrincipalID), f.BaseIdentityID, suffix,
			f.Action, f.LabelName, nowUs, nowUs)
		return mapErr(err)
	})
}

func (m *metadata) UpdateTaggedAddressFilter(ctx context.Context, id, action, labelName string) error {
	if err := validateTaggedAddressAction(action); err != nil {
		return err
	}
	if labelName == "" {
		return fmt.Errorf("%w: tagged address filter: empty label_name", store.ErrInvalidArgument)
	}
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE tagged_address_filters
			   SET action = ?, label_name = ?, updated_at_us = ?
			 WHERE id = ?`,
			action, labelName, usMicros(m.s.clock.Now().UTC()), id)
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

func (m *metadata) DeleteTaggedAddressFilter(ctx context.Context, id string) (string, string, store.PrincipalID, error) {
	var (
		suffix         string
		baseIdentityID string
		principalID    int64
	)
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// SELECT the row first so the caller learns the (suffix,
		// base_identity, principal) triple that the cascade in
		// REQ-TAG-61 needs. The SELECT runs inside the writer
		// transaction so a concurrent DELETE cannot remove the row
		// between read and follow-up cascade.
		err := tx.QueryRowContext(ctx,
			`SELECT suffix, base_identity_id, principal_id
			   FROM tagged_address_filters WHERE id = ?`, id).
			Scan(&suffix, &baseIdentityID, &principalID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM tagged_address_filters WHERE id = ?`, id); err != nil {
			return mapErr(err)
		}
		// REQ-TAG-61: removing a filter MUST also clear any matching
		// dismissal so the user is prompted again next time mail to
		// that suffix arrives.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM tagged_address_dismissals
			  WHERE principal_id = ? AND base_identity_id = ? AND suffix = ?`,
			principalID, baseIdentityID, suffix); err != nil {
			return mapErr(err)
		}
		return nil
	})
	if err != nil {
		return "", "", 0, err
	}
	return suffix, baseIdentityID, store.PrincipalID(principalID), nil
}

func (m *metadata) InsertTaggedAddressDismissal(ctx context.Context, d store.TaggedAddressDismissal) error {
	if d.BaseIdentityID == "" {
		return fmt.Errorf("%w: tagged address dismissal: empty base_identity_id", store.ErrInvalidArgument)
	}
	suffix := strings.ToLower(d.Suffix)
	if suffix == "" {
		return fmt.Errorf("%w: tagged address dismissal: empty suffix", store.ErrInvalidArgument)
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Idempotent check first: if the row already exists, return
		// nil without touching the cap. This matches REQ-TAG-60 (the
		// REST endpoint returns 200 on duplicate dismissal).
		var existing int64
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM tagged_address_dismissals
			  WHERE principal_id = ? AND base_identity_id = ? AND suffix = ?`,
			int64(d.PrincipalID), d.BaseIdentityID, suffix).Scan(&existing)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return mapErr(err)
		}
		// Cap enforcement (REQ-TAG-11) — only for genuinely-new rows.
		var count int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tagged_address_dismissals WHERE principal_id = ?`,
			int64(d.PrincipalID)).Scan(&count); err != nil {
			return mapErr(err)
		}
		if count >= int64(store.MaxTaggedAddressDismissalsPerPrincipal) {
			return fmt.Errorf("principal %d already holds %d tagged-address dismissals: %w",
				int64(d.PrincipalID), count, store.ErrTooManyDismissals)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO tagged_address_dismissals
			  (principal_id, base_identity_id, suffix, dismissed_at_us)
			VALUES (?, ?, ?, ?)`,
			int64(d.PrincipalID), d.BaseIdentityID, suffix, nowUs)
		return mapErr(err)
	})
}

func (m *metadata) DeleteTaggedAddressDismissal(ctx context.Context, principalID store.PrincipalID, baseIdentityID, suffix string) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		// Idempotent: no error when no row matches.
		_, err := tx.ExecContext(ctx,
			`DELETE FROM tagged_address_dismissals
			  WHERE principal_id = ? AND base_identity_id = ? AND suffix = ?`,
			int64(principalID), baseIdentityID, strings.ToLower(suffix))
		return mapErr(err)
	})
}

func (m *metadata) ListTaggedAddressDismissalsForPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.TaggedAddressDismissal, error) {
	rows, err := m.s.db.QueryContext(ctx,
		`SELECT principal_id, base_identity_id, suffix, dismissed_at_us
		   FROM tagged_address_dismissals
		  WHERE principal_id = ?
		  ORDER BY base_identity_id ASC, suffix ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.TaggedAddressDismissal
	for rows.Next() {
		var (
			pid           int64
			baseID        string
			suffix        string
			dismissedAtUs int64
		)
		if err := rows.Scan(&pid, &baseID, &suffix, &dismissedAtUs); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, store.TaggedAddressDismissal{
			PrincipalID:    store.PrincipalID(pid),
			BaseIdentityID: baseID,
			Suffix:         suffix,
			DismissedAt:    fromMicros(dismissedAtUs),
		})
	}
	return out, rows.Err()
}

// LookupTaggedAddressFilterForRecipient joins jmap_identities and
// tagged_address_filters to find the filter (if any) that matches an
// inbound recipient's (baseEmail, suffix) pair under principalID. See
// the store.Metadata interface docstring for the full contract.
//
// The join is by jmap_identities.email (case-insensitive); the row's
// verified_at_us is NOT consulted here because the verification gate
// lives at filter-creation (REQ-IDENT-60 in REST/JMAP), not at delivery
// time. Once a filter exists it routes mail regardless of subsequent
// verification state — the v1 spec offers no admin-action to unverify an
// already-verified identity, and a stuck-unverified identity cannot
// have produced a filter in the first place.
func (m *metadata) LookupTaggedAddressFilterForRecipient(ctx context.Context, principalID store.PrincipalID, baseEmail, suffix string) (store.TaggedAddressFilter, error) {
	row := m.s.db.QueryRowContext(ctx,
		`SELECT f.id, f.principal_id, f.base_identity_id, f.suffix,
		         f.action, f.label_name, f.created_at_us, f.updated_at_us
		    FROM tagged_address_filters AS f
		    JOIN jmap_identities        AS i ON i.id = f.base_identity_id
		   WHERE f.principal_id = ?
		     AND lower(i.email) = ?
		     AND f.suffix      = ?
		   LIMIT 1`,
		int64(principalID), strings.ToLower(baseEmail), strings.ToLower(suffix))
	return scanTaggedAddressFilter(row)
}

func (m *metadata) HasTaggedAddressFilterOrDismissal(ctx context.Context, principalID store.PrincipalID, baseIdentityID, suffix string) (bool, bool, error) {
	lower := strings.ToLower(suffix)
	var hasFilter, hasDismissal int64
	// Two-table EXISTS probe in a single round trip. The SQLite planner
	// satisfies each EXISTS via the indexes on the respective parents
	// (UNIQUE on filters, PK on dismissals).
	err := m.s.db.QueryRowContext(ctx,
		`SELECT
		  EXISTS (SELECT 1 FROM tagged_address_filters
		          WHERE principal_id = ? AND base_identity_id = ? AND suffix = ?),
		  EXISTS (SELECT 1 FROM tagged_address_dismissals
		          WHERE principal_id = ? AND base_identity_id = ? AND suffix = ?)`,
		int64(principalID), baseIdentityID, lower,
		int64(principalID), baseIdentityID, lower).
		Scan(&hasFilter, &hasDismissal)
	if err != nil {
		return false, false, mapErr(err)
	}
	return hasFilter != 0, hasDismissal != 0, nil
}
