package storepg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata EmailBulkJob methods (issue
// #149/#161, REQ-PROTO-40..48 vendor extension
// https://netzhansa.com/jmap/email-bulk-mutation) for the Postgres
// backend. Schema commentary lives in migrations/0076_email_bulk_jobs.sql.

const emailBulkJobSelectColsPG = `
	id, principal_id, filter_json, patch_json, status, matched_estimate,
	total, processed, failures_json, error_message, created_at_us, updated_at_us`

func scanEmailBulkJobPG(row pgx.Row) (store.EmailBulkJob, error) {
	var (
		id, pid, matchedEstimate, total, processed int64
		filterJSON, patchJSON, status              string
		failuresJSON, errorMessage                 string
		createdUs, updatedUs                       int64
	)
	if err := row.Scan(&id, &pid, &filterJSON, &patchJSON, &status, &matchedEstimate,
		&total, &processed, &failuresJSON, &errorMessage, &createdUs, &updatedUs); err != nil {
		return store.EmailBulkJob{}, mapErr(err)
	}
	failures, err := store.DecodeEmailBulkJobFailures(failuresJSON)
	if err != nil {
		return store.EmailBulkJob{}, fmt.Errorf("storepg: decode failures_json: %w", err)
	}
	return store.EmailBulkJob{
		ID:              store.EmailBulkJobID(id),
		PrincipalID:     store.PrincipalID(pid),
		FilterJSON:      filterJSON,
		PatchJSON:       patchJSON,
		Status:          store.EmailBulkJobStatus(status),
		MatchedEstimate: matchedEstimate,
		Total:           total,
		Processed:       processed,
		Failures:        failures,
		ErrorMessage:    errorMessage,
		CreatedAt:       fromMicros(createdUs),
		UpdatedAt:       fromMicros(updatedUs),
	}, nil
}

func (m *metadata) CreateEmailBulkJob(ctx context.Context, create store.EmailBulkJobCreate) (store.EmailBulkJob, error) {
	now := m.s.clock.Now().UTC()
	nowUs := usMicros(now)
	var id int64
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO email_bulk_jobs
			  (principal_id, filter_json, patch_json, status, matched_estimate,
			   total, processed, target_ids_json, failures_json, error_message,
			   created_at_us, updated_at_us)
			VALUES ($1, $2, $3, 'running', $4, -1, 0, '', '[]', '', $5, $6)
			RETURNING id`,
			int64(create.PrincipalID), create.FilterJSON, create.PatchJSON,
			create.MatchedEstimate, nowUs, nowUs).Scan(&id)
	})
	if err != nil {
		return store.EmailBulkJob{}, mapErr(err)
	}
	return store.EmailBulkJob{
		ID:              store.EmailBulkJobID(id),
		PrincipalID:     create.PrincipalID,
		FilterJSON:      create.FilterJSON,
		PatchJSON:       create.PatchJSON,
		Status:          store.EmailBulkJobStatusRunning,
		MatchedEstimate: create.MatchedEstimate,
		Total:           -1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func (m *metadata) GetEmailBulkJob(ctx context.Context, principalID store.PrincipalID, id store.EmailBulkJobID) (store.EmailBulkJob, error) {
	row := m.s.pool.QueryRow(ctx, `
		SELECT `+emailBulkJobSelectColsPG+`
		  FROM email_bulk_jobs
		 WHERE id = $1 AND principal_id = $2`,
		int64(id), int64(principalID))
	return scanEmailBulkJobPG(row)
}

func (m *metadata) ListRunningEmailBulkJobs(ctx context.Context, limit int) ([]store.EmailBulkJob, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := m.s.pool.Query(ctx, `
		SELECT `+emailBulkJobSelectColsPG+`
		  FROM email_bulk_jobs
		 WHERE status = 'running'
		 ORDER BY id ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.EmailBulkJob
	for rows.Next() {
		job, err := scanEmailBulkJobPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func (m *metadata) ResolveEmailBulkJobTargets(ctx context.Context, id store.EmailBulkJobID, targetIDs []store.MessageID) error {
	targetsJSON, err := store.EncodeEmailBulkJobTargetIDs(targetIDs)
	if err != nil {
		return fmt.Errorf("storepg: encode target ids: %w", err)
	}
	now := usMicros(m.s.clock.Now().UTC())
	total := int64(len(targetIDs))
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var existingTotal int64
		if err := tx.QueryRow(ctx,
			`SELECT total FROM email_bulk_jobs WHERE id = $1`, int64(id)).Scan(&existingTotal); err != nil {
			return mapErr(err)
		}
		if existingTotal >= 0 {
			// Already resolved (e.g. a prior crash mid-resolution followed
			// by a successful retry that raced us). No-op.
			return nil
		}
		_, err := tx.Exec(ctx, `
			UPDATE email_bulk_jobs
			   SET target_ids_json = $1, total = $2, matched_estimate = $3, updated_at_us = $4
			 WHERE id = $5 AND total = -1`,
			targetsJSON, total, total, now, int64(id))
		return mapErr(err)
	})
}

func (m *metadata) NextEmailBulkJobBatch(ctx context.Context, id store.EmailBulkJobID, limit int) ([]store.MessageID, error) {
	var targetsJSON string
	var total, processed int64
	err := m.s.pool.QueryRow(ctx,
		`SELECT target_ids_json, total, processed FROM email_bulk_jobs WHERE id = $1`,
		int64(id)).Scan(&targetsJSON, &total, &processed)
	if err != nil {
		return nil, mapErr(err)
	}
	if total < 0 || processed >= total {
		return nil, nil
	}
	ids, err := store.DecodeEmailBulkJobTargetIDs(targetsJSON)
	if err != nil {
		return nil, fmt.Errorf("storepg: decode target_ids_json: %w", err)
	}
	end := processed + int64(limit)
	if end > int64(len(ids)) {
		end = int64(len(ids))
	}
	if processed >= int64(len(ids)) {
		return nil, nil
	}
	return ids[processed:end], nil
}

func (m *metadata) RecordEmailBulkJobBatch(ctx context.Context, id store.EmailBulkJobID, outcomes []store.EmailBulkJobOutcome) error {
	if len(outcomes) == 0 {
		return nil
	}
	now := usMicros(m.s.clock.Now().UTC())
	return m.runTx(ctx, func(tx pgx.Tx) error {
		var processed int64
		var failuresJSON string
		if err := tx.QueryRow(ctx,
			`SELECT processed, failures_json FROM email_bulk_jobs WHERE id = $1`,
			int64(id)).Scan(&processed, &failuresJSON); err != nil {
			return mapErr(err)
		}
		failures, err := store.DecodeEmailBulkJobFailures(failuresJSON)
		if err != nil {
			return fmt.Errorf("storepg: decode failures_json: %w", err)
		}
		for _, o := range outcomes {
			if o.Err != "" {
				failures = append(failures, store.EmailBulkJobFailure{MessageID: o.MessageID, Error: o.Err})
			}
		}
		newFailuresJSON, err := store.EncodeEmailBulkJobFailures(failures)
		if err != nil {
			return fmt.Errorf("storepg: encode failures_json: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE email_bulk_jobs
			   SET processed = $1, failures_json = $2, updated_at_us = $3
			 WHERE id = $4`,
			processed+int64(len(outcomes)), newFailuresJSON, now, int64(id))
		return mapErr(err)
	})
}

func (m *metadata) FinishEmailBulkJob(ctx context.Context, id store.EmailBulkJobID, status store.EmailBulkJobStatus, errMsg string) error {
	now := usMicros(m.s.clock.Now().UTC())
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE email_bulk_jobs
			   SET status = $1, error_message = $2, updated_at_us = $3
			 WHERE id = $4`,
			string(status), errMsg, now, int64(id))
		return mapErr(err)
	})
}
