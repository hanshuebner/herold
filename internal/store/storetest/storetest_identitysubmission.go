package storetest

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// mustMaterializeDefaultIdentity materializes the default identity for pid and
// returns the resulting identity id. Fails the test on error.
func mustMaterializeDefaultIdentity(t *testing.T, s store.Store, pid store.PrincipalID) string {
	t.Helper()
	ctx := ctxT(t)
	id, err := s.Meta().MaterializeDefaultIdentity(ctx, pid)
	if err != nil {
		t.Fatalf("MaterializeDefaultIdentity: %v", err)
	}
	if id == "" {
		t.Fatal("MaterializeDefaultIdentity: returned empty id")
	}
	return id
}

// testMaterializeDefaultIdentity_Idempotent verifies that calling
// MaterializeDefaultIdentity twice returns the same id and does not create a
// second row.
func testMaterializeDefaultIdentity_Idempotent(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "matid-idem@example.com")

	id1 := mustMaterializeDefaultIdentity(t, s, p.ID)
	id2 := mustMaterializeDefaultIdentity(t, s, p.ID)

	if id1 != id2 {
		t.Fatalf("second call returned different id: %q != %q", id1, id2)
	}

	// The row's email must match the principal email.
	row, err := s.Meta().GetJMAPIdentity(ctx, id1)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	if row.Email != "matid-idem@example.com" {
		t.Errorf("identity email = %q; want principal email %q", row.Email, "matid-idem@example.com")
	}
	// must_delete must be false for the materialized default.
	if row.MayDelete {
		t.Error("materialized default identity must have MayDelete=false")
	}
}

// testIdentitySubmission_UpsertGet_Roundtrip verifies that an upserted row
// is returned byte-for-byte by GetIdentitySubmission.
func testIdentitySubmission_UpsertGet_Roundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub-upsert@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:encrypted-password-blob"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("UpsertIdentitySubmission: %v", err)
	}

	got, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	if got.IdentityID != identityID {
		t.Errorf("IdentityID = %q; want %q", got.IdentityID, identityID)
	}
	if got.SubmitHost != "smtp.example.com" {
		t.Errorf("SubmitHost = %q; want smtp.example.com", got.SubmitHost)
	}
	if got.SubmitPort != 587 {
		t.Errorf("SubmitPort = %d; want 587", got.SubmitPort)
	}
	if got.SubmitSecurity != "starttls" {
		t.Errorf("SubmitSecurity = %q; want starttls", got.SubmitSecurity)
	}
	if got.SubmitAuthMethod != "password" {
		t.Errorf("SubmitAuthMethod = %q; want password", got.SubmitAuthMethod)
	}
	if !bytes.Equal(got.PasswordCT, sub.PasswordCT) {
		t.Errorf("PasswordCT = %v; want %v", got.PasswordCT, sub.PasswordCT)
	}
	if got.State != store.IdentitySubmissionStateOK {
		t.Errorf("State = %q; want ok", got.State)
	}
}

// testIdentitySubmission_OAuthFields_Roundtrip verifies OAuth credential fields
// survive the round-trip byte-for-byte.
func testIdentitySubmission_OAuthFields_Roundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub-oauth@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour).Truncate(time.Microsecond)
	due := now.Add(50 * time.Minute).Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:         identityID,
		SubmitHost:         "smtp.gmail.com",
		SubmitPort:         465,
		SubmitSecurity:     "implicit_tls",
		SubmitAuthMethod:   "oauth2",
		OAuthAccessCT:      []byte("v1:sealed-access-token"),
		OAuthRefreshCT:     []byte("v1:sealed-refresh-token"),
		OAuthTokenEndpoint: "https://oauth2.googleapis.com/token",
		OAuthClientID:      "client-123",
		OAuthExpiresAt:     expires,
		RefreshDue:         due,
		State:              store.IdentitySubmissionStateOK,
		StateAt:            now,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("UpsertIdentitySubmission: %v", err)
	}

	got, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission: %v", err)
	}
	if !bytes.Equal(got.OAuthAccessCT, sub.OAuthAccessCT) {
		t.Errorf("OAuthAccessCT mismatch: %v vs %v", got.OAuthAccessCT, sub.OAuthAccessCT)
	}
	if !bytes.Equal(got.OAuthRefreshCT, sub.OAuthRefreshCT) {
		t.Errorf("OAuthRefreshCT mismatch: %v vs %v", got.OAuthRefreshCT, sub.OAuthRefreshCT)
	}
	if got.OAuthTokenEndpoint != sub.OAuthTokenEndpoint {
		t.Errorf("OAuthTokenEndpoint = %q; want %q", got.OAuthTokenEndpoint, sub.OAuthTokenEndpoint)
	}
	if got.OAuthClientID != sub.OAuthClientID {
		t.Errorf("OAuthClientID = %q; want %q", got.OAuthClientID, sub.OAuthClientID)
	}
	if !got.OAuthExpiresAt.Equal(expires) {
		t.Errorf("OAuthExpiresAt = %v; want %v", got.OAuthExpiresAt, expires)
	}
	if !got.RefreshDue.Equal(due) {
		t.Errorf("RefreshDue = %v; want %v", got.RefreshDue, due)
	}
}

