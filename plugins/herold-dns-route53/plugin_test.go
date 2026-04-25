package main_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// Route53 speaks REST+XML. Three operations are exercised by the
// plugin: ListHostedZonesByName (GET /2013-04-01/hostedzonesbyname),
// ChangeResourceRecordSets (POST /2013-04-01/hostedzone/{id}/rrset)
// and ListResourceRecordSets (GET /2013-04-01/hostedzone/{id}/rrset).
// fakeRoute53 stands those endpoints up over httptest, parses just
// enough of the request bodies to make assertions easy, and emits the
// XML wire format the SDK's deserialiser expects.

const (
	r53PathHostedZonesByName = "/2013-04-01/hostedzonesbyname"
	r53PathRRSetPrefix       = "/2013-04-01/hostedzone/"
	r53PathRRSetSuffix       = "/rrset"
)

type capturedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
	Change *changeBatch // populated for POST /rrset
}

// changeBatch is the minimal subset of ChangeResourceRecordSetsRequest
// the tests inspect. It is populated by parsing the XML body.
type changeBatch struct {
	XMLName xml.Name `xml:"ChangeResourceRecordSetsRequest"`
	Changes struct {
		Changes []struct {
			Action            string `xml:"Action"`
			ResourceRecordSet struct {
				Name            string `xml:"Name"`
				Type            string `xml:"Type"`
				TTL             int64  `xml:"TTL"`
				ResourceRecords struct {
					Records []struct {
						Value string `xml:"Value"`
					} `xml:"ResourceRecord"`
				} `xml:"ResourceRecords"`
			} `xml:"ResourceRecordSet"`
		} `xml:"Change"`
	} `xml:"ChangeBatch>Changes"`
}

// firstChange returns the first Change from the parsed batch and a
// bool indicating whether one was present.
func (c *changeBatch) firstChange() (action, name, rrtype, value string, ttl int64, ok bool) {
	if c == nil || len(c.Changes.Changes) == 0 {
		return
	}
	ch := c.Changes.Changes[0]
	rs := ch.ResourceRecordSet
	if len(rs.ResourceRecords.Records) == 0 {
		return ch.Action, rs.Name, rs.Type, "", rs.TTL, true
	}
	return ch.Action, rs.Name, rs.Type, rs.ResourceRecords.Records[0].Value, rs.TTL, true
}

type fakeRoute53 struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []capturedRequest
	handler  func(req capturedRequest) (status int, body string, contentType string)

	calls int64
}

func newFakeRoute53(t *testing.T) *fakeRoute53 {
	t.Helper()
	f := &fakeRoute53{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.calls, 1)
		body, _ := io.ReadAll(r.Body)
		cap := capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, r53PathRRSetSuffix) {
			var cb changeBatch
			if err := xml.Unmarshal(body, &cb); err == nil {
				cap.Change = &cb
			}
		}
		f.mu.Lock()
		f.requests = append(f.requests, cap)
		h := f.handler
		f.mu.Unlock()
		if h == nil {
			http.Error(w, errorXML("InternalError", "no handler"), http.StatusInternalServerError)
			return
		}
		status, payload, ct := h(cap)
		if ct == "" {
			ct = "text/xml"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRoute53) setHandler(h func(req capturedRequest) (int, string, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
}

func (f *fakeRoute53) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
	atomic.StoreInt64(&f.calls, 0)
}

func (f *fakeRoute53) snapshot() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capturedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// changeOK returns the canned XML body for a successful
// ChangeResourceRecordSets response. The plugin only inspects ChangeInfo
// existence so a minimal payload is enough.
func changeOK() string {
	return `<?xml version="1.0"?>
<ChangeResourceRecordSetsResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <ChangeInfo>
    <Id>/change/C-FAKE</Id>
    <Status>INSYNC</Status>
    <SubmittedAt>2026-01-01T00:00:00Z</SubmittedAt>
  </ChangeInfo>
</ChangeResourceRecordSetsResponse>`
}

