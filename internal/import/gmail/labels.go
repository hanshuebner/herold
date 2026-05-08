package gmail

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// SystemLabel enumerates the Gmail system labels herold knows how to
// translate. The set is closed: any X-Gmail-Labels token that does not
// match a SystemLabel is treated as a user-defined label and passes
// through verbatim as a herold Mailbox of role Label.
//
// Categories (Primary / Social / Promotions / Updates / Forums) map
// 1:1 onto the $category-* keywords from REQ-FILT-201.
type SystemLabel int

const (
	// SystemLabelUnknown is the zero value; returned by Lookup when no
	// match is found in the requested locale.
	SystemLabelUnknown SystemLabel = iota
	SystemLabelInbox
	SystemLabelSent
	SystemLabelDrafts
	SystemLabelSpam
	SystemLabelTrash
	SystemLabelStarred
	SystemLabelImportant
	SystemLabelUnread
	SystemLabelOpened // Gmail's "read" pseudo-label
	SystemLabelChats
	SystemLabelCategoryPrimary
	SystemLabelCategorySocial
	SystemLabelCategoryPromotions
	SystemLabelCategoryUpdates
	SystemLabelCategoryForums
)

// String returns a stable canonical name for a SystemLabel, suitable
// for diagnostics. Not localized; not user-facing.
func (s SystemLabel) String() string {
	switch s {
	case SystemLabelInbox:
		return "Inbox"
	case SystemLabelSent:
		return "Sent"
	case SystemLabelDrafts:
		return "Drafts"
	case SystemLabelSpam:
		return "Spam"
	case SystemLabelTrash:
		return "Trash"
	case SystemLabelStarred:
		return "Starred"
	case SystemLabelImportant:
		return "Important"
	case SystemLabelUnread:
		return "Unread"
	case SystemLabelOpened:
		return "Opened"
	case SystemLabelChats:
		return "Chats"
	case SystemLabelCategoryPrimary:
		return "Category:Primary"
	case SystemLabelCategorySocial:
		return "Category:Social"
	case SystemLabelCategoryPromotions:
		return "Category:Promotions"
	case SystemLabelCategoryUpdates:
		return "Category:Updates"
	case SystemLabelCategoryForums:
		return "Category:Forums"
	default:
		return "Unknown"
	}
}

// IsCategory reports whether s is one of the five Gmail category
// labels. Categories map onto $category-* keywords (REQ-FILT-201)
// rather than mailbox roles or system flags.
func (s SystemLabel) IsCategory() bool {
	return s >= SystemLabelCategoryPrimary && s <= SystemLabelCategoryForums
}

// Locale is a BCP-47-ish language tag. The label table keys on the
// primary subtag (e.g. "de", "fr") and is consulted with the locale
// returned by DetectLocale.
type Locale string

// SupportedLocales is the closed set of locales whose Gmail-system-label
// strings are known to this package. Per REQ-IMPORT-13 the table covers
// the major Gmail UI locales; expanding the set is data, not code.
//
// Note: regional variants (de-DE vs de-AT, fr-FR vs fr-CA) share the
// same Gmail system-label strings, so we key on the primary subtag.
var SupportedLocales = []Locale{"en", "de", "fr", "es", "it", "nl", "pt", "ja"}

