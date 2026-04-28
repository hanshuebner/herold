package llmtest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ErrFixtureMissing is returned when the Replayer is asked for a
// (kind, prompt_hash) pair that has no recorded fixture. The error
// message names the missing key so the test output points directly at
// the fixture that needs to be regenerated (REQ-FILT-302):
//
//	llmtest: fixture missing: kind=categorise hash=ab12…
//	  -> run scripts/llm-capture.sh to record fixtures; see Wave 3.16
var ErrFixtureMissing = errors.New("llmtest: fixture missing")

// Replayer serves recorded LLM responses from a fixture file. It
// satisfies both ChatCompleter (for internal/categorise) and
// SpamInvoker (for internal/spam).
//
// Construct one with LoadReplayer; do not create directly.
type Replayer struct {
	kind     FixtureKind
	fixtures map[FixtureKey]*FixtureEntry
}

// NewReplayer builds a Replayer from an already-opened fixture slice.
// Tests that need more control than LoadReplayer provides use this
// directly.
func NewReplayer(kind FixtureKind, entries []FixtureEntry) *Replayer {
	r := &Replayer{kind: kind, fixtures: make(map[FixtureKey]*FixtureEntry, len(entries))}
	for i := range entries {
		e := entries[i]
		r.fixtures[FixtureKey{Kind: e.Kind, Hash: e.PromptHash}] = &e
	}
	return r
}

// LoadReplayer reads the fixture file for the supplied kind from the
// canonical path relative to the calling test's package directory
// (internal/llmtest/fixtures/<kind>/<pkg>.jsonl) and returns a
// Replayer backed by those fixtures.
//
// If the fixture file does not exist the Replayer is empty; every call
// will return ErrFixtureMissing (which causes the test to fail with a
// clear "regenerate fixtures" message rather than a silent stale pass).
//
// t.Helper() is called so any failure is attributed to the test, not
// to this helper.
func LoadReplayer(t *testing.T, kind FixtureKind) *Replayer {
	t.Helper()
	path := fixtureFilePath(kind, callerPkg())
	entries := loadFixtureFile(t, path)
	return NewReplayer(kind, entries)
}

// Complete implements ChatCompleter for the categorise package.
// The lookup key is (kind, SHA-256(prompt+userContent)) — the full
// combined string is hashed so that any change in either the system
// prompt or the user payload invalidates the fixture.
func (r *Replayer) Complete(_ context.Context, prompt, userContent string) (string, error) {
	combined := prompt + "\n" + userContent
	hash := HashPrompt(combined)
	key := FixtureKey{Kind: r.kind, Hash: hash}
	e, ok := r.fixtures[key]
	if !ok {
		return "", fmt.Errorf("%w: kind=%s hash=%s\n  -> run scripts/llm-capture.sh to record fixtures; see Wave 3.16",
			ErrFixtureMissing, r.kind, hash)
	}
	// The categorise package expects the assistant's text content as a
	// JSON string, not a raw JSON object. The fixture stores the full
	// response object; extract the "content" field if it is a string,
	// otherwise return the raw bytes as a string.
	var obj struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(e.Response, &obj) == nil && obj.Content != "" {
		return obj.Content, nil
	}
	return string(e.Response), nil
}

// Call implements SpamInvoker for the spam package. The lookup key is
// (kind, SHA-256(JSON(params))) so any change in the request payload
// invalidates the fixture.
func (r *Replayer) Call(_ context.Context, _, _ string, params any, result any) error {
	hash, err := spamKey(params)
	if err != nil {
		return fmt.Errorf("llmtest replayer: marshal params: %w", err)
	}
	key := FixtureKey{Kind: r.kind, Hash: hash}
	e, ok := r.fixtures[key]
	if !ok {
		return fmt.Errorf("%w: kind=%s hash=%s\n  -> run scripts/llm-capture.sh to record fixtures; see Wave 3.16",
			ErrFixtureMissing, r.kind, hash)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(e.Response, result)
}

// loadFixtureFile reads a JSONL fixture file. If the file does not
// exist it returns nil (empty slice) rather than failing; the Replayer
// then surfaces ErrFixtureMissing on every call.
func loadFixtureFile(t *testing.T, path string) []FixtureEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("llmtest: open fixture file %s: %v", path, err)
	}
	defer f.Close()

	var entries []FixtureEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		var e FixtureEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("llmtest: parse fixture %s line %d: %v", path, lineNum, err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("llmtest: scan fixture %s: %v", path, err)
	}
	return entries
}

// fixtureFilePath returns the canonical fixture file path:
//
//	<repo>/internal/llmtest/fixtures/<kind>/<pkg>.jsonl
//
// The repo root is located by walking up from this file's compile-time
// path until go.mod is found. pkg is the last component of the calling
// test package's import path (e.g. "categorise_test").
func fixtureFilePath(kind FixtureKind, pkg string) string {
	return filepath.Join(thisPackageDir(), "fixtures", string(kind), pkg+".jsonl")
}

// thisPackageDir returns the absolute directory of this source file,
// which is always internal/llmtest/. We use runtime.Caller at package
// init so the path is available without os.Getwd() (which is CWD of
// the test binary, not the source tree).
func thisPackageDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("llmtest: runtime.Caller failed")
	}
	return filepath.Dir(file)
}

// callerPkg returns the last component of the calling test's package
// path, used to construct the fixture file name. It walks up the call
// stack past llmtest frames.
func callerPkg() string {
	for skip := 1; skip < 20; skip++ {
		pc, _, _, ok := runtime.Caller(skip)
		if !ok {
			break
		}
		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}
		name := fn.Name()
		// Skip any frame inside this package.
		if strings.Contains(name, "llmtest") {
			continue
		}
		// The function name is "<import-path>.<FuncName>". The import
		// path is everything up to the last dot-delimited segment. We
		// want the last path component.
		parts := strings.Split(name, "/")
		last := parts[len(parts)-1]
		// Strip the ".FuncName" suffix to get just the package name.
		if dot := strings.Index(last, "."); dot >= 0 {
			last = last[:dot]
		}
		return last
	}
	return "unknown"
}
