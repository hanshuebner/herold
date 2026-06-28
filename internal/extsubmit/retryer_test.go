package extsubmit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/extsubmit"
	"github.com/hanshuebner/herold/internal/store"
)

// --- stubs ---

type stubRetryMeta struct {
	rows      []store.EmailSubmissionRow
	updates   []stubUpdate
	jmapBumps int
	getMsg    func(id store.MessageID) (store.Message, error)
}

type stubUpdate struct {
	id            string
	heldForReauth bool
	undoStatus    string
	properties    []byte
}

func (m *stubRetryMeta) ListEmailSubmissionsHeldForReauth(_ context.Context, identityID string) ([]store.EmailSubmissionRow, error) {
	var out []store.EmailSubmissionRow
	for _, r := range m.rows {
		if r.IdentityID == identityID && r.HeldForReauth {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *stubRetryMeta) GetMessage(_ context.Context, id store.MessageID) (store.Message, error) {
	if m.getMsg != nil {
		return m.getMsg(id)
	}
	return store.Message{ID: id, Blob: store.BlobRef{Hash: "testhash"}}, nil
}

func (m *stubRetryMeta) UpdateEmailSubmissionHeld(_ context.Context, id string, held bool, undo string, props []byte) error {
	m.updates = append(m.updates, stubUpdate{id: id, heldForReauth: held, undoStatus: undo, properties: props})
	// Update in-memory rows so ListEmailSubmissionsHeldForReauth reflects the change.
	for i := range m.rows {
		if m.rows[i].ID == id {
			m.rows[i].HeldForReauth = held
			m.rows[i].UndoStatus = undo
			m.rows[i].Properties = props
		}
	}
	return nil
}

func (m *stubRetryMeta) IncrementJMAPState(_ context.Context, _ store.PrincipalID, _ store.JMAPStateKind) (int64, error) {
	m.jmapBumps++
	return int64(m.jmapBumps), nil
}

type stubBlobGetter struct {
	data []byte
}

func (b *stubBlobGetter) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.data)), nil
}

type stubSubmitter struct {
	outcomes []extsubmit.Outcome
	calls    int
}

func (s *stubSubmitter) Submit(_ context.Context, _ store.IdentitySubmission, _ extsubmit.Envelope) extsubmit.Outcome {
	if s.calls < len(s.outcomes) {
		o := s.outcomes[s.calls]
		s.calls++
		return o
	}
	return extsubmit.Outcome{State: extsubmit.OutcomeTransient, Diagnostic: "no more outcomes"}
}

func makeHeldRow(id, identityID string, deadline time.Time) store.EmailSubmissionRow {
	props, _ := json.Marshal(map[string]any{
		"rcptTo":   []string{"recipient@example.com"},
		"mailFrom": "sender@example.com",
		"extState": "auth-failed",
	})
	return store.EmailSubmissionRow{
		ID:             id,
		EnvelopeID:     store.EnvelopeID(id),
		PrincipalID:    store.PrincipalID(1),
		IdentityID:     identityID,
		EmailID:        store.MessageID(42),
		UndoStatus:     "pending",
		HeldForReauth:  true,
		HoldDeadlineUs: deadline.UnixMicro(),
		Properties:     props,
	}
}

func makeRetryer(meta *stubRetryMeta, blobs *stubBlobGetter, sub *stubSubmitter, now time.Time, hw time.Duration) *extsubmit.Retryer {
	return &extsubmit.Retryer{
		Meta:       meta,
		Blobs:      blobs,
		Submit:     sub,
		Now:        func() time.Time { return now },
		HoldWindow: hw,
	}
}

// --- tests ---

// TestRetryer_SuccessfulRedelivery: parked submission is retried and delivered
// when the identity returns to ok.
func TestRetryer_SuccessfulRedelivery(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(72 * time.Hour)
	meta := &stubRetryMeta{
		rows: []store.EmailSubmissionRow{makeHeldRow("sub-001", "ident-1", deadline)},
	}
	blobs := &stubBlobGetter{data: []byte("raw MIME body")}
	sub := &stubSubmitter{outcomes: []extsubmit.Outcome{{State: extsubmit.OutcomeOK, MTAID: "mta-abc"}}}

	r := makeRetryer(meta, blobs, sub, now, 72*time.Hour)

	identity := store.IdentitySubmission{IdentityID: "ident-1", State: store.IdentitySubmissionStateOK}
	r.RetryForIdentity(context.Background(), "ident-1", identity)

	if len(meta.updates) != 1 {
		t.Fatalf("updates len = %d, want 1", len(meta.updates))
	}
	u := meta.updates[0]
	if u.id != "sub-001" {
		t.Fatalf("update id = %q, want sub-001", u.id)
	}
	if u.heldForReauth {
		t.Fatalf("heldForReauth = true after success, want false")
	}
	if u.undoStatus != "final" {
		t.Fatalf("undoStatus = %q, want final", u.undoStatus)
	}
	// Properties must reflect OutcomeOK state.
	var props map[string]any
	if err := json.Unmarshal(u.properties, &props); err != nil {
		t.Fatalf("unmarshal properties: %v", err)
	}
	if props["extState"] != "ok" {
		t.Fatalf("properties.extState = %v, want ok", props["extState"])
	}
	if meta.jmapBumps != 1 {
		t.Fatalf("jmapBumps = %d, want 1", meta.jmapBumps)
	}
	if sub.calls != 1 {
		t.Fatalf("submit calls = %d, want 1", sub.calls)
	}
}

