package storesqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata's ManagedRule methods for SQLite.
// Each managed_rules row holds the rule as JSON so conditions and actions
// can evolve without schema changes.

const managedRuleSelectCols = `
	id, principal_id, name, enabled, sort_order,
	conditions_json, actions_json,
	created_at_us, updated_at_us`

func scanManagedRule(row rowLike) (store.ManagedRule, error) {
	var (
		id, pid, sortOrder   int64
		enabled              int64 // 0/1 in SQLite STRICT
		name                 string
		condJSON, actJSON    string
		createdUs, updatedUs int64
	)
	if err := row.Scan(&id, &pid, &name, &enabled, &sortOrder,
		&condJSON, &actJSON, &createdUs, &updatedUs); err != nil {
		return store.ManagedRule{}, mapErr(err)
	}
	var conds []store.RuleCondition
	if err := json.Unmarshal([]byte(condJSON), &conds); err != nil {
		return store.ManagedRule{}, fmt.Errorf("managed_rules: decode conditions: %w", err)
	}
	var actions []store.RuleAction
	if err := json.Unmarshal([]byte(actJSON), &actions); err != nil {
		return store.ManagedRule{}, fmt.Errorf("managed_rules: decode actions: %w", err)
	}
	return store.ManagedRule{
		ID:          store.ManagedRuleID(id),
		PrincipalID: store.PrincipalID(pid),
		Name:        name,
		Enabled:     enabled != 0,
		SortOrder:   int(sortOrder),
		Conditions:  conds,
		Actions:     actions,
		CreatedAt:   fromMicros(createdUs),
		UpdatedAt:   fromMicros(updatedUs),
	}, nil
}

func (m *metadata) InsertManagedRule(ctx context.Context, rule store.ManagedRule) (store.ManagedRule, error) {
	condJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return store.ManagedRule{}, fmt.Errorf("%w: conditions: %s", store.ErrInvalidArgument, err)
	}
	actJSON, err := json.Marshal(rule.Actions)
	if err != nil {
		return store.ManagedRule{}, fmt.Errorf("%w: actions: %s", store.ErrInvalidArgument, err)
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	var out store.ManagedRule
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO managed_rules
			  (principal_id, name, enabled, sort_order,
			   conditions_json, actions_json,
			   created_at_us, updated_at_us)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			int64(rule.PrincipalID), rule.Name, boolInt(rule.Enabled), rule.SortOrder,
			string(condJSON), string(actJSON), nowUs, nowUs)
		if err != nil {
			return mapErr(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("managed_rules insert: %w", err)
		}
		row := tx.QueryRowContext(ctx,
			`SELECT `+managedRuleSelectCols+` FROM managed_rules WHERE id = ?`, id)
		out, err = scanManagedRule(row)
		return err
	})
	if err != nil {
		return store.ManagedRule{}, err
	}
	return out, nil
}

func (m *metadata) UpdateManagedRule(ctx context.Context, rule store.ManagedRule) (store.ManagedRule, error) {
	condJSON, err := json.Marshal(rule.Conditions)
	if err != nil {
		return store.ManagedRule{}, fmt.Errorf("%w: conditions: %s", store.ErrInvalidArgument, err)
	}
	actJSON, err := json.Marshal(rule.Actions)
	if err != nil {
		return store.ManagedRule{}, fmt.Errorf("%w: actions: %s", store.ErrInvalidArgument, err)
	}
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	var out store.ManagedRule
	err = m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE managed_rules SET
			  name = ?, enabled = ?, sort_order = ?,
			  conditions_json = ?, actions_json = ?,
			  updated_at_us = ?
			WHERE id = ? AND principal_id = ?`,
			rule.Name, boolInt(rule.Enabled), rule.SortOrder,
			string(condJSON), string(actJSON), nowUs,
			int64(rule.ID), int64(rule.PrincipalID))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("managed_rules update: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		row := tx.QueryRowContext(ctx,
			`SELECT `+managedRuleSelectCols+` FROM managed_rules WHERE id = ?`, int64(rule.ID))
		out, err = scanManagedRule(row)
		return err
	})
	if err != nil {
		return store.ManagedRule{}, err
	}
	return out, nil
}

func (m *metadata) DeleteManagedRule(ctx context.Context, id store.ManagedRuleID, pid store.PrincipalID) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM managed_rules WHERE id = ? AND principal_id = ?`,
			int64(id), int64(pid))
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("managed_rules delete: %w", err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) GetManagedRule(ctx context.Context, id store.ManagedRuleID, pid store.PrincipalID) (store.ManagedRule, error) {
	row := m.s.db.QueryRowContext(ctx, `
		SELECT `+managedRuleSelectCols+`
		  FROM managed_rules
		 WHERE id = ? AND principal_id = ?`, int64(id), int64(pid))
	return scanManagedRule(row)
}

func (m *metadata) ListManagedRules(ctx context.Context, pid store.PrincipalID, filter store.ManagedRuleFilter) ([]store.ManagedRule, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 256
	}
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT `+managedRuleSelectCols+`
		  FROM managed_rules
		 WHERE principal_id = ? AND id > ?
		 ORDER BY sort_order ASC, id ASC
		 LIMIT ?`,
		int64(pid), int64(filter.AfterID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ManagedRule
	for rows.Next() {
		r, err := scanManagedRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// boolInt converts a Go bool to the SQLite STRICT integer form (0 or 1).
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
