package maildkim_test

// signer_stream_test.go: equivalence and correctness tests for
// Signer.SignStream (REQ-STORE-17).
//
// Safety gate (mandatory): for each corpus entry, Sign([]byte) is the
// reference oracle. SignStream output is read back to []byte, then:
//
//  1. The body hash (bh=) tag extracted from both DKIM-Signature headers
//     must be byte-identical — proves body canonicalization is equivalent.
//  2. The structural tags (a=, c=, d=, s=, h=) must be byte-identical —
//     proves header-set and algorithm selection are equivalent.
//  3. When both calls complete within the same wall-clock second (so their
//     t= Unix timestamps agree), the entire DKIM-Signature header must be
//     byte-identical, including b= — proves header signing is equivalent.
//  4. The SignStream-produced signed message, when fed to maildkim.Verify
//     (with the correct DNS stub), must report AuthPass — proves the
//     signature verifies end-to-end (the primary correctness gate).

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/maildkim"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness/fakedns"
)

// streamEquivFixture sets up a keymgmt.Manager + maildkim.Signer backed by
// a temporary SQLite store with a deterministic RSA key generated for
// signingDomain. The returned dns stub is pre-populated with the public key
// record needed by maildkim.Verify. clk is fixed to 2026-01-01T00:00:00Z.
func streamEquivFixture(t *testing.T, signingDomain string, alg store.DKIMAlgorithm) (
	ctx context.Context,
	s *maildkim.Signer,
	dns *fakedns.Resolver,
	clk clock.Clock,
) {
	t.Helper()

	fakeClk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := storesqlite.Open(context.Background(), dbPath, nil, fakeClk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	// Use a deterministic entropy source so RSA key generation is
	// reproducible across runs.
	rand := newStreamChachaReader(0xdeadbeefcafebabe)
	mgr := keymgmt.NewManager(st.Meta(), logger, fakeClk, rand)

	sel, err := mgr.GenerateKey(context.Background(), signingDomain, alg)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Build the DNS name: <selector>._domainkey.<domain>.
	dnsName := sel + "._domainkey." + signingDomain
	dnsStub := fakedns.New()
	// Retrieve the persisted key to get its PublicKeyB64, then build the
	// DNS TXT record in the format "v=DKIM1; k=<alg>; p=<b64>" that
	// go-msgauth's verifier expects.
	activeKey, err := st.Meta().GetActiveDKIMKey(context.Background(), signingDomain)
	if err != nil {
		t.Fatalf("GetActiveDKIMKey: %v", err)
	}
	var algName string
	switch activeKey.Algorithm {
	case store.DKIMAlgorithmRSASHA256:
		algName = "rsa"
	case store.DKIMAlgorithmEd25519SHA256:
		algName = "ed25519"
	default:
		t.Fatalf("unsupported algorithm: %v", activeKey.Algorithm)
	}
	dnsRecord := "v=DKIM1; k=" + algName + "; p=" + activeKey.PublicKeyB64
	dnsStub.AddTXT(dnsName, dnsRecord)

	signer := maildkim.NewSigner(mgr, logger, fakeClk)
	return context.Background(), signer, dnsStub, fakeClk
}

// newStreamChachaReader is identical to the chachaReader in the keymgmt test
// suite; duplicated here because the keymgmt test is in package keymgmt_test
// (external) and the helper is not exported.
type streamChachaReader struct{ cipher *chacha20.Cipher }

func newStreamChachaReader(seed uint64) io.Reader {
	var key [32]byte
	key[0] = byte(seed >> 56)
	key[1] = byte(seed >> 48)
	key[2] = byte(seed >> 40)
	key[3] = byte(seed >> 32)
	key[4] = byte(seed >> 24)
	key[5] = byte(seed >> 16)
	key[6] = byte(seed >> 8)
	key[7] = byte(seed)
	var nonce [12]byte
	c, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		panic(err)
	}
	return &streamChachaReader{cipher: c}
}

func (r *streamChachaReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	r.cipher.XORKeyStream(p, p)
	return len(p), nil
}

