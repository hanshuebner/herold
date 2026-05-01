package protoadmin_test

// telemetry_clientlog_test.go covers PUT /api/v1/me/clientlog/telemetry_enabled
// (REQ-OPS-208, REQ-CLOG-06, REQ-ADM-300).
//
// Test matrix:
//   - unauthenticated caller: 401
//   - bearer-key caller: flip true/false then null, 204 each time
//   - audit log: entries carry correct before/after metadata
//   - session-cookie caller: PUT updates live session row immediately
//   - unknown JSON field: 400 (DisallowUnknownFields)

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

const telemetryPath = "/api/v1/me/clientlog/telemetry_enabled"

// TestTelemetryEnabled_Unauthenticated_Returns401 asserts that a request
// with no credentials gets 401.
func TestTelemetryEnabled_Unauthenticated_Returns401(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res, _ := h.doRequest("PUT", telemetryPath, "", map[string]any{"enabled": true})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated PUT: status=%d, want 401", res.StatusCode)
	}
}

// TestTelemetryEnabled_BearerKey_FlipsValue verifies that a Bearer-key
// caller can flip telemetry on/off/null and each call returns 204. The
// principal row is checked directly for persistence.
func TestTelemetryEnabled_BearerKey_FlipsValue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pid, key := h.bootstrap("telemetry-bearer@example.com")

	ctx := context.Background()

	// Flip to true.
	boolTrue := true
	res, body := h.doRequest("PUT", telemetryPath, key, map[string]any{"enabled": &boolTrue})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT true: status=%d body=%s, want 204", res.StatusCode, body)
	}
	p, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(pid))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if !directory.EffectiveTelemetry(p, false) {
		t.Error("after PUT true: EffectiveTelemetry want true, got false")
	}

	// Flip to false.
	boolFalse := false
	res, body = h.doRequest("PUT", telemetryPath, key, map[string]any{"enabled": &boolFalse})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT false: status=%d body=%s, want 204", res.StatusCode, body)
	}
	p, err = h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(pid))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if directory.EffectiveTelemetry(p, true) {
		t.Error("after PUT false: EffectiveTelemetry want false, got true")
	}

	// Clear to null.
	res, body = h.doRequest("PUT", telemetryPath, key, map[string]any{"enabled": nil})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT null: status=%d body=%s, want 204", res.StatusCode, body)
	}
	p, err = h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(pid))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if p.ClientlogTelemetryEnabled != nil {
		t.Errorf("after PUT null: want nil, got %v", *p.ClientlogTelemetryEnabled)
	}
}

// TestTelemetryEnabled_AuditLog_CarriesBeforeAfter checks that successive
// calls to PUT telemetry_enabled record before/after metadata in the audit
// log per REQ-ADM-300.
func TestTelemetryEnabled_AuditLog_CarriesBeforeAfter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pid, key := h.bootstrap("telemetry-audit@example.com")

	ctx := context.Background()

	// First mutation: null -> true.
	boolTrue := true
	res, body := h.doRequest("PUT", telemetryPath, key, map[string]any{"enabled": &boolTrue})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT true: status=%d body=%s", res.StatusCode, body)
	}

	// Second mutation: true -> false.
	boolFalse := false
	res, body = h.doRequest("PUT", telemetryPath, key, map[string]any{"enabled": &boolFalse})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT false: status=%d body=%s", res.StatusCode, body)
	}

	// Third mutation: false -> null.
	res, body = h.doRequest("PUT", telemetryPath, key, map[string]any{"enabled": nil})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT null: status=%d body=%s", res.StatusCode, body)
	}

	entries, err := h.h.Store.Meta().ListAuditLog(ctx, store.AuditLogFilter{
		PrincipalID: store.PrincipalID(pid),
		Action:      "principal.clientlog_telemetry.set",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("want audit entries, got 0")
	}

	// Each mutation produces two audit entries: one from directory.SetTelemetry
	// (ActorSystem, carries before+after metadata) and one from the protoadmin
	// handler (ActorPrincipal, carries enabled+request_id).  Find the last
	// directory-layer entry (before+after present) for mutation 3 (false->null).
	var lastWithBeforeAfter *store.AuditLogEntry
	for i := range entries {
		e := &entries[i]
		if _, hasBefore := e.Metadata["before"]; hasBefore {
			lastWithBeforeAfter = e
		}
	}
	if lastWithBeforeAfter == nil {
		t.Fatalf("no audit entry carries 'before' field; entries=%+v", entries)
	}
	if before, ok := lastWithBeforeAfter.Metadata["before"]; !ok || before != "false" {
		t.Errorf("last directory entry before = %q (ok=%v), want 'false'", before, ok)
	}
	if after, ok := lastWithBeforeAfter.Metadata["after"]; !ok || after != "null" {
		t.Errorf("last directory entry after = %q (ok=%v), want 'null'", after, ok)
	}
}

