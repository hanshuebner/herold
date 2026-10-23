package protojmap_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// recipientLookupFailStore wraps a store so FirstReceivedToForBlob
// fails, modelling a transient metadata error while a download is
// served.
type recipientLookupFailStore struct{ store.Store }

func (s recipientLookupFailStore) Meta() store.Metadata {
	return recipientLookupFailMeta{s.Store.Meta()}
}

type recipientLookupFailMeta struct{ store.Metadata }

func (recipientLookupFailMeta) FirstReceivedToForBlob(context.Context, store.PrincipalID, string) (string, error) {
	return "", errors.New("injected lookup failure")
}

// cacheServer starts a JMAP server over st with opts and returns a GET
// helper. The helper sends an optional If-None-Match.
func cacheServer(t *testing.T, f *texpFixture, st store.Store, opts protojmap.Options) func(key, account, blobID, ifNoneMatch string) (*http.Response, []byte) {
	t.Helper()
	if opts.DownloadRatePerSec == 0 {
		opts.DownloadRatePerSec = -1
	}
	dir := directory.New(st.Meta(), nil, f.clk, nil)
	srv := protojmap.NewServer(st, dir, nil, nil, f.clk, opts)
	httpd := httptest.NewServer(srv.Handler())
	t.Cleanup(httpd.Close)
	return func(key, account, blobID, ifNoneMatch string) (*http.Response, []byte) {
		t.Helper()
		url := fmt.Sprintf("%s/jmap/download/%s/%s/application%%2Foctet-stream/f.bin",
			httpd.URL, account, strings.ReplaceAll(blobID, "/", "%2F"))
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		res, err := httpd.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return res, body
	}
}

// multipartRaw is a two-part message whose second part is the
// attachment addressed as "<hash>/p3".
func multipartRaw(tag string) []byte {
	return []byte("Subject: " + tag + "\r\nMessage-ID: <" + tag + "@cache.test>\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--B\r\nContent-Type: image/png\r\n\r\nPNGDATA-PNGDATA-PNGDATA\r\n--B--\r\n")
}

// insertReceived stores a message for alice with the given received_to.
func (f *texpFixture) insertReceived(raw []byte, receivedTo string) (hash string) {
	f.t.Helper()
	ctx := context.Background()
	ref, err := f.store.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		f.t.Fatalf("Blobs().Put: %v", err)
	}
	f.seq++
	at := f.clk.Now()
	msg := store.Message{
		PrincipalID:  f.alice,
		InternalDate: at,
		ReceivedAt:   at,
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     store.Envelope{Subject: "s", MessageID: fmt.Sprintf("<cache-%d-%d@test>", f.alice, f.seq)},
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, msg,
		[]store.MessageMailbox{{MailboxID: f.inbox, ReceivedTo: receivedTo}}); err != nil {
		f.t.Fatalf("InsertMessage: %v", err)
	}
	return ref.Hash
}

func wantCacheHeaders(t *testing.T, res *http.Response, blobID, wantPolicy string) {
	t.Helper()
	if got := res.Header.Get("ETag"); got != `"`+blobID+`"` {
		t.Fatalf("ETag = %q, want %q", got, `"`+blobID+`"`)
	}
	if got := res.Header.Get("Cache-Control"); got != wantPolicy {
		t.Fatalf("Cache-Control = %q, want %q", got, wantPolicy)
	}
}

func wantNoCacheHeaders(t *testing.T, res *http.Response) {
	t.Helper()
	if v := res.Header.Get("ETag"); v != "" {
		t.Fatalf("ETag = %q, want none", v)
	}
	if v := res.Header.Get("Cache-Control"); v != "" {
		t.Fatalf("Cache-Control = %q, want none", v)
	}
}

const ownerPolicy = "private, max-age=900, immutable"
const chatPolicy = "private, max-age=300"

func TestDownloadCache_WholeBlob_200Then304(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		raw := multipartRaw("whole")
		hash := f.insertReceived(raw, "")
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)

		res, body := get(f.aliceKey, acct, hash, "")
		if res.StatusCode != http.StatusOK || !bytes.Equal(body, raw) {
			t.Fatalf("status = %d body = %q", res.StatusCode, body)
		}
		wantCacheHeaders(t, res, hash, ownerPolicy)

		res, body = get(f.aliceKey, acct, hash, `"`+hash+`"`)
		if res.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("conditional status = %d body = %q, want 304 empty", res.StatusCode, body)
		}
		wantCacheHeaders(t, res, hash, ownerPolicy)

		// A validator for another blob does not match.
		res, _ = get(f.aliceKey, acct, hash, `"other"`)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("non-matching validator status = %d, want 200", res.StatusCode)
		}
	})
}

