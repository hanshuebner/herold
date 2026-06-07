package store

import "time"

// FileShareSource carries the message back-reference captured at
// confirmation (REQ-SHARE-04). All fields are optional: a zero-value
// FileShareSource means "no source context was provided", which leaves
// the corresponding database columns NULL.
//
// The source is a snapshot, not a live join: it survives deletion of the
// originating message. source_message_id is a soft reference (no FK).
//
// The recipient-facing surfaces MUST NOT expose FileShareSource fields.
type FileShareSource struct {
	// MessageID is the JMAP Email id / store message id of the originating
	// message. Empty string means the caller did not supply a message id.
	MessageID string
	// Subject is the subject line snapshot taken at confirmation time.
	// Empty string means the caller did not supply a subject.
	Subject string
	// Recipients is the snapshot of envelope recipient addresses (To + Cc
	// + Bcc) at confirmation time. Nil/empty slice means the caller did
	// not supply recipients. Stored as a JSON array in source_recipients.
	Recipients []string
}

// IsZero reports whether the FileShareSource carries no context at all:
// all fields empty/nil. A zero source does not update the stored columns
// on a re-confirm.
func (s FileShareSource) IsZero() bool {
	return s.MessageID == "" && s.Subject == "" && len(s.Recipients) == 0
}

// FileShareState is the lifecycle state of a FileShare row.
// REQ-SHARE-20.
type FileShareState string

const (
	// FileShareStatePending is the initial state: the share has been
	// created but not yet confirmed (i.e. the carrying message has not
	// yet been submitted). Pending shares expire at pending_ttl.
	FileShareStatePending FileShareState = "pending"

	// FileShareStateActive is the state after confirmation: the share
	// is publicly accessible (subject to expiry / max_downloads /
	// password gating). Active shares expire at expires_at.
	FileShareStateActive FileShareState = "active"

	// FileShareStateRevoked is the terminal state: the owner or an
	// admin has revoked the share. The row lingers until the sweeper
	// removes it after revoked_grace so the management view can display
	// "revoked". REQ-SHARE-22.
	FileShareStateRevoked FileShareState = "revoked"
)

// FileShare is one row in the file_shares table. REQ-SHARE-02.
type FileShare struct {
	// ID is the capability token: a CSPRNG-derived, URL-safe,
	// base64url-encoded string with >= 128 bits of entropy. It is the
	// only credential authorising access to the file. REQ-SHARE-11b.
	ID string
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// BlobHash is the BLAKE3 hex hash of the content-addressed blob
	// (REQ-STORE-10). Together with BlobSize it forms a BlobRef.
	BlobHash string
	// BlobSize is the byte length of the blob.
	BlobSize int64
	// Filename is the name presented to the recipient on the landing
	// page and in Content-Disposition. Stored verbatim.
	Filename string
	// ContentType is the MIME type served on download. Stored verbatim;
	// the HTTP handler applies the HTML/SVG override (REQ-SHARE-63).
	ContentType string
	// CreatedAt is the row insert instant.
	CreatedAt time.Time
	// ExpiresAt is the absolute expiry. State pending: now+pending_ttl.
	// State active: now+default_ttl (clamped to max_ttl). REQ-SHARE-20.
	ExpiresAt time.Time
	// MaxDownloads is the optional download cap. When non-nil the share
	// becomes inaccessible once DownloadCount >= MaxDownloads.
	MaxDownloads *int64
	// DownloadCount is the number of times the blob has been downloaded
	// via the public route. Incremented atomically by RecordDownload.
	DownloadCount int64
	// PasswordHash is the Argon2id PHC string of the optional password.
	// NULL / empty means no password is required. REQ-SHARE-11c.
	// This field is never returned to callers outside the store; JMAP
	// surfaces HasPassword (bool) only (REQ-SHARE-43).
	PasswordHash string
	// State is the current lifecycle state. REQ-SHARE-20.
	State FileShareState
	// LastDownloadedAt is the instant of the most recent download, or
	// the zero value when no download has occurred. REQ-SHARE-02.
	LastDownloadedAt *time.Time
	// RevokedAt is the instant the share was revoked, or the zero value
	// when not revoked. REQ-SHARE-22.
	RevokedAt *time.Time
	// SourceMessageID is the JMAP Email id / store message id of the
	// originating message, or empty when no back-reference was captured.
	// Populated atomically on the pending -> active transition.
	// REQ-SHARE-04.
	SourceMessageID string
	// SourceSubject is the subject line snapshot at confirmation time, or
	// empty when no subject was captured. REQ-SHARE-04.
	SourceSubject string
	// SourceRecipients is the snapshot of envelope recipient addresses
	// (To + Cc + Bcc) at confirmation time, or nil when no recipients
	// were captured. Decoded from the JSON array stored in
	// source_recipients. REQ-SHARE-04.
	SourceRecipients []string
}

