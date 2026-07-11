package storepg

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata resource-grant methods (epic #182,
// REQ-AC-01..05). Mirrors storesqlite/metadata_grants.go. Schema commentary
// lives in migrations/0079_grants.sql.

const grantColumns = `subject_kind, subject_id, resource_kind, resource_id,
	level, provenance, granted_by, granted_at_us, last_asserted_at_us`

func grantDefaults(g store.Grant) store.Grant {
	if g.SubjectKind == "" {
		g.SubjectKind = store.GrantSubjectPrincipal
	}
	if g.Provenance == "" {
		g.Provenance = store.GrantProvenanceLocal
	}
	return g
}

func (m *metadata) InsertGrant(ctx context.Context, g store.Grant) (store.Grant, error) {
	g = grantDefaults(g)
	now := m.s.clock.Now().UTC()
	var grantedBy *int64
	if g.GrantedBy != nil {
		v := int64(*g.GrantedBy)
		grantedBy = &v
	}
	var lastAsserted *int64
	if g.LastAssertedAt != nil {
		v := usMicros(*g.LastAssertedAt)
		lastAsserted = &v
	}
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO grants (`+grantColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (subject_kind, subject_id, resource_kind, resource_id, provenance)
			DO NOTHING`,
			string(g.SubjectKind), int64(g.SubjectID), string(g.ResourceKind), g.ResourceID,
			string(g.Level), g.Provenance, grantedBy, usMicros(now), lastAsserted)
		return mapErr(err)
	})
	if err != nil {
		return store.Grant{}, err
	}
	row := m.s.pool.QueryRow(ctx, `
		SELECT id, `+grantColumns+`
		  FROM grants
		 WHERE subject_kind = $1 AND subject_id = $2 AND resource_kind = $3
		   AND resource_id = $4 AND provenance = $5`,
		string(g.SubjectKind), int64(g.SubjectID), string(g.ResourceKind),
		g.ResourceID, g.Provenance)
	return scanGrantRow(row)
}

func (m *metadata) DeleteGrant(ctx context.Context, g store.Grant) error {
	g = grantDefaults(g)
	return m.runTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			DELETE FROM grants
			 WHERE subject_kind = $1 AND subject_id = $2 AND resource_kind = $3
			   AND resource_id = $4 AND provenance = $5`,
			string(g.SubjectKind), int64(g.SubjectID), string(g.ResourceKind),
			g.ResourceID, g.Provenance)
		if err != nil {
			return mapErr(err)
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListGrantsForPrincipal(ctx context.Context, principalID store.PrincipalID) ([]store.Grant, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, `+grantColumns+`
		  FROM grants
		 WHERE subject_kind = 'principal' AND subject_id = $1
		 ORDER BY id`,
		int64(principalID))
	if err != nil {
		return nil, mapErr(err)
	}
	return scanGrantRows(rows)
}

func (m *metadata) ListGrantsOnResource(ctx context.Context, resourceKind store.GrantResourceKind, resourceID string) ([]store.Grant, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, `+grantColumns+`
		  FROM grants
		 WHERE resource_kind = $1 AND resource_id = $2
		 ORDER BY id`,
		string(resourceKind), resourceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return scanGrantRows(rows)
}

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrantRow(sc rowScanner) (store.Grant, error) {
	var g store.Grant
	var id, subjectID, grantedAtUs int64
	var subjectKind, resourceKind, level, provenance string
	var grantedBy, lastAssertedAtUs sql.NullInt64
	if err := sc.Scan(&id, &subjectKind, &subjectID, &resourceKind, &g.ResourceID,
		&level, &provenance, &grantedBy, &grantedAtUs, &lastAssertedAtUs); err != nil {
		return store.Grant{}, mapErr(err)
	}
	g.ID = store.GrantID(id)
	g.SubjectKind = store.GrantSubjectKind(subjectKind)
	g.SubjectID = uint64(subjectID)
	g.ResourceKind = store.GrantResourceKind(resourceKind)
	g.Level = store.GrantLevel(level)
	g.Provenance = provenance
	if grantedBy.Valid {
		pid := store.PrincipalID(grantedBy.Int64)
		g.GrantedBy = &pid
	}
	g.GrantedAt = fromMicros(grantedAtUs)
	if lastAssertedAtUs.Valid {
		t := fromMicros(lastAssertedAtUs.Int64)
		g.LastAssertedAt = &t
	}
	return g, nil
}

func scanGrantRows(rows pgx.Rows) ([]store.Grant, error) {
	defer rows.Close()
	out := make([]store.Grant, 0)
	for rows.Next() {
		g, err := scanGrantRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, mapErr(rows.Err())
}
