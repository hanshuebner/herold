package identity

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// This file declares test-only helpers that let tests in this package
// drive the per-method handlers without going through the
// protojmap.CapabilityRegistry's Execute path. Each handler's Execute
// reads the authenticated principal via protojmap.PrincipalFromContext
// (whose context key is package-private to protojmap), so tests cannot
// install a principal directly. Instead, the executeAs helpers
// short-circuit principal lookup.
//
// The helpers are not test-tagged because exposing them under the
// regular build tag is harmless: they are unexported and only the
// test files in this package call them.

type testPrincipalKey struct{}

// contextWithTestPrincipal stashes p in ctx under a package-private
// key so the executeAs helpers can recover it.
func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

// principalFor recovers the test principal stashed by
// contextWithTestPrincipal, falling back to protojmap's standard
// lookup. This keeps production code paths intact while letting the
// in-package tests drive the same handler logic.
func principalFor(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}

// executeAs runs g.Execute with a context carrying p.
func (g getHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return g.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (c changesHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return c.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (s setHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return s.Execute(contextWithTestPrincipal(context.Background(), p), args)
}