// TestRetryer_HoldWindowExpiry: a submission whose deadline has passed is
// abandoned (final/no) without re-attempting submission.
func TestRetryer_HoldWindowExpiry(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	// Deadline is 1 microsecond in the past.
	deadline := now.Add(-time.Microsecond)
	meta := &stubRetryMeta{
		rows: []store.EmailSubmissionRow{makeHeldRow("sub-exp", "ident-2", deadline)},
	}
	blobs := &stubBlobGetter{data: []byte("body")}
	sub := &stubSubmitter{} // must NOT be called

	r := makeRetryer(meta, blobs, sub, now, 72*time.Hour)

	identity := store.IdentitySubmission{IdentityID: "ident-2", State: store.IdentitySubmissionStateOK}
	r.RetryForIdentity(context.Background(), "ident-2", identity)

	if sub.calls != 0 {
		t.Fatalf("Submit called %d times; want 0 (hold window expired)", sub.calls)
	}
	if len(meta.updates) != 1 {
		t.Fatalf("updates len = %d, want 1", len(meta.updates))
	}
	u := meta.updates[0]
	if u.heldForReauth {
		t.Fatalf("heldForReauth = true, want false (abandoned)")
	}
	if u.undoStatus != "final" {
		t.Fatalf("undoStatus = %q, want final", u.undoStatus)
	}
	// Diagnostic must mention hold window.
	var props map[string]any
	_ = json.Unmarshal(u.properties, &props)
	diag, _ := props["extDiag"].(string)
	if !strings.Contains(diag, "hold window") {
		t.Fatalf("expiry diagnostic %q does not mention hold window", diag)
	}
}

// TestRetryer_AuthFailedStaysParked: when redelivery returns OutcomeAuthFailed
// the row is left parked (no update written).
func TestRetryer_AuthFailedStaysParked(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(72 * time.Hour)
	meta := &stubRetryMeta{
		rows: []store.EmailSubmissionRow{makeHeldRow("sub-af", "ident-3", deadline)},
	}
	blobs := &stubBlobGetter{data: []byte("body")}
	sub := &stubSubmitter{outcomes: []extsubmit.Outcome{{State: extsubmit.OutcomeAuthFailed, Diagnostic: "credentials rejected"}}}

	r := makeRetryer(meta, blobs, sub, now, 72*time.Hour)

	identity := store.IdentitySubmission{IdentityID: "ident-3", State: store.IdentitySubmissionStateOK}
	r.RetryForIdentity(context.Background(), "ident-3", identity)

	if len(meta.updates) != 0 {
		t.Fatalf("updates len = %d, want 0 (should stay parked)", len(meta.updates))
	}
	if meta.jmapBumps != 0 {
		t.Fatalf("jmapBumps = %d, want 0", meta.jmapBumps)
	}
}

// TestRetryer_PermanentFailureAbandoned: a 5xx permanent failure abandons
// immediately regardless of the hold window.
func TestRetryer_PermanentFailureAbandoned(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(72 * time.Hour)
	meta := &stubRetryMeta{
		rows: []store.EmailSubmissionRow{makeHeldRow("sub-perm", "ident-4", deadline)},
	}
	blobs := &stubBlobGetter{data: []byte("body")}
	sub := &stubSubmitter{outcomes: []extsubmit.Outcome{{State: extsubmit.OutcomePermanent, Diagnostic: "550 mailbox not found"}}}

	r := makeRetryer(meta, blobs, sub, now, 72*time.Hour)

	identity := store.IdentitySubmission{IdentityID: "ident-4", State: store.IdentitySubmissionStateOK}
	r.RetryForIdentity(context.Background(), "ident-4", identity)

	if len(meta.updates) != 1 {
		t.Fatalf("updates len = %d, want 1", len(meta.updates))
	}
	u := meta.updates[0]
	if u.heldForReauth {
		t.Fatalf("heldForReauth = true, want false (permanent failure)")
	}
	if u.undoStatus != "final" {
		t.Fatalf("undoStatus = %q, want final", u.undoStatus)
	}
}

// TestRetryer_TransientStaysParked: transient failure within the hold window
// leaves the row parked.
func TestRetryer_TransientStaysParked(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(72 * time.Hour)
	meta := &stubRetryMeta{
		rows: []store.EmailSubmissionRow{makeHeldRow("sub-tr", "ident-5", deadline)},
	}
	blobs := &stubBlobGetter{data: []byte("body")}
	sub := &stubSubmitter{outcomes: []extsubmit.Outcome{{State: extsubmit.OutcomeTransient, Diagnostic: "421 try again"}}}

	r := makeRetryer(meta, blobs, sub, now, 72*time.Hour)

	identity := store.IdentitySubmission{IdentityID: "ident-5", State: store.IdentitySubmissionStateOK}
	r.RetryForIdentity(context.Background(), "ident-5", identity)

	if len(meta.updates) != 0 {
		t.Fatalf("updates len = %d, want 0 (transient stays parked)", len(meta.updates))
	}
}

// TestRetryer_NoParkedSubmissions: when no submissions are held, RetryForIdentity
// is a no-op and no submit is called.
func TestRetryer_NoParkedSubmissions(t *testing.T) {
	meta := &stubRetryMeta{} // empty
	blobs := &stubBlobGetter{}
	sub := &stubSubmitter{}

	r := makeRetryer(meta, blobs, sub, time.Now(), 72*time.Hour)
	r.RetryForIdentity(context.Background(), "ident-empty", store.IdentitySubmission{IdentityID: "ident-empty"})

	if sub.calls != 0 {
		t.Fatalf("Submit called %d times; want 0", sub.calls)
	}
	if len(meta.updates) != 0 {
		t.Fatalf("updates len = %d; want 0", len(meta.updates))
	}
}
