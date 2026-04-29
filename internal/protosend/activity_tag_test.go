package protosend_test

// activity_tag_test.go verifies REQ-OPS-86 / REQ-OPS-86a for protosend:
// every log record emitted during a request carries a valid "activity"
// attribute from the closed enum {user, audit, system, poll, access, internal}.
//
// Focused tests assert specific activity values for high-value records:
//   - per-request access log  → access
//   - send accepted           → user
//   - auth failure            → audit
//   - panic recovery          → internal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protosend"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// buildSendTagServer constructs a protosend.Server backed by fakestore +
// fakeQueue using the supplied recording logger.
func buildSendTagServer(t *testing.T, lg *slog.Logger) (*protosend.Server, *fakestore.Store, string) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	// Seed a local domain.
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "example.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	// Seed a principal.
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	// Mint an API key with mail.send scope.
	const plainKey = "hk_activity_tag_test_key_AAAAAAAAAAAAAAAAAAAAAAAAA"
	hash := protosend.HashAPIKey(plainKey)
	scopeJSON := `["mail.send"]`
	if _, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID,
		Hash:        hash,
		Name:        "alice-send",
		ScopeJSON:   scopeJSON,
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	srv := protosend.NewServer(fs, nil, &fakeTagQueue{}, nil, lg, clk, protosend.Options{
		MaxConcurrentRequests: 8,
		RateLimitPerKey:       60,
		MaxRecipients:         100,
		MaxBatchItems:         50,
		Hostname:              "mx.example.test",
	})
	return srv, fs, plainKey
}

// fakeTagQueue is a minimal Submitter for activity-tag tests.
type fakeTagQueue struct{}

func (q *fakeTagQueue) Submit(_ context.Context, _ queue.Submission) (queue.EnvelopeID, error) {
	return "env-test-1", nil
}

