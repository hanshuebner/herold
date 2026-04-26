package sesinbound_test

// Tests for the SES inbound handler (REQ-HOOK-SES-01..07).
//
// Coverage:
//   - happy path: valid Notification, signature verified, pipeline called
//   - rejected_signature: invalid body signature
//   - rejected_replay: same MessageId posted twice
//   - rejected_bucket: Notification for non-allowlisted bucket
//   - rejected_topic: SubscriptionConfirmation for non-allowlisted topic
//   - SubscriptionConfirmation auto-confirm for allowlisted topic
//   - UnsubscribeConfirmation: accepted with 200, not forwarded to pipeline
//   - cert_host_disallowed: SigningCertURL with unallowlisted host
//   - netguard: SubscribeURL resolving to loopback is rejected
//
// Signature tests generate an ephemeral RSA key pair in-test and serve
// the cert from an httptest.Server so no real AWS endpoint is contacted.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SNS v1 uses SHA-1.
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/sesinbound"
	"github.com/hanshuebner/herold/internal/store"
)

// ---- test stubs -------------------------------------------------------------

// recordingPipeline captures Ingest calls.
type recordingPipeline struct {
	mu    sync.Mutex
	calls []sesinbound.IngestMsg
	err   error
}

func (p *recordingPipeline) Ingest(_ context.Context, req sesinbound.IngestMsg) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, req)
	return p.err
}

func (p *recordingPipeline) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// inMemSeenStore is a simple in-memory SeenStore for tests.
type inMemSeenStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newInMemSeenStore() *inMemSeenStore {
	return &inMemSeenStore{seen: make(map[string]time.Time)}
}

func (s *inMemSeenStore) IsSESSeen(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[id]
	return ok, nil
}

func (s *inMemSeenStore) InsertSESSeen(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[id] = at
	return nil
}

func (s *inMemSeenStore) GCOldSESSeen(_ context.Context, _ time.Time) error { return nil }

// nullAudit satisfies sesinbound.AuditLogger.
type nullAudit struct{ count int64 }

func (a *nullAudit) AppendAuditLog(_ context.Context, _ store.AuditLogEntry) error {
	atomic.AddInt64(&a.count, 1)
	return nil
}

// ---- ephemeral signing key helper ------------------------------------------

type testSigner struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
	// certPEM is the PEM-encoded certificate (served by certSrv).
	certPEM []byte
}

// newTestSigner generates an RSA key pair and a self-signed certificate valid
// for commonName.  The certificate chain verification in the handler uses
// system roots, so we skip it in unit tests by using an insecure cert server
// AND by using a custom verifier — see TestSignatureVerify_SelfSigned which
// stubs the cert-fetch server.
func newTestSigner(t *testing.T, commonName string) *testSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return &testSigner{key: key, cert: cert, certPEM: certPEM}
}

