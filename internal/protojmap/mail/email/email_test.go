package email_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/protojmap/mail/thread"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

type fixture struct {
	srv     *testharness.Server
	pid     store.PrincipalID
	inbox   store.Mailbox
	client  *http.Client
	baseURL string
	apiKey  string
}

func setupFixture(t *testing.T) *fixture {
	t.Helper()
	srv, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})

	ctx := context.Background()
	p, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plaintext := "hk_test_alice_" + fmt.Sprintf("%d", p.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	inbox, err := srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	mailbox.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)
	email.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)
	thread.Register(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{srv: srv, pid: p.ID, inbox: inbox, client: client, baseURL: base, apiKey: plaintext}
}

func (f *fixture) invoke(t *testing.T, method string, args any, using ...protojmap.CapabilityID) (string, json.RawMessage) {
	t.Helper()
	if len(using) == 0 {
		using = []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityMail}
	}
	argsBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	body := map[string]any{
		"using":       using,
		"methodCalls": []any{[]any{method, json.RawMessage(argsBytes), "c0"}},
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, f.baseURL+"/jmap", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, respBody)
	}
	var envelope struct {
		MethodResponses []protojmap.Invocation `json:"methodResponses"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, respBody)
	}
	if len(envelope.MethodResponses) != 1 {
		t.Fatalf("got %d method responses, want 1: %s", len(envelope.MethodResponses), respBody)
	}
	return envelope.MethodResponses[0].Name, envelope.MethodResponses[0].Args
}

// putBlob inserts body into the blob store and returns the BlobRef.
func (f *fixture) putBlob(t *testing.T, body string) store.BlobRef {
	t.Helper()
	ref, err := f.srv.Store.Blobs().Put(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	return ref
}

// insertMessage stores a message in the inbox via the store directly.
// Used by tests that exercise the read path without driving Email/set.
func (f *fixture) insertMessage(t *testing.T, body, subject, from, to string, kw []string, ftsText string) store.Message {
	t.Helper()
	ref := f.putBlob(t, body)
	now := f.srv.Clock.Now()
	msg := store.Message{
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Envelope: store.Envelope{
			Subject: subject,
			From:    from,
			To:      to,
			Date:    now,
		},
	}
	uid, modseq, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg, []store.MessageMailbox{{MailboxID: f.inbox.ID, Keywords: kw}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msg.UID = uid
	msg.ModSeq = modseq
	msg.ID = mostRecentMessageID(t, f)
	if ftsText != "" {
		if err := f.srv.Store.FTS().IndexMessage(context.Background(), msg, ftsText); err != nil {
			t.Fatalf("FTS index: %v", err)
		}
	}
	return msg
}

// mostRecentMessageID returns the latest EntityKindEmail/Created
// MessageID for the principal.
func mostRecentMessageID(t *testing.T, f *fixture) store.MessageID {
	t.Helper()
	feed, err := f.srv.Store.Meta().ReadChangeFeed(context.Background(), f.pid, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	var last store.MessageID
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			last = store.MessageID(e.EntityID)
		}
	}
	if last == 0 {
		t.Fatalf("no email created in feed")
	}
	return last
}

// -- tests --------------------------------------------------------

func TestEmail_Get_Envelope_Properties(t *testing.T) {
	f := setupFixture(t)
	body := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Hi\r\n\r\nbody"
	m := f.insertMessage(t, body, "Hi", "sender@example.test", "rcpt@example.test", nil, "")

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", m.ID)},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(resp.List), raw)
	}
	got := resp.List[0]
	if got["subject"] != "Hi" {
		t.Errorf("subject = %v, want Hi", got["subject"])
	}
	if got["size"] == nil {
		t.Errorf("size missing")
	}
	from, ok := got["from"].([]any)
	if !ok || len(from) == 0 {
		t.Errorf("from missing or wrong shape: %v", got["from"])
	}
}

func TestEmail_Get_BodyValues_TruncatedAt(t *testing.T) {
	f := setupFixture(t)
	long := strings.Repeat("abcdefghij", 100) // 1000 chars
	body := "From: x@example.test\r\nTo: y@example.test\r\nSubject: long\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + long
	m := f.insertMessage(t, body, "long", "x@example.test", "y@example.test", nil, "")

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{fmt.Sprintf("%d", m.ID)},
		"fetchTextBodyValues": true,
		"maxBodyValueBytes":   50,
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d, want 1: %s", len(resp.List), raw)
	}
	bodyValues, _ := resp.List[0]["bodyValues"].(map[string]any)
	if len(bodyValues) == 0 {
		t.Fatalf("bodyValues empty: %s", raw)
	}
	for _, v := range bodyValues {
		bv := v.(map[string]any)
		val, _ := bv["value"].(string)
		if len(val) > 50 {
			t.Errorf("value len %d > 50 (truncation not applied)", len(val))
		}
		if !bv["isTruncated"].(bool) {
			t.Errorf("isTruncated false on a clearly-truncated body")
		}
	}
}

func TestEmail_Set_Create_FromBlob(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hello\r\n\r\nhello world"
	ref := f.putBlob(t, body)

	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"new1": map[string]any{
				"blobId":     ref.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
				"keywords":   map[string]bool{"$seen": true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
		NewState   string                    `json:"newState"`
		OldState   string                    `json:"oldState"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	if resp.NewState == resp.OldState {
		t.Errorf("state did not advance: old=%q new=%q", resp.OldState, resp.NewState)
	}
	// Verify the message landed in the store.
	feed, _ := f.srv.Store.Meta().ReadChangeFeed(context.Background(), f.pid, 0, 1000)
	var emailCreated int
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			emailCreated++
		}
	}
	if emailCreated != 1 {
		t.Errorf("expected 1 email-created entry, got %d", emailCreated)
	}
}

