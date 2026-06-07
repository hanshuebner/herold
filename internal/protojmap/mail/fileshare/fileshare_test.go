package fileshare

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

const testPublicBase = "https://mail.example.test"

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := storesqlite.Open(context.Background(),
		filepath.Join(t.TempDir(), "store.db"), nil,
		clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func defaultCfg() store.FileSharesConfig {
	return store.FileSharesConfig{
		DefaultTTL:             30 * 24 * time.Hour,
		MaxTTL:                 90 * 24 * time.Hour,
		PendingTTL:             time.Hour,
		RevokedGrace:           24 * time.Hour,
		MaxSharesPerPrincipal:  1000,
		ShareQuotaPerPrincipal: 5 * 1024 * 1024 * 1024,
	}
}

func newHandlers(t *testing.T) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	st := newStore(t)
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	h := &handlerSet{
		store:         st,
		clk:           clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:           defaultCfg(),
		publicBaseURL: testPublicBase,
	}
	return h, st, p
}

func accountID(p store.Principal) string {
	return string(protojmap.AccountIDForPrincipal(p.ID))
}

// uploadBlob stores bytes in the blob store and returns the blob hash.
func uploadBlob(t *testing.T, st store.Store, data []byte) string {
	t.Helper()
	ref, err := st.Blobs().Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	return ref.Hash
}

// -- FileShare/get --------------------------------------------------

func TestGet_EmptyList(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	r, merr := getHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	resp := r.(getResponse)
	if len(resp.List) != 0 {
		t.Fatalf("expected empty list, got %+v", resp.List)
	}
	if resp.State == "" {
		t.Fatalf("state must be non-empty")
	}
	if resp.AccountID != accountID(p) {
		t.Fatalf("accountId mismatch: %q", resp.AccountID)
	}
}

func TestGet_RejectsForeignAccount(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{"accountId": "not-mine"})
	_, merr := getHandler{h: h}.executeAs(p, args)
	if merr == nil || merr.Type != "accountNotFound" {
		t.Fatalf("expected accountNotFound, got %v", merr)
	}
}

// -- FileShare/set create ------------------------------------------

func TestCreate_BasicFlow(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("hello share"))

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "hello.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set create: %v", merr)
	}
	resp := r.(setResponse)
	if len(resp.NotCreated) != 0 {
		t.Fatalf("unexpected notCreated: %v", resp.NotCreated)
	}
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not in Created")
	}
	if created.ID == "" {
		t.Fatalf("created.ID must not be empty")
	}
	if created.URL != testPublicBase+"/share/"+created.ID {
		t.Fatalf("unexpected URL: %q", created.URL)
	}
	if created.State != "pending" {
		t.Fatalf("new share must be pending, got %q", created.State)
	}
	if created.HasPassword {
		t.Fatalf("hasPassword must be false when no password set")
	}
	if created.BlobID != blobHash {
		t.Fatalf("blobId mismatch: %q", created.BlobID)
	}
	if created.Name != "hello.txt" {
		t.Fatalf("name mismatch: %q", created.Name)
	}
}

func TestCreate_WithPassword(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("secret data"))

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "secret.bin",
				"type":      "application/octet-stream",
				"expiresIn": 3600,
				"password":  "hunter2",
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set create: %v", merr)
	}
	resp := r.(setResponse)
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not in Created")
	}
	// REQ-SHARE-43: password must not be serialised; hasPassword=true.
	if !created.HasPassword {
		t.Fatalf("hasPassword must be true when password set")
	}
}

func TestCreate_MissingBlobID(t *testing.T) {
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"name":      "foo.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
}

func TestCreate_ForeignBlobRejected(t *testing.T) {
	// A blobId that does not exist in the blob store must be rejected
	// with "forbidden" (REQ-SHARE-41).
	h, _, p := newHandlers(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    "0000000000000000000000000000000000000000000000000000000000000000",
				"name":      "nope.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	e, ok := resp.NotCreated["c1"]
	if !ok {
		t.Fatalf("expected notCreated[c1]")
	}
	if e.Type != "forbidden" {
		t.Fatalf("expected forbidden, got %q", e.Type)
	}
}

func TestCreate_ExpiresInClampedToMaxTTL(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("data"))

	// expiresIn bigger than MaxTTL (90 days = 7776000s). Use 200 days.
	args, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "big.bin",
				"type":      "application/octet-stream",
				"expiresIn": 200 * 24 * 3600,
			},
		},
	})
	r, merr := setHandler{h: h}.executeAs(p, args)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := r.(setResponse)
	created, ok := resp.Created["c1"]
	if !ok {
		t.Fatalf("c1 not in Created")
	}
	// The ExpiresAt in the created share must not be more than
	// PendingTTL from the fake clock (2026-01-01) since newly created
	// shares start as pending with expires_at = now + PendingTTL.
	expiry, err := time.Parse("2006-01-02T15:04:05Z", created.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expiresAt: %v", err)
	}
	fakeNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	maxAllowed := fakeNow.Add(defaultCfg().PendingTTL + time.Minute)
	if expiry.After(maxAllowed) {
		t.Fatalf("expiresAt %v is beyond pending_ttl ceiling %v", expiry, maxAllowed)
	}
}

