package directory

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// PrincipalID re-exports store.PrincipalID so callers of directory do not
// have to import internal/store for the common case.
type PrincipalID = store.PrincipalID

// Principal is the directory-visible view of a principal row. It is a
// narrow projection of store.Principal that omits credential material
// from routine reads (PasswordHash, TOTPSecret live in the store row
// but are not returned by List/Get on the directory surface).
type Principal struct {
	ID             PrincipalID
	CanonicalEmail string
	DisplayName    string
	Flags          store.PrincipalFlags
	TOTPEnabled    bool
}

// Sentinel errors returned by Directory methods. Callers classify with
// errors.Is; wrapping is via fmt.Errorf("...: %w", err).
var (
	// ErrUnauthorized is returned by Authenticate / VerifyTOTP when the
	// supplied credential does not match.
	ErrUnauthorized = errors.New("directory: unauthorized")

	// ErrNotFound is returned when the requested principal / alias does
	// not exist. It wraps store.ErrNotFound where appropriate.
	ErrNotFound = errors.New("directory: not found")

	// ErrConflict is returned when a uniqueness constraint fails (e.g.
	// duplicate canonical email on CreatePrincipal).
	ErrConflict = errors.New("directory: conflict")

	// ErrInvalidEmail is returned by CreatePrincipal when the supplied
	// address does not parse as a well-formed mailbox.
	ErrInvalidEmail = errors.New("directory: invalid email address")

	// ErrWeakPassword is returned by CreatePrincipal / UpdatePassword
	// when the supplied password is shorter than MinPasswordLength.
	ErrWeakPassword = errors.New("directory: password too weak")

	// ErrRateLimited is returned by Authenticate / VerifyTOTP when the
	// caller has exceeded the per-(email,source) failure budget.
	ErrRateLimited = errors.New("directory: rate limited")

	// ErrTOTPNotEnrolled is returned by ConfirmTOTP / VerifyTOTP when
	// the principal has no TOTP secret on record.
	ErrTOTPNotEnrolled = errors.New("directory: totp not enrolled")

	// ErrTOTPAlreadyEnabled is returned by EnrollTOTP when TOTP is
	// already confirmed for the principal.
	ErrTOTPAlreadyEnabled = errors.New("directory: totp already enabled")
)

// MinPasswordLength is the minimum accepted password length (REQ-AUTH-22).
// The breach-password list enforcement is deferred to a later wave.
const MinPasswordLength = 12

// Directory is the internal directory backend handle. It is safe for
// concurrent use; all per-request state lives in its arguments.
type Directory struct {
	meta   store.Metadata
	logger *slog.Logger
	clk    clock.Clock
	rand   io.Reader

	// rate limiter for auth failures
	rl *rateLimiter
}

// New constructs a Directory bound to the given metadata repository. The
// logger is used for audit and failure logs; never for secret material.
// clock supplies the time source for rate-limit windows and TOTP. rand
// seeds Argon2 salts and TOTP secrets — crypto/rand.Reader in prod, a
// deterministic reader in tests.
func New(meta store.Metadata, logger *slog.Logger, clk clock.Clock, rnd io.Reader) *Directory {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	// Register the auth collector set on Directory construction.
	// Idempotent across multiple Directory instances in tests.
	observe.RegisterAuthMetrics()
	return &Directory{
		meta:   meta,
		logger: logger,
		clk:    clk,
		rand:   rnd,
		rl:     newRateLimiter(clk),
	}
}

