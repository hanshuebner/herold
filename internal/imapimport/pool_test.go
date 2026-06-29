package imapimport

// pool_test.go covers the Pool + accountWorker state machine for sub-step 3a:
//
//   - successful connect + auth over implicit TLS
//   - successful connect + auth over STARTTLS
//   - password vs app_password (both succeed)
//   - allow_password=false rejects password but allows xoauth2
//   - xoauth2 happy path (fake token endpoint + fake dialer)
//   - bad credential -> error + state transitions to errored after M failures
//     (uses tiny M and fast/zeroed backoff via injected FakeClock)
//   - clean shutdown on ctx cancel

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// testKey returns a deterministic 32-byte data key for use in tests.
func testDataKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// sealCred seals plaintext with the test data key and returns the ciphertext.
func sealCred(t *testing.T, plain string) []byte {
	t.Helper()
	ct, err := secrets.Seal(testDataKey(t), []byte(plain))
	if err != nil {
		t.Fatalf("sealCred: %v", err)
	}
	return ct
}

// makeAccount creates a principal and an IMAPImportAccount row in the
// store and returns the account. Each call uses a unique email to avoid
// conflicts across subtests sharing the same store.
func makeAccount(t *testing.T, s store.Store, cfg accountCfg) store.IMAPImportAccount {
	t.Helper()
	ctx := context.Background()

	// Insert principal; email must be globally unique within the store.
	p, err := s.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: cfg.email,
		DisplayName:    cfg.email,
		QuotaBytes:     1 << 30,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%q): %v", cfg.email, err)
	}

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:  p.ID,
		AccountName:  "Test Account",
		Host:         cfg.host,
		Port:         cfg.port,
		TLSMode:      cfg.tlsMode,
		Username:     cfg.username,
		AuthMethod:   cfg.authMethod,
		CredentialCT: sealCred(t, cfg.credentialPlaintext),
		State:        store.IMAPImportAccountStateEnabled,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	return acc
}

type accountCfg struct {
	// email is the principal's canonical email; must be unique per test.
	email   string
	host    string
	port    int
	tlsMode store.IMAPImportTLSMode
	// username is the IMAP authentication username (may differ from email).
	username            string
	authMethod          store.IMAPImportAuthMethod
	credentialPlaintext string
}

// newTestLogger returns a slog.Logger that writes to t.Log.
func newTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// runWorkerOnce runs a single accountWorker connection attempt (connect,
// authenticate, initial sync, then cancel). It cancels the context after a
// short grace period so the worker exits the IDLE loop cleanly. Returns
// the first error from attempt (nil on clean cancellation).
func runWorkerOnce(t *testing.T, w *accountWorker) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ch <- result{err: w.attempt(ctx)}
	}()

	// Give the worker enough time to connect and run its initial sync.
	// Then cancel to exit the IDLE loop.
	select {
	case r := <-ch:
		// Worker exited before we cancelled — return the error as-is.
		return r.err
	case <-time.After(2 * time.Second):
		// Worker is running (in IDLE loop); cancel and wait for it.
		cancel()
		r := <-ch
		// A nil return after ctx cancel is a clean exit.
		if r.err == nil || r.err == ctx.Err() {
			return nil
		}
		return r.err
	}
}

// TestConnectImplicitTLS verifies a successful connection over implicit
// TLS. REQ-IMAP-IMP-06.
func TestConnectImplicitTLS(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("alice", "secret123")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "alice@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "alice",
		authMethod:          store.IMAPImportAuthMethodPassword,
		credentialPlaintext: "secret123",
	})
	// Override host/port to point at the in-process server.
	acc.Host = "127.0.0.1"
	acc.Port = 0 // not used; fakeDialer ignores it

	w := newAccountWorker(accountWorkerOpts{
		account: acc,
		store:   ha.Store,
		dataKey: testDataKey(t),
		cfg:     sysconfig.IMAPImportConfig{},
		log:     newTestLogger(t),
		clk:     ha.Clock,
		dialer: &fakeDialer{
			ts: ts,
		},
		categoriser: noopCategoriser{},
	})
	if err := runWorkerOnce(t, w); err != nil {
		t.Fatalf("attempt: %v", err)
	}
}

