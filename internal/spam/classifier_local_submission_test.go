package spam

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
)

// TestClassify_LocalSubmissionIgnoresAuthDerivedDecisiveSignals (re
// #559): a model that still reports dmarc_fail + unauthenticated_sender
// for an authenticated local submission must not have its ham verdict
// resolved to spam; the same signals on a message without the flag still
// are. The request handed to the plugin carries local_submission and the
// authenticated-submission summary, with SPF/DKIM/DMARC "none".
func TestClassify_LocalSubmissionIgnoresAuthDerivedDecisiveSignals(t *testing.T) {
	msg := buildMessage(t, coldMarketingPitchRaw)
	run := func(auth *mailauth.AuthResults) (Classification, Request) {
		var seen Request
		invoker := newFakeInvoker()
		invoker.handle("herold-spam-llm", ClassifyMethod, func(_ context.Context, params any) (json.RawMessage, error) {
			b, _ := json.Marshal(params)
			_ = json.Unmarshal(b, &seen)
			return json.RawMessage(`{"verdict":"ham","score":0.05,"spam_signals":["dmarc_fail","unauthenticated_sender"]}`), nil
		})
		c := New(invoker, silentLogger(), clock.NewFake(time.Now()))
		r, err := c.Classify(context.Background(), msg, auth, "herold-spam-llm", ClassifyContext{}, OwnAddressInfo{})
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		return r, seen
	}

	local, req := run(&mailauth.AuthResults{LocalSubmission: true})
	if local.Verdict != Ham {
		t.Errorf("local submission verdict = %v, want Ham", local.Verdict)
	}
	if !req.LocalSubmission || req.AuthSummary != localSubmissionSummary {
		t.Errorf("request local_submission=%v auth_summary=%q, want true and the local-submission summary", req.LocalSubmission, req.AuthSummary)
	}
	if req.SPF != "none" || req.DKIM != "none" || req.DMARC != "none" {
		t.Errorf("request spf/dkim/dmarc = %s/%s/%s, want none/none/none", req.SPF, req.DKIM, req.DMARC)
	}
	if len(local.SpamSignals) != 2 {
		t.Errorf("SpamSignals = %v, want both signals kept on the record", local.SpamSignals)
	}

	external, _ := run(&mailauth.AuthResults{})
	if external.Verdict != Spam {
		t.Errorf("external verdict = %v, want Spam (override still applies without the flag)", external.Verdict)
	}
}
