package sasl_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/sasl"
)

// stubPWLookup stores SCRAMCredentials per authcid.
type stubPWLookup struct {
	creds map[string]sasl.SCRAMCredentials
	pids  map[string]sasl.PrincipalID
}

func (s *stubPWLookup) LookupSCRAMCredentials(ctx context.Context, authcid string) (sasl.SCRAMCredentials, sasl.PrincipalID, error) {
	c, ok := s.creds[authcid]
	if !ok {
		return sasl.SCRAMCredentials{}, 0, errors.New("no such user")
	}
	return c, s.pids[authcid], nil
}

// TestSCRAMSHA256RoundTrip exercises a full client/server exchange
// deterministically: we use known plaintext credentials, derive the
// server-side envelope, and drive a minimal SCRAM client that mirrors
// the RFC 5802 §3 algorithm.
func TestSCRAMSHA256RoundTrip(t *testing.T) {
	user := "alice@example.test"
	pass := "correct-horse-staple"
	salt := []byte("0123456789abcdef")
	creds := sasl.DeriveSCRAMCredentials(pass, salt, 4096)

	lookup := &stubPWLookup{
		creds: map[string]sasl.SCRAMCredentials{user: creds},
		pids:  map[string]sasl.PrincipalID{user: 42},
	}
	m := sasl.NewSCRAMSHA256(nil, lookup, false)

	cNonce := "rOprNGfwEbeRWgbNEkqO"
	clientFirst := "n,," + "n=" + user + ",r=" + cNonce
	serverFirst, done, err := m.Start(context.Background(), []byte(clientFirst))
	if err != nil || done {
		t.Fatalf("start: %v done=%v", err, done)
	}
	// Parse server-first.
	attrs := parseAttrs(string(serverFirst))
	combinedNonce := attrs["r"]
	if !strings.HasPrefix(combinedNonce, cNonce) {
		t.Fatalf("server nonce does not extend client nonce: %q", combinedNonce)
	}
	serverSalt, _ := base64.StdEncoding.DecodeString(attrs["s"])
	if string(serverSalt) != string(salt) {
		t.Fatalf("salt mismatch")
	}

	// Client-final-message without proof.
	cbind := base64.StdEncoding.EncodeToString([]byte("n,,"))
	cfWithoutProof := fmt.Sprintf("c=%s,r=%s", cbind, combinedNonce)
	authMessage := "n=" + user + ",r=" + cNonce + "," + string(serverFirst) + "," + cfWithoutProof

	// Compute client proof.
	clientKey := hmacSha256(saltedPassword(pass, salt, 4096), []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientSig := hmacSha256(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ clientSig[i]
	}
	clientFinal := cfWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)

	serverFinal, done, err := m.Next(context.Background(), []byte(clientFinal))
	if err != nil || !done {
		t.Fatalf("next: %v done=%v serverFinal=%q", err, done, serverFinal)
	}
	// Verify ServerSignature.
	sf := parseAttrs(string(serverFinal))
	gotSig, _ := base64.StdEncoding.DecodeString(sf["v"])
	serverKey := hmacSha256(saltedPassword(pass, salt, 4096), []byte("Server Key"))
	wantSig := hmacSha256(serverKey, []byte(authMessage))
	if !hmac.Equal(gotSig, wantSig) {
		t.Fatalf("server signature mismatch")
	}
	pid, err := m.Principal()
	if err != nil || pid != 42 {
		t.Fatalf("principal: %v pid=%d", err, pid)
	}
}

