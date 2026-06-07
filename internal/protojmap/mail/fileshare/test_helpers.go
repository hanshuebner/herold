package fileshare

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// This file declares test-only helpers that let in-package tests drive
// the handlers without going through the protojmap.CapabilityRegistry's
// dispatcher. Each handler reads the authenticated principal via
// protojmap.PrincipalFromContext (whose context key is package-private
// to protojmap); tests stash a principal under a package-private key
// instead and the principalFor helper checks both keys.

type testPrincipalKey struct{}

// contextWithTestPrincipal stashes p in ctx under a package-private key
// so the executeAs helpers recover it.
func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

// principalFor recovers the test principal or falls back to the
// production middleware value.
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

func (s setHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return s.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (q queryHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return q.Execute(contextWithTestPrincipal(context.Background(), p), args)
}
