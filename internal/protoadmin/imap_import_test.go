package protoadmin_test

// imap_import_test.go covers the IMAP import admin REST endpoints:
//
//   GET    /api/v1/principals/{pid}/imap-imports
//   POST   /api/v1/principals/{pid}/imap-imports
//   PATCH  /api/v1/principals/{pid}/imap-imports/{aid}
//   DELETE /api/v1/principals/{pid}/imap-imports/{aid}
//   GET    /api/v1/imap-imports/status
//
// REQ-IMAP-IMP-60, REQ-IMAP-IMP-65, REQ-ADM-300.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/secrets"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/testharness"
)

// imapImportDataKey is the 32-byte AEAD key used to seal credentials in the
// IMAP import tests. Different from testDataKey (submission tests) to avoid
// cross-contamination.
var imapImportDataKey = []byte("imap-import-test-data-key-32byt!")

// fakeIMAPNow is the reference instant shared by the imap import test clock.
var fakeIMAPNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// fakeStatusProvider is a test double for protoadmin.IMAPImportStatusProvider.
type fakeStatusProvider struct {
	snap []protoadmin.IMAPImportWorkerStatus
}

func (f *fakeStatusProvider) Snapshot() []protoadmin.IMAPImportWorkerStatus {
	return f.snap
}

// imapHarness wraps the standard test harness for IMAP import tests.
type imapHarness struct {
	t         *testing.T
	fs        store.Store
	clk       *clock.FakeClock
	srv       *protoadmin.Server
	client    *http.Client
	baseURL   string
	adminKey  string
	adminPID  uint64
	workerPID uint64
	workerKey string
}

// newIMAPHarness creates the harness. It bootstraps an admin principal and
// creates a non-admin "worker" principal whose imap-import accounts are
// managed via the admin endpoints.
func newIMAPHarness(t *testing.T, statusProvider protoadmin.IMAPImportStatusProvider) *imapHarness {
	t.Helper()
	clk := clock.NewFake(fakeIMAPNow)
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		IMAPImportDataKey:       imapImportDataKey,
		IMAPImportStatus:        statusProvider,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")

	ih := &imapHarness{
		t: t, fs: fs, clk: clk, srv: srv, client: client, baseURL: base,
	}

	// Bootstrap the admin.
	adminPID, adminKey := bootstrapAdmin(t, ih, "admin@example.local")
	ih.adminPID = adminPID
	ih.adminKey = adminKey

	// Create the non-admin principal whose accounts we manage.
	ih.workerPID, ih.workerKey = createWorkerPrincipal(t, ih, "worker@example.local")

	return ih
}

// bootstrapAdmin calls POST /api/v1/bootstrap and returns (pid, key).
func bootstrapAdmin(t *testing.T, ih *imapHarness, email string) (uint64, string) {
	t.Helper()
	res, buf := ih.do("POST", "/api/v1/bootstrap", "", map[string]any{
		"email":        email,
		"display_name": "Test Admin",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		PrincipalID   uint64 `json:"principal_id"`
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("bootstrap unmarshal: %v", err)
	}
	return out.PrincipalID, out.InitialAPIKey
}

// createWorkerPrincipal creates a non-admin principal and returns (pid, key).
func createWorkerPrincipal(t *testing.T, ih *imapHarness, email string) (uint64, string) {
	t.Helper()
	res, buf := ih.do("POST", "/api/v1/principals", ih.adminKey, map[string]any{
		"email":    email,
		"password": "worker-pass-123",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create worker principal: %d: %s", res.StatusCode, buf)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		t.Fatalf("unmarshal principal: %v", err)
	}

	// Create an API key for the worker.
	res2, buf2 := ih.do("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", p.ID), ih.adminKey, map[string]any{
		"label": "worker-key",
		"scope": []string{"mail.send"},
	})
	if res2.StatusCode != http.StatusCreated {
		t.Fatalf("create worker api key: %d: %s", res2.StatusCode, buf2)
	}
	var k struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf2, &k); err != nil {
		t.Fatalf("unmarshal api key: %v", err)
	}
	return p.ID, k.Key
}

// do sends an HTTP request and returns (response, body).
func (ih *imapHarness) do(method, path, key string, body any) (*http.Response, []byte) {
	ih.t.Helper()
	h := &harness{t: ih.t, client: ih.client, baseURL: ih.baseURL}
	return h.doRequest(method, path, key, body)
}

