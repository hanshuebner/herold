package admin

// Tests for `principal set-password` on the caller's own principal and for
// `principal reset-password --offline` (re #540).

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// recordedReq is one request seen by the fake admin server.
type recordedReq struct {
	Method, Path string
	Body         map[string]string
}

// fakeAdminServer answers whoami as principal 7 and serves any
// /api/v1/principals/{id}/password PUT and the step-up POST from the
// supplied handlers.
type fakeAdminServer struct {
	mu   sync.Mutex
	reqs []recordedReq
	srv  *httptest.Server
}

func newFakeAdminServer(t *testing.T, putPassword func(n int, w http.ResponseWriter), stepUp func(code string, w http.ResponseWriter)) *fakeAdminServer {
	t.Helper()
	f := &fakeAdminServer{}
	puts := 0
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedReq{r.Method, r.URL.Path, body})
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/auth/whoami":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"principal_id":7,"email":"me@test.local"}`))
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/password"):
			puts++
			putPassword(puts, w)
		case r.URL.Path == "/api/v1/auth/step-up":
			stepUp(body["totp_code"], w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAdminServer) requests() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedReq(nil), f.reqs...)
}

func stepUpRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"type":"about:blank","title":"Step-up elevation required","status":403,"step_up_required":true,"elevation_scope":"self-service"}`))
}

func noContent(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func runAgainstFake(t *testing.T, f *fakeAdminServer, stdin string, args ...string) (string, string, error) {
	t.Helper()
	env := &cliTestEnv{t: t}
	full := append([]string{"--server-url", f.srv.URL, "--api-key", "hk_fake"}, args...)
	return env.runWithStdin(stdin, full...)
}

func TestCLIPrincipalSetPassword_SelfSendsCurrentPassword(t *testing.T) {
	f := newFakeAdminServer(t, noContent,
		func(string, http.ResponseWriter) { t.Error("step-up must not be called") })
	out, _, err := runAgainstFake(t, f, "the-current-pw\n",
		"principal", "set-password", "7", "--password", "the-new-password-1")
	if err != nil {
		t.Fatalf("set-password: %v", err)
	}
	if !strings.Contains(out, "password updated") {
		t.Fatalf("output %q lacks confirmation", out)
	}
	reqs := f.requests()
	last := reqs[len(reqs)-1]
	if last.Method != "PUT" || last.Body["current_password"] != "the-current-pw" || last.Body["new_password"] != "the-new-password-1" {
		t.Fatalf("PUT body wrong: %+v", last)
	}
}

func TestCLIPrincipalSetPassword_SelfCurrentPasswordFlag(t *testing.T) {
	f := newFakeAdminServer(t, noContent, func(string, http.ResponseWriter) {})
	if _, _, err := runAgainstFake(t, f, "",
		"principal", "set-password", "7", "--password", "the-new-password-1", "--current-password", "flag-current"); err != nil {
		t.Fatalf("set-password: %v", err)
	}
	reqs := f.requests()
	if got := reqs[len(reqs)-1].Body["current_password"]; got != "flag-current" {
		t.Fatalf("current_password = %q", got)
	}
}

func TestCLIPrincipalSetPassword_StepUpThenRetry(t *testing.T) {
	var gotCode string
	f := newFakeAdminServer(t,
		func(n int, w http.ResponseWriter) {
			if n == 1 {
				stepUpRequired(w)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
		func(code string, w http.ResponseWriter) {
			gotCode = code
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"elevation_expires_at":"2026-01-01T00:00:00Z"}`))
		})
	// Prompts in order: current password, then TOTP code (new password via flag).
	out, _, err := runAgainstFake(t, f, "cur-pw-123456\n654321\n",
		"principal", "set-password", "7", "--password", "the-new-password-1")
	if err != nil {
		t.Fatalf("set-password: %v", err)
	}
	if !strings.Contains(out, "password updated") {
		t.Fatalf("output %q lacks confirmation", out)
	}
	if gotCode != "654321" {
		t.Fatalf("step-up code = %q", gotCode)
	}
	var paths []string
	for _, r := range f.requests() {
		paths = append(paths, r.Method+" "+r.Path)
	}
	want := "GET /api/v1/auth/whoami,PUT /api/v1/principals/7/password,POST /api/v1/auth/step-up,PUT /api/v1/principals/7/password"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("request sequence = %s, want %s", got, want)
	}
	if f.requests()[3].Body["current_password"] != "cur-pw-123456" {
		t.Fatalf("retry lost current_password: %+v", f.requests()[3])
	}
}

