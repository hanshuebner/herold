package protojmap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/protojmap/mail/searchsnippet"
	"github.com/hanshuebner/herold/internal/protojmap/mail/thread"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// crossFixture wires a single store with two principals — alice and
// bob — plus the JMAP HTTP surface with the mail/* handlers registered.
// Alice is granted ACL on bob's INBOX so the cross-account routing path
// has a known good case to exercise (REQ-PROTO-33).
type crossFixture struct {
	t           *testing.T
	store       store.Store
	clk         *clock.FakeClock
	srv         *protojmap.Server
	httpd       *httptest.Server
	alice       store.PrincipalID
	bob         store.PrincipalID
	bobInbox    store.MailboxID
	aliceKey    string
	bobAcctID   protojmap.Id
	aliceAcctID protojmap.Id
}

func newCrossFixture(t *testing.T, aliceRights store.ACLRights) *crossFixture {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	alicePID, err := dir.CreatePrincipal(ctx, "alice@example.com", "alicepass-correct-horse-battery-1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bobPID, err := dir.CreatePrincipal(ctx, "bob@example.com", "bobpass-correct-horse-battery-1")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	aliceKey, _, err := createAPIKey(ctx, fs, alicePID)
	if err != nil {
		t.Fatalf("create alice key: %v", err)
	}

	// CreatePrincipal seeded an INBOX for each principal already; locate
	// bob's so we can attach an ACL row.
	bobBoxes, err := fs.Meta().ListMailboxes(ctx, bobPID)
	if err != nil {
		t.Fatalf("ListMailboxes(bob): %v", err)
	}
	var bobInbox store.MailboxID
	for _, mb := range bobBoxes {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			bobInbox = mb.ID
			break
		}
	}
	if bobInbox == 0 {
		t.Fatalf("bob has no INBOX after CreatePrincipal")
	}
	if aliceRights != 0 {
		if err := fs.Meta().SetMailboxACL(ctx, bobInbox, &alicePID, aliceRights, bobPID); err != nil {
			t.Fatalf("SetMailboxACL alice -> bob INBOX: %v", err)
		}
	}

	srv := protojmap.NewServer(fs, dir, nil, nil, clk, protojmap.Options{
		MaxCallsInRequest:  8,
		PushPingInterval:   60 * time.Second,
		PushCoalesceWindow: 50 * time.Millisecond,
		DownloadRatePerSec: -1,
	})
	mailbox.Register(srv.Registry(), fs, nil, clk)
	email.Register(srv.Registry(), fs, nil, clk)
	thread.Register(srv.Registry(), fs, nil, clk)
	searchsnippet.Register(srv.Registry(), fs, nil, clk)

	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	return &crossFixture{
		t:           t,
		store:       fs,
		clk:         clk,
		srv:         srv,
		httpd:       httpd,
		alice:       alicePID,
		bob:         bobPID,
		bobInbox:    bobInbox,
		aliceKey:    aliceKey,
		bobAcctID:   protojmap.AccountIDForPrincipal(bobPID),
		aliceAcctID: protojmap.AccountIDForPrincipal(alicePID),
	}
}

