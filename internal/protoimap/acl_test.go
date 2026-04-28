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
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

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
		Listeners: []testharness.ListenerSpec{{Name: name, Protocol: "imaps"}},
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
		MailboxID: af.aliceShared.ID,
		Flags:     store.MessageFlagDeleted,
		Size:      int64(len(msg)),
		Blob:      blob,
		Envelope:  parseStoreEnvelope(msg),
	})
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
		MailboxID: af.aliceShared.ID, Size: int64(len(msg)), Blob: blob,
		Envelope: parseStoreEnvelope(msg),
	})
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
