package storetest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// REQ-IDENT-01..91 — Identity verification store-method compliance.

// vh32 returns sha256 of s; convenience helper for test inputs that
// match the 32-byte invariant on store inputs.
func vh32(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// insertUnverifiedIdentity inserts a fresh JMAP identity in the
// unverified state (REQ-IDENT-12) and returns its row id. The id is
// allocated from a per-test sequence so tests can run in parallel
// without colliding on the primary key.
func insertUnverifiedIdentity(t *testing.T, s store.Store, pid store.PrincipalID, idSuffix string) string {
	t.Helper()
	ctx := ctxT(t)
	id := "iv-" + idSuffix
	row := store.JMAPIdentity{
		ID:          id,
		PrincipalID: pid,
		Email:       idSuffix + "@example.com",
		MayDelete:   true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity(%s): %v", id, err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("post-insert Get(%s): %v", id, err)
	}
	if got.VerifiedAtUs != 0 {
		t.Fatalf("freshly-inserted identity must be unverified; VerifiedAtUs = %d", got.VerifiedAtUs)
	}
	if got.VerificationTokenHash != nil || got.VerificationCodeHash != nil ||
		got.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("freshly-inserted identity must carry no verification token: %+v", got)
	}
	return id
}

func testIdentityVerifyIssueRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-issue@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "issue")

	tok := vh32("raw-token-issue")
	code := vh32("123456")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok, code, exp); err != nil {
		t.Fatalf("IssueIdentityVerificationToken: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-issue: %v", err)
	}
	if !bytes.Equal(got.VerificationTokenHash, tok) {
		t.Fatalf("token hash mismatch: %x vs %x", got.VerificationTokenHash, tok)
	}
	if !bytes.Equal(got.VerificationCodeHash, code) {
		t.Fatalf("code hash mismatch: %x vs %x", got.VerificationCodeHash, code)
	}
	if got.VerificationTokenExpiresAtUs != exp {
		t.Fatalf("expiry mismatch: %d vs %d", got.VerificationTokenExpiresAtUs, exp)
	}
	if got.VerifiedAtUs != 0 {
		t.Fatalf("issuing a token must not set VerifiedAtUs")
	}

	byTok, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, tok)
	if err != nil {
		t.Fatalf("GetIdentityByVerificationTokenHash: %v", err)
	}
	if byTok.ID != id {
		t.Fatalf("lookup by token returned %q, want %q", byTok.ID, id)
	}

	byCode, err := s.Meta().GetIdentityByVerificationCodeHash(ctx, id, code)
	if err != nil {
		t.Fatalf("GetIdentityByVerificationCodeHash: %v", err)
	}
	if byCode.ID != id {
		t.Fatalf("lookup by code returned %q, want %q", byCode.ID, id)
	}
}

func testIdentityVerifyIssueRejectsLiveToken(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-issue-live@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "issuelive")

	tok := vh32("first-token")
	code := vh32("100001")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok, code, exp); err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	// Second Issue must refuse because a token is already live.
	err := s.Meta().IssueIdentityVerificationToken(ctx, id,
		vh32("second-token"), vh32("100002"), exp+1)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second Issue err = %v, want ErrConflict", err)
	}
	// The first token must still be present and unchanged.
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got.VerificationTokenHash, tok) {
		t.Fatalf("conflicting Issue mutated the token: %x", got.VerificationTokenHash)
	}
}

func testIdentityVerifyResetRotates(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-reset@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "reset")

	tok1 := vh32("token-A")
	code1 := vh32("100100")
	exp1 := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok1, code1, exp1); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	tok2 := vh32("token-B")
	code2 := vh32("100200")
	exp2 := exp1 + 1_000_000
	if err := s.Meta().ResetIdentityVerificationToken(ctx, id, tok2, code2, exp2); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Old token must no longer resolve to the identity.
	if _, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, tok1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old token still resolves: err = %v", err)
	}
	// New token resolves.
	got, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, tok2)
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if got.ID != id {
		t.Fatalf("new token returned %q, want %q", got.ID, id)
	}
	if got.VerificationTokenExpiresAtUs != exp2 {
		t.Fatalf("expiry not updated: %d vs %d", got.VerificationTokenExpiresAtUs, exp2)
	}

	// Reset on a missing row -> ErrNotFound.
	err = s.Meta().ResetIdentityVerificationToken(ctx, "iv-does-not-exist",
		vh32("x"), vh32("y"), exp2)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Reset on missing row: err = %v, want ErrNotFound", err)
	}
}

