package spam

import (
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
)

// StructuralCategory is the server's own small fallback categoriser
// (ADR-0002 "The one thing the server still categorises by itself",
// REQ-FILT-214). It runs only where a classifier plugin's category is
// empty -- because it returned none, or because no plugin is installed
// at all. The classifier plugin's category always wins over this; the
// caller only invokes StructuralCategory when the plugin gave nothing.
//
// It is deliberately dumb: three structural headers, first match wins,
// no configuration, no LLM. Categorisation is not adversarial the way
// spam is -- nobody forges a List-Id to look more like a mailing list --
// which is what makes even this much safe to run unconditionally.
func StructuralCategory(msg mailparse.Message) string {
	if msg.Headers.Get("List-Id") != "" || msg.Headers.Get("List-Unsubscribe") != "" {
		return "forums"
	}
	if strings.EqualFold(strings.TrimSpace(msg.Headers.Get("Precedence")), "bulk") {
		return "promotions"
	}
	if msg.Headers.Get("Auto-Submitted") != "" {
		return "updates"
	}
	return ""
}
