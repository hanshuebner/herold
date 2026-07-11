package email

import (
	"encoding/json"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// This file declares the wire types for the whole-mailbox async
// bulk-mutation vendor extension (issue #149/#161/#179, REQ-PROTO-40..48,
// capability https://netzhansa.com/jmap/email-bulk-mutation):
//
//   - Email/setByQuery resolves a filter (the same criterion shape
//     Email/query accepts) to a match ESTIMATE only and acks
//     immediately with a jobId (REQ-PERF-DEADLINE — see
//     bulkjob_methods.go). The request carries either `patch` (an
//     Email/set-shaped update patch, applied to every match) or
//     `destroy: true` (permanently removes every match from every
//     mailbox, issue #179) — mutually exclusive.
//   - EmailBulkJob/get reports the background job's progress, backed
//     by the row bulkjob_worker.go drains in batches.

// bulkJobIDFromJMAP parses an EmailBulkJob id wire string (a plain
// decimal, matching the Email/Mailbox id convention) into an
// EmailBulkJobID. Empty / unparseable values return (0, false).
func bulkJobIDFromJMAP(id jmapID) (store.EmailBulkJobID, bool) {
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.EmailBulkJobID(v), true
}

// jmapIDFromBulkJob renders an EmailBulkJobID into wire form.
func jmapIDFromBulkJob(id store.EmailBulkJobID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// -- Email/setByQuery --------------------------------------------------

// setByQueryRequest is the wire-form Email/setByQuery request.
type setByQueryRequest struct {
	AccountID jmapID           `json:"accountId"`
	Filter    *json.RawMessage `json:"filter"`
	// Patch is the same per-object update-patch shape Email/set's
	// `update` map values accept (e.g. `keywords/$seen`, `mailboxIds`,
	// `mailboxIds/<id>`). Applied to every message the filter matches.
	// Mutually exclusive with Destroy; exactly one of the two must be
	// given.
	Patch json.RawMessage `json:"patch"`
	// Destroy, when true, permanently removes every message the filter
	// matches from every mailbox it belongs to (issue #179) — the
	// query-scoped equivalent of `Email/set { destroy: [...] }`, run as
	// the same background job archive/delete/label/mark use. Mutually
	// exclusive with Patch.
	Destroy bool `json:"destroy,omitempty"`
}

// setByQueryResponse is the wire-form Email/setByQuery response. The
// server never walks the full match set on this request path — see
// REQ-PERF-DEADLINE in docs/design/server/requirements/01-protocols.md
// REQ-PROTO-40..48.
type setByQueryResponse struct {
	AccountID jmapID `json:"accountId"`
	JobID     jmapID `json:"jobId"`
	// MatchedEstimate is a best-effort count: an indexed SQL COUNT when
	// the filter is SQL-pushable (the common inMailbox/label case), or
	// 0 when the filter requires background resolution — the real
	// total appears in EmailBulkJob/get once the worker resolves it.
	MatchedEstimate int64 `json:"matchedEstimate"`
}

// -- EmailBulkJob/get ----------------------------------------------------

type bulkJobGetRequest struct {
	AccountID jmapID    `json:"accountId"`
	IDs       *[]jmapID `json:"ids"`
}

// jmapBulkJob is the wire-form EmailBulkJob object.
type jmapBulkJob struct {
	ID     jmapID `json:"id"`
	Status string `json:"status"`
	// MatchedEstimate mirrors the value returned by the originating
	// Email/setByQuery call; kept in sync once the worker resolves the
	// authoritative Total.
	MatchedEstimate int64 `json:"matchedEstimate"`
	Processed       int64 `json:"processed"`
	// Total is -1 while the worker has not yet resolved the target set
	// (mirrors store.EmailBulkJob.Total; the client treats -1 as
	// "resolving").
	Total     int64    `json:"total"`
	FailedIDs []jmapID `json:"failedIds"`
	Errors    []string `json:"errors"`
}

type bulkJobGetResponse struct {
	AccountID jmapID        `json:"accountId"`
	State     string        `json:"state"`
	List      []jmapBulkJob `json:"list"`
	NotFound  []jmapID      `json:"notFound"`
}

// renderBulkJob projects a store.EmailBulkJob to the wire form.
func renderBulkJob(j store.EmailBulkJob) jmapBulkJob {
	out := jmapBulkJob{
		ID:              jmapIDFromBulkJob(j.ID),
		Status:          string(j.Status),
		MatchedEstimate: j.MatchedEstimate,
		Processed:       j.Processed,
		Total:           j.Total,
		FailedIDs:       []jmapID{},
		Errors:          []string{},
	}
	for _, f := range j.Failures {
		out.FailedIDs = append(out.FailedIDs, jmapIDFromMessage(f.MessageID))
		out.Errors = append(out.Errors, f.Error)
	}
	return out
}

// rawFilterOrNil converts the stored FilterJSON column ("" or "null"
// means "match everything") into the *json.RawMessage shape
// decodeFilter expects.
func rawFilterOrNil(s string) *json.RawMessage {
	if s == "" || s == "null" {
		return nil
	}
	raw := json.RawMessage(s)
	return &raw
}