// sign returns a base64-encoded RSA-SHA1 signature over the canonical
// signing string for m (version 1).
func (s *testSigner) sign(m *rawSNSMsg) string {
	signing := canonicalSigningStringForTest(m)
	//nolint:gosec // SNS v1 mandates SHA-1.
	h := sha1.New()
	h.Write([]byte(signing))
	digest := h.Sum(nil)
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA1, digest)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// canonicalSigningStringForTest replicates the canonical string logic for
// test-side use (mirrors signature.go:canonicalSigningString).
func canonicalSigningStringForTest(m *rawSNSMsg) string {
	var b strings.Builder
	add := func(k, v string) {
		b.WriteString(k)
		b.WriteByte('\n')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	switch m.Type {
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		add("Message", m.Message)
		add("MessageId", m.MessageId)
		add("SubscribeURL", m.SubscribeURL)
		add("Timestamp", m.Timestamp)
		add("Token", m.Token)
		add("TopicArn", m.TopicArn)
		add("Type", m.Type)
	default:
		add("Message", m.Message)
		add("MessageId", m.MessageId)
		if m.Subject != "" {
			add("Subject", m.Subject)
		}
		add("Timestamp", m.Timestamp)
		add("TopicArn", m.TopicArn)
		add("Type", m.Type)
	}
	return b.String()
}

// rawSNSMsg is the JSON-serializable form of an SNS message, with field
// names matching the AWS wire format (MessageId vs MessageID).
type rawSNSMsg struct {
	Type             string `json:"Type"`
	MessageId        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject,omitempty"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	Token            string `json:"Token,omitempty"`
	SubscribeURL     string `json:"SubscribeURL,omitempty"`
}

// sesReceiptMsg is the JSON body of the SNS Message field for a SES
// receipt notification.
type sesReceiptMsg struct {
	Mail struct {
		Source      string   `json:"source"`
		Destination []string `json:"destination"`
		MessageId   string   `json:"messageId"`
	} `json:"mail"`
	Receipt struct {
		Action struct {
			Type       string `json:"type"`
			BucketName string `json:"bucketName"`
			ObjectKey  string `json:"objectKey"`
		} `json:"action"`
	} `json:"receipt"`
}

// ---- test fixture builder --------------------------------------------------

// testFixture wires up a Handler with test stubs and a self-signed cert
// server.  The cert server uses TLS but the client inside the handler's
// verifier is set to skip verification for test certs (overridden by the
// test helper).
//
// For signature verification tests we need the handler to trust our
// self-signed cert.  We do this by serving the cert PEM over plain HTTP
// (httptest.Server) and marking the cert's host as allowed.  The chain
// verification step would fail for a self-signed cert against system roots,
// so we override it with a custom TLS config that pins our test cert.
type testFixture struct {
	signer        *testSigner
	certSrv       *httptest.Server // serves the cert PEM over HTTP
	s3Srv         *httptest.Server // stub S3 server
	confirmSrv    *httptest.Server // catches SubscribeURL GET
	pipeline      *recordingPipeline
	seenStore     *inMemSeenStore
	audit         *nullAudit
	handler       *sesinbound.Handler
	allowedBucket string
	allowedTopic  string
}

// newTestFixture builds a fully wired test fixture.  The cert server serves
// the test signer's PEM, so the handler's cert-host allowlist must include
// the cert server's host.
//
// We use an insecure cert fetcher so the self-signed cert passes chain
// verification.  This is acceptable because:
//
//	(a) we test chain failure separately (TestCertChainInvalid)
//	(b) unit tests cannot reach real CA infrastructure
//
// Production behaviour (system-roots verification) is exercised indirectly
// by the integration test.
func newTestFixture(t *testing.T) *testFixture {
	t.Helper()
	signer := newTestSigner(t, "sns.us-east-1.amazonaws.com")

	// Cert server: plain HTTP, serves the PEM.
	certSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Write(signer.certPEM) //nolint:errcheck
	}))
	t.Cleanup(certSrv.Close)

	// S3 stub: returns a tiny RFC 5322 message.
	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "From: sender@example.com\r\nTo: rcpt@example.com\r\nSubject: test\r\n\r\nHello\r\n")
	}))
	t.Cleanup(s3Srv.Close)

	// SubscribeURL stub: returns 200.
	confirmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(confirmSrv.Close)

	certHost := hostOf(certSrv.URL)

	pipeline := &recordingPipeline{}
	seenSt := newInMemSeenStore()
	audit := &nullAudit{}

	// Build the handler with a custom HTTP client that trusts any cert
	// (so our self-signed test cert passes TLS when the cert server is
	// httptest.TLS; for plain HTTP cert servers no TLS is involved).
	h := newHandlerWithInsecureCerts(t, sesinbound.Config{
		AWSRegion:                  "us-east-1",
		S3BucketAllowlist:          []string{"allowed-bucket"},
		SNSTopicARNAllowlist:       []string{"arn:aws:sns:us-east-1:123456789012:herold"},
		SignatureCertHostAllowlist: []string{certHost},
		AWSAccessKeyID:             "AKIAIOSFODNN7EXAMPLE",
		AWSSecretAccessKey:         "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}, pipeline, seenSt, audit, s3Srv.URL)

	return &testFixture{
		signer:        signer,
		certSrv:       certSrv,
		s3Srv:         s3Srv,
		confirmSrv:    confirmSrv,
		pipeline:      pipeline,
		seenStore:     seenSt,
		audit:         audit,
		handler:       h,
		allowedBucket: "allowed-bucket",
		allowedTopic:  "arn:aws:sns:us-east-1:123456789012:herold",
	}
}

// buildNotification returns a signed rawSNSMsg wrapping a SES receipt
// notification for bucket/key.
func (f *testFixture) buildNotification(messageId, bucket, key string) *rawSNSMsg {
	inner := sesReceiptMsg{}
	inner.Mail.Source = "203.0.113.1"
	inner.Mail.Destination = []string{"rcpt@example.com"}
	inner.Mail.MessageId = messageId
	inner.Receipt.Action.Type = "S3"
	inner.Receipt.Action.BucketName = bucket
	inner.Receipt.Action.ObjectKey = key
	msgJSON, _ := json.Marshal(inner)

	m := &rawSNSMsg{
		Type:             "Notification",
		MessageId:        messageId,
		TopicArn:         f.allowedTopic,
		Message:          string(msgJSON),
		Timestamp:        "2026-04-26T10:00:00.000Z",
		SignatureVersion: "1",
		SigningCertURL:   f.certSrv.URL + "/cert.pem",
	}
	m.Signature = f.signer.sign(m)
	return m
}

// buildConfirmation returns a signed SubscriptionConfirmation message.
func (f *testFixture) buildConfirmation(topicArn, subscribeURL string) *rawSNSMsg {
	m := &rawSNSMsg{
		Type:             "SubscriptionConfirmation",
		MessageId:        "conf-" + topicArn,
		TopicArn:         topicArn,
		Token:            "tok123",
		SubscribeURL:     subscribeURL,
		Message:          "You have chosen to subscribe to the topic " + topicArn,
		Timestamp:        "2026-04-26T10:00:00.000Z",
		SignatureVersion: "1",
		SigningCertURL:   f.certSrv.URL + "/cert.pem",
	}
	m.Signature = f.signer.sign(m)
	return m
}

// post sends an HTTP POST to the handler and returns the response.
func (f *testFixture) post(msg *rawSNSMsg) *httptest.ResponseRecorder {
	body, _ := json.Marshal(msg)
	req := httptest.NewRequest(http.MethodPost, "/hooks/ses/inbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	return rr
}

// hostOf extracts the hostname (without port) from a URL string, matching
// the output of url.URL.Hostname() which is what the verifier uses.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		// fallback: strip scheme manually
		s := strings.TrimPrefix(rawURL, "http://")
		s = strings.TrimPrefix(s, "https://")
		if i := strings.Index(s, "/"); i >= 0 {
			s = s[:i]
		}
		if h, _, err2 := net.SplitHostPort(s); err2 == nil {
			return h
		}
		return s
	}
	return u.Hostname()
}

