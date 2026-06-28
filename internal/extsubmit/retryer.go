package extsubmit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultHoldWindow is the maximum time a parked submission is kept before
// the Retryer abandons it. 72 hours covers a full weekend so users who
// re-authenticate Monday morning still get delivery for messages sent Friday
// afternoon.
const DefaultHoldWindow = 72 * time.Hour

// RetryMetaStore is the metadata surface the Retryer uses. It is a narrow
// subset of store.Metadata; tests inject a stub.
type RetryMetaStore interface {
	// ListEmailSubmissionsHeldForReauth returns submissions parked for the
	// given identity (re #70, REQ-AUTH-EXT-SUBMIT-05).
	ListEmailSubmissionsHeldForReauth(ctx context.Context, identityID string) ([]store.EmailSubmissionRow, error)
	// GetMessage returns the full message row so the Retryer can locate the
	// body blob.
	GetMessage(ctx context.Context, id store.MessageID) (store.Message, error)
	// UpdateEmailSubmissionHeld clears or re-sets the held flag and updates
	// the outcome fields.
	UpdateEmailSubmissionHeld(ctx context.Context, id string, heldForReauth bool, undoStatus string, properties []byte) error
	// IncrementJMAPState bumps the per-principal EmailSubmission state
	// counter so JMAP push clients observe the transition.
	IncrementJMAPState(ctx context.Context, pid store.PrincipalID, kind store.JMAPStateKind) (int64, error)
}

// RetryBlobGetter reads body blobs from the blob store.
type RetryBlobGetter interface {
	// Get opens a ReadCloser for the blob identified by hash.
	Get(ctx context.Context, hash string) (io.ReadCloser, error)
}

// RetrySubmitter is the narrow submission interface the Retryer uses to
// re-attempt an external send. Production wires *Submitter; tests inject a
// stub.
type RetrySubmitter interface {
	Submit(ctx context.Context, sub store.IdentitySubmission, env Envelope) Outcome
}

// retrySubmissionProperties is the JSON shape the Retryer reads/writes in
// EmailSubmissionRow.Properties. It mirrors the emailsubmission package's
// externalSubmissionProperties type; kept local to avoid a circular import.
type retrySubmissionProperties struct {
	RcptTo   []string `json:"rcptTo,omitempty"`
	ExtState string   `json:"extState,omitempty"`
	ExtDiag  string   `json:"extDiag,omitempty"`
	MailFrom string   `json:"mailFrom,omitempty"`
}

// Retryer retries parked external submissions when an identity's auth state
// transitions from auth-failed to ok (re #70, REQ-AUTH-EXT-SUBMIT-05).
//
// Call RetryForIdentity from:
//   - the Sweeper's refreshRow success path (sweeper-driven token refresh)
//   - the OAuth callback handler after a successful code exchange
//     (user-driven re-authentication)
//
// Parked submissions whose HoldDeadlineUs has elapsed are abandoned: they
// are marked final/delivered=no with a "hold window expired" diagnostic and
// the JMAP state is bumped so push clients see the transition.
type Retryer struct {
	// Meta is the metadata store surface. Required.
	Meta RetryMetaStore
	// Blobs provides body blob access. Required.
	Blobs RetryBlobGetter
	// Submit re-attempts the external send. Required.
	Submit RetrySubmitter
	// Now is a clock function for deterministic testing.
	// Defaults to time.Now when nil.
	Now func() time.Time
	// HoldWindow is the maximum duration a submission is held.
	// Defaults to DefaultHoldWindow when zero.
	HoldWindow time.Duration
	// Logger is the structured logger.
	// Defaults to slog.Default when nil.
	Logger *slog.Logger
}

func (r *Retryer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Retryer) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *Retryer) holdWindow() time.Duration {
	if r.HoldWindow > 0 {
		return r.HoldWindow
	}
	return DefaultHoldWindow
}

// RetryForIdentity attempts redelivery for every submission parked under
// identityID. sub is the freshly-recovered IdentitySubmission row (state=ok)
// used to authenticate the re-attempt. The call is best-effort: individual
// row failures are logged and skipped; the function always returns.
func (r *Retryer) RetryForIdentity(ctx context.Context, identityID string, sub store.IdentitySubmission) {
	rows, err := r.Meta.ListEmailSubmissionsHeldForReauth(ctx, identityID)
	if err != nil {
		r.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.retryer: list held submissions",
			slog.String("identity_id", identityID),
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		return
	}
	if len(rows) == 0 {
		return
	}
	r.logger().LogAttrs(ctx, slog.LevelInfo, "extsubmit.retryer: retrying parked submissions",
		slog.String("identity_id", identityID),
		slog.String("activity", observe.ActivityUser),
		slog.Int("count", len(rows)))

	for _, row := range rows {
		r.retryRow(ctx, row, sub)
	}
}