func TestEmail_Set_Update_Keywords(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nhi"
	m := f.insertMessage(t, body, "hi", "a@example.test", "b@example.test", nil, "")

	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			fmt.Sprintf("%d", m.ID): map[string]any{
				"keywords/$seen": true,
			},
		},
	})
	var resp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Updated) != 1 {
		t.Fatalf("updated=%v notUpdated=%v", resp.Updated, resp.NotUpdated)
	}
	got, _ := f.srv.Store.Meta().GetMessage(context.Background(), m.ID)
	if got.Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("$seen flag not applied: flags=%v", got.Flags)
	}
}

func TestEmail_Query_TextPredicate_BackedByFTS(t *testing.T) {
	f := setupFixture(t)
	f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: payment reminder\r\n\r\nPlease pay the attached invoice promptly.",
		"payment reminder", "a@example.test", "b@example.test", nil, "")
	matchID := mostRecentMessageID(t, f)
	f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: kittens\r\n\r\ncute pictures",
		"kittens", "a@example.test", "b@example.test", nil, "")

	_, raw := f.invoke(t, "Email/query", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"filter":    map[string]any{"body": "invoice"},
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	want := fmt.Sprintf("%d", matchID)
	if len(resp.IDs) != 1 || resp.IDs[0] != want {
		t.Fatalf("ids=%v want [%s] (raw=%s)", resp.IDs, want, raw)
	}
}

func TestEmail_Changes_FromState(t *testing.T) {
	f := setupFixture(t)
	// Read initial state.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{},
	})
	var ge struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &ge)
	startState := ge.State

	// Insert via Email/set so the JMAP state advances in lockstep.
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nhi"
	ref := f.putBlob(t, body)
	_, setRaw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"new1": map[string]any{
				"blobId":     ref.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var sr struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(setRaw, &sr)
	createdID := sr.Created["new1"]["id"].(string)

	_, raw2 := f.invoke(t, "Email/changes", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"sinceState": startState,
	})
	var resp struct {
		Created []string `json:"created"`
	}
	if err := json.Unmarshal(raw2, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw2)
	}
	found := false
	for _, c := range resp.Created {
		if c == createdID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created %v missing %s (raw=%s)", resp.Created, createdID, raw2)
	}
}

func TestEmail_Import_FromUploadedBlob(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: import-me\r\n\r\nimport body"
	ref := f.putBlob(t, body)
	receivedAt := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)

	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"new1": map[string]any{
				"blobId":     ref.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
				"keywords":   map[string]bool{"$flagged": true},
				"receivedAt": receivedAt,
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created=%v notCreated=%v", resp.Created, resp.NotCreated)
	}
	mid, _ := resp.Created["new1"]["id"].(string)
	if mid == "" {
		t.Fatalf("created id missing: %v", resp.Created)
	}
}