// hostedZoneOK returns a ListHostedZonesByName response containing one
// zone with the supplied name + id.
func hostedZoneOK(zoneName, zoneID string) string {
	if !strings.HasSuffix(zoneName, ".") {
		zoneName += "."
	}
	return fmt.Sprintf(`<?xml version="1.0"?>
<ListHostedZonesByNameResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <HostedZones>
    <HostedZone>
      <Id>/hostedzone/%s</Id>
      <Name>%s</Name>
      <CallerReference>test</CallerReference>
    </HostedZone>
  </HostedZones>
  <DNSName>%s</DNSName>
  <IsTruncated>false</IsTruncated>
  <MaxItems>2</MaxItems>
</ListHostedZonesByNameResponse>`, zoneID, zoneName, zoneName)
}

// listRecordsXML renders a ListResourceRecordSets response.
func listRecordsXML(records []recordSet) string {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?>
<ListResourceRecordSetsResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <ResourceRecordSets>`)
	for _, r := range records {
		b.WriteString(fmt.Sprintf(`
    <ResourceRecordSet>
      <Name>%s</Name>
      <Type>%s</Type>
      <TTL>%d</TTL>
      <ResourceRecords><ResourceRecord><Value>%s</Value></ResourceRecord></ResourceRecords>
    </ResourceRecordSet>`, r.Name, r.Type, r.TTL, r.Value))
	}
	b.WriteString(`
  </ResourceRecordSets>
  <IsTruncated>false</IsTruncated>
  <MaxItems>100</MaxItems>
</ListResourceRecordSetsResponse>`)
	return b.String()
}

type recordSet struct {
	Name  string
	Type  string
	TTL   int
	Value string
}

// errorXML returns a Route53-shaped wrapped error body. The deserialiser
// expects <ErrorResponse><Error><Code>...</Code><Message>...</Message></Error>.
func errorXML(code, msg string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<ErrorResponse><Error><Code>%s</Code><Message>%s</Message></Error><RequestId>req-fake</RequestId></ErrorResponse>`, code, msg)
}

// spawnedPlugin owns the plugin process + client. The shape mirrors the
// Cloudflare/Hetzner plugin tests so all four DNS plugin tests read the
// same way.
type spawnedPlugin struct {
	t      *testing.T
	cmd    *exec.Cmd
	client *plug.Client
	done   chan error
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func buildPluginBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "herold-dns-route53-bin-")
		if err != nil {
			binErr = err
			return
		}
		bin := filepath.Join(dir, "herold-dns-route53")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/plugins/herold-dns-route53")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
			return
		}
		binPath = bin
	})
	if binErr != nil {
		t.Fatalf("build plugin: %v", binErr)
	}
	return binPath
}

// spawnPlugin builds the plugin once per test binary, starts a fresh
// process, drives initialize + configure, and returns a live Client.
// AWS credential resolution is short-circuited by injecting static
// values via env vars; the route53 endpoint is overridden via the
// configure-time endpoint_url option (preferred) — falling back to the
// AWS_ENDPOINT_URL env var would also work because aws-sdk-go-v2 honours
// it, but the option keeps the test-only override visible to the
// configure handler. See plugin's main.go: cfg.endpointURL is wired into
// route53.Options.BaseEndpoint.
func spawnPlugin(t *testing.T, configureOpts map[string]any) *spawnedPlugin {
	t.Helper()
	bin := buildPluginBinary(t)

	cmd := exec.Command(bin)
	// Inherit AWS_* env from setupAWSEnv so config.LoadDefaultConfig
	// finds static credentials and skips IMDS / shared-config probing.
	cmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID=test-akid",
		"AWS_SECRET_ACCESS_KEY=test-secret",
		"AWS_REGION=us-east-1",
		"AWS_EC2_METADATA_DISABLED=true",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	client := plug.NewClient(stdout, stdin, plug.ClientOptions{
		Name:          "herold-dns-route53",
		MaxConcurrent: 8,
	})
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()

	sp := &spawnedPlugin{t: t, cmd: cmd, client: client, done: done}

	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res plug.InitializeResult
		if err := client.Call(ctx, plug.MethodInitialize, plug.InitializeParams{
			ServerVersion: "test",
			ABIVersion:    plug.ABIVersion,
		}, &res); err != nil {
			sp.close()
			t.Fatalf("initialize: %v", err)
		}
		if res.Manifest.Name != "herold-dns-route53" {
			sp.close()
			t.Fatalf("manifest.Name = %q", res.Manifest.Name)
		}
	}
	if configureOpts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res plug.ConfigureResult
		if err := client.Call(ctx, plug.MethodConfigure, plug.ConfigureParams{Options: configureOpts}, &res); err != nil {
			sp.close()
			t.Fatalf("configure: %v", err)
		}
	}
	t.Cleanup(sp.close)
	return sp
}

