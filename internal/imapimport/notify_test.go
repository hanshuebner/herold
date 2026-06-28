package imapimport

// notify_test.go covers NOTIFY watch mode (RFC 5465) (REQ-IMAP-IMP-27..29):
//
//   - NOTIFY+IDLE mode is selected when both capabilities are advertised.
//   - A rejected NOTIFY SET falls back to INBOX-only IDLE without erroring
//     the worker.
//   - A wake signal (simulating a non-INBOX event or NOTIFICATIONOVERFLOW)
//     triggers a full syncAllFolders round.
//   - NOOP-poll watch mode is set when neither IDLE nor NOTIFY is advertised.
//
// All tests drive the fake Conn/dialer; the imapmemserver fixture is not
// extended with server-side NOTIFY (the server does not advertise CapNotify).
// notifyCapDialer wraps fakeDialer and injects CapNotify into the caps while
// intercepting EnableNotify so the real IMAP NOTIFY command is never sent to
// the test server.

import (
	"context"
	"sync"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// --------------------------------------------------------------------------
// notifyCapConn: wraps a prodConn to add CapNotify and intercept EnableNotify.
// --------------------------------------------------------------------------

// notifyCapConn wraps an underlying Conn (a *prodConn from fakeDialer) and
// adds imap.CapNotify to the advertised capabilities. EnableNotify is
// intercepted and returns notifyErr instead of issuing a real IMAP command to
// the test server (which does not support NOTIFY). The notify channel from the
// underlying prodConn is exposed so tests can trigger artificial wakes.
type notifyCapConn struct {
	Conn
	notifyErr error
	notify    chan struct{} // same channel as the underlying prodConn.notify
}

// Caps returns the underlying capability set with CapNotify added.
func (c *notifyCapConn) Caps() imap.CapSet {
	out := make(imap.CapSet)
	for k, v := range c.Conn.Caps() {
		out[k] = v
	}
	out[imap.CapNotify] = struct{}{}
	return out
}

// EnableNotify returns notifyErr (nil for success, errNotifyRejected for a
// simulated server rejection). The real IMAP NOTIFY command is never sent.
func (c *notifyCapConn) EnableNotify(_ context.Context) error {
	return c.notifyErr
}

// triggerWake simulates an unsolicited server event (or NOTIFICATIONOVERFLOW)
// by writing to the notify channel that the prodIdleHandle.Wait reads from.
// The IDLE loop wakes exactly as it would for a real EXISTS/EXPUNGE/FETCH or
// NOTIFICATIONOVERFLOW response (REQ-IMAP-IMP-28).
func (c *notifyCapConn) triggerWake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// --------------------------------------------------------------------------
// notifyCapDialer: wraps fakeDialer and returns notifyCapConn connections.
// --------------------------------------------------------------------------

// notifyCapDialer wraps an inner Dialer and adds CapNotify to all returned
// connections. The first Dial call stores the primary connection so tests can
// trigger artificial wakes on it.
type notifyCapDialer struct {
	inner     Dialer
	notifyErr error // nil for success, errNotifyRejected for rejection tests

	mu      sync.Mutex
	primary *notifyCapConn // set on the first Dial call (the primary conn)
}

func (d *notifyCapDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	c, err := d.inner.Dial(ctx, p)
	if err != nil {
		return nil, err
	}
	// White-box access: fakeDialer always returns *prodConn.
	pc := c.(*prodConn)
	nc := &notifyCapConn{
		Conn:      pc,
		notifyErr: d.notifyErr,
		notify:    pc.notify,
	}
	d.mu.Lock()
	if d.primary == nil {
		d.primary = nc
	}
	d.mu.Unlock()
	return nc, nil
}

// primaryConn returns the first connection opened by this dialer (the primary
// IDLE connection). May be nil before the first Dial call completes.
func (d *notifyCapDialer) primaryConn() *notifyCapConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.primary
}

