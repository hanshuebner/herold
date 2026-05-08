package gmail

import "testing"

func TestLookupExact(t *testing.T) {
	cases := []struct {
		s    string
		loc  Locale
		want SystemLabel
	}{
		{"Inbox", "en", SystemLabelInbox},
		{"Posteingang", "de", SystemLabelInbox},
		{"Wichtig", "de", SystemLabelImportant},
		{"Ungelesen", "de", SystemLabelUnread},
		{"Geöffnet", "de", SystemLabelOpened},
		{"Markiert", "de", SystemLabelStarred},
		{"Papierkorb", "de", SystemLabelTrash},
		{"Entwürfe", "de", SystemLabelDrafts},
		{"Gesendet", "de", SystemLabelSent},
		{"Spam", "de", SystemLabelSpam},
		{"受信トレイ", "ja", SystemLabelInbox},
	}
	for _, c := range cases {
		got, ok := Lookup(c.s, c.loc)
		if !ok || got != c.want {
			t.Errorf("Lookup(%q, %q) = (%v, %v), want %v", c.s, c.loc, got, ok, c.want)
		}
	}
}

func TestLookupCaseAndDiacriticFold(t *testing.T) {
	// Diacritic-stripped form (ASCII-only "Geoffnet" vs "Geöffnet") and
	// lowercase variants must still match. Truncated takeout filenames
	// often go through ASCII-fold layers and we want to be tolerant.
	if got, ok := Lookup("geoffnet", "de"); !ok || got != SystemLabelOpened {
		t.Errorf("ASCII-folded German Opened: got (%v, %v)", got, ok)
	}
	if got, ok := Lookup("ENTWÜRFE", "de"); !ok || got != SystemLabelDrafts {
		t.Errorf("uppercase German Drafts: got (%v, %v)", got, ok)
	}
}

func TestLookupMissReturnsUnknown(t *testing.T) {
	if got, ok := Lookup("Archiviert", "de"); ok {
		t.Errorf("Archiviert is a user-defined label, not a system label; got %v", got)
	}
}

func TestLookupAnyFindsLocale(t *testing.T) {
	sym, loc, ok := LookupAny("Posteingang")
	if !ok || sym != SystemLabelInbox || loc != "de" {
		t.Errorf("LookupAny(Posteingang) = (%v, %v, %v), want (Inbox, de, true)", sym, loc, ok)
	}
}

func TestAnyCategoryPrefix(t *testing.T) {
	cases := []struct {
		in       string
		wantTail string
		wantLoc  Locale
	}{
		// Plain English category.
		{`Category: Promotions`, "Promotions", "en"},
		// German with single quotes around a one-word inner name.
		{`Kategorie: "Persönlich"`, "Persönlich", "de"},
		// German with the doubled-quote form Gmail emits in the wire.
		{`Kategorie: ""Soziale Medien""`, "Soziale Medien", "de"},
		// Mixed case + collapsed whitespace.
		{`KATEGORIE:    Neuigkeiten`, "Neuigkeiten", "de"},
		// Japanese.
		{`カテゴリ: メイン`, "メイン", "ja"},
	}
	for _, c := range cases {
		got, loc, ok := AnyCategoryPrefix(c.in)
		if !ok || got != c.wantTail || loc != c.wantLoc {
			t.Errorf("AnyCategoryPrefix(%q) = (%q, %q, %v); want (%q, %q, true)",
				c.in, got, loc, ok, c.wantTail, c.wantLoc)
		}
	}
}

func TestAnyCategoryPrefixMiss(t *testing.T) {
	if _, _, ok := AnyCategoryPrefix("Posteingang"); ok {
		t.Errorf("plain system label should not match a category prefix")
	}
	if _, _, ok := AnyCategoryPrefix(""); ok {
		t.Errorf("empty input should not match")
	}
}

func TestDetectLocaleGerman(t *testing.T) {
	samples := [][]string{
		{"Posteingang", "Wichtig"},
		{"Ungelesen", `Kategorie: "Neuigkeiten"`},
		{"Markiert", "Geöffnet"},
	}
	if loc := DetectLocale(samples); loc != "de" {
		t.Errorf("DetectLocale = %q, want de", loc)
	}
}

func TestDetectLocaleEnglish(t *testing.T) {
	samples := [][]string{
		{"Inbox", "Important"},
		{"Unread", "Category: Updates"},
	}
	if loc := DetectLocale(samples); loc != "en" {
		t.Errorf("DetectLocale = %q, want en", loc)
	}
}

func TestDetectLocaleFallsBackToEnglish(t *testing.T) {
	// User labels only, no recognisable system labels in any locale.
	samples := [][]string{{"Archiviert", "lisp", "elektronik"}}
	if loc := DetectLocale(samples); loc != "en" {
		t.Errorf("DetectLocale fallback = %q, want en", loc)
	}
}

func TestSystemLabelStringStable(t *testing.T) {
	// Diagnostic strings are user-visible in CLI logs; assert they don't
	// drift silently. If you change one, change the test deliberately.
	want := map[SystemLabel]string{
		SystemLabelInbox:              "Inbox",
		SystemLabelCategoryPrimary:    "Category:Primary",
		SystemLabelCategoryUpdates:    "Category:Updates",
		SystemLabelCategoryPromotions: "Category:Promotions",
	}
	for sym, w := range want {
		if got := sym.String(); got != w {
			t.Errorf("%d.String() = %q, want %q", sym, got, w)
		}
	}
}
