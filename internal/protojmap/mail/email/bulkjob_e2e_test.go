package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/store"
)

// TestEmail_SetByQuery_WholeMailboxArchive drives the full whole-mailbox
// async bulk-mutation flow end to end (issue #149/#161,
// REQ-PROTO-40..48):
//
//  1. Email/setByQuery on a filter matching more than one worker batch
//     acks immediately with a jobId + matchedEstimate.
//  2. The background worker (driven here via repeated Tick calls, the
//     same entry point Run loops on) drains the job in batches.
//  3. Every matching message receives the patch; Email/state advances
//     and Email/changes reports the mutations.
//  4. EmailBulkJob/get reports a terminal status with
//     processed == total.
func TestEmail_SetByQuery_WholeMailboxArchive(t *testing.T) {
	f := setupFixture(t)

	const batchSize = 4
	worker := email.NewBulkJobWorker(email.BulkJobWorkerOptions{
		Store:     f.srv.Store,
		Clock:     f.srv.Clock,
		BatchSize: batchSize,
	})

	const messageCount = 13 // more than one batch's worth (13 / 4 = 4 batches)
	for i := 0; i < messageCount; i++ {
		body := fmt.Sprintf("From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: msg %d\r\n\r\nbody %d", i, i)
		f.insertMessage(t, body, fmt.Sprintf("msg %d", i), "sender@example.test", "rcpt@example.test", nil, "")
	}

	// Capture the Email state before the bulk job so Email/changes can be
	// asked "what changed since here" after the job drains.
	_, rawState := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{},
	})
	var stateResp struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rawState, &stateResp); err != nil {
		t.Fatalf("unmarshal Email/get state: %v: %s", err, rawState)
	}
	oldState := stateResp.State

	name, raw := f.invoke(t, "Email/setByQuery", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID)},
		"patch":     map[string]any{"keywords/$seen": true},
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailBulkMutation)
	if name != "Email/setByQuery" {
		t.Fatalf("Email/setByQuery returned %s: %s", name, raw)
	}
	var setByQueryResp struct {
		AccountID       string `json:"accountId"`
		JobID           string `json:"jobId"`
		MatchedEstimate int64  `json:"matchedEstimate"`
	}
	if err := json.Unmarshal(raw, &setByQueryResp); err != nil {
		t.Fatalf("unmarshal Email/setByQuery response: %v: %s", err, raw)
	}
	if setByQueryResp.JobID == "" {
		t.Fatal("Email/setByQuery: empty jobId")
	}
	// REQ-PERF-DEADLINE: the fast (SQL-pushable inMailbox) path gives an
	// authoritative estimate immediately, without walking the match set.
	if setByQueryResp.MatchedEstimate != messageCount {
		t.Errorf("MatchedEstimate = %d, want %d", setByQueryResp.MatchedEstimate, messageCount)
	}

	// Drive the worker to completion. Each Tick processes one bounded
	// step (resolve targets, or one batch); looping until it reports no
	// work mirrors what Run does against a real clock.
	ctx := context.Background()
	ticks := 0
	for {
		worked, err := worker.Tick(ctx)
		if err != nil {
			t.Fatalf("worker.Tick: %v", err)
		}
		ticks++
		if !worked {
			break
		}
		if ticks > 1000 {
			t.Fatal("worker.Tick: did not converge")
		}
	}
	// Resolution + 4 batches (13 messages / batchSize 4) + finalize
	// should take more than one tick, proving the job actually ran in
	// batches rather than being applied in one shot.
	if ticks < 2 {
		t.Errorf("ticks = %d, want the job to have taken multiple batches", ticks)
	}

	// (c) EmailBulkJob/get reports a terminal status with
	// processed == total.
	_, rawJob := f.invoke(t, "EmailBulkJob/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{setByQueryResp.JobID},
	}, protojmap.CapabilityCore, protojmap.CapabilityEmailBulkMutation)
	var jobResp struct {
		List []struct {
			ID        string   `json:"id"`
			Status    string   `json:"status"`
			Processed int64    `json:"processed"`
			Total     int64    `json:"total"`
			FailedIDs []string `json:"failedIds"`
			Errors    []string `json:"errors"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(rawJob, &jobResp); err != nil {
		t.Fatalf("unmarshal EmailBulkJob/get: %v: %s", err, rawJob)
	}
	if len(jobResp.List) != 1 {
		t.Fatalf("EmailBulkJob/get: got %d jobs, want 1: %s", len(jobResp.List), rawJob)
	}
	job := jobResp.List[0]
	if job.Status != "done" {
		t.Errorf("job.Status = %q, want done: %+v", job.Status, job)
	}
	if job.Total != messageCount || job.Processed != messageCount {
		t.Errorf("Total/Processed = %d/%d, want %d/%d", job.Total, job.Processed, messageCount, messageCount)
	}
	if len(job.FailedIDs) != 0 {
		t.Errorf("FailedIDs = %v, want none", job.FailedIDs)
	}

	// (a) every matching email received the patch.
	_, rawQuery := f.invoke(t, "Email/query", map[string]any{
		"accountId":      protojmap.AccountIDForPrincipal(f.pid),
		"filter":         map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID), "hasKeyword": "$seen"},
		"calculateTotal": true,
	})
	var queryResp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rawQuery, &queryResp); err != nil {
		t.Fatalf("unmarshal Email/query: %v: %s", err, rawQuery)
	}
	if queryResp.Total != messageCount {
		t.Errorf("messages with $seen = %d, want %d (patch not applied to all matches)", queryResp.Total, messageCount)
	}

	// (b) Email/state advanced and Email/changes reports the mutations.
	_, rawChanges := f.invoke(t, "Email/changes", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"sinceState": oldState,
	})
	var changesResp struct {
		NewState string   `json:"newState"`
		Updated  []string `json:"updated"`
	}
	if err := json.Unmarshal(rawChanges, &changesResp); err != nil {
		t.Fatalf("unmarshal Email/changes: %v: %s", err, rawChanges)
	}
	if changesResp.NewState == oldState {
		t.Error("Email state did not advance after the bulk job")
	}
	if len(changesResp.Updated) != messageCount {
		t.Errorf("Email/changes reported %d updated ids, want %d", len(changesResp.Updated), messageCount)
	}
}

// TestEmail_SetByQuery_WholeMailboxDestroy drives the query-scoped
// permanent-delete job end to end (issue #179): `destroy: true` instead
// of `patch`, resolved and drained by the same BulkJobWorker, ending
// with every matched message gone from every mailbox rather than
// patched.
func TestEmail_SetByQuery_WholeMailboxDestroy(t *testing.T) {
	f := setupFixture(t)

	const batchSize = 4
	worker := email.NewBulkJobWorker(email.BulkJobWorkerOptions{
		Store:     f.srv.Store,
		Clock:     f.srv.Clock,
		BatchSize: batchSize,
	})

	const messageCount = 9 // more than one batch's worth (9 / 4 = 3 batches)
	for i := 0; i < messageCount; i++ {
		body := fmt.Sprintf("From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: msg %d\r\n\r\nbody %d", i, i)
		f.insertMessage(t, body, fmt.Sprintf("msg %d", i), "sender@example.test", "rcpt@example.test", nil, "")
	}

	name, raw := f.invoke(t, "Email/setByQuery", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID)},
		"destroy":   true,
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailBulkMutation)
	if name != "Email/setByQuery" {
		t.Fatalf("Email/setByQuery returned %s: %s", name, raw)
	}
	var setByQueryResp struct {
		JobID           string `json:"jobId"`
		MatchedEstimate int64  `json:"matchedEstimate"`
	}
	if err := json.Unmarshal(raw, &setByQueryResp); err != nil {
		t.Fatalf("unmarshal Email/setByQuery response: %v: %s", err, raw)
	}
	if setByQueryResp.JobID == "" {
		t.Fatal("Email/setByQuery: empty jobId")
	}
	if setByQueryResp.MatchedEstimate != messageCount {
		t.Errorf("MatchedEstimate = %d, want %d", setByQueryResp.MatchedEstimate, messageCount)
	}

	ctx := context.Background()
	ticks := 0
	for {
		worked, err := worker.Tick(ctx)
		if err != nil {
			t.Fatalf("worker.Tick: %v", err)
		}
		ticks++
		if !worked {
			break
		}
		if ticks > 1000 {
			t.Fatal("worker.Tick: did not converge")
		}
	}
	if ticks < 2 {
		t.Errorf("ticks = %d, want the job to have taken multiple batches", ticks)
	}

	_, rawJob := f.invoke(t, "EmailBulkJob/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{setByQueryResp.JobID},
	}, protojmap.CapabilityCore, protojmap.CapabilityEmailBulkMutation)
	var jobResp struct {
		List []struct {
			Status    string   `json:"status"`
			Processed int64    `json:"processed"`
			Total     int64    `json:"total"`
			FailedIDs []string `json:"failedIds"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawJob, &jobResp); err != nil {
		t.Fatalf("unmarshal EmailBulkJob/get: %v: %s", err, rawJob)
	}
	if len(jobResp.List) != 1 {
		t.Fatalf("EmailBulkJob/get: got %d jobs, want 1: %s", len(jobResp.List), rawJob)
	}
	job := jobResp.List[0]
	if job.Status != "done" {
		t.Errorf("job.Status = %q, want done: %+v", job.Status, job)
	}
	if job.Total != messageCount || job.Processed != messageCount {
		t.Errorf("Total/Processed = %d/%d, want %d/%d", job.Total, job.Processed, messageCount, messageCount)
	}
	if len(job.FailedIDs) != 0 {
		t.Errorf("FailedIDs = %v, want none", job.FailedIDs)
	}

	// Every matched message is gone from the mailbox -- not merely
	// patched -- and the total email count for the mailbox is zero.
	_, rawQuery := f.invoke(t, "Email/query", map[string]any{
		"accountId":      protojmap.AccountIDForPrincipal(f.pid),
		"filter":         map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID)},
		"calculateTotal": true,
	})
	var queryResp struct {
		Total int      `json:"total"`
		IDs   []string `json:"ids"`
	}
	if err := json.Unmarshal(rawQuery, &queryResp); err != nil {
		t.Fatalf("unmarshal Email/query: %v: %s", err, rawQuery)
	}
	if queryResp.Total != 0 {
		t.Errorf("messages remaining in mailbox = %d, want 0 (destroy job did not remove all matches)", queryResp.Total)
	}
}