// labelTable carries the system-label strings for each supported locale.
// Values are the literal strings Gmail emits in X-Gmail-Labels for that
// locale's UI; lookup folds case + diacritics (REQ-IMPORT-12).
//
// Categories are the names that follow the localized "Category: " /
// "Kategorie: " prefix. The prefix itself is handled by the parser
// (parseCategoryToken) rather than the lookup.
var labelTable = map[Locale]map[SystemLabel]string{
	"en": {
		SystemLabelInbox:              "Inbox",
		SystemLabelSent:               "Sent",
		SystemLabelDrafts:             "Drafts",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Trash",
		SystemLabelStarred:            "Starred",
		SystemLabelImportant:          "Important",
		SystemLabelUnread:             "Unread",
		SystemLabelOpened:             "Opened",
		SystemLabelChats:              "Chats",
		SystemLabelCategoryPrimary:    "Personal",
		SystemLabelCategorySocial:     "Social",
		SystemLabelCategoryPromotions: "Promotions",
		SystemLabelCategoryUpdates:    "Updates",
		SystemLabelCategoryForums:     "Forums",
	},
	"de": {
		SystemLabelInbox:              "Posteingang",
		SystemLabelSent:               "Gesendet",
		SystemLabelDrafts:             "Entwürfe",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Papierkorb",
		SystemLabelStarred:            "Markiert",
		SystemLabelImportant:          "Wichtig",
		SystemLabelUnread:             "Ungelesen",
		SystemLabelOpened:             "Geöffnet",
		SystemLabelChats:              "Chats",
		SystemLabelCategoryPrimary:    "Persönlich",
		SystemLabelCategorySocial:     "Soziale Medien",
		SystemLabelCategoryPromotions: "Werbung",
		SystemLabelCategoryUpdates:    "Neuigkeiten",
		SystemLabelCategoryForums:     "Foren",
	},
	"fr": {
		SystemLabelInbox:              "Boîte de réception",
		SystemLabelSent:               "Envoyés",
		SystemLabelDrafts:             "Brouillons",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Corbeille",
		SystemLabelStarred:            "Suivis",
		SystemLabelImportant:          "Important",
		SystemLabelUnread:             "Non lu",
		SystemLabelOpened:             "Ouvert",
		SystemLabelChats:              "Chats",
		SystemLabelCategoryPrimary:    "Principale",
		SystemLabelCategorySocial:     "Réseaux sociaux",
		SystemLabelCategoryPromotions: "Promotions",
		SystemLabelCategoryUpdates:    "Notifications",
		SystemLabelCategoryForums:     "Forums",
	},
	"es": {
		SystemLabelInbox:              "Recibidos",
		SystemLabelSent:               "Enviados",
		SystemLabelDrafts:             "Borradores",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Papelera",
		SystemLabelStarred:            "Destacados",
		SystemLabelImportant:          "Importante",
		SystemLabelUnread:             "No leído",
		SystemLabelOpened:             "Abierto",
		SystemLabelChats:              "Chats",
		SystemLabelCategoryPrimary:    "Principal",
		SystemLabelCategorySocial:     "Social",
		SystemLabelCategoryPromotions: "Promociones",
		SystemLabelCategoryUpdates:    "Notificaciones",
		SystemLabelCategoryForums:     "Foros",
	},
	"it": {
		SystemLabelInbox:              "Posta in arrivo",
		SystemLabelSent:               "Inviata",
		SystemLabelDrafts:             "Bozze",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Cestino",
		SystemLabelStarred:            "Speciali",
		SystemLabelImportant:          "Importanti",
		SystemLabelUnread:             "Da leggere",
		SystemLabelOpened:             "Aperto",
		SystemLabelChats:              "Chat",
		SystemLabelCategoryPrimary:    "Principale",
		SystemLabelCategorySocial:     "Social",
		SystemLabelCategoryPromotions: "Promozioni",
		SystemLabelCategoryUpdates:    "Notifiche",
		SystemLabelCategoryForums:     "Forum",
	},
	"nl": {
		SystemLabelInbox:              "Postvak IN",
		SystemLabelSent:               "Verzonden",
		SystemLabelDrafts:             "Concepten",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Prullenbak",
		SystemLabelStarred:            "Met ster",
		SystemLabelImportant:          "Belangrijk",
		SystemLabelUnread:             "Ongelezen",
		SystemLabelOpened:             "Geopend",
		SystemLabelChats:              "Chats",
		SystemLabelCategoryPrimary:    "Persoonlijk",
		SystemLabelCategorySocial:     "Sociaal",
		SystemLabelCategoryPromotions: "Reclame",
		SystemLabelCategoryUpdates:    "Updates",
		SystemLabelCategoryForums:     "Forums",
	},
	"pt": {
		SystemLabelInbox:              "Caixa de entrada",
		SystemLabelSent:               "Enviados",
		SystemLabelDrafts:             "Rascunhos",
		SystemLabelSpam:               "Spam",
		SystemLabelTrash:              "Lixeira",
		SystemLabelStarred:            "Com estrela",
		SystemLabelImportant:          "Importante",
		SystemLabelUnread:             "Não lida",
		SystemLabelOpened:             "Aberto",
		SystemLabelChats:              "Chats",
		SystemLabelCategoryPrimary:    "Principal",
		SystemLabelCategorySocial:     "Redes sociais",
		SystemLabelCategoryPromotions: "Promoções",
		SystemLabelCategoryUpdates:    "Atualizações",
		SystemLabelCategoryForums:     "Fóruns",
	},
	"ja": {
		SystemLabelInbox:              "受信トレイ",
		SystemLabelSent:               "送信済み",
		SystemLabelDrafts:             "下書き",
		SystemLabelSpam:               "迷惑メール",
		SystemLabelTrash:              "ゴミ箱",
		SystemLabelStarred:            "スター付き",
		SystemLabelImportant:          "重要",
		SystemLabelUnread:             "未読",
		SystemLabelOpened:             "開封済み",
		SystemLabelChats:              "チャット",
		SystemLabelCategoryPrimary:    "メイン",
		SystemLabelCategorySocial:     "ソーシャル",
		SystemLabelCategoryPromotions: "プロモーション",
		SystemLabelCategoryUpdates:    "新着",
		SystemLabelCategoryForums:     "フォーラム",
	},
}

