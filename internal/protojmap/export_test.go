package protojmap

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

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
