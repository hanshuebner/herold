package storepg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata clientlog ring-buffer methods
// (REQ-OPS-206, REQ-OPS-206a) for the Postgres backend.  Schema commentary
// lives in migrations/0037_clientlog.sql.

// clientlogSelectColsPG lists the columns in SELECT order for
// scanClientLogRowPG.
const clientlogSelectColsPG = `id, slice, server_ts, client_ts, clock_skew_ms,
	app, kind, level, user_id, session_id, page_id, request_id, route,
	build_sha, ua, msg, stack, payload_json`

// scanClientLogRowPG populates a ClientLogRow from a pgx.Row.
func scanClientLogRowPG(row pgx.Row) (store.ClientLogRow, error) {
	var (
		r                                          store.ClientLogRow
		serverUs, clientUs                         int64
		sliceStr                                   string
		userID, sessionID, requestID, route, stack *string
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
	r.UserID = userID
	r.SessionID = sessionID
	r.RequestID = requestID
	r.Route = route
	r.Stack = stack
	return r, nil
}

func (m *metadata) AppendClientLog(ctx context.Context, row store.ClientLogRow) error {
	return m.runTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO clientlog
			  (slice, server_ts, client_ts, clock_skew_ms,
			   app, kind, level, user_id, session_id, page_id,
			   request_id, route, build_sha, ua, msg, stack, payload_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
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

	var cursorID int64
	if opts.Cursor != "" {
		_, id, err := decodeClientLogCursor(opts.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("storepg: ListClientLogByCursor: %w", err)
		}
		cursorID = id
	}

	q, args := buildClientLogQueryPG(opts.Filter, cursorID, limit)
	pgRows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", mapErr(err)
	}
	defer pgRows.Close()

	var out []store.ClientLogRow
	for pgRows.Next() {
		r, err := scanClientLogRowPG(pgRows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := pgRows.Err(); err != nil {
		return nil, "", mapErr(err)
	}

	var nextCursor string
	if len(out) == limit {
		last := out[len(out)-1]
		nextCursor = encodeClientLogCursor(last.Slice, last.ID)
	}
	return out, nextCursor, nil
}

// buildClientLogQueryPG assembles the SELECT for ListClientLogByCursor
// using Postgres $N placeholder style.
func buildClientLogQueryPG(f store.ClientLogFilter, cursorID int64, limit int) (string, []any) {
	var where []string
	var args []any
	n := 1

	placeholder := func() string {
		s := fmt.Sprintf("$%d", n)
		n++
		return s
	}

	if f.Slice != "" {
		where = append(where, "slice = "+placeholder())
		args = append(args, string(f.Slice))
	}
	if f.App != "" {
		where = append(where, "app = "+placeholder())
		args = append(args, f.App)
	}
	if f.Kind != "" {
		where = append(where, "kind = "+placeholder())
		args = append(args, f.Kind)
	}
	if f.Level != "" {
		where = append(where, "level = "+placeholder())
		args = append(args, f.Level)
	}
	if !f.Since.IsZero() {
		where = append(where, "server_ts >= "+placeholder())
		args = append(args, usMicros(f.Since))
	}
	if !f.Until.IsZero() {
		where = append(where, "server_ts <= "+placeholder())
		args = append(args, usMicros(f.Until))
	}
	if f.UserID != "" {
		where = append(where, "user_id = "+placeholder())
		args = append(args, f.UserID)
	}
	if f.SessionID != "" {
		where = append(where, "session_id = "+placeholder())
		args = append(args, f.SessionID)
	}
	if f.RequestID != "" {
		where = append(where, "request_id = "+placeholder())
		args = append(args, f.RequestID)
	}
	if f.Route != "" {
		where = append(where, "route = "+placeholder())
		args = append(args, f.Route)
	}
	if f.MsgSubstring != "" {
		p1 := placeholder()
		p2 := placeholder()
		where = append(where, "(msg LIKE "+p1+" OR stack LIKE "+p2+")")
		like := "%" + strings.ReplaceAll(f.MsgSubstring, "%", "\\%") + "%"
		args = append(args, like, like)
	}
	if cursorID > 0 {
		where = append(where, "id < "+placeholder())
		args = append(args, cursorID)
	}

	q := "SELECT " + clientlogSelectColsPG + " FROM clientlog"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC"
	q += fmt.Sprintf(" LIMIT $%d", n)
	args = append(args, limit)
	return q, args
}

func (m *metadata) ListClientLogByRequestID(ctx context.Context, requestID string) ([]store.ClientLogRow, error) {
	pgRows, err := m.s.pool.Query(ctx, `
		SELECT `+clientlogSelectColsPG+`
		  FROM clientlog
		 WHERE request_id = $1
		 ORDER BY id DESC`,
		requestID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer pgRows.Close()
	var out []store.ClientLogRow
	for pgRows.Next() {
		r, err := scanClientLogRowPG(pgRows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, pgRows.Err()
}

func (m *metadata) EvictClientLog(ctx context.Context, opts store.ClientLogEvictOptions) (int, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	slice := string(opts.Slice)
	now := m.s.clock.Now().UTC()
	cutoff := usMicros(now.Add(-opts.MaxAge))

	var deleted int
	err := m.runTx(ctx, func(tx pgx.Tx) error {
		// Compute max id for the slice.
		var maxID int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(id), -1) FROM clientlog WHERE slice = $1`,
			slice).Scan(&maxID); err != nil {
			return mapErr(err)
		}
		capThreshold := maxID - int64(opts.CapRows)

		res, err := tx.Exec(ctx, `
			DELETE FROM clientlog
			 WHERE id IN (
			   SELECT id FROM clientlog
			    WHERE slice = $1
			      AND (server_ts < $2 OR id <= $3)
			    ORDER BY id ASC
			    LIMIT $4
			 )`,
			slice, cutoff, capThreshold, batchSize)
		if err != nil {
			return mapErr(err)
		}
		deleted = int(res.RowsAffected())
		return nil
	})
	return deleted, err
}
