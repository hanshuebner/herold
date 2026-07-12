// Tests for the REQ-AUTH-44 admin-grant gate (issue #12 slice 5):
//
//   PATCH /api/v1/principals/{id}  flags = [..., "admin", ...]
//
// MUST refuse when the target principal has no TOTP enrolled, even if
// the caller is itself a verified admin. Without this gate a logged-in
// admin could promote any password-only account to admin and that
// account would then trip slice 4's login gate — but only AFTER the
// privilege escalation was already accepted by the store. The grant
// itself has to refuse, not just the subsequent login.

package protoadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestPatchPrincipal_GrantAdmin_RefusedWithoutTOTP asserts the slice 5
// gate: a PATCH that would add the admin flag is refused when the
// target principal has FlagTOTPEnabled clear.
func TestPatchPrincipal_GrantAdmin_RefusedWithoutTOTP(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	// Create a non-admin principal with no TOTP. The bootstrap admin
	// itself has admin (and no TOTP — slice 6 will close that loop),
	// so create a separate target.
	targetID := h.createPrincipal(adminKey, "victim@example.com")

	res, buf := h.doRequest("PATCH", fmt.Sprintf("/api/v1/principals/%d", targetID), adminKey,
		map[string]any{"flags": []string{"admin"}})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("PATCH grant-admin without TOTP: status=%d body=%s, want 409", res.StatusCode, buf)
	}
	var detail map[string]any
	if err := json.Unmarshal(buf, &detail); err != nil {
		t.Fatalf("unmarshal problem detail: %v body=%s", err, buf)
	}
	if detail["totp_enrollment_required"] != true {
		t.Errorf("totp_enrollment_required not true: %v", detail)
	}
	// The target principal MUST NOT have been mutated.
	p, err := h.h.Store.Meta().GetPrincipalByID(context.Background(), store.PrincipalID(targetID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if p.Flags.Has(store.PrincipalFlagAdmin) {
		t.Errorf("target principal MUST not carry admin flag after refused grant; flags=%v", p.Flags)
	}
}

// TestPatchPrincipal_GrantAdmin_AcceptedWithTOTP is the positive twin:
// once the target principal has confirmed TOTP enrollment, the grant
// succeeds.
func TestPatchPrincipal_GrantAdmin_AcceptedWithTOTP(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	targetID := h.createPrincipal(adminKey, "promoteme@example.com")

	// Enroll + confirm TOTP for the target principal directly through
	// the directory layer. The harness's directory is the same one the
	// server wires into its handlers.
	ctx := context.Background()
	secret, _, err := h.dir.EnrollTOTP(ctx, store.PrincipalID(targetID))
	if err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	code, err := otpGenerateCode(secret, h.clk.Now())
	if err != nil {
		t.Fatalf("otpGenerateCode: %v", err)
	}
	if err := h.dir.ConfirmTOTP(ctx, store.PrincipalID(targetID), code); err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}

	res, buf := h.doRequest("PATCH", fmt.Sprintf("/api/v1/principals/%d", targetID), adminKey,
		map[string]any{"flags": []string{"admin"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH grant-admin with TOTP: status=%d body=%s, want 200", res.StatusCode, buf)
	}

	p, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(targetID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	if !p.Flags.Has(store.PrincipalFlagAdmin) {
		t.Errorf("target principal should carry admin flag after accepted grant; flags=%v", p.Flags)
	}
	if !p.Flags.Has(store.PrincipalFlagTOTPEnabled) {
		t.Errorf("target principal should still carry TOTPEnabled; flags=%v", p.Flags)
	}
}

// TestPatchPrincipal_PatchNonAdminFlags_NotGated asserts that a PATCH
// that does NOT touch the admin flag remains unaffected by the slice 5
// gate, even when the target has no TOTP.
func TestPatchPrincipal_PatchNonAdminFlags_NotGated(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	targetID := h.createPrincipal(adminKey, "no-totp@example.com")

	res, buf := h.doRequest("PATCH", fmt.Sprintf("/api/v1/principals/%d", targetID), adminKey,
		map[string]any{"flags": []string{"ignore_download_limits"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH non-admin flag: status=%d body=%s, want 200", res.StatusCode, buf)
	}
}

// TestPatchPrincipal_PreservesSuperAdmin guards against a data-loss
// regression found while fixing the admin SPA's principal-detail page
// (re #218): principalFlagsFromStrings deliberately never sets
// SuperAdmin from caller-supplied flag names (it is granted only
// through the dedicated operator-scope endpoints, REQ-ADM-307), so
// unless the handler explicitly re-applies the target's existing
// SuperAdmin bit, an ordinary flags PATCH that only touches an
// unrelated flag (e.g. ignore_download_limits) would silently demote
// an existing super-admin the first time anyone saved their profile.
func TestPatchPrincipal_PreservesSuperAdmin(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@example.com")
	targetID := h.createPrincipal(adminKey, "superadmin-target@example.com")

	ctx := context.Background()
	target, err := h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(targetID))
	if err != nil {
		t.Fatalf("GetPrincipalByID: %v", err)
	}
	// Grant SuperAdmin directly via the store, bypassing the REST layer
	// (which -- correctly -- refuses to let PATCH set it).
	target.Flags |= store.PrincipalFlagSuperAdmin
	if err := h.h.Store.Meta().UpdatePrincipal(ctx, target); err != nil {
		t.Fatalf("UpdatePrincipal grant super-admin: %v", err)
	}

	// PATCH a routine, non-admin flag -- the shape the admin SPA's own
	// profile form sends when toggling e.g. "Ignore download limits".
	res, buf := h.doRequest("PATCH", fmt.Sprintf("/api/v1/principals/%d", targetID), adminKey,
		map[string]any{"flags": []string{"ignore_download_limits"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH flags: status=%d body=%s, want 200", res.StatusCode, buf)
	}

	target, err = h.h.Store.Meta().GetPrincipalByID(ctx, store.PrincipalID(targetID))
	if err != nil {
		t.Fatalf("GetPrincipalByID after PATCH: %v", err)
	}
	if !target.Flags.Has(store.PrincipalFlagSuperAdmin) {
		t.Errorf("PATCH flags must not strip SuperAdmin; flags=%v", target.Flags)
	}
	if !target.Flags.Has(store.PrincipalFlagIgnoreDownloadLimits) {
		t.Errorf("PATCH flags must set the newly toggled flag; flags=%v", target.Flags)
	}
}