func TestDownloadCache_PartBlob_200Then304(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		hash := f.insertReceived(multipartRaw("part"), "")
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)
		id := hash + "/p3"

		res, body := get(f.aliceKey, acct, id, "")
		if res.StatusCode != http.StatusOK || string(body) != "PNGDATA-PNGDATA-PNGDATA" {
			t.Fatalf("status = %d body = %q", res.StatusCode, body)
		}
		wantCacheHeaders(t, res, id, ownerPolicy)

		res, body = get(f.aliceKey, acct, id, `"`+id+`"`)
		if res.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("conditional status = %d body = %q, want 304 empty", res.StatusCode, body)
		}
		wantCacheHeaders(t, res, id, ownerPolicy)
	})
}

// A part of a message that received the per-recipient header is still
// a pure function of the hash: the header goes on whole blobs only.
func TestDownloadCache_PartBlob_OfInjectedMessage_Cacheable(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		hash := f.insertReceived(multipartRaw("partinj"), "rcpt@example.test")
		get := cacheServer(t, f, f.store, protojmap.Options{})
		res, _ := get(f.aliceKey, protojmap.AccountIDForPrincipal(f.alice), hash+"/p3", "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", res.StatusCode)
		}
		wantCacheHeaders(t, res, hash+"/p3", ownerPolicy)
	})
}

func TestDownloadCache_HeaderInjection_NotCached(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		raw := multipartRaw("inject")
		hash := f.insertReceived(raw, "rcpt@example.test")
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)

		res, body := get(f.aliceKey, acct, hash, "")
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "rcpt@example.test") {
			t.Fatalf("status = %d, body lacks injected header: %q", res.StatusCode, body)
		}
		wantNoCacheHeaders(t, res)

		// A matching validator never short-circuits an injected blob.
		res, body = get(f.aliceKey, acct, hash, `"`+hash+`"`)
		if res.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("conditional status = %d, want 200 with body", res.StatusCode)
		}
		wantNoCacheHeaders(t, res)
	})
}

func TestDownloadCache_RecipientLookupFails_NotCached(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		raw := multipartRaw("lookupfail")
		hash := f.insertReceived(raw, "rcpt@example.test")
		get := cacheServer(t, f, recipientLookupFailStore{f.store}, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)

		res, body := get(f.aliceKey, acct, hash, "")
		if res.StatusCode != http.StatusOK || !bytes.Equal(body, raw) {
			t.Fatalf("status = %d body = %q", res.StatusCode, body)
		}
		wantNoCacheHeaders(t, res)

		res, _ = get(f.aliceKey, acct, hash, `"`+hash+`"`)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("conditional status = %d, want 200", res.StatusCode)
		}
		wantNoCacheHeaders(t, res)
	})
}

// A blob with no message row for the principal (delivery still in
// flight, or an upload) may still gain the header when the row lands.
func TestDownloadCache_NoMessageRow_NotCached(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		raw := multipartRaw("norow")
		ref, err := f.store.Blobs().Put(context.Background(), bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("blob put: %v", err)
		}
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)

		res, body := get(f.aliceKey, acct, ref.Hash, "")
		if res.StatusCode != http.StatusOK || !bytes.Equal(body, raw) {
			t.Fatalf("status = %d body = %q", res.StatusCode, body)
		}
		wantNoCacheHeaders(t, res)

		res, _ = get(f.aliceKey, acct, ref.Hash, `"`+ref.Hash+`"`)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("conditional status = %d, want 200", res.StatusCode)
		}
		wantNoCacheHeaders(t, res)

		// The row lands with a received_to: the bytes now differ, and
		// the earlier response was never cached.
		f.insertReceived(raw, "rcpt@example.test")
		res, body = get(f.aliceKey, acct, ref.Hash, `"`+ref.Hash+`"`)
		if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "rcpt@example.test") {
			t.Fatalf("after row: status = %d body = %q", res.StatusCode, body)
		}
	})
}

func TestDownloadCache_Unauthorized_403NeverCachedOr304(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		hash := f.insertReceived(multipartRaw("forbid"), "")
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)

		for _, id := range []string{hash, hash + "/p3"} {
			res, _ := get(f.bobKey, acct, id, `"`+id+`"`)
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("%s: bob status = %d, want 403", id, res.StatusCode)
			}
			wantNoCacheHeaders(t, res)
		}
	})
}

