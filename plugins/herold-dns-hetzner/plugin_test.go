package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// fakeCloudDNS is a stateful stub of the Hetzner Cloud DNS API: zones,
// RRSets with add_records / remove_records / set_records / change_ttl
// actions, rrset creation, and the /actions list used to poll action
// completion. Every request must carry the expected Bearer token.
type fakeCloudDNS struct {
	t      *testing.T
	server *httptest.Server
	token  string

	mu       sync.Mutex
	zones    []fakeZone
	rrsets   map[string]*fakeRRSet // "<zoneID>/<name>/<type>"
	requests []capturedRequest
	actionID int64
	// fail, when set, answers matching requests with an API error.
	fail func(req capturedRequest) (status int, code string, ok bool)
	// runningActions makes actions start as "running" so the plugin
	// has to poll /actions until they finish.
	runningActions bool

	calls int64
}

type fakeZone struct {
	ID   int64
	Name string
	TTL  int
}

type fakeRRSet struct {
	Name    string
	Type    string
	TTL     *int
	Records []string
}

type capturedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

func newFakeCloudDNS(t *testing.T, token string, zones ...fakeZone) *fakeCloudDNS {
	t.Helper()
	f := &fakeCloudDNS{t: t, token: token, zones: zones, rrsets: map[string]*fakeRRSet{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": code, "details": nil}})
}

var meta = map[string]any{"pagination": map[string]any{
	"page": 1, "per_page": 100, "previous_page": nil, "next_page": nil, "last_page": 1, "total_entries": 1,
}}

func (f *fakeCloudDNS) serve(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&f.calls, 1)
	body, _ := io.ReadAll(r.Body)
	req := capturedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)

	if r.Header.Get("Authorization") != "Bearer "+f.token {
		apiErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if f.fail != nil {
		if status, code, ok := f.fail(req); ok {
			apiErr(w, status, code)
			return
		}
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "actions":
		var actions []map[string]any
		for _, s := range r.URL.Query()["id"] {
			id, _ := strconv.ParseInt(s, 10, 64)
			actions = append(actions, f.action(id, "success"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"actions": actions, "meta": meta})
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "zones":
		name := r.URL.Query().Get("name")
		zones := []map[string]any{}
		for _, z := range f.zones {
			if name == "" || z.Name == name {
				zones = append(zones, zoneJSON(z))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"zones": zones, "meta": meta})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "zones":
		z, ok := f.zone(parts[1])
		if !ok {
			apiErr(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"zone": zoneJSON(z)})
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "rrsets":
		z, ok := f.zone(parts[1])
		if !ok {
			apiErr(w, http.StatusNotFound, "not_found")
			return
		}
		f.serveRRSets(w, r, z, parts[3:], body)
	default:
		apiErr(w, http.StatusNotFound, "not_found")
	}
}

func (f *fakeCloudDNS) serveRRSets(w http.ResponseWriter, r *http.Request, z fakeZone, rest []string, body []byte) {
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		q := r.URL.Query()
		sets := []map[string]any{}
		for _, rs := range f.sortedRRSets(z.ID) {
			if q.Get("name") != "" && rs.Name != q.Get("name") {
				continue
			}
			if types := q["type"]; len(types) > 0 && !slices.Contains(types, rs.Type) {
				continue
			}
			sets = append(sets, rrsetJSON(z, rs))
		}
		writeJSON(w, http.StatusOK, map[string]any{"rrsets": sets, "meta": meta})
	case len(rest) == 0 && r.Method == http.MethodPost:
		var in struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			TTL     *int   `json:"ttl"`
			Records []struct {
				Value string `json:"value"`
			} `json:"records"`
		}
		_ = json.Unmarshal(body, &in)
		key := rrKey(z.ID, in.Name, in.Type)
		if _, exists := f.rrsets[key]; exists {
			apiErr(w, http.StatusConflict, "uniqueness_error")
			return
		}
		rs := &fakeRRSet{Name: in.Name, Type: in.Type, TTL: in.TTL}
		for _, rec := range in.Records {
			rs.Records = append(rs.Records, rec.Value)
		}
		f.rrsets[key] = rs
		writeJSON(w, http.StatusCreated, map[string]any{"rrset": rrsetJSON(z, rs), "action": f.newAction("create_rrset")})
	case len(rest) == 2 && r.Method == http.MethodGet:
		rs, ok := f.rrsets[rrKey(z.ID, rest[0], rest[1])]
		if !ok {
			apiErr(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rrset": rrsetJSON(z, rs)})
	case len(rest) == 4 && rest[2] == "actions" && r.Method == http.MethodPost:
		f.serveRRSetAction(w, z, rest[0], rest[1], rest[3], body)
	default:
		apiErr(w, http.StatusNotFound, "not_found")
	}
}

func (f *fakeCloudDNS) serveRRSetAction(w http.ResponseWriter, z fakeZone, name, typ, action string, body []byte) {
	var in struct {
		TTL     *int `json:"ttl"`
		Records []struct {
			Value string `json:"value"`
		} `json:"records"`
	}
	_ = json.Unmarshal(body, &in)
	key := rrKey(z.ID, name, typ)
	rs, exists := f.rrsets[key]
	switch action {
	case "add_records":
		if !exists {
			rs = &fakeRRSet{Name: name, Type: typ, TTL: in.TTL}
			f.rrsets[key] = rs
		} else if in.TTL != nil && rs.TTL != nil && *in.TTL != *rs.TTL {
			apiErr(w, http.StatusUnprocessableEntity, "invalid_input")
			return
		}
		for _, rec := range in.Records {
			if !slices.Contains(rs.Records, rec.Value) {
				rs.Records = append(rs.Records, rec.Value)
			}
		}
	case "remove_records":
		if !exists {
			apiErr(w, http.StatusNotFound, "not_found")
			return
		}
		for _, rec := range in.Records {
			rs.Records = slices.DeleteFunc(rs.Records, func(v string) bool { return v == rec.Value })
		}
		if len(rs.Records) == 0 {
			delete(f.rrsets, key)
		}
	case "set_records":
		if !exists {
			apiErr(w, http.StatusNotFound, "not_found")
			return
		}
		rs.Records = nil
		for _, rec := range in.Records {
			rs.Records = append(rs.Records, rec.Value)
		}
	case "change_ttl":
		if !exists {
			apiErr(w, http.StatusNotFound, "not_found")
			return
		}
		rs.TTL = in.TTL
	default:
		apiErr(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"action": f.newAction(action)})
}

func (f *fakeCloudDNS) newAction(command string) map[string]any {
	f.actionID++
	status := "success"
	if f.runningActions {
		status = "running"
	}
	a := f.action(f.actionID, status)
	a["command"] = command
	return a
}

func (f *fakeCloudDNS) action(id int64, status string) map[string]any {
	var finished any
	if status != "running" {
		finished = "2026-01-01T00:00:01Z"
	}
	return map[string]any{
		"id": id, "command": "rrset_action", "status": status, "progress": 100,
		"started": "2026-01-01T00:00:00Z", "finished": finished,
		"resources": []map[string]any{{"id": 1, "type": "zone"}}, "error": nil,
	}
}

func (f *fakeCloudDNS) zone(idOrName string) (fakeZone, bool) {
	for _, z := range f.zones {
		if strconv.FormatInt(z.ID, 10) == idOrName || z.Name == idOrName {
			return z, true
		}
	}
	return fakeZone{}, false
}

func (f *fakeCloudDNS) sortedRRSets(zoneID int64) []*fakeRRSet {
	var keys []string
	prefix := strconv.FormatInt(zoneID, 10) + "/"
	for k := range f.rrsets {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := make([]*fakeRRSet, 0, len(keys))
	for _, k := range keys {
		out = append(out, f.rrsets[k])
	}
	return out
}

// records returns the values stored in RRSet (name, type) of zoneID.
func (f *fakeCloudDNS) records(zoneID int64, name, typ string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	rs, ok := f.rrsets[rrKey(zoneID, name, typ)]
	if !ok {
		return nil
	}
	return slices.Clone(rs.Records)
}

func (f *fakeCloudDNS) seed(zoneID int64, name, typ string, ttl int, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rrsets[rrKey(zoneID, name, typ)] = &fakeRRSet{Name: name, Type: typ, TTL: &ttl, Records: values}
}

func (f *fakeCloudDNS) setFail(fn func(req capturedRequest) (int, string, bool)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fn
}

func (f *fakeCloudDNS) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
	atomic.StoreInt64(&f.calls, 0)
}

func (f *fakeCloudDNS) snapshot() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func rrKey(zoneID int64, name, typ string) string {
	return fmt.Sprintf("%d/%s/%s", zoneID, name, typ)
}

func zoneJSON(z fakeZone) map[string]any {
	return map[string]any{
		"id": z.ID, "name": z.Name, "ttl": z.TTL, "mode": "primary", "status": "ok",
		"created": "2026-01-01T00:00:00Z", "labels": map[string]string{},
		"protection": map[string]any{"delete": false}, "record_count": 0, "registrar": "other",
		"primary_nameservers": []any{},
		"authoritative_nameservers": map[string]any{
			"assigned": []string{}, "delegated": []string{}, "delegation_last_check": nil, "delegation_status": "valid",
		},
	}
}

func rrsetJSON(z fakeZone, rs *fakeRRSet) map[string]any {
	recs := []map[string]any{}
	for _, v := range rs.Records {
		recs = append(recs, map[string]any{"value": v, "comment": ""})
	}
	return map[string]any{
		"id": rs.Name + "/" + rs.Type, "name": rs.Name, "type": rs.Type, "ttl": rs.TTL,
		"labels": map[string]string{}, "protection": map[string]any{"change": false},
		"records": recs, "zone": z.ID,
	}
}

type spawnedPlugin struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.Closer
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
		dir, err := os.MkdirTemp("", "herold-dns-hetzner-bin-")
		if err != nil {
			binErr = err
			return
		}
		bin := filepath.Join(dir, "herold-dns-hetzner")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "github.com/hanshuebner/herold/plugins/herold-dns-hetzner")
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

func spawnPlugin(t *testing.T, configureOpts map[string]any) *spawnedPlugin {
	t.Helper()
	bin := buildPluginBinary(t)

	cmd := exec.Command(bin)
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
		Name:          "herold-dns-hetzner",
		MaxConcurrent: 8,
	})
	done := make(chan error, 1)
	go func() { done <- client.Run(context.Background()) }()

	sp := &spawnedPlugin{t: t, cmd: cmd, stdin: stdin, client: client, done: done}

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
	// Closing the write end of the plugin's stdin delivers EOF, which
	// makes the plugin shut down.
	_ = s.stdin.Close()
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