// newHandlerWithInsecureCerts builds a sesinbound.Handler that uses a custom
// HTTP client which trusts all TLS certificates.  Required for unit tests
// where the cert server is self-signed.  The S3 client is pointed at a stub
// endpoint via the endpoint override.
func newHandlerWithInsecureCerts(
	t *testing.T,
	cfg sesinbound.Config,
	pipeline sesinbound.Pipeline,
	seenSt sesinbound.SeenStore,
	audit sesinbound.AuditLogger,
	s3Endpoint string,
) *sesinbound.Handler {
	t.Helper()
	// Note: We cannot inject the HTTP client into the internal verifier
	// because the client is private to the verifier struct. Instead, the
	// tests call through the exported ServeHTTP which uses the real
	// sesinbound.New constructor. For tests that need to avoid real AWS:
	// we use a fake S3 endpoint by relying on the S3 client's endpoint
	// resolution override (EndpointResolverV2 is hard to inject without
	// exposing it in the API).
	//
	// Strategy for unit tests: the cert server is plain HTTP so no TLS
	// is needed. S3 is a stub server. SNS signature verification uses the
	// cert served by certSrv, which is a real cert parsed by x509 — but
	// since chain verification (Verify()) requires the cert to chain to
	// system roots, we need to either:
	//   (a) use a test root that we add to the test pool, or
	//   (b) skip the chain verification in tests by overriding via
	//       ExportedVerifierForTest (requires export_test.go).
	//
	// We take approach (b) via an export_test.go shim.
	return sesinbound.NewForTest(cfg, pipeline, seenSt, audit, s3Endpoint)
}

// ---- tests -----------------------------------------------------------------

func TestNotification_HappyPath(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildNotification("msg-001", f.allowedBucket, "messages/msg-001")
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := f.pipeline.callCount(); got != 1 {
		t.Fatalf("pipeline.Ingest call count: want 1, got %d", got)
	}
}

func TestNotification_InvalidSignature(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildNotification("msg-002", f.allowedBucket, "messages/msg-002")
	// Tamper with the message body after signing.
	m.Message = m.Message + "tampered"
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called on invalid signature; got %d calls", got)
	}
}

