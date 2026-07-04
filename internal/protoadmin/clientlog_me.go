package protoadmin

// clientlog_me.go implements the self-service client-log readback endpoint
// (re #83).
//
// GET /api/v1/me/clientlog
//   Auth: auth1 (valid session or API key — no admin scope, no elevation).
//   Returns the caller's own recent rows from the AUTHENTICATED slice only.
//   The principal-id filter is applied server-side from the authenticated
//   context; a caller cannot read another principal's rows.
//
// Query parameters:
//   limit      maximum rows to return (default 100, capped at 500)
//   session_id filter to a specific SPA session UUID
//   since      RFC 3339 lower bound on server_ts (exclusive below this time)
//   cursor     opaque pagination token from a prior call

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// meClientLogLimitCap is the maximum value a caller may request.
const meClientLogLimitCap = 500

// handleMeClientLog implements GET /api/v1/me/clientlog.
//
// The caller receives only their own rows from the authenticated slice.
// The user_id predicate is built server-side from the authenticated principal;
// there is no client-supplied user_id parameter.
func (s *Server) handleMeClientLog(w http.ResponseWriter, r *http.Request) {
	caller, ok := principalFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusUnauthorized,
			"unauthorized", "authentication required", "")
		return
	}

	// Format matches the user_id stored during ingest: decimal principal ID.
	callerUserID := fmt.Sprintf("%d", caller.ID)

	opts, perr := parseMeClientLogOptions(r.URL.Query())
	if perr != nil {
		writeProblem(w, r, http.StatusBadRequest, "clientlog/invalid_query",
			"invalid query parameter", perr.Error())
		return
	}

	// Enforce server-side ownership: always the auth slice, always the caller.
	opts.Filter.Slice = store.ClientLogSliceAuth
	opts.Filter.UserID = callerUserID

	rows, next, err := s.store.Meta().ListClientLogByCursor(r.Context(), opts)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	out := listClientLogResponse{
		Rows:       make([]clientLogRowDTO, 0, len(rows)),
		NextCursor: next,
	}
	for _, row := range rows {
		out.Rows = append(out.Rows, toClientLogRowDTO(row))
	}
	writeJSON(w, http.StatusOK, out)
}

// parseMeClientLogOptions converts URL query values into cursor options for
// the self-service endpoint. Only the filters that a user may legitimately
// narrow by are exposed; cross-user admin filters (app, kind, level, route,
// user, request_id, until, text) are intentionally absent.
func parseMeClientLogOptions(q map[string][]string) (store.ClientLogCursorOptions, error) {
	opts := store.ClientLogCursorOptions{
		Cursor: getOne(q, "cursor"),
		Limit:  100,
	}
	if v := getOne(q, "session_id"); v != "" {
		opts.Filter.SessionID = v
	}
	if v := getOne(q, "since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return opts, errors.New("since must be RFC 3339")
		}
		opts.Filter.Since = t
	}
	if v := getOne(q, "limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return opts, errors.New("limit must be a positive integer")
		}
		if n > meClientLogLimitCap {
			n = meClientLogLimitCap
		}
		opts.Limit = n
	}
	return opts, nil
}
