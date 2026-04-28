package llmtransparency

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// singletonID is the only id an LLMTransparency object carries. One row per
// account; not user-mutable.
const singletonID = "singleton"

// disclosureNote is the server-side text the suite renders verbatim in the
// transparency UI (REQ-CAT-45 / REQ-WEB-12 / web/00-scope.md "content-blind
// on the wire"). It accurately describes what herold sends without revealing
// guardrails.
const disclosureNote = "This is the prompt used to categorise your mail. Your messages are sent to herold's configured classifier endpoint along with this prompt."

// jmapModelInfo is the wire shape of a model reference in transparency objects.
type jmapModelInfo struct {
	Endpoint  string `json:"endpoint,omitempty"`
	ModelName string `json:"modelName,omitempty"`
}

// jmapLLMTransparency is the wire-form LLMTransparency object returned by
// LLMTransparency/get (one singleton per account).
type jmapLLMTransparency struct {
	// ID is always "singleton".
	ID string `json:"id"`
	// SpamPrompt is the user-visible portion of the spam classifier system
	// prompt currently in effect (REQ-FILT-65). Operator guardrails excluded.
	SpamPrompt string `json:"spamPrompt"`
	// SpamModel describes the spam classifier endpoint and model name.
	// Auth headers are excluded (REQ-FILT-67).
	SpamModel jmapModelInfo `json:"spamModel"`
	// CategoriserPrompt is the user-visible system prompt for categorisation
	// (REQ-FILT-211 / REQ-FILT-216). Operator guardrails excluded.
	CategoriserPrompt string `json:"categoriserPrompt"`
	// DerivedCategories is the server-derived list of category names from the
	// most recent successful classifier response (REQ-FILT-216/217). Nil/empty
	// when no successful classifier call has occurred since the last prompt
	// change. Read-only; the prompt is the lever.
	DerivedCategories []string `json:"derivedCategories"`
	// CategoriserModel describes the categoriser endpoint and model name.
	CategoriserModel jmapModelInfo `json:"categoriserModel"`
	// DisclosureNote is a short server-side sentence the suite renders
	// verbatim so the client stays content-blind on the wire (REQ-WEB-12).
	DisclosureNote string `json:"disclosureNote"`
}

// handlerSet bundles dependencies for all LLMTransparency handlers.
type handlerSet struct {
	store        store.Store
	spamPolicy   protoadmin.SpamPolicyStore
	// categoriserEndpoint and categoriserModel are the operator-default values
	// (from system config / Options). Per-account overrides are resolved from
	// the store.CategorisationConfig row.
	categoriserEndpoint string
	categoriserModel    string
}

// AccountCapability satisfies protojmap.AccountCapabilityProvider.
func (h *handlerSet) AccountCapability() any {
	return struct{}{} // no per-account fields needed at the session level
}

// -- LLMTransparency/get -------------------------------------------

type getRequest struct {
	AccountID string    `json:"accountId"`
	IDs       *[]string `json:"ids"`
}

type getResponse struct {
	AccountID string                `json:"accountId"`
	State     string                `json:"state"`
	List      []jmapLLMTransparency `json:"list"`
	NotFound  []string              `json:"notFound"`
}

// getHandler implements LLMTransparency/get.
type getHandler struct{ h *handlerSet }

func (g *getHandler) Method() string { return "LLMTransparency/get" }

