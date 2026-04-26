package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// TestCLICategorisePromptSet_Stdin tests "categorise prompt set" reading from
// stdin (REQ-FILT-211).
func TestCLICategorisePromptSet_Stdin(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "prompt-stdin@test.local")

	// Run with the prompt body on stdin.
	out, _, err := env.runWithStdin(
		"You are a mail sorter. Use your best judgement.",
		"categorise", "prompt", "set", "prompt-stdin@test.local", "--json",
	)
	if err != nil {
		t.Fatalf("prompt set (stdin): %v", err)
	}
	if !strings.Contains(out, "prompt") {
		t.Fatalf("expected prompt field in output: %s", out)
	}
}

// TestCLICategorisePromptSet_File tests "categorise prompt set --file".
func TestCLICategorisePromptSet_File(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "prompt-file@test.local")

	// Write prompt to a temp file.
	dir := t.TempDir()
	f := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(f, []byte("Sort this mail carefully."), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	out, _, err := env.run("categorise", "prompt", "set",
		"prompt-file@test.local", "--file", f, "--json")
	if err != nil {
		t.Fatalf("prompt set (file): %v", err)
	}
	if !strings.Contains(out, "prompt") {
		t.Fatalf("expected prompt field in output: %s", out)
	}
}

// TestCLICategorisePromptShow tests "categorise prompt show".
func TestCLICategorisePromptShow(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "show-prompt@test.local")

	// Set a known prompt first.
	const wantPrompt = "My custom LLM prompt."
	if _, _, err := env.runWithStdin(wantPrompt,
		"categorise", "prompt", "set", "show-prompt@test.local"); err != nil {
		t.Fatalf("prompt set: %v", err)
	}

	// Show it back.
	out, _, err := env.run("categorise", "prompt", "show", "show-prompt@test.local")
	if err != nil {
		t.Fatalf("prompt show: %v", err)
	}
	if !strings.Contains(out, wantPrompt) {
		t.Fatalf("expected prompt %q in output; got %s", wantPrompt, out)
	}
}

// TestCLICategoriseListCategories tests "categorise list-categories".
func TestCLICategoriseListCategories(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "listcat@test.local")

	// The default config seeds a category set; just verify the output is non-empty.
	out, _, err := env.run("categorise", "list-categories", "listcat@test.local")
	if err != nil {
		t.Fatalf("list-categories: %v", err)
	}
	// The default category set contains "primary" (REQ-FILT-201).
	if !strings.Contains(out, "primary") {
		t.Fatalf("expected 'primary' in list-categories output; got %s", out)
	}
}

// TestCLICategorisePromptSet_EmptyBodyRejected verifies that an empty prompt
// body is rejected before making any REST call.
func TestCLICategorisePromptSet_EmptyBodyRejected(t *testing.T) {
	env := newCLITestEnv(t, nil)
	seedPrincipal(t, env, "empty-prompt@test.local")

	_, _, err := env.runWithStdin("",
		"categorise", "prompt", "set", "empty-prompt@test.local")
	if err == nil {
		t.Fatalf("expected error for empty prompt body")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected 'empty' in error message; got %v", err)
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
