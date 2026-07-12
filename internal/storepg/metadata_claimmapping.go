package storepg

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata external-IdP claim-to-grant
// mapping methods (epic #188, REQ-AC-60..70). Mirrors
// storesqlite/metadata_claimmapping.go. Schema commentary lives in
// migrations/0083_oidc_claim_mapping.sql.

// -- Authorization-claim allowlist ------------------------------------

func (m *metadata) InsertClaimAllowlistEntry(ctx context.Context, providerName, claim string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO oidc_claim_allowlist (provider_name, claim)
			VALUES ($1, $2)
			ON CONFLICT (provider_name, claim) DO NOTHING`,
			providerName, claim)
		return mapErr(err)
	})
}

func (m *metadata) DeleteClaimAllowlistEntry(ctx context.Context, providerName, claim string) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx, `
			DELETE FROM oidc_claim_allowlist WHERE provider_name = $1 AND claim = $2`,
			providerName, claim)
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListClaimAllowlist(ctx context.Context, providerName string) ([]string, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT claim FROM oidc_claim_allowlist WHERE provider_name = $1 ORDER BY claim`,
		providerName)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// -- Claim-mapping rules ------------------------------------------------

const claimMappingRuleColumns = `provider_name, claim, match_value, resource_kind,
	resource_id, level, created_by, created_at_us`

