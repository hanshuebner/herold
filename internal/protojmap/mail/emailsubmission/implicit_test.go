package emailsubmission

// implicit_test.go drives EmailSubmission/set's onSuccessUpdateEmail and
// onSuccessDestroyEmail (RFC 8621 section 7.5) through the /jmap endpoint,
// where the dispatcher runs the implicit Email/set through the Email/set
// handler and returns its response right after the EmailSubmission/set
// response, under the same method call id.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness"
)

// jmapClient posts JMAP requests for one principal to a test server that
// serves Mailbox, Email and h's EmailSubmission handlers.
type jmapClient struct {
	client  *http.Client
	baseURL string
	apiKey  string
	account string
}

func serveJMAP(t *testing.T, h *handlerSet, st store.Store, p store.Principal) *jmapClient {
	t.Helper()
	ctx := context.Background()
	srv, _ := testharness.Start(t, testharness.Options{
		Store:     st,
		Clock:     h.clk,
		Listeners: []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}},
	})
	plaintext := fmt.Sprintf("hk_test_submit_%d", p.ID)
	if _, err := st.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        protoadmin.HashAPIKey(plaintext),
		Name:        "test",
		ScopeJSON:   `["mail.receive","mail.send"]`,
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	dir := directory.New(st.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(st, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	mailbox.Register(jmapServ.Registry(), st, srv.Logger, srv.Clock)
	email.Register(jmapServ.Registry(), st, srv.Logger, srv.Clock)
	jmapServ.Registry().Register(capabilitySubmission, getHandler{h: h})
	jmapServ.Registry().Register(capabilitySubmission, setHandler{h: h})
	t.Cleanup(func() { email.WaitBackgroundWrites(jmapServ.Registry()) })
	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &jmapClient{client: client, baseURL: base, apiKey: plaintext, account: protojmap.AccountIDForPrincipal(p.ID)}
}

// call posts methodCalls with the core, mail and submission capabilities
// and returns the method responses.
func (c *jmapClient) call(t *testing.T, using []string, methodCalls ...[]any) []protojmap.Invocation {
	t.Helper()
	body, err := json.Marshal(map[string]any{"using": using, "methodCalls": methodCalls})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/jmap", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap: status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses []protojmap.Invocation `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	return out.MethodResponses
}

func responseNames(got []protojmap.Invocation) []string {
	out := make([]string, len(got))
	for i, inv := range got {
		out[i] = inv.Name + "/" + inv.CallID
	}
	return out
}

var allCapabilities = []string{
	string(protojmap.CapabilityCore),
	string(protojmap.CapabilityMail),
	string(capabilitySubmission),
}

// insertDraft stores a draft in mb and returns its id.
func insertDraft(t *testing.T, st store.Store, p store.Principal, mb store.Mailbox, subject string) store.MessageID {
	t.Helper()
	ctx := context.Background()
	body := "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: " + subject + "\r\n\r\nhi\r\n"
	ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	uid, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID: p.ID,
		Blob:        ref,
		Size:        ref.Size,
		Envelope:    store.Envelope{Subject: subject, From: "alice@example.test", To: "bob@example.test"},
	}, []store.MessageMailbox{{MailboxID: mb.ID, Flags: store.MessageFlagDraft}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.UID == uid {
			return m.ID
		}
	}
	t.Fatalf("draft %q not found", subject)
	return 0
}

func newImplicitSetup(t *testing.T) (*handlerSet, store.Store, store.Principal, store.Mailbox) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h, p, drafts, _ := newSetupFromStore(t, st)
	return h, st, p, drafts
}

// TestEmailSubmission_Set_OnSuccessDestroyEmail_DestroysSubmittedEmail:
// the Email a successful submission sent is destroyed, and the implicit
// Email/set response follows under the same call id with the Email
// state Email/get reports.
func TestEmailSubmission_Set_OnSuccessDestroyEmail_DestroysSubmittedEmail(t *testing.T) {
	h, st, p, drafts := newImplicitSetup(t)
	c := serveJMAP(t, h, st, p)
	draft := insertDraft(t, st, p, drafts, "destroy me")
	draftID := renderEmailID(draft)

	got := c.call(t, allCapabilities,
		[]any{"EmailSubmission/set", map[string]any{
			"accountId":             c.account,
			"create":                map[string]any{"k1": map[string]any{"identityId": "default", "emailId": draftID}},
			"onSuccessDestroyEmail": []string{"#k1"},
		}, "send"},
		[]any{"Email/get", map[string]any{"accountId": c.account, "ids": []string{draftID}}, "get"},
	)
	if len(got) != 3 {
		t.Fatalf("responses = %v, want 3", responseNames(got))
	}
	if got[0].Name != "EmailSubmission/set" || got[1].Name != "Email/set" || got[2].Name != "Email/get" {
		t.Fatalf("response names = %s, %s, %s; want EmailSubmission/set, Email/set, Email/get",
			got[0].Name, got[1].Name, got[2].Name)
	}
	if got[1].CallID != "send" {
		t.Fatalf("implicit Email/set call id = %q, want %q", got[1].CallID, "send")
	}
	var set struct {
		NewState  string   `json:"newState"`
		Destroyed []string `json:"destroyed"`
	}
	if err := json.Unmarshal(got[1].Args, &set); err != nil {
		t.Fatalf("unmarshal Email/set: %v: %s", err, got[1].Args)
	}
	if len(set.Destroyed) != 1 || set.Destroyed[0] != draftID {
		t.Fatalf("implicit Email/set destroyed = %v, want [%s]: %s", set.Destroyed, draftID, got[1].Args)
	}
	var get struct {
		State    string   `json:"state"`
		NotFound []string `json:"notFound"`
	}
	if err := json.Unmarshal(got[2].Args, &get); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, got[2].Args)
	}
	if len(get.NotFound) != 1 || get.NotFound[0] != draftID {
		t.Fatalf("Email/get notFound = %v, want [%s]", get.NotFound, draftID)
	}
	if set.NewState != get.State {
		t.Fatalf("implicit Email/set newState = %q, Email/get state = %q; want equal", set.NewState, get.State)
	}
	if _, err := st.Meta().GetMessage(context.Background(), draft); err == nil {
		t.Fatalf("draft %d still stored after onSuccessDestroyEmail", draft)
	}
}

