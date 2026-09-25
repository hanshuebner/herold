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
// It is deliberately dumb: a handful of structural headers, first match
// wins, no configuration, no LLM. Categorisation is not adversarial the
// way spam is -- nobody forges a List-Id to look more like a mailing
// list -- which is what makes even this much safe to run
// unconditionally.
//
// "forums" is reserved for mail that shows list discussion traffic: a
// List-Post header, a Precedence: list header, or a List-Id paired with
// List-Post. A List-Id or List-Unsubscribe on its own marks one-way bulk
// mail -- a newsletter or a marketing mail, not a discussion list -- and
// falls to "updates", or "promotions" when Precedence: bulk is also
// present (re #491: an association newsletter with only List-Unsubscribe
// and List-Id landed in Forums on every classify failure).
func StructuralCategory(msg mailparse.Message) string {
	precedence := strings.EqualFold(strings.TrimSpace(msg.Headers.Get("Precedence")), "list")
	listPost := msg.Headers.Get("List-Post") != ""
	if listPost || precedence {
		return "forums"
	}
	bulk := strings.EqualFold(strings.TrimSpace(msg.Headers.Get("Precedence")), "bulk")
	if msg.Headers.Get("List-Id") != "" || msg.Headers.Get("List-Unsubscribe") != "" {
		if bulk {
			return "promotions"
		}
		return "updates"
	}
	if bulk {
		return "promotions"
	}
	if msg.Headers.Get("Auto-Submitted") != "" {
		return "updates"
	}
	return ""
}