// retryRow attempts one redelivery. On hold-window expiry it abandons the
// row immediately. On OutcomeOK it clears the parked state. On OutcomeAuthFailed
// it leaves the row parked (the identity will transition back to auth-failed and
// the sweeper will retry on the next successful refresh). On OutcomePermanent it
// abandons immediately. On transient/unreachable within the hold window it stays
// parked for the next retry cycle.
func (r *Retryer) retryRow(ctx context.Context, row store.EmailSubmissionRow, sub store.IdentitySubmission) {
	now := r.now()

	// Decode current properties so we can update ExtState/ExtDiag while
	// preserving RcptTo/MailFrom.
	var props retrySubmissionProperties
	if len(row.Properties) > 0 {
		_ = json.Unmarshal(row.Properties, &props)
	}

	// Hold-window expiry: abandon without re-attempting.
	if row.HoldDeadlineUs > 0 && now.UnixMicro() >= row.HoldDeadlineUs {
		r.logger().LogAttrs(ctx, slog.LevelInfo, "extsubmit.retryer: hold window expired; abandoning",
			slog.String("identity_id", row.IdentityID),
			slog.String("submission_id", row.ID),
			slog.String("activity", observe.ActivityUser))
		props.ExtState = string(OutcomeAuthFailed)
		props.ExtDiag = fmt.Sprintf("delivery abandoned: hold window of %s expired without successful re-authentication", r.holdWindow())
		r.finalise(ctx, row, false, "final", props)
		return
	}

	// Read message body for re-submission.
	msg, err := r.Meta.GetMessage(ctx, row.EmailID)
	if err != nil {
		r.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.retryer: get message",
			slog.String("submission_id", row.ID),
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		// Leave parked; will retry on next sweep.
		return
	}
	if msg.Blob.Hash == "" {
		r.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.retryer: message has empty blob hash",
			slog.String("submission_id", row.ID),
			slog.String("activity", observe.ActivityInternal))
		return
	}

	body, err := r.Blobs.Get(ctx, msg.Blob.Hash)
	if err != nil {
		r.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.retryer: open blob",
			slog.String("submission_id", row.ID),
			slog.String("blob_hash", msg.Blob.Hash),
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		// Leave parked; transient blob read failure.
		return
	}
	defer body.Close()

	// Buffer the body so the Submitter receives a fresh reader per attempt.
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		r.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.retryer: read blob",
			slog.String("submission_id", row.ID),
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		return
	}

	env := Envelope{
		MailFrom:      props.MailFrom,
		RcptTo:        props.RcptTo,
		Body:          bytes.NewReader(bodyBytes),
		CorrelationID: row.ID,
	}

	outcome := r.Submit.Submit(ctx, sub, env)

	switch outcome.State {
	case OutcomeOK:
		r.logger().LogAttrs(ctx, slog.LevelInfo, "extsubmit.retryer: redelivery succeeded",
			slog.String("submission_id", row.ID),
			slog.String("identity_id", row.IdentityID),
			slog.String("activity", observe.ActivityUser),
			slog.String("mta_id", outcome.MTAID))
		props.ExtState = string(OutcomeOK)
		props.ExtDiag = outcome.Diagnostic
		r.finalise(ctx, row, false, "final", props)

	case OutcomeAuthFailed:
		// Auth is still failing. Leave the submission parked; the identity
		// will transition back to auth-failed and the sweeper will retry on
		// the next successful refresh cycle.
		r.logger().LogAttrs(ctx, slog.LevelWarn, "extsubmit.retryer: redelivery auth-failed; staying parked",
			slog.String("submission_id", row.ID),
			slog.String("identity_id", row.IdentityID),
			slog.String("activity", observe.ActivityUser),
			slog.String("diagnostic", outcome.Diagnostic))

	case OutcomePermanent:
		// 5xx from the upstream MTA; abandon immediately regardless of hold window.
		r.logger().LogAttrs(ctx, slog.LevelInfo, "extsubmit.retryer: redelivery permanent failure; abandoning",
			slog.String("submission_id", row.ID),
			slog.String("identity_id", row.IdentityID),
			slog.String("activity", observe.ActivityUser),
			slog.String("diagnostic", outcome.Diagnostic))
		props.ExtState = string(OutcomePermanent)
		props.ExtDiag = outcome.Diagnostic
		r.finalise(ctx, row, false, "final", props)

	default:
		// Transient or unreachable within the hold window: leave parked.
		r.logger().LogAttrs(ctx, slog.LevelDebug, "extsubmit.retryer: redelivery transient failure; staying parked",
			slog.String("submission_id", row.ID),
			slog.String("identity_id", row.IdentityID),
			slog.String("activity", observe.ActivityUser),
			slog.String("state", string(outcome.State)),
			slog.String("diagnostic", outcome.Diagnostic))
	}
}

// finalise writes the resolved outcome to the store and bumps the JMAP
// EmailSubmission state so push clients observe the change.
func (r *Retryer) finalise(ctx context.Context, row store.EmailSubmissionRow, heldForReauth bool, undoStatus string, props retrySubmissionProperties) {
	propsJSON, _ := json.Marshal(props)
	if err := r.Meta.UpdateEmailSubmissionHeld(ctx, row.ID, heldForReauth, undoStatus, propsJSON); err != nil {
		r.logger().LogAttrs(ctx, slog.LevelError, "extsubmit.retryer: update held status",
			slog.String("submission_id", row.ID),
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
		return
	}
	if _, err := r.Meta.IncrementJMAPState(ctx, row.PrincipalID, store.JMAPStateKindEmailSubmission); err != nil {
		r.logger().LogAttrs(ctx, slog.LevelWarn, "extsubmit.retryer: bump jmap state",
			slog.String("submission_id", row.ID),
			slog.String("activity", observe.ActivityInternal),
			slog.String("err", err.Error()))
	}
}