func (s *spawnedPlugin) close() {
	if p, ok := s.cmd.Stdin.(io.Closer); ok {
		_ = p.Close()
	}
	waited := make(chan error, 1)
	go func() { waited <- s.cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-waited
	}
}

func (s *spawnedPlugin) present(t *testing.T, in sdk.DNSPresentParams) (sdk.DNSPresentResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res sdk.DNSPresentResult
	err := s.client.Call(ctx, sdk.MethodDNSPresent, in, &res)
	return res, err
}

func (s *spawnedPlugin) cleanup(t *testing.T, id string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res map[string]any
	return s.client.Call(ctx, sdk.MethodDNSCleanup, sdk.DNSCleanupParams{ID: id}, &res)
}

func (s *spawnedPlugin) list(t *testing.T, in sdk.DNSListParams) ([]sdk.DNSRecord, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var res []sdk.DNSRecord
	err := s.client.Call(ctx, sdk.MethodDNSList, in, &res)
	return res, err
}

// configuredOpts returns the standard option map for tests with the
// fake server pointed at via endpoint_url and zero propagation/timeouts
// so tests do not spend real wall time waiting.
func configuredOpts(endpoint, hostedZoneID string) map[string]any {
	return map[string]any{
		"aws_region":               "us-east-1",
		"hosted_zone_id":           hostedZoneID,
		"endpoint_url":             endpoint,
		"propagation_wait_seconds": 0,
		"request_timeout_seconds":  5,
		"retry_attempts":           2,
		"default_ttl":              300,
	}
}

// TestPresent_TXT_Success drives a single TXT present and asserts the
// fake observed an UPSERT (the plugin maps CREATE→UPSERT to honour
// "present" semantics for ACME callers — see plugin's upsert).
func TestPresent_TXT_Success(t *testing.T) {
	fake := newFakeRoute53(t)
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.Path, r53PathRRSetSuffix) {
			return http.StatusOK, changeOK(), ""
		}
		return http.StatusBadRequest, errorXML("InvalidInput", "unexpected"), ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, "Z123ABCDEF"))

	res, err := p.present(t, sdk.DNSPresentParams{
		Zone:       "example.com",
		RecordType: "TXT",
		Name:       "_acme-challenge.example.com",
		Value:      "abc123",
		TTL:        60,
	})
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("present: empty id")
	}
	reqs := fake.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", r.Method)
	}
	if !strings.HasPrefix(r.Path, r53PathRRSetPrefix+"Z123ABCDEF") {
		t.Fatalf("path = %s, want hostedzone/Z123ABCDEF/rrset", r.Path)
	}
	action, name, rrtype, value, ttl, ok := r.Change.firstChange()
	if !ok {
		t.Fatalf("no change parsed from body: %s", r.Body)
	}
	if action != "UPSERT" {
		t.Fatalf("action = %s, want UPSERT", action)
	}
	if rrtype != "TXT" {
		t.Fatalf("type = %s", rrtype)
	}
	if name != "_acme-challenge.example.com" {
		t.Fatalf("name = %s", name)
	}
	// TXT values are wrapped in double quotes per RFC 1035.
	if value != `"abc123"` {
		t.Fatalf("value = %q, want %q", value, `"abc123"`)
	}
	if ttl != 60 {
		t.Fatalf("ttl = %d, want 60", ttl)
	}
}

