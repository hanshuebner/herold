package storesqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata EmailBulkJob methods (issue
// #149/#161, REQ-PROTO-40..48 vendor extension
// https://netzhansa.com/jmap/email-bulk-mutation) for the SQLite
// backend. Schema commentary lives in migrations/0076_email_bulk_jobs.sql.

const emailBulkJobSelectCols = `
	id, principal_id, filter_json, patch_json, status, matched_estimate,
	total, processed, failures_json, error_message, created_at_us, updated_at_us`

func scanEmailBulkJob(row interface{ Scan(...any) error }) (store.EmailBulkJob, error) {
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
		return store.EmailBulkJob{}, fmt.Errorf("storesqlite: decode failures_json: %w", err)
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
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO email_bulk_jobs
			  (principal_id, filter_json, patch_json, status, matched_estimate,
			   total, processed, target_ids_json, failures_json, error_message,
			   created_at_us, updated_at_us)
			VALUES (?, ?, ?, 'running', ?, -1, 0, '', '[]', '', ?, ?)`,
			int64(create.PrincipalID), create.FilterJSON, create.PatchJSON,
			create.MatchedEstimate, nowUs, nowUs)
		if err != nil {
			return mapErr(err)
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return store.EmailBulkJob{}, err
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
	row := m.s.db.QueryRowContext(ctx, `
		SELECT `+emailBulkJobSelectCols+`
		  FROM email_bulk_jobs
		 WHERE id = ? AND principal_id = ?`,
		int64(id), int64(principalID))
	job, err := scanEmailBulkJob(row)
	if err != nil {
		return store.EmailBulkJob{}, err
	}
	return job, nil
}

func (m *metadata) ListRunningEmailBulkJobs(ctx context.Context, limit int) ([]store.EmailBulkJob, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT `+emailBulkJobSelectCols+`
		  FROM email_bulk_jobs
		 WHERE status = 'running'
		 ORDER BY id ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.EmailBulkJob
	for rows.Next() {
		job, err := scanEmailBulkJob(rows)
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
		return fmt.Errorf("storesqlite: encode target ids: %w", err)
	}
	now := usMicros(m.s.clock.Now().UTC())
	total := int64(len(targetIDs))
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var existingTotal int64
		if err := tx.QueryRowContext(ctx,
			`SELECT total FROM email_bulk_jobs WHERE id = ?`, int64(id)).Scan(&existingTotal); err != nil {
			return mapErr(err)
		}
		if existingTotal >= 0 {
			// Already resolved (e.g. a prior crash mid-resolution followed
			// by a successful retry that raced us). No-op.
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE email_bulk_jobs
			   SET target_ids_json = ?, total = ?, matched_estimate = ?, updated_at_us = ?
			 WHERE id = ? AND total = -1`,
			targetsJSON, total, total, now, int64(id))
		return mapErr(err)
	})
}

func (m *metadata) NextEmailBulkJobBatch(ctx context.Context, id store.EmailBulkJobID, limit int) ([]store.MessageID, error) {
	var targetsJSON string
	var total, processed int64
	err := m.s.db.QueryRowContext(ctx,
		`SELECT target_ids_json, total, processed FROM email_bulk_jobs WHERE id = ?`,
		int64(id)).Scan(&targetsJSON, &total, &processed)
	if err != nil {
		return nil, mapErr(err)
	}
	if total < 0 || processed >= total {
		return nil, nil
	}
	ids, err := store.DecodeEmailBulkJobTargetIDs(targetsJSON)
	if err != nil {
		return nil, fmt.Errorf("storesqlite: decode target_ids_json: %w", err)
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
	return m.runTx(ctx, func(tx *sql.Tx) error {
		var processed int64
		var failuresJSON string
		if err := tx.QueryRowContext(ctx,
			`SELECT processed, failures_json FROM email_bulk_jobs WHERE id = ?`,
			int64(id)).Scan(&processed, &failuresJSON); err != nil {
			return mapErr(err)
		}
		failures, err := store.DecodeEmailBulkJobFailures(failuresJSON)
		if err != nil {
			return fmt.Errorf("storesqlite: decode failures_json: %w", err)
		}
		for _, o := range outcomes {
			if o.Err != "" {
				failures = append(failures, store.EmailBulkJobFailure{MessageID: o.MessageID, Error: o.Err})
			}
		}
		newFailuresJSON, err := store.EncodeEmailBulkJobFailures(failures)
		if err != nil {
			return fmt.Errorf("storesqlite: encode failures_json: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE email_bulk_jobs
			   SET processed = ?, failures_json = ?, updated_at_us = ?
			 WHERE id = ?`,
			processed+int64(len(outcomes)), newFailuresJSON, now, int64(id))
		return mapErr(err)
	})
}

func (m *metadata) FinishEmailBulkJob(ctx context.Context, id store.EmailBulkJobID, status store.EmailBulkJobStatus, errMsg string) error {
	now := usMicros(m.s.clock.Now().UTC())
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE email_bulk_jobs
			   SET status = ?, error_message = ?, updated_at_us = ?
			 WHERE id = ?`,
			string(status), errMsg, now, int64(id))
		return mapErr(err)
	})
}
