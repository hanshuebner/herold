// Package fakeclassify is a deterministic stand-in for a real classifier
// plugin (plugins/herold-spam-llm), for tests and development (re #364).
// It answers the mail.classify / spam.classify JSON-RPC contract
// (plugin type "classifier", internal/spam.MailClassifyMethod) from a
// fixed set of subject-substring rules instead of calling an LLM, so
// categorisation and the LLM transparency surface (Email/llmInspect,
// LLMTransparency/get) can be exercised end to end without Ollama or any
// other model endpoint.
//
// Rules, checked case-insensitively against the message subject, first
// match wins:
//
//   - "+spam"    -> verdict spam, confidence 0.97, no category (spam is a
//     verdict, not a member of the default category set).
//   - "+promo"   -> verdict ham, confidence 0.05, category "promotions".
//   - "+updates" -> verdict ham, confidence 0.05, category "updates".
//   - otherwise  -> verdict ham, confidence 0.05, category "primary".
//
// The returned Reason always names the matched rule, so a persisted
// LLMClassificationRecord (internal/store) and the rendered
// Email/llmInspect entry carry real, inspectable content instead of an
// empty string.
//
// The binary that wires this handler into a real out-of-process plugin is
// cmd/heroldfakeclassify; scripts/dev-instance.sh builds and configures it
// by default as a [[plugin]] block of type "classifier".
package fakeclassify

import (
	"context"
	"strings"

	plug "github.com/hanshuebner/herold/internal/plugin"
	"github.com/hanshuebner/herold/plugins/sdk"
)

// Verdict and category names, exported so tests can assert against the
// same constants the handler returns.
const (
	VerdictSpam = "spam"
	VerdictHam  = "ham"

	CategoryPromotions = "promotions"
	CategoryUpdates    = "updates"
	CategoryPrimary    = "primary"
)

// SpamConfidence and HamConfidence are the fixed scores every rule below
// returns. Deterministic: no randomness, no model call.
const (
	SpamConfidence = 0.97
	HamConfidence  = 0.05
)

// Result is the deterministic outcome of applying the rules to one
// message's subject: the shared shape MailClassify and SpamClassify both
// derive their wire result from.
type Result struct {
	Verdict    string
	Confidence float64
	Reason     string
	// Category is empty for the spam rule -- "spam" is a verdict, not a
	// member of the principal's category set, so returning it as a
	// category would only be dropped by the server (REQ-FILT-230).
	Category string
}

// Classify applies the fixed subject-substring rules and returns the
// deterministic Result. Exported standalone so the rule table itself is
// unit-testable without spinning up a JSON-RPC child process.
func Classify(subject string) Result {
	s := strings.ToLower(subject)
	switch {
	case strings.Contains(s, "+spam"):
		return Result{
			Verdict:    VerdictSpam,
			Confidence: SpamConfidence,
			Reason:     `fakeclassify: subject contains "+spam"`,
		}
	case strings.Contains(s, "+promo"):
		return Result{
			Verdict:    VerdictHam,
			Confidence: HamConfidence,
			Reason:     `fakeclassify: subject contains "+promo"`,
			Category:   CategoryPromotions,
		}
	case strings.Contains(s, "+updates"):
		return Result{
			Verdict:    VerdictHam,
			Confidence: HamConfidence,
			Reason:     `fakeclassify: subject contains "+updates"`,
			Category:   CategoryUpdates,
		}
	default:
		return Result{
			Verdict:    VerdictHam,
			Confidence: HamConfidence,
			Reason:     "fakeclassify: no trigger word matched, defaulting to primary",
			Category:   CategoryPrimary,
		}
	}
}

// Handler implements sdk.Handler, sdk.ClassifierHandler, and
// sdk.SpamHandler over the Classify rule table. The zero value is ready
// to use; OnConfigure accepts an empty options map (Manifest.OptionsSchema
// is empty) and rejects anything else so a typo'd system.toml
// [[plugin]] block fails loud rather than being silently ignored.
type Handler struct{}

// NewHandler returns a ready-to-use Handler.
func NewHandler() *Handler { return &Handler{} }

// OnConfigure rejects any option: this plugin takes none.
func (h *Handler) OnConfigure(ctx context.Context, opts map[string]any) error {
	if len(opts) > 0 {
		keys := make([]string, 0, len(opts))
		for k := range opts {
			keys = append(keys, k)
		}
		return &unknownOptionsError{keys: keys}
	}
	return nil
}

// OnHealth always reports healthy: there is no upstream endpoint to
// probe.
func (h *Handler) OnHealth(ctx context.Context) error { return nil }

// OnShutdown has nothing to drain; every call is synchronous and
// in-memory.
func (h *Handler) OnShutdown(ctx context.Context) error { return nil }

// MailClassify implements sdk.ClassifierHandler (mail.classify): one call
// answers both the spam verdict and the category, from Classify(subject)
// alone. Sender, headers, and body are intentionally ignored -- the rule
// table matches the issue's acceptance criterion exactly ("a mail with
// +promo in the subject").
func (h *Handler) MailClassify(ctx context.Context, in sdk.MailClassifyParams) (sdk.MailClassifyResult, error) {
	r := Classify(in.Subject)
	return sdk.MailClassifyResult{
		Verdict:    r.Verdict,
		Confidence: r.Confidence,
		Reason:     r.Reason,
		Category:   r.Category,
	}, nil
}

// SpamClassify implements sdk.SpamHandler (legacy spam.classify, issue
// #304 Decision 3). The wire shape carries no category field, so the
// Category half of Result is simply dropped.
func (h *Handler) SpamClassify(ctx context.Context, in sdk.SpamClassifyParams) (sdk.SpamClassifyResult, error) {
	r := Classify(in.Subject)
	return sdk.SpamClassifyResult{
		Verdict:    r.Verdict,
		Confidence: r.Confidence,
		Reason:     r.Reason,
	}, nil
}

// SpamHealth mirrors OnHealth on the SpamHandler surface.
func (h *Handler) SpamHealth(ctx context.Context) (sdk.SpamHealthResult, error) {
	return sdk.SpamHealthResult{OK: true}, nil
}

// unknownOptionsError reports the exact option keys OnConfigure rejected,
// matching the "unknown keys fail loud" posture every first-party plugin
// follows (herold-spam-llm's knownOptions check).
type unknownOptionsError struct{ keys []string }

func (e *unknownOptionsError) Error() string {
	return "fakeclassify: unknown option(s): " + strings.Join(e.keys, ", ")
}

// Manifest returns the sdk.Manifest this plugin advertises at handshake:
// type "classifier" (Wave 4.3, issue #304), temperature pinned to 0
// (REQ-FILT-12 -- every spam/classifier-typed plugin must declare this),
// and no options.
func Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name:              "herold-fakeclassify",
		Version:           "0.1.0",
		Type:              plug.TypeClassifier,
		Lifecycle:         plug.LifecycleLongRunning,
		ABIVersion:        plug.ABIVersion,
		ShutdownGraceSec:  5,
		HealthIntervalSec: 30,
		Temperature:       sdk.PinnedTemperature(),
		OptionsSchema:     map[string]plug.OptionSchema{},
	}
}