func setEnv(t *testing.T, envVar, value string) {
	t.Helper()
	old, hadOld := os.LookupEnv(envVar)
	if err := os.Setenv(envVar, value); err != nil {
		t.Fatalf("setenv %s: %v", envVar, err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(envVar, old)
		} else {
			_ = os.Unsetenv(envVar)
		}
	})
}

const exampleZoneID = 4711

var exampleZone = fakeZone{ID: exampleZoneID, Name: "example.com", TTL: 3600}

// testOpts configures the plugin against the fake; zoneID "" exercises
// zone auto-discovery.
func testOpts(envVar, baseURL, zoneID string) map[string]any {
	opts := map[string]any{
		"api_token_env":            envVar,
		"base_url":                 baseURL,
		"propagation_wait_seconds": 0,
		"request_timeout_seconds":  5,
		"retry_attempts":           2,
	}
	if zoneID != "" {
		opts["zone_id"] = zoneID
	}
	return opts
}

func rpcError(t *testing.T, err error) *plug.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var rpcErr *plug.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err type = %T (%v)", err, err)
	}
	return rpcErr
}

// TestConfigure_ServerResolvedOptions configures the plugin the way
// herold does from system.toml: the "$VAR" reference in api_token_env
// arrives resolved to the token, and every option value is a string.
func TestConfigure_ServerResolvedOptions(t *testing.T) {
	fake := newFakeCloudDNS(t, "resolved-token-value", exampleZone)
	p := spawnPlugin(t, map[string]any{
		"api_token_env":            "resolved-token-value",
		"base_url":                 fake.server.URL,
		"zone_id":                  "example.com",
		"propagation_wait_seconds": "0",
		"request_timeout_seconds":  "5",
		"retry_attempts":           "1",
		"default_ttl":              "120",
	})
	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "x",
	}); err != nil {
		t.Fatalf("present: %v", err)
	}
	for _, r := range fake.snapshot() {
		if got := r.Header.Get("Authorization"); got != "Bearer resolved-token-value" {
			t.Fatalf("Authorization = %q", got)
		}
		if strings.HasSuffix(r.Path, "/add_records") && !strings.Contains(string(r.Body), `"ttl":120`) {
			t.Fatalf("add_records body = %s, want default_ttl 120", r.Body)
		}
	}
}

