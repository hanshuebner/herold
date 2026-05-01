package storesqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata clientlog ring-buffer methods
// (REQ-OPS-206, REQ-OPS-206a) for the SQLite backend.  Schema commentary
// lives in migrations/0037_clientlog.sql.

// clientlogSelectCols lists the columns in SELECT order for scanClientLogRow.
const clientlogSelectCols = `id, slice, server_ts, client_ts, clock_skew_ms,
	app, kind, level, user_id, session_id, page_id, request_id, route,
	build_sha, ua, msg, stack, payload_json`

// scanClientLogRow populates a ClientLogRow from a rowLike scanner.
func scanClientLogRow(row rowLike) (store.ClientLogRow, error) {
	var (
		r                                          store.ClientLogRow
		serverUs, clientUs                         int64
		userID, sessionID, requestID, route, stack sql.NullString
		sliceStr                                   string
	)
	if err := row.Scan(
		&r.ID,
		&sliceStr,
		&serverUs,
		&clientUs,
		&r.ClockSkewMS,
		&r.App,
		&r.Kind,
		&r.Level,
		&userID,
		&sessionID,
		&r.PageID,
		&requestID,
		&route,
		&r.BuildSHA,
		&r.UA,
		&r.Msg,
		&stack,
		&r.PayloadJSON,
	); err != nil {
		return store.ClientLogRow{}, mapErr(err)
	}
	r.Slice = store.ClientLogSlice(sliceStr)
	r.ServerTS = fromMicros(serverUs)
	r.ClientTS = fromMicros(clientUs)
	if userID.Valid {
		r.UserID = &userID.String
	}
	if sessionID.Valid {
		r.SessionID = &sessionID.String
	}
	if requestID.Valid {
		r.RequestID = &requestID.String
	}
	if route.Valid {
		r.Route = &route.String
	}
	if stack.Valid {
		r.Stack = &stack.String
	}
	return r, nil
}

func (m *metadata) AppendClientLog(ctx context.Context, row store.ClientLogRow) error {
	return m.runTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO clientlog
			  (slice, server_ts, client_ts, clock_skew_ms,
			   app, kind, level, user_id, session_id, page_id,
			   request_id, route, build_sha, ua, msg, stack, payload_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(row.Slice),
			usMicros(row.ServerTS),
			usMicros(row.ClientTS),
			row.ClockSkewMS,
			row.App,
			row.Kind,
			row.Level,
			row.UserID,
			row.SessionID,
			row.PageID,
			row.RequestID,
			row.Route,
			row.BuildSHA,
			row.UA,
			row.Msg,
			row.Stack,
			row.PayloadJSON,
		)
		return mapErr(err)
	})
}

// clientlogCursor is the opaque cursor payload encoded as base64 JSON.
// It contains just enough information to resume a paginated read without
// exposing internal IDs in a form that clients could manipulate meaningfully.
type clientlogCursor struct {
	Slice string `json:"s"`
	ID    int64  `json:"i"`
}

func encodeClientLogCursor(slice store.ClientLogSlice, id int64) string {
	b, _ := json.Marshal(clientlogCursor{Slice: string(slice), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeClientLogCursor(cursor string) (store.ClientLogSlice, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", 0, fmt.Errorf("clientlog cursor: decode: %w", err)
	}
	var c clientlogCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return "", 0, fmt.Errorf("clientlog cursor: unmarshal: %w", err)
	}
	return store.ClientLogSlice(c.Slice), c.ID, nil
}

func (m *metadata) ListClientLogByCursor(ctx context.Context, opts store.ClientLogCursorOptions) ([]store.ClientLogRow, string, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}

	// Parse cursor (determines the upper bound for id).
	var cursorID int64
	if opts.Cursor != "" {
		_, id, err := decodeClientLogCursor(opts.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("storesqlite: ListClientLogByCursor: %w", err)
		}
		cursorID = id
	}

	q, args := buildClientLogQuery(opts.Filter, cursorID, limit)
	rows, err := m.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", mapErr(err)
	}
	defer rows.Close()

	var out []store.ClientLogRow
	for rows.Next() {
		r, err := scanClientLogRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapErr(err)
	}

	// Derive next cursor from the last (lowest) id in the result set.
	var nextCursor string
	if len(out) == limit {
		last := out[len(out)-1]
		nextCursor = encodeClientLogCursor(last.Slice, last.ID)
	}
	return out, nextCursor, nil
}