// TestPresent_AllRecordTypes covers A, AAAA, MX, TLSA. Each subtest
// reuses the same configured plugin so the binary build cost is paid
// once.
func TestPresent_AllRecordTypes(t *testing.T) {
	fake := newFakeRoute53(t)
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.Path, r53PathRRSetSuffix) {
			return http.StatusOK, changeOK(), ""
		}
		return http.StatusBadRequest, errorXML("InvalidInput", "unexpected"), ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, "Z123ABCDEF"))

	cases := []struct {
		recordType string
		name       string
		value      string
	}{
		{"A", "host.example.com", "192.0.2.1"},
		{"AAAA", "host.example.com", "2001:db8::1"},
		{"MX", "example.com", "10 mail.example.com"},
		{"TLSA", "_25._tcp.mail.example.com", "3 1 1 abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.recordType, func(t *testing.T) {
			fake.reset()
			res, err := p.present(t, sdk.DNSPresentParams{
				Zone:       "example.com",
				RecordType: tc.recordType,
				Name:       tc.name,
				Value:      tc.value,
				TTL:        300,
			})
			if err != nil {
				t.Fatalf("present(%s): %v", tc.recordType, err)
			}
			if res.ID == "" {
				t.Fatalf("present(%s): empty id", tc.recordType)
			}
			reqs := fake.snapshot()
			if len(reqs) != 1 {
				t.Fatalf("got %d requests, want 1", len(reqs))
			}
			action, name, rrtype, value, _, ok := reqs[0].Change.firstChange()
			if !ok {
				t.Fatalf("no change parsed: %s", reqs[0].Body)
			}
			if action != "UPSERT" {
				t.Fatalf("action = %s", action)
			}
			if rrtype != tc.recordType {
				t.Fatalf("type = %s, want %s", rrtype, tc.recordType)
			}
			if name != tc.name {
				t.Fatalf("name = %s, want %s", name, tc.name)
			}
			// Non-TXT values pass through unchanged.
			if value != tc.value {
				t.Fatalf("value = %q, want %q", value, tc.value)
			}
		})
	}
}

// TestCleanup_Removes asserts dns.cleanup first reads the existing
// record (Route53 requires the exact value+TTL on DELETE) then issues
// a DELETE Change.
func TestCleanup_Removes(t *testing.T) {
	fake := newFakeRoute53(t)
	const zoneID = "Z123ABCDEF"

	fake.setHandler(func(req capturedRequest) (int, string, string) {
		switch {
		case req.Method == http.MethodGet && strings.HasSuffix(req.Path, r53PathRRSetSuffix):
			return http.StatusOK, listRecordsXML([]recordSet{
				{Name: "_acme-challenge.example.com.", Type: "TXT", TTL: 60, Value: `"abc123"`},
			}), ""
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, r53PathRRSetSuffix):
			return http.StatusOK, changeOK(), ""
		}
		return http.StatusBadRequest, errorXML("InvalidInput", "unexpected"), ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, zoneID))

	// Cleanup id format is hzID|name|TYPE|value (see encodeID).
	id := fmt.Sprintf("%s|%s|%s|%s", zoneID, "_acme-challenge.example.com", "TXT", `"abc123"`)
	if err := p.cleanup(t, id); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	reqs := fake.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2 (lookup + delete)", len(reqs))
	}
	if reqs[0].Method != http.MethodGet {
		t.Fatalf("first req method = %s, want GET", reqs[0].Method)
	}
	if reqs[1].Method != http.MethodPost {
		t.Fatalf("second req method = %s, want POST", reqs[1].Method)
	}
	action, _, rrtype, value, _, ok := reqs[1].Change.firstChange()
	if !ok {
		t.Fatalf("no change parsed: %s", reqs[1].Body)
	}
	if action != "DELETE" {
		t.Fatalf("action = %s, want DELETE", action)
	}
	if rrtype != "TXT" {
		t.Fatalf("type = %s, want TXT", rrtype)
	}
	if value != `"abc123"` {
		t.Fatalf("value = %q", value)
	}
}

