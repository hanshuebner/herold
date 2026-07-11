package store

import (
	"encoding/json"
	"time"
)

// EmailBulkJobID identifies a background bulk-mutation job created by
// JMAP `Email/setByQuery` (REQ-PROTO-40..48 vendor extension,
// capability https://netzhansa.com/jmap/email-bulk-mutation).
type EmailBulkJobID uint64

// EmailBulkJobStatus is the job's lifecycle state, surfaced verbatim on
// the JMAP wire by `EmailBulkJob/get`.
type EmailBulkJobStatus string

const (
	// EmailBulkJobStatusRunning is the initial state: the request has
	// been acknowledged and the background worker is (or will soon be)
	// resolving targets and/or applying the patch.
	EmailBulkJobStatusRunning EmailBulkJobStatus = "running"
	// EmailBulkJobStatusDone means every matched message was patched
	// successfully (Processed == Total, no failures).
	EmailBulkJobStatusDone EmailBulkJobStatus = "done"
	// EmailBulkJobStatusPartial means every matched message was
	// attempted (Processed == Total) but at least one failed; the
	// failure detail is in Failures.
	EmailBulkJobStatusPartial EmailBulkJobStatus = "partial"
	// EmailBulkJobStatusFailed is a job-level failure — target
	// resolution itself errored, not a per-message failure. Terminal;
	// ErrorMessage carries the reason.
	EmailBulkJobStatusFailed EmailBulkJobStatus = "failed"
)

// EmailBulkJobFailure is one per-message failure recorded against a
// job. Surfaced on the wire as parallel `failedIds` / `errors` arrays.
type EmailBulkJobFailure struct {
	MessageID MessageID
	Error     string
}

// EmailBulkJob is the persisted row backing both the JMAP
// `EmailBulkJob/get` response and the background worker's resumable
// state. Total == -1 means the target set has not yet been resolved
// (the worker resolves it off the request path, per REQ-PERF-DEADLINE —
// see docs/design/server/requirements/01-protocols.md REQ-PROTO-40..48).
type EmailBulkJob struct {
	ID          EmailBulkJobID
	PrincipalID PrincipalID
	// FilterJSON is the raw JMAP Email/query filter criterion the job
	// was created with (opaque to the store; the protojmap/mail/email
	// package interprets it).
	FilterJSON string
	// PatchJSON is the raw JMAP Email/set per-object update patch
	// applied to every matched message (opaque to the store). Empty
	// string is a reserved sentinel meaning "destroy every matched
	// message" instead of patching it (issue #179) — Email/setByQuery
	// never persists an empty patch for a patch job (validated at the
	// JMAP handler), so the two cases can never collide.
	PatchJSON string
	Status    EmailBulkJobStatus
	// MatchedEstimate is the best-effort count computed synchronously
	// at creation time (an indexed SQL COUNT for SQL-pushable filters;
	// 0 when the filter required background resolution).
	MatchedEstimate int64
	// Total is the authoritative match count once resolved; -1 before
	// resolution.
	Total int64
	// Processed is the number of targets attempted so far (success or
	// failure); reaches Total when the job is terminal.
	Processed int64
	Failures  []EmailBulkJobFailure
	// ErrorMessage is set only for EmailBulkJobStatusFailed (a job-level
	// failure, e.g. target resolution errored).
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EmailBulkJobCreate is the input to Metadata.CreateEmailBulkJob.
// PatchJSON == "" creates a destroy job (issue #179); see the
// EmailBulkJob.PatchJSON doc comment.
type EmailBulkJobCreate struct {
	PrincipalID     PrincipalID
	FilterJSON      string
	PatchJSON       string
	MatchedEstimate int64
}

// EmailBulkJobOutcome is one message's patch-application result, as
// recorded by Metadata.RecordEmailBulkJobBatch. Err == "" means success.
type EmailBulkJobOutcome struct {
	MessageID MessageID
	Err       string
}

// EncodeEmailBulkJobTargetIDs renders a message-id list to the JSON
// array form stored in the email_bulk_jobs.target_ids_json column.
// Shared by both backends so the wire encoding never drifts between
// them.
func EncodeEmailBulkJobTargetIDs(ids []MessageID) (string, error) {
	raw := make([]uint64, len(ids))
	for i, id := range ids {
		raw[i] = uint64(id)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeEmailBulkJobTargetIDs parses the JSON array form back into a
// message-id list. Empty input decodes to an empty (nil) slice.
func DecodeEmailBulkJobTargetIDs(s string) ([]MessageID, error) {
	if s == "" {
		return nil, nil
	}
	var raw []uint64
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	out := make([]MessageID, len(raw))
	for i, v := range raw {
		out[i] = MessageID(v)
	}
	return out, nil
}

// emailBulkJobFailureWire is the JSON-array element shape stored in the
// email_bulk_jobs.failures_json column: parallel to failedIds/errors on
// the JMAP wire but kept together here so the two arrays can never
// drift out of index-alignment under concurrent batch appends.
type emailBulkJobFailureWire struct {
	MessageID uint64 `json:"id"`
	Error     string `json:"error"`
}

// EncodeEmailBulkJobFailures renders the failure list to its JSON
// column form.
func EncodeEmailBulkJobFailures(failures []EmailBulkJobFailure) (string, error) {
	raw := make([]emailBulkJobFailureWire, len(failures))
	for i, f := range failures {
		raw[i] = emailBulkJobFailureWire{MessageID: uint64(f.MessageID), Error: f.Error}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeEmailBulkJobFailures parses the JSON column form back into a
// failure list. Empty input decodes to an empty (nil) slice.
func DecodeEmailBulkJobFailures(s string) ([]EmailBulkJobFailure, error) {
	if s == "" {
		return nil, nil
	}
	var raw []emailBulkJobFailureWire
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	out := make([]EmailBulkJobFailure, len(raw))
	for i, f := range raw {
		out[i] = EmailBulkJobFailure{MessageID: MessageID(f.MessageID), Error: f.Error}
	}
	return out, nil
}
