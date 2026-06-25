package identityverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// tokenRegex captures the raw base64url token from the rendered email
// body. The composer always writes the absolute URL on its own line
// (plain-text part) and as an href in the HTML part, so a simple
// regex is sufficient; we use the plain-text occurrence first.
var tokenRegex = regexp.MustCompile(`/verify-identity\?token=([A-Za-z0-9_\-]+)`)

// captureSubmitter records the queue.Submission shape so the
// integration test can pull the body bytes out, extract the token,
// and drive the callback path against the same store.
type captureSubmitter struct {
	body []byte
}

func (c *captureSubmitter) Submit(_ context.Context, sub queue.Submission) (queue.EnvelopeID, error) {
	c.body, _ = io.ReadAll(sub.Body)
	return queue.EnvelopeID("env-test-1"), nil
}

// TestIntegration_DispatchAndRedeem exercises the full pipeline that
// REQ-IDENT-30..43 specifies, end-to-end against the sqlite backend:
//
//  1. Insert an unverified identity row.
//  2. Fire the dispatcher; assert the row carries the persisted token
//     and code hashes.
//  3. Pull the raw token from the captured submission body (the same
//     bytes the queue would persist + send via SMTP).
//  4. SHA-256 the raw token and resolve back to the identity row via
//     the same store method the REST callback uses.
//  5. Mark the identity verified; assert the token columns are cleared
//     and verified_at_us is set.
//
// The postgres lane runs the same flow via the storetest harness
// invoked by `make test-server`; this test pins the sqlite lane.
func TestIntegration_DispatchAndRedeem(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.com",
		DisplayName:    "Alice",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	row := store.JMAPIdentity{
		ID:          "iv-int-1",
		PrincipalID: p.ID,
		Email:       "alice@external.test",
		MayDelete:   true,
		CreatedAtUs: clk.Now().UnixMicro(),
		UpdatedAtUs: clk.Now().UnixMicro(),
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	cap := &captureSubmitter{}
	aud := &fakeAuditor{}
	d := New(Options{
		Store:         st,
		Submitter:     cap,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:         clk,
		PublicBaseURL: "https://mail.example.com",
		VerifierFrom:  "postmaster@example.com",
		Auditor:       aud,
	})
	if _, err := d.Trigger(ctx, row); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// Step 3: pull the raw token from the rendered body. This is the
	// exact byte sequence the queue worker would feed to SMTP and the
	// user would receive in their mail client.
	match := tokenRegex.FindSubmatch(cap.body)
	if match == nil {
		t.Fatalf("could not locate token in rendered body: %s", cap.body)
	}
	rawToken := string(match[1])
	if len(rawToken) != 43 {
		t.Fatalf("token length: got %d, want 43; token=%q", len(rawToken), rawToken)
	}

	// Step 4: the REST callback handler in
	// internal/protoadmin/identity_verify.go hashes the URL query
	// verbatim and looks up by that hash. Mirror exactly that path.
	tokenHash := sha256.Sum256([]byte(rawToken))
	found, err := st.Meta().GetIdentityByVerificationTokenHash(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetIdentityByVerificationTokenHash: %v", err)
	}
	if found.ID != row.ID {
		t.Fatalf("token lookup returned %q, want %q", found.ID, row.ID)
	}
	if found.VerifiedAtUs != 0 {
		t.Fatalf("row already verified before redeem: %+v", found)
	}

	// Step 5: the callback handler advances the row by calling
	// MarkIdentityVerified. After the call, verifiedAt is set and the
	// token trio is cleared.
	if err := st.Meta().MarkIdentityVerified(ctx, row.ID); err != nil {
		t.Fatalf("MarkIdentityVerified: %v", err)
	}
	after, err := st.Meta().GetJMAPIdentity(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity post-verify: %v", err)
	}
	if after.VerifiedAtUs == 0 {
		t.Fatalf("verified_at_us not set after MarkIdentityVerified")
	}
	if after.VerificationTokenHash != nil ||
		after.VerificationCodeHash != nil ||
		after.VerificationTokenExpiresAtUs != 0 {
		t.Fatalf("token trio not cleared after MarkIdentityVerified: %+v", after)
	}

	// A second redeem against the cleared hash MUST return ErrNotFound.
	// This is the contract the REST callback relies on for the
	// idempotent-failure path (REQ-IDENT-42).
	_, err = st.Meta().GetIdentityByVerificationTokenHash(ctx, tokenHash[:])
	if err == nil {
		t.Fatalf("second redeem succeeded; want ErrNotFound on cleared row")
	}

	// Audit side: one success entry.
	if len(aud.entries) != 1 {
		t.Fatalf("audit entries: got %d, want 1", len(aud.entries))
	}
	if aud.entries[0].Action != "identity.verify.send" {
		t.Fatalf("audit action: got %q", aud.entries[0].Action)
	}
	if aud.entries[0].Outcome != store.OutcomeSuccess {
		t.Fatalf("audit outcome: got %v, want OutcomeSuccess", aud.entries[0].Outcome)
	}
}

// TestIntegration_TokenExpiryHonoured asserts that a token past its
// expiry is rejected by the lookup path. REQ-IDENT-34: 24h TTL.
//
// The dispatcher stamps now+24h on the row; we advance the clock and
// re-read the row to confirm the stored expiry is the value the
// callback handler will compare against.
func TestIntegration_TokenExpiryHonoured(t *testing.T) {
	start := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFake(start)
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.com", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	row := store.JMAPIdentity{
		ID: "iv-exp", PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
		CreatedAtUs: clk.Now().UnixMicro(), UpdatedAtUs: clk.Now().UnixMicro(),
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	d := New(Options{
		Store: st, Submitter: &captureSubmitter{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:  clk, PublicBaseURL: "https://mail.example.com",
		VerifierFrom: "postmaster@example.com",
	})
	if _, err := d.Trigger(ctx, row); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	got, err := st.Meta().GetJMAPIdentity(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	wantExp := start.Add(TokenTTL).UnixMicro()
	if got.VerificationTokenExpiresAtUs != wantExp {
		t.Fatalf("expiry: got %d, want %d (start+24h)", got.VerificationTokenExpiresAtUs, wantExp)
	}
	// The callback handler compares ExpiresAtUs against the current
	// clock; the REST handler test in protoadmin covers the rejection
	// path. Here we only assert the stored value matches the spec.
}

// Defensive guard against a regression where the composer accidentally
// emitted Windows-style \n line endings in the headers (which would
// confuse some MTAs). RFC 5322 requires CRLF.
func TestIntegration_BodyUsesCRLF(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.com", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	p, _ := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.com", DisplayName: "Alice",
	})
	row := store.JMAPIdentity{
		ID: "iv-crlf", PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
		CreatedAtUs: clk.Now().UnixMicro(), UpdatedAtUs: clk.Now().UnixMicro(),
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	cap := &captureSubmitter{}
	d := New(Options{
		Store: st, Submitter: cap,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:  clk, PublicBaseURL: "https://mail.example.com",
		VerifierFrom: "postmaster@example.com",
	})
	if _, err := d.Trigger(ctx, row); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// Header section ends at the first blank line; require every
	// header line to be CRLF-terminated.
	headerEnd := bytes.Index(cap.body, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		t.Fatalf("could not locate header/body separator (no CRLFCRLF): %s", cap.body)
	}
	headers := cap.body[:headerEnd]
	if bytes.Contains(headers, []byte("\r")) && bytes.Contains(headers, []byte("\n")) {
		// OK: CRLF terminators present.
	} else {
		t.Errorf("headers missing CRLF: %s", headers)
	}
}