func TestPresent_RecordTypes(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_RECORDS"
	setEnv(t, env, "tok-records")
	fake := newFakeCloudDNS(t, "tok-records", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	cases := []struct {
		recordType string
		name       string
		value      string
		rrName     string
		stored     string
	}{
		{"TXT", "_acme-challenge.example.com", "abc123", "_acme-challenge", `"abc123"`},
		{"A", "host.example.com", "192.0.2.1", "host", "192.0.2.1"},
		{"AAAA", "host", "2001:db8::1", "host", "2001:db8::1"},
		{"MX", "@", "10 mail.example.com.", "@", "10 mail.example.com."},
		{"TLSA", "_25._tcp.mail.example.com", "3 1 1 abcd", "_25._tcp.mail", "3 1 1 abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.recordType, func(t *testing.T) {
			fake.reset()
			res, err := p.present(t, sdk.DNSPresentParams{
				Zone: "example.com", RecordType: tc.recordType, Name: tc.name, Value: tc.value, TTL: 300,
			})
			if err != nil {
				t.Fatalf("present(%s): %v", tc.recordType, err)
			}
			if res.ID == "" {
				t.Fatal("empty id")
			}
			if got := fake.records(exampleZoneID, tc.rrName, tc.recordType); !slices.Equal(got, []string{tc.stored}) {
				t.Fatalf("stored records = %q, want [%q]", got, tc.stored)
			}
			var add *capturedRequest
			for _, r := range fake.snapshot() {
				if got := r.Header.Get("Authorization"); got != "Bearer tok-records" {
					t.Fatalf("Authorization = %q", got)
				}
				if strings.HasSuffix(r.Path, "/actions/add_records") {
					add = &r
				}
			}
			if add == nil {
				t.Fatal("no add_records request")
			}
			wantPath := fmt.Sprintf("/zones/%d/rrsets/%s/%s/actions/add_records", exampleZoneID, tc.rrName, tc.recordType)
			if add.Path != wantPath {
				t.Fatalf("path = %s, want %s", add.Path, wantPath)
			}
			var body struct {
				TTL     int `json:"ttl"`
				Records []struct {
					Value string `json:"value"`
				} `json:"records"`
			}
			if err := json.Unmarshal(add.Body, &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.TTL != 300 || len(body.Records) != 1 || body.Records[0].Value != tc.stored {
				t.Fatalf("add_records body = %s", add.Body)
			}
		})
	}
}

// TestPresentCleanup_ConcurrentChallenges models two ACME challenges on
// the same name (e.g. a wildcard plus its base domain): both values
// coexist, and cleaning up one leaves the other in place.
func TestPresentCleanup_ConcurrentChallenges(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_CLEANUP"
	setEnv(t, env, "tok-cleanup")
	fake := newFakeCloudDNS(t, "tok-cleanup", exampleZone)
	fake.runningActions = true
	p := spawnPlugin(t, testOpts(env, fake.server.URL, ""))

	present := func(value string) string {
		res, err := p.present(t, sdk.DNSPresentParams{
			Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: value, TTL: 60,
		})
		if err != nil {
			t.Fatalf("present(%s): %v", value, err)
		}
		return res.ID
	}
	idA := present("token-a")
	idB := present("token-b")
	if got := fake.records(exampleZoneID, "_acme-challenge", "TXT"); !slices.Equal(got, []string{`"token-a"`, `"token-b"`}) {
		t.Fatalf("records after present = %q", got)
	}

	fake.reset()
	if err := p.cleanup(t, idA); err != nil {
		t.Fatalf("cleanup A: %v", err)
	}
	if got := fake.records(exampleZoneID, "_acme-challenge", "TXT"); !slices.Equal(got, []string{`"token-b"`}) {
		t.Fatalf("records after cleanup A = %q", got)
	}
	var sawRemove, sawPoll bool
	for _, r := range fake.snapshot() {
		switch {
		case r.Method == http.MethodPost && r.Path == fmt.Sprintf("/zones/%d/rrsets/_acme-challenge/TXT/actions/remove_records", exampleZoneID):
			sawRemove = true
			if !strings.Contains(string(r.Body), `"value":"\"token-a\""`) {
				t.Fatalf("remove_records body = %s", r.Body)
			}
		case r.Method == http.MethodGet && r.Path == "/actions":
			sawPoll = true
		case r.Method != http.MethodPost:
			t.Fatalf("unexpected request %s %s", r.Method, r.Path)
		}
	}
	if !sawRemove || !sawPoll {
		t.Fatalf("remove_records=%v action poll=%v", sawRemove, sawPoll)
	}

	if err := p.cleanup(t, idB); err != nil {
		t.Fatalf("cleanup B: %v", err)
	}
	if got := fake.records(exampleZoneID, "_acme-challenge", "TXT"); got != nil {
		t.Fatalf("records after cleanup B = %q, want RRSet gone", got)
	}
	// Cleaning up again finds no RRSet and succeeds.
	if err := p.cleanup(t, idB); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
}

// TestPresent_ExistingRRSetKeepsTTL asserts add_records on an existing
// RRSet sends that RRSet's TTL, which the API requires to match.
func TestPresent_ExistingRRSetKeepsTTL(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_TTL"
	setEnv(t, env, "tok-ttl")
	fake := newFakeCloudDNS(t, "tok-ttl", exampleZone)
	fake.seed(exampleZoneID, "_acme-challenge", "TXT", 120, `"older"`)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, "example.com"))

	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "newer", TTL: 60,
	}); err != nil {
		t.Fatalf("present: %v", err)
	}
	if got := fake.records(exampleZoneID, "_acme-challenge", "TXT"); !slices.Equal(got, []string{`"older"`, `"newer"`}) {
		t.Fatalf("records = %q", got)
	}
}

