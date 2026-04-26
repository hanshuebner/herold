package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
)

// stubCategoriser drives the protoadmin recategorise endpoint without
// pulling in the full categorise package.
type stubCategoriser struct {
	processed int
	err       error
	progress  []int // captured (done, total) pairs flattened: d0,t0,d1,t1...
}

func (s *stubCategoriser) RecategoriseRecent(ctx context.Context, _ store.PrincipalID, limit int, progress func(done, total int)) (int, error) {
	steps := 3
	for i := 1; i <= steps; i++ {
		if progress != nil {
			progress(i, steps)
			s.progress = append(s.progress, i, steps)
		}
		// small sleep to let the polling client see at least one update
		time.Sleep(10 * time.Millisecond)
	}
	if s.err != nil {
		return 0, s.err
	}
	if s.processed == 0 {
		s.processed = steps
	}
	return s.processed, nil
}

func TestCLICategoriseRecategorise_Sync(t *testing.T) {
	stub := &stubCategoriser{}
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.Categoriser = stub
	})
	seedPrincipal(t, env, "cat@test.local")
	out, _, err := env.run("categorise", "recategorise", "cat@test.local",
		"--poll-interval", "20ms", "--json")
	if err != nil {
		t.Fatalf("recategorise: %v", err)
	}
	// The terminal snapshot must report state=done.
	if !strings.Contains(out, `"state": "done"`) && !strings.Contains(out, `"state":"done"`) {
		t.Fatalf("expected state=done in final snapshot; got %s", out)
	}
}

func TestCLICategoriseRecategorise_Async(t *testing.T) {
	stub := &stubCategoriser{}
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.Categoriser = stub
	})
	seedPrincipal(t, env, "cat-async@test.local")
	out, _, err := env.run("categorise", "recategorise", "cat-async@test.local",
		"--async", "--json")
	if err != nil {
		t.Fatalf("recategorise --async: %v", err)
	}
	if !strings.Contains(out, "jobId") {
		t.Fatalf("expected jobId in async output: %s", out)
	}
}

func TestCLICategoriseRecategorise_Failure(t *testing.T) {
	stub := &stubCategoriser{err: errors.New("boom")}
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.Categoriser = stub
	})
	seedPrincipal(t, env, "cat-fail@test.local")
	_, _, err := env.run("categorise", "recategorise", "cat-fail@test.local",
		"--poll-interval", "20ms")
	if err == nil {
		t.Fatalf("expected error for failed job")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the failure message in the error: %v", err)
	}
}

func TestCLICategoriseRecategorise_NoCategoriser(t *testing.T) {
	env := newCLITestEnv(t, nil) // no Categoriser wired
	seedPrincipal(t, env, "no-cat@test.local")
	_, _, err := env.run("categorise", "recategorise", "no-cat@test.local")
	if err == nil {
		t.Fatalf("expected 501 not_implemented")
	}
	if !strings.Contains(err.Error(), "501") && !strings.Contains(err.Error(), "not_implemented") {
		t.Fatalf("expected 501; got %v", err)
	}
}

func TestIntFromAny(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{float64(7), 7},
		{int(3), 3},
		{int64(9), 9},
		{"12", 12},
		{"junk", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := intFromAny(c.in); got != c.want {
			t.Errorf("intFromAny(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
