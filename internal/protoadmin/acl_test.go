package protoadmin_test

// acl_test.go covers the admin REST mailbox ACL endpoints:
//
//   GET    /api/v1/principals/{pid}/mailboxes
//   GET    /api/v1/principals/{pid}/mailboxes/{mailbox}/acl
//   PUT    /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//   DELETE /api/v1/principals/{pid}/mailboxes/{mailbox}/acl/{grantee}
//
// The test matrix covers:
//   - PUT round-trips through GET (verify stored rights).
//   - PUT same grantee twice (upsert semantics: second write wins).
//   - DELETE removes; subsequent GET no longer contains the row.
//   - GET on non-existent mailbox returns 404.
//   - PUT with unknown rights letter returns 400.
//   - PUT with duplicate rights letter returns 400.
//   - GET/PUT/DELETE with missing auth returns 401.
//   - GET/PUT/DELETE with non-admin key returns 403.
//   - GET mailbox listing returns the principal's mailboxes.
//   - DELETE on absent ACL row returns 404.
//
// Both backends are exercised: SQLite always; Postgres when HEROLD_PG_DSN
// is set.  The dual-backend helper at the bottom of this file mirrors the
// pattern used in internal/storepg/storepg_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// aclTestHarness is a self-contained harness for ACL endpoint tests.
type aclTestHarness struct {
	t        *testing.T
	fs       store.Store
	hs       *httptest.Server
	client   *http.Client
	adminKey string
}

// newACLHarness creates a test harness backed by the given store.
func newACLHarness(t *testing.T, fs store.Store) *aclTestHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	opts := protoadmin.Options{
		BootstrapPerWindow:      100,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 10000,
	}
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, opts)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	res, body := aclHTTP(t, hs.Client(), hs.URL, "POST", "/api/v1/bootstrap", "",
		map[string]any{"email": "admin@acl.test"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d: %s", res.StatusCode, body)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(body, &boot); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	return &aclTestHarness{
		t:        t,
		fs:       fs,
		hs:       hs,
		client:   hs.Client(),
		adminKey: boot.InitialAPIKey,
	}
}

// openSQLiteStore opens a fresh in-process SQLite store suitable for testing.
func openSQLiteStore(t *testing.T) store.Store {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}

// openPGStore opens a Postgres-backed store using HEROLD_PG_DSN, or
// skips the test when the DSN is not set.
func openPGStore(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres lane")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs, err := storepg.Open(context.Background(), dsn, filepath.Join(t.TempDir(), "blobs"), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v (skipping Postgres lane)", err)
	}
	// Truncate all rows so this test run starts from a clean state.
	if tr, ok := fs.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = fs.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}

// aclHTTP issues one HTTP request and returns the response + body bytes.
func aclHTTP(t *testing.T, c *http.Client, base, method, path, key string, body any) (*http.Response, []byte) {
	t.Helper()
	return dkimHTTP(t, c, base, method, path, key, body)
}

func (h *aclTestHarness) do(method, path string, body any) (*http.Response, []byte) {
	return aclHTTP(h.t, h.client, h.hs.URL, method, path, h.adminKey, body)
}

func (h *aclTestHarness) doAs(key, method, path string, body any) (*http.Response, []byte) {
	return aclHTTP(h.t, h.client, h.hs.URL, method, path, key, body)
}

// createPrincipalACL creates a non-admin principal, returning its numeric ID.
func (h *aclTestHarness) createPrincipalACL(email string) uint64 {
	h.t.Helper()
	res, buf := h.do("POST", "/api/v1/principals", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("createPrincipal %s: %d: %s", email, res.StatusCode, buf)
	}
	var p struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(buf, &p); err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	return p.ID
}

