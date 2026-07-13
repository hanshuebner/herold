package protojmap_test

// Mailing-list archive mailbox, Stage 4, JMAP half (epic #187,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-70..74).
// Mirrors crossaccount_test.go's fixture style, but the grant under test
// is written by internal/maillist.SyncMemberArchiveGrant (the exact call
// the admin REST roster handlers make) rather than a hand-seeded
// SetMailboxACL row -- proving the mailing-list wiring produces a grant
// the JMAP sharing surface (epic #210) honours with no protocol change,
// symmetric to internal/protoimap/mlist_archive_test.go's IMAP proof.

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
	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// mlistArchiveJMAPFixture wires one store with a mailing list, its
// archive mailbox, an owner, a nomail member with a REQ-MLIST-72 archive
// read grant, and an outsider principal with none.
type mlistArchiveJMAPFixture struct {
	t            *testing.T
	store        store.Store
	clk          *clock.FakeClock
	srv          *protojmap.Server
	httpd        *httptest.Server
	ml           store.MailingList
	archiveMB    store.MailboxID
	memberID     store.PrincipalID
	memberKey    string
	outsiderID   store.PrincipalID
	outsiderKey  string
	groupAcctID  protojmap.Id
	memberAcctID protojmap.Id
}

func newMlistArchiveJMAPFixture(t *testing.T) *mlistArchiveJMAPFixture {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(ctx, filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)

	owner, err := dir.CreatePrincipal(ctx, "owner@example.com", "ownerpass-correct-horse-battery-1")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	memberID, err := dir.CreatePrincipal(ctx, "member@example.com", "memberpass-correct-horse-battery-1")
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	outsiderID, err := dir.CreatePrincipal(ctx, "outsider@example.com", "outsiderpass-correct-horse-battery-1")
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	memberKey, _, err := createAPIKey(ctx, fs, memberID)
	if err != nil {
		t.Fatalf("create member key: %v", err)
	}
	outsiderKey, _, err := createAPIKey(ctx, fs, outsiderID)
	if err != nil {
		t.Fatalf("create outsider key: %v", err)
	}

	group, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindGroup, CanonicalEmail: "list@example.com", DisplayName: "Archive List",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(group): %v", err)
	}
	ml, err := fs.Meta().InsertMailingList(ctx, store.MailingList{
		PrincipalID: group.ID, PostingAddress: "list@example.com", DisplayName: "Archive List",
		OwnerID: owner,
	})
	if err != nil {
		t.Fatalf("InsertMailingList: %v", err)
	}
	archiveMB, err := fs.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: group.ID, Name: maillist.ArchiveMailboxName(ml), Attributes: store.MailboxAttrArchive,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(archive): %v", err)
	}
	ml.ArchiveMailboxID = &archiveMB.ID
	if err := fs.Meta().UpdateMailingList(ctx, ml); err != nil {
		t.Fatalf("UpdateMailingList(set archive): %v", err)
	}

	memRow, err := fs.Meta().AddMailingListMember(ctx, store.MailingListMember{
		ListID: ml.ID, PrincipalID: &memberID, DeliveryMode: store.MailingListDeliveryNoMail,
	})
	if err != nil {
		t.Fatalf("AddMailingListMember: %v", err)
	}
	if err := maillist.SyncMemberArchiveGrant(ctx, fs.Meta(), ml, memRow); err != nil {
		t.Fatalf("SyncMemberArchiveGrant: %v", err)
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

	return &mlistArchiveJMAPFixture{
		t: t, store: fs, clk: clk, srv: srv, httpd: httpd,
		ml: ml, archiveMB: archiveMB.ID,
		memberID: memberID, memberKey: memberKey,
		outsiderID: outsiderID, outsiderKey: outsiderKey,
		groupAcctID:  protojmap.AccountIDForPrincipal(group.ID),
		memberAcctID: protojmap.AccountIDForPrincipal(memberID),
	}
}

