package email_test

// Issue #550: $snoozed and snoozedUntil form an atomic pair. Email/import
// cannot carry a deadline, so the keyword in the request is not stored;
// the other keywords are. Email/copy reaches the same create path and is
// pinned here too.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

func TestEmail_Import_SnoozedKeywordNotStored_SQLite(t *testing.T) {
	testImportSnoozedKeyword(t, setupFixture(t))
}

func TestEmail_Import_SnoozedKeywordNotStored_Postgres(t *testing.T) {
	testImportSnoozedKeyword(t, setupFixturePostgres(t))
}

func importWithKeywords(t *testing.T, f *fixture, body string, kws map[string]bool) string {
	t.Helper()
	ref := f.putBlob(t, body)
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{"n1": map[string]any{
			"blobId":     ref.Hash,
			"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			"keywords":   kws,
		}},
	})
	var resp struct {
		Created map[string]map[string]any `json:"created"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	id, _ := resp.Created["n1"]["id"].(string)
	if id == "" {
		t.Fatalf("import not created: %s", raw)
	}
	return id
}

type keywordsAndDeadline struct {
	Keywords     map[string]bool `json:"keywords"`
	SnoozedUntil *string         `json:"snoozedUntil"`
}

func getKeywordsAndDeadline(t *testing.T, f *fixture, id string) keywordsAndDeadline {
	t.Helper()
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{id},
		"properties": []string{"keywords", "snoozedUntil"},
	})
	var g struct {
		List []keywordsAndDeadline `json:"list"`
	}
	if err := json.Unmarshal(raw, &g); err != nil || len(g.List) != 1 {
		t.Fatalf("unmarshal get: %v: %s", err, raw)
	}
	return g.List[0]
}

// assertNoHalfPair checks through the JMAP surface that the message does
// not match hasKeyword $snoozed and reports no snoozedUntil.
func assertNoHalfPair(t *testing.T, f *fixture, id string) {
	t.Helper()
	_, raw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"hasKeyword": "$snoozed"},
	})
	var q struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatalf("unmarshal query: %v: %s", err, raw)
	}
	for _, qid := range q.IDs {
		if qid == id {
			t.Errorf("message %s matches hasKeyword $snoozed without a deadline", id)
		}
	}
	g := getKeywordsAndDeadline(t, f, id)
	if g.SnoozedUntil != nil {
		t.Errorf("snoozedUntil = %v, want null", *g.SnoozedUntil)
	}
	if g.Keywords["$snoozed"] {
		t.Errorf("keywords carry $snoozed: %v", g.Keywords)
	}
}

func testImportSnoozedKeyword(t *testing.T, f *fixture) {
	// Fresh create: $snoozed dropped, the other keywords kept.
	id := importWithKeywords(t, f,
		"From: a@example.test\r\nTo: b@example.test\r\nMessage-ID: <s550a@example.test>\r\nSubject: one\r\n\r\none",
		map[string]bool{"$snoozed": true, "$seen": true, "project": true})
	assertNoHalfPair(t, f, id)
	g := getKeywordsAndDeadline(t, f, id)
	if !g.Keywords["$seen"] || !g.Keywords["project"] {
		t.Errorf("other keywords lost: %v", g.Keywords)
	}

	// Dedup fold onto a live message: same rule.
	body2 := "From: a@example.test\r\nTo: b@example.test\r\nMessage-ID: <s550b@example.test>\r\nSubject: two\r\n\r\ntwo"
	m2 := insertWithMessageID(t, f, body2, "s550b@example.test")
	id2 := importWithKeywords(t, f, body2, map[string]bool{"$snoozed": true})
	if id2 != fmt.Sprintf("%d", m2.ID) {
		t.Fatalf("import did not fold onto the existing message: id %s, want %d", id2, m2.ID)
	}
	assertNoHalfPair(t, f, id2)

	// A message that is really snoozed keeps its pair when re-imported.
	body3 := "From: a@example.test\r\nTo: b@example.test\r\nMessage-ID: <s550c@example.test>\r\nSubject: three\r\n\r\nthree"
	m3 := insertWithMessageID(t, f, body3, "s550c@example.test")
	until := f.srv.Clock.Now().Add(24 * time.Hour)
	if _, err := f.srv.Store.Meta().SetSnooze(context.Background(), m3.ID, f.inbox.ID, &until, nil); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	id3 := importWithKeywords(t, f, body3, map[string]bool{"$snoozed": true})
	if id3 != fmt.Sprintf("%d", m3.ID) {
		t.Fatalf("import did not fold onto the snoozed message: id %s, want %d", id3, m3.ID)
	}
	g3 := getKeywordsAndDeadline(t, f, id3)
	if g3.SnoozedUntil == nil || !g3.Keywords["$snoozed"] {
		t.Errorf("re-import dissolved a real snooze: %+v", g3)
	}
}

// insertWithMessageID stores a message carrying a Message-ID envelope
// field, so Email/import's dedup lookup finds it.
func insertWithMessageID(t *testing.T, f *fixture, body, msgID string) store.Message {
	t.Helper()
	ref := f.putBlob(t, body)
	now := f.srv.Clock.Now()
	msg := store.Message{
		PrincipalID:  f.pid,
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     store.Envelope{From: "a@example.test", To: "b@example.test", MessageID: msgID},
	}
	if _, _, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg, []store.MessageMailbox{{MailboxID: f.inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg.ID = mostRecentMessageID(t, f)
	return msg
}

func TestEmail_Copy_SnoozedKeywordNoHalfPair_SQLite(t *testing.T) {
	testCopySnoozedKeyword(t, setupFixture(t))
}

func TestEmail_Copy_SnoozedKeywordNoHalfPair_Postgres(t *testing.T) {
	testCopySnoozedKeyword(t, setupFixturePostgres(t))
}

func testCopySnoozedKeyword(t *testing.T, f *fixture) {
	body := "From: a@example.test\r\nTo: b@example.test\r\nMessage-ID: <s550d@example.test>\r\nSubject: cp\r\n\r\ncp"
	src := f.insertMessage(t, body, "cp", "a@example.test", "b@example.test", nil, "")
	acct := protojmap.AccountIDForPrincipal(f.pid)
	_, raw := f.invoke(t, "Email/copy", map[string]any{
		"fromAccountId": acct,
		"accountId":     acct,
		"create": map[string]any{"c1": map[string]any{
			"id":         fmt.Sprintf("%d", src.ID),
			"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			"keywords":   map[string]bool{"$snoozed": true},
		}},
	})
	var resp struct {
		Created map[string]map[string]any `json:"created"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	for _, c := range resp.Created {
		id, _ := c["id"].(string)
		assertNoHalfPair(t, f, id)
	}
	assertNoHalfPair(t, f, fmt.Sprintf("%d", src.ID))
}
