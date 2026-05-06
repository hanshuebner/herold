package protojmap_test

// Tests for cross-account session descriptor, account resolver, and
// Blob/copy. Covers REQ-PROTO-40/41.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// multiFixture holds two principals (alice = caller, bob = target owner)
// with an INBOX for bob. ACL is set by each test.
type multiFixture struct {
	t        *testing.T
	st       store.Store
	dir      *directory.Directory
	clk      *clock.FakeClock
	srv      *protojmap.Server
	httpd    *httptest.Server
	alicePID store.PrincipalID
	bobPID   store.PrincipalID
	aliceKey string
	bobKey   string
	bobInbox store.Mailbox
}

func newMultiFixture(t *testing.T) *multiFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	ctx := context.Background()
	if err := fs.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	alicePID, err := dir.CreatePrincipal(ctx, "alice@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bobPID, err := dir.CreatePrincipal(ctx, "bob@example.com", "correct-horse-battery-staple-1")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	aliceKey, _, err := createAPIKeyNamed(ctx, fs, alicePID, "alice")
	if err != nil {
		t.Fatalf("alice api key: %v", err)
	}
	bobKey, _, err := createAPIKeyNamed(ctx, fs, bobPID, "bob")
	if err != nil {
		t.Fatalf("bob api key: %v", err)
	}
	// CreatePrincipal auto-provisions an INBOX; fetch it rather than inserting a duplicate.
	mboxes, err := fs.Meta().ListMailboxes(ctx, bobPID)
	if err != nil {
		t.Fatalf("list bob mailboxes: %v", err)
	}
	var bobInbox store.Mailbox
	for _, m := range mboxes {
		if m.Name == "INBOX" {
			bobInbox = m
			break
		}
	}
	if bobInbox.ID == 0 {
		t.Fatalf("bob INBOX not found after principal creation")
	}
	srv := protojmap.NewServer(fs, dir, nil, nil, clk, protojmap.Options{
		DownloadRatePerSec: -1,
	})
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)
	return &multiFixture{
		t:        t,
		st:       fs,
		dir:      dir,
		clk:      clk,
		srv:      srv,
		httpd:    httpd,
		alicePID: alicePID,
		bobPID:   bobPID,
		aliceKey: aliceKey,
		bobKey:   bobKey,
		bobInbox: bobInbox,
	}
}

// grantACL sets an ACL row on bob's INBOX granting rights to alice.
func (f *multiFixture) grantACL(rights store.ACLRights) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.st.Meta().SetMailboxACL(ctx, f.bobInbox.ID, &f.alicePID, rights, f.bobPID); err != nil {
		f.t.Fatalf("grantACL: %v", err)
	}
}

