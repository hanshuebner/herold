package email_test

// late_ancestor_merge_test.go -- REQ-STORE-40 late-ancestor thread merge
// (issue #485), observed through the JMAP wire surface rather than the
// store API directly: a reply ingested before the message it answers
// must be reported as an updated Email (not a second creation) once the
// ancestor arrives and the store merges the two threads, and Thread/get
// /changes must reflect the merged membership.

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

func TestLateAncestorMerge_JMAP(t *testing.T) {
	testLateAncestorMerge_JMAP(t, setupFixture(t))
}

func TestLateAncestorMerge_JMAP_Postgres(t *testing.T) {
	testLateAncestorMerge_JMAP(t, setupFixturePostgres(t))
}

// insertMsgWithHeaders stores a message with an explicit Message-ID and
// In-Reply-To directly via the store (the SMTP-delivery/IMAP-import
// shape this defect actually arrives through; a JMAP client never
// controls these headers for inbound mail) and returns its store id.
func insertMsgWithHeaders(t *testing.T, f *fixture, messageID, inReplyTo, subject string) store.MessageID {
	t.Helper()
	now := f.srv.Clock.Now()
	ref := f.putBlob(t, "body-"+messageID)
	msg := store.Message{
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Envelope: store.Envelope{
			Subject:   subject,
			MessageID: messageID,
			InReplyTo: inReplyTo,
			Date:      now,
		},
	}
	if _, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg,
		[]store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage %s: %v", messageID, err)
	}
	return mostRecentMessageID(t, f)
}

func testLateAncestorMerge_JMAP(t *testing.T, f *fixture) {
	t.Helper()
	acct := protojmap.AccountIDForPrincipal(f.pid)

	// The reply arrives first; its ancestor is not yet in the store.
	replyID := insertMsgWithHeaders(t, f, "jmap-late-reply@test", "<jmap-late-orig@test>", "Hello")

	_, rawEmailState := f.invoke(t, "Email/get", map[string]any{
		"accountId": acct,
		"ids":       []string{},
	})
	var emailState0 struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rawEmailState, &emailState0); err != nil {
		t.Fatalf("unmarshal Email/get state: %v: %s", err, rawEmailState)
	}

	_, rawThreadState := f.invoke(t, "Thread/get", map[string]any{
		"accountId": acct,
	})
	var threadState0 struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rawThreadState, &threadState0); err != nil {
		t.Fatalf("unmarshal Thread/get state: %v: %s", err, rawThreadState)
	}

	// The ancestor arrives late.
	origID := insertMsgWithHeaders(t, f, "jmap-late-orig@test", "", "Hello")

	// Email/changes since the reply's own arrival must report the reply
	// as updated (its threadId moved) and the original as created --
	// never the reply as a second creation.
	_, rawEmailChanges := f.invoke(t, "Email/changes", map[string]any{
		"accountId":  acct,
		"sinceState": emailState0.State,
	})
	var emailChanges struct {
		Created []string `json:"created"`
		Updated []string `json:"updated"`
	}
	if err := json.Unmarshal(rawEmailChanges, &emailChanges); err != nil {
		t.Fatalf("unmarshal Email/changes: %v: %s", err, rawEmailChanges)
	}
	replyJMAPID := jmapEmailID(replyID)
	origJMAPID := jmapEmailID(origID)
	if !containsStr(emailChanges.Created, origJMAPID) {
		t.Fatalf("Email/changes created = %v, want to contain the original %s", emailChanges.Created, origJMAPID)
	}
	if !containsStr(emailChanges.Updated, replyJMAPID) {
		t.Fatalf("Email/changes updated = %v, want to contain the merged reply %s", emailChanges.Updated, replyJMAPID)
	}
	if containsStr(emailChanges.Created, replyJMAPID) {
		t.Fatalf("Email/changes created = %v must not contain the reply %s (it already existed)", emailChanges.Created, replyJMAPID)
	}

	// Both messages must now report the same threadId.
	_, rawGet := f.invoke(t, "Email/get", map[string]any{
		"accountId":  acct,
		"ids":        []string{replyJMAPID, origJMAPID},
		"properties": []string{"threadId"},
	})
	var get struct {
		List []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawGet, &get); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, rawGet)
	}
	threadByID := map[string]string{}
	for _, e := range get.List {
		threadByID[e.ID] = e.ThreadID
	}
	mergedThreadID := threadByID[origJMAPID]
	if mergedThreadID == "" {
		t.Fatalf("original's threadId is empty: %+v", get.List)
	}
	if threadByID[replyJMAPID] != mergedThreadID {
		t.Fatalf("reply threadId = %q, want the merged thread %q (same as the original's)",
			threadByID[replyJMAPID], mergedThreadID)
	}

	// Thread/changes since the reply's own arrival must report the merged
	// thread.
	_, rawThreadChanges := f.invoke(t, "Thread/changes", map[string]any{
		"accountId":  acct,
		"sinceState": threadState0.State,
	})
	var threadChanges struct {
		Created []string `json:"created"`
		Updated []string `json:"updated"`
	}
	if err := json.Unmarshal(rawThreadChanges, &threadChanges); err != nil {
		t.Fatalf("unmarshal Thread/changes: %v: %s", err, rawThreadChanges)
	}
	if !containsStr(threadChanges.Updated, mergedThreadID) && !containsStr(threadChanges.Created, mergedThreadID) {
		t.Fatalf("Thread/changes reported neither created nor updated for the merged thread %s: created=%v updated=%v",
			mergedThreadID, threadChanges.Created, threadChanges.Updated)
	}

	// Thread/get on the merged thread must list both messages.
	_, rawThreadGet := f.invoke(t, "Thread/get", map[string]any{
		"accountId": acct,
		"ids":       []string{mergedThreadID},
	})
	var threadGet struct {
		List []struct {
			ID       string   `json:"id"`
			EmailIDs []string `json:"emailIds"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawThreadGet, &threadGet); err != nil {
		t.Fatalf("unmarshal Thread/get: %v: %s", err, rawThreadGet)
	}
	if len(threadGet.List) != 1 {
		t.Fatalf("Thread/get(%s) list = %+v, want exactly 1 thread", mergedThreadID, threadGet.List)
	}
	if !containsStr(threadGet.List[0].EmailIDs, replyJMAPID) || !containsStr(threadGet.List[0].EmailIDs, origJMAPID) {
		t.Fatalf("merged thread emailIds = %v, want both %s and %s",
			threadGet.List[0].EmailIDs, replyJMAPID, origJMAPID)
	}
}

func jmapEmailID(id store.MessageID) string {
	return strconv.FormatUint(uint64(id), 10)
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
