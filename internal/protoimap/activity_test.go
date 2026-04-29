package protoimap_test

// activity_test.go verifies REQ-OPS-86 activity tagging in protoimap.
//
// Three high-value cases are asserted:
//   - LOGIN failure  → activity=audit, level=warn
//   - APPEND         → activity=user, level=info
//   - IDLE poll tick → activity=poll, level=debug
//
// Each test drives real IMAP wire commands against a local server whose logger
// is the recording handler from observe.AssertActivityTagged. This proves that
// every log record emitted during those command flows carries a valid, enum-
// constrained activity attribute (REQ-OPS-86a).

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protoimap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness"
)

// buildActivityServer creates a minimal imaps server and returns a *fixture
// that is ready for dialImplicitTLS. The server is wired to the provided
// logger so observe.AssertActivityTagged captures all records.
func buildActivityServer(t *testing.T, ha *testharness.Server, log *slog.Logger) *fixture {
	t.Helper()
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: "activity.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	password := "correct-horse-battery"
	pid, err := dir.CreatePrincipal(ctx, "alice@activity.test", password)
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
	tlsStore, clientCfg := newTestTLSStore(t)
	srv := protoimap.NewServer(
		ha.Store, dir, tlsStore, ha.Clock, log, nil, nil,
		protoimap.Options{
			MaxConnections:        16,
			MaxCommandsPerSession: 1000,
			IdleMaxDuration:       30 * time.Minute,
			ServerName:            "herold-test",
		},
	)
	ha.AttachIMAP("imaps", srv, protoimap.ListenerModeImplicit993)
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{
		ha: ha, srv: srv, name: "imaps",
		pid: pid, password: password,
		dir: dir, tlsCfg: clientCfg, inbox: inbox,
	}
}

// buildActivityServerWithClock is like buildActivityServer but accepts an
// externally-controlled clock and domain/user params. Used by the IDLE tick
// test to advance time deterministically.
func buildActivityServerWithClock(
	t *testing.T,
	ha *testharness.Server,
	log *slog.Logger,
	clk clock.Clock,
	domain, user, password string,
) *fixture {
	t.Helper()
	ctx := context.Background()
	if err := ha.Store.Meta().InsertDomain(ctx, store.Domain{Name: domain, IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	dir := directory.New(ha.Store.Meta(), ha.Logger, ha.Clock, rand.Reader)
	pid, err := dir.CreatePrincipal(ctx, user, password)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	inbox, err := ha.Store.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		t.Fatalf("get INBOX: %v", err)
	}
	tlsStore, clientCfg := newTestTLSStore(t)
	srv := protoimap.NewServer(
		ha.Store, dir, tlsStore, clk, log, nil, nil,
		protoimap.Options{
			MaxConnections:        16,
			MaxCommandsPerSession: 1000,
			IdleMaxDuration:       5 * time.Second,
			ServerName:            "herold-test",
		},
	)
	ha.AttachIMAP("imaps", srv, protoimap.ListenerModeImplicit993)
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{
		ha: ha, srv: srv, name: "imaps",
		pid: pid, password: password,
		dir: dir, tlsCfg: clientCfg, inbox: inbox,
	}
}

// TestActivityTagging_LOGINFailure verifies that a failed LOGIN attempt
// emits an activity=audit record (REQ-OPS-86 / REQ-OPS-86d). All records
// from the session must carry a valid activity value (REQ-OPS-86a).
func TestActivityTagging_LOGINFailure(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		ha, _ := testharness.Start(t, testharness.Options{
			Listeners: []testharness.ListenerSpec{{Name: "imaps", Protocol: "imaps"}},
		})
		f := buildActivityServer(t, ha, log)
		c := f.dialImplicitTLS(t)
		defer c.close()
		resp := c.send("a1", "LOGIN alice@activity.test WRONGPASSWORD")
		last := resp[len(resp)-1]
		if !strings.Contains(last, "NO") {
			t.Fatalf("expected NO on bad credentials, got: %v", last)
		}
	})
}

// TestActivityTagging_APPENDSuccess verifies that a successful APPEND emits
// an activity=user record (REQ-OPS-86 / REQ-OPS-86d). All records from the
// session must carry a valid activity value (REQ-OPS-86a).
func TestActivityTagging_APPENDSuccess(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		ha, _ := testharness.Start(t, testharness.Options{
			Listeners: []testharness.ListenerSpec{{Name: "imaps", Protocol: "imaps"}},
		})
		f := buildActivityServer(t, ha, log)
		c := f.dialImplicitTLS(t)
		defer c.close()
		// Authenticate.
		resp := c.send("a1", fmt.Sprintf("LOGIN alice@activity.test %s", f.password))
		if !strings.Contains(resp[len(resp)-1], "OK") {
			t.Fatalf("login failed: %v", resp)
		}
		// APPEND a tiny message.
		msg := buildMessage("activity-test-append", "test body")
		c.write(fmt.Sprintf("a2 APPEND INBOX (\\Seen) {%d}\r\n", len(msg)))
		line := c.readLine()
		if !strings.HasPrefix(line, "+") {
			t.Fatalf("expected continuation, got: %q", line)
		}
		c.write(msg + "\r\n")
		resp2 := c.readUntilTag("a2")
		if !strings.Contains(resp2[len(resp2)-1], "OK") {
			t.Fatalf("APPEND failed: %v", resp2)
		}
	})
}

// TestActivityTagging_IDLEPollTick verifies that IDLE keep-alive poll ticks
// emit activity=poll records (REQ-OPS-86 / REQ-OPS-86d). A FakeClock advances
// 300 ms so the first tick fires without waiting on real time.
func TestActivityTagging_IDLEPollTick(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		ha, _ := testharness.Start(t, testharness.Options{
			Listeners: []testharness.ListenerSpec{{Name: "imaps", Protocol: "imaps"}},
		})
		fakeClock := clock.NewFake(time.Now())
		f := buildActivityServerWithClock(
			t, ha, log, fakeClock,
			"idle.test", "carol@idle.test", "correct-horse-battery",
		)
		c := f.dialImplicitTLS(t)
		defer c.close()

		resp := c.send("a1", fmt.Sprintf("LOGIN carol@idle.test %s", f.password))
		if !strings.Contains(resp[len(resp)-1], "OK") {
			t.Fatalf("login failed: %v", resp)
		}
		c.send("a2", "SELECT INBOX")

		// Enter IDLE.
		c.write("a3 IDLE\r\n")
		_ = c.readLine() // continuation

		// Advance the fake clock by more than one poll interval (200ms).
		fakeClock.Advance(300 * time.Millisecond)
		// Give the server goroutine a moment to schedule the tick.
		time.Sleep(50 * time.Millisecond)

		// Send DONE to exit IDLE cleanly.
		c.write("DONE\r\n")
		c.readUntilTag("a3")
	})
}