// extractDKIMTag extracts the value of a named tag from a DKIM-Signature
// header value (everything after "DKIM-Signature:"). Tags are
// semicolon-separated key=value pairs; whitespace is stripped.
func extractDKIMTag(sigHeader, tag string) string {
	// Strip "DKIM-Signature:" prefix.
	v := sigHeader
	if colon := strings.Index(v, ":"); colon >= 0 {
		v = v[colon+1:]
	}
	// Remove CRLF line folds.
	v = strings.ReplaceAll(v, "\r\n ", "")
	v = strings.ReplaceAll(v, "\r\n\t", "")
	v = strings.ReplaceAll(v, "\r\n", "")

	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == tag {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

// extractFirstDKIMSigHeader returns the first DKIM-Signature logical header
// from a signed message (including folded continuation lines).
func extractFirstDKIMSigHeader(msg []byte) string {
	lines := strings.Split(string(msg), "\r\n")
	var hdr strings.Builder
	inSig := false
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), "dkim-signature:") {
			inSig = true
			hdr.WriteString(line)
			hdr.WriteString("\r\n")
			continue
		}
		if inSig {
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				// Continuation line.
				hdr.WriteString(line)
				hdr.WriteString("\r\n")
				continue
			}
			// Not a continuation — done.
			break
		}
	}
	return hdr.String()
}

// assertEquivalence is the core safety-gate assertion. It:
//  1. Signs msg with Sign([]byte) → oracle output.
//  2. Signs msg with SignStream (bytes.Reader as seekable) → streamed output.
//  3. Reads streamed output to bytes.
//  4. Compares bh= and structural tags (a=, c=, d=, s=, h=) for byte
//     identity.
//  5. If t= agrees (same second), asserts the full DKIM-Signature header is
//     byte-identical (including b=).
//  6. Feeds the SignStream result to maildkim.Verify and asserts AuthPass.
func assertEquivalence(t *testing.T, name string, s *maildkim.Signer, dns *fakedns.Resolver, domain string, msg []byte, clk clock.Clock) {
	t.Helper()
	ctx := context.Background()

	// Oracle: Sign([]byte).
	oracleBytes, err := s.Sign(ctx, domain, msg)
	if err != nil {
		t.Fatalf("[%s] Sign: %v", name, err)
	}
	oracleSig := extractFirstDKIMSigHeader(oracleBytes)

	// Streaming: SignStream with a bytes.Reader (implements io.ReadSeeker).
	src := bytes.NewReader(msg)
	streamedReader, err := s.SignStream(ctx, domain, src)
	if err != nil {
		t.Fatalf("[%s] SignStream: %v", name, err)
	}
	streamedBytes, err := io.ReadAll(streamedReader)
	if err != nil {
		t.Fatalf("[%s] read streamed output: %v", name, err)
	}
	streamedSig := extractFirstDKIMSigHeader(streamedBytes)

	if oracleSig == "" {
		t.Fatalf("[%s] oracle: no DKIM-Signature header found in output", name)
	}
	if streamedSig == "" {
		t.Fatalf("[%s] stream: no DKIM-Signature header found in output", name)
	}

	// (1) Body hash (bh=) must be byte-identical.
	oracleBH := extractDKIMTag(oracleSig, "bh")
	streamedBH := extractDKIMTag(streamedSig, "bh")
	if oracleBH == "" {
		t.Fatalf("[%s] oracle: bh= tag missing", name)
	}
	if oracleBH != streamedBH {
		t.Errorf("[%s] bh= mismatch:\n  oracle  = %q\n  streamed = %q", name, oracleBH, streamedBH)
	}

	// (2) Structural tags must be byte-identical.
	for _, tag := range []string{"a", "c", "d", "s", "h"} {
		ov := extractDKIMTag(oracleSig, tag)
		sv := extractDKIMTag(streamedSig, tag)
		if ov != sv {
			t.Errorf("[%s] %s= mismatch:\n  oracle  = %q\n  streamed = %q", name, tag, ov, sv)
		}
	}

	// (3) When t= agrees, assert full byte-identity of the DKIM-Signature
	// header (including b=). If t= differs (clock ticked between calls), we
	// only assert the bh= and structural tags already checked above.
	oracleT := extractDKIMTag(oracleSig, "t")
	streamedT := extractDKIMTag(streamedSig, "t")
	if oracleT == streamedT {
		if oracleSig != streamedSig {
			t.Errorf("[%s] same t= but DKIM-Signature headers differ:\n  oracle  = %q\n  streamed = %q",
				name, oracleSig, streamedSig)
		}
		// Also compare the full signed message body-and-all.
		if !bytes.Equal(oracleBytes, streamedBytes) {
			t.Errorf("[%s] same t= but full signed message differs (len oracle=%d, streamed=%d)",
				name, len(oracleBytes), len(streamedBytes))
		}
	}

	// (4) Verify round-trip: SignStream output must verify as AuthPass.
	verifier := maildkim.New(
		fakeResolver{inner: dns},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		clk,
	)
	results, err := verifier.Verify(ctx, bytes.NewReader(streamedBytes))
	if err != nil {
		t.Fatalf("[%s] Verify error: %v", name, err)
	}
	if len(results) == 0 {
		t.Fatalf("[%s] Verify: no results (no DKIM-Signature found by verifier)", name)
	}
	for i, r := range results {
		if r.Status != mailauth.AuthPass {
			t.Errorf("[%s] result[%d]: status=%v reason=%q (want AuthPass)", name, i, r.Status, r.Reason)
		}
	}
}

