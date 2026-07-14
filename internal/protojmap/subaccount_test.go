package protojmap_test

// Sub-account substrate: the JMAP half (issue #227, REQ-SUBACCT-03,
// REQ-SUBACCT-04, REQ-SUBACCT-11). The store half (ownership, auth
// exclusion, quota attribution) landed separately and is exercised by
// internal/store/storetest/storetest_subaccounts.go; these tests drive
// the real JMAP method handlers over HTTP to demonstrate:
//
//   - the session descriptor advertises a sub-account with its own
//     accountCapabilities (REQ-SUBACCT-03) and the sub-accounts
//     capability is present (REQ-SUBACCT-11);
//   - a query scoped to the parent account never returns the
//     sub-account's mail, and vice versa (REQ-SUBACCT-04);
//   - the two accounts' state strings advance independently
//     (REQ-SUBACCT-03/04);
//   - a principal with no relationship to the sub-account cannot
//     resolve it at all.

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
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// subFixture wires a single store with a parent principal (alice), a
// sub-account owned by alice (alice-work, a separated identity per
// REQ-SUBACCT-01), and an unrelated third principal (mallory) used to
// prove that only alice reaches the sub-account.
type subFixture struct {
	t             *testing.T
	store         store.Store
	clk           *clock.FakeClock
	srv           *protojmap.Server
	httpd         *httptest.Server
	alice         store.PrincipalID
	sub           store.PrincipalID
	mallory       store.PrincipalID
	aliceInbox    store.MailboxID
	subInbox      store.MailboxID
	aliceKey      string
	malloryKey    string
	aliceAcctID   protojmap.Id
	subAcctID     protojmap.Id
	malloryAcctID protojmap.Id
}

func newSubFixture(t *testing.T) *subFixture {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC))
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
	malloryPID, err := dir.CreatePrincipal(ctx, "mallory@example.com", "malpass-correct-horse-battery-1")
	if err != nil {
		t.Fatalf("create mallory: %v", err)
	}
	sub, err := fs.Meta().InsertSubPrincipal(ctx, alicePID, store.Principal{
		CanonicalEmail: "alice-work@example.com",
		DisplayName:    "Alice (work)",
	})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}

	aliceKey, _, err := createAPIKey(ctx, fs, alicePID)
	if err != nil {
		t.Fatalf("create alice key: %v", err)
	}
	malloryKey, _, err := createAPIKey(ctx, fs, malloryPID)
	if err != nil {
		t.Fatalf("create mallory key: %v", err)
	}

	aliceBoxes, err := fs.Meta().ListMailboxes(ctx, alicePID)
	if err != nil {
		t.Fatalf("ListMailboxes(alice): %v", err)
	}
	var aliceInbox store.MailboxID
	for _, mb := range aliceBoxes {
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			aliceInbox = mb.ID
			break
		}
	}
	if aliceInbox == 0 {
		t.Fatalf("alice has no INBOX after CreatePrincipal")
	}

	// InsertSubPrincipal does not auto-seed an INBOX (unlike
	// CreatePrincipal): the sub-account starts with zero mailboxes.
	subInboxMB, err := fs.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: sub.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("insert sub-account INBOX: %v", err)
	}

	srv := protojmap.NewServer(fs, dir, nil, nil, clk, protojmap.Options{
		MaxCallsInRequest:  8,
		PushPingInterval:   60 * time.Second,
		PushCoalesceWindow: 50 * time.Millisecond,
		DownloadRatePerSec: -1,
	})
	mailbox.Register(srv.Registry(), fs, nil, clk)
	email.Register(srv.Registry(), fs, nil, clk)

	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)

	return &subFixture{
		t:             t,
		store:         fs,
		clk:           clk,
		srv:           srv,
		httpd:         httpd,
		alice:         alicePID,
		sub:           sub.ID,
		mallory:       malloryPID,
		aliceInbox:    aliceInbox,
		subInbox:      subInboxMB.ID,
		aliceKey:      aliceKey,
		malloryKey:    malloryKey,
		aliceAcctID:   protojmap.AccountIDForPrincipal(alicePID),
		subAcctID:     protojmap.AccountIDForPrincipal(sub.ID),
		malloryAcctID: protojmap.AccountIDForPrincipal(malloryPID),
	}
}

