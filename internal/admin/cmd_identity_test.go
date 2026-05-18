package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// runIdentity drives the cobra root with --system-config <cfgPath> and
// returns stdout, stderr, and the resulting error.
func runIdentity(t *testing.T, cfgPath string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errBuf)
	all := append([]string{"--system-config", cfgPath}, args...)
	root.SetArgs(all)
	root.SetContext(context.Background())
	err := root.Execute()
	return out.String(), errBuf.String(), err
}

// openIdentityTestStore opens the SQLite store created by
// minimalConfigFixture so tests can seed identities before invoking the
// CLI. Mirrors openContactTestStore.
func openIdentityTestStore(t *testing.T, cfgPath string) store.Store {
	t.Helper()
	return openContactTestStore(t, cfgPath)
}

// seedIdentityRow inserts one persisted JMAP Identity row in the
// unverified state and returns its id.
func seedIdentityRow(t *testing.T, st store.Store, pid store.PrincipalID, identityID, email string) string {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID:          identityID,
		PrincipalID: pid,
		Name:        "Test Identity",
		Email:       email,
		MayDelete:   true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity(%s): %v", identityID, err)
	}
	return identityID
}

// seedPrincipalDirect inserts a principal directly into the store and
// returns the materialised row (CreatedAt populated).
func seedPrincipalDirect(t *testing.T, st store.Store, email string) store.Principal {
	t.Helper()
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%s): %v", email, err)
	}
	return p
}

// TestCLIIdentityVerify_Persistent: happy-path verify on a persisted
// unverified identity; verifies the store mutation and audit emission.
func TestCLIIdentityVerify_Persistent(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p := seedPrincipalDirect(t, st, "alice@test.local")
	id := seedIdentityRow(t, st, p.ID, "ident-1", "alice+alt@example.com")

	stdout, _, err := runIdentity(t, cfgPath, "identity", "verify", id)
	if err != nil {
		t.Fatalf("identity verify: %v", err)
	}
	if !strings.Contains(stdout, "verified "+id) || !strings.Contains(stdout, "alice+alt@example.com") {
		t.Fatalf("expected 'verified <id> <email>' in stdout, got: %q", stdout)
	}

	row, err := st.Meta().GetJMAPIdentity(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if row.VerifiedAtUs == 0 {
		t.Fatalf("VerifiedAtUs not set after verify CLI: %+v", row)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.verify.admin",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Subject != "identity:"+id {
		t.Errorf("audit subject: got %q, want identity:%s", e.Subject, id)
	}
	if e.Metadata["identity_id"] != id {
		t.Errorf("audit metadata identity_id: got %q, want %s", e.Metadata["identity_id"], id)
	}
	if e.Metadata["email"] != "alice+alt@example.com" {
		t.Errorf("audit metadata email: got %q", e.Metadata["email"])
	}
}

// TestCLIIdentityVerify_WithOperator: --operator resolves to
// ActorPrincipal/<pid> in the audit log.
func TestCLIIdentityVerify_WithOperator(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	op := seedPrincipalDirect(t, st, "admin@test.local")
	user := seedPrincipalDirect(t, st, "user@test.local")
	id := seedIdentityRow(t, st, user.ID, "ident-2", "user+alt@example.com")

	if _, _, err := runIdentity(t, cfgPath, "identity", "verify", id,
		"--operator", "admin@test.local"); err != nil {
		t.Fatalf("identity verify --operator: %v", err)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.verify.admin",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].ActorKind != store.ActorPrincipal {
		t.Errorf("ActorKind: got %v, want ActorPrincipal", entries[0].ActorKind)
	}
	wantID := strconv.FormatUint(uint64(op.ID), 10)
	if entries[0].ActorID != wantID {
		t.Errorf("ActorID: got %q, want %q", entries[0].ActorID, wantID)
	}
}

// TestCLIIdentityVerify_DefaultIsRejected: REQ-IDENT-02 — the
// synthesised default identity cannot be verified.
func TestCLIIdentityVerify_DefaultIsRejected(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	seedPrincipalDirect(t, st, "alice@test.local")

	_, _, err := runIdentity(t, cfgPath, "identity", "verify", "default")
	if err == nil {
		t.Fatal("expected error verifying 'default' identity, got nil")
	}
	if !strings.Contains(err.Error(), "verified by construction") {
		t.Fatalf("expected 'verified by construction' message, got: %v", err)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.verify.admin",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 audit entries on default-reject, got %d", len(entries))
	}
}