// TestList_EnumeratesRecords pre-populates the fake with two A records
// and asserts both come back from dns.list with stable ids.
func TestList_EnumeratesRecords(t *testing.T) {
	fake := newFakeRoute53(t)
	const zoneID = "Z123ABCDEF"
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		if req.Method != http.MethodGet || !strings.HasSuffix(req.Path, r53PathRRSetSuffix) {
			return http.StatusBadRequest, errorXML("InvalidInput", "bad"), ""
		}
		return http.StatusOK, listRecordsXML([]recordSet{
			{Name: "host.example.com.", Type: "A", TTL: 300, Value: "192.0.2.1"},
			{Name: "host.example.com.", Type: "A", TTL: 300, Value: "192.0.2.2"},
		}), ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, zoneID))

	recs, err := p.list(t, sdk.DNSListParams{Zone: "example.com", RecordType: "A", Name: "host.example.com"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(recs), recs)
	}
	got := map[string]struct{}{recs[0].Value: {}, recs[1].Value: {}}
	if _, ok := got["192.0.2.1"]; !ok {
		t.Fatalf("missing 192.0.2.1 in %+v", recs)
	}
	if _, ok := got["192.0.2.2"]; !ok {
		t.Fatalf("missing 192.0.2.2 in %+v", recs)
	}
	for _, r := range recs {
		if r.ID == "" {
			t.Fatalf("empty id in %+v", r)
		}
		if r.TTL != 300 {
			t.Fatalf("ttl = %d, want 300", r.TTL)
		}
	}
}

// TestAuthFailure_403_MapsToError asserts a 403 from Route53 (e.g.
// SignatureDoesNotMatch) surfaces as a structured RPC error and that
// the SDK does not retry on 4xx.
func TestAuthFailure_403_MapsToError(t *testing.T) {
	fake := newFakeRoute53(t)
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		return http.StatusForbidden, errorXML("InvalidSignature", "request signature invalid"), ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, "Z123ABCDEF"))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T, want *plug.Error: %v", err, err)
	}
	// Plugin wraps with "ChangeResourceRecordSets:" — assert the
	// service-level signal makes it through.
	msg := strings.ToLower(rpcErr.Message)
	if !strings.Contains(msg, "invalidsignature") && !strings.Contains(msg, "signature") &&
		!strings.Contains(msg, "403") && !strings.Contains(msg, "forbidden") {
		t.Fatalf("msg = %q, want auth failure indicator", rpcErr.Message)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 1 {
		t.Fatalf("got %d calls, want 1 (no retry on 4xx)", n)
	}
}

// TestServerError_5xx_RetryThenSuccess asserts the SDK's built-in retry
// kicks in: first call 503, second call 200. retry_attempts=2 in
// configuredOpts means up to 3 total attempts.
func TestServerError_5xx_RetryThenSuccess(t *testing.T) {
	fake := newFakeRoute53(t)
	var step int64
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		n := atomic.AddInt64(&step, 1)
		if n == 1 {
			return http.StatusServiceUnavailable, errorXML("ServiceUnavailable", "boom"), ""
		}
		return http.StatusOK, changeOK(), ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, "Z123ABCDEF"))

	res, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	})
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("empty id")
	}
	if n := atomic.LoadInt64(&fake.calls); n != 2 {
		t.Fatalf("got %d calls, want 2 (one retry on 5xx)", n)
	}
}