// -- FileShare/set update (confirm) --------------------------------

func TestUpdate_Confirm(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("confirm me"))

	// Create a pending share.
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "doc.pdf",
				"type":      "application/pdf",
				"expiresIn": 3600,
			},
		},
	})
	cr, merr := setHandler{h: h}.executeAs(p, createArgs)
	if merr != nil {
		t.Fatalf("create: %v", merr)
	}
	shareID := cr.(setResponse).Created["c1"].ID

	// Confirm it.
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			shareID: map[string]any{"state": "active"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, updateArgs)
	if merr != nil {
		t.Fatalf("confirm: %v", merr)
	}
	resp := ur.(setResponse)
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("unexpected notUpdated: %v", resp.NotUpdated)
	}
	updated, ok := resp.Updated[shareID]
	if !ok {
		t.Fatalf("share not in Updated")
	}
	if updated.State != "active" {
		t.Fatalf("expected active, got %q", updated.State)
	}
}

// TestUpdate_ShortenExpiry exercises the REQ-SHARE-42 shorten-expiry path
// end to end through the store's UpdateFileShareExpiry.
func TestUpdate_ShortenExpiry(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("shorten"))

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{"blobId": blobHash, "name": "a.zip", "type": "application/zip", "expiresIn": 3600},
		},
	})
	cr, merr := setHandler{h: h}.executeAs(p, createArgs)
	if merr != nil {
		t.Fatalf("create: %v", merr)
	}
	shareID := cr.(setResponse).Created["c1"].ID

	// Confirm to active so it carries the full expiry.
	confirmArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{shareID: map[string]any{"state": "active"}},
	})
	cur, merr := setHandler{h: h}.executeAs(p, confirmArgs)
	if merr != nil {
		t.Fatalf("confirm: %v", merr)
	}
	active := cur.(setResponse).Updated[shareID]
	activeExpiry, err := time.Parse("2006-01-02T15:04:05Z", active.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expiry: %v", err)
	}
	shorter := activeExpiry.Add(-30 * time.Minute).UTC().Format("2006-01-02T15:04:05Z")

	updArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{shareID: map[string]any{"expiresAt": shorter}},
	})
	ur, merr := setHandler{h: h}.executeAs(p, updArgs)
	if merr != nil {
		t.Fatalf("shorten: %v", merr)
	}
	resp := ur.(setResponse)
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("unexpected notUpdated: %v", resp.NotUpdated)
	}
	u, ok := resp.Updated[shareID]
	if !ok {
		t.Fatalf("share not in Updated")
	}
	if u.ExpiresAt != shorter {
		t.Fatalf("expiresAt = %q, want %q", u.ExpiresAt, shorter)
	}
}

// TestUpdate_LowerMaxDownloads exercises the REQ-SHARE-42 lower-cap path
// end to end through the store's UpdateFileShareMaxDownloads.
func TestUpdate_LowerMaxDownloads(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("lower cap"))

	five := 5
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{"blobId": blobHash, "name": "a.zip", "type": "application/zip", "expiresIn": 3600, "maxDownloads": five},
		},
	})
	cr, merr := setHandler{h: h}.executeAs(p, createArgs)
	if merr != nil {
		t.Fatalf("create: %v", merr)
	}
	shareID := cr.(setResponse).Created["c1"].ID

	updArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{shareID: map[string]any{"maxDownloads": 2}},
	})
	ur, merr := setHandler{h: h}.executeAs(p, updArgs)
	if merr != nil {
		t.Fatalf("lower: %v", merr)
	}
	resp := ur.(setResponse)
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("unexpected notUpdated: %v", resp.NotUpdated)
	}
	u, ok := resp.Updated[shareID]
	if !ok {
		t.Fatalf("share not in Updated")
	}
	if u.MaxDownloads == nil || *u.MaxDownloads != 2 {
		t.Fatalf("maxDownloads = %v, want 2", u.MaxDownloads)
	}

	// Raising it again must be rejected (REQ-SHARE-42).
	raiseArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{shareID: map[string]any{"maxDownloads": 9}},
	})
	rr, merr := setHandler{h: h}.executeAs(p, raiseArgs)
	if merr != nil {
		t.Fatalf("raise: %v", merr)
	}
	if _, bad := rr.(setResponse).Updated[shareID]; bad {
		t.Fatalf("raising maxDownloads should not succeed")
	}
	if _, ok := rr.(setResponse).NotUpdated[shareID]; !ok {
		t.Fatalf("expected raise to be rejected in NotUpdated")
	}
}