// seedArchivePost inserts a message into the archive mailbox.
func (f *mlistArchiveJMAPFixture) seedArchivePost(t *testing.T) store.MessageID {
	t.Helper()
	ctx := context.Background()
	rawBody := "Subject: archived-post\r\nMessage-ID: <archived-post@test>\r\nFrom: poster@example.net\r\nTo: list@example.com\r\nDate: Wed, 01 Jul 2026 00:00:00 +0000\r\n\r\nbody\r\n"
	ref, err := f.store.Blobs().Put(ctx, bytes.NewReader([]byte(rawBody)))
	if err != nil {
		t.Fatalf("Blobs().Put: %v", err)
	}
	msg := store.Message{
		PrincipalID:  0, // set below via the group's own id read back
		MailboxID:    f.archiveMB,
		InternalDate: f.clk.Now(),
		ReceivedAt:   f.clk.Now(),
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     store.Envelope{Subject: "archived-post", MessageID: "archived-post@test"},
	}
	msg.PrincipalID = f.ml.PrincipalID
	if _, _, err := f.store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: f.archiveMB}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	feed, err := f.store.Meta().ReadChangeFeed(ctx, f.ml.PrincipalID, 0, 1000)
	if err != nil {
		t.Fatalf("ReadChangeFeed: %v", err)
	}
	for i := len(feed) - 1; i >= 0; i-- {
		if feed[i].Kind == store.EntityKindEmail && feed[i].Op == store.ChangeOpCreated {
			return store.MessageID(feed[i].EntityID)
		}
	}
	t.Fatal("could not locate inserted archive message id in change feed")
	return 0
}

