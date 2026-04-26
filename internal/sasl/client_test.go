package sasl_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/sasl"
)

// stubScramLookup is a PasswordLookup over a fixed credential.
type stubScramLookup struct {
	authcid string
	cred    sasl.SCRAMCredentials
	pid     sasl.PrincipalID
}

func (s *stubScramLookup) LookupSCRAMCredentials(ctx context.Context, authcid string) (sasl.SCRAMCredentials, sasl.PrincipalID, error) {
	if authcid != s.authcid {
		return sasl.SCRAMCredentials{}, 0, errors.New("not found")
	}
	return s.cred, s.pid, nil
}

func TestClientPLAIN_RoundTrip(t *testing.T) {
	srvAuth := &stubAuth{users: map[string]string{"alice@example.com": "pw"}, pid: 42}
	srv := sasl.NewPLAIN(srvAuth)
	cli := sasl.NewClientPLAIN("", "alice@example.com", "pw")

	ir, ok, err := cli.Start()
	if err != nil || !ok {
		t.Fatalf("client start: ok=%v err=%v", ok, err)
	}
	if cli.Name() != "PLAIN" {
		t.Errorf("name: %q", cli.Name())
	}
	ctx := sasl.WithTLS(context.Background(), true)
	chal, done, err := srv.Start(ctx, ir)
	if err != nil || !done {
		t.Fatalf("server start: done=%v err=%v chal=%q", done, err, chal)
	}
	pid, _ := srv.Principal()
	if pid != 42 {
		t.Fatalf("server pid: %d", pid)
	}
}

