package protojmap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// countingStore counts the metadata reads the session descriptor and
// the session state are built from.
type countingStore struct {
	store.Store
	meta *countingMeta
}

func (c *countingStore) Meta() store.Metadata { return c.meta }

type countingMeta struct {
	store.Metadata
	mu     sync.Mutex
	counts map[string]int
}

func (m *countingMeta) hit(name string) {
	m.mu.Lock()
	m.counts[name]++
	m.mu.Unlock()
}

// snapshot returns the counts since the previous snapshot.
func (m *countingMeta) snapshot() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.counts
	m.counts = make(map[string]int)
	return out
}

func (m *countingMeta) DescriptorEpochs() *store.DescriptorEpochs {
	if src, ok := m.Metadata.(store.DescriptorEpochSource); ok {
		return src.DescriptorEpochs()
	}
	return nil
}

func (m *countingMeta) GetJMAPStates(ctx context.Context, pid store.PrincipalID) (store.JMAPStates, error) {
	m.hit("GetJMAPStates")
	return m.Metadata.GetJMAPStates(ctx, pid)
}

func (m *countingMeta) ListAppsForPrincipal(ctx context.Context, pid store.PrincipalID) ([]store.App, error) {
	m.hit("ListAppsForPrincipal")
	return m.Metadata.ListAppsForPrincipal(ctx, pid)
}

func (m *countingMeta) GetSession(ctx context.Context, id string) (store.SessionRow, error) {
	m.hit("GetSession")
	return m.Metadata.GetSession(ctx, id)
}

func (m *countingMeta) ListMailboxesAccessibleBy(ctx context.Context, pid store.PrincipalID) ([]store.Mailbox, error) {
	m.hit("ListMailboxesAccessibleBy")
	return m.Metadata.ListMailboxesAccessibleBy(ctx, pid)
}

func (m *countingMeta) GetPrincipalByID(ctx context.Context, pid store.PrincipalID) (store.Principal, error) {
	m.hit("GetPrincipalByID")
	return m.Metadata.GetPrincipalByID(ctx, pid)
}

func (m *countingMeta) GetMailboxACL(ctx context.Context, id store.MailboxID) ([]store.MailboxACL, error) {
	m.hit("GetMailboxACL")
	return m.Metadata.GetMailboxACL(ctx, id)
}

func (m *countingMeta) ListSubPrincipals(ctx context.Context, pid store.PrincipalID) ([]store.Principal, error) {
	m.hit("ListSubPrincipals")
	return m.Metadata.ListSubPrincipals(ctx, pid)
}

func (m *countingMeta) GetAPIKeyByHash(ctx context.Context, hash string) (store.APIKey, error) {
	m.hit("GetAPIKeyByHash")
	return m.Metadata.GetAPIKeyByHash(ctx, hash)
}

// stateFixture is a JMAP server with the Mail datatypes over a counting
// store, with alice (Bearer key) and bob, each owning an INBOX.
type stateFixture struct {
	t     *testing.T
	st    store.Store
	meta  *countingMeta
	clk   *clock.FakeClock
	srv   *protojmap.Server
	httpd *httptest.Server
	alice store.Principal
	bob   store.Principal
	key   string
	inbox store.MailboxID
	bobMB store.MailboxID
	seq   int
}

func forEachStateBackend(t *testing.T, fn func(t *testing.T, f *stateFixture)) {
	t.Run("sqlite", func(t *testing.T) {
		clk := clock.NewFake(time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC))
		st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), nil, clk)
		if err != nil {
			t.Fatalf("storesqlite.Open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		fn(t, newStateFixture(t, st, clk))
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("HEROLD_PG_DSN")
		if dsn == "" {
			t.Skip("HEROLD_PG_DSN not set")
		}
		clk := clock.NewFake(time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC))
		st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
		if err != nil {
			t.Fatalf("storepg.Open: %v", err)
		}
		if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
			_ = tr.TruncateAll(context.Background())
		}
		t.Cleanup(func() { _ = st.Close() })
		fn(t, newStateFixture(t, st, clk))
	})
}