// sessionForAlice fetches the session descriptor as alice and returns the
// parsed accounts map.
func (f *multiFixture) sessionForAlice() map[string]any {
	f.t.Helper()
	req, err := http.NewRequest("GET", f.httpd.URL+"/.well-known/jmap", nil)
	if err != nil {
		f.t.Fatalf("newRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.aliceKey)
	resp, err := f.httpd.Client().Do(req)
	if err != nil {
		f.t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("session status = %d", resp.StatusCode)
	}
	var desc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&desc); err != nil {
		f.t.Fatalf("decode: %v", err)
	}
	accounts, _ := desc["accounts"].(map[string]any)
	return accounts
}

// -- Session secondary accounts tests ------------------------------------

// TestSession_SecondaryAccount_Absent: no ACL on bob's mailbox, so alice's
// session should only show her own account.
func TestSession_SecondaryAccount_Absent(t *testing.T) {
	f := newMultiFixture(t)
	accts := f.sessionForAlice()
	if len(accts) != 1 {
		t.Fatalf("expected 1 account (own only), got %d: %v", len(accts), accts)
	}
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	if _, ok := accts[aliceAccountID]; !ok {
		t.Fatalf("alice's own account missing from session")
	}
}

// TestSession_SecondaryAccount_ReadOnly: alice has Lookup+Read on bob's INBOX
// but no write rights, so bob's account appears as isReadOnly=true.
func TestSession_SecondaryAccount_ReadOnly(t *testing.T) {
	f := newMultiFixture(t)
	f.grantACL(store.ACLRightLookup | store.ACLRightRead)
	accts := f.sessionForAlice()
	if len(accts) != 2 {
		t.Fatalf("expected 2 accounts (own + bob), got %d", len(accts))
	}
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	bobDesc, ok := accts[bobAccountID]
	if !ok {
		t.Fatalf("bob's account missing from session")
	}
	desc := bobDesc.(map[string]any)
	if isPersonal, _ := desc["isPersonal"].(bool); isPersonal {
		t.Fatalf("bob's account should not be isPersonal")
	}
	if isReadOnly, _ := desc["isReadOnly"].(bool); !isReadOnly {
		t.Fatalf("bob's read-only account should have isReadOnly=true")
	}
}

// TestSession_SecondaryAccount_Writable: alice has insert rights on bob's INBOX,
// so bob's account appears as isReadOnly=false.
func TestSession_SecondaryAccount_Writable(t *testing.T) {
	f := newMultiFixture(t)
	f.grantACL(store.ACLRightLookup | store.ACLRightRead | store.ACLRightInsert)
	accts := f.sessionForAlice()
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	bobDesc, ok := accts[bobAccountID]
	if !ok {
		t.Fatalf("bob's account missing from session")
	}
	desc := bobDesc.(map[string]any)
	if isReadOnly, _ := desc["isReadOnly"].(bool); isReadOnly {
		t.Fatalf("bob's writable account should have isReadOnly=false")
	}
}

// -- ResolveAccount unit tests ------------------------------------------

func TestResolveAccount_OwnAccount(t *testing.T) {
	f := newMultiFixture(t)
	ctx := context.Background()
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	pid, merr := protojmap.ResolveAccount(ctx, f.st.Meta(), f.alicePID, aliceAccountID)
	if merr != nil {
		t.Fatalf("ResolveAccount own account: %v", merr)
	}
	if pid != f.alicePID {
		t.Fatalf("pid = %d, want %d", pid, f.alicePID)
	}
}

func TestResolveAccount_ForeignWithAccess(t *testing.T) {
	f := newMultiFixture(t)
	f.grantACL(store.ACLRightLookup)
	ctx := context.Background()
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	pid, merr := protojmap.ResolveAccount(ctx, f.st.Meta(), f.alicePID, bobAccountID)
	if merr != nil {
		t.Fatalf("ResolveAccount foreign with access: %v", merr)
	}
	if pid != f.bobPID {
		t.Fatalf("pid = %d, want %d", pid, f.bobPID)
	}
}

func TestResolveAccount_ForeignWithoutAccess(t *testing.T) {
	f := newMultiFixture(t)
	// No ACL granted.
	ctx := context.Background()
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	_, merr := protojmap.ResolveAccount(ctx, f.st.Meta(), f.alicePID, bobAccountID)
	if merr == nil {
		t.Fatalf("expected accountNotFound error, got nil")
	}
	if merr.Type != "accountNotFound" {
		t.Fatalf("error type = %q, want accountNotFound", merr.Type)
	}
}

func TestResolveAccount_InvalidAccountID(t *testing.T) {
	f := newMultiFixture(t)
	ctx := context.Background()
	_, merr := protojmap.ResolveAccount(ctx, f.st.Meta(), f.alicePID, "not-valid-account-id")
	if merr == nil {
		t.Fatalf("expected error for invalid accountId")
	}
	if merr.Type != "accountNotFound" {
		t.Fatalf("error type = %q, want accountNotFound", merr.Type)
	}
}

func TestResolveAccount_EmptyAccountID(t *testing.T) {
	f := newMultiFixture(t)
	ctx := context.Background()
	_, merr := protojmap.ResolveAccount(ctx, f.st.Meta(), f.alicePID, "")
	if merr == nil {
		t.Fatalf("expected error for empty accountId")
	}
	if merr.Type != "invalidArguments" {
		t.Fatalf("error type = %q, want invalidArguments", merr.Type)
	}
}

// -- Blob/copy tests ----------------------------------------------------

// jmapPostAs issues a JMAP POST as the given API key.
func jmapPostAs(t *testing.T, httpd *httptest.Server, key string, calls []protojmap.Invocation) (protojmap.Response, int) {
	t.Helper()
	envelope := protojmap.Request{
		Using:       []protojmap.CapabilityID{protojmap.CapabilityCore},
		MethodCalls: calls,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest("POST", httpd.URL+"/jmap", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpd.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return protojmap.Response{}, resp.StatusCode
	}
	var out protojmap.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out, resp.StatusCode
}

func TestBlobCopy_SamePrincipal_Success(t *testing.T) {
	f := newMultiFixture(t)
	ctx := context.Background()
	// Upload a blob so Stat() finds it.
	content := []byte("blob copy test content")
	ref, err := f.st.Blobs().Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	args := map[string]any{
		"fromAccountId": aliceAccountID,
		"accountId":     aliceAccountID,
		"blobIds":       []string{ref.Hash},
	}
	argsJSON, _ := json.Marshal(args)
	resp, status := jmapPostAs(t, f.httpd, f.aliceKey, []protojmap.Invocation{
		{Name: "Blob/copy", Args: json.RawMessage(argsJSON), CallID: "c1"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if len(resp.MethodResponses) != 1 {
		t.Fatalf("len responses = %d", len(resp.MethodResponses))
	}
	r := resp.MethodResponses[0]
	if r.Name == "error" {
		t.Fatalf("got error response: %s", r.Args)
	}
	var copyResp struct {
		Copied    map[string]string `json:"copied"`
		NotCopied map[string]any    `json:"notCopied"`
	}
	if err := json.Unmarshal(r.Args, &copyResp); err != nil {
		t.Fatalf("decode copy response: %v", err)
	}
	if copyResp.Copied[ref.Hash] != ref.Hash {
		t.Fatalf("copied[%q] = %q, want %q", ref.Hash, copyResp.Copied[ref.Hash], ref.Hash)
	}
}

func TestBlobCopy_CrossAccount_Success(t *testing.T) {
	f := newMultiFixture(t)
	f.grantACL(store.ACLRightLookup | store.ACLRightRead)
	ctx := context.Background()
	// Upload a blob.
	content := []byte("cross-account blob copy test")
	ref, err := f.st.Blobs().Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	args := map[string]any{
		"fromAccountId": bobAccountID,
		"accountId":     aliceAccountID,
		"blobIds":       []string{ref.Hash},
	}
	argsJSON, _ := json.Marshal(args)
	resp, status := jmapPostAs(t, f.httpd, f.aliceKey, []protojmap.Invocation{
		{Name: "Blob/copy", Args: json.RawMessage(argsJSON), CallID: "c1"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	r := resp.MethodResponses[0]
	if r.Name == "error" {
		t.Fatalf("got error: %s", r.Args)
	}
	var copyResp struct {
		Copied map[string]string `json:"copied"`
	}
	if err := json.Unmarshal(r.Args, &copyResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if copyResp.Copied[ref.Hash] != ref.Hash {
		t.Fatalf("expected blob %q in copied, got %v", ref.Hash, copyResp.Copied)
	}
}

func TestBlobCopy_BlobNotFound(t *testing.T) {
	f := newMultiFixture(t)
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	missing := fmt.Sprintf("%064x", 0) // all-zero hash that doesn't exist
	args := map[string]any{
		"fromAccountId": aliceAccountID,
		"accountId":     aliceAccountID,
		"blobIds":       []string{missing},
	}
	argsJSON, _ := json.Marshal(args)
	resp, status := jmapPostAs(t, f.httpd, f.aliceKey, []protojmap.Invocation{
		{Name: "Blob/copy", Args: json.RawMessage(argsJSON), CallID: "c1"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	r := resp.MethodResponses[0]
	if r.Name == "error" {
		t.Fatalf("unexpected error response: %s", r.Args)
	}
	var copyResp struct {
		NotCopied map[string]struct {
			Type string `json:"type"`
		} `json:"notCopied"`
	}
	if err := json.Unmarshal(r.Args, &copyResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if copyResp.NotCopied[missing].Type != "blobNotFound" {
		t.Fatalf("notCopied[%q].type = %q, want blobNotFound", missing, copyResp.NotCopied[missing].Type)
	}
}

func TestBlobCopy_FromAccountNotFound(t *testing.T) {
	f := newMultiFixture(t)
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	// Use bob's account ID without granting ACL.
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	args := map[string]any{
		"fromAccountId": bobAccountID,
		"accountId":     aliceAccountID,
		"blobIds":       []string{"someblobid"},
	}
	argsJSON, _ := json.Marshal(args)
	resp, status := jmapPostAs(t, f.httpd, f.aliceKey, []protojmap.Invocation{
		{Name: "Blob/copy", Args: json.RawMessage(argsJSON), CallID: "c1"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	r := resp.MethodResponses[0]
	if r.Name != "error" {
		t.Fatalf("expected error response, got %q", r.Name)
	}
	var merr struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r.Args, &merr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if merr.Type != "fromAccountNotFound" {
		t.Fatalf("error type = %q, want fromAccountNotFound", merr.Type)
	}
}

func TestBlobCopy_DestAccountNotFound(t *testing.T) {
	f := newMultiFixture(t)
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	bobAccountID := protojmap.AccountIDForPrincipal(f.bobPID)
	// fromAccount is alice (accessible), accountId is bob (not accessible to alice).
	args := map[string]any{
		"fromAccountId": aliceAccountID,
		"accountId":     bobAccountID,
		"blobIds":       []string{"someblobid"},
	}
	argsJSON, _ := json.Marshal(args)
	resp, status := jmapPostAs(t, f.httpd, f.aliceKey, []protojmap.Invocation{
		{Name: "Blob/copy", Args: json.RawMessage(argsJSON), CallID: "c1"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	r := resp.MethodResponses[0]
	if r.Name != "error" {
		t.Fatalf("expected error, got %q", r.Name)
	}
	var merr struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r.Args, &merr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if merr.Type != "accountNotFound" {
		t.Fatalf("error type = %q, want accountNotFound", merr.Type)
	}
}

func TestBlobCopy_PartialSuccess(t *testing.T) {
	f := newMultiFixture(t)
	ctx := context.Background()
	// Upload one blob; the second one doesn't exist.
	content := []byte("partial success test")
	ref, err := f.st.Blobs().Put(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	missingHash := fmt.Sprintf("%064x", 1)
	aliceAccountID := protojmap.AccountIDForPrincipal(f.alicePID)
	args := map[string]any{
		"fromAccountId": aliceAccountID,
		"accountId":     aliceAccountID,
		"blobIds":       []string{ref.Hash, missingHash},
	}
	argsJSON, _ := json.Marshal(args)
	resp, status := jmapPostAs(t, f.httpd, f.aliceKey, []protojmap.Invocation{
		{Name: "Blob/copy", Args: json.RawMessage(argsJSON), CallID: "c1"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	r := resp.MethodResponses[0]
	if r.Name == "error" {
		t.Fatalf("unexpected error: %s", r.Args)
	}
	var copyResp struct {
		Copied    map[string]string `json:"copied"`
		NotCopied map[string]struct {
			Type string `json:"type"`
		} `json:"notCopied"`
	}
	if err := json.Unmarshal(r.Args, &copyResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if copyResp.Copied[ref.Hash] != ref.Hash {
		t.Fatalf("expected existing blob in copied")
	}
	if copyResp.NotCopied[missingHash].Type != "blobNotFound" {
		t.Fatalf("expected missing blob in notCopied, got %v", copyResp.NotCopied)
	}
}