func (g *getHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	p, ok := principalFrom(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	var req getRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	if merr := validateAccountID(p, req.AccountID); merr != nil {
		return nil, merr
	}

	// Spam prompt: user-visible portion from SpamPolicy.SystemPromptOverride.
	// No guardrail returned (REQ-FILT-67).
	spamPrompt := ""
	spamModel := jmapModelInfo{}
	if g.h.spamPolicy != nil {
		pol := g.h.spamPolicy.GetSpamPolicy()
		spamPrompt = pol.SystemPromptOverride
		// Guardrail (pol.Guardrail) is intentionally NOT included.
		spamModel = jmapModelInfo{
			ModelName: pol.Model,
			// Endpoint is not stored in SpamPolicy; the plugin owns its own
			// endpoint configuration. We omit it here rather than expose
			// the plugin name (plugin names are operator-internal).
		}
	}

	// Categorisation prompt: user-visible portion from CategorisationConfig.
	catPrompt := ""
	catModel := jmapModelInfo{
		Endpoint:  g.h.categoriserEndpoint,
		ModelName: g.h.categoriserModel,
	}
	cfg, err := g.h.store.Meta().GetCategorisationConfig(ctx, p.ID)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}
	catPrompt = cfg.Prompt // Guardrail (cfg.Guardrail) is NOT included.
	derivedCategories := cfg.DerivedCategories
	if derivedCategories == nil {
		derivedCategories = []string{}
	}
	// Per-account model/endpoint overrides from the config row.
	if cfg.Model != nil && *cfg.Model != "" {
		catModel.ModelName = *cfg.Model
	}
	if cfg.Endpoint != nil && *cfg.Endpoint != "" {
		catModel.Endpoint = *cfg.Endpoint
	}

	obj := jmapLLMTransparency{
		ID:                singletonID,
		SpamPrompt:        spamPrompt,
		SpamModel:         spamModel,
		CategoriserPrompt: catPrompt,
		DerivedCategories: derivedCategories,
		CategoriserModel:  catModel,
		DisclosureNote:    disclosureNote,
	}

	// State is derived from the categorisation config's UpdatedAtUs so that
	// clients can detect when the prompt changed. We use it as an opaque
	// string cursor per RFC 8620 §4.6.
	state := strconv.FormatInt(cfg.UpdatedAtUs, 10)

	resp := getResponse{
		AccountID: protojmap.AccountIDForPrincipal(p.ID),
		State:     state,
		List:      []jmapLLMTransparency{},
		NotFound:  []string{},
	}
	if req.IDs == nil {
		resp.List = append(resp.List, obj)
		return resp, nil
	}
	for _, id := range *req.IDs {
		if id == singletonID {
			resp.List = append(resp.List, obj)
		} else {
			resp.NotFound = append(resp.NotFound, id)
		}
	}
	return resp, nil
}

// -- Email/llmInspect -----------------------------------------------

// llmInspectRequest is the inbound shape for Email/llmInspect.
type llmInspectRequest struct {
	AccountID string   `json:"accountId"`
	IDs       []string `json:"ids"`
}

// jmapSpamDetail is the spam sub-record in an llmInspect result entry.
type jmapSpamDetail struct {
	// Verdict is the classifier's verdict string.
	Verdict string `json:"verdict"`
	// Confidence is the [0,1] confidence returned by the classifier.
	Confidence float64 `json:"confidence"`
	// Reason is the short reason text the classifier returned.
	Reason string `json:"reason,omitempty"`
	// PromptApplied is the user-visible prompt as built for this message
	// (REQ-FILT-66). Guardrails excluded; body excerpt not re-exposed.
	PromptApplied string `json:"promptApplied"`
	// Model is the model identifier used.
	Model string `json:"model,omitempty"`
	// ClassifiedAt is the ISO 8601 instant classification ran.
	ClassifiedAt string `json:"classifiedAt,omitempty"`
}

// jmapCategoryDetail is the categorisation sub-record in an llmInspect
// result entry.
type jmapCategoryDetail struct {
	// Assigned is the assigned category name (no "$category-" prefix).
	Assigned string `json:"assigned"`
	// PromptApplied is the user-visible prompt as applied to this message
	// (REQ-FILT-216 / REQ-FILT-66). Guardrails excluded.
	PromptApplied string `json:"promptApplied"`
	// Model is the model identifier used.
	Model string `json:"model,omitempty"`
	// ClassifiedAt is the ISO 8601 instant categorisation ran.
	ClassifiedAt string `json:"classifiedAt,omitempty"`
}

// jmapLLMInspectEntry is one result entry in an Email/llmInspect response.
type jmapLLMInspectEntry struct {
	// ID is the JMAP Email id.
	ID string `json:"id"`
	// Spam is present when the spam classifier ran on this message.
	Spam *jmapSpamDetail `json:"spam,omitempty"`
	// Category is present when the categoriser ran on this message.
	Category *jmapCategoryDetail `json:"category,omitempty"`
}

// llmInspectResponse is the outbound shape for Email/llmInspect.
type llmInspectResponse struct {
	AccountID string                `json:"accountId"`
	List      []jmapLLMInspectEntry `json:"list"`
}

// llmInspectHandler implements Email/llmInspect.
type llmInspectHandler struct{ h *handlerSet }