// invokeAs posts a single method call as the given API key and returns
// the response invocation triple.
func (f *mlistArchiveJMAPFixture) invokeAs(t *testing.T, key, method string, args any) (string, json.RawMessage) {
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
	req.Header.Set("Authorization", "Bearer "+key)
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

// TestMlistArchive_JMAP_Member_CanRead_CannotWriteOrDestroy is the
// REQ-MLIST-72/73 JMAP-side proof: a nomail member sees the archive
// account, reads the seeded post via Email/get, and Email/set (update or
// destroy) on it is forbidden.
func TestMlistArchive_JMAP_Member_CanRead_CannotWriteOrDestroy(t *testing.T) {
	f := newMlistArchiveJMAPFixture(t)
	mid := f.seedArchivePost(t)

	// The archive's account (the Group principal) appears as a secondary,
	// read-only account in the member's session.
	req, _ := http.NewRequest(http.MethodGet, f.httpd.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+f.memberKey)
	resp, err := f.httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var desc struct {
		Accounts map[string]map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("unmarshal session: %v: %s", err, body)
	}
	acct, ok := desc.Accounts[string(f.groupAcctID)]
	if !ok {
		t.Fatalf("archive account missing from member's session: %s", body)
	}
	if !acct["isReadOnly"].(bool) {
		t.Fatalf("archive account isReadOnly = false; want true (REQ-MLIST-72 strictly read-only grant): %+v", acct)
	}

	// Mailbox/get: the archive mailbox is visible.
	_, raw := f.invokeAs(t, f.memberKey, "Mailbox/get", map[string]any{"accountId": f.groupAcctID})
	var mbResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &mbResp); err != nil {
		t.Fatalf("unmarshal Mailbox/get: %v: %s", err, raw)
	}
	if len(mbResp.List) != 1 {
		t.Fatalf("Mailbox/get list = %+v; want exactly the archive mailbox", mbResp.List)
	}

	// Email/get: the member can read the archived post.
	_, raw = f.invokeAs(t, f.memberKey, "Email/get", map[string]any{
		"accountId":  f.groupAcctID,
		"ids":        []string{fmt.Sprintf("%d", mid)},
		"properties": []string{"id", "subject"},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, raw)
	}
	if len(getResp.List) != 1 || getResp.List[0]["subject"] != "archived-post" {
		t.Fatalf("Email/get did not return the archived post: %s", raw)
	}

	// Email/query: search surfaces the archived post too.
	_, raw = f.invokeAs(t, f.memberKey, "Email/query", map[string]any{"accountId": f.groupAcctID})
	var queryResp struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &queryResp); err != nil {
		t.Fatalf("unmarshal Email/query: %v: %s", err, raw)
	}
	found := false
	for _, id := range queryResp.IDs {
		if id == fmt.Sprintf("%d", mid) {
			found = true
		}
	}
	if !found {
		t.Fatalf("Email/query did not include the archived post: %v", queryResp.IDs)
	}

	// Email/set destroy: forbidden -- the archive grant carries neither
	// 'e' (Expunge) nor 't' (DeleteMessage), so protojmap's first-pass
	// mutate check (mail/email/set.go's mutateMask) denies it outright.
	_, raw = f.invokeAs(t, f.memberKey, "Email/set", map[string]any{
		"accountId": f.groupAcctID,
		"destroy":   []string{fmt.Sprintf("%d", mid)},
	})
	var setResp struct {
		Destroyed    []string                  `json:"destroyed"`
		NotDestroyed map[string]map[string]any `json:"notDestroyed"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal Email/set destroy: %v: %s", err, raw)
	}
	if len(setResp.Destroyed) != 0 {
		t.Fatalf("archive member destroyed the archived post: %+v", setResp)
	}
	if len(setResp.NotDestroyed) != 1 || setResp.NotDestroyed[fmt.Sprintf("%d", mid)]["type"] != "forbidden" {
		t.Fatalf("Email/set destroy notDestroyed = %+v; want forbidden", setResp.NotDestroyed)
	}

	// Email/set update of an arbitrary keyword: forbidden -- the archive
	// grant carries no write bit at all (REQ-MLIST-72's "cannot ...
	// otherwise mutate the archive"; the grant is deliberately narrower
	// than the coarse GrantLevelRead tier precisely so this is denied,
	// see internal/maillist/archive.go's archiveReadRights doc comment).
	_, raw = f.invokeAs(t, f.memberKey, "Email/set", map[string]any{
		"accountId": f.groupAcctID,
		"update": map[string]any{
			fmt.Sprintf("%d", mid): map[string]any{"keywords/$flagged": true},
		},
	})
	var updResp struct {
		Updated    map[string]any            `json:"updated"`
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &updResp); err != nil {
		t.Fatalf("unmarshal Email/set update: %v: %s", err, raw)
	}
	if len(updResp.Updated) != 0 {
		t.Fatalf("archive member updated a non-seen keyword on the archived post: %+v", updResp)
	}
	if len(updResp.NotUpdated) != 1 || updResp.NotUpdated[fmt.Sprintf("%d", mid)]["type"] != "forbidden" {
		t.Fatalf("Email/set update notUpdated = %+v; want forbidden", updResp.NotUpdated)
	}

	// The post survived every denied mutation.
	_, raw = f.invokeAs(t, f.memberKey, "Email/get", map[string]any{
		"accountId": f.groupAcctID, "ids": []string{fmt.Sprintf("%d", mid)}, "properties": []string{"id"},
	})
	var stillThere struct {
		List []map[string]any `json:"list"`
	}
	_ = json.Unmarshal(raw, &stillThere)
	if len(stillThere.List) != 1 {
		t.Fatalf("archived post gone after denied-mutation attempts: %s", raw)
	}
}

// TestMlistArchive_JMAP_NonMember_AccountNotFound verifies a principal
// with no archive grant gets accountNotFound, not an empty/error leak of
// the archive's existence.
func TestMlistArchive_JMAP_NonMember_AccountNotFound(t *testing.T) {
	f := newMlistArchiveJMAPFixture(t)
	f.seedArchivePost(t)

	name, raw := f.invokeAs(t, f.outsiderKey, "Mailbox/get", map[string]any{"accountId": f.groupAcctID})
	if name != "error" {
		t.Fatalf("outsider Mailbox/get response = %s; want error: %s", name, raw)
	}
	var merr protojmap.MethodError
	if err := json.Unmarshal(raw, &merr); err != nil {
		t.Fatalf("unmarshal error: %v: %s", err, raw)
	}
	if merr.Type != "accountNotFound" {
		t.Fatalf("outsider merr.Type = %s; want accountNotFound", merr.Type)
	}
}