// TestCLIIdentityVerify_NotFound: clear operator-facing error for an
// unknown identity id.
func TestCLIIdentityVerify_NotFound(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	openIdentityTestStore(t, cfgPath)

	_, _, err := runIdentity(t, cfgPath, "identity", "verify", "ghost")
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got: %v", err)
	}
}

// TestCLIIdentityUnverify_Persistent: happy-path unverify reverts an
// already-verified identity row.
func TestCLIIdentityUnverify_Persistent(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p := seedPrincipalDirect(t, st, "bob@test.local")
	id := seedIdentityRow(t, st, p.ID, "ident-bob", "bob+alt@example.com")

	// Bring the row into the verified state first.
	if err := st.Meta().MarkIdentityVerified(context.Background(), id); err != nil {
		t.Fatalf("MarkIdentityVerified seed: %v", err)
	}
	row, err := st.Meta().GetJMAPIdentity(context.Background(), id)
	if err != nil {
		t.Fatalf("Get post-seed: %v", err)
	}
	if row.VerifiedAtUs == 0 {
		t.Fatalf("seed setup: expected VerifiedAtUs set")
	}

	stdout, _, err := runIdentity(t, cfgPath, "identity", "unverify", id)
	if err != nil {
		t.Fatalf("identity unverify: %v", err)
	}
	if !strings.Contains(stdout, "unverified "+id) {
		t.Fatalf("expected 'unverified <id>' in stdout, got: %q", stdout)
	}

	row, err = st.Meta().GetJMAPIdentity(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJMAPIdentity post-cli: %v", err)
	}
	if row.VerifiedAtUs != 0 {
		t.Fatalf("VerifiedAtUs not cleared after unverify CLI: %d", row.VerifiedAtUs)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.unverify",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Subject != "identity:"+id {
		t.Errorf("audit subject: got %q, want identity:%s", entries[0].Subject, id)
	}
}

// TestCLIIdentityUnverify_DefaultIsRejected: synthesised default
// identity cannot be unverified (REQ-IDENT-51 explicit refusal).
func TestCLIIdentityUnverify_DefaultIsRejected(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	seedPrincipalDirect(t, st, "alice@test.local")

	_, _, err := runIdentity(t, cfgPath, "identity", "unverify", "default")
	if err == nil {
		t.Fatal("expected error unverifying 'default' identity")
	}
	if !strings.Contains(err.Error(), "cannot be unverified") {
		t.Fatalf("expected 'cannot be unverified' message, got: %v", err)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.unverify",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 audit entries on default-reject, got %d", len(entries))
	}
}

// TestCLIIdentityList_NoFilter enumerates every principal's identities;
// each principal's synthesised default appears first in its group.
func TestCLIIdentityList_NoFilter(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p1 := seedPrincipalDirect(t, st, "alice@test.local")
	p2 := seedPrincipalDirect(t, st, "bob@test.local")
	seedIdentityRow(t, st, p1.ID, "alice-overlay-1", "alice+work@example.com")
	seedIdentityRow(t, st, p2.ID, "bob-overlay-1", "bob+ext@example.com")

	stdout, _, err := runIdentity(t, cfgPath, "identity", "list")
	if err != nil {
		t.Fatalf("identity list: %v", err)
	}

	for _, want := range []string{
		"PRINCIPAL-ID", "IDENTITY-ID", "EMAIL", "VERIFIED", "CREATED-AT", "NOTE",
		"default", "alice@test.local", "bob@test.local",
		"alice-overlay-1", "alice+work@example.com",
		"bob-overlay-1", "bob+ext@example.com",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("identity list output missing %q.\nstdout:\n%s", want, stdout)
		}
	}

	// Verify the synthesised default for principal 1 appears before its
	// overlay row.
	pid1 := strconv.FormatUint(uint64(p1.ID), 10)
	lines := strings.Split(stdout, "\n")
	defaultRow, overlayRow := -1, -1
	for i, ln := range lines {
		// Only consider lines starting with principal-1's id (whitespace-
		// delimited; tabwriter expands to spaces).
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, pid1+" ") && trimmed != pid1 {
			continue
		}
		if defaultRow == -1 && strings.Contains(ln, "default") {
			defaultRow = i
		} else if overlayRow == -1 && strings.Contains(ln, "alice-overlay-1") {
			overlayRow = i
		}
	}
	if defaultRow == -1 || overlayRow == -1 {
		t.Fatalf("could not locate default/overlay rows for principal %s; stdout:\n%s", pid1, stdout)
	}
	if defaultRow > overlayRow {
		t.Errorf("default row (%d) must precede overlay row (%d)", defaultRow, overlayRow)
	}
}