func TestUpdate_RejectImmutableFields(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("immutable"))

	// Create a share.
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "file.bin",
				"type":      "application/octet-stream",
				"expiresIn": 3600,
			},
		},
	})
	cr, merr := setHandler{h: h}.executeAs(p, createArgs)
	if merr != nil {
		t.Fatalf("create: %v", merr)
	}
	shareID := cr.(setResponse).Created["c1"].ID

	// Attempt to mutate blobId (REQ-SHARE-42).
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			shareID: map[string]any{"blobId": "deadbeef"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, updateArgs)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := ur.(setResponse)
	e, ok := resp.NotUpdated[shareID]
	if !ok {
		t.Fatalf("expected notUpdated[shareID]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
}

// TestUpdate_Revoke exercises the REQ-SHARE-22 revoke path: update
// state to "revoked" marks the share revoked (the row survives so the
// management view can show it) and the public surface then 410s.
func TestUpdate_Revoke(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("revoke me"))

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{"blobId": blobHash, "name": "x.txt", "type": "text/plain", "expiresIn": 3600},
		},
	})
	cr, _ := setHandler{h: h}.executeAs(p, createArgs)
	shareID := cr.(setResponse).Created["c1"].ID

	// Confirm to active, then revoke.
	confirmArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{shareID: map[string]any{"state": "active"}},
	})
	if _, merr := (setHandler{h: h}).executeAs(p, confirmArgs); merr != nil {
		t.Fatalf("confirm: %v", merr)
	}
	revokeArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update":    map[string]any{shareID: map[string]any{"state": "revoked"}},
	})
	ur, merr := setHandler{h: h}.executeAs(p, revokeArgs)
	if merr != nil {
		t.Fatalf("revoke: %v", merr)
	}
	resp := ur.(setResponse)
	if len(resp.NotUpdated) != 0 {
		t.Fatalf("unexpected notUpdated: %v", resp.NotUpdated)
	}
	u, ok := resp.Updated[shareID]
	if !ok {
		t.Fatalf("share not in Updated")
	}
	if u.State != "revoked" {
		t.Fatalf("state = %q, want revoked", u.State)
	}
}

func TestUpdate_RejectInvalidState(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("state test"))

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "x.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	cr, _ := setHandler{h: h}.executeAs(p, createArgs)
	shareID := cr.(setResponse).Created["c1"].ID

	// "pending" is not a valid update target — only "active" (confirm)
	// and "revoked" are accepted.
	updateArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			shareID: map[string]any{"state": "pending"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, updateArgs)
	if merr != nil {
		t.Fatalf("set: %v", merr)
	}
	resp := ur.(setResponse)
	e, ok := resp.NotUpdated[shareID]
	if !ok {
		t.Fatalf("expected notUpdated[shareID]")
	}
	if e.Type != "invalidProperties" {
		t.Fatalf("expected invalidProperties, got %q", e.Type)
	}
}

// -- FileShare/set destroy -----------------------------------------

func TestDestroy_Basic(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("destroy me"))

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "bye.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	cr, _ := setHandler{h: h}.executeAs(p, createArgs)
	shareID := cr.(setResponse).Created["c1"].ID

	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{shareID},
	})
	dr, merr := setHandler{h: h}.executeAs(p, destroyArgs)
	if merr != nil {
		t.Fatalf("destroy: %v", merr)
	}
	resp := dr.(setResponse)
	if len(resp.NotDestroyed) != 0 {
		t.Fatalf("unexpected notDestroyed: %v", resp.NotDestroyed)
	}
	if len(resp.Destroyed) != 1 || resp.Destroyed[0] != shareID {
		t.Fatalf("expected Destroyed[%q], got %v", shareID, resp.Destroyed)
	}

	// Verify it is gone from /get.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ids":       []string{shareID},
	})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	gr2 := gr.(getResponse)
	if len(gr2.NotFound) != 1 || gr2.NotFound[0] != shareID {
		t.Fatalf("expected NotFound after destroy, got %+v", gr2)
	}
}