// CategoryPrefix is the localized leader Gmail prepends to category
// names in X-Gmail-Labels (e.g. "Category: " in en, "Kategorie: " in
// de). The trailing space-or-not is stripped before compare.
var categoryPrefixes = map[Locale]string{
	"en": "Category:",
	"de": "Kategorie:",
	"fr": "Catégorie:",
	"es": "Categoría:",
	"it": "Categoria:",
	"nl": "Categorie:",
	"pt": "Categoria:",
	"ja": "カテゴリ:",
}

// fold normalises a label string for comparison: lowercased + diacritic-
// stripped + collapsed whitespace + leading/trailing whitespace removed.
// Gmail's localized labels round-trip stably under this fold across all
// the locales in SupportedLocales.
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	out = strings.ToLower(out)
	// Collapse internal whitespace runs to a single space.
	var b strings.Builder
	b.Grow(len(out))
	prevSpace := true
	for _, r := range out {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimRight(b.String(), " ")
}

// Lookup returns the SystemLabel that matches s in the given locale,
// or (SystemLabelUnknown, false) when s names a user-defined label.
// Comparison folds case and diacritics (REQ-IMPORT-12).
func Lookup(s string, locale Locale) (SystemLabel, bool) {
	table, ok := labelTable[locale]
	if !ok {
		return SystemLabelUnknown, false
	}
	folded := fold(s)
	for sym, lit := range table {
		if fold(lit) == folded {
			return sym, true
		}
	}
	return SystemLabelUnknown, false
}

// LookupAny tries every supported locale and returns the first match.
// Used when DetectLocale has not yet picked one (e.g. on the first
// few messages of a stream) so early labels can still be classified.
func LookupAny(s string) (SystemLabel, Locale, bool) {
	for _, loc := range SupportedLocales {
		if sym, ok := Lookup(s, loc); ok {
			return sym, loc, true
		}
	}
	return SystemLabelUnknown, "", false
}

// CategoryPrefix returns the locale's "Category: " prefix, or "" when
// the locale is not in the table.
func CategoryPrefix(locale Locale) string {
	return categoryPrefixes[locale]
}

// AnyCategoryPrefix tries every supported locale's prefix against s
// (case- and diacritic-insensitive) and returns the trimmed tail —
// the inner category name with surrounding whitespace and outer
// double-quote characters stripped — plus the matched locale.
// Returns ("", "", false) when no prefix matches.
//
// Gmail wraps multi-word category names in ASCII double quotes and
// sometimes doubles them (e.g. `Kategorie: ""Persönlich""`); both
// forms are accepted.
func AnyCategoryPrefix(s string) (tail string, loc Locale, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	fs := fold(s)
	for l, prefix := range categoryPrefixes {
		fp := fold(prefix)
		if !strings.HasPrefix(fs, fp) {
			continue
		}
		// Skip the prefix's rune count in the original s. fold preserves
		// rune count for the prefixes we ship (apart from whitespace
		// collapse, and our prefixes have no internal whitespace runs).
		n := utf8.RuneCountInString(prefix)
		rs := []rune(s)
		if n > len(rs) {
			continue
		}
		t := strings.TrimSpace(string(rs[n:]))
		t = strings.Trim(t, "\"")
		t = strings.TrimSpace(t)
		return t, l, true
	}
	return "", "", false
}

// DetectLocale picks the locale whose system-label set best fits the
// supplied X-Gmail-Labels samples. Each sample is one parsed token list
// from one message. The picker scores each locale by how many tokens
// across the sample set match a SystemLabel; the highest-scoring locale
// wins. Returns "en" as a fallback when no locale scores above zero.
func DetectLocale(samples [][]string) Locale {
	scores := make(map[Locale]int, len(SupportedLocales))
	prefixHits := make(map[Locale]int, len(SupportedLocales))
	for _, tokens := range samples {
		for _, tok := range tokens {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			// Category lines: prefix-match the localized "Category: ".
			if tail, loc, ok := AnyCategoryPrefix(tok); ok {
				prefixHits[loc]++
				_ = tail
				continue
			}
			// Plain system-label match.
			for _, loc := range SupportedLocales {
				if _, ok := Lookup(tok, loc); ok {
					scores[loc]++
				}
			}
		}
	}
	best := Locale("en")
	bestScore := -1
	for _, loc := range SupportedLocales {
		s := scores[loc] + 2*prefixHits[loc]
		if s > bestScore {
			bestScore = s
			best = loc
		}
	}
	if bestScore <= 0 {
		return "en"
	}
	return best
}
