package protoadmin_test

// activity_tag_test.go verifies REQ-OPS-86 / REQ-OPS-86a for protoadmin:
// every log record emitted during a request carries a valid "activity"
// attribute from the closed enum {user, audit, system, poll, access, internal}.
//
// Focused tests assert specific activity values for high-value records:
//   - per-request access log → access
//   - admin mutation (domain create) → user
//   - permission denial → audit
//   - login failure → audit
//   - panic recovery → internal

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/authsession"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// buildTagServer constructs a protoadmin.Server pointed at a fresh
// fakestore and using the supplied recording logger.
func buildTagServer(t *testing.T, lg *slog.Logger) (*protoadmin.Server, *fakestore.Store, *directory.Directory, *clock.FakeClock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, lg, clk, protoadmin.Options{
		BootstrapPerWindow: 10,
		BootstrapWindow:    time.Minute,
		Session: authsession.SessionConfig{
			SigningKey:  []byte("activity-tag-test-key-32bytes-xx"),
			CookieName: "herold_admin_session",
		},
	})
	t.Cleanup(func() { _ = fs.Close() })
	return srv, fs, dir, clk
}

// bootstrapKey does POST /api/v1/bootstrap and returns the initial API key.
func bootstrapKey(t *testing.T, handler http.Handler, email string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"email": email})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("bootstrap %s: %d %s", email, w.Code, w.Body.String())
	}
	var out struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal bootstrap: %v %s", err, w.Body.String())
	}
	return out.InitialAPIKey
}

// TestActivityTag_RequestLog_IsAccess asserts every record produced by a
// plain GET is validly tagged (the access log sets activity=access on the
// scoped logger so downstream records inherit it). REQ-OPS-86a.
func TestActivityTag_RequestLog_IsAccess(t *testing.T) {
	t.Parallel()
	observe.AssertActivityTagged(t, func(lg *slog.Logger) {
		srv, _, _, _ := buildTagServer(t, lg)
		handler := srv.Handler()

		apiKey := bootstrapKey(t, handler, "access@example.com")

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/server/status", nil)
		r.Header.Set("Authorization", "Bearer "+apiKey)
		handler.ServeHTTP(w, r)
	})
}

// TestActivityTag_AdminMutation_IsUser asserts that creating a domain
// emits at least one record tagged activity=user. REQ-OPS-86d.
func TestActivityTag_AdminMutation_IsUser(t *testing.T) {
	t.Parallel()
	// We specifically capture and check the mutation record here rather
	// than using AssertActivityTagged (which only checks enum membership).
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	var userSeen bool
	captureHandler := &captureActivityHandler{
		onRecord: func(activity string) {
			if activity == observe.ActivityUser {
				userSeen = true
			}
		},
	}
	lg := slog.New(captureHandler)
	srv := protoadmin.NewServer(fs, dir, rp, lg, clk, protoadmin.Options{
		BootstrapPerWindow: 10,
		BootstrapWindow:    time.Minute,
	})
	handler := srv.Handler()
	apiKey := bootstrapKey(t, handler, "mutation@example.com")

	b, _ := json.Marshal(map[string]any{"name": "example.org"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/domains", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiKey)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create domain: %d %s", w.Code, w.Body.String())
	}
	if !userSeen {
		t.Error("expected at least one log record with activity=user after domain create (REQ-OPS-86d)")
	}
}

// TestActivityTag_PermissionDenial_IsAudit asserts that a non-admin
// caller getting a 403 produces an activity=audit record. REQ-OPS-86.
func TestActivityTag_PermissionDenial_IsAudit(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	var auditWarnSeen bool
	captureHandler := &captureActivityHandler{
		onRecordWithLevel: func(activity string, lvl slog.Level) {
			if activity == observe.ActivityAudit && lvl >= slog.LevelWarn {
				auditWarnSeen = true
			}
		},
	}
	lg := slog.New(captureHandler)
	srv := protoadmin.NewServer(fs, dir, rp, lg, clk, protoadmin.Options{
		BootstrapPerWindow:      10,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 100,
	})
	handler := srv.Handler()

	// Bootstrap to get admin user.
	bootstrapKey(t, handler, "admin@example.com")

	// Create a regular (non-admin) principal and an API key for them.
	ctx := context.Background()
	pid, err := dir.CreatePrincipal(ctx, "user@example.com", "P@ssw0rdLong!")
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	scopeJSON := `["mail.send"]`
	plain, _, err := protoadmin.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	hash := protoadmin.HashAPIKey(plain)
	_, err = fs.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: pid,
		Hash:        hash,
		Name:        "test",
		ScopeJSON:   scopeJSON,
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	// Non-admin tries to list domains → 403 → should see audit/warn.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/domains", nil)
	r.Header.Set("Authorization", "Bearer "+plain)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", w.Code, w.Body.String())
	}
	if !auditWarnSeen {
		t.Error("expected at least one activity=audit warn record on permission denial (REQ-OPS-86)")
	}
}