func TestDestroy_NotFound(t *testing.T) {
	h, _, p := newHandlers(t)
	destroyArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"destroy":   []string{"nonexistent-id"},
	})
	dr, merr := setHandler{h: h}.executeAs(p, destroyArgs)
	if merr != nil {
		t.Fatalf("destroy: %v", merr)
	}
	resp := dr.(setResponse)
	e, ok := resp.NotDestroyed["nonexistent-id"]
	if !ok {
		t.Fatalf("expected notDestroyed[nonexistent-id]")
	}
	if e.Type != "notFound" {
		t.Fatalf("expected notFound, got %q", e.Type)
	}
}

// -- Password write-only (REQ-SHARE-43) ----------------------------

func TestPasswordWriteOnly(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("password test"))

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "locked.bin",
				"type":      "application/octet-stream",
				"expiresIn": 3600,
				"password":  "s3cr3t",
			},
		},
	})
	cr, merr := setHandler{h: h}.executeAs(p, createArgs)
	if merr != nil {
		t.Fatalf("create: %v", merr)
	}
	createdShare := cr.(setResponse).Created["c1"]
	shareID := createdShare.ID

	// hasPassword must be true.
	if !createdShare.HasPassword {
		t.Fatalf("hasPassword must be true")
	}

	// The wire-form must not contain passwordHash or password fields.
	wire, _ := json.Marshal(createdShare)
	wireStr := string(wire)
	if containsKey(t, wireStr, "passwordHash") {
		t.Fatalf("passwordHash must never appear in wire output")
	}
	if containsKey(t, wireStr, `"password"`) {
		t.Fatalf("password must never appear in wire output")
	}

	// Verify /get also returns hasPassword=true, no raw hash.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ids":       []string{shareID},
	})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	got := gr.(getResponse).List[0]
	if !got.HasPassword {
		t.Fatalf("/get: hasPassword must be true")
	}
	wire2, _ := json.Marshal(got)
	if containsKey(t, string(wire2), "passwordHash") {
		t.Fatalf("/get: passwordHash must never appear in wire output")
	}
}

func containsKey(t *testing.T, wireStr, key string) bool {
	t.Helper()
	return len(wireStr) > 0 && indexOf(wireStr, key) >= 0
}

func indexOf(s, sub string) int {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// -- FileShare/changes --------------------------------------------

func TestChanges_NoChange(t *testing.T) {
	h, _, p := newHandlers(t)

	// Get current state.
	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	currentState := gr.(getResponse).State

	// /changes with sinceState == current should return empty lists.
	changesArgs, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": currentState,
	})
	cr, merr := changesHandler{h: h}.executeAs(p, changesArgs)
	if merr != nil {
		t.Fatalf("changes: %v", merr)
	}
	resp := cr.(changesResponse)
	if len(resp.Created) != 0 || len(resp.Updated) != 0 || len(resp.Destroyed) != 0 {
		t.Fatalf("expected empty changes, got %+v", resp)
	}
	if resp.NewState != currentState {
		t.Fatalf("newState should equal sinceState when unchanged")
	}
}

func TestChanges_AfterCreate(t *testing.T) {
	h, st, p := newHandlers(t)

	// Capture state before create.
	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	before := gr.(getResponse).State

	blobHash := uploadBlob(t, st, []byte("changes test"))
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "changes.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	setHandler{h: h}.executeAs(p, createArgs) //nolint:errcheck

	// /changes from before should report a diff.
	changesArgs, _ := json.Marshal(map[string]any{
		"accountId":  accountID(p),
		"sinceState": before,
	})
	cr, merr := changesHandler{h: h}.executeAs(p, changesArgs)
	if merr != nil {
		t.Fatalf("changes: %v", merr)
	}
	resp := cr.(changesResponse)
	if resp.NewState == before {
		t.Fatalf("state did not advance after create")
	}
}

// -- FileShare/query ----------------------------------------------

func TestQuery_FilterByState(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("query filter"))

	// Create two pending shares.
	for i := 0; i < 2; i++ {
		createArgs, _ := json.Marshal(map[string]any{
			"accountId": accountID(p),
			"create": map[string]any{
				"c1": map[string]any{
					"blobId":    blobHash,
					"name":      "q.txt",
					"type":      "text/plain",
					"expiresIn": 3600,
				},
			},
		})
		setHandler{h: h}.executeAs(p, createArgs) //nolint:errcheck
	}

	// Query with state=pending filter.
	queryArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"filter":    map[string]any{"state": "pending"},
	})
	qr, merr := queryHandler{h: h}.executeAs(p, queryArgs)
	if merr != nil {
		t.Fatalf("query: %v", merr)
	}
	resp := qr.(queryResponse)
	if len(resp.IDs) != 2 {
		t.Fatalf("expected 2 pending shares, got %d", len(resp.IDs))
	}

	// Query with state=active filter (none exist).
	queryActiveArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"filter":    map[string]any{"state": "active"},
	})
	qar, _ := queryHandler{h: h}.executeAs(p, queryActiveArgs)
	if len(qar.(queryResponse).IDs) != 0 {
		t.Fatalf("expected 0 active shares")
	}
}

