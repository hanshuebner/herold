package protoimap_test

// IMAP ACL (RFC 4314) wire-level tests. These exercise the SETACL /
// GETACL / MYRIGHTS / LISTRIGHTS surface plus the gating those rights
// drive on SELECT / LIST / APPEND / EXPUNGE.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// seedMailboxGrant inserts a `mailbox` grant row directly via InsertGrant
// (epic #210, REQ-AC-50), bypassing SETACL/SetMailboxACL. Level is a coarse
// tier word (store.GrantLevelRead/Write/Admin) rather than an RFC 4314
// letter-set, simulating a non-ACL-wire grant (e.g. a mailing-list archive
// read grant or a future IdP claim-mapping rule) -- exercises
// authz.ResolveMailboxRights's dual-format decode (internal/aclcodec.DecodeGrantLevel)
// independently of the SETACL-driven letter-set path.
func seedMailboxGrant(t *testing.T, af *aclFixture, mb store.Mailbox, grantee store.PrincipalID, level store.GrantLevel) {
	t.Helper()
	_, err := af.ha.Store.Meta().InsertGrant(context.Background(), store.Grant{
		SubjectID:    uint64(grantee),
		ResourceKind: store.GrantResourceMailbox,
		ResourceID:   strconv.FormatUint(uint64(mb.ID), 10),
		Level:        level,
		GrantedBy:    &af.aliceID,
	})
	if err != nil {
		t.Fatalf("seed mailbox grant: %v", err)
	}
}

// aclFixture is a two-principal fixture: alice owns Shared/support,
// bob is a separate authenticated principal who may or may not have
// ACL rows on alice's mailboxes depending on the test.
type aclFixture struct {
	ha          *testharness.Server
	srv         *protoimap.Server
	name        string
	dir         *directory.Directory
	tlsCfg      *tls.Config
	aliceID     store.PrincipalID
	bobID       store.PrincipalID
	alicePass   string
	bobPass     string
	aliceShared store.Mailbox // Shared/support, owned by alice
	aliceInbox  store.Mailbox
	bobInbox    store.Mailbox
}

func newACLFixture(t *testing.T) *aclFixture {
	t.Helper()
	name := "imaps"
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: name, Protocol: "imap"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	alicePass, bobPass := "alice-correct-horse", "bob-staple-battery"
	aliceID, err := dir.CreatePrincipal(ctx, "alice@example.test", alicePass)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bobID, err := dir.CreatePrincipal(ctx, "bob@example.test", bobPass)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	// directory.CreatePrincipal auto-provisions INBOX (+Sent/Drafts/...);
	// the IMAP harness needs it marked subscribed so SUBSCRIBE/LIST work
	// without an explicit subscribe step in every test.
	aliceInbox, err := ha.Store.Meta().GetMailboxByName(ctx, aliceID, "INBOX")
	if err != nil {
		t.Fatalf("alice INBOX: %v", err)
	}
	if err := ha.Store.Meta().SetMailboxSubscribed(ctx, aliceInbox.ID, true); err != nil {
		t.Fatalf("alice INBOX subscribe: %v", err)
	}
	bobInbox, err := ha.Store.Meta().GetMailboxByName(ctx, bobID, "INBOX")
	if err != nil {
		t.Fatalf("bob INBOX: %v", err)
	}
	if err := ha.Store.Meta().SetMailboxSubscribed(ctx, bobInbox.ID, true); err != nil {
		t.Fatalf("bob INBOX subscribe: %v", err)
	}
	aliceShared, err := ha.Store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: aliceID, Name: "Shared/support",
	})
	if err != nil {
		t.Fatalf("Shared/support: %v", err)
	}

	tlsStore, clientCfg := newACLTLSStore(t)
	srv := protoimap.NewServer(
		ha.Store, dir, tlsStore, ha.Clock, ha.Logger, nil, nil,
		protoimap.Options{
			MaxConnections:        16,
			MaxCommandsPerSession: 1000,
			IdleMaxDuration:       time.Minute,
			ServerName:            "herold",
		},
	)
	ha.AttachIMAP(name, srv, protoimap.ListenerModeImplicit993)
	t.Cleanup(func() { _ = srv.Close() })

	return &aclFixture{
		ha: ha, srv: srv, name: name, dir: dir, tlsCfg: clientCfg,
		aliceID: aliceID, bobID: bobID,
		alicePass: alicePass, bobPass: bobPass,
		aliceShared: aliceShared,
		aliceInbox:  aliceInbox,
		bobInbox:    bobInbox,
	}
}