func testIdentityVerifyMarkClearsAndIdempotent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-mark@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "mark")

	tok := vh32("mark-token")
	code := vh32("200200")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok, code, exp); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := s.Meta().MarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-mark: %v", err)
	}
	if got.VerifiedAtUs == 0 {
		t.Fatalf("VerifiedAtUs not set after Mark")
	}
	if got.VerificationTokenHash != nil || got.VerificationCodeHash != nil ||
		got.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("verification trio not cleared after Mark: %+v", got)
	}
	// Token lookup must now miss (REQ-IDENT-42: an invalid token on an
	// already-verified row returns ErrNotFound).
	if _, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, tok); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-mark token lookup: err = %v, want ErrNotFound", err)
	}

	// Calling Mark a second time is idempotent (REQ-IDENT-42); the
	// store method itself always writes verified_at_us, but the row
	// remains verified and tokens stay cleared.
	if err := s.Meta().MarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("Mark idempotent: %v", err)
	}
	got2, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-second-mark: %v", err)
	}
	if got2.VerifiedAtUs == 0 {
		t.Fatalf("VerifiedAtUs cleared by second Mark")
	}

	// Mark on missing row -> ErrNotFound.
	if err := s.Meta().MarkIdentityVerified(ctx, "iv-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Mark missing: err = %v, want ErrNotFound", err)
	}
}

func testIdentityVerifyUnmarkClearsVerifiedAt(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-unmark@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "unmark")

	tok := vh32("unmark-token")
	code := vh32("210210")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok, code, exp); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := s.Meta().MarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-mark: %v", err)
	}
	if got.VerifiedAtUs == 0 {
		t.Fatalf("VerifiedAtUs not set after Mark")
	}

	// Unmark clears verified_at_us; verification trio (already cleared
	// by Mark) stays cleared.
	if err := s.Meta().UnmarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("UnmarkIdentityVerified: %v", err)
	}
	got, err = s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-unmark: %v", err)
	}
	if got.VerifiedAtUs != 0 {
		t.Fatalf("VerifiedAtUs not cleared after Unmark: %d", got.VerifiedAtUs)
	}

	// Unmark is idempotent on an already-unverified row.
	if err := s.Meta().UnmarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("UnmarkIdentityVerified idempotent: %v", err)
	}

	// Unmark on a missing row returns ErrNotFound.
	if err := s.Meta().UnmarkIdentityVerified(ctx, "iv-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Unmark missing: err = %v, want ErrNotFound", err)
	}

	// Unmark does not touch the verification trio. Re-issue a fresh
	// token (the previous Mark cleared everything), then Unmark and
	// confirm the live token survives.
	tok2 := vh32("unmark-token-2")
	code2 := vh32("210211")
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok2, code2, exp); err != nil {
		t.Fatalf("Issue 2: %v", err)
	}
	if err := s.Meta().UnmarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("Unmark with live token: %v", err)
	}
	got, err = s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-unmark-with-token: %v", err)
	}
	if !bytes.Equal(got.VerificationTokenHash, tok2) {
		t.Fatalf("Unmark mutated VerificationTokenHash: %x vs %x", got.VerificationTokenHash, tok2)
	}
}

