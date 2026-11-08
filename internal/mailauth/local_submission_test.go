package mailauth

import "testing"

// TestParseAuthResults_LocalSubmission (re #559): the "auth=pass" method
// herold renders for an authenticated local submission round-trips, so a
// reclassify run reads the stored header as a local submission too.
func TestParseAuthResults_LocalSubmission(t *testing.T) {
	got, ok := ParseAuthResults("mx.test; auth=pass smtp.auth=local; x-herold-spam=ham (score=0.05)")
	if !ok || !got.LocalSubmission {
		t.Fatalf("ParseAuthResults ok=%v LocalSubmission=%v, want both true", ok, got.LocalSubmission)
	}
	if got.SPF.Status != AuthUnknown || got.DMARC.Status != AuthUnknown {
		t.Errorf("spf/dmarc = %v/%v, want unevaluated", got.SPF.Status, got.DMARC.Status)
	}
	if other, ok := ParseAuthResults("mx.test; auth=fail"); ok || other.LocalSubmission {
		t.Errorf("auth=fail parsed as local submission")
	}
}