// TestZone_AutoDiscovery asserts that without zone_id the plugin finds
// the zone by name, walking up from the request zone (a host name for
// ACME) to the registrable domain Hetzner holds.
func TestZone_AutoDiscovery(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_AUTODISCO"
	setEnv(t, env, "tok-autodisco")
	fake := newFakeCloudDNS(t, "tok-autodisco", fakeZone{ID: 99, Name: "example.org", TTL: 3600}, exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, ""))

	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "mail.example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "x", TTL: 60,
	}); err != nil {
		t.Fatalf("present: %v", err)
	}
	if got := fake.records(exampleZoneID, "_acme-challenge.mail", "TXT"); !slices.Equal(got, []string{`"x"`}) {
		t.Fatalf("records = %q", got)
	}
	var lookups []string
	for _, r := range fake.snapshot() {
		if r.Method == http.MethodGet && r.Path == "/zones" {
			lookups = append(lookups, r.Query)
		}
	}
	if !slices.Equal(lookups, []string{"name=mail.example.com", "name=example.com"}) {
		t.Fatalf("zone lookups = %q", lookups)
	}

	// The discovered zone is cached.
	fake.reset()
	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "y", TTL: 60,
	}); err != nil {
		t.Fatalf("second present: %v", err)
	}
	for _, r := range fake.snapshot() {
		if r.Path == "/zones" {
			t.Fatalf("unexpected zone lookup %s", r.Query)
		}
	}
}