func TestClientPLAIN_BadCreds(t *testing.T) {
	srvAuth := &stubAuth{users: map[string]string{"alice@example.com": "pw"}, pid: 42}
	srv := sasl.NewPLAIN(srvAuth)
	cli := sasl.NewClientPLAIN("", "alice@example.com", "wrong")
	ir, _, _ := cli.Start()
	ctx := sasl.WithTLS(context.Background(), true)
	_, _, err := srv.Start(ctx, ir)
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestClientLOGIN_RoundTrip(t *testing.T) {
	srvAuth := &stubAuth{users: map[string]string{"alice@example.com": "pw"}, pid: 7}
	srv := sasl.NewLOGIN(srvAuth)
	cli := sasl.NewClientLOGIN("alice@example.com", "pw")

	if _, ok, err := cli.Start(); err != nil || ok {
		t.Fatalf("client LOGIN should not produce IR: ok=%v err=%v", ok, err)
	}
	ctx := sasl.WithTLS(context.Background(), true)

	// Server emits "Username:" prompt.
	prompt1, done, err := srv.Start(ctx, nil)
	if err != nil || done || string(prompt1) != "Username:" {
		t.Fatalf("server start: %v done=%v prompt=%q", err, done, prompt1)
	}
	resp1, ok, err := cli.Next(prompt1)
	if err != nil || ok {
		t.Fatalf("client user resp: %v ok=%v", err, ok)
	}
	prompt2, done, err := srv.Next(ctx, resp1)
	if err != nil || done || string(prompt2) != "Password:" {
		t.Fatalf("server next: %v done=%v prompt=%q", err, done, prompt2)
	}
	resp2, ok, err := cli.Next(prompt2)
	if err != nil || !ok {
		t.Fatalf("client pass resp: %v ok=%v", err, ok)
	}
	_, done, err = srv.Next(ctx, resp2)
	if err != nil || !done {
		t.Fatalf("server final: %v done=%v", err, done)
	}
	pid, _ := srv.Principal()
	if pid != 7 {
		t.Errorf("pid: %d", pid)
	}
}

func TestClientXOAUTH2_RoundTrip(t *testing.T) {
	tv := &stubTokenVerifier{valid: map[string]sasl.PrincipalID{"tok": 99}}
	srv := sasl.NewXOAUTH2(tv)
	cli := sasl.NewClientXOAUTH2("user@example.com", "tok")

	ir, ok, err := cli.Start()
	if err != nil || !ok {
		t.Fatalf("client start: ok=%v err=%v", ok, err)
	}
	// Sanity: ir must contain user= and Bearer + the SOH/SOH terminator.
	if !bytes.Contains(ir, []byte("user=user@example.com")) ||
		!bytes.Contains(ir, []byte("auth=Bearer tok")) ||
		!bytes.HasSuffix(ir, []byte{0x01, 0x01}) {
		t.Fatalf("XOAUTH2 IR shape wrong: %q", ir)
	}
	ctx := sasl.WithTLS(context.Background(), true)
	_, done, err := srv.Start(ctx, ir)
	if err != nil || !done {
		t.Fatalf("server: %v done=%v", err, done)
	}
	pid, _ := srv.Principal()
	if pid != 99 {
		t.Errorf("pid: %d", pid)
	}
}

func TestClientXOAUTH2_BadToken(t *testing.T) {
	tv := &stubTokenVerifier{valid: map[string]sasl.PrincipalID{"tok": 99}}
	srv := sasl.NewXOAUTH2(tv)
	cli := sasl.NewClientXOAUTH2("user@example.com", "wrong")

	ir, _, _ := cli.Start()
	ctx := sasl.WithTLS(context.Background(), true)
	chal, done, err := srv.Start(ctx, ir)
	if err != nil || done {
		t.Fatalf("server should challenge on bad token: err=%v done=%v", err, done)
	}
	if len(chal) == 0 {
		t.Fatalf("expected JSON challenge")
	}
	// Client acks the failure with an empty resp; server then closes.
	resp, _, _ := cli.Next(chal)
	_, done, err = srv.Next(ctx, resp)
	if !errors.Is(err, sasl.ErrAuthFailed) || !done {
		t.Fatalf("server final: err=%v done=%v", err, done)
	}
}

func TestClientSCRAM_SHA256_RoundTrip(t *testing.T) {
	salt := []byte("0123456789abcdef")
	cred := sasl.DeriveSCRAMCredentials("hunter2", salt, 4096)
	lookup := &stubScramLookup{authcid: "alice", cred: cred, pid: 11}
	srv := sasl.NewSCRAMSHA256(nil, lookup, false)
	cli := sasl.NewClientSCRAMSHA256("alice", "hunter2", nil)

	ir, ok, err := cli.Start()
	if err != nil || ok {
		t.Fatalf("client start: ir=%q ok=%v err=%v", ir, ok, err)
	}
	if !strings.HasPrefix(string(ir), "n,,n=alice,r=") {
		t.Fatalf("client-first shape: %q", ir)
	}
	ctx := context.Background()
	srvFirst, done, err := srv.Start(ctx, ir)
	if err != nil || done {
		t.Fatalf("server start: err=%v done=%v", err, done)
	}
	clientFinal, ok, err := cli.Next(srvFirst)
	if err != nil || ok {
		t.Fatalf("client-final: %v ok=%v", err, ok)
	}
	srvFinal, done, err := srv.Next(ctx, clientFinal)
	if err != nil || !done {
		t.Fatalf("server-final: %v done=%v", err, done)
	}
	if !strings.HasPrefix(string(srvFinal), "v=") {
		t.Fatalf("server-final shape: %q", srvFinal)
	}
	_, ok, err = cli.Next(srvFinal)
	if err != nil || !ok {
		t.Fatalf("client verify: %v ok=%v", err, ok)
	}
	pid, _ := srv.Principal()
	if pid != 11 {
		t.Errorf("pid: %d", pid)
	}
}

func TestClientSCRAM_SHA256_BadPassword(t *testing.T) {
	salt := []byte("0123456789abcdef")
	cred := sasl.DeriveSCRAMCredentials("hunter2", salt, 4096)
	lookup := &stubScramLookup{authcid: "alice", cred: cred, pid: 11}
	srv := sasl.NewSCRAMSHA256(nil, lookup, false)
	cli := sasl.NewClientSCRAMSHA256("alice", "wrong", nil)

	ir, _, _ := cli.Start()
	ctx := context.Background()
	srvFirst, _, err := srv.Start(ctx, ir)
	if err != nil {
		t.Fatalf("server start: %v", err)
	}
	clientFinal, _, err := cli.Next(srvFirst)
	if err != nil {
		t.Fatalf("client-final: %v", err)
	}
	_, _, err = srv.Next(ctx, clientFinal)
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestClientSCRAM_BadServerNonce(t *testing.T) {
	cli := sasl.NewClientSCRAMSHA256("alice", "pw", nil)
	if _, _, err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	// Hand a server-first whose nonce does NOT echo the client nonce.
	bogus := "r=ZZZZZZ," + "s=" + base64.StdEncoding.EncodeToString([]byte("salt")) + ",i=4096"
	_, _, err := cli.Next([]byte(bogus))
	if !errors.Is(err, sasl.ErrClientProtocol) {
		t.Fatalf("want ErrClientProtocol, got %v", err)
	}
}

func TestClientPLAIN_StartTwiceRejected(t *testing.T) {
	cli := sasl.NewClientPLAIN("", "u", "p")
	if _, _, err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cli.Start(); !errors.Is(err, sasl.ErrClientProtocol) {
		t.Fatalf("want ErrClientProtocol, got %v", err)
	}
}