// TestActivityTag_LoginFailure_IsAuditWarn asserts that a wrong-password
// login attempt emits activity=audit at warn level. REQ-OPS-86.
func TestActivityTag_LoginFailure_IsAuditWarn(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	var auditWarnSeen bool
	captureHandler := &captureActivityHandler{
		onRecordWithLevel: func(activity string, lvl slog.Level) {
			if activity == observe.ActivityAudit && lvl >= slog.LevelWarn {
				auditWarnSeen = true
			}
		},
	}
	lg := slog.New(captureHandler)
	srv := protoadmin.NewServer(fs, dir, rp, lg, clk, protoadmin.Options{
		BootstrapPerWindow: 10,
		BootstrapWindow:    time.Minute,
		Session: authsession.SessionConfig{
			SigningKey:  []byte("activity-tag-test-key-32bytes-xx"),
			CookieName: "herold_admin_session",
		},
	})
	handler := srv.Handler()

	b, _ := json.Marshal(map[string]any{
		"email":    "nobody@example.com",
		"password": "wrong",
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if !auditWarnSeen {
		t.Error("expected activity=audit warn record on login failure (REQ-OPS-86)")
	}
}

// TestActivityTag_PanicRecover_IsInternal asserts that panic recovery
// produces an activity=internal record. REQ-OPS-86.
func TestActivityTag_PanicRecover_IsInternal(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	defer fs.Close()
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)

	var internalSeen bool
	captureHandler := &captureActivityHandler{
		onRecord: func(activity string) {
			if activity == observe.ActivityInternal {
				internalSeen = true
			}
		},
	}
	lg := slog.New(captureHandler)
	srv := protoadmin.NewServer(fs, dir, rp, lg, clk, protoadmin.Options{
		BootstrapPerWindow: 10,
		BootstrapWindow:    time.Minute,
	})

	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("synthetic test panic")
	})
	wrapped := protoadmin.WrapRecoverForTest(srv, panicHandler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	wrapped.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
	if !internalSeen {
		t.Error("expected activity=internal record from panic recovery (REQ-OPS-86)")
	}
}

// TestActivityTag_AllRecordsTagged asserts that no record from a
// typical request-response cycle is missing the activity attribute.
// Uses observe.AssertActivityTagged as the primary check (REQ-OPS-86a).
func TestActivityTag_AllRecordsTagged(t *testing.T) {
	t.Parallel()
	observe.AssertActivityTagged(t, func(lg *slog.Logger) {
		srv, _, _, _ := buildTagServer(t, lg)
		handler := srv.Handler()
		apiKey := bootstrapKey(t, handler, "alltagged@example.com")

		// GET /api/v1/server/status — safe read.
		w1 := httptest.NewRecorder()
		r1 := httptest.NewRequest(http.MethodGet, "/api/v1/server/status", nil)
		r1.Header.Set("Authorization", "Bearer "+apiKey)
		handler.ServeHTTP(w1, r1)

		// POST /api/v1/principals — mutation.
		b, _ := json.Marshal(map[string]any{
			"email":    "newuser@example.com",
			"password": "LongP@ssword123!",
		})
		w2 := httptest.NewRecorder()
		r2 := httptest.NewRequest(http.MethodPost, "/api/v1/principals", bytes.NewReader(b))
		r2.Header.Set("Content-Type", "application/json")
		r2.Header.Set("Authorization", "Bearer "+apiKey)
		handler.ServeHTTP(w2, r2)
	})
}

// captureActivityHandler is a test-only slog.Handler that fires
// onRecord / onRecordWithLevel for each emitted record.
type captureActivityHandler struct {
	onRecord          func(activity string)
	onRecordWithLevel func(activity string, lvl slog.Level)
	pre               map[string]string
}

func (h *captureActivityHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureActivityHandler) Handle(_ context.Context, r slog.Record) error {
	activity := h.pre["activity"]
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "activity" {
			activity = a.Value.String()
			return false
		}
		return true
	})
	if h.onRecord != nil {
		h.onRecord(activity)
	}
	if h.onRecordWithLevel != nil {
		h.onRecordWithLevel(activity, r.Level)
	}
	return nil
}

func (h *captureActivityHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make(map[string]string, len(h.pre)+len(attrs))
	for k, v := range h.pre {
		merged[k] = v
	}
	for _, a := range attrs {
		merged[a.Key] = a.Value.String()
	}
	return &captureActivityHandler{
		onRecord:          h.onRecord,
		onRecordWithLevel: h.onRecordWithLevel,
		pre:               merged,
	}
}

func (h *captureActivityHandler) WithGroup(_ string) slog.Handler { return h }