// TestEmail_SetByQuery_PatchAndDestroyMutuallyExclusive verifies that a
// request carrying both `patch` and `destroy: true` is rejected as
// invalidArguments rather than silently preferring one over the other.
func TestEmail_SetByQuery_PatchAndDestroyMutuallyExclusive(t *testing.T) {
	f := setupFixture(t)

	name, raw := f.invoke(t, "Email/setByQuery", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID)},
		"patch":     map[string]any{"keywords/$seen": true},
		"destroy":   true,
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailBulkMutation)
	if name != "error" {
		t.Fatalf("Email/setByQuery with both patch and destroy returned %s, want error: %s", name, raw)
	}
	var errResp struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v: %s", err, raw)
	}
	if errResp.Type != "invalidArguments" {
		t.Errorf("error type = %q, want invalidArguments: %s", errResp.Type, raw)
	}
}

// TestEmail_SetByQuery_NeitherPatchNorDestroy verifies that omitting
// both `patch` and `destroy` is rejected rather than defaulting to a
// no-op job.
func TestEmail_SetByQuery_NeitherPatchNorDestroy(t *testing.T) {
	f := setupFixture(t)

	name, raw := f.invoke(t, "Email/setByQuery", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID)},
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailBulkMutation)
	if name != "error" {
		t.Fatalf("Email/setByQuery with neither patch nor destroy returned %s, want error: %s", name, raw)
	}
	var errResp struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v: %s", err, raw)
	}
	if errResp.Type != "invalidArguments" {
		t.Errorf("error type = %q, want invalidArguments: %s", errResp.Type, raw)
	}
}

