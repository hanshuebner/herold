package storepg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements store.Metadata's ManagedRule methods for Postgres.

const managedRuleSelectColsPG = `
	id, principal_id, name, enabled, sort_order,
	conditions_json, actions_json,
	created_at_us, updated_at_us`

func scanManagedRulePG(row rowLike) (store.ManagedRule, error) {
	var (
		id, pid, sortOrder   int64
		enabled              bool
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
		Enabled:     enabled,
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
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO managed_rules
			  (principal_id, name, enabled, sort_order,
			   conditions_json, actions_json,
			   created_at_us, updated_at_us)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+managedRuleSelectColsPG,
			int64(rule.PrincipalID), rule.Name, rule.Enabled, rule.SortOrder,
			string(condJSON), string(actJSON), nowUs, nowUs)
		var err error
		out, err = scanManagedRulePG(row)
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
	err = m.runTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE managed_rules SET
			  name = $1, enabled = $2, sort_order = $3,
			  conditions_json = $4, actions_json = $5,
			  updated_at_us = $6
			WHERE id = $7 AND principal_id = $8
			RETURNING `+managedRuleSelectColsPG,
			rule.Name, rule.Enabled, rule.SortOrder,
			string(condJSON), string(actJSON), nowUs,
			int64(rule.ID), int64(rule.PrincipalID))
		var err error
		out, err = scanManagedRulePG(row)
		if err != nil {
			return mapErr(err)
		}
		return nil
	})
	if err != nil {
		return store.ManagedRule{}, err
	}
	return out, nil
}

func (m *metadata) DeleteManagedRule(ctx context.Context, id store.ManagedRuleID, pid store.PrincipalID) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM managed_rules WHERE id = $1 AND principal_id = $2`,
			int64(id), int64(pid))
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (m *metadata) GetManagedRule(ctx context.Context, id store.ManagedRuleID, pid store.PrincipalID) (store.ManagedRule, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT `+managedRuleSelectColsPG+`
		  FROM managed_rules
		 WHERE id = $1 AND principal_id = $2`, int64(id), int64(pid))
	return scanManagedRulePG(row)
}

func (m *metadata) ListManagedRules(ctx context.Context, pid store.PrincipalID, filter store.ManagedRuleFilter) ([]store.ManagedRule, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 256
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT `+managedRuleSelectColsPG+`
		  FROM managed_rules
		 WHERE principal_id = $1 AND id > $2
		 ORDER BY sort_order ASC, id ASC
		 LIMIT $3`,
		int64(pid), int64(filter.AfterID), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ManagedRule
	for rows.Next() {
		r, err := scanManagedRulePG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