func TestQuery_InvalidSortRejected(t *testing.T) {
	h, _, p := newHandlers(t)
	queryArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"sort":      []map[string]any{{"property": "name"}},
	})
	_, merr := queryHandler{h: h}.executeAs(p, queryArgs)
	if merr == nil || merr.Type != "unsupportedSort" {
		t.Fatalf("expected unsupportedSort, got %v", merr)
	}
}

func TestQuery_InvalidFilterStateRejected(t *testing.T) {
	h, _, p := newHandlers(t)
	queryArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"filter":    map[string]any{"state": "unknown-state"},
	})
	_, merr := queryHandler{h: h}.executeAs(p, queryArgs)
	if merr == nil || merr.Type != "invalidArguments" {
		t.Fatalf("expected invalidArguments, got %v", merr)
	}
}

// -- State counter wiring -----------------------------------------

func TestStateAdvancesOnMutation(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("state advance"))

	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	state0 := gr.(getResponse).State

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "adv.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	setHandler{h: h}.executeAs(p, createArgs) //nolint:errcheck

	gr2, _ := getHandler{h: h}.executeAs(p, getArgs)
	state1 := gr2.(getResponse).State
	if state1 == state0 {
		t.Fatalf("state did not advance after create: %q == %q", state0, state1)
	}
}

// -- Round-trip: create + confirm + get ---------------------------

func TestRoundTrip_CreateConfirmGet(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("round trip bytes"))

	// Create (pending).
	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "rt.bin",
				"type":      "application/octet-stream",
				"expiresIn": 3600,
			},
		},
	})
	cr, merr := setHandler{h: h}.executeAs(p, createArgs)
	if merr != nil {
		t.Fatalf("create: %v", merr)
	}
	shareID := cr.(setResponse).Created["c1"].ID

	// Confirm (active).
	confirmArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"update": map[string]any{
			shareID: map[string]any{"state": "active"},
		},
	})
	ur, merr := setHandler{h: h}.executeAs(p, confirmArgs)
	if merr != nil {
		t.Fatalf("confirm: %v", merr)
	}
	if _, bad := ur.(setResponse).NotUpdated[shareID]; bad {
		t.Fatalf("confirm failed: %v", ur.(setResponse).NotUpdated[shareID])
	}

	// /get should return state=active.
	getArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"ids":       []string{shareID},
	})
	gr, merr := getHandler{h: h}.executeAs(p, getArgs)
	if merr != nil {
		t.Fatalf("get: %v", merr)
	}
	got := gr.(getResponse)
	if len(got.List) != 1 {
		t.Fatalf("expected 1 share, got %d", len(got.List))
	}
	if got.List[0].State != "active" {
		t.Fatalf("expected active, got %q", got.List[0].State)
	}
	if got.List[0].BlobID != blobHash {
		t.Fatalf("blobId mismatch: %q != %q", got.List[0].BlobID, blobHash)
	}
}

// -- Revoke via store (not JMAP but verify /get reflects it) -----

func TestRevokedShareAppearsInGet(t *testing.T) {
	h, st, p := newHandlers(t)
	blobHash := uploadBlob(t, st, []byte("revoke test"))

	createArgs, _ := json.Marshal(map[string]any{
		"accountId": accountID(p),
		"create": map[string]any{
			"c1": map[string]any{
				"blobId":    blobHash,
				"name":      "rev.txt",
				"type":      "text/plain",
				"expiresIn": 3600,
			},
		},
	})
	cr, _ := setHandler{h: h}.executeAs(p, createArgs)
	shareID := cr.(setResponse).Created["c1"].ID

	// Revoke via store directly (simulating admin revocation).
	if err := st.Meta().RevokeFileShare(context.Background(), p.ID, shareID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// /get with no ids should include the revoked share.
	getArgs, _ := json.Marshal(map[string]any{"accountId": accountID(p)})
	gr, _ := getHandler{h: h}.executeAs(p, getArgs)
	resp := gr.(getResponse)
	found := false
	for _, fs := range resp.List {
		if fs.ID == shareID {
			if fs.State != "revoked" {
				t.Fatalf("expected revoked state, got %q", fs.State)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("revoked share not returned in /get list")
	}
}