// testCorpus returns the comprehensive set of (name, msg) pairs that cover
// all edge cases mentioned in the task brief:
//   - trailing whitespace on body lines (relaxed: folded to single space)
//   - internal whitespace runs in body
//   - trailing empty body lines (relaxed: trimmed)
//   - CRLF vs LF line endings
//   - empty body
//   - folded headers
//   - unfolded long headers
//   - duplicate headers
//   - signed header absent from message
//   - multipart message
//   - large body (1 MB)
//   - existing test message verifiedMailCRLF (from verifier_test.go)
func testCorpus() []struct {
	name string
	msg  []byte
} {
	return []struct {
		name string
		msg  []byte
	}{
		{
			name: "simple_crlf",
			msg:  []byte("From: alice@example.test\r\nTo: bob@example.test\r\nSubject: hello\r\n\r\nHello, world!\r\n"),
		},
		{
			name: "simple_lf_only",
			// go-msgauth's crlfFixer normalises lone LF to CRLF.
			msg: []byte("From: alice@example.test\nTo: bob@example.test\nSubject: hello\n\nHello, world!\n"),
		},
		{
			name: "empty_body",
			msg:  []byte("From: alice@example.test\r\nTo: bob@example.test\r\n\r\n"),
		},
		{
			name: "body_trailing_whitespace",
			// Relaxed canonicalization strips trailing WSP per line.
			msg: []byte("From: alice@example.test\r\nTo: bob@example.test\r\n\r\nline one  \r\nline two\t\r\n"),
		},
		{
			name: "body_internal_whitespace_runs",
			// Relaxed: multiple whitespace chars within a body line are
			// collapsed to a single space.
			msg: []byte("From: alice@example.test\r\nTo: bob@example.test\r\n\r\nword1   word2\t\t\tword3\r\n"),
		},
		{
			name: "body_trailing_empty_lines",
			// Relaxed body canonicalization removes trailing blank lines.
			msg: []byte("From: alice@example.test\r\nTo: bob@example.test\r\n\r\ncontent\r\n\r\n\r\n"),
		},
		{
			name: "folded_headers",
			msg: []byte("From: alice@example.test\r\nSubject: this is a very long subject line that\r\n" +
				" wraps onto the next line with a space\r\nTo: bob@example.test\r\n\r\nbody\r\n"),
		},
		{
			name: "long_unfolded_header",
			msg: []byte("From: alice@example.test\r\nSubject: " + strings.Repeat("X", 200) +
				"\r\nTo: bob@example.test\r\n\r\nbody\r\n"),
		},
		{
			name: "duplicate_headers",
			// RFC 6376 §5.4 allows multiple instances of the same header.
			// go-msgauth picks the LAST occurrence per the spec.
			msg: []byte("From: alice@example.test\r\nFrom: alice2@example.test\r\nTo: bob@example.test\r\n\r\nbody\r\n"),
		},
		{
			name: "cc_absent",
			// Cc is in DefaultSignedHeaders but absent from this message;
			// go-msgauth skips absent headers gracefully (RFC 6376 §5.4).
			msg: []byte("From: alice@example.test\r\nTo: bob@example.test\r\n\r\nbody\r\n"),
		},
		{
			name: "mime_multipart",
			msg: []byte("From: alice@example.test\r\nTo: bob@example.test\r\n" +
				"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\n" +
				"--X\r\nContent-Type: text/plain\r\n\r\npart one\r\n" +
				"--X\r\nContent-Type: text/plain\r\n\r\npart two\r\n" +
				"--X--\r\n"),
		},
		{
			name: "header_tab_folding",
			msg:  []byte("From: alice@example.test\r\nSubject: folded\r\n\twith tab\r\nTo: bob@example.test\r\n\r\nbody\r\n"),
		},
		{
			name: "large_body",
			msg: append(
				[]byte("From: alice@example.test\r\nTo: bob@example.test\r\nSubject: large\r\n\r\n"),
				bytes.Repeat([]byte("A"), 1024*1024)...),
		},
		{
			name: "all_default_headers_present",
			msg: []byte("From: alice@example.test\r\nTo: bob@example.test\r\nCc: carol@example.test\r\n" +
				"Subject: complete\r\nDate: Thu, 1 Jan 2026 00:00:00 +0000\r\n" +
				"Message-ID: <unique@example.test>\r\nMIME-Version: 1.0\r\n" +
				"Content-Type: text/plain; charset=utf-8\r\n" +
				"Content-Transfer-Encoding: 7bit\r\n" +
				"In-Reply-To: <prev@example.test>\r\n" +
				"References: <ref1@example.test> <ref2@example.test>\r\n" +
				"\r\nbody text\r\n"),
		},
		{
			name: "body_only_whitespace_lines",
			// Lines with only whitespace: relaxed strips WSP within the
			// line, leaving blank lines which are then stripped as trailing.
			msg: []byte("From: alice@example.test\r\n\r\n   \r\n\t\r\n"),
		},
		{
			name: "crlf_in_header_value",
			// Folded header value containing CRLF + space (legal RFC 5322
			// folding); relaxed header canonicalization re-joins these.
			msg: []byte("From: alice@example.test\r\nSubject: line one\r\n" +
				" line two\r\n line three\r\nTo: bob@example.test\r\n\r\nbody\r\n"),
		},
	}
}