// newACLTLSStore mirrors server_test.go's newTestTLSStore but is named
// distinctly so the symbol is unique within the test package.
func newACLTLSStore(t *testing.T) (*heroldtls.Store, *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mail.example.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"mail.example.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	leaf, _ := x509.ParseCertificate(der)
	cert.Leaf = leaf
	st := heroldtls.NewStore()
	st.SetDefault(&cert)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return st, &tls.Config{RootCAs: pool, ServerName: "mail.example.test"}
}

// loginAsACL connects to the implicit-TLS listener and logs in as the
// given principal. Returns a *client ready to issue commands.
func loginAsACL(t *testing.T, af *aclFixture, email, pass string) *client {
	t.Helper()
	conn, err := af.ha.DialIMAPSByName(context.Background(), af.name, af.tlsCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &client{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.readLine() // greeting
	resp := c.send("LOGIN", fmt.Sprintf("LOGIN %s %s", email, pass))
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("login %s: %v", email, resp)
	}
	return c
}

// -----------------------------------------------------------------------------
// SETACL / GETACL / MYRIGHTS / LISTRIGHTS
// -----------------------------------------------------------------------------

func TestSETACL_SetsACLEntry(t *testing.T) {
	af := newACLFixture(t)
	c := loginAsACL(t, af, "alice@example.test", af.alicePass)
	defer c.close()
	resp := c.send("s1", `SETACL "Shared/support" "bob@example.test" "lrswi"`)
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("SETACL failed: %v", resp)
	}
	rows, err := af.ha.Store.Meta().GetMailboxACL(context.Background(), af.aliceShared.ID)
	if err != nil {
		t.Fatalf("read back ACL: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 ACL row, got %d", len(rows))
	}
	if rows[0].PrincipalID == nil || *rows[0].PrincipalID != af.bobID {
		t.Fatalf("ACL row principal mismatch: %+v", rows[0])
	}
	want := store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen | store.ACLRightWrite | store.ACLRightInsert
	if rows[0].Rights != want {
		t.Fatalf("rights: got %v want %v", rows[0].Rights, want)
	}
}

func TestGETACL_ReturnsCurrentEntries(t *testing.T) {
	af := newACLFixture(t)
	c := loginAsACL(t, af, "alice@example.test", af.alicePass)
	defer c.close()
	c.send("s1", `SETACL "Shared/support" "bob@example.test" "lr"`)
	resp := c.send("g1", `GETACL "Shared/support"`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "alice@example.test") {
		t.Fatalf("GETACL missing owner: %v", resp)
	}
	if !strings.Contains(joined, "bob@example.test") {
		t.Fatalf("GETACL missing bob: %v", resp)
	}
	if !strings.Contains(joined, `"lr"`) {
		t.Fatalf("GETACL missing bob's lr rights: %v", resp)
	}
}

func TestMYRIGHTS_DefaultsForOwner(t *testing.T) {
	af := newACLFixture(t)
	c := loginAsACL(t, af, "alice@example.test", af.alicePass)
	defer c.close()
	resp := c.send("m1", `MYRIGHTS "Shared/support"`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "lrswipkxtea") {
		t.Fatalf("owner MYRIGHTS missing full set: %v", resp)
	}
}

func TestSELECT_RefusedWithoutLookup(t *testing.T) {
	af := newACLFixture(t)
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	resp := c.send("s1", `SELECT "Shared/support"`)
	last := resp[len(resp)-1]
	if !strings.Contains(last, "NO") {
		t.Fatalf("expected NO, got: %v", last)
	}
}