// invokeAs posts a single method call authenticated with apiKey and
// returns the response invocation triple.
func (f *subFixture) invokeAs(t *testing.T, apiKey, method string, args any) (string, json.RawMessage) {
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
	req.Header.Set("Authorization", "Bearer "+apiKey)
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

// insertMessage inserts a single envelope-only message into mb owned by
// ownerPID; returns the assigned MessageID via the change feed.
func (f *subFixture) insertMessage(t *testing.T, ownerPID store.PrincipalID, mb store.MailboxID, subject string) store.MessageID {
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

// -- tests --------------------------------------------------------------

// TestSession_ListsSubAccount: alice's session descriptor includes her
// sub-account with its own accountCapabilities, isPersonal true, and
// isReadOnly false. The top-level capabilities map advertises
// CapabilitySubAccounts (REQ-SUBACCT-03, REQ-SUBACCT-11).
func TestSession_ListsSubAccount(t *testing.T) {
	f := newSubFixture(t)

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
		Capabilities map[string]any            `json:"capabilities"`
		Accounts     map[string]map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, body)
	}
	if _, ok := desc.Capabilities[string(protojmap.CapabilitySubAccounts)]; !ok {
		t.Fatalf("missing %s in session.capabilities: %s", protojmap.CapabilitySubAccounts, body)
	}
	subAcct, ok := desc.Accounts[string(f.subAcctID)]
	if !ok {
		t.Fatalf("missing sub-account %s in session.accounts: %s", f.subAcctID, body)
	}
	if subAcct["name"] != "alice-work@example.com" {
		t.Fatalf("sub-account name = %v; want alice-work@example.com", subAcct["name"])
	}
	if subAcct["isPersonal"] != true {
		t.Fatalf("sub-account isPersonal = %v; want true", subAcct["isPersonal"])
	}
	if subAcct["isReadOnly"] != false {
		t.Fatalf("sub-account isReadOnly = %v; want false", subAcct["isReadOnly"])
	}
	caps, ok := subAcct["accountCapabilities"].(map[string]any)
	if !ok || len(caps) == 0 {
		t.Fatalf("sub-account accountCapabilities missing or empty: %+v", subAcct)
	}
	if _, ok := caps[string(protojmap.CapabilityMail)]; !ok {
		t.Fatalf("sub-account accountCapabilities missing Mail capability: %+v", caps)
	}
	// The parent's own primary account is still listed alongside the
	// sub-account, not replaced by it.
	if _, ok := desc.Accounts[string(f.aliceAcctID)]; !ok {
		t.Fatalf("missing alice's own primary account: %s", body)
	}
}

// TestSession_MalloryHasNoSubAccounts: an unrelated principal's session
// lists neither alice's own account nor her sub-account.
func TestSession_MalloryHasNoSubAccounts(t *testing.T) {
	f := newSubFixture(t)

	req, _ := http.NewRequest(http.MethodGet, f.httpd.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+f.malloryKey)
	resp, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var desc struct {
		Accounts map[string]map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, body)
	}
	if len(desc.Accounts) != 1 {
		t.Fatalf("mallory accounts len = %d; want 1 (only her own): %s", len(desc.Accounts), body)
	}
	if _, ok := desc.Accounts[string(f.subAcctID)]; ok {
		t.Fatalf("mallory's session must not list alice's sub-account: %s", body)
	}
}

