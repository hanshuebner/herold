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
		MailboxID:    f.inbox.ID,
		InternalDate: now,
		ReceivedAt:   now,
		Size:         ref.Size,
		Blob:         ref,
		Keywords:     kw,
		Envelope: store.Envelope{
			Subject: subject,
			From:    from,
			To:      to,
			Date:    now,
		},
	}
	uid, modseq, err := f.srv.Store.Meta().InsertMessage(context.Background(), msg)
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
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: invoice\r\n\r\nplease pay",
		"invoice", "a@example.test", "b@example.test", nil, "subject invoice please pay")
	matchID := mostRecentMessageID(t, f)
	f.insertMessage(t,
		"From: a@example.test\r\nTo: b@example.test\r\nSubject: kittens\r\n\r\ncute pictures",
		"kittens", "a@example.test", "b@example.test", nil, "subject kittens cute pictures")

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
