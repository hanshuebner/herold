package email_test

// bodymeta_test.go -- tests for the body-metadata fast-path optimisation.
//
// Deliverable 7 requirements:
//   - Zero-blob test: Email/get for EMAIL_LIST_PROPERTIES on messages with
//     BodyMetaComputed=true must call Blobs.Get zero times.
//   - Fallback test: an uncomputed message requesting preview falls back to
//     the blob, returns a correct preview, AND persists the meta (next call
//     is zero-blob).
//   - Property projection test: a properties:["id","preview"] request does
//     not include bodyValues/textBody/htmlBody/bodyStructure/attachments.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/email"
	"github.com/hanshuebner/herold/internal/protojmap/mail/mailbox"
	"github.com/hanshuebner/herold/internal/protojmap/mail/thread"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness"
)

// emailListProperties mirrors the suite's EMAIL_LIST_PROPERTIES constant.
var emailListProperties = []string{
	"id", "threadId", "mailboxIds", "keywords", "from", "to", "subject",
	"preview", "receivedAt", "hasAttachment", "snoozedUntil", "internalizePending",
}

// countingBlobs wraps a store.Blobs and atomically increments getCount on
// every Get call. Used to assert that the zero-blob fast path is taken.
type countingBlobs struct {
	inner    store.Blobs
	getCount atomic.Int64
}

func (c *countingBlobs) Put(ctx context.Context, r io.Reader) (store.BlobRef, error) {
	return c.inner.Put(ctx, r)
}

func (c *countingBlobs) Get(ctx context.Context, hash string) (io.ReadCloser, error) {
	c.getCount.Add(1)
	return c.inner.Get(ctx, hash)
}

func (c *countingBlobs) Stat(ctx context.Context, hash string) (int64, int, error) {
	return c.inner.Stat(ctx, hash)
}

func (c *countingBlobs) Delete(ctx context.Context, hash string) error {
	return c.inner.Delete(ctx, hash)
}

// countingStore wraps a store.Store and substitutes a countingBlobs.
type countingStore struct {
	store.Store
	blobs *countingBlobs
}

func (c *countingStore) Blobs() store.Blobs { return c.blobs }