// TestResolveAccount_SubAccount: alice resolves her sub-account without
// any ACL grant; mallory cannot resolve it at all (REQ-SUBACCT-02/04).
func TestResolveAccount_SubAccount(t *testing.T) {
	f := newSubFixture(t)
	ctx := context.Background()
	meta := f.store.Meta()

	if pid, merr := protojmap.ResolveAccount(ctx, meta, f.alice, f.subAcctID); merr != nil || pid != f.sub {
		t.Fatalf("ResolveAccount(alice -> sub) = (%d, %v); want (%d, nil)", pid, merr, f.sub)
	}
	if _, merr := protojmap.ResolveAccount(ctx, meta, f.mallory, f.subAcctID); merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("ResolveAccount(mallory -> sub) merr = %v; want accountNotFound", merr)
	}
}

// TestMailboxGet_SubAccount: alice sees the sub-account's own INBOX (not
// hers) when she scopes Mailbox/get to the sub-account, and her own
// INBOX (not the sub-account's) when scoped to her own account.
func TestMailboxGet_SubAccount(t *testing.T) {
	f := newSubFixture(t)

	_, raw := f.invokeAs(t, f.aliceKey, "Mailbox/get", map[string]any{
		"accountId": f.subAcctID,
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("got %d mailboxes for sub-account; want 1: %+v", len(resp.List), resp.List)
	}
	if got := resp.List[0]["id"].(string); got != fmt.Sprintf("%d", f.subInbox) {
		t.Fatalf("returned mailbox id %s; want sub-account INBOX %d", got, f.subInbox)
	}

	_, raw2 := f.invokeAs(t, f.aliceKey, "Mailbox/get", map[string]any{
		"accountId": f.aliceAcctID,
	})
	var resp2 struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw2, &resp2); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw2)
	}
	for _, mb := range resp2.List {
		if mb["id"].(string) == fmt.Sprintf("%d", f.subInbox) {
			t.Fatalf("alice's own-account Mailbox/get leaked the sub-account's INBOX: %+v", resp2.List)
		}
	}

	// Mallory cannot address the sub-account at all.
	name, raw3 := f.invokeAs(t, f.malloryKey, "Mailbox/get", map[string]any{
		"accountId": f.subAcctID,
	})
	if name != "error" {
		t.Fatalf("mallory Mailbox/get(sub) = %s; want error: %s", name, raw3)
	}
	var merr protojmap.MethodError
	if err := json.Unmarshal(raw3, &merr); err != nil {
		t.Fatalf("unmarshal error: %v: %s", err, raw3)
	}
	if merr.Type != "accountNotFound" {
		t.Fatalf("mallory Mailbox/get(sub) merr.Type = %s; want accountNotFound", merr.Type)
	}
}

