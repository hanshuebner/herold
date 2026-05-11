package identityverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

// TokenTTL is the lifespan of a verification token (REQ-IDENT-34).
const TokenTTL = 24 * time.Hour

// Submitter is the narrow seam Dispatcher uses to enqueue the
// verification message. Production wires *queue.Queue (which satisfies
// this method shape); tests inject a recorder.
type Submitter interface {
	Submit(ctx context.Context, sub queue.Submission) (queue.EnvelopeID, error)
}

// Auditor is the narrow audit-log seam. The dispatcher emits an
// identity.verify.send entry for each enqueued message so operators
// can correlate the outbound dispatch with the user-visible state on
// REQ-IDENT-43 / REQ-IDENT-90.
type Auditor interface {
	Append(ctx context.Context, entry store.AuditLogEntry) error
}

// Resolver is the narrow seam the dispatcher uses to look up the
// requesting principal's canonical email for the body's "Initiated
// by:" line (REQ-IDENT-33). Production wires store.Meta(); tests
// inject a stub. The method returns ErrNotFound when the principal
// row is missing.
type Resolver interface {
	GetPrincipalByID(ctx context.Context, id store.PrincipalID) (store.Principal, error)
}

// Tokens captures the issued raw token + 6-digit code so callers (or
// tests) can re-derive the persisted hashes. Production code never
// reads these — the dispatcher persists and forgets — but the
// integration tests rely on the raw values to drive a follow-up
// /verify-identity?token=... callback.
type Tokens struct {
	Token string
	Code  string
}

// Options bundles the dispatcher's runtime configuration.
type Options struct {
	// Store is the persistent metadata sink. Required; the dispatcher
	// calls IssueIdentityVerificationToken and reads the requesting
	// principal row.
	Store store.Store
	// Submitter is the outbound queue seam. Required.
	Submitter Submitter
	// Logger is the structured logger. Required.
	Logger *slog.Logger
	// Clock is the time source for token expiry and Date: headers.
	// Required; production wires the real clock.
	Clock clock.Clock
	// Hostname is the public-listener hostname used in the
	// verification URL and Message-ID (REQ-IDENT-30). Required.
	Hostname string
	// VerifierFrom is the envelope MAIL FROM + header From: address
	// for verification messages (REQ-IDENT-30; default
	// postmaster@<canonical hosted domain>). Required: when empty,
	// the trigger fails closed and logs.
	VerifierFrom string
	// Auditor receives identity.verify.send entries. Optional; when
	// nil the trigger still completes but the audit-log entry is
	// dropped. Production wires the metadata store; tests may inject
	// a recorder.
	Auditor Auditor
}

// Dispatcher generates the verification token+code pair, persists the
// hashes on the identity row, composes the email body, and enqueues
// the message for outbound delivery.
type Dispatcher struct {
	opts Options
}

// New builds a Dispatcher. Caller must verify opts via Validate before
// the trigger fires; New does not panic on missing fields so tests can
// observe Validate's error path.
func New(opts Options) *Dispatcher {
	return &Dispatcher{opts: opts}
}

// Validate returns a non-nil error when a required Option is missing.
// Called by the trigger before any side effect; failures are logged
// and the create call still succeeds (REQ-IDENT-30 best-effort).
func (d *Dispatcher) Validate() error {
	if d.opts.Store == nil {
		return fmt.Errorf("identityverify: Store is required")
	}
	if d.opts.Submitter == nil {
		return fmt.Errorf("identityverify: Submitter is required")
	}
	if d.opts.Logger == nil {
		return fmt.Errorf("identityverify: Logger is required")
	}
	if d.opts.Clock == nil {
		return fmt.Errorf("identityverify: Clock is required")
	}
	if strings.TrimSpace(d.opts.Hostname) == "" {
		return fmt.Errorf("identityverify: Hostname is required")
	}
	if strings.TrimSpace(d.opts.VerifierFrom) == "" {
		return fmt.Errorf("identityverify: VerifierFrom is required")
	}
	return nil
}

