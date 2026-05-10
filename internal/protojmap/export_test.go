package protojmap

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// RunEventSourceForTest exposes the unexported runEventSource push loop
// so a test can swap in a synthetic poller and assert the
// REQ-EXTIMG-BG-INTERNAL-12 / -62 invariant: a polling cycle that
// surfaces only cause = 'background' rows advances the cursor without
// arming the SSE flush timer.
func (s *Server) RunEventSourceForTest(
	ctx context.Context,
	log *slog.Logger,
	w http.ResponseWriter,
	flusher http.Flusher,
	p store.Principal,
	types map[string]struct{},
	closeAfterState bool,
	ping time.Duration,
	poll func(ctx context.Context, pid store.PrincipalID, fromSeq store.ChangeSeq, max int) ([]store.StateChange, error),
) error {
	return s.runEventSource(ctx, log, w, flusher, p, types, closeAfterState, ping, changeFeedPoller(poll))
}

// HashAPIKeyForTest exposes the package-internal hash function so the
// test fixture can mint API keys without going through protoadmin's
// surface. Test-only by virtue of the _test.go filename.
func HashAPIKeyForTest(plaintext string) string { return hashAPIKey(plaintext) }

// CollectStateMapForTest exposes the EventSource state-derivation path
// so we can assert that buildStateChange now reads from the change
// feed for change-feed-driven types (Email, Mailbox, Thread). The
// types set follows the same semantics as the wire path: nil means
// "all known types"; an explicit set restricts to those names.
func (s *Server) CollectStateMapForTest(ctx context.Context, p store.Principal, types map[string]struct{}) (map[string]string, error) {
	return s.collectStateMap(ctx, p, types)
}

// BuildClientlogCapForTest exposes buildClientLogCapability so test
// code can inspect the capability descriptor without going through HTTP
// and the cookie auth path.
func (s *Server) BuildClientlogCapForTest(ctx context.Context, sessionID string) clientLogCapability {
	return s.buildClientLogCapability(ctx, sessionID)
}

// SessionStateForTest exposes sessionState for tests that want to
// assert the state changes when clientlog columns change. It attaches
// a synthetic principal and session_id to the context.
func (s *Server) SessionStateForTest(ctx context.Context, pid store.PrincipalID, sessionID string) string {
	p, err := s.store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		return "lookup-failed"
	}
	ctx = context.WithValue(ctx, ctxKeyPrincipal, p)
	ctx = context.WithValue(ctx, ctxKeySessionID, sessionID)
	return s.sessionState(ctx)
}
