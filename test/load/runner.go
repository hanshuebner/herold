package load

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// BackendFromEnv selects the store backend from the STORE_BACKEND environment
// variable.  Defaults to "sqlite".  Returns the backend name, a factory
// function that opens a fresh store, and any skip reason (non-empty if the
// requested backend is not available).
func BackendFromEnv(t testing.TB) (name string, factory func(t testing.TB) store.Store, skip string) {
	t.Helper()
	backend := os.Getenv("STORE_BACKEND")
	if backend == "" {
		backend = "sqlite"
	}
	switch backend {
	case "sqlite":
		return "sqlite", func(t testing.TB) store.Store {
			t.Helper()
			dir := t.TempDir()
			s, err := storesqlite.Open(context.Background(), filepath.Join(dir, "load.db"), nil, nil)
			if err != nil {
				t.Fatalf("storesqlite.Open: %v", err)
			}
			return s
		}, ""

	case "postgres":
		dsn := os.Getenv("HEROLD_PG_DSN")
		if dsn == "" {
			return "postgres", nil, "STORE_BACKEND=postgres but HEROLD_PG_DSN is not set"
		}
		return "postgres", func(t testing.TB) store.Store {
			t.Helper()
			// Import storepg at runtime only when Postgres is selected to
			// avoid pulling the Postgres driver into the default build.
			// Because test binaries are compiled with all imports resolved
			// at link time, we vendor the factory construction inline here
			// rather than using a plugin.  The build tag approach is
			// reserved for CGO isolation; both backends are pure Go so
			// this is fine.
			return openPostgresStore(t, dsn)
		}, ""

	default:
		return backend, nil, fmt.Sprintf("unknown STORE_BACKEND: %q (want sqlite or postgres)", backend)
	}
}

// RunScenario is the top-level helper that tests call.  It:
//  1. Opens the backend store.
//  2. Spins up a Harness.
//  3. Runs sc.Run.
//  4. Writes the JSON summary to dir (or a default path under os.TempDir()).
//  5. Fails the test if any gate did not pass.
func RunScenario(t testing.TB, sc Scenario, opts HarnessOpts) *RunResult {
	t.Helper()

	name, factory, skip := BackendFromEnv(t)
	if skip != "" {
		t.Skipf("load: %s", skip)
	}
	if opts.Backend == "" {
		opts.Backend = name
	}
	if opts.Store == nil {
		opts.Store = factory(t)
		t.Cleanup(func() { _ = opts.Store.Close() })
	}

	h := newHarness(t, opts)

	// The scenario owns its own deadline (Scenario.Run wraps the per-run
	// context with the scenario-specific TimeoutSeconds). The harness-level
	// context is unbounded; the outer `go test -timeout=...` flag is the
	// backstop for runaway scenarios.
	ctx := context.Background()

	r := sc.Run(ctx, h)

	// Always write the JSON summary.
	dir := opts.RunsDir
	if dir == "" {
		ts := time.Now().UTC().Format("20060102T150405Z")
		dir = filepath.Join(repoRoot(), "test", "load", "runs", ts)
	}
	if path, err := writeJSON(r, dir); err != nil {
		t.Logf("load: write JSON: %v", err)
	} else {
		t.Logf("load: result written to %s", path)
	}

	// Print summary to test output.
	b, _ := json.MarshalIndent(r, "", "  ")
	t.Logf("load result:\n%s", b)

	// Fail on gate violations.
	if !r.Passed {
		for _, g := range r.Gates {
			if !g.Passed {
				t.Errorf("gate %q: required %s %.4g, measured %.4g",
					g.Name, g.Direction, g.Required, g.Measured)
			}
		}
	}
	return r
}

// repoRoot returns the absolute path to the repo root by walking up from
// the binary's working directory until a go.mod is found.  Falls back to
// os.TempDir() when not inside a module.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return os.TempDir()
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return os.TempDir()
		}
		dir = parent
	}
}