func TestZone_NotFound(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_NOZONE"
	setEnv(t, env, "tok-nozone")
	fake := newFakeCloudDNS(t, "tok-nozone", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, ""))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "other.net", RecordType: "TXT", Name: "_acme-challenge", Value: "x", TTL: 60,
	})
	if msg := rpcError(t, err).Message; !strings.Contains(msg, "no Hetzner zone") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestReplace_SetsSingleValue(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_REPLACE"
	setEnv(t, env, "tok-replace")
	fake := newFakeCloudDNS(t, "tok-replace", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	replace := func(value string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var res sdk.DNSPresentResult
		if err := p.client.Call(ctx, sdk.MethodDNSReplace, sdk.DNSPresentParams{
			Zone: "example.com", RecordType: "TXT", Name: "_mta-sts", Value: value, TTL: 300,
		}, &res); err != nil {
			t.Fatalf("replace(%s): %v", value, err)
		}
	}
	replace("v=STSv1; id=1")
	replace("v=STSv1; id=2")
	if got := fake.records(exampleZoneID, "_mta-sts", "TXT"); !slices.Equal(got, []string{`"v=STSv1; id=2"`}) {
		t.Fatalf("records = %q", got)
	}
}

func TestList_EnumeratesRecords(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_LIST"
	setEnv(t, env, "tok-list")
	fake := newFakeCloudDNS(t, "tok-list", exampleZone)
	fake.seed(exampleZoneID, "host", "A", 300, "192.0.2.1", "192.0.2.2")
	fake.seed(exampleZoneID, "host", "TXT", 300, `"hello" " world"`)
	fake.seed(exampleZoneID, "other", "A", 300, "192.0.2.3")
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	recs, err := p.list(t, sdk.DNSListParams{Zone: "example.com", RecordType: "A", Name: "host.example.com"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 2 || recs[0].Value != "192.0.2.1" || recs[1].Value != "192.0.2.2" || recs[0].TTL != 300 {
		t.Fatalf("records = %+v", recs)
	}

	txt, err := p.list(t, sdk.DNSListParams{Zone: "example.com", RecordType: "TXT", Name: "host"})
	if err != nil {
		t.Fatalf("list TXT: %v", err)
	}
	if len(txt) != 1 || txt[0].Value != "hello world" {
		t.Fatalf("TXT records = %+v", txt)
	}
	// A listed id cleans up exactly that record.
	if err := p.cleanup(t, recs[0].ID); err != nil {
		t.Fatalf("cleanup listed id: %v", err)
	}
	if got := fake.records(exampleZoneID, "host", "A"); !slices.Equal(got, []string{"192.0.2.2"}) {
		t.Fatalf("records after cleanup = %q", got)
	}
}

func TestPresent_LongTXTIsSplit(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_LONGTXT"
	setEnv(t, env, "tok-longtxt")
	fake := newFakeCloudDNS(t, "tok-longtxt", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	value := strings.Repeat("a", 255) + `b"c`
	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "dkim._domainkey", Value: value, TTL: 300,
	}); err != nil {
		t.Fatalf("present: %v", err)
	}
	want := `"` + strings.Repeat("a", 255) + `" "b\"c"`
	if got := fake.records(exampleZoneID, "dkim._domainkey", "TXT"); !slices.Equal(got, []string{want}) {
		t.Fatalf("records = %q", got)
	}
	recs, err := p.list(t, sdk.DNSListParams{Zone: "example.com", RecordType: "TXT", Name: "dkim._domainkey"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].Value != value {
		t.Fatalf("listed = %+v", recs)
	}
}

func TestPresent_AuthFailure(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_AUTH"
	setEnv(t, env, "tok-bad")
	fake := newFakeCloudDNS(t, "tok-good", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "x", TTL: 60,
	})
	if msg := rpcError(t, err).Message; !strings.Contains(msg, "auth") {
		t.Fatalf("msg = %q", msg)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 1 {
		t.Fatalf("got %d calls, want 1 (no retry on auth failure)", n)
	}
}

func TestPresent_APIError(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_APIERR"
	setEnv(t, env, "tok-apierr")
	fake := newFakeCloudDNS(t, "tok-apierr", exampleZone)
	fake.setFail(func(req capturedRequest) (int, string, bool) {
		return http.StatusUnprocessableEntity, "incorrect_zone_mode", strings.HasSuffix(req.Path, "/add_records")
	})
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "x", TTL: 60,
	})
	if msg := rpcError(t, err).Message; !strings.Contains(msg, "incorrect_zone_mode") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestPresent_RetryOnRateLimit(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_RETRY"
	setEnv(t, env, "tok-retry")
	fake := newFakeCloudDNS(t, "tok-retry", exampleZone)
	var failed atomic.Bool
	fake.setFail(func(req capturedRequest) (int, string, bool) {
		if strings.HasSuffix(req.Path, "/add_records") && !failed.Swap(true) {
			return http.StatusTooManyRequests, "rate_limit_exceeded", true
		}
		return 0, "", false
	})
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	if _, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "TXT", Name: "_acme-challenge", Value: "x", TTL: 60,
	}); err != nil {
		t.Fatalf("present: %v", err)
	}
	adds := 0
	for _, r := range fake.snapshot() {
		if strings.HasSuffix(r.Path, "/add_records") {
			adds++
		}
	}
	if adds != 2 {
		t.Fatalf("add_records sent %d times, want 2 (retry once)", adds)
	}
}

func TestPresent_UnknownRecordType(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_UNKNOWN"
	setEnv(t, env, "tok-unknown")
	fake := newFakeCloudDNS(t, "tok-unknown", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, strconv.Itoa(exampleZoneID)))

	_, err := p.present(t, sdk.DNSPresentParams{
		Zone: "example.com", RecordType: "PTR", Name: "x.example.com", Value: "v", TTL: 60,
	})
	if msg := rpcError(t, err).Message; !strings.Contains(msg, "unsupported") {
		t.Fatalf("msg = %q", msg)
	}
	if n := atomic.LoadInt64(&fake.calls); n != 0 {
		t.Fatalf("provider got %d calls, want 0", n)
	}
}

func TestCleanup_InvalidID(t *testing.T) {
	const env = "HEROLD_DNS_HETZNER_TEST_TOKEN_BADID"
	setEnv(t, env, "tok-badid")
	fake := newFakeCloudDNS(t, "tok-badid", exampleZone)
	p := spawnPlugin(t, testOpts(env, fake.server.URL, ""))

	if msg := rpcError(t, p.cleanup(t, "rec-1")).Message; !strings.Contains(msg, "invalid record id") {
		t.Fatalf("msg = %q", msg)
	}
}
