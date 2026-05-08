package gmail

import "strings"

// Decision is the importer-internal verdict about where a Gmail message
// goes inside the herold store. One Decision is computed per message
// from the X-Gmail-Labels token list using the locale picked by
// DetectLocale; the importer then materialises Decision into store
// mutations (mailbox membership, keyword set, category keyword).
type Decision struct {
	// Roles is the set of mailbox role-bits the message should be filed
	// under, expressed as SystemLabel constants from the role family
	// (Inbox, Sent, Drafts, Spam, Trash). A message with no role lands
	// in the archive (no role mailbox, only user labels).
	//
	// Note: Inbox, Sent, Drafts, Spam, Trash are mutually exclusive in
	// Gmail except in pathological cases where a message ends up in
	// both Spam and Inbox simultaneously. The mapper preserves what
	// Gmail wrote rather than imposing exclusivity; the importer's
	// store layer files the message into one mailbox per Role and the
	// JMAP / IMAP views render the union.
	Roles []SystemLabel

	// Keywords is the set of canonical IMAP keyword names to attach
	// (e.g. "\\Flagged", "\\Seen", "\\Draft", "\\Important",
	// "$category-updates"). Stable strings; the store layer takes them
	// verbatim.
	Keywords []string

	// Seen is the explicit \Seen flag derivation from the Unread /
	// Opened pseudo-labels: true means "set \Seen", false means "do
	// not". The importer applies this independent of Keywords because
	// store.MessageMailbox.Flags carries \Seen as a flag bit, not a
	// keyword.
	Seen bool

	// UserLabels is the list of Gmail user-defined labels that did not
	// resolve to a SystemLabel. The importer creates one Mailbox of
	// role Label per unique name. Slashes in the name preserve
	// hierarchy ("Travel/2024/Italy") via Mailbox.parentId.
	UserLabels []string

	// Category is the matched $category-* keyword, when the labels
	// included one of the five Gmail categories. Empty string means
	// "no category" — equivalent to Gmail's Primary inbox-tab default
	// for some accounts. The keyword name is lowercase ASCII per
	// REQ-FILT-201.
	Category string
}

// Map turns one parsed token list into a Decision under the given
// locale. Locale is the result of DetectLocale across the whole stream;
// callers should not call Map with a fresh per-message detection.
func Map(tokens []string, locale Locale) Decision {
	d := Decision{Seen: true} // default: read, unless Unread label says otherwise
	hasUnread := false
	hasOpened := false

	addKw := func(kw string) {
		for _, existing := range d.Keywords {
			if existing == kw {
				return
			}
		}
		d.Keywords = append(d.Keywords, kw)
	}
	addRole := func(r SystemLabel) {
		for _, existing := range d.Roles {
			if existing == r {
				return
			}
		}
		d.Roles = append(d.Roles, r)
	}

	for _, raw := range tokens {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		// Category prefix? Resolves to a $category-* keyword.
		if tail, _, ok := AnyCategoryPrefix(tok); ok {
			cat, ok := categoryKeyword(tail, locale)
			if ok {
				d.Category = cat
				addKw(cat)
			} else {
				// Unrecognised category name — pass through as a user
				// label so it is not silently lost. This is rare; it
				// happens when a user's Gmail UI is in a locale we
				// don't yet ship a category table for, or when the
				// category name was renamed by Google.
				d.UserLabels = appendUnique(d.UserLabels, tok)
			}
			continue
		}
		// System label in the chosen locale?
		if sym, ok := Lookup(tok, locale); ok {
			classifySymbol(sym, &d, addRole, addKw, &hasUnread, &hasOpened)
			continue
		}
		// Fallback: try every locale. Useful when a takeout was made
		// while the user's UI language differed from the dominant
		// locale (extremely rare; defensive).
		if sym, _, ok := LookupAny(tok); ok {
			classifySymbol(sym, &d, addRole, addKw, &hasUnread, &hasOpened)
			continue
		}
		// User-defined label — passthrough.
		d.UserLabels = appendUnique(d.UserLabels, tok)
	}

	// Unread wins over Opened: if both are present (Gmail sometimes
	// includes both for messages never opened in any client), the
	// explicit Unread token marks the message unseen.
	switch {
	case hasUnread:
		d.Seen = false
	case hasOpened:
		d.Seen = true
	}
	return d
}

// classifySymbol updates a Decision in place for one matched system
// label. Split out so the locale-specific path and the cross-locale
// fallback share the same decision logic.
func classifySymbol(
	sym SystemLabel,
	d *Decision,
	addRole func(SystemLabel),
	addKw func(string),
	hasUnread, hasOpened *bool,
) {
	switch sym {
	case SystemLabelInbox:
		addRole(sym)
	case SystemLabelSent:
		addRole(sym)
		addKw(`\Sent`)
	case SystemLabelDrafts:
		addRole(sym)
		addKw(`\Draft`)
	case SystemLabelSpam:
		addRole(sym)
		addKw(`\Junk`)
	case SystemLabelTrash:
		addRole(sym)
	case SystemLabelStarred:
		addKw(`\Flagged`)
	case SystemLabelImportant:
		addKw(`\Important`)
	case SystemLabelUnread:
		*hasUnread = true
	case SystemLabelOpened:
		*hasOpened = true
	case SystemLabelChats:
		// Gmail's Chats history is not currently mappable onto herold;
		// drop the role but record so the importer can summarise.
		// Treated as user-defined "Chats" mailbox so messages are not
		// lost — the operator can clean up later.
		d.UserLabels = appendUnique(d.UserLabels, "Chats")
	}
}

// categoryKeyword maps a localized category name (the inner string of
// a `Kategorie: "..."` token, with quotes already stripped) to its
// canonical $category-* keyword. Returns ("", false) when the name
// does not match any of the five default categories in the locale.
func categoryKeyword(name string, locale Locale) (string, bool) {
	if name == "" {
		return "", false
	}
	if sym, ok := Lookup(name, locale); ok && sym.IsCategory() {
		return categoryKeywordFor(sym), true
	}
	if sym, _, ok := LookupAny(name); ok && sym.IsCategory() {
		return categoryKeywordFor(sym), true
	}
	return "", false
}

// categoryKeywordFor maps a SystemLabel category constant onto its
// $category-* keyword name (REQ-FILT-201).
func categoryKeywordFor(sym SystemLabel) string {
	switch sym {
	case SystemLabelCategoryPrimary:
		return "$category-primary"
	case SystemLabelCategorySocial:
		return "$category-social"
	case SystemLabelCategoryPromotions:
		return "$category-promotions"
	case SystemLabelCategoryUpdates:
		return "$category-updates"
	case SystemLabelCategoryForums:
		return "$category-forums"
	}
	return ""
}

// appendUnique appends s to slice if it is not already present (case-
// sensitive compare, since Gmail user labels are case-preserving).
func appendUnique(slice []string, s string) []string {
	for _, existing := range slice {
		if existing == s {
			return slice
		}
	}
	return append(slice, s)
}