// listPath returns the list path for the worker's imap-imports.
func (ih *imapHarness) listPath() string {
	return fmt.Sprintf("/api/v1/principals/%d/imap-imports", ih.workerPID)
}

// accountPath returns the path for a specific account under the worker principal.
func (ih *imapHarness) accountPath(aid string) string {
	return fmt.Sprintf("/api/v1/principals/%d/imap-imports/%s", ih.workerPID, aid)
}

// minimalCreateBody returns a valid create body.
func minimalCreateBody() map[string]any {
	return map[string]any{
		"account_name":     "My Gmail",
		"host":             "imap.gmail.com",
		"port":             993,
		"tls_mode":         "implicit",
		"username":         "user@gmail.com",
		"auth_method":      "password",
		"backfill_horizon": "30d",
		"credential":       "s3cr3t-p4ssw0rd",
	}
}

// ---- tests ---------------------------------------------------------------

// TestIMAPImport_ListEmpty verifies that an authenticated admin can list
// a principal's (empty) imap import accounts.
func TestIMAPImport_ListEmpty(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("GET", ih.listPath(), ih.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Items) != 0 {
		t.Fatalf("expected empty list, got %d items", len(out.Items))
	}
}

// TestIMAPImport_CreateBasic verifies the happy path of creating an account.
func TestIMAPImport_CreateBasic(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID          string `json:"id"`
		AccountName string `json:"account_name"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		TLSMode     string `json:"tls_mode"`
		AuthMethod  string `json:"auth_method"`
		State       string `json:"state"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID == "" {
		t.Fatalf("id must not be empty")
	}
	if out.AccountName != "My Gmail" {
		t.Fatalf("account_name = %q, want %q", out.AccountName, "My Gmail")
	}
	if out.Host != "imap.gmail.com" {
		t.Fatalf("host = %q, want %q", out.Host, "imap.gmail.com")
	}
	if out.Port != 993 {
		t.Fatalf("port = %d, want 993", out.Port)
	}
	if out.TLSMode != "implicit" {
		t.Fatalf("tls_mode = %q, want %q", out.TLSMode, "implicit")
	}
	if out.AuthMethod != "password" {
		t.Fatalf("auth_method = %q, want %q", out.AuthMethod, "password")
	}
	if out.State != "enabled" {
		t.Fatalf("state = %q, want %q", out.State, "enabled")
	}
}

// TestIMAPImport_CredentialSealed verifies that the stored credential_ct
// carries the v1: prefix and decrypts back to the original plaintext.
func TestIMAPImport_CredentialSealed(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Load the raw row from the store and inspect the sealed credential.
	row, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), out.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	ctStr := string(row.CredentialCT)
	if !strings.HasPrefix(ctStr, "v1:") {
		t.Fatalf("credential_ct must start with v1:, got %q", ctStr[:min(10, len(ctStr))])
	}
	plain, err := secrets.Open(imapImportDataKey, row.CredentialCT)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if string(plain) != "s3cr3t-p4ssw0rd" {
		t.Fatalf("decrypted credential = %q, want %q", plain, "s3cr3t-p4ssw0rd")
	}
}

// TestIMAPImport_ListHidesCredential verifies that list responses never
// contain the sealed credential or its plaintext.
func TestIMAPImport_ListHidesCredential(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}

	res2, buf2 := ih.do("GET", ih.listPath(), ih.adminKey, nil)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("list: %d: %s", res2.StatusCode, buf2)
	}
	listStr := string(buf2)
	for _, banned := range []string{"credential_ct", "s3cr3t-p4ssw0rd"} {
		if strings.Contains(listStr, banned) {
			t.Fatalf("response must not contain %q", banned)
		}
	}
	// has_credential should be true.
	if !strings.Contains(listStr, `"has_credential":true`) {
		t.Fatalf("list response should report has_credential:true; got: %s", listStr)
	}
	_ = res
}

// TestIMAPImport_HorizonRelative verifies that the "30d" horizon resolves
// to fakeIMAPNow - 30 days at create time.
func TestIMAPImport_HorizonRelative(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	row, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), out.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate == nil {
		t.Fatalf("BackfillFloorDate must not be nil for 30d horizon")
	}
	expected := fakeIMAPNow.Add(-30 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !row.BackfillFloorDate.Equal(expected) {
		t.Fatalf("BackfillFloorDate = %v, want %v", *row.BackfillFloorDate, expected)
	}
}