func TestCLIPrincipalSetPassword_TOTPCodeFlag(t *testing.T) {
	var gotCode string
	f := newFakeAdminServer(t,
		func(n int, w http.ResponseWriter) {
			if n == 1 {
				stepUpRequired(w)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
		func(code string, w http.ResponseWriter) { gotCode = code; w.WriteHeader(http.StatusOK) })
	if _, _, err := runAgainstFake(t, f, "",
		"principal", "set-password", "7", "--password", "the-new-password-1",
		"--current-password", "cur", "--totp-code", "111222"); err != nil {
		t.Fatalf("set-password: %v", err)
	}
	if gotCode != "111222" {
		t.Fatalf("step-up code = %q", gotCode)
	}
}

func TestCLIPrincipalSetPassword_WrongTOTPFailsCleanly(t *testing.T) {
	puts := 0
	f := newFakeAdminServer(t,
		func(_ int, w http.ResponseWriter) { puts++; stepUpRequired(w) },
		func(_ string, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"unauthorized","status":401,"detail":"invalid TOTP code"}`))
		})
	_, _, err := runAgainstFake(t, f, "",
		"principal", "set-password", "7", "--password", "the-new-password-1",
		"--current-password", "secret-current-pw", "--totp-code", "999888")
	if err == nil {
		t.Fatalf("expected failure on a rejected TOTP code")
	}
	if !strings.Contains(err.Error(), "step-up") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should name the step-up failure: %v", err)
	}
	for _, secret := range []string{"999888", "secret-current-pw", "the-new-password-1"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error text leaks a secret: %v", err)
		}
	}
	if puts != 1 {
		t.Fatalf("password PUT attempted %d times, want exactly 1 (no retry after failed step-up)", puts)
	}
}

func TestCLIPrincipalSetPassword_RetriesOnlyOnce(t *testing.T) {
	puts := 0
	f := newFakeAdminServer(t,
		func(_ int, w http.ResponseWriter) { puts++; stepUpRequired(w) },
		func(_ string, w http.ResponseWriter) { w.WriteHeader(http.StatusOK) })
	_, _, err := runAgainstFake(t, f, "",
		"principal", "set-password", "7", "--password", "the-new-password-1",
		"--current-password", "cur", "--totp-code", "123123")
	if err == nil {
		t.Fatalf("expected the second step_up_required to surface as an error")
	}
	if puts != 2 {
		t.Fatalf("password PUT attempted %d times, want 2", puts)
	}
}

// The admin override for another principal sends no current password and
// never steps up.
func TestCLIPrincipalSetPassword_OtherPrincipalSendsNoCurrentPassword(t *testing.T) {
	f := newFakeAdminServer(t, noContent,
		func(string, http.ResponseWriter) { t.Error("step-up must not be called") })
	if _, _, err := runAgainstFake(t, f, "", "principal", "set-password", "8", "--password", "the-new-password-1"); err != nil {
		t.Fatalf("set-password: %v", err)
	}
	reqs := f.requests()
	last := reqs[len(reqs)-1]
	if _, ok := last.Body["current_password"]; ok || last.Path != "/api/v1/principals/8/password" {
		t.Fatalf("admin override must not send current_password: %+v", last)
	}
}

// fixedSalt yields deterministic bytes so a seeded hash is reproducible.
type fixedSalt struct{}

func (fixedSalt) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i + 1)
	}
	return len(p), nil
}

// TestCLIPrincipalSetPassword_SelfAgainstRealServer drives the whole flow
// against the real protoadmin server: the API key's principal has a
// password and TOTP enrolled, so the first PUT answers step_up_required
// and the CLI's step-up (Bearer key) unlocks the retry.
func TestCLIPrincipalSetPassword_SelfAgainstRealServer(t *testing.T) {
	env := newCLITestEnv(t, nil)
	ctx := context.Background()
	dir := directory.New(env.store.Meta(), nil, env.clk, nil)
	admin, err := env.store.Meta().GetPrincipalByEmail(ctx, "admin@test.local")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	oldHash, err := directory.HashPassword(fixedSalt{}, "old-password-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Herold", AccountName: "admin@test.local"})
	if err != nil {
		t.Fatalf("totp.Generate: %v", err)
	}
	admin.PasswordHash = oldHash
	admin.TOTPSecret = []byte(key.Secret())
	admin.Flags |= store.PrincipalFlagTOTPEnabled
	if err := env.store.Meta().UpdatePrincipal(ctx, admin); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	code, err := totp.GenerateCodeCustom(key.Secret(), env.clk.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	// A wrong code fails and leaves the password unchanged.
	_, _, err = env.run("principal", "set-password", "admin@test.local",
		"--password", "brand-new-password-9", "--current-password", "old-password-123", "--totp-code", "000000")
	if err == nil {
		t.Fatalf("wrong TOTP code must fail")
	}
	if _, err := dir.Authenticate(ctx, "admin@test.local", "old-password-123"); err != nil {
		t.Fatalf("old password must still work after a failed step-up: %v", err)
	}

	out, _, err := env.run("principal", "set-password", "admin@test.local",
		"--password", "brand-new-password-9", "--current-password", "old-password-123", "--totp-code", code)
	if err != nil {
		t.Fatalf("set-password with step-up: %v", err)
	}
	if !strings.Contains(out, "password updated") {
		t.Fatalf("output %q lacks confirmation", out)
	}
	if _, err := dir.Authenticate(ctx, "admin@test.local", "brand-new-password-9"); err != nil {
		t.Fatalf("new password must authenticate: %v", err)
	}
	if _, err := dir.Authenticate(ctx, "admin@test.local", "old-password-123"); err == nil {
		t.Fatalf("old password must no longer authenticate")
	}
}

// -- offline reset ----------------------------------------------------

type resetBackend struct {
	name string
	open func(t *testing.T) store.Store
}

// resetBackends returns sqlite always and postgres when HEROLD_PG_DSN
// names a database the test may wipe (storepg is opened explicitly; the
// testharness would ignore the DSN).
func resetBackends() []resetBackend {
	out := []resetBackend{{"sqlite", func(t *testing.T) store.Store {
		return sqlitetest.Open(t, clock.NewReal())
	}}}
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		return out
	}
	return append(out, resetBackend{"postgres", func(t *testing.T) store.Store {
		st, err := storepg.Open(context.Background(), dsn, filepath.Join(t.TempDir(), "blobs"), nil, clock.NewReal())
		if err != nil {
			t.Fatalf("storepg.Open: %v", err)
		}
		if tr, ok := st.(interface{ TruncateAll(context.Context) error }); ok {
			if err := tr.TruncateAll(context.Background()); err != nil {
				t.Fatalf("TruncateAll: %v", err)
			}
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}})
}

func TestResetPasswordOffline_BothBackends(t *testing.T) {
	for _, b := range resetBackends() {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			clk := clock.NewReal()
			st := b.open(t)
			dir := directory.New(st.Meta(), nil, clk, nil)
			if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "test.local", IsLocal: true}); err != nil {
				t.Fatalf("InsertDomain: %v", err)
			}
			pid, err := dir.CreatePrincipal(ctx, "locked@test.local", "the-forgotten-pw-1")
			if err != nil {
				t.Fatalf("CreatePrincipal: %v", err)
			}

			if err := resetPasswordOffline(ctx, st, clk, "Locked@Test.local", "short"); !errors.Is(err, directory.ErrWeakPassword) {
				t.Fatalf("weak password: got %v", err)
			}
			if err := resetPasswordOffline(ctx, st, clk, "ghost@test.local", "a-long-enough-pw-1"); err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("unknown principal: got %v", err)
			}

			if err := resetPasswordOffline(ctx, st, clk, "Locked@Test.local", "recovered-password-2"); err != nil {
				t.Fatalf("resetPasswordOffline: %v", err)
			}
			if _, err := dir.Authenticate(ctx, "locked@test.local", "recovered-password-2"); err != nil {
				t.Fatalf("new password must authenticate: %v", err)
			}
			if _, err := dir.Authenticate(ctx, "locked@test.local", "the-forgotten-pw-1"); err == nil {
				t.Fatalf("old password must no longer authenticate")
			}

			entries, err := st.Meta().ListAuditLog(ctx, store.AuditLogFilter{Action: "principal.password.reset"})
			if err != nil {
				t.Fatalf("ListAuditLog: %v", err)
			}
			found := false
			for _, e := range entries {
				if e.Subject == "principal:"+strconv.FormatUint(uint64(pid), 10) && e.Outcome == store.OutcomeSuccess {
					found = true
					if strings.Contains(e.Message, "recovered-password-2") {
						t.Fatalf("audit entry leaks the password")
					}
				}
			}
			if !found {
				t.Fatalf("no audit entry for the reset: %+v", entries)
			}
		})
	}
}

// TestCLIPrincipalResetPassword_Offline drives the cobra command against a
// store opened from a system.toml, as an operator on the host would.
func TestCLIPrincipalResetPassword_Offline(t *testing.T) {
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	dir := directory.New(st.Meta(), nil, clock.NewReal(), nil)
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "test.local", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	if _, err := dir.CreatePrincipal(ctx, "sole@test.local", "the-forgotten-pw-1"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	env := &cliTestEnv{t: t}

	// Without --offline the command refuses and touches nothing.
	if _, _, err := env.run("--system-config", systomlPath, "principal", "reset-password", "sole@test.local", "--password", "recovered-password-2"); err == nil ||
		!strings.Contains(err.Error(), "--offline") {
		t.Fatalf("want --offline requirement, got %v", err)
	}

	// A mismatched confirmation fails and changes nothing.
	if _, _, err := env.runWithStdin("aaaaaaaaaaaaaa\nbbbbbbbbbbbbbb\n",
		"--system-config", systomlPath, "principal", "reset-password", "sole@test.local", "--offline"); err == nil {
		t.Fatalf("mismatched confirmation must fail")
	}

	// Prompted twice on piped stdin.
	out, _, err := env.runWithStdin("recovered-password-2\nrecovered-password-2\n",
		"--system-config", systomlPath, "principal", "reset-password", "sole@test.local", "--offline")
	if err != nil {
		t.Fatalf("reset-password: %v", err)
	}
	if strings.Contains(out, "recovered-password-2") {
		t.Fatalf("output echoes the password: %q", out)
	}

	st2, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	dir2 := directory.New(st2.Meta(), nil, clock.NewReal(), nil)
	if _, err := dir2.Authenticate(ctx, "sole@test.local", "recovered-password-2"); err != nil {
		t.Fatalf("new password must authenticate: %v", err)
	}
	if _, err := dir2.Authenticate(ctx, "sole@test.local", "the-forgotten-pw-1"); err == nil {
		t.Fatalf("old password must not authenticate")
	}
}

func TestServerListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cfg := &sysconfig.Config{Listener: []sysconfig.ListenerConfig{{Kind: "admin", Address: ln.Addr().String(), TLS: "none"}}}
	if !serverListening(cfg) {
		t.Fatalf("listening admin port not detected")
	}
	_ = ln.Close()
	time.Sleep(10 * time.Millisecond)
	if serverListening(cfg) {
		t.Fatalf("closed admin port reported as listening")
	}
}