func TestDownloadCache_NotFound_NoCacheHeaders(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)
		missing := strings.Repeat("ab", 32)
		for _, id := range []string{missing, missing + "/p3"} {
			res, _ := get(f.aliceKey, acct, id, `"`+id+`"`)
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("%s: status = %d, want 404", id, res.StatusCode)
			}
			wantNoCacheHeaders(t, res)
		}
	})
}

// A non-owner chat-blob URL carries the short policy without
// "immutable", and a revoked membership turns the next request,
// including a conditional one, into 403.
func TestDownloadCache_ChatBlob_NonOwner(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		ctx := context.Background()
		raw := multipartRaw("chat")
		ref, err := f.store.Blobs().Put(ctx, bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("blob put: %v", err)
		}
		convID, err := f.store.Meta().InsertChatConversation(ctx, store.ChatConversation{
			Kind: store.ChatConversationKindDM, Name: "ab",
			CreatedByPrincipalID: f.alice, CreatedAt: f.clk.Now(), UpdatedAt: f.clk.Now(),
		})
		if err != nil {
			t.Fatalf("conversation: %v", err)
		}
		var bobMembership store.MembershipID
		for _, pid := range []store.PrincipalID{f.alice, f.bob} {
			id, err := f.store.Meta().InsertChatMembership(ctx, store.ChatMembership{
				ConversationID: convID, PrincipalID: pid, Role: store.ChatRoleMember,
				JoinedAt: f.clk.Now(), NotificationsSetting: store.ChatNotificationsAll,
			})
			if err != nil {
				t.Fatalf("membership: %v", err)
			}
			if pid == f.bob {
				bobMembership = id
			}
		}
		sender := f.alice
		if _, err := f.store.Meta().InsertChatMessage(ctx, store.ChatMessage{
			ConversationID: convID, SenderPrincipalID: &sender,
			BodyHTML: fmt.Sprintf(`<img src="/jmap/download/%s/%s/image%%2Fpng/i.png"/>`,
				protojmap.AccountIDForPrincipal(f.alice), ref.Hash),
			BodyFormat: store.ChatBodyFormatHTML, CreatedAt: f.clk.Now(),
		}); err != nil {
			t.Fatalf("chat message: %v", err)
		}
		get := cacheServer(t, f, f.store, protojmap.Options{})
		acct := protojmap.AccountIDForPrincipal(f.alice)

		res, body := get(f.bobKey, acct, ref.Hash, "")
		if res.StatusCode != http.StatusOK || !bytes.Equal(body, raw) {
			t.Fatalf("status = %d body = %q", res.StatusCode, body)
		}
		wantCacheHeaders(t, res, ref.Hash, chatPolicy)
		if strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
			t.Fatalf("chat policy must not be immutable")
		}

		res, body = get(f.bobKey, acct, ref.Hash, `"`+ref.Hash+`"`)
		if res.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("conditional status = %d, want 304", res.StatusCode)
		}
		wantCacheHeaders(t, res, ref.Hash, chatPolicy)

		if err := f.store.Meta().DeleteChatMembership(ctx, bobMembership); err != nil {
			t.Fatalf("delete membership: %v", err)
		}
		res, _ = get(f.bobKey, acct, ref.Hash, `"`+ref.Hash+`"`)
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("revoked status = %d, want 403", res.StatusCode)
		}
		wantNoCacheHeaders(t, res)
	})
}

// A 304 consumes no download budget, while a full response does.
func TestDownloadCache_304ChargesNoRateLimit(t *testing.T) {
	opts := protojmap.Options{DownloadRatePerSec: 1024, DownloadBurstBytes: 32}
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		hash := f.insertReceived(multipartRaw("budget"), "")
		get := cacheServer(t, f, f.store, opts)
		acct := protojmap.AccountIDForPrincipal(f.alice)
		part := hash + "/p3"

		// The first full response drains the burst.
		if res, _ := get(f.aliceKey, acct, hash, ""); res.StatusCode != http.StatusOK {
			t.Fatalf("first status = %d, want 200", res.StatusCode)
		}
		if res, _ := get(f.aliceKey, acct, hash, ""); res.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("drained status = %d, want 429", res.StatusCode)
		}
		// Conditional requests still succeed on an empty bucket.
		for i := 0; i < 3; i++ {
			if res, _ := get(f.aliceKey, acct, hash, `"`+hash+`"`); res.StatusCode != http.StatusNotModified {
				t.Fatalf("whole conditional status = %d, want 304", res.StatusCode)
			}
			if res, _ := get(f.aliceKey, acct, part, `"`+part+`"`); res.StatusCode != http.StatusNotModified {
				t.Fatalf("part conditional status = %d, want 304", res.StatusCode)
			}
		}
	})
}