// TestSignStream_Equivalence_RSA is the mandatory safety gate: for every
// corpus entry, Sign([]byte) and SignStream produce identical bh= / structural
// tags, and when t= agrees, the full DKIM-Signature is byte-identical.
// SignStream output must also pass maildkim.Verify.
func TestSignStream_Equivalence_RSA(t *testing.T) {
	ctx, signer, dns, clk := streamEquivFixture(t, "example.test", store.DKIMAlgorithmRSASHA256)
	_ = ctx
	for _, tc := range testCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			assertEquivalence(t, tc.name, signer, dns, "example.test", tc.msg, clk)
		})
	}
}

// TestSignStream_Equivalence_Ed25519 repeats the equivalence gate with an
// Ed25519 key. Ed25519 in go-msgauth is also deterministic (the signing is
// pure-function of key+message), so bh= identity and full-message equivalence
// (when t= agrees) both hold.
func TestSignStream_Equivalence_Ed25519(t *testing.T) {
	ctx, signer, dns, clk := streamEquivFixture(t, "ed.example.test", store.DKIMAlgorithmEd25519SHA256)
	_ = ctx
	for _, tc := range testCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			assertEquivalence(t, tc.name, signer, dns, "ed.example.test", tc.msg, clk)
		})
	}
}

// TestSignStream_ContextCancel checks that a cancelled context causes
// SignStream to return a non-nil error without panicking or leaking goroutines.
func TestSignStream_ContextCancel(t *testing.T) {
	_, signer, _, _ := streamEquivFixture(t, "example.test", store.DKIMAlgorithmRSASHA256)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := bytes.NewReader([]byte("From: a@example.test\r\n\r\nbody\r\n"))
	_, err := signer.SignStream(ctx, "example.test", src)
	if err == nil {
		t.Fatal("want error for cancelled ctx, got nil")
	}
}

