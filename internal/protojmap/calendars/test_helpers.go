package calendars

// This file is compiled in non-test builds (no _test.go suffix); the
// helpers it exposes are used both by external _test.go files and by
// callers that wire the package into the test harness directly. The
// contacts sibling package follows the same convention.

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// testPrincipalKey is the context key used by tests to inject an
// authenticated principal without going through the JMAP middleware.
type testPrincipalKey struct{}

// ContextWithTestPrincipal attaches p to ctx so that handlers see it
// via protojmap.PrincipalFromContext. Exported only for the package's
// own _test.go files.
func ContextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}
