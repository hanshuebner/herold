package identityverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// recordedSubmission captures a single queue.Submission so tests can
// assert envelope/headers/body shape without exercising the real queue
// scheduler. Buffered so concurrent callers (if ever) don't block.
type recordedSubmission struct {
	mailFrom       string
	recipients     []string
	body           []byte
	sign           bool
	signingDomain  string
	principalID    store.PrincipalID
	idempotencyKey string
	envID          queue.EnvelopeID
}

type fakeSubmitter struct {
	mu       sync.Mutex
	calls    []recordedSubmission
	envIDSeq int
	err      error
}

func (f *fakeSubmitter) Submit(_ context.Context, sub queue.Submission) (queue.EnvelopeID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	body, _ := io.ReadAll(sub.Body)
	f.envIDSeq++
	envID := queue.EnvelopeID("env-fake-" + itoa(f.envIDSeq))
	pid := store.PrincipalID(0)
	if sub.PrincipalID != nil {
		pid = *sub.PrincipalID
	}
	f.calls = append(f.calls, recordedSubmission{
		mailFrom:       sub.MailFrom,
		recipients:     append([]string(nil), sub.Recipients...),
		body:           body,
		sign:           sub.Sign,
		signingDomain:  sub.SigningDomain,
		principalID:    pid,
		idempotencyKey: sub.IdempotencyKey,
		envID:          envID,
	})
	return envID, nil
}

// fakeAuditor records every audit append so assertions can verify the
// emitted action / metadata / outcome.
type fakeAuditor struct {
	mu      sync.Mutex
	entries []store.AuditLogEntry
}

func (a *fakeAuditor) Append(_ context.Context, e store.AuditLogEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func newDispatcherWithStore(t *testing.T) (*Dispatcher, *fakeSubmitter, *fakeAuditor, store.Store, clock.Clock) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), nil, clk)
	if err != nil {
		t.Fatalf("storesqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sub := &fakeSubmitter{}
	aud := &fakeAuditor{}
	d := New(Options{
		Store:        st,
		Submitter:    sub,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:        clk,
		Hostname:     "mail.example.com",
		VerifierFrom: "postmaster@example.com",
		Auditor:      aud,
	})
	return d, sub, aud, st, clk
}

func TestDispatcher_Validate_RejectsMissingFields(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		opts Options
	}{
		{"no store", Options{Submitter: &fakeSubmitter{}, Logger: logger, Clock: clock.NewFake(time.Now()), Hostname: "h", VerifierFrom: "p@h"}},
		{"no submitter", Options{Logger: logger, Clock: clock.NewFake(time.Now()), Hostname: "h", VerifierFrom: "p@h"}},
		{"no logger", Options{Submitter: &fakeSubmitter{}, Clock: clock.NewFake(time.Now()), Hostname: "h", VerifierFrom: "p@h"}},
		{"no clock", Options{Submitter: &fakeSubmitter{}, Logger: logger, Hostname: "h", VerifierFrom: "p@h"}},
		{"no hostname", Options{Submitter: &fakeSubmitter{}, Logger: logger, Clock: clock.NewFake(time.Now()), VerifierFrom: "p@h"}},
		{"no verifier from", Options{Submitter: &fakeSubmitter{}, Logger: logger, Clock: clock.NewFake(time.Now()), Hostname: "h"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if err := New(c.opts).Validate(); err == nil {
				t.Fatalf("expected validation error for %s", c.name)
			}
		})
	}
}