// CreatePrincipal hashes the password with Argon2id and persists a new
// principal row. The returned PrincipalID is the store-assigned stable
// key. Returns ErrInvalidEmail on a malformed address, ErrWeakPassword on
// a password shorter than MinPasswordLength, and ErrConflict if the
// canonical email is already taken.
func (d *Directory) CreatePrincipal(ctx context.Context, email, password string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	canon, err := canonicalizeEmail(email)
	if err != nil {
		return 0, err
	}
	if len(password) < MinPasswordLength {
		return 0, ErrWeakPassword
	}
	hash, err := hashPassword(d.rand, password)
	if err != nil {
		return 0, fmt.Errorf("directory: hash password: %w", err)
	}
	p, err := d.meta.InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: canon,
		PasswordHash:   hash,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return 0, fmt.Errorf("%w: %s", ErrConflict, canon)
		}
		return 0, fmt.Errorf("directory: insert principal: %w", err)
	}
	d.audit(ctx, p.ID, "principal.create", slog.String("email", canon))
	// Provision the standard mailbox set so JMAP / IMAP clients find an
	// INBOX immediately. Provisioning lives here (not in protoadmin)
	// because herold has multiple principal-creation entry points -- the
	// admin REST API, the bootstrap CLI, the upcoming OIDC autoprovision
	// flow -- and they all funnel through this method. A failure to
	// provision is logged but not surfaced as an error: the principal row
	// is committed, and the first SMTP delivery will recreate any missing
	// mailbox via the existing lazy ensureMailbox path in protosmtp.
	d.provisionDefaultMailboxes(ctx, p.ID)
	return p.ID, nil
}

// provisionDefaultMailboxes creates the standard mailbox set for a
// newly-created user principal: INBOX, Sent, Drafts, Trash, Junk,
// Archive. Each is tagged with its IMAP SPECIAL-USE attribute so JMAP
// clients can resolve role=inbox / role=drafts / etc. immediately.
//
// Errors are logged and ignored. ErrConflict is treated as success so
// the call is idempotent against re-runs (bootstrap re-invoked, OIDC
// autoprovision racing the admin REST path, etc.).
func (d *Directory) provisionDefaultMailboxes(ctx context.Context, pid PrincipalID) {
	specs := []struct {
		name string
		attr store.MailboxAttributes
	}{
		{"INBOX", store.MailboxAttrInbox},
		{"Sent", store.MailboxAttrSent},
		{"Drafts", store.MailboxAttrDrafts},
		{"Trash", store.MailboxAttrTrash},
		{"Junk", store.MailboxAttrJunk},
		{"Archive", store.MailboxAttrArchive},
	}
	for _, s := range specs {
		_, err := d.meta.InsertMailbox(ctx, store.Mailbox{
			PrincipalID: pid,
			Name:        s.name,
			Attributes:  s.attr,
		})
		if err == nil || errors.Is(err, store.ErrConflict) {
			continue
		}
		d.logger.Warn("directory.provision_mailbox_failed",
			"activity", observe.ActivityInternal,
			"principal_id", pid,
			"mailbox", s.name,
			"err", err,
		)
	}
}

// GetPrincipalByEmail resolves a principal by canonical email or alias.
// Returns ErrNotFound when the address does not belong here.
func (d *Directory) GetPrincipalByEmail(ctx context.Context, email string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	canon, err := canonicalizeEmail(email)
	if err != nil {
		return Principal{}, err
	}
	p, err := d.meta.GetPrincipalByEmail(ctx, canon)
	if err == nil {
		return principalFromStore(p), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return Principal{}, fmt.Errorf("directory: lookup principal: %w", err)
	}
	// Fall back to alias resolution.
	local, domain, ok := splitAddress(canon)
	if !ok {
		return Principal{}, fmt.Errorf("%w: %s", ErrNotFound, canon)
	}
	pid, aerr := d.meta.ResolveAlias(ctx, local, domain)
	if aerr != nil {
		if errors.Is(aerr, store.ErrNotFound) {
			return Principal{}, fmt.Errorf("%w: %s", ErrNotFound, canon)
		}
		return Principal{}, fmt.Errorf("directory: resolve alias: %w", aerr)
	}
	p, err = d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		return Principal{}, fmt.Errorf("directory: load principal after alias: %w", err)
	}
	return principalFromStore(p), nil
}

// ListPrincipals returns up to limit principals starting after the
// given cursor (zero for the first page), in ascending ID order.
// Non-positive limits apply the default of 100; the store enforces a
// hard cap of 1000.
func (d *Directory) ListPrincipals(ctx context.Context, limit int, after PrincipalID) ([]Principal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.meta.ListPrincipals(ctx, after, limit)
	if err != nil {
		return nil, fmt.Errorf("directory: list principals: %w", err)
	}
	out := make([]Principal, 0, len(rows))
	for _, p := range rows {
		out = append(out, principalFromStore(p))
	}
	return out, nil
}