// TestIMAPImport_HorizonAll verifies that the "all" sentinel stores nil floor.
func TestIMAPImport_HorizonAll(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	body := minimalCreateBody()
	body["backfill_horizon"] = "all"
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		ID              string `json:"id"`
		BackfillHorizon string `json:"backfill_horizon"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.BackfillHorizon != "all" {
		t.Fatalf("backfill_horizon = %q, want %q", out.BackfillHorizon, "all")
	}
	row, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), out.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if row.BackfillFloorDate != nil {
		t.Fatalf("BackfillFloorDate must be nil for all, got %v", *row.BackfillFloorDate)
	}
}

// TestIMAPImport_UpdatePreservesCredentialWhenOmitted verifies that a PATCH
// without a credential field leaves the sealed credential_ct unchanged.
func TestIMAPImport_UpdatePreservesCredentialWhenOmitted(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rowBefore, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount before: %v", err)
	}
	ctBefore := append([]byte(nil), rowBefore.CredentialCT...)

	// Patch only the account_name.
	res2, buf2 := ih.do("PATCH", ih.accountPath(created.ID), ih.adminKey, map[string]any{
		"account_name": "Renamed Account",
	})
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d: %s", res2.StatusCode, buf2)
	}

	rowAfter, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount after: %v", err)
	}
	if string(rowAfter.CredentialCT) != string(ctBefore) {
		t.Fatalf("credential_ct changed unexpectedly after patch without credential")
	}
	if rowAfter.AccountName != "Renamed Account" {
		t.Fatalf("account_name = %q, want %q", rowAfter.AccountName, "Renamed Account")
	}
}

// TestIMAPImport_UpdateResealsCredentialWhenProvided verifies that supplying
// a new credential in the patch replaces the ciphertext.
func TestIMAPImport_UpdateResealsCredentialWhenProvided(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rowBefore, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount before: %v", err)
	}
	ctBefore := append([]byte(nil), rowBefore.CredentialCT...)

	res2, buf2 := ih.do("PATCH", ih.accountPath(created.ID), ih.adminKey, map[string]any{
		"credential": "new-password-456",
	})
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d: %s", res2.StatusCode, buf2)
	}

	rowAfter, err := ih.fs.Meta().GetIMAPImportAccount(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount after: %v", err)
	}
	if string(rowAfter.CredentialCT) == string(ctBefore) {
		t.Fatalf("credential_ct did not change after patch with new credential")
	}
	if !strings.HasPrefix(string(rowAfter.CredentialCT), "v1:") {
		t.Fatalf("new credential_ct missing v1: prefix")
	}
	plain, err := secrets.Open(imapImportDataKey, rowAfter.CredentialCT)
	if err != nil {
		t.Fatalf("secrets.Open new ct: %v", err)
	}
	if string(plain) != "new-password-456" {
		t.Fatalf("decrypted new credential = %q, want %q", plain, "new-password-456")
	}
}

// TestIMAPImport_Delete verifies that deleting an account makes it disappear
// from subsequent list responses.
func TestIMAPImport_Delete(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	res2, buf2 := ih.do("DELETE", ih.accountPath(created.ID), ih.adminKey, nil)
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d: %s", res2.StatusCode, buf2)
	}

	// List should be empty again.
	res3, buf3 := ih.do("GET", ih.listPath(), ih.adminKey, nil)
	if res3.StatusCode != http.StatusOK {
		t.Fatalf("list after delete: %d: %s", res3.StatusCode, buf3)
	}
	var listOut struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(buf3, &listOut); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(listOut.Items) != 0 {
		t.Fatalf("expected empty list after delete, got %d items", len(listOut.Items))
	}
}

// TestIMAPImport_AidNotBelongingToPid verifies that a PATCH/DELETE using an
// account id that belongs to a different principal returns 404.
func TestIMAPImport_AidNotBelongingToPid(t *testing.T) {
	ih := newIMAPHarness(t, nil)

	// Create account under worker.
	res, buf := ih.do("POST", ih.listPath(), ih.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d: %s", res.StatusCode, buf)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Create a second principal.
	otherPID, _ := createWorkerPrincipal(t, ih, "other@example.local")

	// Try to PATCH using otherPID's path but the worker's account id.
	otherAccountPath := fmt.Sprintf("/api/v1/principals/%d/imap-imports/%s", otherPID, created.ID)
	res2, buf2 := ih.do("PATCH", otherAccountPath, ih.adminKey, map[string]any{
		"account_name": "hijack",
	})
	if res2.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-principal patch: want 404, got %d: %s", res2.StatusCode, buf2)
	}

	// Try to DELETE using otherPID's path but the worker's account id.
	res3, buf3 := ih.do("DELETE", otherAccountPath, ih.adminKey, nil)
	if res3.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-principal delete: want 404, got %d: %s", res3.StatusCode, buf3)
	}
}

// TestIMAPImport_ValidationErrors verifies that the create endpoint rejects
// requests with missing required fields or invalid field values.
func TestIMAPImport_ValidationErrors(t *testing.T) {
	ih := newIMAPHarness(t, nil)

	cases := []struct {
		name   string
		body   map[string]any
		wantSt int
	}{
		{
			name: "missing account_name",
			body: func() map[string]any {
				b := minimalCreateBody()
				delete(b, "account_name")
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
		{
			name: "missing host",
			body: func() map[string]any {
				b := minimalCreateBody()
				delete(b, "host")
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
		{
			name: "invalid tls_mode",
			body: func() map[string]any {
				b := minimalCreateBody()
				b["tls_mode"] = "none"
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
		{
			name: "invalid auth_method",
			body: func() map[string]any {
				b := minimalCreateBody()
				b["auth_method"] = "kerberos"
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
		{
			name: "invalid backfill_horizon",
			body: func() map[string]any {
				b := minimalCreateBody()
				b["backfill_horizon"] = "forever"
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
		{
			name: "missing credential",
			body: func() map[string]any {
				b := minimalCreateBody()
				delete(b, "credential")
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
		{
			name: "port out of range",
			body: func() map[string]any {
				b := minimalCreateBody()
				b["port"] = 99999
				return b
			}(),
			wantSt: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, buf := ih.do("POST", ih.listPath(), ih.adminKey, tc.body)
			if res.StatusCode != tc.wantSt {
				t.Fatalf("%s: want %d, got %d: %s", tc.name, tc.wantSt, res.StatusCode, buf)
			}
			// Should be a problem+json response.
			ct := res.Header.Get("Content-Type")
			if !strings.Contains(ct, "application/problem+json") {
				t.Fatalf("%s: Content-Type = %q, want application/problem+json", tc.name, ct)
			}
		})
	}
}

// TestIMAPImport_NoDataKeyReturns503 verifies that when IMAPImportDataKey
// is not configured, create/update return 503.
func TestIMAPImport_NoDataKeyReturns503(t *testing.T) {
	clk := clock.NewFake(fakeIMAPNow)
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	// Deliberately do NOT set IMAPImportDataKey.
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")

	ih2 := &imapHarness{t: t, fs: fs, clk: clk, srv: srv, client: client, baseURL: base}
	ih2.adminPID, ih2.adminKey = bootstrapAdmin(t, ih2, "nodatakey@example.local")
	ih2.workerPID, ih2.workerKey = createWorkerPrincipal(t, ih2, "w2@example.local")

	res, buf := ih2.do("POST", ih2.listPath(), ih2.adminKey, minimalCreateBody())
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", res.StatusCode, buf)
	}
}

// TestIMAPImport_AuthRequired verifies that unauthenticated requests get 401.
func TestIMAPImport_AuthRequired(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	paths := []struct {
		method string
		path   string
	}{
		{"GET", ih.listPath()},
		{"POST", ih.listPath()},
		{"GET", "/api/v1/imap-imports/status"},
	}
	for _, tc := range paths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res, _ := ih.do(tc.method, tc.path, "" /* no key */, nil)
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s: want 401, got %d", tc.method, tc.path, res.StatusCode)
			}
		})
	}
}

// TestIMAPImport_StatusNilProvider verifies that when no status provider is
// configured, GET /api/v1/imap-imports/status returns 200 with an empty list.
func TestIMAPImport_StatusNilProvider(t *testing.T) {
	ih := newIMAPHarness(t, nil /* nil provider */)
	res, buf := ih.do("GET", "/api/v1/imap-imports/status", ih.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Items) != 0 {
		t.Fatalf("expected empty items, got %d", len(out.Items))
	}
}

// TestIMAPImport_StatusWithProvider verifies that GET /api/v1/imap-imports/status
// returns the injected snapshot.
func TestIMAPImport_StatusWithProvider(t *testing.T) {
	now := fakeIMAPNow
	snap := []protoadmin.IMAPImportWorkerStatus{
		{
			AccountID:       "acc-1",
			PrincipalID:     "42",
			AccountName:     "My Gmail",
			Host:            "imap.gmail.com",
			Phase:           "idle",
			ConnMode:        "dual",
			Connected:       true,
			PhaseSince:      now,
			MessagesFetched: 100,
			FlagsPropagated: 5,
		},
		{
			AccountID:           "acc-2",
			PrincipalID:         "43",
			AccountName:         "Fastmail",
			Host:                "imap.fastmail.com",
			Phase:               "backoff",
			ConnMode:            "single",
			Connected:           false,
			PhaseSince:          now.Add(-time.Minute),
			ConsecutiveFailures: 3,
			LastError:           "connection refused",
		},
	}
	provider := &fakeStatusProvider{snap: snap}
	ih := newIMAPHarness(t, provider)

	res, buf := ih.do("GET", "/api/v1/imap-imports/status", ih.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Items []struct {
			AccountID           string `json:"account_id"`
			Phase               string `json:"phase"`
			ConnMode            string `json:"conn_mode"`
			Connected           bool   `json:"connected"`
			MessagesFetched     int64  `json:"messages_fetched"`
			ConsecutiveFailures int    `json:"consecutive_failures"`
			LastError           string `json:"last_error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out.Items))
	}
	// First item: acc-1
	if out.Items[0].AccountID != "acc-1" {
		t.Fatalf("items[0].account_id = %q, want acc-1", out.Items[0].AccountID)
	}
	if out.Items[0].Phase != "idle" {
		t.Fatalf("items[0].phase = %q, want idle", out.Items[0].Phase)
	}
	if out.Items[0].ConnMode != "dual" {
		t.Fatalf("items[0].conn_mode = %q, want dual", out.Items[0].ConnMode)
	}
	if !out.Items[0].Connected {
		t.Fatalf("items[0].connected must be true")
	}
	if out.Items[0].MessagesFetched != 100 {
		t.Fatalf("items[0].messages_fetched = %d, want 100", out.Items[0].MessagesFetched)
	}
	// Second item: acc-2
	if out.Items[1].AccountID != "acc-2" {
		t.Fatalf("items[1].account_id = %q, want acc-2", out.Items[1].AccountID)
	}
	if out.Items[1].Phase != "backoff" {
		t.Fatalf("items[1].phase = %q, want backoff", out.Items[1].Phase)
	}
	if out.Items[1].Connected {
		t.Fatalf("items[1].connected must be false")
	}
	if out.Items[1].ConsecutiveFailures != 3 {
		t.Fatalf("items[1].consecutive_failures = %d, want 3", out.Items[1].ConsecutiveFailures)
	}
	if out.Items[1].LastError != "connection refused" {
		t.Fatalf("items[1].last_error = %q, want connection refused", out.Items[1].LastError)
	}
}

// TestIMAPImport_DeleteNotFound verifies that deleting a nonexistent account
// returns 404.
func TestIMAPImport_DeleteNotFound(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("DELETE", ih.accountPath("nonexistent-id"), ih.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("delete nonexistent: want 404, got %d: %s", res.StatusCode, buf)
	}
}

// TestIMAPImport_NonAdminCannotList verifies that a non-admin user (worker)
// is denied access to the admin-only endpoints.
func TestIMAPImport_NonAdminCannotList(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("GET", ih.listPath(), ih.workerKey, nil)
	// Non-admin with mail.send scope -> 403 (scope gate).
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin list: want 403, got %d: %s", res.StatusCode, buf)
	}
}

// TestIMAPImport_UnknownPrincipalReturns404 verifies that referencing a
// non-existent principal PID returns 404 (not a 500 or misrouted response).
func TestIMAPImport_UnknownPrincipalReturns404(t *testing.T) {
	ih := newIMAPHarness(t, nil)
	res, buf := ih.do("GET", "/api/v1/principals/99999/imap-imports", ih.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown pid: want 404, got %d: %s", res.StatusCode, buf)
	}
}