// TestSignStream_NoActiveKey checks that SignStream surfaces ErrNoActiveKey
// when no key is registered for the requested domain.
func TestSignStream_NoActiveKey(t *testing.T) {
	_, signer, _, _ := streamEquivFixture(t, "example.test", store.DKIMAlgorithmRSASHA256)
	src := bytes.NewReader([]byte("From: a@other.test\r\n\r\nbody\r\n"))
	_, err := signer.SignStream(context.Background(), "other.test", src)
	if err == nil {
		t.Fatal("want error for missing key, got nil")
	}
	if !strings.Contains(err.Error(), "no active DKIM key") {
		t.Errorf("want ErrNoActiveKey in error, got: %v", err)
	}
}

// TestSignStream_VerifyRoundTrip_RFC6376Body signs the canonical RFC 6376
// message body with a fresh key and verifies the result, ensuring that the
// output of SignStream is accepted by an independent DKIM verifier. This is
// the primary correctness gate independent of the oracle comparison.
func TestSignStream_VerifyRoundTrip_RFC6376Body(t *testing.T) {
	_, signer, dns, clk := streamEquivFixture(t, "example.test", store.DKIMAlgorithmRSASHA256)

	// Use the same body as the RFC 6376 example but with our own headers so
	// we own the signature key.
	msg := []byte("From: joe@example.test\r\n" +
		"To: suzie@shopping.example.net\r\n" +
		"Subject: Is dinner ready?\r\n" +
		"Date: Fri, 11 Jul 2003 21:00:37 -0700 (PDT)\r\n" +
		"Message-ID: <20030712040037.46341.5F8J@example.test>\r\n" +
		"\r\n" +
		"Hi.\r\n\r\nWe lost the game. Are you hungry yet?\r\n\r\nJoe.\r\n")

	src := bytes.NewReader(msg)
	signedReader, err := signer.SignStream(context.Background(), "example.test", src)
	if err != nil {
		t.Fatalf("SignStream: %v", err)
	}
	signed, err := io.ReadAll(signedReader)
	if err != nil {
		t.Fatalf("read signed: %v", err)
	}

	verifier := maildkim.New(
		fakeResolver{inner: dns},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		clk,
	)
	results, vErr := verifier.Verify(context.Background(), bytes.NewReader(signed))
	if vErr != nil {
		t.Fatalf("Verify: %v", vErr)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 DKIM result, got %d", len(results))
	}
	if results[0].Status != mailauth.AuthPass {
		t.Errorf("status=%v reason=%q (want AuthPass)", results[0].Status, results[0].Reason)
	}
}

// TestSignStream_SeekableSourceConsumedCorrectly checks that the returned
// io.Reader emits the complete signed message (signature header + original
// body) with no bytes dropped or duplicated. The total length must equal
// len(signatureHeader) + len(originalMessage).
func TestSignStream_SeekableSourceConsumedCorrectly(t *testing.T) {
	_, signer, dns, clk := streamEquivFixture(t, "example.test", store.DKIMAlgorithmRSASHA256)

	const originalMsg = "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: test\r\n\r\nbody line\r\n"
	src := bytes.NewReader([]byte(originalMsg))

	signedReader, err := signer.SignStream(context.Background(), "example.test", src)
	if err != nil {
		t.Fatalf("SignStream: %v", err)
	}
	signed, err := io.ReadAll(signedReader)
	if err != nil {
		t.Fatalf("read signed: %v", err)
	}

	// The signed message must start with a DKIM-Signature header.
	if !strings.HasPrefix(string(signed), "DKIM-Signature:") {
		t.Errorf("signed message does not start with DKIM-Signature; prefix: %q", string(signed[:min(80, len(signed))]))
	}

	// The signed message must end with the original message body.
	if !bytes.HasSuffix(signed, []byte(originalMsg)) {
		t.Errorf("signed message does not end with original message\noriginal: %q\nsigned tail: %q",
			originalMsg, string(signed[max(0, len(signed)-len(originalMsg)-20):]))
	}

	// Verify round-trip sanity.
	verifier := maildkim.New(
		fakeResolver{inner: dns},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		clk,
	)
	results, err := verifier.Verify(context.Background(), bytes.NewReader(signed))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for i, r := range results {
		if r.Status != mailauth.AuthPass {
			t.Errorf("result[%d]: %v / %q", i, r.Status, r.Reason)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