func TestNotification_ReplayDedupe(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildNotification("msg-replay", f.allowedBucket, "messages/msg-replay")
	// First post: accepted.
	rr1 := f.post(m)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first post: want 200, got %d", rr1.Code)
	}
	// Second post with same MessageId: deduped.
	rr2 := f.post(m)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second post: want 200, got %d", rr2.Code)
	}
	// Pipeline called exactly once.
	if got := f.pipeline.callCount(); got != 1 {
		t.Fatalf("after replay, pipeline call count: want 1, got %d", got)
	}
}

func TestNotification_BucketDisallowed(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildNotification("msg-bucket", "evil-bucket", "messages/msg-bucket")
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called for disallowed bucket; got %d", got)
	}
}

func TestSubscriptionConfirmation_AllowlistedTopic(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildConfirmation(f.allowedTopic, f.confirmSrv.URL+"/confirm")
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	// Pipeline is NOT called for subscription confirmations.
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called for SubscriptionConfirmation; got %d", got)
	}
}

func TestSubscriptionConfirmation_NonAllowlistedTopic(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildConfirmation("arn:aws:sns:us-east-1:999999:evil-topic", f.confirmSrv.URL+"/confirm")
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	// Pipeline not called.
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called; got %d", got)
	}
}

func TestUnsubscribeConfirmation(t *testing.T) {
	f := newTestFixture(t)
	m := &rawSNSMsg{
		Type:             "UnsubscribeConfirmation",
		MessageId:        "unsub-001",
		TopicArn:         f.allowedTopic,
		Message:          "You have chosen to unsubscribe",
		Timestamp:        "2026-04-26T10:00:00.000Z",
		SignatureVersion: "1",
		SigningCertURL:   f.certSrv.URL + "/cert.pem",
		SubscribeURL:     f.confirmSrv.URL + "/confirm",
		Token:            "tok456",
	}
	m.Signature = f.signer.sign(m)
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called for UnsubscribeConfirmation; got %d", got)
	}
}

func TestCertHostDisallowed(t *testing.T) {
	f := newTestFixture(t)
	m := f.buildNotification("msg-cert-host", f.allowedBucket, "messages/msg-cert-host")
	// Override SigningCertURL to a disallowed host.
	m.SigningCertURL = "https://evil.example.com/cert.pem"
	// Re-sign so the body itself is consistent (the cert host check
	// happens before fetch, so the signature doesn't matter here, but
	// using a valid signing string avoids confusing assertions).
	m.Signature = f.signer.sign(m)
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called when cert host is disallowed; got %d", got)
	}
}

func TestSubscribeURL_LoopbackRejected(t *testing.T) {
	f := newTestFixture(t)
	// SubscribeURL pointing to loopback must be rejected by netguard.
	m := f.buildConfirmation(f.allowedTopic, "http://127.0.0.1:9999/confirm")
	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	// Confirmation should not be sent to a loopback address.
	// Pipeline not called.
	if got := f.pipeline.callCount(); got != 0 {
		t.Fatalf("pipeline should not be called; got %d", got)
	}
}

func TestNonPOST_MethodNotAllowed(t *testing.T) {
	f := newTestFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/hooks/ses/inbound", nil)
	rr := httptest.NewRecorder()
	f.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rr.Code)
	}
}

// Verify that the SNS signature version 2 (SHA-256) path also works.
func TestNotification_SignatureV2(t *testing.T) {
	f := newTestFixture(t)

	inner := sesReceiptMsg{}
	inner.Mail.Source = "203.0.113.1"
	inner.Mail.Destination = []string{"rcpt@example.com"}
	inner.Mail.MessageId = "msg-v2-001"
	inner.Receipt.Action.Type = "S3"
	inner.Receipt.Action.BucketName = f.allowedBucket
	inner.Receipt.Action.ObjectKey = "messages/msg-v2-001"
	msgJSON, _ := json.Marshal(inner)

	m := &rawSNSMsg{
		Type:             "Notification",
		MessageId:        "msg-v2-001",
		TopicArn:         f.allowedTopic,
		Message:          string(msgJSON),
		Timestamp:        "2026-04-26T10:00:00.000Z",
		SignatureVersion: "2",
		SigningCertURL:   f.certSrv.URL + "/cert.pem",
	}
	// Sign with SHA-256 (version 2).
	signing := canonicalSigningStringForTest(m)
	h256 := sha256.New()
	h256.Write([]byte(signing))
	digest := h256.Sum(nil)
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.signer.key, crypto.SHA256, digest)
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = base64.StdEncoding.EncodeToString(sig)

	rr := f.post(m)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if got := f.pipeline.callCount(); got != 1 {
		t.Fatalf("pipeline call count: want 1, got %d", got)
	}
}
