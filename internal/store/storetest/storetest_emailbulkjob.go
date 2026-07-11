package storetest

import (
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// testEmailBulkJob_CreateResolveProcessFinish drives the full lifecycle
// a background worker exercises: create (unresolved), resolve targets,
// drain in batches recording a mix of success/failure outcomes, then
// finish. Verifies the row lands correctly on both backends (issue
// #149/#161, REQ-PROTO-40..48).
func testEmailBulkJob_CreateResolveProcessFinish(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "bulkjob-lifecycle@example.com")

	job, err := s.Meta().CreateEmailBulkJob(ctx, store.EmailBulkJobCreate{
		PrincipalID:     p.ID,
		FilterJSON:      `{"inMailbox":"1"}`,
		PatchJSON:       `{"keywords/$seen":true}`,
		MatchedEstimate: 3,
	})
	if err != nil {
		t.Fatalf("CreateEmailBulkJob: %v", err)
	}
	if job.ID == 0 {
		t.Fatal("CreateEmailBulkJob: returned zero ID")
	}
	if job.Status != store.EmailBulkJobStatusRunning {
		t.Errorf("Status = %q, want running", job.Status)
	}
	if job.Total != -1 {
		t.Errorf("Total = %d, want -1 (unresolved)", job.Total)
	}

	got, err := s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob: %v", err)
	}
	if got.MatchedEstimate != 3 {
		t.Errorf("MatchedEstimate = %d, want 3", got.MatchedEstimate)
	}
	if got.FilterJSON != job.FilterJSON || got.PatchJSON != job.PatchJSON {
		t.Errorf("filter/patch mismatch: got %+v", got)
	}

	// A running job must show up in ListRunningEmailBulkJobs.
	running, err := s.Meta().ListRunningEmailBulkJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRunningEmailBulkJobs: %v", err)
	}
	found := false
	for _, j := range running {
		if j.ID == job.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("ListRunningEmailBulkJobs: job not found")
	}

	// Before resolution, NextEmailBulkJobBatch returns nothing.
	batch, err := s.Meta().NextEmailBulkJobBatch(ctx, job.ID, 10)
	if err != nil {
		t.Fatalf("NextEmailBulkJobBatch (unresolved): %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("NextEmailBulkJobBatch (unresolved) = %v, want empty", batch)
	}

	targets := []store.MessageID{101, 102, 103}
	if err := s.Meta().ResolveEmailBulkJobTargets(ctx, job.ID, targets); err != nil {
		t.Fatalf("ResolveEmailBulkJobTargets: %v", err)
	}
	// Idempotent: a second resolve call must not clobber the first.
	if err := s.Meta().ResolveEmailBulkJobTargets(ctx, job.ID, []store.MessageID{999}); err != nil {
		t.Fatalf("ResolveEmailBulkJobTargets (second call): %v", err)
	}

	got, err = s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob after resolve: %v", err)
	}
	if got.Total != 3 {
		t.Fatalf("Total = %d, want 3", got.Total)
	}

	// Drain in batches of 2.
	batch, err = s.Meta().NextEmailBulkJobBatch(ctx, job.ID, 2)
	if err != nil {
		t.Fatalf("NextEmailBulkJobBatch (batch 1): %v", err)
	}
	if len(batch) != 2 || batch[0] != 101 || batch[1] != 102 {
		t.Fatalf("batch 1 = %v, want [101 102]", batch)
	}
	if err := s.Meta().RecordEmailBulkJobBatch(ctx, job.ID, []store.EmailBulkJobOutcome{
		{MessageID: 101, Err: ""},
		{MessageID: 102, Err: "message not found"},
	}); err != nil {
		t.Fatalf("RecordEmailBulkJobBatch (batch 1): %v", err)
	}

	batch, err = s.Meta().NextEmailBulkJobBatch(ctx, job.ID, 2)
	if err != nil {
		t.Fatalf("NextEmailBulkJobBatch (batch 2): %v", err)
	}
	if len(batch) != 1 || batch[0] != 103 {
		t.Fatalf("batch 2 = %v, want [103]", batch)
	}
	if err := s.Meta().RecordEmailBulkJobBatch(ctx, job.ID, []store.EmailBulkJobOutcome{
		{MessageID: 103, Err: ""},
	}); err != nil {
		t.Fatalf("RecordEmailBulkJobBatch (batch 2): %v", err)
	}

	// Exhausted: no more targets.
	batch, err = s.Meta().NextEmailBulkJobBatch(ctx, job.ID, 2)
	if err != nil {
		t.Fatalf("NextEmailBulkJobBatch (exhausted): %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("NextEmailBulkJobBatch (exhausted) = %v, want empty", batch)
	}

	got, err = s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob after drain: %v", err)
	}
	if got.Processed != 3 {
		t.Errorf("Processed = %d, want 3", got.Processed)
	}
	if len(got.Failures) != 1 || got.Failures[0].MessageID != 102 ||
		got.Failures[0].Error != "message not found" {
		t.Errorf("Failures = %+v, want one entry for message 102", got.Failures)
	}

	if err := s.Meta().FinishEmailBulkJob(ctx, job.ID, store.EmailBulkJobStatusPartial, ""); err != nil {
		t.Fatalf("FinishEmailBulkJob: %v", err)
	}
	got, err = s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob after finish: %v", err)
	}
	if got.Status != store.EmailBulkJobStatusPartial {
		t.Errorf("Status = %q, want partial", got.Status)
	}

	// A job owned by a different principal must not be visible.
	other := mustInsertPrincipal(t, s, "bulkjob-other@example.com")
	if _, err := s.Meta().GetEmailBulkJob(ctx, other.ID, job.ID); err == nil {
		t.Error("GetEmailBulkJob: cross-principal read unexpectedly succeeded")
	}

	// A terminal job must not appear in ListRunningEmailBulkJobs.
	running, err = s.Meta().ListRunningEmailBulkJobs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRunningEmailBulkJobs (after finish): %v", err)
	}
	for _, j := range running {
		if j.ID == job.ID {
			t.Fatal("ListRunningEmailBulkJobs: terminal job still listed as running")
		}
	}
}