// UpdatePassword replaces the principal's password hash. Requires the
// current password; returns ErrUnauthorized on mismatch.
func (d *Directory) UpdatePassword(ctx context.Context, pid PrincipalID, oldPassword, newPassword string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(newPassword) < MinPasswordLength {
		return ErrWeakPassword
	}
	p, err := d.meta.GetPrincipalByID(ctx, pid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return fmt.Errorf("directory: load principal: %w", err)
	}
	if !verifyPassword(p.PasswordHash, oldPassword) {
		return ErrUnauthorized
	}
	hash, err := hashPassword(d.rand, newPassword)
	if err != nil {
		return fmt.Errorf("directory: hash password: %w", err)
	}
	p.PasswordHash = hash
	if err := d.meta.UpdatePrincipal(ctx, p); err != nil {
		return fmt.Errorf("directory: update principal: %w", err)
	}
	d.audit(ctx, pid, "principal.password.change")
	return nil
}

// DeletePrincipal hard-deletes the principal and cascades every row
// that belongs to it (aliases, OIDC links, API keys, mailboxes,
// messages-in-mailboxes, state-change and audit-log entries). The
// underlying blobs are dereferenced; the blob GC sweep reclaims them
// once the grace window elapses. The audit entry lands after the
// cascade so the record itself survives the deletion (the row's
// Subject refers to the now-gone pid by value, not by FK).
func (d *Directory) DeletePrincipal(ctx context.Context, pid PrincipalID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.meta.DeletePrincipal(ctx, pid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: principal %d", ErrNotFound, pid)
		}
		return fmt.Errorf("directory: delete principal: %w", err)
	}
	d.audit(ctx, pid, "principal.delete")
	return nil
}

// Authenticator is the interface SASL mechanisms consume; *Directory
// satisfies it via its Authenticate method. Restated here (rather than
// pulled from internal/sasl) so directory has no inbound coupling.
type Authenticator interface {
	Authenticate(ctx context.Context, email, password string) (PrincipalID, error)
}

// AuthSource is an optional context value carrying the caller's source
// identifier (remote IP string) so rate limiting can key on it. Callers
// that omit it default to the bucket "-".
type authSourceKey struct{}

// WithAuthSource annotates ctx with the remote source string. SMTP/IMAP
// front-ends should populate it with the peer IP before calling
// Authenticate / VerifyTOTP.
func WithAuthSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, authSourceKey{}, source)
}

func authSource(ctx context.Context) string {
	if v, ok := ctx.Value(authSourceKey{}).(string); ok && v != "" {
		return v
	}
	return "-"
}

// Authenticate verifies an email + password pair. It returns
// ErrUnauthorized on mismatch, ErrRateLimited when the per-(email,source)
// failure budget is exhausted, or the principal ID on success.
func (d *Directory) Authenticate(ctx context.Context, email, password string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	canon, err := canonicalizeEmail(email)
	if err != nil {
		// Never reveal whether the address is malformed or unknown to
		// the caller; treat it like any other auth failure.
		d.rl.record(rlKey{email: strings.ToLower(strings.TrimSpace(email)), source: authSource(ctx)})
		observe.AuthAttemptsTotal.WithLabelValues("password", "fail").Inc()
		return 0, ErrUnauthorized
	}
	key := rlKey{email: canon, source: authSource(ctx)}
	if !d.rl.allow(key) {
		observe.AuthAttemptsTotal.WithLabelValues("password", "rate_limited").Inc()
		return 0, ErrRateLimited
	}
	p, err := d.meta.GetPrincipalByEmail(ctx, canon)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			d.rl.record(key)
			observe.AuthAttemptsTotal.WithLabelValues("password", "fail").Inc()
			return 0, ErrUnauthorized
		}
		return 0, fmt.Errorf("directory: load principal: %w", err)
	}
	if p.Flags&store.PrincipalFlagDisabled != 0 {
		d.rl.record(key)
		observe.AuthAttemptsTotal.WithLabelValues("password", "fail").Inc()
		return 0, ErrUnauthorized
	}
	if !verifyPassword(p.PasswordHash, password) {
		d.rl.record(key)
		observe.AuthAttemptsTotal.WithLabelValues("password", "fail").Inc()
		return 0, ErrUnauthorized
	}
	d.rl.clear(key)
	observe.AuthAttemptsTotal.WithLabelValues("password", "ok").Inc()
	return p.ID, nil
}