// TestTelemetryEnabled_UnknownField_Returns400 verifies that DisallowUnknownFields
// is active: sending an unknown key in the body yields 400.
func TestTelemetryEnabled_UnknownField_Returns400(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, key := h.bootstrap("telemetry-unknown@example.com")

	res, body := h.doRequest("PUT", telemetryPath, key, map[string]any{
		"enabled": true,
		"bogus":   "field",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field: status=%d body=%s, want 400", res.StatusCode, body)
	}
}

// TestTelemetryEnabled_SessionCookie_UpdatesLiveRow verifies the
// session-descriptor projection: when the caller is authenticated via a
// session cookie, a successful PUT immediately updates the live session row
// so TelemetryGate.IsEnabled returns the new value without waiting for a
// session refresh (REQ-OPS-208).
//
// The CSRF cookie value equals the session row's PK (SessionID) because
// WriteSessionCookie sets the CSRF cookie to sess.CSRFToken, which is
// also used as the session row PK in handleLogin.
func TestTelemetryEnabled_SessionCookie_UpdatesLiveRow(t *testing.T) {
	t.Parallel()
	sh := newSessionHarness(t)
	email, password, _ := sh.bootstrapWithPassword("telemetry-session@example.com")

	// Login to establish a session row. The default effective flag is true
	// (defaultTelemetryEnabled = true in handleLogin).
	if code, _ := sh.doLogin(email, password, nil); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	csrf := sh.csrfToken()

	// The CSRF token IS the session row PK. Confirm initial state is true.
	ctx := context.Background()
	gate := directory.NewTelemetryGate(sh.h.Store.Meta())

	initialEnabled, err := gate.IsEnabled(ctx, csrf)
	if err != nil {
		t.Fatalf("IsEnabled before PUT: %v", err)
	}
	if !initialEnabled {
		t.Fatal("IsEnabled before PUT: want true (default), got false")
	}

	// PUT false via the cookie-authenticated client (needs CSRF header for
	// the mutation). The handler decodes the session cookie to extract the
	// CSRFToken and calls UpdateSessionTelemetry on that row.
	putCode, putBody := sh.doWithCookie("PUT", telemetryPath, map[string]any{"enabled": false}, csrf)
	if putCode != http.StatusNoContent {
		t.Fatalf("PUT false: status=%d body=%s, want 204", putCode, putBody)
	}

	// The session row must now reflect false without any login/logout cycle.
	gotAfter, err := gate.IsEnabled(ctx, csrf)
	if err != nil {
		t.Fatalf("IsEnabled after PUT: %v", err)
	}
	if gotAfter {
		t.Error("IsEnabled after PUT false: want false, got true")
	}

	// Flip back to true.
	putCode, putBody = sh.doWithCookie("PUT", telemetryPath, map[string]any{"enabled": true}, csrf)
	if putCode != http.StatusNoContent {
		t.Fatalf("PUT true: status=%d body=%s, want 204", putCode, putBody)
	}
	gotAfterTrue, err := gate.IsEnabled(ctx, csrf)
	if err != nil {
		t.Fatalf("IsEnabled after PUT true: %v", err)
	}
	if !gotAfterTrue {
		t.Error("IsEnabled after PUT true: want true, got false")
	}
}