func TestLIST_FiltersByACLForNonOwner(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	if err := af.ha.Store.Meta().SetMailboxACL(ctx, af.aliceShared.ID, &af.bobID,
		store.ACLRightLookup|store.ACLRightRead, af.aliceID); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	resp := c.send("l1", `LIST "" "*"`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "Shared/support") {
		t.Fatalf("LIST should show Shared/support to bob: %v", resp)
	}
	if err := af.ha.Store.Meta().RemoveMailboxACL(ctx, af.aliceShared.ID, &af.bobID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	resp2 := c.send("l2", `LIST "" "*"`)
	if strings.Contains(strings.Join(resp2, "\n"), "Shared/support") {
		t.Fatalf("LIST must not show Shared/support after revoke: %v", resp2)
	}
}

func TestAPPEND_RequiresInsert(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	if err := af.ha.Store.Meta().SetMailboxACL(ctx, af.aliceShared.ID, &af.bobID,
		store.ACLRightLookup|store.ACLRightRead, af.aliceID); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	msg := buildMessage("hello", "body")
	c.write(fmt.Sprintf("a1 APPEND \"Shared/support\" {%d}\r\n", len(msg)))
	var last string
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "+") {
			c.write(msg + "\r\n")
			continue
		}
		if strings.HasPrefix(line, "a1 ") {
			last = line
			break
		}
	}
	if !strings.Contains(last, "NO") {
		t.Fatalf("APPEND should be refused without 'i': %v", last)
	}
}

func TestEXPUNGE_RequiresExpungeRight(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	msg := buildMessage("expunge-me", "body")
	blob, _ := af.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, err := af.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size:     int64(len(msg)),
		Blob:     blob,
		Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: af.aliceShared.ID, Flags: store.MessageFlagDeleted}})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := af.ha.Store.Meta().SetMailboxACL(ctx, af.aliceShared.ID, &af.bobID,
		store.ACLRightLookup|store.ACLRightRead|store.ACLRightDeleteMessage, af.aliceID); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	c.send("s1", `SELECT "Shared/support"`)
	resp := c.send("e1", "EXPUNGE")
	last := resp[len(resp)-1]
	if !strings.Contains(last, "NO") {
		t.Fatalf("EXPUNGE should be refused without 'e': %v", last)
	}
}

func TestSharedMailbox_TwoPrincipals_OneSupportInbox(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	cAlice := loginAsACL(t, af, "alice@example.test", af.alicePass)
	defer cAlice.close()
	resp := cAlice.send("s1", `SETACL "Shared/support" "bob@example.test" "lrswi"`)
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("SETACL: %v", resp)
	}
	msg := buildMessage("ticket-1", "first ticket body")
	blob, _ := af.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, err := af.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob,
		Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: af.aliceShared.ID}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cBob := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer cBob.close()
	listResp := cBob.send("l1", `LIST "" "*"`)
	if !strings.Contains(strings.Join(listResp, "\n"), "Shared/support") {
		t.Fatalf("bob LIST missing Shared/support: %v", listResp)
	}
	selResp := cBob.send("s1", `SELECT "Shared/support"`)
	if !strings.Contains(selResp[len(selResp)-1], "OK") {
		t.Fatalf("bob SELECT failed: %v", selResp)
	}
	fetchResp := cBob.send("f1", `FETCH 1 (UID FLAGS ENVELOPE)`)
	if !strings.Contains(strings.Join(fetchResp, "\n"), "ticket-1") {
		t.Fatalf("bob FETCH did not return seeded message: %v", fetchResp)
	}
}

// -----------------------------------------------------------------------------
// Unified grant substrate (epic #182/#186/#210, REQ-AC-50..53): a `mailbox`
// grant row written directly to the grants table (not through SETACL) is
// honoured by IMAP visibility and rights checks exactly like a SETACL-written
// row -- there is one storage, one read path.
// -----------------------------------------------------------------------------