// testEmailBulkJob_DestroyJobPatchJSONSentinel verifies that a destroy
// job (PatchJSON == "", issue #179) round-trips its empty PatchJSON
// faithfully on both backends -- CreateEmailBulkJob's caller relies on
// this staying distinct from every patch job, which always has a
// non-empty PatchJSON (validated at the JMAP handler).
func testEmailBulkJob_DestroyJobPatchJSONSentinel(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "bulkjob-destroy@example.com")

	job, err := s.Meta().CreateEmailBulkJob(ctx, store.EmailBulkJobCreate{
		PrincipalID:     p.ID,
		FilterJSON:      `{"inMailbox":"1"}`,
		PatchJSON:       "",
		MatchedEstimate: 2,
	})
	if err != nil {
		t.Fatalf("CreateEmailBulkJob: %v", err)
	}
	if job.PatchJSON != "" {
		t.Errorf("PatchJSON = %q, want empty (destroy sentinel)", job.PatchJSON)
	}

	got, err := s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob: %v", err)
	}
	if got.PatchJSON != "" {
		t.Errorf("GetEmailBulkJob: PatchJSON = %q, want empty (destroy sentinel)", got.PatchJSON)
	}

	// The rest of the lifecycle (resolve/drain/finish) is identical to
	// the patch case and already covered by
	// testEmailBulkJob_CreateResolveProcessFinish; this test only pins
	// down that the empty-string sentinel itself survives a round trip.
	if err := s.Meta().ResolveEmailBulkJobTargets(ctx, job.ID, []store.MessageID{201, 202}); err != nil {
		t.Fatalf("ResolveEmailBulkJobTargets: %v", err)
	}
	if err := s.Meta().RecordEmailBulkJobBatch(ctx, job.ID, []store.EmailBulkJobOutcome{
		{MessageID: 201, Err: ""},
		{MessageID: 202, Err: ""},
	}); err != nil {
		t.Fatalf("RecordEmailBulkJobBatch: %v", err)
	}
	if err := s.Meta().FinishEmailBulkJob(ctx, job.ID, store.EmailBulkJobStatusDone, ""); err != nil {
		t.Fatalf("FinishEmailBulkJob: %v", err)
	}
	got, err = s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob after finish: %v", err)
	}
	if got.Status != store.EmailBulkJobStatusDone || got.Processed != 2 {
		t.Errorf("got %+v, want status=done processed=2", got)
	}
}

// testEmailBulkJob_FinishFailed verifies the job-level failure path
// (target resolution itself errored, distinct from a per-message
// failure) persists the error message.
func testEmailBulkJob_FinishFailed(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "bulkjob-failed@example.com")

	job, err := s.Meta().CreateEmailBulkJob(ctx, store.EmailBulkJobCreate{
		PrincipalID: p.ID,
		PatchJSON:   `{"keywords/$seen":true}`,
	})
	if err != nil {
		t.Fatalf("CreateEmailBulkJob: %v", err)
	}
	if err := s.Meta().FinishEmailBulkJob(ctx, job.ID, store.EmailBulkJobStatusFailed, "resolve: store exploded"); err != nil {
		t.Fatalf("FinishEmailBulkJob: %v", err)
	}
	got, err := s.Meta().GetEmailBulkJob(ctx, p.ID, job.ID)
	if err != nil {
		t.Fatalf("GetEmailBulkJob: %v", err)
	}
	if got.Status != store.EmailBulkJobStatusFailed {
		t.Errorf("Status = %q, want failed", got.Status)
	}
	if got.ErrorMessage != "resolve: store exploded" {
		t.Errorf("ErrorMessage = %q, want %q", got.ErrorMessage, "resolve: store exploded")
	}
}