// TestEmailSubmission_Set_OnSuccess_SkipsFailedCreate: neither argument
// touches the Email of a submission that was not created.
func TestEmailSubmission_Set_OnSuccess_SkipsFailedCreate(t *testing.T) {
	h, st, p, drafts := newImplicitSetup(t)
	c := serveJMAP(t, h, st, p)
	draft := insertDraft(t, st, p, drafts, "kept")

	got := c.call(t, allCapabilities,
		[]any{"EmailSubmission/set", map[string]any{
			"accountId":             c.account,
			"create":                map[string]any{"k1": map[string]any{"identityId": "no-such-identity", "emailId": renderEmailID(draft)}},
			"onSuccessUpdateEmail":  map[string]any{"#k1": map[string]any{"keywords/$seen": true}},
			"onSuccessDestroyEmail": []string{"#k1"},
		}, "send"},
	)
	if len(got) != 1 || got[0].Name != "EmailSubmission/set" {
		t.Fatalf("responses = %v, want only EmailSubmission/set", responseNames(got))
	}
	m, err := st.Meta().GetMessage(context.Background(), draft)
	if err != nil {
		t.Fatalf("draft gone after a failed submission: %v", err)
	}
	if m.Flags&store.MessageFlagSeen != 0 {
		t.Fatalf("draft became $seen after a failed submission")
	}
}

// TestEmailSubmission_Set_OnSuccessUpdateEmail_ImplicitResponse: the
// implicit Email/set response carries the submission's call id and the
// Email state Email/changes and Email/get work from, and its patch adds
// and removes mailboxes the way Email/set does.
func TestEmailSubmission_Set_OnSuccessUpdateEmail_ImplicitResponse(t *testing.T) {
	h, st, p, drafts := newImplicitSetup(t)
	ctx := context.Background()
	sent, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Sent", Attributes: store.MailboxAttrSent})
	if err != nil {
		t.Fatalf("InsertMailbox Sent: %v", err)
	}
	archive, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "Archive"})
	if err != nil {
		t.Fatalf("InsertMailbox Archive: %v", err)
	}
	c := serveJMAP(t, h, st, p)
	draft := insertDraft(t, st, p, drafts, "file me")
	draftID := renderEmailID(draft)

	got := c.call(t, allCapabilities,
		[]any{"EmailSubmission/set", map[string]any{
			"accountId": c.account,
			"create":    map[string]any{"k1": map[string]any{"identityId": "default", "emailId": draftID}},
			"onSuccessUpdateEmail": map[string]any{"#k1": map[string]any{
				fmt.Sprintf("mailboxIds/%d", drafts.ID):  nil,
				fmt.Sprintf("mailboxIds/%d", sent.ID):    true,
				fmt.Sprintf("mailboxIds/%d", archive.ID): true,
				"keywords/$draft":                        nil,
				"keywords/$seen":                         true,
			}},
		}, "send"},
		[]any{"Email/get", map[string]any{
			"accountId": c.account, "ids": []string{draftID}, "properties": []string{"mailboxIds", "keywords"},
		}, "get"},
	)
	if len(got) != 3 || got[1].Name != "Email/set" {
		t.Fatalf("responses = %v, want EmailSubmission/set, Email/set, Email/get", responseNames(got))
	}
	if got[1].CallID != "send" {
		t.Fatalf("implicit Email/set call id = %q, want %q", got[1].CallID, "send")
	}
	var set struct {
		NewState string                     `json:"newState"`
		Updated  map[string]json.RawMessage `json:"updated"`
	}
	if err := json.Unmarshal(got[1].Args, &set); err != nil {
		t.Fatalf("unmarshal Email/set: %v: %s", err, got[1].Args)
	}
	if _, ok := set.Updated[draftID]; !ok {
		t.Fatalf("implicit Email/set updated = %s, want %s", got[1].Args, draftID)
	}
	var get struct {
		State string `json:"state"`
		List  []struct {
			MailboxIDs map[string]bool `json:"mailboxIds"`
			Keywords   map[string]bool `json:"keywords"`
		} `json:"list"`
	}
	if err := json.Unmarshal(got[2].Args, &get); err != nil {
		t.Fatalf("unmarshal Email/get: %v: %s", err, got[2].Args)
	}
	if set.NewState != get.State {
		t.Fatalf("implicit Email/set newState = %q, Email/get state = %q; want equal", set.NewState, get.State)
	}
	if len(get.List) != 1 {
		t.Fatalf("Email/get list = %s", got[2].Args)
	}
	wantMailboxes := map[string]bool{fmt.Sprintf("%d", sent.ID): true, fmt.Sprintf("%d", archive.ID): true}
	if fmt.Sprint(get.List[0].MailboxIDs) != fmt.Sprint(wantMailboxes) {
		t.Fatalf("mailboxIds = %v, want %v", get.List[0].MailboxIDs, wantMailboxes)
	}
	if get.List[0].Keywords["$draft"] || !get.List[0].Keywords["$seen"] {
		t.Fatalf("keywords = %v, want $seen without $draft", get.List[0].Keywords)
	}
}