// TestEmail_SetByQuery_PartialFailureContinues verifies that a per-batch
// failure (one target message vanishing mid-job) is surfaced in
// failedIds/errors while every other message in the job still completes
// (REQ-PROTO-40..48 acceptance criterion (d)).
func TestEmail_SetByQuery_PartialFailureContinues(t *testing.T) {
	f := setupFixture(t)

	worker := email.NewBulkJobWorker(email.BulkJobWorkerOptions{
		Store:     f.srv.Store,
		Clock:     f.srv.Clock,
		BatchSize: 10,
	})

	const messageCount = 6
	var ids []store.MessageID
	for i := 0; i < messageCount; i++ {
		body := fmt.Sprintf("From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: msg %d\r\n\r\nbody %d", i, i)
		m := f.insertMessage(t, body, fmt.Sprintf("msg %d", i), "sender@example.test", "rcpt@example.test", nil, "")
		ids = append(ids, m.ID)
	}

	_, raw := f.invoke(t, "Email/setByQuery", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID)},
		"patch":     map[string]any{"keywords/$flagged": true},
	}, protojmap.CapabilityCore, protojmap.CapabilityMail, protojmap.CapabilityEmailBulkMutation)
	var setByQueryResp struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(raw, &setByQueryResp); err != nil {
		t.Fatalf("unmarshal Email/setByQuery response: %v: %s", err, raw)
	}

	ctx := context.Background()
	// First tick: resolves the target list (job.Total becomes messageCount)
	// but does not yet drain any batch.
	if worked, err := worker.Tick(ctx); err != nil || !worked {
		t.Fatalf("worker.Tick (resolve): worked=%v err=%v", worked, err)
	}

	// Inject a failure: expunge one target message out from under the job
	// before the worker's patch-application batch runs. The job already
	// resolved its target-id snapshot, so this message stays a target but
	// updateEmail will now report notFound for it.
	victim := ids[2]
	if err := f.srv.Store.Meta().ExpungeMessages(context.Background(), f.inbox.ID, []store.MessageID{victim}); err != nil {
		t.Fatalf("ExpungeMessages: %v", err)
	}

	// Drain to completion.
	ticks := 1
	for {
		worked, err := worker.Tick(ctx)
		if err != nil {
			t.Fatalf("worker.Tick: %v", err)
		}
		ticks++
		if !worked {
			break
		}
		if ticks > 1000 {
			t.Fatal("worker.Tick: did not converge")
		}
	}

	_, rawJob := f.invoke(t, "EmailBulkJob/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{setByQueryResp.JobID},
	}, protojmap.CapabilityCore, protojmap.CapabilityEmailBulkMutation)
	var jobResp struct {
		List []struct {
			Status    string   `json:"status"`
			Processed int64    `json:"processed"`
			Total     int64    `json:"total"`
			FailedIDs []string `json:"failedIds"`
			Errors    []string `json:"errors"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawJob, &jobResp); err != nil {
		t.Fatalf("unmarshal EmailBulkJob/get: %v: %s", err, rawJob)
	}
	if len(jobResp.List) != 1 {
		t.Fatalf("EmailBulkJob/get: got %d jobs, want 1: %s", len(jobResp.List), rawJob)
	}
	job := jobResp.List[0]

	// Every target was attempted (success or failure): processed reaches
	// total despite the injected failure.
	if job.Processed != messageCount || job.Total != messageCount {
		t.Fatalf("Processed/Total = %d/%d, want %d/%d", job.Processed, job.Total, messageCount, messageCount)
	}
	if job.Status != "partial" {
		t.Errorf("Status = %q, want partial", job.Status)
	}
	if len(job.FailedIDs) != 1 || len(job.Errors) != 1 {
		t.Fatalf("FailedIDs/Errors = %v/%v, want exactly one entry", job.FailedIDs, job.Errors)
	}

	// The rest of the batch still completed: every surviving message
	// carries the patch.
	_, rawQuery := f.invoke(t, "Email/query", map[string]any{
		"accountId":      protojmap.AccountIDForPrincipal(f.pid),
		"filter":         map[string]any{"inMailbox": fmt.Sprintf("%d", f.inbox.ID), "hasKeyword": "$flagged"},
		"calculateTotal": true,
	})
	var queryResp struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rawQuery, &queryResp); err != nil {
		t.Fatalf("unmarshal Email/query: %v: %s", err, rawQuery)
	}
	if queryResp.Total != messageCount-1 {
		t.Errorf("flagged messages = %d, want %d (the rest of the batch should still complete)", queryResp.Total, messageCount-1)
	}
}
