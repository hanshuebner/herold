package spam

import (
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
)

func parseHeadersForTest(t *testing.T, raw string) mailparse.Message {
	t.Helper()
	msg, err := mailparse.ParseHeadersOnly([]byte(raw + "\r\n\r\n"))
	if err != nil {
		t.Fatalf("ParseHeadersOnly: %v", err)
	}
	return msg
}

// TestStructuralCategory covers the ADR-0002 / REQ-FILT-214 fallback rule
// with the header shapes observed in production (re #491): a discussion
// list needs a List-Post or Precedence: list marker to earn "forums";
// List-Id/List-Unsubscribe alone is one-way bulk mail and falls to
// "updates" or "promotions".
func TestStructuralCategory(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "association newsletter: List-Unsubscribe + List-Id, no List-Post",
			raw: "From: newsletter@kulturrat.de\r\n" +
				"To: hans@netzhansa.com\r\n" +
				"Subject: Der kulturpolitische Wochenreport (39. KW)\r\n" +
				"List-Id: Kulturrat Newsletter <newsletter.kulturrat.de>\r\n" +
				"List-Unsubscribe: <mailto:unsubscribe@kulturrat.de>\r\n",
			want: "updates",
		},
		{
			name: "ADFC campaign newsletter: List-Unsubscribe only, no List-Post",
			raw: "From: newsletter@adfc.de\r\n" +
				"To: hans@netzhansa.com\r\n" +
				"Subject: ADFC-Fahrradklima-Test 2026 - Deine Teilnahme\r\n" +
				"List-Unsubscribe: <mailto:unsubscribe@adfc.de>\r\n",
			want: "updates",
		},
		{
			name: "bulk marketing mail: List-Unsubscribe + Precedence: bulk",
			raw: "From: no-reply@finanzas-ia.example\r\n" +
				"To: hans@netzhansa.com\r\n" +
				"Subject: Control financiero, dashboards y analisis con apoyo de Claude\r\n" +
				"List-Unsubscribe: <mailto:unsubscribe@finanzas-ia.example>\r\n" +
				"Precedence: bulk\r\n",
			want: "promotions",
		},
		{
			name: "discussion list: List-Post + List-Id",
			raw: "From: someone@example.org\r\n" +
				"To: discuss@lists.example.org\r\n" +
				"Subject: Re: agenda for next meeting\r\n" +
				"List-Id: Project Discuss <discuss.lists.example.org>\r\n" +
				"List-Post: <mailto:discuss@lists.example.org>\r\n" +
				"List-Unsubscribe: <mailto:discuss-unsubscribe@lists.example.org>\r\n",
			want: "forums",
		},
		{
			name: "discussion list via Precedence: list only",
			raw: "From: someone@example.org\r\n" +
				"To: discuss@lists.example.org\r\n" +
				"Subject: Re: agenda for next meeting\r\n" +
				"Precedence: list\r\n",
			want: "forums",
		},
		{
			name: "Auto-Submitted with no list headers",
			raw: "From: robot@example.org\r\n" +
				"To: hans@netzhansa.com\r\n" +
				"Subject: Out of office\r\n" +
				"Auto-Submitted: auto-replied\r\n",
			want: "updates",
		},
		{
			name: "no structural markers at all",
			raw: "From: friend@example.org\r\n" +
				"To: hans@netzhansa.com\r\n" +
				"Subject: Dinner Friday?\r\n",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := parseHeadersForTest(t, tc.raw)
			got := StructuralCategory(msg)
			if got != tc.want {
				t.Fatalf("StructuralCategory() = %q, want %q", got, tc.want)
			}
		})
	}
}
