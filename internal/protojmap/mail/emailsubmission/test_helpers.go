package emailsubmission

import (
	"context"
	"encoding/json"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

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

func (q queryHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return q.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (q queryChangesHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return q.Execute(contextWithTestPrincipal(context.Background(), p), args)
}

func (s setHandler) executeAs(p store.Principal, args json.RawMessage) (any, *protojmap.MethodError) {
	return s.Execute(contextWithTestPrincipal(context.Background(), p), args)
}
