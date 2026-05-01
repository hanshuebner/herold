package fakestore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// This file implements the store.Metadata clientlog ring-buffer methods
// (REQ-OPS-206) for the in-memory fakestore.  The implementation is
// intentionally simple: rows are stored in a slice ordered by insertion
// (ascending ID); all queries do a linear scan.  Tests that need the
// ring-buffer behaviour (append, paginate, evict) use the SQLite /
// Postgres backends directly.

// clientlogData holds the in-memory clientlog state for the fakestore.
type clientlogData struct {
	rows   []store.ClientLogRow
	nextID int64
}

func newClientlogData() *clientlogData {
	return &clientlogData{nextID: 1}
}

func (s *Store) ensureClientlog() *clientlogData {
	if s.clientlog == nil {
		s.clientlog = newClientlogData()
	}
	return s.clientlog
}

// fakeCursor is the opaque cursor used by the fakestore implementation.
type fakeCursor struct {
	ID int64 `json:"i"`
}

func encodeFakeCursor(id int64) string {
	b, _ := json.Marshal(fakeCursor{ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeFakeCursor(cursor string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("fakestore clientlog cursor: %w", err)
	}
	var c fakeCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return 0, fmt.Errorf("fakestore clientlog cursor: %w", err)
	}
	return c.ID, nil
}

func (m *metaFace) AppendClientLog(ctx context.Context, row store.ClientLogRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.ensureClientlog()
	row.ID = d.nextID
	d.nextID++
	d.rows = append(d.rows, row)
	return nil
}

func (m *metaFace) ListClientLogByCursor(ctx context.Context, opts store.ClientLogCursorOptions) ([]store.ClientLogRow, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}

	var cursorID int64
	if opts.Cursor != "" {
		id, err := decodeFakeCursor(opts.Cursor)
		if err != nil {
			return nil, "", err
		}
		cursorID = id
	}

	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.clientlog == nil {
		return nil, "", nil
	}

	// Collect matching rows in reverse insertion order (id DESC).
	f := opts.Filter
	var out []store.ClientLogRow
	for i := len(s.clientlog.rows) - 1; i >= 0; i-- {
		r := s.clientlog.rows[i]
		if cursorID > 0 && r.ID >= cursorID {
			continue
		}
		if f.Slice != "" && r.Slice != f.Slice {
			continue
		}
		if f.App != "" && r.App != f.App {
			continue
		}
		if f.Kind != "" && r.Kind != f.Kind {
			continue
		}
		if f.Level != "" && r.Level != f.Level {
			continue
		}
		if !f.Since.IsZero() && r.ServerTS.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && r.ServerTS.After(f.Until) {
			continue
		}
		if f.UserID != "" && (r.UserID == nil || *r.UserID != f.UserID) {
			continue
		}
		if f.SessionID != "" && (r.SessionID == nil || *r.SessionID != f.SessionID) {
			continue
		}
		if f.RequestID != "" && (r.RequestID == nil || *r.RequestID != f.RequestID) {
			continue
		}
		if f.Route != "" && (r.Route == nil || *r.Route != f.Route) {
			continue
		}
		if f.MsgSubstring != "" {
			if !strings.Contains(r.Msg, f.MsgSubstring) {
				if r.Stack == nil || !strings.Contains(*r.Stack, f.MsgSubstring) {
					continue
				}
			}
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}

	var nextCursor string
	if len(out) == limit {
		nextCursor = encodeFakeCursor(out[len(out)-1].ID)
	}
	return out, nextCursor, nil
}

func (m *metaFace) ListClientLogByRequestID(ctx context.Context, requestID string) ([]store.ClientLogRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := m.s()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.clientlog == nil {
		return nil, nil
	}
	var out []store.ClientLogRow
	for i := len(s.clientlog.rows) - 1; i >= 0; i-- {
		r := s.clientlog.rows[i]
		if r.RequestID != nil && *r.RequestID == requestID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *metaFace) EvictClientLog(ctx context.Context, opts store.ClientLogEvictOptions) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	s := m.s()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clientlog == nil {
		return 0, nil
	}

	now := s.clk.Now()
	cutoff := now.Add(-opts.MaxAge)

	// Find the max id for this slice to compute the cap threshold.
	var maxID int64
	for _, r := range s.clientlog.rows {
		if r.Slice == opts.Slice && r.ID > maxID {
			maxID = r.ID
		}
	}
	capThreshold := maxID - int64(opts.CapRows)

	// Collect IDs to evict: too old OR below cap threshold.
	// Scan in insertion order (ascending id) and stop at batchSize.
	var evictIDs []int64
	for _, r := range s.clientlog.rows {
		if r.Slice != opts.Slice {
			continue
		}
		if r.ServerTS.Before(cutoff) || r.ID <= capThreshold {
			evictIDs = append(evictIDs, r.ID)
			if len(evictIDs) == batchSize {
				break
			}
		}
	}

	if len(evictIDs) == 0 {
		return 0, nil
	}

	evictSet := make(map[int64]bool, len(evictIDs))
	for _, id := range evictIDs {
		evictSet[id] = true
	}

	kept := s.clientlog.rows[:0]
	for _, r := range s.clientlog.rows {
		if !evictSet[r.ID] {
			kept = append(kept, r)
		}
	}
	deleted := len(s.clientlog.rows) - len(kept)
	s.clientlog.rows = kept
	return deleted, nil
}