func newStateFixture(t *testing.T, raw store.Store, clk *clock.FakeClock) *stateFixture {
	t.Helper()
	ctx := context.Background()
	meta := &countingMeta{Metadata: raw.Meta(), counts: make(map[string]int)}
	st := &countingStore{Store: raw, meta: meta}
	f := &stateFixture{t: t, st: st, meta: meta, clk: clk}
	mk := func(email string) (store.Principal, store.MailboxID) {
		p, err := raw.Meta().InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser, CanonicalEmail: email})
		if err != nil {
			t.Fatalf("InsertPrincipal: %v", err)
		}
		mb, err := raw.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
		if err != nil {
			t.Fatalf("InsertMailbox: %v", err)
		}
		return p, mb.ID
	}
	f.alice, f.inbox = mk("alice@example.test")
	f.bob, f.bobMB = mk("bob@example.test")
	key, _, err := createAPIKey(ctx, raw, f.alice.ID)
	if err != nil {
		t.Fatalf("createAPIKey: %v", err)
	}
	f.key = key
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := directory.New(st.Meta(), logger, clk, nil)
	f.srv = protojmap.NewServer(st, dir, nil, logger, clk, protojmap.Options{DownloadRatePerSec: -1})
	mail.Register(f.srv.Registry(), st, logger, clk)
	f.httpd = httptest.NewServer(f.srv.Handler())
	t.Cleanup(f.httpd.Close)
	return f
}

