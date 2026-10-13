package protojmap_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/auth"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// texpFixture is a JMAP server over one store with two principals:
// alice owns Inbox, Trash, Junk and Shared mailboxes; bob holds
// lookup+read on alice's Shared mailbox only.
type texpFixture struct {
	t        *testing.T
	store    store.Store
	clk      *clock.FakeClock
	httpd    *httptest.Server
	alice    store.PrincipalID
	bob      store.PrincipalID
	aliceKey string
	bobKey   string
	inbox    store.MailboxID
	trash    store.MailboxID
	junk     store.MailboxID
	shared   store.MailboxID
	seq      int
}

// forEachBackend runs fn against SQLite and, when HEROLD_PG_DSN is set,
// Postgres.
func forEachBackend(t *testing.T, opts protojmap.Options, fn func(t *testing.T, f *texpFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		clk := clock.NewFake(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
		st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
		if err != nil {
			t.Fatalf("storesqlite.Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		fn(t, newTexpFixture(t, st, clk, opts))
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("HEROLD_PG_DSN")
		if dsn == "" {
			t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
		}
		clk := clock.NewFake(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
		st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
		if err != nil {
			t.Skipf("storepg.Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		fn(t, newTexpFixture(t, st, clk, opts))
	})
}

func newTexpFixture(t *testing.T, st store.Store, clk *clock.FakeClock, opts protojmap.Options) *texpFixture {
	t.Helper()
	ctx := context.Background()
	nonce := time.Now().UnixNano()
	mkPrincipal := func(name string) store.PrincipalID {
		p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind:           store.PrincipalKindUser,
			CanonicalEmail: fmt.Sprintf("%s-%d@example.test", name, nonce),
		})
		if err != nil {
			t.Fatalf("InsertPrincipal %s: %v", name, err)
		}
		return p.ID
	}
	f := &texpFixture{t: t, store: st, clk: clk}
	f.alice = mkPrincipal("alice")
	f.bob = mkPrincipal("bob")
	mkBox := func(name string, attrs store.MailboxAttributes) store.MailboxID {
		mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: f.alice, Name: name, Attributes: attrs})
		if err != nil {
			t.Fatalf("InsertMailbox %s: %v", name, err)
		}
		return mb.ID
	}
	f.inbox = mkBox("INBOX", store.MailboxAttrInbox)
	f.trash = mkBox("Trash", store.MailboxAttrTrash)
	f.junk = mkBox("Junk", store.MailboxAttrJunk)
	f.shared = mkBox("Shared", 0)
	if err := st.Meta().SetMailboxACL(ctx, f.shared, &f.bob, store.ACLRightLookup|store.ACLRightRead, f.alice); err != nil {
		t.Fatalf("SetMailboxACL: %v", err)
	}
	var err error
	if f.aliceKey, _, err = createAPIKeyNamed(ctx, st, f.alice, "texp"); err != nil {
		t.Fatalf("api key alice: %v", err)
	}
	if f.bobKey, _, err = createAPIKeyNamed(ctx, st, f.bob, "texp"); err != nil {
		t.Fatalf("api key bob: %v", err)
	}
	if opts.DownloadRatePerSec == 0 {
		opts.DownloadRatePerSec = -1
	}
	dir := directory.New(st.Meta(), nil, clk, nil)
	srv := protojmap.NewServer(st, dir, nil, nil, clk, opts)
	f.httpd = httptest.NewServer(srv.Handler())
	t.Cleanup(f.httpd.Close)
	return f
}

// seeded is one inserted message: its id and raw bytes.
type seeded struct {
	id  store.MessageID
	raw []byte
}

// insert stores a message for alice in mb, received at offset from the
// fixture clock, in thread (0 starts a new thread keyed by its own id).
// raw == nil generates a unique body.
func (f *texpFixture) insert(mb store.MailboxID, thread uint64, subject string, offset time.Duration, raw []byte) seeded {
	f.t.Helper()
	ctx := context.Background()
	f.seq++
	if raw == nil {
		raw = []byte(fmt.Sprintf("Subject: %s\r\nMessage-ID: <texp-%d-%d@test>\r\nFrom: a@x\r\nTo: b@x\r\n\r\nbody %d\r\n",
			subject, f.alice, f.seq, f.seq))
	}
	ref, err := f.store.Blobs().Put(ctx, bytes.NewReader(raw))
	if err != nil {
		f.t.Fatalf("Blobs().Put: %v", err)
	}
	at := f.clk.Now().Add(offset)
	msg := store.Message{
		PrincipalID:  f.alice,
		InternalDate: at,
		ReceivedAt:   at,
		Size:         ref.Size,
		Blob:         ref,
		ThreadID:     thread,
		Envelope:     store.Envelope{Subject: subject, MessageID: fmt.Sprintf("<texp-%d-%d@test>", f.alice, f.seq)},
	}
	if _, _, err := f.store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mb}}); err != nil {
		f.t.Fatalf("InsertMessage: %v", err)
	}
	feed, err := f.store.Meta().ReadChangeFeed(ctx, f.alice, 0, 10000)
	if err != nil {
		f.t.Fatalf("ReadChangeFeed: %v", err)
	}
	for i := len(feed) - 1; i >= 0; i-- {
		if feed[i].Kind == store.EntityKindEmail && feed[i].Op == store.ChangeOpCreated {
			return seeded{id: store.MessageID(feed[i].EntityID), raw: raw}
		}
	}
	f.t.Fatal("inserted message id not found in change feed")
	return seeded{}
}

