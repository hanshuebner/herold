package gmail

import (
	"strings"
	"testing"
)

func TestTranslateFiltersGermanRealistic(t *testing.T) {
	// Pulled from the user's actual Filter.json.
	filters := []GmailFilter{
		{
			Query:          "subject:Chefkoch subject:Newsletter",
			LabelsToAdd:    []string{"kochen", "Archiviert"},
			LabelsToRemove: []string{"Posteingang"},
		},
		{
			Query:          "from:hetzner.de",
			LabelsToAdd:    []string{"hetzner", "Archiviert"},
			LabelsToRemove: []string{"Posteingang"},
		},
	}
	out := TranslateFiltersToSieve(filters, "de", nil)
	if !strings.Contains(out, `require ["fileinto", "imap4flags"];`) {
		t.Errorf("missing require line:\n%s", out)
	}
	if !strings.Contains(out, `header :contains "subject" "Chefkoch"`) {
		t.Errorf("subject:Chefkoch translation missing:\n%s", out)
	}
	if !strings.Contains(out, `header :contains "from" "hetzner.de"`) {
		t.Errorf("from:hetzner.de translation missing:\n%s", out)
	}
	if !strings.Contains(out, `fileinto "Archiviert";`) {
		t.Errorf("user label fileinto missing:\n%s", out)
	}
	if !strings.Contains(out, `# (Gmail: skip the Inbox)`) {
		t.Errorf("Posteingang removal annotation missing:\n%s", out)
	}
}

func TestTranslateFiltersUserLabelMailboxOverride(t *testing.T) {
	filters := []GmailFilter{
		{Query: "subject:foo", LabelsToAdd: []string{"travel/2024"}, LabelsToRemove: []string{"Inbox"}},
	}
	out := TranslateFiltersToSieve(filters, "en", func(s string) string {
		return "Labels/" + s
	})
	if !strings.Contains(out, `fileinto "Labels/travel/2024";`) {
		t.Errorf("user label override missing:\n%s", out)
	}
}

func TestTranslateFiltersSystemLabelActions(t *testing.T) {
	// Adding "Starred" should map to addflag \\Flagged (not fileinto).
	filters := []GmailFilter{
		{Query: "from:alerts", LabelsToAdd: []string{"Starred"}},
	}
	out := TranslateFiltersToSieve(filters, "en", nil)
	if !strings.Contains(out, `addflag "\\Flagged";`) {
		t.Errorf("Starred → \\Flagged missing:\n%s", out)
	}
}

func TestTranslateFiltersSecondaryActions(t *testing.T) {
	filters := []GmailFilter{
		{Query: "from:notifications", ShouldMarkAsRead: true, ShouldStar: true},
	}
	out := TranslateFiltersToSieve(filters, "en", nil)
	if !strings.Contains(out, `addflag "\\Seen";`) {
		t.Errorf("ShouldMarkAsRead → \\Seen missing:\n%s", out)
	}
	if !strings.Contains(out, `addflag "\\Flagged";`) {
		t.Errorf("ShouldStar → \\Flagged missing:\n%s", out)
	}
}

func TestTranslateFiltersForwardCommented(t *testing.T) {
	filters := []GmailFilter{
		{Query: "from:x", ForwardTo: "elsewhere@example.com"},
	}
	out := TranslateFiltersToSieve(filters, "en", nil)
	if !strings.Contains(out, "forward to elsewhere@example.com") {
		t.Errorf("forwardTo annotation missing:\n%s", out)
	}
}

func TestTranslateFiltersEmpty(t *testing.T) {
	out := TranslateFiltersToSieve(nil, "en", nil)
	if !strings.Contains(out, "translated from Gmail filters") {
		t.Errorf("expected header banner:\n%s", out)
	}
}

func TestSplitQueryTokensQuoted(t *testing.T) {
	got := splitQueryTokens(`from:alerts "weekly summary" subject:foo`)
	want := []string{`from:alerts`, `"weekly summary"`, `subject:foo`}
	if len(got) != len(want) {
		t.Fatalf("token count: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestQuoteSieveStringEscapes(t *testing.T) {
	got := quoteSieveString(`he said "ok\back"`)
	if got != `"he said \"ok\\back\""` {
		t.Errorf("got %q", got)
	}
}
