package thread

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// This file declares test-only helpers that let tests in this package
// drive the per-method handlers without going through the
// CapabilityRegistry's HTTP path. See identity/test_helpers.go for the
// reasoning.

type testPrincipalKey struct{}

func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

func principalFor(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}

func (g getHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return g.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (c changesHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return c.Execute(contextWithTestPrincipal(context.Background(), p), args)
}