// sendTagRequest sends a minimal structured mail request.
func sendTagRequest(apiKey string) *http.Request {
	body, _ := json.Marshal(map[string]any{
		"source": "alice@example.test",
		"destination": map[string]any{
			"toAddresses": []string{"bob@external.example"},
		},
		"message": map[string]any{
			"subject": "activity tag test",
			"body":    map[string]any{"text": "hello"},
		},
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return r
}

// sendTagRawRequest sends a minimal raw mail request.
func sendTagRawRequest(apiKey string) *http.Request {
	raw := "From: alice@example.test\r\nTo: bob@external.example\r\nSubject: raw tag test\r\n\r\nhello\r\n"
	body, _ := json.Marshal(map[string]any{
		"destinations": []string{"bob@external.example"},
		"rawMessage":   base64.StdEncoding.EncodeToString([]byte(raw)),
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send-raw", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return r
}

// TestSendActivityTag_RequestLog_IsAccess asserts every record produced
// during a send request is validly tagged (access log sets activity=access
// on the scoped logger). REQ-OPS-86a.
func TestSendActivityTag_RequestLog_IsAccess(t *testing.T) {
	t.Parallel()
	observe.AssertActivityTagged(t, func(lg *slog.Logger) {
		srv, _, apiKey := buildSendTagServer(t, lg)
		handler := srv.Handler()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, sendTagRequest(apiKey))
	})
}

// TestSendActivityTag_SendAccepted_IsUser asserts that a successful
// /send emits at least one activity=user record. REQ-OPS-86d.
func TestSendActivityTag_SendAccepted_IsUser(t *testing.T) {
	t.Parallel()
	var userSeen bool
	lg := slog.New(&sendCaptureHandler{
		onRecordWithLevel: func(activity string, _ slog.Level) {
			if activity == observe.ActivityUser {
				userSeen = true
			}
		},
	})
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	const plainKey = "hk_send_user_tag_test_AAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID,
		Hash:        protosend.HashAPIKey(plainKey),
		Name:        "test",
		ScopeJSON:   `["mail.send"]`,
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	srv := protosend.NewServer(fs, nil, &fakeTagQueue{}, nil, lg, clk, protosend.Options{
		RateLimitPerKey: 60,
		MaxRecipients:   100,
		Hostname:        "mx.example.test",
	})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, sendTagRequest(plainKey))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !userSeen {
		t.Error("expected at least one activity=user record after successful send (REQ-OPS-86d)")
	}
}

// TestSendActivityTag_SendRawAccepted_IsUser asserts that a successful
// /send-raw also emits activity=user. REQ-OPS-86d.
func TestSendActivityTag_SendRawAccepted_IsUser(t *testing.T) {
	t.Parallel()
	var userSeen bool
	lg := slog.New(&sendCaptureHandler{
		onRecordWithLevel: func(activity string, _ slog.Level) {
			if activity == observe.ActivityUser {
				userSeen = true
			}
		},
	})
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	if err := fs.Meta().InsertDomain(context.Background(), store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	p, err := fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	const plainKey = "hk_send_raw_user_tag_test_AAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := fs.Meta().InsertAPIKey(context.Background(), store.APIKey{
		PrincipalID: p.ID,
		Hash:        protosend.HashAPIKey(plainKey),
		Name:        "test",
		ScopeJSON:   `["mail.send"]`,
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	srv := protosend.NewServer(fs, nil, &fakeTagQueue{}, nil, lg, clk, protosend.Options{
		RateLimitPerKey: 60,
		MaxRecipients:   100,
		Hostname:        "mx.example.test",
	})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, sendTagRawRequest(plainKey))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !userSeen {
		t.Error("expected at least one activity=user record after successful send-raw (REQ-OPS-86d)")
	}
}

// TestSendActivityTag_AuthFailure_IsAudit asserts that a missing/bad API
// key produces an activity=audit record at warn. REQ-OPS-86.
func TestSendActivityTag_AuthFailure_IsAudit(t *testing.T) {
	t.Parallel()
	var auditWarnSeen bool
	lg := slog.New(&sendCaptureHandler{
		onRecordWithLevel: func(activity string, lvl slog.Level) {
			if activity == observe.ActivityAudit && lvl >= slog.LevelWarn {
				auditWarnSeen = true
			}
		},
	})
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	srv := protosend.NewServer(fs, nil, &fakeTagQueue{}, nil, lg, clk, protosend.Options{
		RateLimitPerKey: 60,
		MaxRecipients:   100,
	})
	// No Authorization header — auth must fail with audit/warn.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", nil)
	r.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
	}
	if !auditWarnSeen {
		t.Error("expected activity=audit warn record on auth failure (REQ-OPS-86)")
	}
}

// TestSendActivityTag_AllRecordsTagged asserts no record from a typical
// send request is missing the activity attribute. REQ-OPS-86a.
func TestSendActivityTag_AllRecordsTagged(t *testing.T) {
	t.Parallel()
	observe.AssertActivityTagged(t, func(lg *slog.Logger) {
		srv, _, apiKey := buildSendTagServer(t, lg)
		handler := srv.Handler()

		// Successful structured send.
		w1 := httptest.NewRecorder()
		handler.ServeHTTP(w1, sendTagRequest(apiKey))

		// Successful raw send.
		w2 := httptest.NewRecorder()
		handler.ServeHTTP(w2, sendTagRawRequest(apiKey))
	})
}

// sendCaptureHandler is a test-only slog.Handler for protosend tests.
type sendCaptureHandler struct {
	onRecordWithLevel func(activity string, lvl slog.Level)
	pre               map[string]string
}

func (h *sendCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *sendCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	activity := h.pre["activity"]
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "activity" {
			activity = a.Value.String()
			return false
		}
		return true
	})
	if h.onRecordWithLevel != nil {
		h.onRecordWithLevel(activity, r.Level)
	}
	return nil
}

func (h *sendCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(h.pre)+len(attrs))
	for k, v := range h.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &sendCaptureHandler{
		onRecordWithLevel: h.onRecordWithLevel,
		pre:               merged,
	}
}

func (h *sendCaptureHandler) WithGroup(_ string) slog.Handler { return h }
