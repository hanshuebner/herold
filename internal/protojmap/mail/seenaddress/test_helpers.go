package seenaddress

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// This file provides in-package test helpers that let tests drive the
// SeenAddress handlers without going through the
// CapabilityRegistry/HTTP path.  The protojmap.PrincipalFromContext
// context key is unexported, so test code cannot install a principal
// that way.  Instead we keep a parallel package-private test key and
// intercept it in principalFor.

type testPrincipalKey struct{}

func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

// principalFor is the package-internal principal accessor used by all
// SeenAddress handlers.  In tests it consults the test key; in
// production it falls through to protojmap.PrincipalFromContext.
func principalFor(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}

// executeAs variants let tests invoke handlers with an explicit principal.

func (g getHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return g.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (c changesHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return c.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (s setHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return s.Execute(contextWithTestPrincipal(context.Background(), p), args)
}
