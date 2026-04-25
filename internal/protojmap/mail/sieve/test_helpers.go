package sieve

import (
	"context"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// This file mirrors internal/protojmap/mail/identity/test_helpers.go: it
// declares unexported helpers that let in-package tests drive Execute
// without going through the dispatcher's authentication path. The
// protojmap package-private principal context key is owned by the Core
// agent's protojmap package; we cannot read or write it directly, so
// the helpers stash a separate test-only key and the
// requirePrincipal() override prefers it when present.
//
// The file is not _test.go-tagged because nothing here is sensitive:
// the helpers are unexported and only test files in this package
// reference them.

type testPrincipalKey struct{}

// contextWithTestPrincipal stores p under a package-private key so
// principalFromTestCtx can recover it. Used by sieve_test.go fixtures.
func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

// principalFromTestCtx returns the test-installed principal when
// present, falling back to protojmap's standard lookup. The handler's
// requirePrincipal calls this so the existing production code path
// continues to work in real servers.
func principalFromTestCtx(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}
