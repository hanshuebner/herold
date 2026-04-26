package coach

import (
	"context"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// This file mirrors internal/protojmap/push/test_helpers.go: it
// declares unexported helpers that let in-package tests drive Execute
// without going through the dispatcher's authentication path.

type testPrincipalKey struct{}

// contextWithTestPrincipal stores p under a package-private key so
// principalFromTestCtx can recover it.
func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

// principalFromTestCtx returns the test-installed principal when
// present, falling back to protojmap's standard lookup.
func principalFromTestCtx(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}
