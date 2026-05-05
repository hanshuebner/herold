package load

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Scenario is the interface every load scenario must implement.
// Run receives a live Harness and drives the workload.  It returns a
// RunResult that the outer runner serialises to JSON.
//
// Scenarios must honour ctx cancellation promptly and return a
// non-nil RunResult even when ctx is cancelled (record what was
// measured up to cancellation).
type Scenario interface {
	// Name returns the short identifier used in JSON output and file names.
	Name() string
	// Run executes the scenario against h and returns structured results.
	Run(ctx context.Context, h *Harness) *RunResult
}

// RunResult is the JSON-serialisable outcome of one scenario execution.
// The shape is stable; callers (CI, nightly, scripts) should rely on it.
type RunResult struct {
	// Scenario is the scenario name.
	Scenario string `json:"scenario"`
	// Backend is the store backend used ("sqlite" or "postgres").
	Backend string `json:"backend"`
	// StartedAt is the wall-clock time the scenario started (RFC 3339).
	StartedAt string `json:"started_at"`
	// DurationSeconds is the elapsed wall time.
	DurationSeconds float64 `json:"duration_seconds"`
	// Passed is true when all pass/fail gates held.
	Passed bool `json:"passed"`
	// Gates records each gate's name, required value, measured value, and
	// whether it passed.
	Gates []Gate `json:"gates"`
	// Metrics is a bag of named measurements surfaced by the scenario (e.g.
	// messages_delivered, p99_latency_ms, peak_rss_bytes).
	Metrics map[string]float64 `json:"metrics"`
	// Errors is a list of non-fatal error strings observed during the run.
	Errors []string `json:"errors,omitempty"`
	// PprofDir is the path where pprof profiles were written, if any.
	PprofDir string `json:"pprof_dir,omitempty"`
	// GoVersion is the Go runtime version used during the run.
	GoVersion string `json:"go_version"`
	// GOOS / GOARCH allow comparing runs across platforms.
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

// Gate is a single pass/fail assertion on a measured metric.
type Gate struct {
	// Name is a human-readable identifier for the gate.
	Name string `json:"name"`
	// Required is the threshold value (semantics: Measured >= Required for >=
	// direction, Measured <= Required for <= direction).
	Required float64 `json:"required"`
	// Measured is the value actually observed.
	Measured float64 `json:"measured"`
	// Direction is ">=" or "<=".
	Direction string `json:"direction"`
	// Passed is true when the direction-specific comparison held.
	Passed bool `json:"passed"`
}

// newRunResult creates a skeleton RunResult pre-populated with runtime and
// start-time fields.  Scenarios call this at the top of Run.
func newRunResult(name, backend string) (*RunResult, time.Time) {
	start := time.Now()
	return &RunResult{
		Scenario:  name,
		Backend:   backend,
		StartedAt: start.UTC().Format(time.RFC3339),
		Metrics:   make(map[string]float64),
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}, start
}

// finish stamps the elapsed duration and recomputes the top-level Passed
// field from all gates.  Call at the end of Run.
func (r *RunResult) finish(start time.Time) {
	r.DurationSeconds = time.Since(start).Seconds()
	r.Passed = true
	for _, g := range r.Gates {
		if !g.Passed {
			r.Passed = false
			break
		}
	}
}

// addGateGTE adds a gate of the form "measured >= required" and evaluates it.
func (r *RunResult) addGateGTE(name string, required, measured float64) {
	r.Gates = append(r.Gates, Gate{
		Name:      name,
		Required:  required,
		Measured:  measured,
		Direction: ">=",
		Passed:    measured >= required,
	})
}

// addGateLTE adds a gate of the form "measured <= required" and evaluates it.
func (r *RunResult) addGateLTE(name string, required, measured float64) {
	r.Gates = append(r.Gates, Gate{
		Name:      name,
		Required:  required,
		Measured:  measured,
		Direction: "<=",
		Passed:    measured <= required,
	})
}

// addError records a non-fatal error string.
func (r *RunResult) addError(err error) {
	r.Errors = append(r.Errors, err.Error())
}

// writeJSON writes the RunResult as indented JSON to dir/<scenario>_<timestamp>.json
// and returns the file path.
func writeJSON(r *RunResult, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	name := filepath.Join(dir, fmt.Sprintf("%s_%s.json", r.Scenario, ts))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(name, b, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	return name, nil
}