// --------------------------------------------------------------------------
// Test: NOTIFY+IDLE mode selected when both capabilities are advertised.
// --------------------------------------------------------------------------

// TestNOTIFYModeSelected verifies that when the upstream advertises both
// NOTIFY and IDLE, the worker enters NOTIFY+IDLE watch mode (WatchMode ==
// "notify") after the initial sync. REQ-IMAP-IMP-27/29.
func TestNOTIFYModeSelected(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("notifymode1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "notifymode1@example.test",
		username:            "notifymode1",
		credentialPlaintext: "pw",
	}, nil)

	nd := &notifyCapDialer{
		inner:     &fakeDialer{ts: ts},
		notifyErr: nil, // NOTIFY SET succeeds
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      nd,
		categoriser: noopCategoriser{},
	})

	done := make(chan error, 1)
	go func() { done <- w.attempt(ctx) }()

	// Wait until the watch mode reaches "notify".
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := w.status.snapshot(); s.WatchMode == "notify" {
			cancel()
			<-done
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done
	t.Errorf("watch mode did not reach 'notify' within deadline; got %q",
		w.status.snapshot().WatchMode)
}

// --------------------------------------------------------------------------
// Test: rejected NOTIFY SET falls back to INBOX-only IDLE.
// --------------------------------------------------------------------------

// TestNOTIFYRejectedFallsBackToIDLE verifies that when the server rejects the
// NOTIFY SET command (NO/BAD), the worker falls back to INBOX-only IDLE watch
// mode and does NOT flip to errored state. REQ-IMAP-IMP-27.
func TestNOTIFYRejectedFallsBackToIDLE(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("notifyreject1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "notifyreject1@example.test",
		username:            "notifyreject1",
		credentialPlaintext: "pw",
	}, nil)

	nd := &notifyCapDialer{
		inner:     &fakeDialer{ts: ts},
		notifyErr: errNotifyRejected, // server rejects NOTIFY SET
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      nd,
		categoriser: noopCategoriser{},
	})

	done := make(chan error, 1)
	go func() { done <- w.attempt(ctx) }()

	// Wait until the watch mode reaches "idle" (the fallback).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := w.status.snapshot()
		if s.WatchMode == "idle" {
			// The worker must NOT be in errored phase (REQ-IMAP-IMP-27).
			if s.Phase == PhaseErrored {
				t.Error("worker must not flip to errored after a rejected NOTIFY SET")
			}
			cancel()
			<-done
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done
	t.Errorf("watch mode did not fall back to 'idle' within deadline; got %q",
		w.status.snapshot().WatchMode)
}

// --------------------------------------------------------------------------
// Test: wake signal triggers full syncAllFolders.
// --------------------------------------------------------------------------

