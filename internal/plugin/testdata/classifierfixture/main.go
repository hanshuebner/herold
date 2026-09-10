// Command classifierfixture is a deterministic classifier-type plugin
// binary used only by internal/plugin, internal/admin, and
// internal/imapimport test suites to exercise the mail.classify contract
// (Wave 4.3, issue #304) end-to-end, as a real child process -- no mocks
// at the process boundary (STANDARDS section 8). It is not a first-party
// plugin (docs/design/server/implementation/08-classifier-plugin.md
// names herold-spam-llm as that); it lives under testdata so `go build
// ./...` and `go vet ./...` skip it by the standard testdata convention,
// and the tests that use it build it explicitly by import path (mirrors
// internal/plugin/testdata/spamfixture).
//
// Behaviour is controlled entirely by environment variables so a test
// can script exact responses without shaping JSON-RPC frames itself:
//
//   - HEROLD_TEST_CLASSIFY_VERDICT: "ham" | "spam" | "suspect" |
//     "unclassified" (default "ham").
//   - HEROLD_TEST_CLASSIFY_SCORE: float, default "0.1".
//   - HEROLD_TEST_CLASSIFY_REASON: free text, default "".
//   - HEROLD_TEST_CLASSIFY_CATEGORY: exact category name to return
//     (default ""). Takes precedence over
//     HEROLD_TEST_CLASSIFY_CATEGORY_FROM_SET. Ignored on the
//     spam.classify wire contract (SpamClassifyResult carries no
//     category field), matching a real TypeSpam plugin.
//   - HEROLD_TEST_CLASSIFY_CATEGORY_FROM_SET=1: return the first entry
//     of the request's context.categories (REQ-FILT-210) rather than a
//     hardcoded name -- for tests that don't want to hardcode the
//     principal's seeded category set.
//   - HEROLD_TEST_CLASSIFY_SLEEP_MS: sleeps this long, honouring ctx
//     cancellation, before responding -- exercises the server's classify
//     budget cutoff (Wave 4.4, REQ-FILT-40/42).
//   - HEROLD_TEST_CLASSIFY_TEMPERATURE: pins the declared manifest
//     temperature; unset leaves it nil (undeclared, refused by
//     Manifest.Validate) -- mirrors spamfixture's knob for the same test
//     shape re-used against the classifier type.
//   - HEROLD_TEST_CLASSIFY_PLUGIN_TYPE: "classifier" (default) or
//     "spam" -- the manifest type this process declares at handshake.
//     "spam" exercises issue #304 Decision 3's one-release compatibility
//     path: the plugin answers spam.classify (verdict only, no
//     category), the legacy TypeSpam contract, regardless of what the
//     operator's system.toml [[plugin]] block says.
//   - HEROLD_TEST_CLASSIFY_CALL_LOG: when set, names a file this process
//     appends one line to on every classify call (spam.classify or
//     mail.classify) -- lets a test count real subprocess invocations by
//     counting lines, since the test and the plugin are different
//     processes and cannot share an in-memory counter.
package main

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

type handler struct {
	mu sync.Mutex
}

func (h *handler) OnConfigure(context.Context, map[string]any) error { return nil }
func (h *handler) OnHealth(context.Context) error                    { return nil }
func (h *handler) OnShutdown(context.Context) error                  { return nil }

func envFloat(name string, def float64) float64 {
	if s := os.Getenv(name); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return def
}

// recordCall appends one line to HEROLD_TEST_CLASSIFY_CALL_LOG, when
// set, so a test in a different process can count real classify
// invocations. Serialised with a mutex since concurrent RPC calls are
// possible (max_concurrent_requests > 1) and O_APPEND alone does not
// guarantee atomicity of the write(2) call across goroutines within
// this process.
func (h *handler) recordCall() {
	path := os.Getenv("HEROLD_TEST_CLASSIFY_CALL_LOG")
	if path == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("1\n")
}