// FileShareCreate carries the parameters for FileShares.Create.
// All blob and identity information must be resolved by the caller
// (JMAP layer) before calling Create; the store does not perform any
// blobId resolution itself.
type FileShareCreate struct {
	// PrincipalID is the owning principal.
	PrincipalID PrincipalID
	// BlobHash + BlobSize identify the content-addressed blob.
	BlobHash string
	BlobSize int64
	// Filename is presented to the recipient (Content-Disposition).
	Filename string
	// ContentType is served verbatim on download (HTML/SVG override
	// lives in the HTTP handler per REQ-SHARE-63).
	ContentType string
	// PendingTTL controls how long the share stays in pending state
	// before the sweeper removes it. Populated from
	// FileSharesConfig.PendingTTL by the JMAP layer.
	PendingTTL time.Duration
	// MaxDownloads is the optional download cap. Nil means unlimited.
	MaxDownloads *int64
	// Password, when non-empty, is hashed with Argon2id and stored
	// in password_hash. REQ-SHARE-11c.
	Password string
	// Config carries the per-deployment caps and TTL values that the
	// store enforces atomically during Create.
	Config FileSharesConfig
}

// FileSharesConfig carries the operator-supplied policy values that
// FileShares methods enforce. The wiring layer populates this from
// sysconfig so the store package never reads sysconfig directly.
// REQ-SHARE-12, REQ-SHARE-50, REQ-SHARE-51.
type FileSharesConfig struct {
	// DefaultTTL is the active share lifetime granted on Confirm.
	DefaultTTL time.Duration
	// MaxTTL is the ceiling the owner cannot exceed when shortening or
	// confirming. Confirm clamps ExpiresAt to now+MaxTTL.
	MaxTTL time.Duration
	// PendingTTL is the unconfirmed share lifetime. Sweeper deletes
	// pending rows older than this.
	PendingTTL time.Duration
	// RevokedGrace is how long a revoked share row lingers before the
	// sweeper deletes it. REQ-SHARE-22.
	RevokedGrace time.Duration
	// MaxSharesPerPrincipal is the hard cap on active+pending shares
	// per principal. REQ-SHARE-12.
	MaxSharesPerPrincipal int64
	// ShareQuotaPerPrincipal is the per-principal byte quota measured
	// over the set of DISTINCT blob hashes referenced by the
	// principal's shares (dedup-aware). REQ-SHARE-50.
	ShareQuotaPerPrincipal int64
}

// SweepStats carries the per-reason deletion counts returned by
// FileShares.Sweep. REQ-SHARE-23.
type SweepStats struct {
	// DeletedPending is the number of pending shares removed because
	// they exceeded pending_ttl.
	DeletedPending int64
	// DeletedExpired is the number of active shares removed because
	// their expires_at passed.
	DeletedExpired int64
	// DeletedRevoked is the number of revoked shares removed because
	// they exceeded revoked_grace.
	DeletedRevoked int64
}

// FileShareListFilter controls pagination for FileShares.ListByPrincipal.
type FileShareListFilter struct {
	// State, when non-empty, restricts the result to shares in that
	// state. An empty string returns all states.
	State FileShareState
	// AfterID, when non-empty, returns only rows with ID > AfterID
	// (lexicographic; stable because IDs are base64url CSPRNG values
	// — not sequential — but the ordering is still deterministic once
	// issued). Zero value starts from the first row.
	AfterID string
	// Limit caps the number of returned rows. Values <= 0 are treated
	// as 100; values > 1000 are silently lowered to 1000.
	Limit int
}
