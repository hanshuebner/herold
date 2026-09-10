// Command classifierfixture is a deterministic classifier-type plugin
// binary used only by internal/plugin and internal/admin test suites to
// exercise the mail.classify contract (Wave 4.3, issue #304) end-to-end,
// as a real child process -- no mocks at the process boundary
// (STANDARDS section 8). It is not a first-party plugin
// (docs/design/server/implementation/08-classifier-plugin.md names
// herold-spam-llm as that); it lives under testdata so `go build ./...`
// and `go vet ./...` skip it by the standard testdata convention, and
// the tests that use it build it explicitly by import path (mirrors
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
//     HEROLD_TEST_CLASSIFY_CATEGORY_FROM_SET.
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
package main

import (
	"context"
	"os"
	"strconv"
	"time"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

type handler struct{}

func (handler) OnConfigure(context.Context, map[string]any) error { return nil }
func (handler) OnHealth(context.Context) error                    { return nil }
func (handler) OnShutdown(context.Context) error                  { return nil }

func envFloat(name string, def float64) float64 {
	if s := os.Getenv(name); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return def
}

func (handler) MailClassify(ctx context.Context, in sdk.MailClassifyParams) (sdk.MailClassifyResult, error) {
	if ms := os.Getenv("HEROLD_TEST_CLASSIFY_SLEEP_MS"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			select {
			case <-time.After(time.Duration(n) * time.Millisecond):
			case <-ctx.Done():
				return sdk.MailClassifyResult{}, ctx.Err()
			}
		}
	}
	verdict := os.Getenv("HEROLD_TEST_CLASSIFY_VERDICT")
	if verdict == "" {
		verdict = "ham"
	}
	category := os.Getenv("HEROLD_TEST_CLASSIFY_CATEGORY")
	if category == "" && os.Getenv("HEROLD_TEST_CLASSIFY_CATEGORY_FROM_SET") == "1" && len(in.Context.Categories) > 0 {
		category = in.Context.Categories[0].Name
	}
	return sdk.MailClassifyResult{
		Verdict:    verdict,
		Confidence: envFloat("HEROLD_TEST_CLASSIFY_SCORE", 0.1),
		Reason:     os.Getenv("HEROLD_TEST_CLASSIFY_REASON"),
		Category:   category,
	}, nil
}

func main() {
	manifest := sdk.Manifest{
		Name:       "classifierfixture",
		Version:    "0.0.1",
		Type:       plug.TypeClassifier,
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
	if err := sdk.Run(manifest, handler{}); err != nil {
		os.Exit(1)
	}
}
