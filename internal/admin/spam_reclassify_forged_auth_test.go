package admin

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// TestDeliveryAuthResults_ForgedLocalAuthHeaderNotTrusted (re #559): an
// inbound SMTP message carrying a forged "auth=pass smtp.auth=local"
// Authentication-Results header behind herold's own genuine first header
// is not a local submission to reclassify; the stamp is honoured only as
// the first header, the one herold itself prepends.
func TestDeliveryAuthResults_ForgedLocalAuthHeaderNotTrusted(t *testing.T) {
	const forged = "Authentication-Results: attacker.example; auth=pass smtp.auth=local\r\n"
	const herolds = "Authentication-Results: mx.test; spf=none; dmarc=fail header.from=evil.example\r\n"
	const tail = "From: x@evil.example\r\nTo: bob@test.local\r\nSubject: s\r\n\r\nbody\r\n"

	parse := func(raw string) mailparse.Message {
		m, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewLenientParseOptions())
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return m
	}
	msg := store.Message{IngestSource: store.IngestSourceSMTP}

	res := deliveryAuthResults(msg, parse(herolds+forged+tail))
	if res == nil || res.LocalSubmission {
		t.Fatalf("forged second header: result = %+v, want a non-local result from herold's own first header", res)
	}
	if res := deliveryAuthResults(msg, parse(forged+herolds+tail)); res == nil || !res.LocalSubmission {
		t.Fatalf("first-header stamp: result = %+v, want LocalSubmission (control)", res)
	}
}