// setupBodyMetaFixture returns a fixture backed by a real SQLite store, plus a
// countingStore that wraps it so tests can assert blob-get call counts. The
// returned JMAP server uses the countingStore so its Blobs() calls are
// instrumented.
func setupBodyMetaFixture(t *testing.T) (*fixture, *countingStore) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.Open(context.Background(), dbPath, nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cs := &countingStore{
		Store: st,
		blobs: &countingBlobs{inner: st.Blobs()},
	}

	srv, _ := testharness.Start(t, testharness.Options{
		Store:     cs,
		Clock:     clk,
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
	plaintext := "hk_bm_alice_" + fmt.Sprintf("%d", p.ID)
	hash := protoadmin.HashAPIKey(plaintext)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	inboxMB, err := srv.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID,
		Name:        "INBOX",
		Attributes:  store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(cs, dir, nil, srv.Logger, srv.Clock, protojmap.Options{})
	mailbox.Register(jmapServ.Registry(), cs, srv.Logger, srv.Clock)
	email.Register(jmapServ.Registry(), cs, srv.Logger, srv.Clock)
	thread.Register(jmapServ.Registry(), cs, srv.Logger, srv.Clock)

	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	f := &fixture{
		srv:     srv,
		pid:     p.ID,
		inbox:   inboxMB,
		client:  client,
		baseURL: base,
		apiKey:  plaintext,
	}
	return f, cs
}

// TestEmailGet_ZeroBlob_WithBodyMetaComputed asserts that Email/get for
// EMAIL_LIST_PROPERTIES on messages with BodyMetaComputed=true does NOT call
// Blobs.Get at all. This is the production perf fix: opening a 24-message
// inbox must not decode any body blobs.
func TestEmailGet_ZeroBlob_WithBodyMetaComputed(t *testing.T) {
	f, cs := setupBodyMetaFixture(t)
	ctx := context.Background()

	// Insert several messages and mark them with precomputed body meta.
	const n = 5
	msgBody := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Test\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello, this is a test message body."
	var ids []string
	for i := 0; i < n; i++ {
		m := f.insertMessage(t, msgBody, fmt.Sprintf("Subject %d", i), "sender@example.test", "rcpt@example.test", nil, "")
		// Mark body meta as computed.
		if err := f.srv.Store.Meta().SetMessageBodyMeta(ctx, m.ID, "Hello, this is a test message body.", false); err != nil {
			t.Fatalf("SetMessageBodyMeta: %v", err)
		}
		ids = append(ids, fmt.Sprintf("%d", m.ID))
	}

	// Reset the blob-get counter after the insert/setup phase
	// (insertMessage calls Blobs.Put which may internally call Get for
	// dedup; we want to measure only the Email/get calls).
	cs.blobs.getCount.Store(0)

	// Issue Email/get with EMAIL_LIST_PROPERTIES.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        ids,
		"properties": emailListProperties,
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != n {
		t.Fatalf("want %d messages, got %d: %s", n, len(resp.List), raw)
	}

	// The core assertion: zero blob reads.
	if got := cs.blobs.getCount.Load(); got != 0 {
		t.Errorf("Blobs.Get called %d times for %d precomputed messages; want 0", got, n)
	}

	// Sanity: preview and hasAttachment must be populated.
	for _, m := range resp.List {
		if _, ok := m["preview"]; !ok {
			t.Errorf("preview missing from response: %v", m)
		}
		if _, ok := m["hasAttachment"]; !ok {
			t.Errorf("hasAttachment missing from response: %v", m)
		}
	}
}

// TestEmailGet_FallbackAndPersist asserts that an uncomputed message requesting
// preview falls back to the blob, returns a correct preview, and persists the
// meta so the NEXT call is zero-blob.
func TestEmailGet_FallbackAndPersist(t *testing.T) {
	f, cs := setupBodyMetaFixture(t)
	ctx := context.Background()

	msgBody := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Fallback\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nFallback preview text."
	m := f.insertMessage(t, msgBody, "Fallback", "sender@example.test", "rcpt@example.test", nil, "")
	msgID := fmt.Sprintf("%d", m.ID)

	// Verify that BodyMetaComputed is false initially.
	fresh, err := f.srv.Store.Meta().GetMessage(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if fresh.BodyMetaComputed {
		t.Fatal("expected BodyMetaComputed=false after insert")
	}

	// Reset counter.
	cs.blobs.getCount.Store(0)

	// First Email/get: must fall back to blob.
	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{msgID},
		"properties": emailListProperties,
	})
	var resp1 struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp1); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp1.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(resp1.List), raw)
	}

	// Should have read the blob.
	if got := cs.blobs.getCount.Load(); got == 0 {
		t.Error("expected at least one Blobs.Get call for uncomputed message")
	}

	// Preview must be correct.
	preview, _ := resp1.List[0]["preview"].(string)
	if preview != "Fallback preview text." {
		t.Errorf("preview = %q, want %q", preview, "Fallback preview text.")
	}

	// The opportunistic persist should have set BodyMetaComputed=true.
	// Give the write a moment by checking the store directly.
	persisted, err := f.srv.Store.Meta().GetMessage(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMessage after first Email/get: %v", err)
	}
	if !persisted.BodyMetaComputed {
		t.Error("expected BodyMetaComputed=true after first Email/get (opportunistic persist)")
	}
	if persisted.Preview != "Fallback preview text." {
		t.Errorf("persisted preview = %q, want %q", persisted.Preview, "Fallback preview text.")
	}

	// Second Email/get: must be zero-blob because BodyMetaComputed is now true.
	cs.blobs.getCount.Store(0)
	_, raw2 := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{msgID},
		"properties": emailListProperties,
	})
	var resp2 struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw2, &resp2); err != nil {
		t.Fatalf("unmarshal second call: %v: %s", err, raw2)
	}
	if len(resp2.List) != 1 {
		t.Fatalf("want 1 message on second call, got %d: %s", len(resp2.List), raw2)
	}
	if got := cs.blobs.getCount.Load(); got != 0 {
		t.Errorf("second call: Blobs.Get called %d times; want 0 (metadata should be cached)", got)
	}
	// Preview must still be correct.
	preview2, _ := resp2.List[0]["preview"].(string)
	if preview2 != "Fallback preview text." {
		t.Errorf("second call preview = %q, want %q", preview2, "Fallback preview text.")
	}
}

// TestEmailGet_PropertyProjection asserts that a properties:["id","preview"]
// request does NOT include bodyValues/textBody/htmlBody/bodyStructure/attachments
// in the marshalled output (Deliverable 4 -- wire amplification fix).
func TestEmailGet_PropertyProjection(t *testing.T) {
	f, cs := setupBodyMetaFixture(t)
	ctx := context.Background()

	// Use a multipart message so bodyValues/textBody/etc. would be non-empty if
	// they were mistakenly populated.
	msgBody := "From: sender@example.test\r\nTo: rcpt@example.test\r\nSubject: Projection\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nText body here.\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"x.pdf\"\r\n\r\nPDF\r\n--b--\r\n"
	m := f.insertMessage(t, msgBody, "Projection", "sender@example.test", "rcpt@example.test", nil, "")
	// Do NOT set BodyMetaComputed so we go through the blob path.
	_ = ctx

	_, raw := f.invoke(t, "Email/get", map[string]any{
		"accountId":  protojmap.AccountIDForPrincipal(f.pid),
		"ids":        []string{fmt.Sprintf("%d", m.ID)},
		"properties": []string{"id", "preview"},
	})
	var resp struct {
		List []json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(resp.List) != 1 {
		t.Fatalf("want 1 message, got %d: %s", len(resp.List), raw)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(resp.List[0], &obj); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}

	// These body-only keys must not appear at all.
	forbidden := []string{"bodyValues", "textBody", "htmlBody", "bodyStructure", "attachments"}
	for _, key := range forbidden {
		if _, present := obj[key]; present {
			t.Errorf("key %q must not appear in properties:[id,preview] response; got: %s", key, resp.List[0])
		}
	}

	// id and preview must be present.
	if _, ok := obj["id"]; !ok {
		t.Error("id must be present")
	}
	if _, ok := obj["preview"]; !ok {
		t.Error("preview must be present")
	}
	// cs is used in this test so the compiler does not complain.
	_ = cs
}