// TestConnectSTARTTLS verifies a successful connection over STARTTLS.
// REQ-IMAP-IMP-06.
func TestConnectSTARTTLS(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("bob", "passw0rd")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "bob@example.test",
		host:                "127.0.0.1",
		port:                143,
		tlsMode:             store.IMAPImportTLSModeSTARTTLS,
		username:            "bob",
		authMethod:          store.IMAPImportAuthMethodPassword,
		credentialPlaintext: "passw0rd",
	})

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &fakeDialer{ts: ts},
		categoriser: noopCategoriser{},
	})
	// Override TLSMode so fakeDialer uses the STARTTLS address.
	w.opts.account.TLSMode = store.IMAPImportTLSModeSTARTTLS

	if err := runWorkerOnce(t, w); err != nil {
		t.Fatalf("attempt (STARTTLS): %v", err)
	}
}

// TestAuthMethodPassword and TestAuthMethodAppPassword verify that
// both auth methods use the same IMAP LOGIN path and succeed.
// REQ-IMAP-IMP-04.
func TestAuthMethodPassword(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("carol", "pw")

	for _, method := range []store.IMAPImportAuthMethod{
		store.IMAPImportAuthMethodPassword,
		store.IMAPImportAuthMethodAppPassword,
	} {
		method := method
		t.Run(string(method), func(t *testing.T) {
			// Each subtest gets its own store to avoid email conflicts.
			ha, _ := testharness.Start(t, testharness.Options{})
			acc := makeAccount(t, ha.Store, accountCfg{
				email:               "carol@example.test",
				host:                "127.0.0.1",
				port:                993,
				tlsMode:             store.IMAPImportTLSModeImplicit,
				username:            "carol",
				authMethod:          method,
				credentialPlaintext: "pw",
			})
			w := newAccountWorker(accountWorkerOpts{
				account:     acc,
				store:       ha.Store,
				dataKey:     testDataKey(t),
				cfg:         sysconfig.IMAPImportConfig{},
				log:         newTestLogger(t),
				clk:         ha.Clock,
				dialer:      &fakeDialer{ts: ts},
				categoriser: noopCategoriser{},
			})
			if err := runWorkerOnce(t, w); err != nil {
				t.Fatalf("attempt (%s): %v", method, err)
			}
		})
	}
}

// TestAllowPasswordFalse_RejectsPassword verifies that when
// allow_password=false the worker rejects password/app_password auth
// methods before dialing. REQ-IMAP-IMP-05.
func TestAllowPasswordFalse_RejectsPassword(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("dave", "secret")

	f := false
	cfg := sysconfig.IMAPImportConfig{AllowPassword: &f}

	for _, method := range []store.IMAPImportAuthMethod{
		store.IMAPImportAuthMethodPassword,
		store.IMAPImportAuthMethodAppPassword,
	} {
		method := method
		t.Run(string(method), func(t *testing.T) {
			// Each subtest gets its own store to avoid principal email conflicts.
			ha, _ := testharness.Start(t, testharness.Options{})
			acc := makeAccount(t, ha.Store, accountCfg{
				email:               "dave@example.test",
				host:                "127.0.0.1",
				port:                993,
				tlsMode:             store.IMAPImportTLSModeImplicit,
				username:            "dave",
				authMethod:          method,
				credentialPlaintext: "secret",
			})
			w := newAccountWorker(accountWorkerOpts{
				account:     acc,
				store:       ha.Store,
				dataKey:     testDataKey(t),
				cfg:         cfg,
				log:         newTestLogger(t),
				clk:         ha.Clock,
				dialer:      &fakeDialer{ts: ts},
				categoriser: noopCategoriser{},
			})
			err := runWorkerOnce(t, w)
			if err == nil {
				t.Fatal("expected error for allow_password=false, got nil")
			}
			// Verify the error does not contain any credential material.
			errStr := err.Error()
			if contains(errStr, "secret") {
				t.Errorf("error message may expose credential: %q", errStr)
			}
		})
	}
}

