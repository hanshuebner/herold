package observe

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth_LivenessAlways200(t *testing.T) {
	h := NewHealth()
	srv := httptest.NewServer(h.LivenessHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("liveness status = %d, want 200", resp.StatusCode)
	}
}

func TestHealth_ReadinessFlip(t *testing.T) {
	h := NewHealth()
	srv := httptest.NewServer(h.ReadinessHandler())
	defer srv.Close()

	resp1, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("pre-MarkReady = %d, want 503", resp1.StatusCode)
	}

	h.MarkReady()

	resp2, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("post-MarkReady = %d, want 200", resp2.StatusCode)
	}

	h.MarkNotReady()
	resp3, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("post-MarkNotReady = %d, want 503", resp3.StatusCode)
	}
}

func TestHealth_ReadyReflectsState(t *testing.T) {
	h := NewHealth()
	if h.Ready() {
		t.Fatalf("fresh Health should not be ready")
	}
	h.MarkReady()
	if !h.Ready() {
		t.Fatalf("after MarkReady should report ready")
	}
	h.MarkNotReady()
	if h.Ready() {
		t.Fatalf("after MarkNotReady should report not ready")
	}
}

func TestHealth_ACMEGate_NotRequired(t *testing.T) {
	// When ACME is not configured, readiness must not be blocked.
	h := NewHealth()
	h.MarkACMENotRequired()
	h.MarkReady()
	if !h.Ready() {
		t.Fatalf("ready with ACME not required, want ready=true")
	}
}

func TestHealth_ACMEGate_RequiredButNotReady(t *testing.T) {
	// When [acme] is configured but account not yet loaded, server is not ready.
	h := NewHealth()
	h.MarkACMERequired()
	h.MarkReady()
	if h.Ready() {
		t.Fatalf("ACME required but not ready: want ready=false, got true")
	}
}

func TestHealth_ACMEGate_RequiredAndReady(t *testing.T) {
	// Once ACME account is loaded, the gate clears.
	h := NewHealth()
	h.MarkACMERequired()
	h.MarkReady()
	h.MarkACMEReady()
	if !h.Ready() {
		t.Fatalf("ACME required and ready: want ready=true, got false")
	}
}