// testIdentitySubmission_GetNotFound returns ErrNotFound when no row exists.
func testIdentitySubmission_GetNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	_, err := s.Meta().GetIdentitySubmission(ctx, "nonexistent-id")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetIdentitySubmission: want ErrNotFound, got %v", err)
	}
}

// testIdentitySubmission_StateTransition verifies that a state update is
// reflected by a subsequent Get.
func testIdentitySubmission_StateTransition(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub-state@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:pw"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	// Transition to auth-failed.
	later := now.Add(time.Minute)
	sub.State = store.IdentitySubmissionStateAuthFailed
	sub.StateAt = later
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("state-update upsert: %v", err)
	}

	got, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission after state update: %v", err)
	}
	if got.State != store.IdentitySubmissionStateAuthFailed {
		t.Errorf("State = %q; want %q", got.State, store.IdentitySubmissionStateAuthFailed)
	}
}

// testIdentitySubmission_Delete_NotFoundAfter verifies that Delete removes the
// row and subsequent Get returns ErrNotFound.
func testIdentitySubmission_Delete_NotFoundAfter(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub-del@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:pw"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Meta().DeleteIdentitySubmission(ctx, identityID); err != nil {
		t.Fatalf("DeleteIdentitySubmission: %v", err)
	}
	_, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	// Second delete should also return ErrNotFound.
	if err := s.Meta().DeleteIdentitySubmission(ctx, identityID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
}

// testIdentitySubmission_Cascade verifies that deleting the parent identity
// cascades to the submission row.
func testIdentitySubmission_Cascade(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "sub-cascade@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:pw"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Delete the identity row itself (which should CASCADE to submission).
	if err := s.Meta().DeleteJMAPIdentity(ctx, identityID); err != nil {
		t.Fatalf("DeleteJMAPIdentity: %v", err)
	}

	// Submission row must now be gone.
	_, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after cascade, got %v", err)
	}
}

// testIdentitySubmission_ListDue verifies that ListIdentitySubmissionsDue
// returns only rows with non-null refresh_due_us <= the cutoff, ordered by
// refresh_due_us ascending.
func testIdentitySubmission_ListDue(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)

	base := time.Now().UTC().Truncate(time.Microsecond)

	// Insert three identities with different RefreshDue values.
	makeEntry := func(email string, offset time.Duration) string {
		p := mustInsertPrincipal(t, s, email)
		identityID := mustMaterializeDefaultIdentity(t, s, p.ID)
		due := base.Add(offset)
		sub := store.IdentitySubmission{
			IdentityID:       identityID,
			SubmitHost:       "smtp.example.com",
			SubmitPort:       587,
			SubmitSecurity:   "starttls",
			SubmitAuthMethod: "oauth2",
			OAuthAccessCT:    []byte("v1:tok"),
			RefreshDue:       due,
			State:            store.IdentitySubmissionStateOK,
			StateAt:          base,
			CreatedAt:        base,
		}
		if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
			t.Fatalf("upsert %s: %v", email, err)
		}
		return identityID
	}

	id1 := makeEntry("sub-due1@example.com", -2*time.Minute) // past — due
	id2 := makeEntry("sub-due2@example.com", -1*time.Minute) // past — due
	_ = makeEntry("sub-due3@example.com", +2*time.Minute)    // future — not due

	// A password entry with no refresh_due (nil in DB) — must not appear.
	p4 := mustInsertPrincipal(t, s, "sub-due4@example.com")
	id4 := mustMaterializeDefaultIdentity(t, s, p4.ID)
	subPw := store.IdentitySubmission{
		IdentityID:       id4,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:pw"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          base,
		CreatedAt:        base,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, subPw); err != nil {
		t.Fatalf("upsert password entry: %v", err)
	}

	due, err := s.Meta().ListIdentitySubmissionsDue(ctx, base)
	if err != nil {
		t.Fatalf("ListIdentitySubmissionsDue: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("ListIdentitySubmissionsDue: got %d rows, want 2", len(due))
	}
	// Results must be ordered by refresh_due_us ascending.
	if due[0].IdentityID != id1 {
		t.Errorf("row[0].IdentityID = %q; want %q (earliest)", due[0].IdentityID, id1)
	}
	if due[1].IdentityID != id2 {
		t.Errorf("row[1].IdentityID = %q; want %q (second earliest)", due[1].IdentityID, id2)
	}
}

