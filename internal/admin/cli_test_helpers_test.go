package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/rand"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"net/http"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// cliTestEnv bundles an in-process protoadmin server backed by a
// fakestore plus a seeded admin API key. The Wave 2.4 CLI tests share
// this helper to avoid duplicating the boot dance.
type cliTestEnv struct {
	t        *testing.T
	clk      *clock.FakeClock
	store    store.Store
	srv      *protoadmin.Server
	httpSrv  *httptest.Server
	apiKey   string
	homeDir  string
	credPath string
}

// newCLITestEnv stands up a fakestore-backed protoadmin behind an
// httptest.Server, seeds an admin principal + API key, and writes
// credentials into a temp HOME so the CLI can pick them up. Options are
// applied to the Options struct before NewServer is called so callers
// can inject hooks like a CertRenewer.
func newCLITestEnv(t *testing.T, optsMutator func(*protoadmin.Options)) *cliTestEnv {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	opts := protoadmin.Options{
		BootstrapPerWindow:      100,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 10000,
	}
	if optsMutator != nil {
		optsMutator(&opts)
	}
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, opts)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)

	// Seed admin principal and API key.
	ctx := context.Background()
	pid, err := fs.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "admin@test.local",
		Flags:          store.PrincipalFlagAdmin,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plain, hash := mustGenAPIKey(t)
	if _, err := fs.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid.ID,
		Hash:        hash,
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	// Write credentials so the CLI client picks the key + URL up.
	home := t.TempDir()
	credPath := filepath.Join(home, "credentials.toml")
	prevPath := credentialsPath.v
	SetCredentialsPath(credPath)
	t.Cleanup(func() { SetCredentialsPath(prevPath) })
	if _, err := saveCredentials(plain, httpSrv.URL); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	return &cliTestEnv{
		t:        t,
		clk:      clk,
		store:    fs,
		srv:      srv,
		httpSrv:  httpSrv,
		apiKey:   plain,
		homeDir:  home,
		credPath: credPath,
	}
}

// run executes the cobra root command with the given args and returns the
// captured stdout / stderr along with the resulting error.
func (e *cliTestEnv) run(args ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errBuf)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	return out.String(), errBuf.String(), err
}

// runWithStdin executes the cobra root command with the given stdin
// content. Used by interactive subcommands (queue delete, queue flush)
// that prompt for a 'yes' confirmation.
func (e *cliTestEnv) runWithStdin(stdin string, args ...string) (string, string, error) {
	e.t.Helper()
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return out.String(), errBuf.String(), err
}

// jsonRun runs the given args with --json and decodes the stdout into a
// map. Returns the decode error.
func (e *cliTestEnv) jsonRun(args ...string) (map[string]any, string, error) {
	e.t.Helper()
	full := append([]string{"--json"}, args...)
	stdout, stderr, err := e.run(full...)
	if err != nil {
		return nil, stderr, err
	}
	var v map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &v); jerr != nil {
		return nil, stderr, fmt.Errorf("decode json: %w; raw=%s", jerr, stdout)
	}
	return v, stderr, nil
}

// mustGenAPIKey produces a fresh plaintext + sha256 hash, the same way
// bootstrap does it.
func mustGenAPIKey(t *testing.T) (plain, hash string) {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	plain = protoadmin.APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return plain, protoadmin.HashAPIKey(plain)
}

// rebuildAdminServer rebuilds the protoadmin.Server attached to env.httpSrv
// with a fresh Options. The store is preserved (so seeded data persists);
// the URL changes, so credentials are re-saved. Returns nothing — the
// existing env handle is mutated.
func rebuildAdminServer(t *testing.T, env *cliTestEnv, optsMutator func(*protoadmin.Options)) {
	t.Helper()
	dir := directory.New(env.store.Meta(), nil, env.clk, nil)
	rp := directoryoidc.New(env.store.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, env.clk)
	opts := protoadmin.Options{
		BootstrapPerWindow:      100,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 10000,
	}
	if optsMutator != nil {
		optsMutator(&opts)
	}
	srv := protoadmin.NewServer(env.store, dir, rp, nil, env.clk, opts)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	env.srv = srv
	env.httpSrv = httpSrv
	if _, err := saveCredentials(env.apiKey, httpSrv.URL); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
}

// memSpamPolicyStore is a thread-safe in-memory implementation of
// SpamPolicyStore for tests.
type memSpamPolicyStore struct {
	mu     sync.Mutex
	policy protoadmin.SpamPolicy
}

func (m *memSpamPolicyStore) GetSpamPolicy() protoadmin.SpamPolicy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policy
}

func (m *memSpamPolicyStore) SetSpamPolicy(p protoadmin.SpamPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = p
}
