package categorise

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// NormalizeSender returns the canonical form of a sender-rule address:
// trimmed and lower-cased.
func NormalizeSender(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// SenderRuleCategory returns the category of the first rule whose Sender
// equals one of the From addresses (case-insensitive), or "" when no rule
// matches. Rule senders are compared in normalised form, so a rule stored
// by an older writer with mixed case still matches.
func SenderRuleCategory(rules []store.SenderCategoryRule, from []mail.Address) string {
	if len(rules) == 0 {
		return ""
	}
	for _, a := range from {
		addr := NormalizeSender(a.Address)
		if addr == "" {
			continue
		}
		for _, r := range rules {
			if NormalizeSender(r.Sender) == addr {
				return r.Category
			}
		}
	}
	return ""
}

// FromAddresses parses a raw From header value (the form stored in the
// message envelope cache) into lower-cased bare addresses. An
// unparseable value yields nil.
func FromAddresses(raw string) []string {
	list, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		if addr := NormalizeSender(a.Address); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

// AllowedCategories returns the account's category set: the names the
// classifier last derived plus the configured category vocabulary, in
// that order, de-duplicated.
func AllowedCategories(cfg store.CategorisationConfig) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range cfg.DerivedCategories {
		add(n)
	}
	for _, c := range cfg.CategorySet {
		add(c.Name)
	}
	return out
}

// recategoriseSenderPage is the per-page message count RecategoriseSender
// asks the store for.
const recategoriseSenderPage = 500

// RecategoriseSender applies category to every stored message of
// principal whose From address equals sender, through the same keyword
// path as CategorySettings/recategorise (ApplyCategoryKeyword): each
// membership gains $category-<category> and loses any other $category-*
// keyword, and the store's change feed notifies clients. Memberships in
// Trash and Junk are left alone. Returns the number of memberships
// visited (memberships already carrying exactly the category are not rewritten).
func RecategoriseSender(ctx context.Context, st store.Store, principal store.PrincipalID, sender, category string) (int, error) {
	sender = NormalizeSender(sender)
	if sender == "" || category == "" {
		return 0, nil
	}
	changed := 0
	var after store.MessageID
	for {
		rows, err := st.Meta().ListMessagesFromCandidates(ctx, principal, sender, after, recategoriseSenderPage)
		if err != nil {
			return changed, fmt.Errorf("list candidates: %w", err)
		}
		if len(rows) == 0 {
			return changed, nil
		}
		for _, m := range rows {
			if m.ID > after {
				after = m.ID
			}
			if !fromMatches(m.Envelope.From, sender) {
				continue
			}
			if err := ApplyCategoryKeyword(ctx, st, m, category); err != nil {
				return changed, fmt.Errorf("apply keyword to message %d: %w", m.ID, err)
			}
			changed++
		}
	}
}

func fromMatches(rawFrom, sender string) bool {
	for _, a := range FromAddresses(rawFrom) {
		if a == sender {
			return true
		}
	}
	return false
}