// sleepIfScripted honours HEROLD_TEST_CLASSIFY_SLEEP_MS, returning
// ctx.Err() (non-nil) when ctx is cancelled before the sleep elapses.
func sleepIfScripted(ctx context.Context) error {
	ms := os.Getenv("HEROLD_TEST_CLASSIFY_SLEEP_MS")
	if ms == "" {
		return nil
	}
	n, err := strconv.Atoi(ms)
	if err != nil || n <= 0 {
		return nil
	}
	select {
	case <-time.After(time.Duration(n) * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func scriptedVerdict() string {
	verdict := os.Getenv("HEROLD_TEST_CLASSIFY_VERDICT")
	if verdict == "" {
		verdict = "ham"
	}
	return verdict
}

func (h *handler) MailClassify(ctx context.Context, in sdk.MailClassifyParams) (sdk.MailClassifyResult, error) {
	h.recordCall()
	if err := sleepIfScripted(ctx); err != nil {
		return sdk.MailClassifyResult{}, err
	}
	category := os.Getenv("HEROLD_TEST_CLASSIFY_CATEGORY")
	if category == "" && os.Getenv("HEROLD_TEST_CLASSIFY_CATEGORY_FROM_SET") == "1" && len(in.Context.Categories) > 0 {
		category = in.Context.Categories[0].Name
	}
	return sdk.MailClassifyResult{
		Verdict:    scriptedVerdict(),
		Confidence: envFloat("HEROLD_TEST_CLASSIFY_SCORE", 0.1),
		Reason:     os.Getenv("HEROLD_TEST_CLASSIFY_REASON"),
		Category:   category,
	}, nil
}

// SpamClassify implements the legacy spam.classify wire contract
// (issue #304 Decision 3). SpamClassifyResult carries no category field
// -- there is nothing to drop, the wire shape itself never had one.
func (h *handler) SpamClassify(ctx context.Context, in sdk.SpamClassifyParams) (sdk.SpamClassifyResult, error) {
	h.recordCall()
	if err := sleepIfScripted(ctx); err != nil {
		return sdk.SpamClassifyResult{}, err
	}
	return sdk.SpamClassifyResult{
		Verdict:    scriptedVerdict(),
		Confidence: envFloat("HEROLD_TEST_CLASSIFY_SCORE", 0.1),
		Reason:     os.Getenv("HEROLD_TEST_CLASSIFY_REASON"),
	}, nil
}

// SpamHealth implements the second half of sdk.SpamHandler; classifierfixture
// is always healthy once running, matching MailClassify/OnHealth.
func (h *handler) SpamHealth(context.Context) (sdk.SpamHealthResult, error) {
	return sdk.SpamHealthResult{OK: true}, nil
}

func main() {
	pluginType := plug.TypeClassifier
	if os.Getenv("HEROLD_TEST_CLASSIFY_PLUGIN_TYPE") == "spam" {
		pluginType = plug.TypeSpam
	}
	manifest := sdk.Manifest{
		Name:       "classifierfixture",
		Version:    "0.0.1",
		Type:       pluginType,
		Lifecycle:  plug.LifecycleLongRunning,
		ABIVersion: plug.ABIVersion,
	}
	// HEROLD_TEST_CLASSIFY_TEMPERATURE: "unset" leaves Manifest.Temperature
	// nil (undeclared, refused by Manifest.Validate -- mirrors
	// spamfixture's default knob for manifest-validation tests); any other
	// parseable float pins it to that value; the unset env var (the
	// common case: most tests just want a working plugin) defaults to 0.
	switch s := os.Getenv("HEROLD_TEST_CLASSIFY_TEMPERATURE"); s {
	case "unset":
		// leave Temperature nil
	case "":
		z := 0.0
		manifest.Temperature = &z
	default:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			os.Exit(2)
		}
		manifest.Temperature = &v
	}
	if err := sdk.Run(manifest, &handler{}); err != nil {
		os.Exit(1)
	}
}
