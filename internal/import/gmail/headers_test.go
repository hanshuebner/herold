package gmail

import (
	"reflect"
	"testing"
)

func TestParseGmailLabelsPlainASCII(t *testing.T) {
	got := ParseGmailLabels("Posteingang,Wichtig,Ungelesen")
	want := []string{"Posteingang", "Wichtig", "Ungelesen"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseGmailLabelsRFC2047(t *testing.T) {
	// As emitted by Google Takeout for the user's actual mbox.
	in := "=?UTF-8?Q?Archiviert,Wichtig,Ge=C3=B6ffnet,Kategorie:_=22=22Pers=C3=B6nlich=22=22?="
	got := ParseGmailLabels(in)
	want := []string{"Archiviert", "Wichtig", "Geöffnet", `Kategorie: ""Persönlich""`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestParseGmailLabelsCommaInsideQuotedCategory(t *testing.T) {
	in := `Posteingang,Kategorie: "Soziale, Medien"`
	got := ParseGmailLabels(in)
	want := []string{"Posteingang", `Kategorie: "Soziale, Medien"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseGmailLabelsEmpty(t *testing.T) {
	if got := ParseGmailLabels(""); got != nil {
		t.Errorf("empty: got %v, want nil", got)
	}
	if got := ParseGmailLabels("   "); got != nil {
		t.Errorf("whitespace-only: got %v, want nil", got)
	}
}

func TestParseGmailLabelsEnglishCategory(t *testing.T) {
	in := `Inbox,Important,Category: Updates`
	got := ParseGmailLabels(in)
	want := []string{"Inbox", "Important", "Category: Updates"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