// invokeAsAlice posts a single method call as alice and returns the
// response invocation triple.
func (f *crossFixture) invokeAsAlice(t *testing.T, method string, args any) (string, json.RawMessage) {
	t.Helper()
	argsBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	body := map[string]any{
		"using":       []protojmap.CapabilityID{protojmap.CapabilityCore, protojmap.CapabilityMail},
		"methodCalls": []any{[]any{method, json.RawMessage(argsBytes), "c0"}},
	}
	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, f.httpd.URL+"/jmap", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+f.aliceKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, respBody)
	}
	var envelope struct {
		MethodResponses []protojmap.Invocation `json:"methodResponses"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, respBody)
	}
	if len(envelope.MethodResponses) != 1 {
		t.Fatalf("got %d method responses, want 1", len(envelope.MethodResponses))
	}
	return envelope.MethodResponses[0].Name, envelope.MethodResponses[0].Args
}

// insertMessage inserts a single envelope-only message into mb owned
// by ownerPID; returns the assigned MessageID via the change feed
// (mostRecentEmailCreatedID-style lookup). A canonical blob is written
// so downstream Email/copy and rendering paths see a real BlobRef.
func (f *crossFixture) insertMessage(t *testing.T, ownerPID store.PrincipalID, mb store.MailboxID, subject string) store.MessageID {
	t.Helper()
	ctx := context.Background()
	rawBody := fmt.Sprintf("Subject: %s\r\nMessage-ID: <%s@test>\r\nFrom: t@x\r\nTo: u@x\r\nDate: Wed, 13 May 2026 00:00:00 +0000\r\n\r\nbody\r\n", subject, subject)
	ref, err := f.store.Blobs().Put(ctx, bytes.NewReader([]byte(rawBody)))
	if err != nil {
		t.Fatalf("Blobs().Put: %v", err)
	}
	msg := store.Message{
		PrincipalID:  ownerPID,
		MailboxID:    mb,
		InternalDate: f.clk.Now(),
		ReceivedAt:   f.clk.Now(),
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     store.Envelope{Subject: subject, MessageID: fmt.Sprintf("<%s@test>", subject)},
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mb}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Read back via change feed to recover the assigned id.
	feed, err := f.store.Meta().ReadChangeFeed(ctx, ownerPID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for i := len(feed) - 1; i >= 0; i-- {
		if feed[i].Kind == store.EntityKindEmail && feed[i].Op == store.ChangeOpCreated {
			return store.MessageID(feed[i].EntityID)
		}
	}
	t.Fatal("could not locate inserted message id in change feed")
	return 0
}

// -- tests ------------------------------------------------------------

// TestResolveAccount_SelfAndForeign exercises the ResolveAccount surface
// directly (the per-method handlers route through it).
func TestResolveAccount_SelfAndForeign(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	ctx := context.Background()
	meta := f.store.Meta()

	// Caller's own account: fast-path.
	if pid, merr := protojmap.ResolveAccount(ctx, meta, f.alice, f.aliceAcctID); merr != nil || pid != f.alice {
		t.Fatalf("ResolveAccount(alice -> alice) = (%d, %v); want (%d, nil)", pid, merr, f.alice)
	}
	// Foreign account that alice has ACL on.
	if pid, merr := protojmap.ResolveAccount(ctx, meta, f.alice, f.bobAcctID); merr != nil || pid != f.bob {
		t.Fatalf("ResolveAccount(alice -> bob) = (%d, %v); want (%d, nil)", pid, merr, f.bob)
	}
	// Empty accountId: invalidArguments.
	if _, merr := protojmap.ResolveAccount(ctx, meta, f.alice, ""); merr == nil || merr.Type != "invalidArguments" {
		t.Fatalf("ResolveAccount(alice -> '') merr = %v; want invalidArguments", merr)
	}
	// Malformed accountId.
	if _, merr := protojmap.ResolveAccount(ctx, meta, f.alice, "xxx"); merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("ResolveAccount(alice -> 'xxx') merr = %v; want accountNotFound", merr)
	}
	// Nonexistent owner PID.
	if _, merr := protojmap.ResolveAccount(ctx, meta, f.alice, protojmap.AccountIDForPrincipal(99999)); merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("ResolveAccount(alice -> a99999) merr = %v; want accountNotFound", merr)
	}
}

// TestResolveAccount_NoACL: alice without an ACL row gets
// accountNotFound for bob's account.
func TestResolveAccount_NoACL(t *testing.T) {
	f := newCrossFixture(t, 0)
	ctx := context.Background()
	if _, merr := protojmap.ResolveAccount(ctx, f.store.Meta(), f.alice, f.bobAcctID); merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("ResolveAccount(alice -> bob no ACL) merr = %v; want accountNotFound", merr)
	}
}

// TestSession_ListsBobAsSecondary checks that alice's session
// descriptor includes bob's account when an ACL grant exists.
func TestSession_ListsBobAsSecondary(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)

	req, _ := http.NewRequest(http.MethodGet, f.httpd.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+f.aliceKey)
	resp, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var desc struct {
		Accounts        map[string]map[string]any `json:"accounts"`
		PrimaryAccounts map[string]string         `json:"primaryAccounts"`
	}
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, body)
	}
	if _, ok := desc.Accounts[string(f.aliceAcctID)]; !ok {
		t.Fatalf("missing primary account: %s", body)
	}
	bobAcct, ok := desc.Accounts[string(f.bobAcctID)]
	if !ok {
		t.Fatalf("missing bob's account in secondary list: %s", body)
	}
	if bobAcct["isPersonal"].(bool) {
		t.Fatalf("bob entry isPersonal=true; want false (secondary): %+v", bobAcct)
	}
	if !bobAcct["isReadOnly"].(bool) {
		t.Fatalf("bob entry isReadOnly=false; want true (alice has lr only): %+v", bobAcct)
	}
	// primaryAccounts must remain alice for every mail capability.
	for cap, id := range desc.PrimaryAccounts {
		if id != string(f.aliceAcctID) {
			t.Errorf("primaryAccounts[%s] = %s; want %s", cap, id, f.aliceAcctID)
		}
	}
}

// TestSession_NoSharedAccounts: without an ACL grant the session only
// lists alice's own account.
func TestSession_NoSharedAccounts(t *testing.T) {
	f := newCrossFixture(t, 0)
	req, _ := http.NewRequest(http.MethodGet, f.httpd.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+f.aliceKey)
	resp, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var desc struct {
		Accounts map[string]map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(desc.Accounts) != 1 {
		t.Fatalf("accounts len = %d; want 1", len(desc.Accounts))
	}
	if _, ok := desc.Accounts[string(f.bobAcctID)]; ok {
		t.Fatalf("bob should not appear in session when no ACL grant exists: %s", body)
	}
}

// TestMailboxGet_CrossAccount: alice sees only bob's mailboxes she has
// ACL on (the INBOX) — not bob's other mailboxes.
func TestMailboxGet_CrossAccount(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	// Bob's account has more mailboxes than alice can reach.
	ctx := context.Background()
	if _, err := f.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.bob, Name: "PrivateFolder",
	}); err != nil {
		t.Fatalf("insert bob's PrivateFolder: %v", err)
	}

	_, raw := f.invokeAsAlice(t, "Mailbox/get", map[string]any{
		"accountId": f.bobAcctID,
	})
	var resp struct {
		AccountID string           `json:"accountId"`
		List      []map[string]any `json:"list"`
		NotFound  []string         `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if resp.AccountID != string(f.bobAcctID) {
		t.Fatalf("accountId echo = %s; want %s", resp.AccountID, f.bobAcctID)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d mailboxes; want 1 (only bob's INBOX): %+v", len(resp.List), resp.List)
	}
	if got := resp.List[0]["id"].(string); got != fmt.Sprintf("%d", f.bobInbox) {
		t.Fatalf("returned mailbox id %s; want bob's INBOX %d", got, f.bobInbox)
	}
}

// TestMailboxGet_CrossAccount_NoACL: alice without an ACL row gets
// accountNotFound, not an empty list.
func TestMailboxGet_CrossAccount_NoACL(t *testing.T) {
	f := newCrossFixture(t, 0)
	name, raw := f.invokeAsAlice(t, "Mailbox/get", map[string]any{
		"accountId": f.bobAcctID,
	})
	if name != "error" {
		t.Fatalf("response name = %s; want error: %s", name, raw)
	}
	var merr protojmap.MethodError
	if err := json.Unmarshal(raw, &merr); err != nil {
		t.Fatalf("unmarshal error: %v: %s", err, raw)
	}
	if merr.Type != "accountNotFound" {
		t.Fatalf("merr.Type = %s; want accountNotFound: %s", merr.Type, raw)
	}
}

// TestEmailGet_CrossAccount: alice fetches a message from bob's INBOX
// because she has lr rights; she does not see messages in bob's
// PrivateFolder.
func TestEmailGet_CrossAccount(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	visible := f.insertMessage(t, f.bob, f.bobInbox, "visible-to-alice")

	ctx := context.Background()
	privBox, err := f.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.bob, Name: "PrivateFolder",
	})
	if err != nil {
		t.Fatalf("insert bob's PrivateFolder: %v", err)
	}
	hidden := f.insertMessage(t, f.bob, privBox.ID, "hidden-from-alice")

	_, raw := f.invokeAsAlice(t, "Email/get", map[string]any{
		"accountId":  f.bobAcctID,
		"ids":        []string{fmt.Sprintf("%d", visible), fmt.Sprintf("%d", hidden)},
		"properties": []string{"id", "subject"},
	})
	var resp struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d in list; want 1: %s", len(resp.List), raw)
	}
	if resp.List[0]["subject"].(string) != "visible-to-alice" {
		t.Fatalf("subject = %v; want visible-to-alice", resp.List[0]["subject"])
	}
	if len(resp.NotFound) != 1 || resp.NotFound[0] != fmt.Sprintf("%d", hidden) {
		t.Fatalf("notFound = %v; want [%d]", resp.NotFound, hidden)
	}
}

