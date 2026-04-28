package llmtransparency

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

type testPrincipalKey struct{}

// contextWithTestPrincipal injects a principal into ctx for tests. The
// production handlers call protojmap.PrincipalFromContext; in tests the
// JMAP middleware is absent so we inject via this helper.
func contextWithTestPrincipal(ctx context.Context, p store.Principal) context.Context {
	return context.WithValue(ctx, testPrincipalKey{}, p)
}

// principalFrom returns the authenticated principal from ctx. In
// production it delegates to the JMAP middleware value; in tests it
// falls back to the injected test principal.
func principalFrom(ctx context.Context) (store.Principal, bool) {
	if v, ok := ctx.Value(testPrincipalKey{}).(store.Principal); ok {
		return v, true
	}
	return protojmap.PrincipalFromContext(ctx)
}

func (g *getHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return g.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (i *llmInspectHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return i.Execute(contextWithTestPrincipal(context.Background(), p), args)
}