// TestAllowPasswordFalse_AllowsXOAuth2 verifies that xoauth2 is still
// permitted when allow_password=false. REQ-IMAP-IMP-05.
func TestAllowPasswordFalse_AllowsXOAuth2(t *testing.T) {
	ts := startTestIMAPServer(t)
	// Seed user with "access-token-123" as their password so the
	// xoauthFakeDialer's LOGIN(accessToken) will succeed.
	ts.addUser("eve", "access-token-123")

	ha, _ := testharness.Start(t, testharness.Options{})

	f := false
	fakeTok := &fakeTokenSource{accessToken: "access-token-123"}
	cfg := sysconfig.IMAPImportConfig{
		AllowPassword: &f,
		OAuth: map[string]sysconfig.IMAPImportOAuthProvider{
			"fakeprovider": {
				ClientID:        "cid",
				ClientSecretRef: "fakesecret",
				TokenEndpoint:   "https://example.test/token",
			},
		},
	}

	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "eve@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "eve",
		authMethod:          store.IMAPImportAuthMethodXOAuth2,
		credentialPlaintext: "refresh-token-abc",
	})

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         cfg,
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &xoauthFakeDialer{ts: ts, tok: fakeTok},
		categoriser: noopCategoriser{},
	})

	// Wire the fake token source into the worker so openCredential uses it.
	w.testTokenSource = fakeTok

	if err := runWorkerOnce(t, w); err != nil {
		t.Fatalf("attempt (xoauth2 with allow_password=false): %v", err)
	}
	// Each successful connection dials once; we now open up to 3 connections
	// (primary + secondary-sync + write-back) per session, so >=1 call is the
	// correct bound. The key property is that at least one exchange happened.
	if fakeTok.calls < 1 {
		t.Errorf("expected >= 1 token exchange call, got %d", fakeTok.calls)
	}
}

// TestXOAuth2HappyPath verifies the full xoauth2 path:
//  1. openCredential decrypts the refresh token
//  2. calls the token source -> access token
//  3. passes the access token to the dialer as CredentialPlaintext
//  4. connect succeeds
func TestXOAuth2HappyPath(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("frank", "fresh-access-token")

	ha, _ := testharness.Start(t, testharness.Options{})

	fakeTok := &fakeTokenSource{accessToken: "fresh-access-token"}
	tr := true
	cfg := sysconfig.IMAPImportConfig{
		AllowPassword: &tr,
		OAuth: map[string]sysconfig.IMAPImportOAuthProvider{
			"myprovider": {
				ClientID:        "cid",
				ClientSecretRef: "secret",
				TokenEndpoint:   "https://example.test/token",
			},
		},
	}

	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "frank@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "frank",
		authMethod:          store.IMAPImportAuthMethodXOAuth2,
		credentialPlaintext: "my-refresh-token",
	})

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         cfg,
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &xoauthFakeDialer{ts: ts, tok: fakeTok},
		categoriser: noopCategoriser{},
	})
	w.testTokenSource = fakeTok

	if err := runWorkerOnce(t, w); err != nil {
		t.Fatalf("xoauth2 happy path: %v", err)
	}
	// We open up to 3 connections per session; at least 1 token exchange is
	// required.
	if fakeTok.calls < 1 {
		t.Errorf("expected >= 1 token call, got %d", fakeTok.calls)
	}
}

// TestBadCredential_ErrorsAfterMFailures verifies the state machine:
// M consecutive failures -> state=errored, worker stops.
// Uses a tiny M via the testMaxConsecutiveFailures knob and a zeroed
// FakeClock so backoff delays are skipped. REQ-IMAP-IMP-25.
func TestBadCredential_ErrorsAfterMFailures(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("grace", "correctpassword")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "grace@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "grace",
		authMethod:          store.IMAPImportAuthMethodPassword,
		credentialPlaintext: "wrongpassword",
	})

	// Use FakeClock; advance it instantly whenever the worker waits.
	fc := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	w := newAccountWorker(accountWorkerOpts{
		account:                acc,
		store:                  ha.Store,
		dataKey:                testDataKey(t),
		cfg:                    sysconfig.IMAPImportConfig{},
		log:                    newTestLogger(t),
		clk:                    fc,
		dialer:                 &badCredDialer{},
		categoriser:            noopCategoriser{},
		maxConsecutiveFailures: 3, // tiny M for test speed
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// run() blocks until the worker reaches errored or ctx is cancelled.
	// We run it in a goroutine and drive the fake clock to skip backoff waits.
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.run(ctx)
	}()

	// Advance the clock to skip all backoff waits. We do this in a tight
	// loop until the worker's done channel closes.
	for {
		select {
		case <-done:
			goto verify
		default:
			fc.Advance(backoffCap + time.Second)
			// Small pause so the worker goroutine can make progress.
			time.Sleep(time.Millisecond)
		}
	}

verify:
	// Verify the account is now in errored state.
	stored, err := ha.Store.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if stored.State != store.IMAPImportAccountStateErrored {
		t.Errorf("state = %q; want %q", stored.State, store.IMAPImportAccountStateErrored)
	}
	if stored.LastError == "" {
		t.Error("LastError should be set after erroring")
	}
}

