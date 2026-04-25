package categorise

import (
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// DefaultPrompt is the seeded system prompt (REQ-FILT-211). The store
// seeds this string when a principal first reads their categorisation
// config; this constant is exported so admin tooling and tests can
// reset to default without re-deriving the canonical text.
const DefaultPrompt = `You are an email-categorisation assistant. Given an email envelope and a short body excerpt, choose exactly one category from the supplied list whose description best fits the message, or return "none" if no category is a clear match. Respond ONLY with a single JSON object of the form {"category":"<name>"} where <name> is one of the listed category names or the literal "none". Do not include any other text.`

// DefaultCategorySet is the seeded per-account category set
// (REQ-FILT-201/210). The order is preserved when surfaced to clients
// (admin REST + tabard) so a "reset to default" toggle stays stable.
var DefaultCategorySet = []store.CategoryDef{
	{Name: "primary", Description: "Personal correspondence and important messages from people you know."},
	{Name: "social", Description: "Messages from social networks and dating sites."},
	{Name: "promotions", Description: "Marketing emails, offers, deals, newsletters."},
	{Name: "updates", Description: "Receipts, confirmations, statements, account notices."},
	{Name: "forums", Description: "Mailing-list digests, online community discussions."},
}

// renderSystemPrompt joins the configured prompt with a serialised
// description of the category set. The shape mirrors what the LLM
// needs to follow the instruction: prompt body first, then a bullet
// list of "<name>: <description>" lines, one per category.
func renderSystemPrompt(basePrompt string, set []store.CategoryDef) string {
	var b strings.Builder
	b.Grow(len(basePrompt) + 64*len(set))
	b.WriteString(strings.TrimRight(basePrompt, " \n\t"))
	b.WriteString("\n\nAvailable categories:\n")
	for _, c := range set {
		fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Description)
	}
	b.WriteString("- none: no listed category fits the message.\n")
	return b.String()
}