func TestGrantMailboxRead_AllowsSelectFetch_DeniesWrite(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	seedMailboxGrant(t, af, af.aliceShared, af.bobID, store.GrantLevelRead)
	msg := buildMessage("granted-read", "body")
	blob, _ := af.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, err := af.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob,
		Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: af.aliceShared.ID}})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()

	listResp := c.send("l1", `LIST "" "*"`)
	if !strings.Contains(strings.Join(listResp, "\n"), "Shared/support") {
		t.Fatalf("mailbox:read grant should make Shared/support visible in LIST: %v", listResp)
	}
	selResp := c.send("s1", `SELECT "Shared/support"`)
	if !strings.Contains(selResp[len(selResp)-1], "OK") {
		t.Fatalf("mailbox:read grant should allow SELECT: %v", selResp)
	}
	fetchResp := c.send("f1", `FETCH 1 (UID FLAGS ENVELOPE)`)
	if !strings.Contains(strings.Join(fetchResp, "\n"), "granted-read") {
		t.Fatalf("mailbox:read grant should allow FETCH: %v", fetchResp)
	}
	msg2 := buildMessage("should-not-append", "body")
	c.write(fmt.Sprintf("a1 APPEND \"Shared/support\" {%d}\r\n", len(msg2)))
	var last string
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "+") {
			c.write(msg2 + "\r\n")
			continue
		}
		if strings.HasPrefix(line, "a1 ") {
			last = line
			break
		}
	}
	if !strings.Contains(last, "NO") {
		t.Fatalf("mailbox:read grant must not allow APPEND: %v", last)
	}
}

func TestGrantMailboxWrite_AllowsAppendStoreExpunge(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	msg := buildMessage("granted-write", "body")
	blob, _ := af.ha.Store.Blobs().Put(ctx, strings.NewReader(msg))
	_, _, err := af.ha.Store.Meta().InsertMessage(ctx, store.Message{
		Size: int64(len(msg)), Blob: blob,
		Envelope: parseStoreEnvelope(msg),
	}, []store.MessageMailbox{{MailboxID: af.aliceShared.ID, Flags: store.MessageFlagDeleted}})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	seedMailboxGrant(t, af, af.aliceShared, af.bobID, store.GrantLevelWrite)
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()

	appendMsg := buildMessage("appended-by-grant", "body")
	c.write(fmt.Sprintf("a1 APPEND \"Shared/support\" {%d}\r\n", len(appendMsg)))
	var last string
	for {
		line := c.readLine()
		if strings.HasPrefix(line, "+") {
			c.write(appendMsg + "\r\n")
			continue
		}
		if strings.HasPrefix(line, "a1 ") {
			last = line
			break
		}
	}
	if !strings.Contains(last, "OK") {
		t.Fatalf("mailbox:write grant should allow APPEND: %v", last)
	}
	selResp := c.send("s1", `SELECT "Shared/support"`)
	if !strings.Contains(selResp[len(selResp)-1], "OK") {
		t.Fatalf("SELECT: %v", selResp)
	}
	expungeResp := c.send("e1", "EXPUNGE")
	if !strings.Contains(expungeResp[len(expungeResp)-1], "OK") {
		t.Fatalf("mailbox:write grant should allow EXPUNGE: %v", expungeResp)
	}
}

func TestGrantMailboxWrite_DeniesSETACL(t *testing.T) {
	af := newACLFixture(t)
	seedMailboxGrant(t, af, af.aliceShared, af.bobID, store.GrantLevelWrite)
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	resp := c.send("s1", `SETACL "Shared/support" "bob@example.test" "a"`)
	if !strings.Contains(resp[len(resp)-1], "NO") {
		t.Fatalf("mailbox:write grant must not allow SETACL: %v", resp)
	}
}

func TestGrantMailboxAdmin_AllowsSETACL(t *testing.T) {
	af := newACLFixture(t)
	seedMailboxGrant(t, af, af.aliceShared, af.bobID, store.GrantLevelAdmin)
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	resp := c.send("s1", `SETACL "Shared/support" "bob@example.test" "lr"`)
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("mailbox:admin grant should allow SETACL: %v", resp)
	}
}