func (i *llmInspectHandler) Method() string { return "Email/llmInspect" }

func (i *llmInspectHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	p, ok := principalFrom(ctx)
	if !ok {
		return nil, protojmap.NewMethodError("forbidden", "no authenticated principal")
	}
	var req llmInspectRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, protojmap.NewMethodError("invalidArguments", err.Error())
	}
	if merr := validateAccountID(p, req.AccountID); merr != nil {
		return nil, merr
	}
	if len(req.IDs) == 0 {
		return llmInspectResponse{
			AccountID: protojmap.AccountIDForPrincipal(p.ID),
			List:      []jmapLLMInspectEntry{},
		}, nil
	}
	// Cap the request size to prevent unbounded batch reads.
	const maxIDs = 1000
	if len(req.IDs) > maxIDs {
		return nil, protojmap.NewMethodError("requestTooLarge",
			"ids must not exceed 1000 entries per call")
	}

	// Convert JMAP Email IDs (strings) to store.MessageID (uint64).
	msgIDs := make([]store.MessageID, 0, len(req.IDs))
	idToJMAP := make(map[store.MessageID]string, len(req.IDs))
	for _, jid := range req.IDs {
		n, err := parseMessageID(jid)
		if err != nil {
			// Unknown / unparseable ID: skip; the response simply omits it.
			continue
		}
		msgIDs = append(msgIDs, n)
		idToJMAP[n] = jid
	}

	records, err := i.h.store.Meta().BatchGetLLMClassifications(ctx, msgIDs)
	if err != nil {
		return nil, protojmap.NewMethodError("serverFail", err.Error())
	}

	// Build result list in the same order the client sent the IDs.
	list := make([]jmapLLMInspectEntry, 0, len(req.IDs))
	for _, jid := range req.IDs {
		n, err := parseMessageID(jid)
		if err != nil {
			continue
		}
		rec, ok := records[n]
		if !ok {
			// No classification record for this message; omit from response
			// (REQ-FILT-66: "If a message wasn't classified…the corresponding
			// sub-object is omitted").
			continue
		}
		// Verify ownership: the record's principal must match the caller.
		if rec.PrincipalID != p.ID {
			// Access denied; silently skip (not a transparency leak).
			continue
		}
		entry := jmapLLMInspectEntry{ID: jid}
		if rec.SpamVerdict != nil {
			entry.Spam = &jmapSpamDetail{
				Verdict:       derefStr(rec.SpamVerdict),
				Confidence:    derefF64(rec.SpamConfidence),
				Reason:        derefStr(rec.SpamReason),
				PromptApplied: derefStr(rec.SpamPromptApplied),
				Model:         derefStr(rec.SpamModel),
				ClassifiedAt:  formatTime(rec.SpamClassifiedAt),
			}
		}
		if rec.CategoryPromptApplied != nil {
			entry.Category = &jmapCategoryDetail{
				Assigned:      derefStr(rec.CategoryAssigned),
				PromptApplied: derefStr(rec.CategoryPromptApplied),
				Model:         derefStr(rec.CategoryModel),
				ClassifiedAt:  formatTime(rec.CategoryClassifiedAt),
			}
		}
		if entry.Spam != nil || entry.Category != nil {
			list = append(list, entry)
		}
	}

	return llmInspectResponse{
		AccountID: protojmap.AccountIDForPrincipal(p.ID),
		List:      list,
	}, nil
}

// -- helpers ----------------------------------------------------------

// validateAccountID rejects a mismatched or absent accountId.
func validateAccountID(p store.Principal, accountID string) *protojmap.MethodError {
	if accountID == "" {
		return protojmap.NewMethodError("invalidArguments", "accountId is required")
	}
	if accountID != protojmap.AccountIDForPrincipal(p.ID) {
		return protojmap.NewMethodError("accountNotFound",
			"requested account is not accessible to the caller")
	}
	return nil
}

// parseMessageID converts a JMAP Email id string to a store.MessageID.
// JMAP Email IDs in herold are decimal string representations of uint64.
func parseMessageID(jid string) (store.MessageID, error) {
	n, err := strconv.ParseUint(jid, 10, 64)
	if err != nil {
		return 0, errors.New("invalid email id")
	}
	return store.MessageID(n), nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefF64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