// listMailboxes calls GET /api/v1/principals/{pid}/mailboxes and returns the
// items array.
func (h *aclTestHarness) listMailboxes(pid uint64) []struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
} {
	h.t.Helper()
	res, buf := h.do("GET", fmt.Sprintf("/api/v1/principals/%d/mailboxes", pid), nil)
	if res.StatusCode != http.StatusOK {
		h.t.Fatalf("listMailboxes pid=%d: %d: %s", pid, res.StatusCode, buf)
	}
	var page struct {
		Items []struct {
			ID   uint64 `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(buf, &page); err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	return page.Items
}

// findMailboxByName returns the ID of the named mailbox among items, or
// fails the test if not found.
func (h *aclTestHarness) findMailboxByName(
	items []struct {
		ID   uint64 `json:"id"`
		Name string `json:"name"`
	},
	name string,
) uint64 {
	h.t.Helper()
	for _, mb := range items {
		if mb.Name == name {
			return mb.ID
		}
	}
	h.t.Fatalf("mailbox %q not found in listing", name)
	return 0
}

// getACL calls GET .../acl and returns the rows.
func (h *aclTestHarness) getACL(ownerPID, mailboxID uint64) []struct {
	GranteePrincipalID uint64 `json:"grantee_principal_id"`
	Rights             string `json:"rights"`
} {
	h.t.Helper()
	path := fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl", ownerPID, mailboxID)
	res, buf := h.do("GET", path, nil)
	if res.StatusCode != http.StatusOK {
		h.t.Fatalf("getACL: %d: %s", res.StatusCode, buf)
	}
	var rows []struct {
		GranteePrincipalID uint64 `json:"grantee_principal_id"`
		Rights             string `json:"rights"`
	}
	if err := json.Unmarshal(buf, &rows); err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	return rows
}

// putACL calls PUT .../acl/{grantee} and returns the status code.
func (h *aclTestHarness) putACL(ownerPID, mailboxID, granteePID uint64, rights string) int {
	h.t.Helper()
	path := fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", ownerPID, mailboxID, granteePID)
	res, _ := h.do("PUT", path, map[string]any{"rights": rights})
	return res.StatusCode
}

// deleteACL calls DELETE .../acl/{grantee} and returns the status code.
func (h *aclTestHarness) deleteACL(ownerPID, mailboxID, granteePID uint64) int {
	h.t.Helper()
	path := fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", ownerPID, mailboxID, granteePID)
	res, _ := h.do("DELETE", path, nil)
	return res.StatusCode
}

// runACLTests is the shared table-driven test body run against both backends.
func runACLTests(t *testing.T, fs store.Store) {
	t.Helper()
	h := newACLHarness(t, fs)

	// Seed two principals: alice (owner of the mailbox) and bob (grantee).
	alicePID := h.createPrincipalACL("alice@acl.test")
	bobPID := h.createPrincipalACL("bob@acl.test")

	// --- listing endpoint ------------------------------------------------

	t.Run("ListMailboxes_ReturnsMailboxes", func(t *testing.T) {
		items := h.listMailboxes(alicePID)
		if len(items) == 0 {
			t.Fatalf("no mailboxes returned for alice")
		}
		found := false
		for _, mb := range items {
			if mb.Name == "INBOX" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("INBOX not in mailbox listing: %+v", items)
		}
	})

	t.Run("ListMailboxes_UnknownPrincipal_404", func(t *testing.T) {
		res, _ := h.do("GET", "/api/v1/principals/99999/mailboxes", nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown principal: got %d, want 404", res.StatusCode)
		}
	})

	// Resolve alice's INBOX id for subsequent ACL tests.
	mbs := h.listMailboxes(alicePID)
	inboxID := h.findMailboxByName(mbs, "INBOX")

	// --- PUT round-trip through GET --------------------------------------

	t.Run("PutAndGet_RoundTrip", func(t *testing.T) {
		if code := h.putACL(alicePID, inboxID, bobPID, "lr"); code != http.StatusOK {
			t.Fatalf("PUT: got %d, want 200", code)
		}
		rows := h.getACL(alicePID, inboxID)
		found := false
		for _, row := range rows {
			if row.GranteePrincipalID == bobPID {
				if row.Rights != "lr" {
					t.Fatalf("rights: got %q, want %q", row.Rights, "lr")
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("bob not in ACL rows: %+v", rows)
		}
	})

	t.Run("Put_Upsert_SecondWriteWins", func(t *testing.T) {
		// First grant.
		if code := h.putACL(alicePID, inboxID, bobPID, "lr"); code != http.StatusOK {
			t.Fatalf("first PUT: got %d, want 200", code)
		}
		// Second grant with different rights.
		if code := h.putACL(alicePID, inboxID, bobPID, "lrswipkxtea"); code != http.StatusOK {
			t.Fatalf("second PUT: got %d, want 200", code)
		}
		rows := h.getACL(alicePID, inboxID)
		for _, row := range rows {
			if row.GranteePrincipalID == bobPID {
				if row.Rights != "lrswipkxtea" {
					t.Fatalf("after upsert: rights = %q, want %q", row.Rights, "lrswipkxtea")
				}
				return
			}
		}
		t.Fatalf("bob not found in rows after upsert: %+v", rows)
	})

	// --- DELETE ----------------------------------------------------------

	t.Run("Delete_RemovesRow", func(t *testing.T) {
		// Ensure the row exists.
		if code := h.putACL(alicePID, inboxID, bobPID, "lr"); code != http.StatusOK {
			t.Fatalf("PUT before delete: got %d, want 200", code)
		}
		if code := h.deleteACL(alicePID, inboxID, bobPID); code != http.StatusNoContent {
			t.Fatalf("DELETE: got %d, want 204", code)
		}
		// GET must no longer contain bob.
		rows := h.getACL(alicePID, inboxID)
		for _, row := range rows {
			if row.GranteePrincipalID == bobPID {
				t.Fatalf("bob still in ACL after DELETE: %+v", rows)
			}
		}
	})

	t.Run("Delete_AbsentRow_404", func(t *testing.T) {
		// Remove any existing row first.
		_ = h.deleteACL(alicePID, inboxID, bobPID)
		// Now delete a non-existent row.
		if code := h.deleteACL(alicePID, inboxID, bobPID); code != http.StatusNotFound {
			t.Fatalf("DELETE absent: got %d, want 404", code)
		}
	})

	// --- Error cases -----------------------------------------------------

	t.Run("Get_NonExistentMailbox_404", func(t *testing.T) {
		res, _ := h.do("GET", fmt.Sprintf("/api/v1/principals/%d/mailboxes/99999/acl", alicePID), nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("non-existent mailbox: got %d, want 404", res.StatusCode)
		}
	})

	t.Run("Put_UnknownRightsLetter_400", func(t *testing.T) {
		path := fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", alicePID, inboxID, bobPID)
		res, buf := h.do("PUT", path, map[string]any{"rights": "lrZ"})
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("unknown letter: got %d: %s; want 400", res.StatusCode, buf)
		}
		if !strings.Contains(string(buf), "invalid_rights") {
			t.Fatalf("body should mention invalid_rights: %s", buf)
		}
	})

	t.Run("Put_DuplicateRightsLetter_400", func(t *testing.T) {
		path := fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", alicePID, inboxID, bobPID)
		res, buf := h.do("PUT", path, map[string]any{"rights": "lrr"})
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("duplicate letter: got %d: %s; want 400", res.StatusCode, buf)
		}
	})

	// --- Auth checks (independent of backend) ----------------------------

	t.Run("MissingAuth_401", func(t *testing.T) {
		paths := []struct {
			method string
			path   string
			body   any
		}{
			{"GET", fmt.Sprintf("/api/v1/principals/%d/mailboxes", alicePID), nil},
			{"GET", fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl", alicePID, inboxID), nil},
			{"PUT", fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", alicePID, inboxID, bobPID), map[string]any{"rights": "lr"}},
			{"DELETE", fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", alicePID, inboxID, bobPID), nil},
		}
		for _, tc := range paths {
			res, _ := h.doAs("", tc.method, tc.path, tc.body)
			if res.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d, want 401", tc.method, tc.path, res.StatusCode)
			}
		}
	})

	t.Run("NonAdminKey_403", func(t *testing.T) {
		// Create a non-admin principal and mint an API key for it.
		nonAdminPID := h.createPrincipalACL("nonadmin@acl.test")
		res, buf := h.do("POST", fmt.Sprintf("/api/v1/principals/%d/api-keys", nonAdminPID),
			map[string]any{"label": "nonadmin-key"})
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("create non-admin key: %d: %s", res.StatusCode, buf)
		}
		var keyOut struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(buf, &keyOut); err != nil {
			t.Fatalf("decode key: %v", err)
		}
		nonAdminKey := keyOut.Key

		paths := []struct {
			method string
			path   string
			body   any
		}{
			{"GET", fmt.Sprintf("/api/v1/principals/%d/mailboxes", alicePID), nil},
			{"GET", fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl", alicePID, inboxID), nil},
			{"PUT", fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", alicePID, inboxID, bobPID), map[string]any{"rights": "lr"}},
			{"DELETE", fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d", alicePID, inboxID, bobPID), nil},
		}
		for _, tc := range paths {
			res, _ := h.doAs(nonAdminKey, tc.method, tc.path, tc.body)
			if res.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, res.StatusCode)
			}
		}
	})

	// --- Mailbox ownership boundary ---------------------------------------

	t.Run("Get_MailboxOwnedByOtherPrincipal_404", func(t *testing.T) {
		// Alice's INBOX must not be visible under bob's principal path.
		res, _ := h.do("GET",
			fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl", bobPID, inboxID), nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("cross-principal mailbox: got %d, want 404", res.StatusCode)
		}
	})
}

// TestMailboxACL_SQLite runs the full matrix against an in-process SQLite
// backend.
func TestMailboxACL_SQLite(t *testing.T) {
	fs := openSQLiteStore(t)
	runACLTests(t, fs)
}

// TestMailboxACL_Postgres runs the full matrix against a real Postgres
// backend.  The test is skipped when HEROLD_PG_DSN is not set.
func TestMailboxACL_Postgres(t *testing.T) {
	fs := openPGStore(t)
	runACLTests(t, fs)
}

// TestACLCodec_ParseAndFormat exercises the rights-string codec in isolation.
func TestACLCodec_ParseAndFormat(t *testing.T) {
	// Tested indirectly via the PUT endpoint; this table hits the parser
	// directly through the HTTP surface to confirm the 400 codes and the
	// round-trip invariant without a full harness.
	cases := []struct {
		rights  string
		wantErr bool
	}{
		{"", false},            // empty is valid (zero rights)
		{"lr", false},          // partial set
		{"lrswipkxtea", false}, // all rights
		{"a", false},           // single letter
		{"lrZ", true},          // unknown letter
		{"lrr", true},          // duplicate letter
		{"lrswipkxteaZ", true}, // full + unknown
		{"LRSWIPKXTEA", true},  // uppercase not accepted
	}
	fs := openSQLiteStore(t)
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	opts := protoadmin.Options{
		BootstrapPerWindow:      100,
		BootstrapWindow:         time.Minute,
		RequestsPerMinutePerKey: 10000,
	}
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, opts)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Bootstrap an admin.
	res, body := aclHTTP(t, hs.Client(), hs.URL, "POST", "/api/v1/bootstrap", "",
		map[string]any{"email": "codectest@acl.test"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: %d: %s", res.StatusCode, body)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(body, &boot); err != nil {
		t.Fatalf("decode: %v", err)
	}
	adminKey := boot.InitialAPIKey

	ch := &aclTestHarness{t: t, fs: fs, hs: hs, client: hs.Client(), adminKey: adminKey}

	// Create owner principal.
	ownerPID := ch.createPrincipalACL("owner@acl.test")
	granteePID := ch.createPrincipalACL("grantee@acl.test")

	mbs := ch.listMailboxes(ownerPID)
	inboxID := ch.findMailboxByName(mbs, "INBOX")

	for _, tc := range cases {
		tc := tc
		t.Run("rights="+tc.rights, func(t *testing.T) {
			path := fmt.Sprintf("/api/v1/principals/%d/mailboxes/%d/acl/%d",
				ownerPID, inboxID, granteePID)
			res, buf := aclHTTP(t, hs.Client(), hs.URL, "PUT", path, adminKey,
				map[string]any{"rights": tc.rights})
			if tc.wantErr {
				if res.StatusCode != http.StatusBadRequest {
					t.Fatalf("rights=%q: got %d, want 400; body=%s", tc.rights, res.StatusCode, buf)
				}
			} else {
				if res.StatusCode != http.StatusOK {
					t.Fatalf("rights=%q: got %d, want 200; body=%s", tc.rights, res.StatusCode, buf)
				}
				// Round-trip: GET must reflect the same rights string.
				rows := ch.getACL(ownerPID, inboxID)
				for _, row := range rows {
					if row.GranteePrincipalID == granteePID {
						if row.Rights != tc.rights {
							t.Fatalf("rights=%q: round-trip got %q", tc.rights, row.Rights)
						}
						return
					}
				}
				if tc.rights == "" {
					// Zero rights: row may exist with empty rights or
					// may not appear; either is acceptable.
					return
				}
				t.Fatalf("grantee not in ACL after PUT rights=%q", tc.rights)
			}
		})
	}
}