// testIdentitySubmission_UpsertWithoutMaterialize verifies that upserting a
// submission row for a non-existent identity returns an error (no orphan row).
func testIdentitySubmission_UpsertWithoutMaterialize(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:       "nonexistent-identity",
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:pw"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	err := s.Meta().UpsertIdentitySubmission(ctx, sub)
	if err == nil {
		t.Fatal("expected error when upserting with non-existent identity, got nil")
	}
}

// testIdentitySubmission_CTValidation_ValidPrefix verifies that an upsert
// with properly v1:-prefixed CT fields succeeds.
func testIdentitySubmission_CTValidation_ValidPrefix(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ct-valid@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       []byte("v1:sealed-password-bytes"),
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("UpsertIdentitySubmission with valid v1: prefix: %v", err)
	}
	got, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission after valid upsert: %v", err)
	}
	if !bytes.Equal(got.PasswordCT, sub.PasswordCT) {
		t.Errorf("PasswordCT = %v; want %v", got.PasswordCT, sub.PasswordCT)
	}
}

// testIdentitySubmission_CTValidation_InvalidPrefix verifies that an upsert
// with a bare (non-v1:) CT payload is rejected and nothing is written.
func testIdentitySubmission_CTValidation_InvalidPrefix(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ct-invalid@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	// A 16-byte bare payload — no "v1:" prefix.
	bareCT := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		PasswordCT:       bareCT,
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	err := s.Meta().UpsertIdentitySubmission(ctx, sub)
	if err == nil {
		t.Fatal("UpsertIdentitySubmission with bare CT: expected rejection error, got nil")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("expected error wrapping ErrInvalidArgument, got: %v", err)
	}
	// Confirm nothing was written to the table.
	_, getErr := s.Meta().GetIdentitySubmission(ctx, identityID)
	if !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound after rejected upsert, got: %v", getErr)
	}
}

// testIdentitySubmission_CountOAuth verifies that CountOAuthIdentitySubmissions
// returns the total count of oauth2-method rows regardless of refresh_due_us,
// and does not count password-method rows.
func testIdentitySubmission_CountOAuth(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Helper to insert an identity with the given auth method.
	insertSub := func(email, authMethod string) {
		t.Helper()
		p := mustInsertPrincipal(t, s, email)
		identityID := mustMaterializeDefaultIdentity(t, s, p.ID)
		sub := store.IdentitySubmission{
			IdentityID:       identityID,
			SubmitHost:       "smtp.example.com",
			SubmitPort:       587,
			SubmitSecurity:   "starttls",
			SubmitAuthMethod: authMethod,
			State:            store.IdentitySubmissionStateOK,
			StateAt:          now,
			CreatedAt:        now,
		}
		if authMethod == "oauth2" {
			sub.OAuthAccessCT = []byte("v1:tok")
			// RefreshDue intentionally not set to verify the query is
			// independent of refresh_due_us.
		} else {
			sub.PasswordCT = []byte("v1:pw")
		}
		if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
			t.Fatalf("UpsertIdentitySubmission(%s): %v", email, err)
		}
	}

	// Empty table: count must be 0.
	n, err := s.Meta().CountOAuthIdentitySubmissions(ctx)
	if err != nil {
		t.Fatalf("CountOAuthIdentitySubmissions (empty): %v", err)
	}
	if n != 0 {
		t.Errorf("empty table: count = %d; want 0", n)
	}

	// Insert two oauth2 rows and one password row.
	insertSub("count-oauth1@example.com", "oauth2")
	insertSub("count-oauth2@example.com", "oauth2")
	insertSub("count-pw@example.com", "password")

	n, err = s.Meta().CountOAuthIdentitySubmissions(ctx)
	if err != nil {
		t.Fatalf("CountOAuthIdentitySubmissions: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d; want 2 (only oauth2 rows)", n)
	}
}

// testIdentitySubmission_CTValidation_NilFields verifies that an upsert
// with all-nil CT fields succeeds (sparse row).
func testIdentitySubmission_CTValidation_NilFields(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "ct-nil@example.com")
	identityID := mustMaterializeDefaultIdentity(t, s, p.ID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	// No CT fields set — simulates an identity without stored credentials.
	sub := store.IdentitySubmission{
		IdentityID:       identityID,
		SubmitHost:       "smtp.example.com",
		SubmitPort:       587,
		SubmitSecurity:   "starttls",
		SubmitAuthMethod: "password",
		State:            store.IdentitySubmissionStateOK,
		StateAt:          now,
		CreatedAt:        now,
	}
	if err := s.Meta().UpsertIdentitySubmission(ctx, sub); err != nil {
		t.Fatalf("UpsertIdentitySubmission with nil CT fields: %v", err)
	}
	got, err := s.Meta().GetIdentitySubmission(ctx, identityID)
	if err != nil {
		t.Fatalf("GetIdentitySubmission after nil-CT upsert: %v", err)
	}
	if got.PasswordCT != nil {
		t.Errorf("PasswordCT = %v; want nil", got.PasswordCT)
	}
}
