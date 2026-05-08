package gmail

import (
	"reflect"
	"sort"
	"testing"
)

func TestMapGermanInboxImportantUnread(t *testing.T) {
	d := Map([]string{"Posteingang", "Wichtig", "Ungelesen", `Kategorie: "Neuigkeiten"`}, "de")
	if !roleEq(d.Roles, []SystemLabel{SystemLabelInbox}) {
		t.Errorf("Roles = %v, want [Inbox]", d.Roles)
	}
	if d.Seen {
		t.Errorf("Seen = true, want false (Unread present)")
	}
	wantKw := []string{`\Important`, "$category-updates"}
	sort.Strings(d.Keywords)
	sort.Strings(wantKw)
	if !reflect.DeepEqual(d.Keywords, wantKw) {
		t.Errorf("Keywords = %v, want %v", d.Keywords, wantKw)
	}
	if d.Category != "$category-updates" {
		t.Errorf("Category = %q, want $category-updates", d.Category)
	}
	if len(d.UserLabels) != 0 {
		t.Errorf("UserLabels = %v, want empty", d.UserLabels)
	}
}

func TestMapGermanArchivedUserLabel(t *testing.T) {
	// "Archiviert" is the user's own label, not a German Gmail system
	// label; it must pass through as a UserLabel.
	d := Map([]string{"Archiviert", "Wichtig", "Geöffnet", `Kategorie: "Persönlich"`}, "de")
	if len(d.Roles) != 0 {
		t.Errorf("Roles = %v, want empty (no Inbox; archived)", d.Roles)
	}
	if !d.Seen {
		t.Errorf("Seen = false, want true (Geöffnet sets seen)")
	}
	if !contains(d.UserLabels, "Archiviert") {
		t.Errorf("UserLabels = %v, want to contain Archiviert", d.UserLabels)
	}
	if d.Category != "$category-primary" {
		t.Errorf("Category = %q, want $category-primary (Persönlich)", d.Category)
	}
}

func TestMapEnglishStarredSent(t *testing.T) {
	d := Map([]string{"Sent", "Starred"}, "en")
	if !roleEq(d.Roles, []SystemLabel{SystemLabelSent}) {
		t.Errorf("Roles = %v, want [Sent]", d.Roles)
	}
	if !contains(d.Keywords, `\Sent`) || !contains(d.Keywords, `\Flagged`) {
		t.Errorf("Keywords = %v, want \\Sent and \\Flagged", d.Keywords)
	}
}

func TestMapDraftAddsDraftFlag(t *testing.T) {
	d := Map([]string{"Drafts"}, "en")
	if !roleEq(d.Roles, []SystemLabel{SystemLabelDrafts}) {
		t.Errorf("Roles = %v, want [Drafts]", d.Roles)
	}
	if !contains(d.Keywords, `\Draft`) {
		t.Errorf("Keywords missing \\Draft: %v", d.Keywords)
	}
}

func TestMapSpamMapsToJunk(t *testing.T) {
	d := Map([]string{"Spam"}, "en")
	if !roleEq(d.Roles, []SystemLabel{SystemLabelSpam}) {
		t.Errorf("Roles = %v, want [Spam]", d.Roles)
	}
	if !contains(d.Keywords, `\Junk`) {
		t.Errorf("Keywords missing \\Junk: %v", d.Keywords)
	}
}

func TestMapEmptyTokens(t *testing.T) {
	d := Map(nil, "de")
	if len(d.Roles) != 0 || len(d.Keywords) != 0 || len(d.UserLabels) != 0 {
		t.Errorf("empty input should yield empty Decision; got %+v", d)
	}
	if !d.Seen {
		t.Errorf("default Seen = true expected; got false")
	}
}

func TestMapHierarchicalUserLabel(t *testing.T) {
	// Slashes in user labels are preserved; the importer turns them
	// into mailbox parent-child relationships at materialisation time.
	d := Map([]string{"Travel/2024/Italy"}, "en")
	if !contains(d.UserLabels, "Travel/2024/Italy") {
		t.Errorf("UserLabels = %v, want to contain Travel/2024/Italy", d.UserLabels)
	}
}

// helpers

func roleEq(got, want []SystemLabel) bool {
	if len(got) != len(want) {
		return false
	}
	g := make([]int, len(got))
	w := make([]int, len(want))
	for i, r := range got {
		g[i] = int(r)
	}
	for i, r := range want {
		w[i] = int(r)
	}
	sort.Ints(g)
	sort.Ints(w)
	return reflect.DeepEqual(g, w)
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
