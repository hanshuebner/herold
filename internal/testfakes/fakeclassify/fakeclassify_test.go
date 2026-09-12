package fakeclassify

import (
	"context"
	"testing"

	"github.com/hanshuebner/herold/plugins/sdk"
)

func TestClassify_Rules(t *testing.T) {
	cases := []struct {
		name        string
		subject     string
		wantVerdict string
		wantCat     string
	}{
		{"spam wins", "Buy now +SPAM offer", VerdictSpam, ""},
		{"promo", "Weekly deals +promo inside", VerdictHam, CategoryPromotions},
		{"updates", "Your order shipped +updates", VerdictHam, CategoryUpdates},
		{"default primary", "Hi from a friend", VerdictHam, CategoryPrimary},
		{"case insensitive promo", "SAVE BIG +PROMO", VerdictHam, CategoryPromotions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Classify(tc.subject)
			if r.Verdict != tc.wantVerdict {
				t.Errorf("verdict = %q, want %q", r.Verdict, tc.wantVerdict)
			}
			if r.Category != tc.wantCat {
				t.Errorf("category = %q, want %q", r.Category, tc.wantCat)
			}
			if r.Reason == "" {
				t.Error("reason must be non-empty so the transparency record carries content")
			}
		})
	}
}

func TestClassify_SpamConfidenceHigherThanHam(t *testing.T) {
	if Classify("+spam").Confidence <= Classify("hello").Confidence {
		t.Error("spam confidence must exceed the default ham confidence")
	}
}

func TestHandler_MailClassify(t *testing.T) {
	h := NewHandler()
	res, err := h.MailClassify(context.Background(), sdk.MailClassifyParams{
		SpamClassifyParams: sdk.SpamClassifyParams{Subject: "Weekly deals +promo"},
	})
	if err != nil {
		t.Fatalf("MailClassify: %v", err)
	}
	if res.Category != CategoryPromotions {
		t.Errorf("category = %q, want %q", res.Category, CategoryPromotions)
	}
	if res.Verdict != VerdictHam {
		t.Errorf("verdict = %q, want %q", res.Verdict, VerdictHam)
	}
}

func TestHandler_SpamClassify_NoCategory(t *testing.T) {
	h := NewHandler()
	res, err := h.SpamClassify(context.Background(), sdk.SpamClassifyParams{Subject: "+promo"})
	if err != nil {
		t.Fatalf("SpamClassify: %v", err)
	}
	// SpamClassifyResult carries no category field at all -- confirming
	// only that the call succeeds and returns the ham verdict.
	if res.Verdict != VerdictHam {
		t.Errorf("verdict = %q, want %q", res.Verdict, VerdictHam)
	}
}

func TestHandler_OnConfigure_RejectsUnknownOptions(t *testing.T) {
	h := NewHandler()
	if err := h.OnConfigure(context.Background(), map[string]any{"bogus": "x"}); err == nil {
		t.Error("expected an error for an unknown option")
	}
	if err := h.OnConfigure(context.Background(), nil); err != nil {
		t.Errorf("empty options must be accepted: %v", err)
	}
}

func TestManifest_TemperaturePinned(t *testing.T) {
	m := Manifest()
	if m.Temperature == nil || *m.Temperature != 0 {
		t.Error("Manifest.Temperature must be pinned to 0")
	}
}