func testIdentityVerifyClearLeavesVerifiedAtAlone(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-clear@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "clear")

	tok := vh32("clear-token")
	code := vh32("300300")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok, code, exp); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Clear without first marking verified.
	if err := s.Meta().ClearIdentityVerificationToken(ctx, id); err != nil {
		t.Fatalf("ClearIdentityVerificationToken: %v", err)
	}
	got, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("Get post-clear: %v", err)
	}
	if got.VerifiedAtUs != 0 {
		t.Fatalf("Clear must not touch VerifiedAtUs (was %d)", got.VerifiedAtUs)
	}
	if got.VerificationTokenHash != nil || got.VerificationCodeHash != nil ||
		got.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("verification trio not cleared: %+v", got)
	}

	if err := s.Meta().ClearIdentityVerificationToken(ctx, "iv-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Clear missing: err = %v, want ErrNotFound", err)
	}
}

func testIdentityVerifyLookupByCodeIsIdentityScoped(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-scope@example.com")
	idA := insertUnverifiedIdentity(t, s, p.ID, "scopeA")
	idB := insertUnverifiedIdentity(t, s, p.ID, "scopeB")

	// Both identities receive tokens with the SAME code (REQ-IDENT-32
	// allows the 6-digit code to repeat across identities because the
	// URL carries the id).
	sharedCode := vh32("999999")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, idA, vh32("tokA"), sharedCode, exp); err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if err := s.Meta().IssueIdentityVerificationToken(ctx, idB, vh32("tokB"), sharedCode, exp); err != nil {
		t.Fatalf("Issue B: %v", err)
	}

	gotA, err := s.Meta().GetIdentityByVerificationCodeHash(ctx, idA, sharedCode)
	if err != nil {
		t.Fatalf("GetByCode A: %v", err)
	}
	if gotA.ID != idA {
		t.Fatalf("GetByCode A returned %q, want %q", gotA.ID, idA)
	}
	gotB, err := s.Meta().GetIdentityByVerificationCodeHash(ctx, idB, sharedCode)
	if err != nil {
		t.Fatalf("GetByCode B: %v", err)
	}
	if gotB.ID != idB {
		t.Fatalf("GetByCode B returned %q, want %q", gotB.ID, idB)
	}
	// Wrong code -> ErrNotFound.
	if _, err := s.Meta().GetIdentityByVerificationCodeHash(ctx, idA, vh32("111111")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong code lookup: err = %v, want ErrNotFound", err)
	}
	// Wrong id with right code -> ErrNotFound.
	if _, err := s.Meta().GetIdentityByVerificationCodeHash(ctx, "iv-missing", sharedCode); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing-id lookup: err = %v, want ErrNotFound", err)
	}
}

func testIdentityVerifyListUnverifiedOlderThan(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-list-unv@example.com")

	// Insert three identities with increasing CreatedAtUs so we can
	// pivot the GC predicate cleanly. CreatedAtUs is allowed to be set
	// by the caller (InsertJMAPIdentity will not overwrite a non-zero
	// value).
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		row := store.JMAPIdentity{
			ID:          "iv-list-" + strconv.Itoa(i),
			PrincipalID: p.ID,
			Email:       "u" + strconv.Itoa(i) + "@example.com",
			MayDelete:   true,
			CreatedAtUs: t0.Add(time.Duration(i) * time.Hour).UnixMicro(),
		}
		if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	// Mark the middle row verified so it is excluded from the list.
	if err := s.Meta().MarkIdentityVerified(ctx, "iv-list-1"); err != nil {
		t.Fatalf("Mark verified: %v", err)
	}

	// Cut-off between row 0 (older) and row 2 (newer); the verified
	// row 1 is filtered by predicate even though its CreatedAtUs falls
	// inside the window.
	cutoff := t0.Add(90 * time.Minute)
	got, err := s.Meta().ListUnverifiedIdentitiesOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "iv-list-0" {
		t.Fatalf("List returned %+v, want exactly iv-list-0", got)
	}

	// Wide cutoff returns the two unverified rows in ascending order.
	wide := t0.Add(48 * time.Hour)
	got, err = s.Meta().ListUnverifiedIdentitiesOlderThan(ctx, wide)
	if err != nil {
		t.Fatalf("List wide: %v", err)
	}
	if len(got) != 2 || got[0].ID != "iv-list-0" || got[1].ID != "iv-list-2" {
		t.Fatalf("wide List returned %+v, want [iv-list-0, iv-list-2]", got)
	}
}