func TestSCRAMSHA256BadProof(t *testing.T) {
	user := "alice@example.test"
	salt := []byte("0123456789abcdef")
	creds := sasl.DeriveSCRAMCredentials("correct-horse-staple", salt, 4096)
	lookup := &stubPWLookup{
		creds: map[string]sasl.SCRAMCredentials{user: creds},
		pids:  map[string]sasl.PrincipalID{user: 42},
	}
	m := sasl.NewSCRAMSHA256(nil, lookup, false)
	cNonce := "rOprNGfwEbeRWgbNEkqO"
	clientFirst := "n,,n=" + user + ",r=" + cNonce
	serverFirst, _, err := m.Start(context.Background(), []byte(clientFirst))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	attrs := parseAttrs(string(serverFirst))
	combinedNonce := attrs["r"]
	cbind := base64.StdEncoding.EncodeToString([]byte("n,,"))
	cfWithoutProof := fmt.Sprintf("c=%s,r=%s", cbind, combinedNonce)
	// Use a deliberately wrong proof of the right length.
	proof := make([]byte, sha256.Size)
	clientFinal := cfWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	_, _, err = m.Next(context.Background(), []byte(clientFinal))
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestSCRAMSHA256UnknownUser(t *testing.T) {
	lookup := &stubPWLookup{creds: map[string]sasl.SCRAMCredentials{}, pids: map[string]sasl.PrincipalID{}}
	m := sasl.NewSCRAMSHA256(nil, lookup, false)
	cNonce := "rOprNGfwEbeRWgbNEkqO"
	clientFirst := "n,,n=nobody@example.test,r=" + cNonce
	// Start should still emit a server-first (to preserve timing).
	serverFirst, done, err := m.Start(context.Background(), []byte(clientFirst))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if done {
		t.Fatalf("should not be done yet")
	}
	attrs := parseAttrs(string(serverFirst))
	combinedNonce := attrs["r"]
	cbind := base64.StdEncoding.EncodeToString([]byte("n,,"))
	cfWithoutProof := fmt.Sprintf("c=%s,r=%s", cbind, combinedNonce)
	proof := make([]byte, sha256.Size)
	clientFinal := cfWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	_, _, err = m.Next(context.Background(), []byte(clientFinal))
	if !errors.Is(err, sasl.ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

// TestSCRAMSHA256PLUS verifies the tls-server-end-point channel binding
// path: Start advertises PLUS, client's gs2 prefixes "p=tls-server-end-point",
// and the cbind value in client-final-message must equal base64(gs2 || ep).
func TestSCRAMSHA256PLUS(t *testing.T) {
	user := "alice@example.test"
	pass := "correct-horse-staple"
	salt := []byte("0123456789abcdef")
	creds := sasl.DeriveSCRAMCredentials(pass, salt, 4096)
	lookup := &stubPWLookup{
		creds: map[string]sasl.SCRAMCredentials{user: creds},
		pids:  map[string]sasl.PrincipalID{user: 42},
	}
	m := sasl.NewSCRAMSHA256(nil, lookup, true)

	ep := []byte("EP-HASH-BYTES-32-byte-SHA-256-hash")
	ctx := sasl.WithTLSServerEndpoint(context.Background(), ep)
	cNonce := "rOprNGfwEbeRWgbNEkqO"
	gs2 := "p=tls-server-end-point,,"
	clientFirst := gs2 + "n=" + user + ",r=" + cNonce
	serverFirst, _, err := m.Start(ctx, []byte(clientFirst))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	attrs := parseAttrs(string(serverFirst))
	combinedNonce := attrs["r"]
	cbind := base64.StdEncoding.EncodeToString(append([]byte(gs2), ep...))
	cfWithoutProof := fmt.Sprintf("c=%s,r=%s", cbind, combinedNonce)
	authMessage := "n=" + user + ",r=" + cNonce + "," + string(serverFirst) + "," + cfWithoutProof
	clientKey := hmacSha256(saltedPassword(pass, salt, 4096), []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientSig := hmacSha256(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ clientSig[i]
	}
	clientFinal := cfWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	_, done, err := m.Next(ctx, []byte(clientFinal))
	if err != nil || !done {
		t.Fatalf("next: %v done=%v", err, done)
	}
	pid, _ := m.Principal()
	if pid != 42 {
		t.Fatalf("pid: %d", pid)
	}
}

// parseAttrs is the test-side sibling of the server's parser.
func parseAttrs(s string) map[string]string {
	out := make(map[string]string)
	for _, kv := range strings.Split(s, ",") {
		if len(kv) < 2 || kv[1] != '=' {
			continue
		}
		out[kv[:1]] = kv[2:]
	}
	return out
}

func hmacSha256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func saltedPassword(pw string, salt []byte, iter int) []byte {
	// PBKDF2-HMAC-SHA256 with 32-byte output.
	return pbkdf2HMACSha256([]byte(pw), salt, iter, 32)
}

func pbkdf2HMACSha256(password, salt []byte, iter, keylen int) []byte {
	// Minimal PBKDF2 to keep this test self-contained.
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keylen + hashLen - 1) / hashLen
	var out []byte
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		var bb [4]byte
		bb[0] = byte(block >> 24)
		bb[1] = byte(block >> 16)
		bb[2] = byte(block >> 8)
		bb[3] = byte(block)
		prf.Write(bb[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for x := range t {
				t[x] ^= u[x]
			}
		}
		out = append(out, t...)
	}
	return out[:keylen]
}
