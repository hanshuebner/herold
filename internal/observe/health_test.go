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