func (f *texpFixture) get(key string, account store.PrincipalID, threadID, name string) (*http.Response, []byte) {
	f.t.Helper()
	url := fmt.Sprintf("%s/jmap/download-thread/%s/%s/%s", f.httpd.URL,
		protojmap.AccountIDForPrincipal(account), threadID, name)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := f.httpd.Client().Do(req)
	if err != nil {
		f.t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatalf("read body: %v", err)
	}
	return res, body
}

type zipEntry struct {
	name   string
	method uint16
	data   []byte
}

func readZip(t *testing.T, body []byte) []zipEntry {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	out := make([]zipEntry, 0, len(zr.File))
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", zf.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", zf.Name, err)
		}
		out = append(out, zipEntry{name: zf.Name, method: zf.Method, data: data})
	}
	return out
}

func threadWireID(id store.MessageID) string { return fmt.Sprintf("t%d", id) }

func TestThreadExport_OrderDedupAndNaming(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		root := f.insert(f.inbox, 0, "Old computers / to give away", 2*time.Hour, nil)
		tk := uint64(root.id)
		first := f.insert(f.inbox, tk, "Re: Old computers", 0, nil)
		last := f.insert(f.shared, tk, "", 26*time.Hour, nil)
		// A second copy of the root's bytes in another mailbox shares its
		// blob hash and is exported once.
		f.insert(f.shared, tk, "Old computers / to give away", 3*time.Hour, root.raw)

		res, body := f.get(f.aliceKey, f.alice, threadWireID(root.id), "Old%20computers.zip")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.StatusCode, body)
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/zip" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cc := res.Header.Get("Cache-Control"); cc != "private, no-store" {
			t.Errorf("Cache-Control = %q", cc)
		}
		if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename="Old computers.zip"; filename*=UTF-8''Old%20computers.zip` {
			t.Errorf("Content-Disposition = %q", cd)
		}
		entries := readZip(t, body)
		want := []struct {
			name string
			raw  []byte
		}{
			{"1-2026-10-01-Re_ Old computers.eml", first.raw},
			{"2-2026-10-01-Old computers _ to give away.eml", root.raw},
			{"3-2026-10-02-message.eml", last.raw},
		}
		if len(entries) != len(want) {
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.name
			}
			t.Fatalf("entries = %v, want %d", names, len(want))
		}
		for i, w := range want {
			if entries[i].name != w.name {
				t.Errorf("entry %d name = %q, want %q", i, entries[i].name, w.name)
			}
			if entries[i].method != zip.Deflate {
				t.Errorf("entry %d method = %d, want Deflate", i, entries[i].method)
			}
			if !bytes.Equal(entries[i].data, w.raw) {
				t.Errorf("entry %d bytes differ from the stored message", i)
			}
		}
	})
}

func TestThreadExport_ExcludesTrashAndJunk(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		keep := f.insert(f.inbox, 0, "keep", 0, nil)
		tk := uint64(keep.id)
		f.insert(f.trash, tk, "binned", time.Hour, nil)
		f.insert(f.junk, tk, "spam", 2*time.Hour, nil)

		res, body := f.get(f.aliceKey, f.alice, threadWireID(keep.id), "t.zip")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.StatusCode, body)
		}
		entries := readZip(t, body)
		if len(entries) != 1 || !bytes.Equal(entries[0].data, keep.raw) {
			t.Fatalf("got %d entries, want only the Inbox message", len(entries))
		}
	})
}

func TestThreadExport_OnlyTrashedMessagesAreExported(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		a := f.insert(f.trash, 0, "a", 0, nil)
		b := f.insert(f.junk, uint64(a.id), "b", time.Hour, nil)

		res, body := f.get(f.aliceKey, f.alice, threadWireID(a.id), "t.zip")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.StatusCode, body)
		}
		entries := readZip(t, body)
		if len(entries) != 2 || !bytes.Equal(entries[0].data, a.raw) || !bytes.Equal(entries[1].data, b.raw) {
			t.Fatalf("got %d entries, want both binned messages in order", len(entries))
		}
	})
}

func TestThreadExport_SharedAccountFiltersByACL(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		private := f.insert(f.inbox, 0, "private", 0, nil)
		shared := f.insert(f.shared, uint64(private.id), "shared", time.Hour, nil)
		onlyPrivate := f.insert(f.inbox, 0, "only private", 0, nil)

		res, body := f.get(f.bobKey, f.alice, threadWireID(private.id), "t.zip")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.StatusCode, body)
		}
		entries := readZip(t, body)
		if len(entries) != 1 || !bytes.Equal(entries[0].data, shared.raw) {
			t.Fatalf("got %d entries, want only the Shared message", len(entries))
		}

		if res, body := f.get(f.bobKey, f.alice, threadWireID(onlyPrivate.id), "t.zip"); res.StatusCode != http.StatusNotFound {
			t.Fatalf("invisible thread: status = %d, want 404; body = %s", res.StatusCode, body)
		}
		// Alice holds no grant in bob's account.
		if res, body := f.get(f.aliceKey, f.bob, threadWireID(private.id), "t.zip"); res.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign account: status = %d, want 403; body = %s", res.StatusCode, body)
		}
	})
}

func TestThreadExport_NotFound(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		for _, id := range []string{"t999999999", "nonsense", "t0"} {
			if res, body := f.get(f.aliceKey, f.alice, id, "t.zip"); res.StatusCode != http.StatusNotFound {
				t.Errorf("thread %q: status = %d, want 404; body = %s", id, res.StatusCode, body)
			}
		}
	})
}

func TestThreadExport_Auth(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		m := f.insert(f.inbox, 0, "x", 0, nil)
		if res, _ := f.get("", f.alice, threadWireID(m.id), "t.zip"); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("no credentials: status = %d, want 401", res.StatusCode)
		}
		sendOnly, _, err := createAPIKeyWithScope(context.Background(), f.store, f.alice, "texp-send", []auth.Scope{auth.ScopeMailSend})
		if err != nil {
			t.Fatalf("createAPIKeyWithScope: %v", err)
		}
		if res, _ := f.get(sendOnly, f.alice, threadWireID(m.id), "t.zip"); res.StatusCode != http.StatusForbidden {
			t.Errorf("mail.send-only key: status = %d, want 403", res.StatusCode)
		}
	})
}

func TestThreadExport_CountsAgainstDownloadLimit(t *testing.T) {
	forEachBackend(t, protojmap.Options{DownloadRatePerSec: 1, DownloadBurstBytes: 32}, func(t *testing.T, f *texpFixture) {
		m := f.insert(f.inbox, 0, "x", 0, nil)
		if res, body := f.get(f.aliceKey, f.alice, threadWireID(m.id), "t.zip"); res.StatusCode != http.StatusOK {
			t.Fatalf("first: status = %d, body = %s", res.StatusCode, body)
		}
		res, _ := f.get(f.aliceKey, f.alice, threadWireID(m.id), "t.zip")
		if res.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("second: status = %d, want 429", res.StatusCode)
		}
		if res.Header.Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	})
}

func TestThreadExport_CapabilityAdvertised(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		req, _ := http.NewRequest(http.MethodGet, f.httpd.URL+"/.well-known/jmap", nil)
		req.Header.Set("Authorization", "Bearer "+f.aliceKey)
		res, err := f.httpd.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer res.Body.Close()
		var sess struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if err := json.NewDecoder(res.Body).Decode(&sess); err != nil {
			t.Fatalf("decode session: %v", err)
		}
		raw, ok := sess.Capabilities[string(protojmap.CapabilityThreadExport)]
		if !ok {
			t.Fatalf("capability %s not advertised", protojmap.CapabilityThreadExport)
		}
		if got, want := string(raw), `{"downloadUrl":"/jmap/download-thread/{accountId}/{threadId}/{name}"}`; got != want {
			t.Fatalf("descriptor = %s, want %s", got, want)
		}
	})
}

func TestThreadExporter_StreamEnforcesByteBound(t *testing.T) {
	forEachBackend(t, protojmap.Options{}, func(t *testing.T, f *texpFixture) {
		a := f.insert(f.inbox, 0, "a", 0, nil)
		f.insert(f.inbox, uint64(a.id), "b", time.Hour, nil)
		x := protojmap.NewThreadExporter(f.store)
		ctx := context.Background()
		exp, err := x.Resolve(ctx, f.alice, f.alice, threadWireID(a.id))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		total := exp.TotalSize()
		drain := func(_ protojmap.ThreadExportMessage, r io.Reader) error {
			_, err := io.Copy(io.Discard, r)
			return err
		}
		if err := x.Stream(ctx, exp, total, drain); err != nil {
			t.Fatalf("Stream at exact bound: %v", err)
		}
		if err := x.Stream(ctx, exp, total-1, drain); !errors.Is(err, protojmap.ErrExportTooLarge) {
			t.Fatalf("Stream below bound = %v, want ErrExportTooLarge", err)
		}
		if _, err := x.Resolve(ctx, f.bob, f.alice, threadWireID(a.id)); !errors.Is(err, protojmap.ErrThreadNotFound) {
			t.Fatalf("Resolve as bob = %v, want ErrThreadNotFound", err)
		}
	})
}

func TestZipEntryName(t *testing.T) {
	at := time.Date(2026, 10, 12, 23, 30, 0, 0, time.FixedZone("x", -3*3600))
	m := protojmap.ThreadExportMessage{Index: 3, ReceivedAt: at, Subject: "Hello"}
	if got, want := protojmap.ZipEntryName(m, 12), "03-2026-10-13-Hello.eml"; got != want {
		t.Errorf("ZipEntryName = %q, want %q", got, want)
	}
	if got, want := protojmap.ZipEntryName(m, 100), "003-2026-10-13-Hello.eml"; got != want {
		t.Errorf("ZipEntryName = %q, want %q", got, want)
	}
}

func TestSanitiseSubjectFilename(t *testing.T) {
	long := strings.Repeat("ä", 90)
	cases := []struct{ in, want string }{
		{"", "message"},
		{"  ", "message"},
		{":: <<>>", "message"},
		{"a/b\\c", "a_b_c"},
		{"tab\there\x01x", "tab_here_x"},
		{"  many   spaces  ", "many spaces"},
		{"Re: Fw: x?", "Re_ Fw_ x_"},
		{long, strings.Repeat("ä", 80)},
		{"bad\xffbyte", "bad_byte"},
	}
	for _, c := range cases {
		if got := protojmap.SanitiseSubjectFilename(c.in, "message"); got != c.want {
			t.Errorf("SanitiseSubjectFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func FuzzSanitiseSubjectFilename(f *testing.F) {
	for _, s := range []string{"", "Re: hello", "a/b\\c", "\x00\x1f\x7f", strings.Repeat("x", 200), "bad\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, subject string) {
		got := protojmap.SanitiseSubjectFilename(subject, "message")
		if got == "" {
			t.Fatal("empty result")
		}
		if n := len([]rune(got)); n > 80 {
			t.Fatalf("result has %d characters, want <= 80", n)
		}
		for _, r := range got {
			if strings.ContainsRune(`\/:*?"<>|`, r) || r < 0x20 || r == 0x7f {
				t.Fatalf("result %q contains forbidden rune %U", got, r)
			}
		}
	})
}
