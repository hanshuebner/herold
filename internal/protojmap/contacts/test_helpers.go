package contacts

// This file is compiled in non-test builds because it carries no
// _test.go suffix; the helpers it exposes are used both by external
// _test.go files and by callers that wire the package into the test
// harness directly. (Other JMAP datatype packages follow the same
// convention.)

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
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

// principalFor returns the principal attached to ctx, falling back to
// the standard protojmap context lookup. Used by handlers when they
// run under the test fixture; production traffic flows through the
// dispatcher's auth middleware which uses
// protojmap.PrincipalFromContext directly.
func principalFor(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}

// rawJSON is a tiny helper for tests building method args.
func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
