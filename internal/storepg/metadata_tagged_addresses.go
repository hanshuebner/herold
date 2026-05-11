package storepg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the tagged-address store methods for the
// Postgres backend (REQ-TAG-10..11, REQ-TAG-30..32). Mirrors
// internal/storesqlite/metadata_tagged_addresses.go. Schema lives in
// migrations/0050_tagged_address_filters.sql and
// migrations/0051_tagged_address_dismissals.sql.

const taggedAddressFilterSelectColsPG = `
	id, principal_id, base_identity_id, suffix, action, label_name,
	created_at_us, updated_at_us`

func scanTaggedAddressFilterPG(row rowLike) (store.TaggedAddressFilter, error) {
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

// validateTaggedAddressActionPG mirrors the sqlite-side helper. Kept
// per-backend so the package boundary is independent of storesqlite.
func validateTaggedAddressActionPG(action string) error {
	switch action {
	case store.TaggedAddressActionLabel,
		store.TaggedAddressActionLabelArchive,
		store.TaggedAddressActionLabelArchiveRead:
		return nil
	}
	return fmt.Errorf("%w: tagged address action %q not one of {label, label_archive, label_archive_read}",
		store.ErrInvalidArgument, action)
}

// newOpaqueIDPG returns a 32-hex-char random opaque id. Mirrors the
// sqlite-side helper; production wires crypto/rand and tests inject a
// deterministic reader via the rs argument to Open.
func newOpaqueIDPG(rs io.Reader) (string, error) {
	if rs == nil {
		rs = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(rs, b[:]); err != nil {
		return "", fmt.Errorf("storepg: opaque id entropy: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (m *metadata) GetTaggedAddressFilter(ctx context.Context, principalID store.PrincipalID, baseIdentityID, suffix string) (store.TaggedAddressFilter, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+taggedAddressFilterSelectColsPG+`
		   FROM tagged_address_filters
		  WHERE principal_id = $1 AND base_identity_id = $2 AND suffix = $3`,
		int64(principalID), baseIdentityID, strings.ToLower(suffix))
	return scanTaggedAddressFilterPG(row)
}

func (m *metadata) ListTaggedAddressFiltersForPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.TaggedAddressFilter, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT `+taggedAddressFilterSelectColsPG+`
		   FROM tagged_address_filters
		  WHERE principal_id = $1
		  ORDER BY base_identity_id ASC, suffix ASC`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.TaggedAddressFilter
	for rows.Next() {
		f, err := scanTaggedAddressFilterPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (m *metadata) InsertTaggedAddressFilter(ctx context.Context, f store.TaggedAddressFilter) error {
	if err := validateTaggedAddressActionPG(f.Action); err != nil {
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
		id, err := newOpaqueIDPG(m.s.randReader)
		if err != nil {
			return err
		}
		f.ID = id
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var count int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM tagged_address_filters WHERE principal_id = $1`,
			int64(f.PrincipalID)).Scan(&count); err != nil {
			return mapErr(err)
		}
		if count >= int64(store.MaxTaggedAddressFiltersPerPrincipal) {
			return fmt.Errorf("principal %d already holds %d tagged-address filters: %w",
				int64(f.PrincipalID), count, store.ErrTooManyFilters)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO tagged_address_filters
			  (id, principal_id, base_identity_id, suffix, action, label_name,
			   created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			f.ID, int64(f.PrincipalID), f.BaseIdentityID, suffix,
			f.Action, f.LabelName, nowUs, nowUs)
		return mapErr(err)
	})
}

func (m *metadata) UpdateTaggedAddressFilter(ctx context.Context, id, action, labelName string) error {
	if err := validateTaggedAddressActionPG(action); err != nil {
		return err
	}
	if labelName == "" {
		return fmt.Errorf("%w: tagged address filter: empty label_name", store.ErrInvalidArgument)
	}
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tagged_address_filters
			   SET action = $1, label_name = $2, updated_at_us = $3
			 WHERE id = $4`,
			action, labelName, usMicros(m.s.clock.Now().UTC()), id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
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
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT suffix, base_identity_id, principal_id
			   FROM tagged_address_filters WHERE id = $1`, id).
			Scan(&suffix, &baseIdentityID, &principalID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM tagged_address_filters WHERE id = $1`, id); err != nil {
			return mapErr(err)
		}
		// REQ-TAG-61: cascade the matching dismissal so the user is
		// prompted again next time mail to that suffix arrives.
		if _, err := tx.Exec(ctx,
			`DELETE FROM tagged_address_dismissals
			  WHERE principal_id = $1 AND base_identity_id = $2 AND suffix = $3`,
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
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var existing int64
		err := tx.QueryRow(ctx,
			`SELECT 1 FROM tagged_address_dismissals
			  WHERE principal_id = $1 AND base_identity_id = $2 AND suffix = $3`,
			int64(d.PrincipalID), d.BaseIdentityID, suffix).Scan(&existing)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return mapErr(err)
		}
		var count int64
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM tagged_address_dismissals WHERE principal_id = $1`,
			int64(d.PrincipalID)).Scan(&count); err != nil {
			return mapErr(err)
		}
		if count >= int64(store.MaxTaggedAddressDismissalsPerPrincipal) {
			return fmt.Errorf("principal %d already holds %d tagged-address dismissals: %w",
				int64(d.PrincipalID), count, store.ErrTooManyDismissals)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO tagged_address_dismissals
			  (principal_id, base_identity_id, suffix, dismissed_at_us)
			VALUES ($1, $2, $3, $4)`,
			int64(d.PrincipalID), d.BaseIdentityID, suffix, nowUs)
		return mapErr(err)
	})
}

func (m *metadata) DeleteTaggedAddressDismissal(ctx context.Context, principalID store.PrincipalID, baseIdentityID, suffix string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM tagged_address_dismissals
			  WHERE principal_id = $1 AND base_identity_id = $2 AND suffix = $3`,
			int64(principalID), baseIdentityID, strings.ToLower(suffix))
		return mapErr(err)
	})
}

func (m *metadata) ListTaggedAddressDismissalsForPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.TaggedAddressDismissal, error) {
	rows, err := m.s.pool.Query(ctx,
		`SELECT principal_id, base_identity_id, suffix, dismissed_at_us
		   FROM tagged_address_dismissals
		  WHERE principal_id = $1
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
func (m *metadata) LookupTaggedAddressFilterForRecipient(ctx context.Context, principalID store.PrincipalID, baseEmail, suffix string) (store.TaggedAddressFilter, error) {
	row := m.s.pool.QueryRow(ctx,
		`SELECT f.id, f.principal_id, f.base_identity_id, f.suffix,
		         f.action, f.label_name, f.created_at_us, f.updated_at_us
		    FROM tagged_address_filters AS f
		    JOIN jmap_identities        AS i ON i.id = f.base_identity_id
		   WHERE f.principal_id = $1
		     AND lower(i.email) = $2
		     AND f.suffix      = $3
		   LIMIT 1`,
		int64(principalID), strings.ToLower(baseEmail), strings.ToLower(suffix))
	return scanTaggedAddressFilterPG(row)
}

func (m *metadata) HasTaggedAddressFilterOrDismissal(ctx context.Context, principalID store.PrincipalID, baseIdentityID, suffix string) (bool, bool, error) {
	lower := strings.ToLower(suffix)
	var hasFilter, hasDismissal bool
	err := m.s.pool.QueryRow(ctx,
		`SELECT
		  EXISTS (SELECT 1 FROM tagged_address_filters
		          WHERE principal_id = $1 AND base_identity_id = $2 AND suffix = $3),
		  EXISTS (SELECT 1 FROM tagged_address_dismissals
		          WHERE principal_id = $4 AND base_identity_id = $5 AND suffix = $6)`,
		int64(principalID), baseIdentityID, lower,
		int64(principalID), baseIdentityID, lower).
		Scan(&hasFilter, &hasDismissal)
	if err != nil {
		return false, false, mapErr(err)
	}
	return hasFilter, hasDismissal, nil
}