// TestCLIIdentityList_WithPrincipal scopes to one principal and asserts
// the other principal's identities are absent.
func TestCLIIdentityList_WithPrincipal(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p1 := seedPrincipalDirect(t, st, "alice@test.local")
	p2 := seedPrincipalDirect(t, st, "bob@test.local")
	seedIdentityRow(t, st, p1.ID, "alice-overlay-2", "alice+x@example.com")
	seedIdentityRow(t, st, p2.ID, "bob-overlay-2", "bob+x@example.com")

	stdout, _, err := runIdentity(t, cfgPath, "identity", "list",
		"--principal", "alice@test.local")
	if err != nil {
		t.Fatalf("identity list --principal: %v", err)
	}

	if !strings.Contains(stdout, "alice-overlay-2") {
		t.Errorf("expected alice's overlay in output: %s", stdout)
	}
	if !strings.Contains(stdout, "default") {
		t.Errorf("expected synthesised default in output: %s", stdout)
	}
	if strings.Contains(stdout, "bob-overlay-2") || strings.Contains(stdout, "bob+x@example.com") {
		t.Errorf("bob's identities leaked into alice-scoped list: %s", stdout)
	}
	if strings.Contains(stdout, "bob@test.local") {
		t.Errorf("bob's principal email leaked into alice-scoped list: %s", stdout)
	}
}

// TestCLIIdentityList_VerifiedColumn: "yes" vs "no" rendering on
// persisted rows.
func TestCLIIdentityList_VerifiedColumn(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p := seedPrincipalDirect(t, st, "carol@test.local")
	idVerified := seedIdentityRow(t, st, p.ID, "carol-v", "carol+v@example.com")
	idUnverified := seedIdentityRow(t, st, p.ID, "carol-u", "carol+u@example.com")
	if err := st.Meta().MarkIdentityVerified(context.Background(), idVerified); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}

	stdout, _, err := runIdentity(t, cfgPath, "identity", "list",
		"--principal", "carol@test.local")
	if err != nil {
		t.Fatalf("identity list: %v", err)
	}
	lines := strings.Split(stdout, "\n")
	var sawVerifiedYes, sawUnverifiedNo bool
	for _, ln := range lines {
		if strings.Contains(ln, idVerified) && strings.Contains(ln, "yes") {
			sawVerifiedYes = true
		}
		if strings.Contains(ln, idUnverified) && strings.Contains(ln, "no") {
			sawUnverifiedNo = true
		}
	}
	if !sawVerifiedYes {
		t.Errorf("expected verified row to show 'yes'; stdout:\n%s", stdout)
	}
	if !sawUnverifiedNo {
		t.Errorf("expected unverified row to show 'no'; stdout:\n%s", stdout)
	}
}

// parseReissueCode extracts the value following a "<label>:" prefix in
// the reissue-code command's "label: value" stdout lines.
func parseReissueCode(t *testing.T, stdout, label string) string {
	t.Helper()
	for _, ln := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(ln, label+":") {
			return strings.TrimSpace(strings.TrimPrefix(ln, label+":"))
		}
	}
	t.Fatalf("reissue-code output missing %q line; stdout:\n%s", label, stdout)
	return ""
}