// Trigger is the VerificationTrigger callback installed via
// jmapidentity.Options. Returns an error when the dispatch failed in
// a way the caller might want to surface; the caller (the JMAP /set
// handler) logs the error but still reports create-success per
// REQ-IDENT-30. The returned Tokens carry the raw values for tests;
// production callers ignore them.
//
// The trigger consults the dispatcher's clock for the expiry instant
// and the Date: header. A trigger that races with the GC pass
// (REQ-IDENT-35) is harmless: a token that has already been GC'd
// before the email reaches the user is observable as an "invalid
// link" failure on the callback endpoint, and the user can resend
// from the SPA.
func (d *Dispatcher) Trigger(ctx context.Context, row store.JMAPIdentity) (Tokens, error) {
	if err := d.Validate(); err != nil {
		d.logFailure(ctx, row.ID, "config_invalid", err)
		return Tokens{}, err
	}

	token, tokenBytes, err := GenerateToken()
	if err != nil {
		d.logFailure(ctx, row.ID, "token_generation_failed", err)
		return Tokens{}, err
	}
	code, err := GenerateCode()
	if err != nil {
		d.logFailure(ctx, row.ID, "code_generation_failed", err)
		return Tokens{}, err
	}

	tokenHash := sha256.Sum256([]byte(token))
	codeHash := sha256.Sum256([]byte(code))
	// Cross-check the token bytes hash to the same value as the
	// base64url-encoded ASCII — the store stores sha256 of the *raw*
	// token (REQ-IDENT-31 wording is ambiguous between the encoded
	// and raw bytes, but the callback handler hashes the URL query
	// string verbatim, which is the encoded form). Keep tokenBytes
	// for the test surface; the persisted hash matches the callback.
	_ = tokenBytes

	now := d.opts.Clock.Now()
	expiresAt := now.Add(TokenTTL).UnixMicro()
	if err := d.opts.Store.Meta().IssueIdentityVerificationToken(
		ctx, row.ID, tokenHash[:], codeHash[:], expiresAt); err != nil {
		d.logFailure(ctx, row.ID, "token_persist_failed", err)
		return Tokens{}, err
	}

	initiator, err := d.resolveInitiator(ctx, row.PrincipalID)
	if err != nil {
		d.opts.Logger.WarnContext(ctx, "identityverify.initiator_unresolved",
			slog.String("subsystem", "identityverify"),
			slog.String("identity_id", row.ID),
			slog.Uint64("principal_id", uint64(row.PrincipalID)),
			slog.String("err", err.Error()))
		initiator = ""
	}

	composed, err := Compose(ComposeInput{
		From:           d.opts.VerifierFrom,
		To:             row.Email,
		Token:          token,
		Code:           code,
		Hostname:       d.opts.Hostname,
		InitiatorEmail: initiator,
		Now:            now,
	})
	if err != nil {
		d.logFailure(ctx, row.ID, "compose_failed", err)
		return Tokens{}, err
	}

	signingDomain := domainOf(d.opts.VerifierFrom)
	pid := row.PrincipalID
	sub := queue.Submission{
		PrincipalID:   &pid,
		MailFrom:      d.opts.VerifierFrom,
		Recipients:    []string{row.Email},
		Body:          bytes.NewReader(composed.Bytes),
		Sign:          true,
		SigningDomain: signingDomain,
		// IdempotencyKey ties retries to one envelope. The row ID is
		// stable across resends within one token issuance; if the
		// caller resends (REQ-IDENT-37) the token rotates and the
		// hash changes, so the IdempotencyKey rotates with it.
		IdempotencyKey: fmt.Sprintf("identity-verify:%s:%x", row.ID, tokenHash[:8]),
	}
	envID, err := d.opts.Submitter.Submit(ctx, sub)
	if err != nil {
		d.logFailure(ctx, row.ID, "queue_submit_failed", err)
		return Tokens{}, err
	}

	d.opts.Logger.InfoContext(ctx, "identityverify.queued",
		slog.String("subsystem", "identityverify"),
		slog.String("identity_id", row.ID),
		slog.String("envelope_id", string(envID)),
		slog.String("message_id", composed.MessageID),
		slog.String("recipient", row.Email),
		slog.String("verifier_from", d.opts.VerifierFrom),
	)
	d.audit(ctx, row, store.OutcomeSuccess, "queued", map[string]string{
		"envelope_id": string(envID),
		"message_id":  composed.MessageID,
		"recipient":   row.Email,
		"result":      "queued",
	})
	return Tokens{Token: token, Code: code}, nil
}

// resolveInitiator fetches the requesting principal's canonical email.
// Returns ErrNotFound (or the underlying store error) when the
// principal row cannot be read; the dispatcher then falls back to an
// "(unknown)" initiator marker in the body and logs the warning.
func (d *Dispatcher) resolveInitiator(ctx context.Context, pid store.PrincipalID) (string, error) {
	if pid == 0 {
		return "", nil
	}
	p, err := d.opts.Store.Meta().GetPrincipalByID(ctx, pid)
	if err != nil {
		return "", err
	}
	return p.CanonicalEmail, nil
}

// logFailure writes a structured failure line and audit entry for one
// dispatch attempt that bailed before queue submission.
func (d *Dispatcher) logFailure(ctx context.Context, identityID, classification string, err error) {
	if d.opts.Logger != nil {
		d.opts.Logger.ErrorContext(ctx, "identityverify.dispatch_failed",
			slog.String("subsystem", "identityverify"),
			slog.String("identity_id", identityID),
			slog.String("classification", classification),
			slog.String("err", errMsg(err)),
		)
	}
	d.audit(ctx, store.JMAPIdentity{ID: identityID}, store.OutcomeFailure,
		errMsg(err), map[string]string{
			"identity_id":    identityID,
			"classification": classification,
			"result":         "failure",
		})
}

// audit writes an identity.verify.send entry via the configured
// Auditor. When the auditor is nil the entry is dropped (the slog
// line still survives in the structured log).
func (d *Dispatcher) audit(
	ctx context.Context,
	row store.JMAPIdentity,
	outcome store.AuditOutcome,
	message string,
	meta map[string]string,
) {
	if d.opts.Auditor == nil {
		return
	}
	if meta == nil {
		meta = map[string]string{}
	}
	if row.Email != "" {
		meta["identity_email"] = row.Email
	}
	if row.PrincipalID != 0 {
		meta["principal_id"] = fmt.Sprintf("%d", row.PrincipalID)
	}
	entry := store.AuditLogEntry{
		At:        d.opts.Clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "system",
		Action:    "identity.verify.send",
		Subject:   fmt.Sprintf("identity:%s", row.ID),
		Outcome:   outcome,
		Message:   message,
		Metadata:  meta,
	}
	if err := d.opts.Auditor.Append(ctx, entry); err != nil {
		d.opts.Logger.WarnContext(ctx, "identityverify.audit_append_failed",
			slog.String("subsystem", "identityverify"),
			slog.String("identity_id", row.ID),
			slog.String("err", err.Error()))
	}
}

// errMsg renders err as a string without panicking on nil.
func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// domainOf extracts the lowercased domain portion of an addr-spec.
// Returns "" when no @ is present.
func domainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}