// buildClientLogQuery assembles the SELECT for ListClientLogByCursor.
// cursorID, when non-zero, adds an id < cursorID predicate so pagination
// resumes below the last seen row.
func buildClientLogQuery(f store.ClientLogFilter, cursorID int64, limit int) (string, []any) {
	var where []string
	var args []any

	if f.Slice != "" {
		where = append(where, "slice = ?")
		args = append(args, string(f.Slice))
	}
	if f.App != "" {
		where = append(where, "app = ?")
		args = append(args, f.App)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Level != "" {
		where = append(where, "level = ?")
		args = append(args, f.Level)
	}
	if !f.Since.IsZero() {
		where = append(where, "server_ts >= ?")
		args = append(args, usMicros(f.Since))
	}
	if !f.Until.IsZero() {
		where = append(where, "server_ts <= ?")
		args = append(args, usMicros(f.Until))
	}
	if f.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.SessionID != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.RequestID != "" {
		where = append(where, "request_id = ?")
		args = append(args, f.RequestID)
	}
	if f.Route != "" {
		where = append(where, "route = ?")
		args = append(args, f.Route)
	}
	if f.MsgSubstring != "" {
		// Substring match on msg OR stack.  SQLite LIKE is case-insensitive
		// for ASCII by default; that is acceptable for log search.
		where = append(where, "(msg LIKE ? OR stack LIKE ?)")
		like := "%" + strings.ReplaceAll(f.MsgSubstring, "%", "\\%") + "%"
		args = append(args, like, like)
	}
	if cursorID > 0 {
		where = append(where, "id < ?")
		args = append(args, cursorID)
	}

	q := "SELECT " + clientlogSelectCols + " FROM clientlog"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC"
	q += " LIMIT ?"
	args = append(args, limit)
	return q, args
}

func (m *metadata) ListClientLogByRequestID(ctx context.Context, requestID string) ([]store.ClientLogRow, error) {
	rows, err := m.s.db.QueryContext(ctx, `
		SELECT `+clientlogSelectCols+`
		  FROM clientlog
		 WHERE request_id = ?
		 ORDER BY id DESC`,
		requestID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.ClientLogRow
	for rows.Next() {
		r, err := scanClientLogRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *metadata) EvictClientLog(ctx context.Context, opts store.ClientLogEvictOptions) (int, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	slice := string(opts.Slice)
	now := m.s.clock.Now().UTC()
	cutoff := usMicros(now.Add(-opts.MaxAge))

	// We delete rows that are either too old or below the row-count
	// threshold.  The two conditions are OR'd: a row is evicted if it
	// matches either.  We compute the cap threshold as:
	//   max_id_in_slice - capRows
	// If fewer rows than capRows exist the threshold is -1 (matches
	// nothing for the count-cap branch).
	//
	// Both deletions run in a single statement to keep the lock window
	// tight on SQLite.  LIMIT on DELETE is available in SQLite since
	// 3.35.0; the modernc.org/sqlite bundle embeds a sufficiently recent
	// version.
	var deleted int
	err := m.runTx(ctx, func(tx *sql.Tx) error {
		// Compute max id for the slice.
		var maxID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), -1) FROM clientlog WHERE slice = ?`,
			slice).Scan(&maxID); err != nil {
			return mapErr(err)
		}
		capThreshold := maxID - int64(opts.CapRows)

		res, err := tx.ExecContext(ctx, `
			DELETE FROM clientlog
			 WHERE id IN (
			   SELECT id FROM clientlog
			    WHERE slice = ?
			      AND (server_ts < ? OR id <= ?)
			    ORDER BY id ASC
			    LIMIT ?
			 )`,
			slice, cutoff, capThreshold, batchSize)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storesqlite: evict clientlog rows affected: %w", err)
		}
		deleted = int(n)
		return nil
	})
	return deleted, err
}