// TestCleanShutdown_CtxCancel verifies that a Pool shuts down cleanly
// when the context is cancelled (even if a worker is in a backoff wait).
func TestCleanShutdown_CtxCancel(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("henry", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "henry@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "henry",
		authMethod:          store.IMAPImportAuthMethodPassword,
		credentialPlaintext: "pw",
	})
	_ = acc // account is in enabled state but pool is cancelled immediately

	ctx, cancel := context.WithCancel(context.Background())

	tr := true
	pool := NewPool(PoolOptions{
		Store:   ha.Store,
		DataKey: testDataKey(t),
		Config: sysconfig.IMAPImportConfig{
			AllowPassword:      &tr,
			ConcurrentAccounts: 1,
		},
		Logger: newTestLogger(t),
		Clock:  ha.Clock,
		Dialer: &fakeDialer{ts: ts},
	})

	done := make(chan error, 1)
	go func() {
		done <- pool.Run(ctx)
	}()

	// Cancel immediately.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Pool.Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pool.Run did not return after ctx cancel within 5 seconds")
	}
}

// TestPool_DynamicAccountPickup verifies that the pool starts a worker for an
// account created after Run was called with an empty store. REQ-IMAP-IMP-26.
func TestPool_DynamicAccountPickup(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("ivan", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})

	tr := true
	pool := NewPool(PoolOptions{
		Store:   ha.Store,
		DataKey: testDataKey(t),
		Config: sysconfig.IMAPImportConfig{
			AllowPassword:      &tr,
			ConcurrentAccounts: 4,
		},
		Logger:       newTestLogger(t),
		Clock:        ha.Clock,
		Dialer:       &fakeDialer{ts: ts},
		PollInterval: 50 * time.Millisecond, // fast tick for test
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- pool.Run(ctx)
	}()

	// Give the pool one tick to confirm no workers started yet.
	time.Sleep(100 * time.Millisecond)
	snap := pool.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected 0 workers before account creation, got %d", len(snap))
	}

	// Create the account now — the pool should pick it up on the next tick.
	acc := makeAccount(t, ha.Store, accountCfg{
		email:               "ivan@example.test",
		host:                "127.0.0.1",
		port:                993,
		tlsMode:             store.IMAPImportTLSModeImplicit,
		username:            "ivan",
		authMethod:          store.IMAPImportAuthMethodPassword,
		credentialPlaintext: "pw",
	})
	_ = acc

	// Wait up to 2 s for the pool to pick up the new account.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap := pool.Snapshot(); len(snap) > 0 {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Pool.Run returned error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Pool.Run did not return after ctx cancel")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done
	t.Fatal("pool did not start a worker for the dynamically-added account within 2 s")
}

// TestBackoffDuration verifies the backoff duration formula stays in bounds.
func TestBackoffDuration(t *testing.T) {
	for n := 1; n <= 30; n++ {
		d := backoffDuration(n)
		if d < time.Second {
			t.Errorf("backoffDuration(%d) = %v; want >= 1s", n, d)
		}
		// Cap + some slack for jitter.
		if d > backoffCap+backoffBase {
			t.Errorf("backoffDuration(%d) = %v; want <= cap+base=%v", n, d, backoffCap+backoffBase)
		}
	}
	// First failure must be around 2s (base).
	d1 := backoffDuration(1)
	if d1 < time.Second || d1 > 4*time.Second {
		t.Errorf("backoffDuration(1) = %v; want roughly 2s", d1)
	}
}

// TestClassifyErrorKind verifies the error classifier produces expected labels.
func TestClassifyErrorKind(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("authenticate: LOGIN failed"), "auth"},
		{fmt.Errorf("tls handshake error"), "tls"},
		{fmt.Errorf("x509 certificate signed by unknown authority"), "tls"},
		{fmt.Errorf("too many connections"), "rate_limit"},
		{fmt.Errorf("connection refused"), "network"},
		{fmt.Errorf("dial tcp: timeout"), "network"},
		{fmt.Errorf("something unexpected"), "other"},
	}
	for _, tc := range cases {
		got := classifyErrorKind(tc.err)
		if got != tc.want {
			t.Errorf("classifyErrorKind(%q) = %q; want %q", tc.err, got, tc.want)
		}
	}
}