// TestCLIIdentityReissueCode_Persistent: happy-path re-issue on a
// persisted unverified identity. The printed code's sha256 must match
// the persisted verification_code_hash, and a fresh 24h expiry must be
// written.
func TestCLIIdentityReissueCode_Persistent(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p := seedPrincipalDirect(t, st, "alice@test.local")
	id := seedIdentityRow(t, st, p.ID, "ident-reissue", "alice+r@example.com")

	stdout, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", id)
	if err != nil {
		t.Fatalf("identity reissue-code: %v", err)
	}

	code := parseReissueCode(t, stdout, "code")
	token := parseReissueCode(t, stdout, "token")
	if len(code) != 6 {
		t.Errorf("expected 6-digit code, got %q", code)
	}
	if token == "" {
		t.Errorf("expected non-empty token")
	}
	if !strings.Contains(stdout, "/verify-identity?token=") {
		t.Errorf("expected verification link in output; stdout:\n%s", stdout)
	}

	row, err := st.Meta().GetJMAPIdentity(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	wantCodeHash := sha256.Sum256([]byte(code))
	if !bytes.Equal(row.VerificationCodeHash, wantCodeHash[:]) {
		t.Errorf("persisted code hash does not match printed code")
	}
	wantTokenHash := sha256.Sum256([]byte(token))
	if !bytes.Equal(row.VerificationTokenHash, wantTokenHash[:]) {
		t.Errorf("persisted token hash does not match printed token")
	}
	if row.VerificationTokenExpiresAtUs == 0 {
		t.Errorf("expected fresh expiry on the row, got 0")
	}
	if row.VerifiedAtUs != 0 {
		t.Errorf("reissue-code must not verify the identity; VerifiedAtUs=%d", row.VerifiedAtUs)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.reissue-code.admin",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Subject != "identity:"+id {
		t.Errorf("audit subject: got %q, want identity:%s", entries[0].Subject, id)
	}
	if entries[0].Metadata["email"] != "alice+r@example.com" {
		t.Errorf("audit metadata email: got %q", entries[0].Metadata["email"])
	}
}

// TestCLIIdentityReissueCode_RotatesCode: a second re-issue produces a
// fresh code and overwrites the persisted hash.
func TestCLIIdentityReissueCode_RotatesCode(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p := seedPrincipalDirect(t, st, "alice@test.local")
	id := seedIdentityRow(t, st, p.ID, "ident-rotate", "alice+rot@example.com")

	out1, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", id)
	if err != nil {
		t.Fatalf("first reissue-code: %v", err)
	}
	out2, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", id)
	if err != nil {
		t.Fatalf("second reissue-code: %v", err)
	}

	token1 := parseReissueCode(t, out1, "token")
	token2 := parseReissueCode(t, out2, "token")
	if token1 == token2 {
		t.Errorf("expected the token to rotate between re-issues")
	}

	row, err := st.Meta().GetJMAPIdentity(context.Background(), id)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	want := sha256.Sum256([]byte(parseReissueCode(t, out2, "code")))
	if !bytes.Equal(row.VerificationCodeHash, want[:]) {
		t.Errorf("persisted hash must match the most recent re-issue")
	}
}

// TestCLIIdentityReissueCode_WithOperator: --operator resolves to
// ActorPrincipal/<pid> in the audit log.
func TestCLIIdentityReissueCode_WithOperator(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	op := seedPrincipalDirect(t, st, "admin@test.local")
	user := seedPrincipalDirect(t, st, "user@test.local")
	id := seedIdentityRow(t, st, user.ID, "ident-op", "user+op@example.com")

	if _, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", id,
		"--operator", "admin@test.local"); err != nil {
		t.Fatalf("identity reissue-code --operator: %v", err)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.reissue-code.admin",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].ActorKind != store.ActorPrincipal {
		t.Errorf("ActorKind: got %v, want ActorPrincipal", entries[0].ActorKind)
	}
	wantID := strconv.FormatUint(uint64(op.ID), 10)
	if entries[0].ActorID != wantID {
		t.Errorf("ActorID: got %q, want %q", entries[0].ActorID, wantID)
	}
}

// TestCLIIdentityReissueCode_AlreadyVerifiedRejected: re-issuing a code
// for an already-verified identity is meaningless and is refused.
func TestCLIIdentityReissueCode_AlreadyVerifiedRejected(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	p := seedPrincipalDirect(t, st, "alice@test.local")
	id := seedIdentityRow(t, st, p.ID, "ident-verified", "alice+v@example.com")
	if err := st.Meta().MarkIdentityVerified(context.Background(), id); err != nil {
		t.Fatalf("MarkIdentityVerified seed: %v", err)
	}

	_, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", id)
	if err == nil {
		t.Fatal("expected error re-issuing code for a verified identity")
	}
	if !strings.Contains(err.Error(), "already verified") {
		t.Fatalf("expected 'already verified' message, got: %v", err)
	}

	entries, err := st.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "identity.reissue-code.admin",
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 audit entries on verified-reject, got %d", len(entries))
	}
}

// TestCLIIdentityReissueCode_DefaultIsRejected: the synthesised default
// identity has no row and is verified by construction.
func TestCLIIdentityReissueCode_DefaultIsRejected(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openIdentityTestStore(t, cfgPath)
	seedPrincipalDirect(t, st, "alice@test.local")

	_, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", "default")
	if err == nil {
		t.Fatal("expected error re-issuing code for 'default' identity")
	}
	if !strings.Contains(err.Error(), "verified by construction") {
		t.Fatalf("expected 'verified by construction' message, got: %v", err)
	}
}

// TestCLIIdentityReissueCode_NotFound: clear operator-facing error for
// an unknown identity id.
func TestCLIIdentityReissueCode_NotFound(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	openIdentityTestStore(t, cfgPath)

	_, _, err := runIdentity(t, cfgPath, "identity", "reissue-code", "ghost")
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got: %v", err)
	}
}
