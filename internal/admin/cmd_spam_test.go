package admin

import (
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/protoadmin"
)

func TestCLISpamPolicy_RoundTrip(t *testing.T) {
	store := &memSpamPolicyStore{}
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.SpamPolicyStore = store
	})

	// Set a policy.
	if _, _, err := env.run("spam", "policy-set",
		"--plugin=llm",
		"--threshold=0.8",
		"--model=gpt-4"); err != nil {
		t.Fatalf("policy-set: %v", err)
	}

	// Show should reflect the change.
	out, _, err := env.run("spam", "policy-show", "--json")
	if err != nil {
		t.Fatalf("policy-show: %v", err)
	}
	if !strings.Contains(out, `"plugin_name": "llm"`) {
		t.Fatalf("policy-show missing plugin_name=llm: %s", out)
	}
	if !strings.Contains(out, `"threshold": 0.8`) {
		t.Fatalf("policy-show missing threshold=0.8: %s", out)
	}
	if !strings.Contains(out, `"model": "gpt-4"`) {
		t.Fatalf("policy-show missing model=gpt-4: %s", out)
	}

	// Direct store inspection corroborates.
	got := store.GetSpamPolicy()
	if got.PluginName != "llm" || got.Threshold != 0.8 || got.Model != "gpt-4" {
		t.Fatalf("store has unexpected policy: %+v", got)
	}
}

func TestCLISpamPolicySet_RejectsBadThreshold(t *testing.T) {
	store := &memSpamPolicyStore{}
	env := newCLITestEnv(t, func(o *protoadmin.Options) {
		o.SpamPolicyStore = store
	})
	_, _, err := env.run("spam", "policy-set", "--plugin=llm", "--threshold=2.0")
	if err == nil {
		t.Fatalf("expected error for threshold > 1.0")
	}
	if !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("expected 'threshold' in error; got %v", err)
	}
}

func TestCLISpamPolicy_NoStoreConfigured(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("spam", "policy-show")
	if err == nil {
		t.Fatalf("expected 501 not_implemented")
	}
	if !strings.Contains(err.Error(), "501") && !strings.Contains(err.Error(), "not_implemented") {
		t.Fatalf("expected 501 / not_implemented; got %v", err)
	}
}
