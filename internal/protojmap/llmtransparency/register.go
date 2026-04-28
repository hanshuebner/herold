package llmtransparency

import (
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// Register installs the LLMTransparency/* and Email/llmInspect method
// handlers under CapabilityLLMTransparency
// ("https://netzhansa.com/jmap/llm-transparency") and registers the
// per-server capability descriptor.
//
// spamPolicy may be nil when no spam plugin is configured; the
// LLMTransparency/get handler handles nil gracefully and returns empty
// spam fields.
//
// categoriserEndpoint and categoriserModel are the operator-default
// values from system config / Options. Per-account overrides are
// resolved from the store's CategorisationConfig row at request time.
func Register(
	reg *protojmap.CapabilityRegistry,
	st store.Store,
	spamPolicy protoadmin.SpamPolicyStore,
	categoriserEndpoint string,
	categoriserModel string,
) {
	h := &handlerSet{
		store:               st,
		spamPolicy:          spamPolicy,
		categoriserEndpoint: categoriserEndpoint,
		categoriserModel:    categoriserModel,
	}
	reg.Register(protojmap.CapabilityLLMTransparency, &getHandler{h: h})
	reg.Register(protojmap.CapabilityLLMTransparency, &llmInspectHandler{h: h})
	// Per-account capability descriptor is the empty object; no per-account
	// fields are needed at the session level for this capability.
	reg.RegisterAccountCapability(protojmap.CapabilityLLMTransparency, h)
	reg.RegisterCapabilityDescriptor(protojmap.CapabilityLLMTransparency, struct{}{})
}
