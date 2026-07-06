package imapimport

// debug_log_test.go verifies per-account debug logging via AppendSystemEvent
// (re #138, REQ-IMAP-IMP-XX):
//
//   - When DebugLog is enabled on an account, the worker emits system events
//     tagged with the account ID as actor_id during connection and IDLE/sync.
//   - When DebugLog is disabled, no imapimport.* system events appear.

import (
	"context"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// makeDebugAccount creates an account for debug log tests, then updates the
// debug_log flag to the given value before returning the account.
func makeDebugAccount(t *testing.T, s store.Store, ts *testIMAPServer, email, username, cred string, debugLog bool) store.IMAPImportAccount {
	t.Helper()
	acc := makeAccountWithFloor(t, s, ts, accountCfg{
		email:               email,
		username:            username,
		credentialPlaintext: cred,
	}, nil)
	dl := debugLog
	updated, err := s.Meta().UpdateIMAPImportAccount(context.Background(), store.IMAPImportAccountUpdate{
		ID:               acc.ID,
		PrincipalID:      acc.PrincipalID,
		AccountName:      acc.AccountName,
		Host:             acc.Host,
		Port:             acc.Port,
		TLSMode:          acc.TLSMode,
		Username:         acc.Username,
		AuthMethod:       acc.AuthMethod,
		State:            acc.State,
		DeletePropagates: acc.DeletePropagates,
		DebugLog:         &dl,
	})
	if err != nil {
		t.Fatalf("UpdateIMAPImportAccount (set debug_log=%v): %v", debugLog, err)
	}
	return updated
}

// TestDebugLog_EmitsSystemEvents verifies that when debug_log is true the
// worker appends system events tagged with the account ID as actor_id.
func TestDebugLog_EmitsSystemEvents(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("dbgon", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeDebugAccount(t, ha.Store, ts,
		"dbgon@example.test", "dbgon", "pw", true /* debug on */)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
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
		done <- w.attempt(ctx)
	}()

	// Wait for the worker to reach IDLE (initial sync completes).
	time.Sleep(1 * time.Second)

	// Cancel the worker so it exits cleanly.
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not exit after cancel within 10s")
	}

	// Query system events from the store; at least one must be tagged with
	// the account ID and have an action starting with "imapimport.".
	evs, err := ha.Store.Meta().ListSystemEvents(context.Background(), store.SystemEventFilter{
		Limit:   100,
		ActorID: acc.ID,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents: %v", err)
	}

	var found bool
	for _, ev := range evs {
		if ev.ActorID == acc.ID && len(ev.Action) >= 11 && ev.Action[:11] == "imapimport." {
			found = true
			break
		}
	}
	if !found {
		actions := make([]string, 0, len(evs))
		for _, ev := range evs {
			actions = append(actions, ev.Action)
		}
		t.Errorf("no imapimport.* system event with actor_id=%q found; events: %v", acc.ID, actions)
	}
}

// TestDebugLog_NoEventsWhenOff verifies that with debug_log disabled the
// worker does not append any imapimport.* system events.
func TestDebugLog_NoEventsWhenOff(t *testing.T) {
	ts := startTestIMAPServer(t)
	ts.addUser("dbgoff", "pw")

	ha, _ := testharness.Start(t, testharness.Options{})
	acc := makeDebugAccount(t, ha.Store, ts,
		"dbgoff@example.test", "dbgoff", "pw", false /* debug off */)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
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
		done <- w.attempt(ctx)
	}()

	// Let the worker run for a moment (initial sync + entering IDLE).
	time.Sleep(1 * time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not exit after cancel within 10s")
	}

	// No imapimport.* events should have been appended for this account.
	evs, err := ha.Store.Meta().ListSystemEvents(context.Background(), store.SystemEventFilter{
		Limit:   100,
		ActorID: acc.ID,
	})
	if err != nil {
		t.Fatalf("ListSystemEvents: %v", err)
	}
	for _, ev := range evs {
		if ev.ActorID == acc.ID && len(ev.Action) >= 11 && ev.Action[:11] == "imapimport." {
			t.Errorf("unexpected imapimport.* event when debug_log=false: action=%q actor_id=%q", ev.Action, ev.ActorID)
		}
	}
}