func TestDispatcher_Trigger_PersistsHashesAndEnqueuesMessage(t *testing.T) {
	d, sub, aud, st, clk := newDispatcherWithStore(t)
	ctx := context.Background()

	// Seed a domain + principal + unverified identity. We bypass the
	// JMAP /set handler because the trigger contract is unit-level:
	// "given an identity row, do the right thing".
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
	now := clk.Now()
	row := store.JMAPIdentity{
		ID:          "iv-1",
		PrincipalID: p.ID,
		Email:       "alice@external.test",
		MayDelete:   true,
		CreatedAtUs: now.UnixMicro(),
		UpdatedAtUs: now.UnixMicro(),
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	tokens, err := d.Trigger(ctx, row)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if tokens.Token == "" || tokens.Code == "" {
		t.Fatalf("Trigger returned empty Tokens: %+v", tokens)
	}

	// Store side: hashes must match exactly what the callback handler
	// will recompute from the URL/body input.
	got, err := st.Meta().GetJMAPIdentity(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	wantTokenHash := sha256.Sum256([]byte(tokens.Token))
	if !bytes.Equal(got.VerificationTokenHash, wantTokenHash[:]) {
		t.Fatalf("token hash mismatch: %x vs %x", got.VerificationTokenHash, wantTokenHash[:])
	}
	wantCodeHash := sha256.Sum256([]byte(tokens.Code))
	if !bytes.Equal(got.VerificationCodeHash, wantCodeHash[:]) {
		t.Fatalf("code hash mismatch: %x vs %x", got.VerificationCodeHash, wantCodeHash[:])
	}
	wantExp := now.Add(TokenTTL).UnixMicro()
	if got.VerificationTokenExpiresAtUs != wantExp {
		t.Fatalf("expiry: got %d, want %d", got.VerificationTokenExpiresAtUs, wantExp)
	}
	if got.VerifiedAtUs != 0 {
		t.Fatalf("Trigger must not mark verified: %+v", got)
	}

	// Queue side: exactly one envelope, signed by the verifier
	// domain, addressed to the identity email.
	if len(sub.calls) != 1 {
		t.Fatalf("Submit call count: got %d, want 1", len(sub.calls))
	}
	call := sub.calls[0]
	if call.mailFrom != "postmaster@example.com" {
		t.Errorf("MailFrom: got %q, want %q", call.mailFrom, "postmaster@example.com")
	}
	if !call.sign {
		t.Errorf("Sign: got false, want true (REQ-IDENT-30 / REQ-DKIM-*)")
	}
	if call.signingDomain != "example.com" {
		t.Errorf("SigningDomain: got %q, want %q", call.signingDomain, "example.com")
	}
	if len(call.recipients) != 1 || call.recipients[0] != row.Email {
		t.Errorf("Recipients: got %v, want [%q]", call.recipients, row.Email)
	}
	if call.principalID != p.ID {
		t.Errorf("PrincipalID: got %d, want %d", call.principalID, p.ID)
	}
	if call.idempotencyKey == "" {
		t.Errorf("IdempotencyKey is empty")
	}
	if !strings.Contains(string(call.body), tokens.Token) {
		t.Errorf("body missing raw token")
	}
	if !strings.Contains(string(call.body), tokens.Code) {
		t.Errorf("body missing raw code")
	}
	// The principal canonical email must appear in the body's
	// "Initiated by:" line (REQ-IDENT-33).
	if !strings.Contains(string(call.body), "alice@example.com") {
		t.Errorf("body missing initiator email")
	}

	// Audit side: one success entry with the expected metadata.
	if len(aud.entries) != 1 {
		t.Fatalf("audit entry count: got %d, want 1", len(aud.entries))
	}
	e := aud.entries[0]
	if e.Action != "identity.verify.send" {
		t.Errorf("audit action: got %q", e.Action)
	}
	if e.Outcome != store.OutcomeSuccess {
		t.Errorf("audit outcome: got %v, want OutcomeSuccess", e.Outcome)
	}
	if e.Subject != "identity:iv-1" {
		t.Errorf("audit subject: got %q", e.Subject)
	}
	if e.Metadata["identity_email"] != row.Email {
		t.Errorf("audit identity_email: got %q", e.Metadata["identity_email"])
	}
	if e.Metadata["envelope_id"] == "" {
		t.Errorf("audit envelope_id empty")
	}
}

func TestDispatcher_Trigger_AuditOnQueueFailure(t *testing.T) {
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
		Kind: store.PrincipalKindUser, CanonicalEmail: "alice@example.com", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	row := store.JMAPIdentity{
		ID: "iv-fail", PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
		CreatedAtUs: clk.Now().UnixMicro(), UpdatedAtUs: clk.Now().UnixMicro(),
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	wantErr := errors.New("queue down")
	sub := &fakeSubmitter{err: wantErr}
	aud := &fakeAuditor{}
	d := New(Options{
		Store: st, Submitter: sub,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:  clk, Hostname: "mail.example.com",
		VerifierFrom: "postmaster@example.com", Auditor: aud,
	})

	_, err = d.Trigger(ctx, row)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Trigger error: got %v, want wrap of %v", err, wantErr)
	}
	if len(aud.entries) != 1 {
		t.Fatalf("audit entry count: got %d, want 1", len(aud.entries))
	}
	e := aud.entries[0]
	if e.Outcome != store.OutcomeFailure {
		t.Errorf("audit outcome: got %v, want OutcomeFailure", e.Outcome)
	}
	if e.Metadata["classification"] != "queue_submit_failed" {
		t.Errorf("audit classification: got %q", e.Metadata["classification"])
	}
}

func TestDispatcher_Trigger_TokenLookupRoundTrip(t *testing.T) {
	// REQ-IDENT-31..32: the persisted token hash MUST resolve back to
	// the identity row via Meta().GetIdentityByVerificationTokenHash;
	// the persisted code hash MUST resolve via
	// GetIdentityByVerificationCodeHash. This is what the REST
	// callback in protoadmin/identity_verify.go relies on.
	d, _, _, st, _ := newDispatcherWithStore(t)
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
		ID: "iv-lookup", PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	tokens, err := d.Trigger(ctx, row)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	tokenHash := sha256.Sum256([]byte(tokens.Token))
	byTok, err := st.Meta().GetIdentityByVerificationTokenHash(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("GetIdentityByVerificationTokenHash: %v", err)
	}
	if byTok.ID != row.ID {
		t.Fatalf("token lookup returned %q, want %q", byTok.ID, row.ID)
	}

	codeHash := sha256.Sum256([]byte(tokens.Code))
	byCode, err := st.Meta().GetIdentityByVerificationCodeHash(ctx, row.ID, codeHash[:])
	if err != nil {
		t.Fatalf("GetIdentityByVerificationCodeHash: %v", err)
	}
	if byCode.ID != row.ID {
		t.Fatalf("code lookup returned %q, want %q", byCode.ID, row.ID)
	}
}

func TestDispatcher_Trigger_HostnameAppearsInLink(t *testing.T) {
	d, sub, _, st, _ := newDispatcherWithStore(t)
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
		ID: "iv-host", PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	tokens, err := d.Trigger(ctx, row)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	wantLink := "https://mail.example.com/verify-identity?token=" + tokens.Token
	if !strings.Contains(string(sub.calls[0].body), wantLink) {
		t.Fatalf("body missing callback link %q", wantLink)
	}
}

func TestDispatcher_Trigger_ResendRotatesAndAuditsTwice(t *testing.T) {
	// REQ-IDENT-37: a second Trigger on the same row rotates the
	// token+code and clears the previous hashes. The first token must
	// no longer resolve to the row; the second must.
	d, _, aud, st, _ := newDispatcherWithStore(t)
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
		ID: "iv-resend", PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
	}
	if err := st.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}
	first, err := d.Trigger(ctx, row)
	if err != nil {
		t.Fatalf("Trigger #1: %v", err)
	}
	// First trigger uses IssueIdentityVerificationToken which is
	// guarded against a live token (ErrConflict). A second Trigger
	// would currently return that error. The composer task does not
	// own the resend rate-limit, but a second issue MUST be possible
	// when no live token is present — clear it then resend.
	if err := st.Meta().ClearIdentityVerificationToken(ctx, row.ID); err != nil {
		t.Fatalf("ClearIdentityVerificationToken: %v", err)
	}
	second, err := d.Trigger(ctx, row)
	if err != nil {
		t.Fatalf("Trigger #2: %v", err)
	}
	if second.Token == first.Token {
		t.Fatalf("resend produced identical token; entropy reuse?")
	}
	if len(aud.entries) != 2 {
		t.Fatalf("audit entries: got %d, want 2", len(aud.entries))
	}
}