// post runs one method call as alice and returns the response's
// sessionState and the method response arguments.
func (f *stateFixture) post(method string, args map[string]any) (string, map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"using":       []string{string(protojmap.CapabilityCore), string(protojmap.CapabilityMail)},
		"methodCalls": []any{[]any{method, args, "c0"}},
	})
	req, _ := http.NewRequest(http.MethodPost, f.httpd.URL+"/jmap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("POST /jmap: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
		SessionState    string              `json:"sessionState"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.MethodResponses) != 1 {
		f.t.Fatalf("POST %s: %d %s", method, res.StatusCode, raw)
	}
	var name string
	var payload map[string]any
	_ = json.Unmarshal(out.MethodResponses[0][0], &name)
	_ = json.Unmarshal(out.MethodResponses[0][1], &payload)
	if name == "error" {
		f.t.Fatalf("%s: method error %v", method, payload)
	}
	return out.SessionState, payload
}

// state is the sessionState of a Core/echo call.
func (f *stateFixture) state() string {
	f.t.Helper()
	s, _ := f.post("Core/echo", map[string]any{})
	return s
}

type stateSession struct {
	Accounts map[string]struct {
		Name       string `json:"name"`
		IsPersonal bool   `json:"isPersonal"`
		IsReadOnly bool   `json:"isReadOnly"`
	} `json:"accounts"`
	State string `json:"state"`
}

func (f *stateFixture) session() stateSession {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.httpd.URL+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("GET session: %v", err)
	}
	defer res.Body.Close()
	var d stateSession
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		f.t.Fatalf("decode session: %v", err)
	}
	return d
}

// deliver inserts a message into mailbox mb the way local delivery does.
func (f *stateFixture) deliver(owner store.PrincipalID, mb store.MailboxID) {
	f.t.Helper()
	ctx := context.Background()
	f.seq++
	raw := fmt.Sprintf("From: x@remote.test\r\nTo: y@example.test\r\nSubject: m%d\r\nMessage-ID: <m%d@remote.test>\r\n\r\nbody\r\n", f.seq, f.seq)
	ref, err := f.st.Blobs().Put(ctx, bytes.NewReader([]byte(raw)))
	if err != nil {
		f.t.Fatalf("Blobs.Put: %v", err)
	}
	now := f.clk.Now()
	if _, _, err := f.st.Meta().InsertMessage(ctx, store.Message{PrincipalID: owner, InternalDate: now,
		ReceivedAt: now, Size: ref.Size, Blob: ref,
		Envelope: store.Envelope{Subject: fmt.Sprintf("m%d", f.seq), MessageID: fmt.Sprintf("m%d@remote.test", f.seq)}},
		[]store.MessageMailbox{{MailboxID: mb}}); err != nil {
		f.t.Fatalf("InsertMessage: %v", err)
	}
}

func TestSessionState_UnchangedByMailActivity(t *testing.T) {
	forEachStateBackend(t, func(t *testing.T, f *stateFixture) {
		acct := protojmap.AccountIDForPrincipal(f.alice.ID)
		before := f.state()
		if got := f.session().State; got != before {
			t.Fatalf("GET state %q != POST sessionState %q", got, before)
		}

		f.deliver(f.alice.ID, f.inbox)
		if got := f.state(); got != before {
			t.Fatalf("sessionState changed after delivery: %q -> %q", before, got)
		}

		_, q := f.post("Email/query", map[string]any{"accountId": acct})
		ids, _ := q["ids"].([]any)
		if len(ids) != 1 {
			t.Fatalf("Email/query ids = %v", q["ids"])
		}
		s, set := f.post("Email/set", map[string]any{"accountId": acct,
			"update": map[string]any{ids[0].(string): map[string]any{"keywords/$seen": true}}})
		if upd, _ := set["updated"].(map[string]any); len(upd) != 1 {
			t.Fatalf("Email/set = %v", set)
		}
		if s != before {
			t.Fatalf("sessionState changed by Email/set: %q -> %q", before, s)
		}
		if got := f.state(); got != before {
			t.Fatalf("sessionState after Email/set: %q -> %q", before, got)
		}

		s, set = f.post("Mailbox/set", map[string]any{"accountId": acct,
			"create": map[string]any{"k1": map[string]any{"name": "Projects"}}})
		if cr, _ := set["created"].(map[string]any); len(cr) != 1 {
			t.Fatalf("Mailbox/set = %v", set)
		}
		if s != before {
			t.Fatalf("sessionState changed by Mailbox/set: %q -> %q", before, s)
		}
		if got := f.state(); got != before {
			t.Fatalf("sessionState after Mailbox/set: %q -> %q", before, got)
		}
		if got := f.session().State; got != before {
			t.Fatalf("GET state changed by mail activity: %q -> %q", before, got)
		}
	})
}

func TestSessionState_ForeignAccountGrant(t *testing.T) {
	forEachStateBackend(t, func(t *testing.T, f *stateFixture) {
		ctx := context.Background()
		before := f.state()
		bobAcct := protojmap.AccountIDForPrincipal(f.bob.ID)

		alice := f.alice.ID
		if err := f.st.Meta().SetMailboxACL(ctx, f.bobMB, &alice, store.ACLRightLookup|store.ACLRightRead, f.bob.ID); err != nil {
			t.Fatalf("SetMailboxACL: %v", err)
		}
		granted := f.state()
		if granted == before {
			t.Fatal("sessionState unchanged after a grant added a foreign account")
		}
		sess := f.session()
		if a, ok := sess.Accounts[bobAcct]; !ok || a.IsPersonal || !a.IsReadOnly {
			t.Fatalf("foreign account = %+v (present %v)", a, ok)
		}
		if sess.State != granted {
			t.Fatalf("GET state %q != POST sessionState %q", sess.State, granted)
		}

		// Mail activity in the shared account leaves the state alone.
		f.deliver(f.bob.ID, f.bobMB)
		if got := f.state(); got != granted {
			t.Fatalf("sessionState changed by delivery into a shared mailbox: %q -> %q", granted, got)
		}

		// A write right flips isReadOnly.
		if err := f.st.Meta().SetMailboxACL(ctx, f.bobMB, &alice, store.ACLRightLookup|store.ACLRightRead|store.ACLRightWrite, f.bob.ID); err != nil {
			t.Fatalf("SetMailboxACL: %v", err)
		}
		writable := f.state()
		if writable == granted || f.session().Accounts[bobAcct].IsReadOnly {
			t.Fatal("sessionState or isReadOnly unchanged after the grant gained a write right")
		}

		// Renaming the owner renames the account.
		bob := f.bob
		bob.CanonicalEmail = "robert@example.test"
		if err := f.st.Meta().UpdatePrincipal(ctx, bob); err != nil {
			t.Fatalf("UpdatePrincipal: %v", err)
		}
		renamed := f.state()
		if renamed == writable || f.session().Accounts[bobAcct].Name != "robert@example.test" {
			t.Fatal("sessionState or account name unchanged after the owner was renamed")
		}

		// Revoking the grant removes the account; the state is a function
		// of the descriptor content, so it returns to its original value
		// once the owner's name is restored too.
		if err := f.st.Meta().RemoveMailboxACL(ctx, f.bobMB, &alice); err != nil {
			t.Fatalf("RemoveMailboxACL: %v", err)
		}
		if _, ok := f.session().Accounts[bobAcct]; ok {
			t.Fatal("foreign account still listed after revocation")
		}
		if got := f.state(); got != before {
			t.Fatalf("sessionState after revocation = %q, want the pre-grant %q", got, before)
		}
	})
}

func TestSessionState_SubAccount(t *testing.T) {
	forEachStateBackend(t, func(t *testing.T, f *stateFixture) {
		ctx := context.Background()
		before := f.state()
		sub, err := f.st.Meta().InsertSubPrincipal(ctx, f.alice.ID, store.Principal{
			Kind: store.PrincipalKindSubAccount, CanonicalEmail: "alice-ext@example.test"})
		if err != nil {
			t.Fatalf("InsertSubPrincipal: %v", err)
		}
		added := f.state()
		if added == before {
			t.Fatal("sessionState unchanged after a sub-account was added")
		}
		if _, ok := f.session().Accounts[protojmap.AccountIDForPrincipal(sub.ID)]; !ok {
			t.Fatal("sub-account not listed")
		}
		subMB, err := f.st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: sub.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
		if err != nil {
			t.Fatalf("InsertMailbox: %v", err)
		}
		f.deliver(sub.ID, subMB.ID)
		if got := f.state(); got != added {
			t.Fatalf("sessionState changed by delivery into a sub-account: %q -> %q", added, got)
		}
		if err := f.st.Meta().DeletePrincipal(ctx, sub.ID); err != nil {
			t.Fatalf("DeletePrincipal: %v", err)
		}
		if got := f.state(); got != before {
			t.Fatalf("sessionState after removing the sub-account = %q, want %q", got, before)
		}
	})
}

func TestSessionState_RegistryChange(t *testing.T) {
	forEachStateBackend(t, func(t *testing.T, f *stateFixture) {
		before := f.state()
		f.srv.Registry().RegisterCapabilityDescriptor("https://example.test/jmap/late", struct{}{})
		after := f.state()
		if after == before {
			t.Fatal("sessionState unchanged after a capability was registered")
		}
		if got := f.session().State; got != after {
			t.Fatalf("GET state %q != POST sessionState %q", got, after)
		}
	})
}

// TestSessionState_StoreReads counts the metadata reads behind the
// session state: none on a warm POST /jmap, one recomputation after a
// write that invalidates the cache.
func TestSessionState_StoreReads(t *testing.T) {
	forEachStateBackend(t, func(t *testing.T, f *stateFixture) {
		ctx := context.Background()
		alice := f.alice.ID
		if err := f.st.Meta().SetMailboxACL(ctx, f.bobMB, &alice, store.ACLRightLookup|store.ACLRightRead, f.bob.ID); err != nil {
			t.Fatalf("SetMailboxACL: %v", err)
		}
		if _, err := f.st.Meta().InsertSubPrincipal(ctx, f.alice.ID, store.Principal{
			Kind: store.PrincipalKindSubAccount, CanonicalEmail: "alice-ext@example.test"}); err != nil {
			t.Fatalf("InsertSubPrincipal: %v", err)
		}
		// Bearer auth reads the API key and the principal.
		auth := map[string]int{"GetAPIKeyByHash": 1, "GetPrincipalByID": 1}
		stateReads := func() map[string]int {
			got := f.meta.snapshot()
			for k, n := range auth {
				if got[k] -= n; got[k] == 0 {
					delete(got, k)
				}
			}
			return got
		}

		f.state()
		cold := stateReads()
		if len(cold) == 0 {
			t.Fatal("first POST read nothing; the counting store is not in the path")
		}
		const n = 20
		for range n {
			f.state()
		}
		warm := f.meta.snapshot()
		for k, v := range auth {
			warm[k] -= v * n
			if warm[k] == 0 {
				delete(warm, k)
			}
		}
		if len(warm) != 0 {
			t.Fatalf("warm POST /jmap made session-state reads: %v", warm)
		}
		t.Logf("session-state reads per POST /jmap: cold %v, warm none (%d requests)", cold, n)

		f.deliver(f.alice.ID, f.inbox)
		f.state()
		if got := stateReads(); len(got) != 0 {
			t.Fatalf("POST after delivery made session-state reads: %v", got)
		}

		if err := f.st.Meta().RemoveMailboxACL(ctx, f.bobMB, &alice); err != nil {
			t.Fatalf("RemoveMailboxACL: %v", err)
		}
		f.state()
		if got := stateReads(); got["ListMailboxesAccessibleBy"] != 1 {
			t.Fatalf("POST after an ACL write did not recompute: %v", got)
		}
		f.state()
		if got := stateReads(); len(got) != 0 {
			t.Fatalf("second POST after an ACL write made session-state reads: %v", got)
		}
	})
}

func TestSessionState_ClientlogSettings(t *testing.T) {
	forEachStateBackend(t, func(t *testing.T, f *stateFixture) {
		ctx := context.Background()
		now := f.clk.Now()
		const sessID = "csrf-state-523"
		row := store.SessionRow{SessionID: sessID, PrincipalID: f.alice.ID, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		upsertSessionRow(ctx, t, f.st, row)
		s1 := f.srv.SessionStateForTest(ctx, f.alice.ID, sessID)
		f.meta.snapshot()
		if again := f.srv.SessionStateForTest(ctx, f.alice.ID, sessID); again != s1 {
			t.Fatalf("sessionState unstable: %q -> %q", s1, again)
		}
		if got := f.meta.snapshot()["GetSession"]; got != 0 {
			t.Fatalf("warm sessionState read the session row %d times", got)
		}

		if err := f.st.Meta().UpdateSessionTelemetry(ctx, sessID, true); err != nil {
			t.Fatalf("UpdateSessionTelemetry: %v", err)
		}
		s2 := f.srv.SessionStateForTest(ctx, f.alice.ID, sessID)
		if s2 == s1 {
			t.Fatal("sessionState unchanged after the telemetry flag flipped")
		}

		until := now.Add(5 * time.Minute)
		row.ClientlogTelemetryEnabled = true
		row.ClientlogLivetailUntil = &until
		upsertSessionRow(ctx, t, f.st, row)
		s3 := f.srv.SessionStateForTest(ctx, f.alice.ID, sessID)
		if s3 == s2 {
			t.Fatal("sessionState unchanged after livetail was activated")
		}

		// The descriptor drops livetail_until once it has passed, with no
		// write to the session row.
		f.clk.Advance(6 * time.Minute)
		s4 := f.srv.SessionStateForTest(ctx, f.alice.ID, sessID)
		if s4 == s3 {
			t.Fatal("sessionState unchanged after livetail expired")
		}
		if s4 != s2 {
			t.Fatalf("sessionState after livetail expiry = %q, want the pre-livetail %q", s4, s2)
		}
	})
}
