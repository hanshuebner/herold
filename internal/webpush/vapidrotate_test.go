package webpush

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/vapid"
)

// TestDispatcher_VAPIDRotation_SkipsStale exercises the rotation filter:
// a subscription registered against an old VAPID public key (the
// dispatcher's manager now serves a different key) must be skipped
// with outcome=dropped_no_match_vapid and the row preserved (clients
// re-register on next session response).
func TestDispatcher_VAPIDRotation_SkipsStale(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)

	// Drop the existing subscription so the only push target carries
	// the wrong VAPID key.
	if err := f.store.Meta().DeletePushSubscription(context.Background(), f.subID); err != nil {
		t.Fatalf("DeletePushSubscription: %v", err)
	}
	other, err := vapid.Generate(nil)
	if err != nil {
		t.Fatalf("vapid.Generate(other): %v", err)
	}
	staleID, err := f.store.Meta().InsertPushSubscription(context.Background(), f.makeSubscription("device-stale", other.PublicKeyB64URL))
	if err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}
	f.triggerEmailChange(t)
	if got := len(f.gateway.Calls()); got != 0 {
		t.Fatalf("stale-VAPID subscription emitted %d POSTs; want 0", got)
	}
	// Subscription must still exist — the dispatcher does NOT prune.
	if _, err := f.store.Meta().GetPushSubscription(context.Background(), staleID); err != nil {
		t.Fatalf("subscription was deleted on VAPID mismatch: %v", err)
	}
}

// TestDispatcher_VAPIDRotation_PassesWhenMatching is the negative
// counterpart: a subscription whose key matches the manager's current
// key delivers normally. (The fixture's default subscription leaves
// VAPIDKeyAtRegistration empty, which already passes the filter; this
// test additionally pins the matching key to prove a positive match.)
func TestDispatcher_VAPIDRotation_PassesWhenMatching(t *testing.T) {
	t.Parallel()
	f := newDispatcherFixture(t, http.StatusCreated)
	cur, err := f.disp.vapid.PublicKeyB64URL()
	if err != nil {
		t.Fatalf("PublicKeyB64URL: %v", err)
	}
	if err := f.store.Meta().DeletePushSubscription(context.Background(), f.subID); err != nil {
		t.Fatalf("DeletePushSubscription: %v", err)
	}
	if _, err := f.store.Meta().InsertPushSubscription(context.Background(), f.makeSubscription("device-fresh", cur)); err != nil {
		t.Fatalf("InsertPushSubscription: %v", err)
	}
	f.triggerEmailChange(t)
	if got := len(f.gateway.Calls()); got != 1 {
		t.Fatalf("matching-VAPID subscription emitted %d POSTs; want 1", got)
	}
}