// TestEmailIsolation_ParentAndSubAccount is the acceptance test for
// REQ-SUBACCT-04: mail delivered to the sub-account lands in the
// sub-account's own mailbox tree and is absent from every query scoped
// to the parent account; the parent's mail is likewise absent from the
// sub-account.
func TestEmailIsolation_ParentAndSubAccount(t *testing.T) {
	f := newSubFixture(t)
	parentMail := f.insertMessage(t, f.alice, f.aliceInbox, "parent-only-mail")
	subMail := f.insertMessage(t, f.sub, f.subInbox, "sub-only-mail")

	// Email/query scoped to alice's own account: sees her mail, never
	// the sub-account's.
	_, raw := f.invokeAs(t, f.aliceKey, "Email/query", map[string]any{
		"accountId": f.aliceAcctID,
	})
	var queryResp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &queryResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	assertContains(t, queryResp.IDs, fmt.Sprintf("%d", parentMail), "alice's own Email/query")
	assertNotContains(t, queryResp.IDs, fmt.Sprintf("%d", subMail), "alice's own Email/query")

	// Email/query scoped to the sub-account: sees the sub-account's
	// mail, never alice's own.
	_, raw2 := f.invokeAs(t, f.aliceKey, "Email/query", map[string]any{
		"accountId": f.subAcctID,
	})
	var queryResp2 struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw2, &queryResp2); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw2)
	}
	assertContains(t, queryResp2.IDs, fmt.Sprintf("%d", subMail), "sub-account Email/query")
	assertNotContains(t, queryResp2.IDs, fmt.Sprintf("%d", parentMail), "sub-account Email/query")

	// Email/get: fetching the sub-account's message via alice's own
	// accountId reports notFound rather than leaking it; likewise the
	// parent's message is notFound when addressed via the sub-account.
	_, raw3 := f.invokeAs(t, f.aliceKey, "Email/get", map[string]any{
		"accountId":  f.aliceAcctID,
		"ids":        []string{fmt.Sprintf("%d", subMail)},
		"properties": []string{"id"},
	})
	var getResp struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	if err := json.Unmarshal(raw3, &getResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw3)
	}
	if len(getResp.List) != 0 || len(getResp.NotFound) != 1 {
		t.Fatalf("Email/get(alice's account, subMail) = %+v; want notFound only", getResp)
	}

	_, raw4 := f.invokeAs(t, f.aliceKey, "Email/get", map[string]any{
		"accountId":  f.subAcctID,
		"ids":        []string{fmt.Sprintf("%d", parentMail)},
		"properties": []string{"id"},
	})
	var getResp2 struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	if err := json.Unmarshal(raw4, &getResp2); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw4)
	}
	if len(getResp2.List) != 0 || len(getResp2.NotFound) != 1 {
		t.Fatalf("Email/get(sub-account, parentMail) = %+v; want notFound only", getResp2)
	}
}

// TestEmailState_AdvancesIndependently: REQ-SUBACCT-03/04 -- inserting
// mail into the sub-account must not move the parent's Email state, and
// inserting mail into the parent must not move the sub-account's.
func TestEmailState_AdvancesIndependently(t *testing.T) {
	f := newSubFixture(t)

	stateOf := func(acctID protojmap.Id) string {
		_, raw := f.invokeAs(t, f.aliceKey, "Email/get", map[string]any{
			"accountId": acctID,
			"ids":       []string{},
		})
		var resp struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, raw)
		}
		return resp.State
	}

	aliceBefore := stateOf(f.aliceAcctID)
	subBefore := stateOf(f.subAcctID)

	// Mutate only the sub-account.
	f.insertMessage(t, f.sub, f.subInbox, "advance-sub-state")

	aliceAfterSub := stateOf(f.aliceAcctID)
	subAfterSub := stateOf(f.subAcctID)
	if aliceAfterSub != aliceBefore {
		t.Fatalf("alice's Email state moved after a sub-account-only mutation: %s -> %s", aliceBefore, aliceAfterSub)
	}
	if subAfterSub == subBefore {
		t.Fatalf("sub-account's Email state did not advance after its own mutation: stayed at %s", subBefore)
	}

	// Mutate only the parent.
	f.insertMessage(t, f.alice, f.aliceInbox, "advance-parent-state")

	aliceAfterParent := stateOf(f.aliceAcctID)
	subAfterParent := stateOf(f.subAcctID)
	if subAfterParent != subAfterSub {
		t.Fatalf("sub-account's Email state moved after a parent-only mutation: %s -> %s", subAfterSub, subAfterParent)
	}
	if aliceAfterParent == aliceAfterSub {
		t.Fatalf("alice's Email state did not advance after her own mutation: stayed at %s", aliceAfterSub)
	}
}

func assertContains(t *testing.T, xs []string, want, ctxMsg string) {
	t.Helper()
	for _, x := range xs {
		if x == want {
			return
		}
	}
	t.Fatalf("%s: %v does not contain %q", ctxMsg, xs, want)
}

func assertNotContains(t *testing.T, xs []string, unwanted, ctxMsg string) {
	t.Helper()
	for _, x := range xs {
		if x == unwanted {
			t.Fatalf("%s: %v unexpectedly contains %q", ctxMsg, xs, unwanted)
		}
	}
}