func TestEmail_Parse_HeadersWithoutImport(t *testing.T) {
	f := setupFixture(t)
	body := "From: parsed@example.test\r\nTo: rx@example.test\r\nSubject: parse-me\r\nMessage-ID: <msg-1@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nhello"
	ref := f.putBlob(t, body)

	_, raw := f.invoke(t, "Email/parse", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"blobIds":             []string{ref.Hash},
		"fetchTextBodyValues": true,
		"maxBodyValueBytes":   1000,
	})
	var resp struct {
		Parsed      map[string]map[string]any `json:"parsed"`
		NotParsable []string                  `json:"notParsable"`
		NotFound    []string                  `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.Parsed) != 1 {
		t.Fatalf("parsed=%v notParsable=%v notFound=%v", resp.Parsed, resp.NotParsable, resp.NotFound)
	}
	got := resp.Parsed[ref.Hash]
	if got["subject"] != "parse-me" {
		t.Errorf("subject = %v, want parse-me", got["subject"])
	}
	// Confirm no message row was created (Email/parse must not persist).
	feed, _ := f.srv.Store.Meta().ReadChangeFeed(context.Background(), f.pid, 0, 100)
	for _, e := range feed {
		if e.Kind == store.EntityKindEmail && e.Op == store.ChangeOpCreated {
			t.Errorf("Email/parse persisted a message: feed=%+v", feed)
		}
	}
}

// -- REQ-PROTO-49 JMAP snooze ----------------------------------------

func messageHasSnoozeKeyword(t *testing.T, f *fixture, id store.MessageID) bool {
	t.Helper()
	m, err := f.srv.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	for _, k := range m.Keywords {
		if k == "$snoozed" {
			return true
		}
	}
	return false
}

func messageSnoozedUntil(t *testing.T, f *fixture, id store.MessageID) *time.Time {
	t.Helper()
	m, err := f.srv.Store.Meta().GetMessage(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	return m.SnoozedUntil
}

func TestSnoozeCapability_IsAdvertised(t *testing.T) {
	f := setupFixture(t)
	// The Mail register sets CapabilityMailSnooze; the test
	// fixture only registers email + mailbox handlers, so we
	// register the capability descriptor manually here to mirror
	// what mail.Register does (the per-test fixture skips that
	// helper to avoid a circular-package dep).
	dir := directory.New(f.srv.Store.Meta(), f.srv.Logger, f.srv.Clock, nil)
	js := protojmap.NewServer(f.srv.Store, dir, nil, f.srv.Logger, f.srv.Clock, protojmap.Options{})
	mailbox.Register(js.Registry(), f.srv.Store, f.srv.Logger, f.srv.Clock)
	email.Register(js.Registry(), f.srv.Store, f.srv.Logger, f.srv.Clock)
	js.Registry().RegisterCapabilityDescriptor(protojmap.CapabilityMailSnooze, struct{}{})
	if !js.Registry().HasCapability(protojmap.CapabilityMailSnooze) {
		t.Fatalf("snooze capability not advertised")
	}
}

func TestEmailGet_RendersSnoozedUntil(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: snoozed\r\n\r\nbody"
	m := f.insertMessage(t, body, "snoozed", "a@example.test", "b@example.test", nil, "")
	t1 := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := f.srv.Store.Meta().SetSnooze(context.Background(), m.ID, f.inbox.ID, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{fmt.Sprintf("%d", m.ID)},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(resp.List), raw)
	}
	got, ok := resp.List[0]["snoozedUntil"].(string)
	if !ok {
		t.Fatalf("snoozedUntil missing or not a string: %v", resp.List[0]["snoozedUntil"])
	}
	if got != "2030-01-02T03:04:05Z" {
		t.Errorf("snoozedUntil = %q, want 2030-01-02T03:04:05Z", got)
	}
}

func TestEmailSet_SetsSnoozedAtomically(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nhi"
	m := f.insertMessage(t, body, "hi", "a@example.test", "b@example.test", nil, "")
	wakeAt := "2030-06-01T12:00:00Z"
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			fmt.Sprintf("%d", m.ID): map[string]any{
				"snoozedUntil": wakeAt,
			},
		},
	})
	var resp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("notUpdated = %v", resp.NotUpdated)
	}
	if !messageHasSnoozeKeyword(t, f, m.ID) {
		t.Errorf("$snoozed keyword not set after Email/set with snoozedUntil")
	}
	got := messageSnoozedUntil(t, f, m.ID)
	if got == nil {
		t.Fatalf("SnoozedUntil nil after set")
	}
	want, _ := time.Parse(time.RFC3339, wakeAt)
	if !got.Equal(want) {
		t.Errorf("SnoozedUntil = %v, want %v", got, want)
	}
}

func TestEmailSet_ClearsSnoozedAtomically(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: clear\r\n\r\nbody"
	m := f.insertMessage(t, body, "clear", "a@example.test", "b@example.test", nil, "")
	t1 := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := f.srv.Store.Meta().SetSnooze(context.Background(), m.ID, f.inbox.ID, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			fmt.Sprintf("%d", m.ID): map[string]any{
				"snoozedUntil": nil,
			},
		},
	})
	var resp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("notUpdated = %v", resp.NotUpdated)
	}
	if messageHasSnoozeKeyword(t, f, m.ID) {
		t.Errorf("$snoozed keyword still set after clear")
	}
	if got := messageSnoozedUntil(t, f, m.ID); got != nil {
		t.Errorf("SnoozedUntil = %v, want nil", got)
	}
}

func TestEmailSet_RejectsKeywordOnlyWithoutDate(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: rej\r\n\r\nbody"
	m := f.insertMessage(t, body, "rej", "a@example.test", "b@example.test", nil, "")
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			fmt.Sprintf("%d", m.ID): map[string]any{
				"keywords/$snoozed": true,
			},
		},
	})
	var resp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	key := fmt.Sprintf("%d", m.ID)
	if _, ok := resp.NotUpdated[key]; !ok {
		t.Fatalf("expected NotUpdated entry for %s, got: %s", key, raw)
	}
	if got, _ := resp.NotUpdated[key]["type"].(string); got != "invalidProperties" {
		t.Errorf("error type = %q, want invalidProperties", got)
	}
	// And no side-effects: keyword + column unchanged.
	if messageHasSnoozeKeyword(t, f, m.ID) {
		t.Errorf("$snoozed keyword set despite rejection")
	}
}

func TestEmailSet_ClearKeywordAlsoClearsDate(t *testing.T) {
	f := setupFixture(t)
	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: kc\r\n\r\nbody"
	m := f.insertMessage(t, body, "kc", "a@example.test", "b@example.test", nil, "")
	t1 := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := f.srv.Store.Meta().SetSnooze(context.Background(), m.ID, f.inbox.ID, &t1); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update": map[string]any{
			fmt.Sprintf("%d", m.ID): map[string]any{
				"keywords/$snoozed": false,
			},
		},
	})
	var resp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("notUpdated = %v", resp.NotUpdated)
	}
	if messageHasSnoozeKeyword(t, f, m.ID) {
		t.Errorf("$snoozed keyword still present after clear")
	}
	if got := messageSnoozedUntil(t, f, m.ID); got != nil {
		t.Errorf("SnoozedUntil = %v, want nil after keyword clear", got)
	}
}

// TestEmail_Thread_IngestTimeResolution verifies that when message B is
// imported with an In-Reply-To header pointing at message A's Message-ID,
// Thread/get for B's threadId returns a thread containing both A and B.
//
// This is the end-to-end acceptance test for the ingest-time thread
// resolution introduced alongside migration 0022.
func TestEmail_Thread_IngestTimeResolution(t *testing.T) {
	f := setupFixture(t)
	accountID := protojmap.AccountIDForPrincipal(f.pid)

	// Ingest message A: a standalone original message.
	bodyA := "From: alice@example.test\r\nTo: bob@example.test\r\n" +
		"Subject: Hello\r\nMessage-ID: <thread-test-a@example.test>\r\n\r\nHello"
	refA := f.putBlob(t, bodyA)
	_, rawA := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"a": map[string]any{
				"blobId":     refA.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var importA struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(rawA, &importA); err != nil {
		t.Fatalf("unmarshal Email/import A: %v %s", err, rawA)
	}
	emailIDA := importA.Created["a"].ID
	if emailIDA == "" {
		t.Fatalf("Email/import A: no id in created: %s", rawA)
	}

	// Ingest message B: a reply to A.
	bodyB := "From: bob@example.test\r\nTo: alice@example.test\r\n" +
		"Subject: Re: Hello\r\nMessage-ID: <thread-test-b@example.test>\r\n" +
		"In-Reply-To: <thread-test-a@example.test>\r\n\r\nHi back"
	refB := f.putBlob(t, bodyB)
	_, rawB := f.invoke(t, "Email/import", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"emails": map[string]any{
			"b": map[string]any{
				"blobId":     refB.Hash,
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
			},
		},
	})
	var importB struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(rawB, &importB); err != nil {
		t.Fatalf("unmarshal Email/import B: %v %s", err, rawB)
	}
	emailIDB := importB.Created["b"].ID
	if emailIDB == "" {
		t.Fatalf("Email/import B: no id in created: %s", rawB)
	}

	// Fetch B's threadId via Email/get.
	_, rawGet := f.invoke(t, "Email/get", map[string]any{
		"accountId":  accountID,
		"ids":        []string{emailIDB},
		"properties": []string{"threadId"},
	})
	var getResp struct {
		List []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"list"`
	}
	if err := json.Unmarshal(rawGet, &getResp); err != nil {
		t.Fatalf("unmarshal Email/get: %v %s", err, rawGet)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("Email/get list len = %d, want 1: %s", len(getResp.List), rawGet)
	}
	threadID := getResp.List[0].ThreadID
	if threadID == "" {
		t.Fatalf("Email/get: B has empty threadId: %s", rawGet)
	}

	// Thread/get with B's threadId must return a thread containing both
	// A and B.
	_, rawThread := f.invoke(t, "Thread/get", map[string]any{
		"accountId": accountID,
		"ids":       []string{threadID},
	})
	var threadResp struct {
		List []struct {
			ID       string   `json:"id"`
			EmailIDs []string `json:"emailIds"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(rawThread, &threadResp); err != nil {
		t.Fatalf("unmarshal Thread/get: %v %s", err, rawThread)
	}
	if len(threadResp.NotFound) > 0 {
		t.Fatalf("Thread/get: notFound = %v for threadId %q", threadResp.NotFound, threadID)
	}
	if len(threadResp.List) != 1 {
		t.Fatalf("Thread/get: list len = %d, want 1: %s", len(threadResp.List), rawThread)
	}
	emailIDs := threadResp.List[0].EmailIDs
	if len(emailIDs) != 2 {
		t.Fatalf("Thread/get: emailIds = %v, want [A, B] (2 emails): %s",
			emailIDs, rawThread)
	}
	hasA, hasB := false, false
	for _, id := range emailIDs {
		if id == emailIDA {
			hasA = true
		}
		if id == emailIDB {
			hasB = true
		}
	}
	if !hasA || !hasB {
		t.Fatalf("Thread/get: emailIds = %v, want both A=%q and B=%q",
			emailIDs, emailIDA, emailIDB)
	}
}

// -- RFC 8621 §4.6 inline bodyValues creation path ----------------------

// setCreateFromBodyValues is a helper that fires Email/set with the
// inline-body payload and returns the raw set response.
func setCreateFromBodyValues(t *testing.T, f *fixture, payload map[string]any) (created map[string]map[string]any, notCreated map[string]map[string]any) {
	t.Helper()
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"create": map[string]any{
			"draft": payload,
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Email/set response: %v: %s", err, raw)
	}
	return resp.Created, resp.NotCreated
}

// getBodyValues retrieves bodyValues for a message via Email/get.
func getBodyValues(t *testing.T, f *fixture, emailID string) (map[string]any, map[string]any) {
	t.Helper()
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{emailID},
		"fetchTextBodyValues": true,
		"fetchHTMLBodyValues": true,
		"maxBodyValueBytes":   64 * 1024,
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal Email/get response: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("Email/get: got %d entries, want 1: %s", len(resp.List), raw)
	}
	bv, _ := resp.List[0]["bodyValues"].(map[string]any)
	tb, _ := resp.List[0]["textBody"].([]any)
	// Build a map of partId -> bodyValue from the flat bodyValues map,
	// filtered to the partIds listed in textBody.
	textPartIDs := map[string]struct{}{}
	for _, x := range tb {
		if m, ok := x.(map[string]any); ok {
			if pid, ok := m["partId"].(string); ok {
				textPartIDs[pid] = struct{}{}
			}
		}
	}
	textBV := map[string]any{}
	htmlBV := map[string]any{}
	for k, v := range bv {
		if _, ok := textPartIDs[k]; ok {
			textBV[k] = v
		} else {
			htmlBV[k] = v
		}
	}
	return textBV, htmlBV
}

func TestEmailSet_Create_FromBodyValues_TextOnly(t *testing.T) {
	f := setupFixture(t)

	created, notCreated := setCreateFromBodyValues(t, f, map[string]any{
		"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
		"keywords":   map[string]bool{"$draft": true, "$seen": true},
		"from":       []map[string]any{{"name": "Alice", "email": "alice@example.test"}},
		"to":         []map[string]any{{"name": "Bob", "email": "bob@example.test"}},
		"subject":    "Text-only draft",
		"bodyValues": map[string]any{
			"1": map[string]any{"value": "Hello Bob, this is a plain-text draft.", "isTruncated": false, "isEncodingProblem": false},
		},
		"textBody": []map[string]any{
			{"partId": "1", "type": "text/plain", "charset": "utf-8"},
		},
	})

	if len(notCreated) != 0 {
		t.Fatalf("notCreated = %v", notCreated)
	}
	if len(created) != 1 {
		t.Fatalf("created = %v", created)
	}
	emailID, _ := created["draft"]["id"].(string)
	if emailID == "" {
		t.Fatalf("no id in created: %v", created)
	}

	// Confirm body round-trips.
	textBV, _ := getBodyValues(t, f, emailID)
	found := false
	for _, v := range textBV {
		bv := v.(map[string]any)
		if strings.Contains(bv["value"].(string), "plain-text draft") {
			found = true
		}
	}
	if !found {
		t.Errorf("text body value not found in Email/get response; textBV=%v", textBV)
	}
}

func TestEmailSet_Create_FromBodyValues_HtmlOnly(t *testing.T) {
	f := setupFixture(t)

	created, notCreated := setCreateFromBodyValues(t, f, map[string]any{
		"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
		"keywords":   map[string]bool{"$draft": true},
		"from":       []map[string]any{{"name": "Alice", "email": "alice@example.test"}},
		"to":         []map[string]any{{"name": "Bob", "email": "bob@example.test"}},
		"subject":    "HTML draft",
		"bodyValues": map[string]any{
			"2": map[string]any{"value": "<p>Hello from HTML</p>", "isTruncated": false, "isEncodingProblem": false},
		},
		"htmlBody": []map[string]any{
			{"partId": "2", "type": "text/html", "charset": "utf-8"},
		},
	})

	if len(notCreated) != 0 {
		t.Fatalf("notCreated = %v", notCreated)
	}
	if len(created) != 1 {
		t.Fatalf("created = %v", created)
	}
	emailID, _ := created["draft"]["id"].(string)
	if emailID == "" {
		t.Fatalf("no id in created: %v", created)
	}

	// Email/get with fetchHTMLBodyValues should return the HTML content.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{emailID},
		"fetchHTMLBodyValues": true,
		"maxBodyValueBytes":   64 * 1024,
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	bv, _ := getResp.List[0]["bodyValues"].(map[string]any)
	found := false
	for _, v := range bv {
		if m, ok := v.(map[string]any); ok {
			if s, ok := m["value"].(string); ok && strings.Contains(s, "HTML") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("HTML body value not found; bodyValues=%v", bv)
	}
}

func TestEmailSet_Create_FromBodyValues_Multipart(t *testing.T) {
	f := setupFixture(t)
	textContent := "This is the plain-text part."
	htmlContent := "<p>This is the HTML part.</p>"

	created, notCreated := setCreateFromBodyValues(t, f, map[string]any{
		"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
		"keywords":   map[string]bool{"$draft": true, "$seen": true},
		"from":       []map[string]any{{"name": "Alice", "email": "alice@example.test"}},
		"to":         []map[string]any{{"name": "Bob", "email": "bob@example.test"}},
		"subject":    "Multipart draft",
		"bodyValues": map[string]any{
			"1": map[string]any{"value": textContent, "isTruncated": false, "isEncodingProblem": false},
			"2": map[string]any{"value": htmlContent, "isTruncated": false, "isEncodingProblem": false},
		},
		"textBody": []map[string]any{
			{"partId": "1", "type": "text/plain", "charset": "utf-8"},
		},
		"htmlBody": []map[string]any{
			{"partId": "2", "type": "text/html", "charset": "utf-8"},
		},
	})

	if len(notCreated) != 0 {
		t.Fatalf("notCreated = %v", notCreated)
	}
	if len(created) != 1 {
		t.Fatalf("created = %v", created)
	}
	emailID, _ := created["draft"]["id"].(string)
	if emailID == "" {
		t.Fatalf("no id in created: %v", created)
	}

	// Fetch with both text and html body values.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":           protojmap.AccountIDForPrincipal(f.pid),
		"ids":                 []string{emailID},
		"fetchTextBodyValues": true,
		"fetchHTMLBodyValues": true,
		"maxBodyValueBytes":   64 * 1024,
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	item := getResp.List[0]
	bv, _ := item["bodyValues"].(map[string]any)
	if len(bv) < 2 {
		t.Fatalf("expected at least 2 body values (text + html), got %d: %s", len(bv), raw)
	}
	foundText, foundHTML := false, false
	for _, v := range bv {
		if m, ok := v.(map[string]any); ok {
			s, _ := m["value"].(string)
			if strings.Contains(s, "plain-text part") {
				foundText = true
			}
			if strings.Contains(s, "HTML part") {
				foundHTML = true
			}
		}
	}
	if !foundText {
		t.Errorf("text part not found in bodyValues: %v", bv)
	}
	if !foundHTML {
		t.Errorf("HTML part not found in bodyValues: %v", bv)
	}
}

func TestEmailSet_Create_FromBodyValues_WithReplyHeaders(t *testing.T) {
	f := setupFixture(t)

	originalMsgID := "<original-1234@example.test>"
	refMsgID := "<ref-5678@example.test>"

	created, notCreated := setCreateFromBodyValues(t, f, map[string]any{
		"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
		"keywords":   map[string]bool{"$draft": true},
		"from":       []map[string]any{{"name": "Bob", "email": "bob@example.test"}},
		"to":         []map[string]any{{"name": "Alice", "email": "alice@example.test"}},
		"subject":    "Re: Original",
		"inReplyTo":  []string{originalMsgID},
		"references": []string{refMsgID, originalMsgID},
		"bodyValues": map[string]any{
			"1": map[string]any{"value": "Thanks for the original message.", "isTruncated": false, "isEncodingProblem": false},
		},
		"textBody": []map[string]any{
			{"partId": "1", "type": "text/plain", "charset": "utf-8"},
		},
	})

	if len(notCreated) != 0 {
		t.Fatalf("notCreated = %v", notCreated)
	}
	if len(created) != 1 {
		t.Fatalf("created = %v", created)
	}
	emailID, _ := created["draft"]["id"].(string)
	if emailID == "" {
		t.Fatalf("no id in created: %v", created)
	}

	// Fetch the message and verify the envelope carries the reply headers.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{emailID},
		"properties": []string{"inReplyTo", "references", "subject"},
	})
	var getResp struct {
		List []struct {
			InReplyTo  []string `json:"inReplyTo"`
			References []string `json:"references"`
			Subject    string   `json:"subject"`
		} `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("got %d entries, want 1: %s", len(getResp.List), raw)
	}
	got := getResp.List[0]
	if len(got.InReplyTo) == 0 {
		t.Errorf("inReplyTo empty in Email/get response")
	} else if got.InReplyTo[0] != originalMsgID {
		t.Errorf("inReplyTo[0] = %q, want %q", got.InReplyTo[0], originalMsgID)
	}
	if got.Subject != "Re: Original" {
		t.Errorf("subject = %q, want Re: Original", got.Subject)
	}
}

func TestEmailSet_Create_NoBlobAndNoBody(t *testing.T) {
	f := setupFixture(t)

	created, notCreated := setCreateFromBodyValues(t, f, map[string]any{
		"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
		"keywords":   map[string]bool{"$draft": true},
		// No blobId, no bodyValues, no textBody, no htmlBody.
	})

	if len(created) != 0 {
		t.Fatalf("expected no created entry, got %v", created)
	}
	if len(notCreated) != 1 {
		t.Fatalf("expected 1 notCreated entry, got %v", notCreated)
	}
	entry := notCreated["draft"]
	if errType, _ := entry["type"].(string); errType != "invalidProperties" {
		t.Errorf("error type = %q, want invalidProperties", errType)
	}
	desc, _ := entry["description"].(string)
	if !strings.Contains(desc, "blobId") {
		t.Errorf("error description %q does not mention blobId", desc)
	}
}