// TestAutoDiscoverZoneID asserts the plugin resolves the hosted zone
// via ListHostedZonesByName when hosted_zone_id is unset, then reuses
// the discovered id for subsequent calls. The plugin caches the id
// inside h.opts.hostedZoneID after a successful lookup is currently NOT
// done — resolveHostedZone re-queries every call when hosted_zone_id is
// empty. See finding R53-1 in test/e2e/findings.md; this test asserts
// the present-time behaviour (one lookup + one create per call).
func TestAutoDiscoverZoneID(t *testing.T) {
	fake := newFakeRoute53(t)
	const discoveredZone = "ZDISCOVER1"
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		switch {
		case req.Method == http.MethodGet && req.Path == r53PathHostedZonesByName:
			if got := req.Query.Get("dnsname"); got != "example.com." {
				t.Errorf("dnsname=%q, want example.com.", got)
			}
			return http.StatusOK, hostedZoneOK("example.com", discoveredZone), ""
		case req.Method == http.MethodPost && strings.HasSuffix(req.Path, r53PathRRSetSuffix):
			if !strings.Contains(req.Path, "/"+discoveredZone+"/") {
				t.Errorf("create path=%s, want zone %s", req.Path, discoveredZone)
			}
			return http.StatusOK, changeOK(), ""
		}
		return http.StatusBadRequest, errorXML("InvalidInput", "unexpected "+req.Method+" "+req.Path), ""
	})

	// hosted_zone_id intentionally omitted to force discovery.
	opts := map[string]any{
		"aws_region":               "us-east-1",
		"endpoint_url":             fake.server.URL,
		"propagation_wait_seconds": 0,
		"request_timeout_seconds":  5,
		"retry_attempts":           1,
		"default_ttl":              300,
	}
	p := spawnPlugin(t, opts)

	// First call: lookup + create (2 requests).
	res, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "x", TTL: 60,
	})
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("empty id")
	}
	reqs := fake.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("first call: got %d requests, want 2 (lookup + create)", len(reqs))
	}
	if reqs[0].Path != r53PathHostedZonesByName {
		t.Fatalf("first request path = %s, want %s", reqs[0].Path, r53PathHostedZonesByName)
	}
	if !strings.Contains(reqs[1].Path, "/"+discoveredZone+"/") {
		t.Fatalf("second request path = %s, want zone %s", reqs[1].Path, discoveredZone)
	}

	// Second call: the plugin currently does NOT cache the discovered
	// id (resolveHostedZone re-queries every call when hosted_zone_id
	// is empty), so we expect another lookup + create. See finding
	// R53-1 in test/e2e/findings.md.
	fake.reset()
	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge.example.com", Value: "y", TTL: 60,
	}); err != nil {
		t.Fatalf("second present: %v", err)
	}
	reqs2 := fake.snapshot()
	if len(reqs2) != 2 {
		t.Fatalf("second call: got %d requests, want 2 (lookup + create per finding R53-1)", len(reqs2))
	}
}

// TestUnknownRecordType asserts an unsupported record type is rejected
// before any provider call leaves the plugin.
func TestUnknownRecordType(t *testing.T) {
	fake := newFakeRoute53(t)
	fake.setHandler(func(req capturedRequest) (int, string, string) {
		t.Errorf("unexpected provider call: %s %s", req.Method, req.Path)
		return http.StatusOK, "", ""
	})

	p := spawnPlugin(t, configuredOpts(fake.server.URL, "Z123ABCDEF"))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "PTR", Name: "x.example.com", Value: "v", TTL: 60,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T", err)
	}
	if !strings.Contains(rpcErr.Message, "PTR") && !strings.Contains(rpcErr.Message, "unsupported") {
		t.Fatalf("msg = %q", rpcErr.Message)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 0 {
		t.Fatalf("provider got %d calls, want 0", n)
	}
}