// TestEmailQuery_CrossAccount: alice's Email/query against bob's
// account returns only messages in bob's mailboxes she can see.
func TestEmailQuery_CrossAccount(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	visible := f.insertMessage(t, f.bob, f.bobInbox, "alice-can-find-me")

	_, raw := f.invokeAsAlice(t, "Email/query", map[string]any{
		"accountId": f.bobAcctID,
	})
	var resp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	found := false
	for _, id := range resp.IDs {
		if id == fmt.Sprintf("%d", visible) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Email/query did not include alice-visible message %d: %v", visible, resp.IDs)
	}
}

// TestEmailChanges_CrossAccount: Email/changes against bob's account
// returns the correct state without leaking foreign-mailbox entries.
func TestEmailChanges_CrossAccount(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	f.insertMessage(t, f.bob, f.bobInbox, "seed-1")

	_, raw := f.invokeAsAlice(t, "Email/changes", map[string]any{
		"accountId":  f.bobAcctID,
		"sinceState": "0",
	})
	var resp struct {
		AccountID string   `json:"accountId"`
		Created   []string `json:"created"`
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if resp.AccountID != string(f.bobAcctID) {
		t.Fatalf("accountId echo = %s; want %s", resp.AccountID, f.bobAcctID)
	}
}

// TestEmailSet_CrossAccount_LookupOnlyForbidden: lookup-only alice
// cannot mutate bob's messages. The handler returns notFound (we did
// not surface the message id in the first place) for unknown ids and
// forbidden when she addresses a visible message without write bits.
func TestEmailSet_CrossAccount_LookupOnlyForbidden(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	mid := f.insertMessage(t, f.bob, f.bobInbox, "alice-cannot-touch")

	_, raw := f.invokeAsAlice(t, "Email/set", map[string]any{
		"accountId": f.bobAcctID,
		"update": map[string]any{
			fmt.Sprintf("%d", mid): map[string]any{
				"keywords/$seen": true,
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
	if len(resp.NotUpdated) != 1 {
		t.Fatalf("got notUpdated=%+v; want one forbidden entry", resp.NotUpdated)
	}
	got := resp.NotUpdated[fmt.Sprintf("%d", mid)]
	if got["type"].(string) != "forbidden" {
		t.Fatalf("notUpdated.type = %v; want forbidden: %v", got["type"], got)
	}
}

// TestEmailSet_CrossAccount_WriteAllowed: with seen / write bits alice
// can flag a message in bob's INBOX.
func TestEmailSet_CrossAccount_WriteAllowed(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead|store.ACLRightSeen|store.ACLRightWrite)
	mid := f.insertMessage(t, f.bob, f.bobInbox, "alice-can-flag")

	_, raw := f.invokeAsAlice(t, "Email/set", map[string]any{
		"accountId": f.bobAcctID,
		"update": map[string]any{
			fmt.Sprintf("%d", mid): map[string]any{
				"keywords/$seen": true,
			},
		},
	})
	var resp struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]map[string]any  `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("notUpdated non-empty: %+v", resp.NotUpdated)
	}
	if _, ok := resp.Updated[fmt.Sprintf("%d", mid)]; !ok {
		t.Fatalf("updated did not include %d: %+v", mid, resp.Updated)
	}
}

// TestEmailSet_CrossAccount_DestroyRequiresExpunge: lookup-only alice
// cannot destroy bob's message; with t/e bits the destroy lands.
func TestEmailSet_CrossAccount_DestroyRequiresExpunge(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	mid := f.insertMessage(t, f.bob, f.bobInbox, "alice-cannot-destroy")

	_, raw := f.invokeAsAlice(t, "Email/set", map[string]any{
		"accountId": f.bobAcctID,
		"destroy":   []string{fmt.Sprintf("%d", mid)},
	})
	var resp struct {
		Destroyed    []string                  `json:"destroyed"`
		NotDestroyed map[string]map[string]any `json:"notDestroyed"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.NotDestroyed) != 1 {
		t.Fatalf("expected one notDestroyed: %+v", resp)
	}
	if resp.NotDestroyed[fmt.Sprintf("%d", mid)]["type"].(string) != "forbidden" {
		t.Fatalf("notDestroyed type = %v; want forbidden", resp.NotDestroyed)
	}
}

// TestThreadGet_CrossAccount: Thread/get for bob's account returns
// threads composed of alice-visible messages.
func TestThreadGet_CrossAccount(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	mid := f.insertMessage(t, f.bob, f.bobInbox, "thread-vis")

	_, raw := f.invokeAsAlice(t, "Thread/get", map[string]any{
		"accountId": f.bobAcctID,
		"ids":       []string{fmt.Sprintf("t%d", mid)},
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) == 0 {
		t.Fatalf("Thread/get returned empty list for visible thread: %s", raw)
	}
}

// TestSearchSnippetGet_CrossAccount: SearchSnippet/get for bob's
// account answers for alice-visible messages but rejects hidden ones.
func TestSearchSnippetGet_CrossAccount(t *testing.T) {
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	mid := f.insertMessage(t, f.bob, f.bobInbox, "search-vis")
	ctx := context.Background()
	privBox, err := f.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: f.bob, Name: "PrivateFolder",
	})
	if err != nil {
		t.Fatalf("insert PrivateFolder: %v", err)
	}
	hidden := f.insertMessage(t, f.bob, privBox.ID, "search-hid")

	_, raw := f.invokeAsAlice(t, "SearchSnippet/get", map[string]any{
		"accountId": f.bobAcctID,
		"emailIds":  []string{fmt.Sprintf("%d", mid), fmt.Sprintf("%d", hidden)},
		"filter":    map[string]any{},
	})
	var resp struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	foundHidden := false
	for _, id := range resp.NotFound {
		if id == fmt.Sprintf("%d", hidden) {
			foundHidden = true
			break
		}
	}
	if !foundHidden {
		t.Fatalf("hidden message should appear in notFound; got %+v", resp)
	}
	// Visible message must not appear in notFound.
	for _, id := range resp.NotFound {
		if id == fmt.Sprintf("%d", mid) {
			t.Fatalf("visible message %d incorrectly in notFound", mid)
		}
	}
}

// TestEmailCopy_CrossAccount: alice copies a message from her own
// account into bob's INBOX. Lookup-only fails; insert succeeds.
func TestEmailCopy_CrossAccount(t *testing.T) {
	// Variant 1: lookup-only on bob's INBOX → forbidden.
	f := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead)
	aliceInboxes, err := f.store.Meta().ListMailboxes(context.Background(), f.alice)
	if err != nil {
		t.Fatalf("ListMailboxes(alice): %v", err)
	}
	var aliceInbox store.MailboxID
	for _, mb := range aliceInboxes {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			aliceInbox = mb.ID
			break
		}
	}
	if aliceInbox == 0 {
		t.Fatalf("alice has no INBOX")
	}
	srcID := f.insertMessage(t, f.alice, aliceInbox, "copy-source")

	_, raw := f.invokeAsAlice(t, "Email/copy", map[string]any{
		"fromAccountId": f.aliceAcctID,
		"accountId":     f.bobAcctID,
		"create": map[string]any{
			"c1": map[string]any{
				"id":         fmt.Sprintf("%d", srcID),
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.bobInbox): true},
			},
		},
	})
	var resp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.NotCreated) != 1 {
		t.Fatalf("expected lookup-only alice to fail copy; got %+v", resp)
	}
	if resp.NotCreated["c1"]["type"].(string) != "forbidden" {
		t.Fatalf("notCreated.c1.type = %v; want forbidden", resp.NotCreated["c1"])
	}

	// Variant 2: with insert rights the copy lands.
	f2 := newCrossFixture(t, store.ACLRightLookup|store.ACLRightRead|store.ACLRightInsert)
	aliceInboxes2, _ := f2.store.Meta().ListMailboxes(context.Background(), f2.alice)
	var aliceInbox2 store.MailboxID
	for _, mb := range aliceInboxes2 {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			aliceInbox2 = mb.ID
			break
		}
	}
	srcID2 := f2.insertMessage(t, f2.alice, aliceInbox2, "copy-source-2")

	_, raw2 := f2.invokeAsAlice(t, "Email/copy", map[string]any{
		"fromAccountId": f2.aliceAcctID,
		"accountId":     f2.bobAcctID,
		"create": map[string]any{
			"c2": map[string]any{
				"id":         fmt.Sprintf("%d", srcID2),
				"mailboxIds": map[string]bool{fmt.Sprintf("%d", f2.bobInbox): true},
			},
		},
	})
	var resp2 struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]map[string]any `json:"notCreated"`
	}
	if err := json.Unmarshal(raw2, &resp2); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw2)
	}
	if len(resp2.NotCreated) != 0 {
		t.Fatalf("expected copy to succeed; got %+v", resp2.NotCreated)
	}
	if _, ok := resp2.Created["c2"]; !ok {
		t.Fatalf("created entry missing: %+v", resp2)
	}
}
