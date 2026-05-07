package scripted

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"crypto/tls"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
	heroldtls "github.com/hanshuebner/herold/internal/tls"
)

// TestIMAPTestConformance runs Dovecot's `imaptest` against an
// in-process protoimap server when the binary is on PATH. The test
// skips with a diagnostic when imaptest is not installed so the CI
// conformance lane still passes on minimal runners; the make
// interop-imaptest target sets up the binary in a Docker image and
// covers the full surface.
//
// imaptest's exit code is 0 on success and non-zero on test failure
// or error; the runner pipes stdout/stderr through to the test log so
// regressions show the imaptest's diagnostic verbatim.
func TestIMAPTestConformance(t *testing.T) {
	bin, err := exec.LookPath("imaptest")
	if err != nil {
		t.Skipf("imaptest not on PATH; skipping (install dovecot-imaptest to run): %v", err)
	}
	host, port, password := startIMAPServer(t)

	// imaptest secs= bounds the run; the conformance lane wants a
	// quick smoke (5s) so the per-PR job stays under the 30-min
	// budget. The nightly interop lane uses run-imaptest.sh which
	// drives a longer soak.
	args := []string{
		"host=" + host,
		"port=" + strconv.Itoa(port),
		"user=alice@example.test",
		"pass=" + password,
		"mbox=INBOX",
		"secs=5",
		"clients=2",
		"no_pipelining",
	}
	t.Logf("imaptest command: %s %s", bin, strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("imaptest failed: %v\n--imaptest output--\n%s", err, out.String())
	}
	if t.Failed() {
		t.Logf("imaptest output:\n%s", out.String())
	}
}

// startIMAPServer spins up a protoimap server bound to a random
// localhost port and returns (host, port, password) so the imaptest
// runner can connect. The harness is torn down via t.Cleanup.
func startIMAPServer(t *testing.T) (host string, port int, password string) {
	t.Helper()
	ha, _ := testharness.Start(t, testharness.Options{
		Listeners: []testharness.ListenerSpec{{Name: "imap", Protocol: "imap"}},
	})
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	password = "correct-horse-staple-battery"
	pid, err := dir.CreatePrincipal(ctx, "alice@example.test", password)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	inbox, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("get INBOX: %v", err)
	}
	if err := ha.Store.Meta().SetMailboxSubscribed(ctx, inbox.ID, true); err != nil {
		t.Fatalf("subscribe INBOX: %v", err)
	}

	tlsStore := newScriptedTLSStore(t)
	srv := protoimap.NewServer(
		ha.Store,
		dir,
		tlsStore,
		ha.Clock,
		ha.Logger,
		nil, // SCRAM lookup not needed: imaptest uses LOGIN over plaintext
		nil, // token verifier
		protoimap.Options{
			MaxConnections:            16,
			MaxCommandsPerSession:     10000,
			IdleMaxDuration:           30 * time.Minute,
			AllowPlainLoginWithoutTLS: true, // imaptest defaults to LOGIN cleartext
			ServerName:                "herold",
		},
	)
	t.Cleanup(func() { _ = srv.Close() })
	ha.AttachIMAP("imap", srv, protoimap.ListenerModeSTARTTLS)
	addr, ok := ha.ListenerAddr("imap")
	if !ok {
		t.Fatalf("no listener addr for imap")
	}
	tcp := addr.(*net.TCPAddr)
	return "127.0.0.1", tcp.Port, password
}

// newScriptedTLSStore returns a self-signed TLS store sufficient for
// STARTTLS handshakes. imaptest uses LOGIN over plaintext (configured
// via the AllowPlainLoginWithoutTLS option) by default, but the
// listener still needs a cert in case the test grows STARTTLS
// coverage later.
func newScriptedTLSStore(t *testing.T) *heroldtls.Store {
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
	return st
}