func testIdentityVerifyListExpiredTokens(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-list-exp@example.com")
	idA := insertUnverifiedIdentity(t, s, p.ID, "expA")
	idB := insertUnverifiedIdentity(t, s, p.ID, "expB")
	idC := insertUnverifiedIdentity(t, s, p.ID, "expC") // no token at all

	// All timestamps fixed (no time.Now()): tests run against backends
	// with either a real or fake clock and must not depend on wall time.
	expPast := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()
	expFuture := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()
	if err := s.Meta().IssueIdentityVerificationToken(ctx, idA,
		vh32("expA-tok"), vh32("400400"), expPast); err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if err := s.Meta().IssueIdentityVerificationToken(ctx, idB,
		vh32("expB-tok"), vh32("400500"), expFuture); err != nil {
		t.Fatalf("Issue B: %v", err)
	}

	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got, err := s.Meta().ListExpiredVerificationTokens(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListExpiredVerificationTokens: %v", err)
	}
	if len(got) != 1 || got[0].ID != idA {
		t.Fatalf("returned %+v, want exactly %q", got, idA)
	}
	// idC must never appear — it has no token.
	for _, r := range got {
		if r.ID == idC {
			t.Fatalf("identity with no token leaked into ListExpired: %+v", r)
		}
	}
}

func testIdentityVerifyFullLifecycle(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-life@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "life")

	tok := vh32("life-token")
	code := vh32("500500")
	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()

	// Issue -> looks-up by token works.
	if err := s.Meta().IssueIdentityVerificationToken(ctx, id, tok, code, exp); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, tok); err != nil {
		t.Fatalf("GetByToken post-Issue: %v", err)
	}

	// Mark verified.
	if err := s.Meta().MarkIdentityVerified(ctx, id); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	// Token lookup must now miss.
	if _, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, tok); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-Mark GetByToken: err = %v, want ErrNotFound", err)
	}

	// The identity row itself is intact.
	final, err := s.Meta().GetJMAPIdentity(ctx, id)
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if final.VerifiedAtUs == 0 {
		t.Fatalf("VerifiedAtUs not persisted: %+v", final)
	}
}

func testIdentityVerifyInvalidInputs(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "iv-bad@example.com")
	id := insertUnverifiedIdentity(t, s, p.ID, "bad")

	exp := time.Now().UTC().Add(24 * time.Hour).UnixMicro()
	good := vh32("good")

	// Wrong-length hashes are rejected as ErrInvalidArgument.
	for _, tc := range []struct {
		name           string
		token, code    []byte
		expiresAtUs    int64
		wantInvalidArg bool
	}{
		{"short token", []byte{1, 2, 3}, good, exp, true},
		{"empty token", []byte{}, good, exp, true},
		{"short code", good, []byte{1, 2, 3}, exp, true},
		{"empty code", good, []byte{}, exp, true},
		{"zero expiry", good, good, 0, true},
		{"negative expiry", good, good, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Meta().IssueIdentityVerificationToken(ctx, id, tc.token, tc.code, tc.expiresAtUs)
			if !errors.Is(err, store.ErrInvalidArgument) {
				t.Fatalf("Issue %q: err = %v, want ErrInvalidArgument", tc.name, err)
			}
			err = s.Meta().ResetIdentityVerificationToken(ctx, id, tc.token, tc.code, tc.expiresAtUs)
			if !errors.Is(err, store.ErrInvalidArgument) {
				t.Fatalf("Reset %q: err = %v, want ErrInvalidArgument", tc.name, err)
			}
		})
	}

	// Empty hash on the lookup paths.
	if _, err := s.Meta().GetIdentityByVerificationTokenHash(ctx, nil); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("GetByToken nil: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := s.Meta().GetIdentityByVerificationCodeHash(ctx, id, nil); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("GetByCode nil: err = %v, want ErrInvalidArgument", err)
	}
}