// ResolveAddress resolves local@domain to a PrincipalID by checking, in
// order: exact canonical-email match, alias, and (via the alias index)
// catch-all rows stored as '*' locals. Returns ErrNotFound when the
// address does not belong here.
func (d *Directory) ResolveAddress(ctx context.Context, local, domain string) (PrincipalID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	local = strings.ToLower(strings.TrimSpace(local))
	domain = strings.ToLower(strings.TrimSpace(domain))
	if local == "" || domain == "" {
		return 0, ErrInvalidEmail
	}
	// Exact canonical.
	p, err := d.meta.GetPrincipalByEmail(ctx, local+"@"+domain)
	if err == nil {
		if p.Flags&store.PrincipalFlagDisabled != 0 {
			return 0, fmt.Errorf("%w: %s@%s", ErrNotFound, local, domain)
		}
		return p.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("directory: canonical lookup: %w", err)
	}
	// Alias (also covers group expansion when the alias target is a group
	// principal; the caller uses principal.Kind to choose fanout).
	pid, err := d.meta.ResolveAlias(ctx, local, domain)
	if err == nil {
		return pid, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("directory: alias lookup: %w", err)
	}
	// Catch-all: stored with LocalPart = "*".
	pid, err = d.meta.ResolveAlias(ctx, "*", domain)
	if err == nil {
		return pid, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("directory: catch-all lookup: %w", err)
	}
	return 0, fmt.Errorf("%w: %s@%s", ErrNotFound, local, domain)
}

// audit records a mutation to the durable audit log (REQ-AUTH-62) and
// emits a matching INFO-level slog entry for real-time log collectors.
// The slog attrs are captured as structured Metadata on the audit
// entry so backend queries can filter on the same keys the logs
// surface.
func (d *Directory) audit(ctx context.Context, pid PrincipalID, action string, attrs ...slog.Attr) {
	merged := make([]slog.Attr, 0, 3+len(attrs))
	merged = append(merged,
		slog.String("activity", observe.ActivityAudit),
		slog.Uint64("principal_id", uint64(pid)),
		slog.String("action", action),
	)
	merged = append(merged, attrs...)
	d.logger.LogAttrs(ctx, slog.LevelInfo, "directory.audit", merged...)

	meta := make(map[string]string, len(attrs))
	for _, a := range attrs {
		meta[a.Key] = a.Value.String()
	}
	entry := store.AuditLogEntry{
		At:         d.clk.Now(),
		ActorKind:  store.ActorSystem,
		ActorID:    "system",
		Action:     action,
		Subject:    fmt.Sprintf("principal:%d", pid),
		RemoteAddr: authSource(ctx),
		Outcome:    store.OutcomeSuccess,
		Metadata:   meta,
	}
	if err := d.meta.AppendAuditLog(ctx, entry); err != nil {
		// Audit failure must not break the caller's mutation; we log
		// at WARN so the operator notices a silently-dropping audit
		// pipeline.
		d.logger.LogAttrs(ctx, slog.LevelWarn, "directory.audit.append_failed",
			slog.String("activity", observe.ActivityInternal),
			slog.String("action", action),
			slog.String("err", err.Error()))
	}
}

// canonicalizeEmail parses an address, lowercases, and trims.
func canonicalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidEmail, err)
	}
	canon := strings.ToLower(addr.Address)
	if _, _, ok := splitAddress(canon); !ok {
		return "", ErrInvalidEmail
	}
	return canon, nil
}

func splitAddress(canon string) (local, domain string, ok bool) {
	at := strings.LastIndexByte(canon, '@')
	if at <= 0 || at == len(canon)-1 {
		return "", "", false
	}
	return canon[:at], canon[at+1:], true
}

func principalFromStore(p store.Principal) Principal {
	return Principal{
		ID:             p.ID,
		CanonicalEmail: p.CanonicalEmail,
		DisplayName:    p.DisplayName,
		Flags:          p.Flags,
		TOTPEnabled:    p.Flags.Has(store.PrincipalFlagTOTPEnabled),
	}
}