func (m *metadata) InsertClaimMappingRule(ctx context.Context, rule store.ClaimMappingRule) (store.ClaimMappingRule, error) {
	now := m.s.clock.Now().UTC()
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		var createdBy any
		if rule.CreatedBy != 0 {
			createdBy = int64(rule.CreatedBy)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO oidc_claim_mapping_rules (`+claimMappingRuleColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			rule.ProviderName, rule.Claim, rule.MatchValue, string(rule.ResourceKind),
			rule.ResourceID, string(rule.Level), createdBy, usMicros(now),
		).Scan(&id)
	})
	if err != nil {
		return store.ClaimMappingRule{}, mapErr(err)
	}
	rule.ID = store.ClaimMappingRuleID(id)
	rule.CreatedAt = now
	return rule, nil
}

func (m *metadata) DeleteClaimMappingRule(ctx context.Context, id store.ClaimMappingRuleID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Exec(ctx,
			`DELETE FROM oidc_claim_mapping_rules WHERE id = $1`, int64(id))
		if err != nil {
			return mapErr(err)
		}
		if res.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) ListClaimMappingRules(ctx context.Context, providerName string) ([]store.ClaimMappingRule, error) {
	rows, err := m.s.pool.Query(ctx, `
		SELECT id, `+claimMappingRuleColumns+`
		  FROM oidc_claim_mapping_rules
		 WHERE provider_name = $1
		 ORDER BY id`, providerName)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]store.ClaimMappingRule, 0)
	for rows.Next() {
		var r store.ClaimMappingRule
		var id, createdAtUs int64
		var resourceKind, level string
		var createdBy sql.NullInt64
		if err := rows.Scan(&id, &r.ProviderName, &r.Claim, &r.MatchValue, &resourceKind,
			&r.ResourceID, &level, &createdBy, &createdAtUs); err != nil {
			return nil, mapErr(err)
		}
		r.ID = store.ClaimMappingRuleID(id)
		r.ResourceKind = store.GrantResourceKind(resourceKind)
		r.Level = store.GrantLevel(level)
		if createdBy.Valid {
			r.CreatedBy = store.PrincipalID(createdBy.Int64)
		}
		r.CreatedAt = fromMicros(createdAtUs)
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// -- IdP grant reconciliation and staleness sweep -----------------------

// ReconcileIdPGrants implements store.Metadata.ReconcileIdPGrants. See the
// interface doc comment (internal/store/store.go) for the exact diff
// semantics; this is the Postgres transaction that realises it. Mirrors
// storesqlite/metadata_claimmapping.go.
func (m *metadata) ReconcileIdPGrants(ctx context.Context, subjectID store.PrincipalID, provider string, desired []store.GrantDesired, asOf time.Time) (added, removed []store.Grant, err error) {
	provenance := "idp:" + provider
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT id, `+grantColumns+`
			  FROM grants
			 WHERE subject_kind = 'principal' AND subject_id = $1 AND provenance = $2`,
			int64(subjectID), provenance)
		if qerr != nil {
			return mapErr(qerr)
		}
		current, serr := scanGrantRows(rows)
		if serr != nil {
			return serr
		}
		type key struct {
			kind store.GrantResourceKind
			id   string
		}
		currentByKey := make(map[key]store.Grant, len(current))
		for _, g := range current {
			currentByKey[key{g.ResourceKind, g.ResourceID}] = g
		}
		// If desired names the same resource twice, the last entry wins
		// (the store has no notion of level ordering; callers -- namely
		// internal/authz.ReconcileIdP -- resolve to at most one entry per
		// resource, taking the highest level, before calling this method).
		desiredByKey := make(map[key]store.GrantLevel, len(desired))
		for _, d := range desired {
			desiredByKey[key{d.ResourceKind, d.ResourceID}] = d.Level
		}

		// Insert or update every desired (resource, level).
		for k, level := range desiredByKey {
			cur, exists := currentByKey[k]
			if !exists {
				if _, ierr := tx.Exec(ctx, `
					INSERT INTO grants (`+grantColumns+`)
					VALUES ('principal', $1, $2, $3, $4, $5, NULL, $6, $7)`,
					int64(subjectID), string(k.kind), k.id, string(level), provenance,
					usMicros(asOf), usMicros(asOf)); ierr != nil {
					return mapErr(ierr)
				}
				added = append(added, store.Grant{
					SubjectKind: store.GrantSubjectPrincipal, SubjectID: uint64(subjectID),
					ResourceKind: k.kind, ResourceID: k.id, Level: level,
					Provenance: provenance, GrantedAt: asOf, LastAssertedAt: &asOf,
				})
				continue
			}
			if cur.Level != level {
				if _, uerr := tx.Exec(ctx, `
					UPDATE grants SET level = $1, last_asserted_at_us = $2
					 WHERE id = $3`, string(level), usMicros(asOf), int64(cur.ID)); uerr != nil {
					return mapErr(uerr)
				}
				removed = append(removed, cur)
				updated := cur
				updated.Level = level
				updated.LastAssertedAt = &asOf
				added = append(added, updated)
				continue
			}
			// Survivor at the same level: refresh last_asserted_at only.
			if _, uerr := tx.Exec(ctx, `
				UPDATE grants SET last_asserted_at_us = $1 WHERE id = $2`,
				usMicros(asOf), int64(cur.ID)); uerr != nil {
				return mapErr(uerr)
			}
		}

		// Delete every current row not in the desired set.
		for k, cur := range currentByKey {
			if _, ok := desiredByKey[k]; ok {
				continue
			}
			if _, derr := tx.Exec(ctx, `DELETE FROM grants WHERE id = $1`, int64(cur.ID)); derr != nil {
				return mapErr(derr)
			}
			removed = append(removed, cur)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return added, removed, nil
}

// SweepStaleIdPGrants implements store.Metadata.SweepStaleIdPGrants.
func (m *metadata) SweepStaleIdPGrants(ctx context.Context, olderThan time.Time) ([]store.Grant, error) {
	var removed []store.Grant
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT id, `+grantColumns+`
			  FROM grants
			 WHERE provenance LIKE 'idp:%' AND last_asserted_at_us IS NOT NULL
			   AND last_asserted_at_us < $1`, usMicros(olderThan))
		if qerr != nil {
			return mapErr(qerr)
		}
		stale, serr := scanGrantRows(rows)
		if serr != nil {
			return serr
		}
		for _, g := range stale {
			if _, derr := tx.Exec(ctx, `DELETE FROM grants WHERE id = $1`, int64(g.ID)); derr != nil {
				return mapErr(derr)
			}
			removed = append(removed, g)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].ID < removed[j].ID })
	return removed, nil
}