func TestMYRIGHTS_ReflectsGrantTier(t *testing.T) {
	af := newACLFixture(t)
	seedMailboxGrant(t, af, af.aliceShared, af.bobID, store.GrantLevelWrite)
	c := loginAsACL(t, af, "bob@example.test", af.bobPass)
	defer c.close()
	resp := c.send("m1", `MYRIGHTS "Shared/support"`)
	joined := strings.Join(resp, "\n")
	for _, want := range []string{"l", "r", "s", "i", "p", "k", "x", "t", "e", "w"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("MYRIGHTS for mailbox:write grant missing %q: %v", want, resp)
		}
	}
	if strings.Contains(joined, `"lrswipkxtea"`) {
		t.Fatalf("MYRIGHTS for mailbox:write grant must not include admin 'a': %v", resp)
	}
}

func TestGETACL_ShowsGrantSubstrateRow(t *testing.T) {
	af := newACLFixture(t)
	seedMailboxGrant(t, af, af.aliceShared, af.bobID, store.GrantLevelRead)
	c := loginAsACL(t, af, "alice@example.test", af.alicePass)
	defer c.close()
	resp := c.send("g1", `GETACL "Shared/support"`)
	joined := strings.Join(resp, "\n")
	if !strings.Contains(joined, "bob@example.test") {
		t.Fatalf("GETACL should surface the grant-substrate row for bob: %v", resp)
	}
	if !strings.Contains(joined, `"lrs"`) {
		t.Fatalf("GETACL grant row should render the read tier as 'lrs': %v", resp)
	}
}

// TestSETACL_NormalisesProvenanceOnReSet verifies that re-SETACL-ing a
// grantee whose row currently carries a non-local provenance (standing in
// for a migrated mailbox_acl row, provenance "acl-migration") replaces that
// row in place -- normalising its provenance to "local" -- rather than
// creating a second, competing entry for the same grantee. SETACL's RFC
// 4314 "replace wholesale" semantics (epic #210) hold regardless of the
// row's prior provenance.
func TestSETACL_NormalisesProvenanceOnReSet(t *testing.T) {
	af := newACLFixture(t)
	ctx := context.Background()
	if _, err := af.ha.Store.Meta().InsertGrant(ctx, store.Grant{
		SubjectID:    uint64(af.bobID),
		ResourceKind: store.GrantResourceMailbox,
		ResourceID:   strconv.FormatUint(uint64(af.aliceShared.ID), 10),
		Level:        "lrsw",
		Provenance:   store.GrantProvenanceACLMigration,
		GrantedBy:    &af.aliceID,
	}); err != nil {
		t.Fatalf("seed migrated grant: %v", err)
	}
	c := loginAsACL(t, af, "alice@example.test", af.alicePass)
	defer c.close()
	resp := c.send("s1", `SETACL "Shared/support" "bob@example.test" "lrs"`)
	if !strings.Contains(resp[len(resp)-1], "OK") {
		t.Fatalf("SETACL: %v", resp)
	}
	grants, err := af.ha.Store.Meta().ListGrantsOnResource(ctx, store.GrantResourceMailbox,
		strconv.FormatUint(uint64(af.aliceShared.ID), 10))
	if err != nil {
		t.Fatalf("ListGrantsOnResource: %v", err)
	}
	var bobGrants int
	for _, g := range grants {
		if g.SubjectKind == store.GrantSubjectPrincipal && store.PrincipalID(g.SubjectID) == af.bobID {
			bobGrants++
			if g.Provenance != store.GrantProvenanceLocal {
				t.Errorf("bob's grant provenance = %q; want %q (re-set normalises)", g.Provenance, store.GrantProvenanceLocal)
			}
		}
	}
	if bobGrants != 1 {
		t.Fatalf("bob grant rows on the mailbox = %d; want exactly 1 (re-set replaces, not adds)", bobGrants)
	}
	rows, err := af.ha.Store.Meta().GetMailboxACL(ctx, af.aliceShared.ID)
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	want := store.ACLRightLookup | store.ACLRightRead | store.ACLRightSeen
	if len(rows) != 1 || rows[0].Rights != want {
		t.Fatalf("bob's rights after re-SETACL = %+v; want a single row with %v (write-tier bit from the migrated grant gone)", rows, want)
	}
}