// TestNOTIFYWakeTriggersSync verifies that an artificial wake on the notify
// channel — which simulates either a non-INBOX NOTIFY event or a
// NOTIFICATIONOVERFLOW response (REQ-IMAP-IMP-28) — causes the worker to run
// syncAllFolders and pick up a message that was placed in a non-INBOX folder
// after the initial sync. REQ-IMAP-IMP-27/28.
func TestNOTIFYWakeTriggersSync(t *testing.T) {
	ts := startTestIMAPServer(t)
	u := ts.addUser("notifywake1", "pw")
	// Create a Sent folder on the upstream so the worker can map it.
	if err := u.Create("Sent", nil); err != nil {
		t.Fatalf("create Sent: %v", err)
	}

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "notifywake1@example.test",
		username:            "notifywake1",
		credentialPlaintext: "pw",
	}, nil)

	nd := &notifyCapDialer{
		inner:     &fakeDialer{ts: ts},
		notifyErr: nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         sysconfig.IMAPImportConfig{},
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      nd,
		categoriser: noopCategoriser{},
	})

	done := make(chan error, 1)
	go func() { done <- w.attempt(ctx) }()

	// Wait for NOTIFY+IDLE to be armed before doing anything.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := w.status.snapshot()
		if s.WatchMode == "notify" && s.Phase == PhaseIdle {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := w.status.snapshot(); s.WatchMode != "notify" {
		cancel()
		<-done
		t.Fatalf("worker did not reach notify+idle; watch=%q phase=%q", s.WatchMode, s.Phase)
	}

	// Allow a short window for the IDLE command to be armed on the server.
	time.Sleep(100 * time.Millisecond)

	// Append a message to Sent (non-INBOX). With NOTIFY active on a real
	// server this would push an event; on the test server we trigger the
	// wake manually to simulate a NOTIFY event (or NOTIFICATIONOVERFLOW).
	d := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	raw := buildRFC822("notify-wake@test", "NOTIFY Wake Test", d)
	appendToServer(t, ts, "notifywake1", "pw", "Sent", raw, nil, d)

	// Trigger the wake (simulates a non-INBOX NOTIFY event or overflow).
	if pc := nd.primaryConn(); pc != nil {
		pc.triggerWake()
	}

	// Poll until the Sent message appears in herold.
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if n := countMailboxMessages(t, ha.Store, acc.PrincipalID, "Sent"); n == 1 {
			cancel()
			<-done
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done
	t.Error("Sent message not synced after NOTIFY wake within deadline")
}

// --------------------------------------------------------------------------
// Test: watch mode "poll" is set when IDLE is not advertised.
// --------------------------------------------------------------------------

// TestNOTIFYWatchModePoll verifies that when the upstream does not advertise
// IDLE (or NOTIFY), the worker sets WatchMode == "poll". REQ-IMAP-IMP-29.
func TestNOTIFYWatchModePoll(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("notifypoll1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "notifypoll1@example.test",
		username:            "notifypoll1",
		credentialPlaintext: "pw",
	}, nil)

	// Short poll interval so the worker reaches the polling phase quickly.
	cfg := sysconfig.IMAPImportConfig{
		PollInterval: sysconfig.Duration(100 * time.Millisecond),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	w := newAccountWorker(accountWorkerOpts{
		account:     acc,
		store:       ha.Store,
		dataKey:     testDataKey(t),
		cfg:         cfg,
		log:         newTestLogger(t),
		clk:         ha.Clock,
		dialer:      &noIDLEDialer{inner: &fakeDialer{ts: ts}},
		categoriser: noopCategoriser{},
	})

	done := make(chan error, 1)
	go func() { done <- w.attempt(ctx) }()

	// Wait for the worker to enter polling phase with watch mode "poll".
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := w.status.snapshot()
		if s.WatchMode == "poll" && s.Phase == PhasePolling {
			cancel()
			<-done
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done
	s := w.status.snapshot()
	t.Errorf("worker did not reach poll watch mode; watch=%q phase=%q", s.WatchMode, s.Phase)
}

// --------------------------------------------------------------------------
// Test: watch mode "idle" is set when only IDLE is advertised (no NOTIFY).
// --------------------------------------------------------------------------

// TestNOTIFYWatchModeIdleOnly verifies that when the upstream advertises IDLE
// but not NOTIFY, WatchMode is "idle". REQ-IMAP-IMP-29.
func TestNOTIFYWatchModeIdleOnly(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("notifyidle1", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeAccountWithFloor(t, ha.Store, ts, accountCfg{
		email:               "notifyidle1@example.test",
		username:            "notifyidle1",
		credentialPlaintext: "pw",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Standard fakeDialer: test server advertises IMAP4rev2 (includes IDLE)
	// but NOT NOTIFY.
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

	done := make(chan error, 1)
	go func() { done <- w.attempt(ctx) }()

	// Wait for the worker to enter the IDLE phase with watch mode "idle".
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := w.status.snapshot()
		if s.WatchMode == "idle" && s.Phase == PhaseIdle {
			cancel()
			<-done
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done
	s := w.status.snapshot()
	t.Errorf("worker did not reach idle watch mode; watch=%q phase=%q", s.WatchMode, s.Phase)
}
