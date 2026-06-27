package store

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// -- IMAP import types (REQ-IMAP-IMP-02, -15..19, -34, -42, -70, -74) ---

// IMAPImportTLSMode encodes the TLS connection mode for an upstream
// IMAP account.
type IMAPImportTLSMode string

const (
	// IMAPImportTLSModeSTARTTLS requires a STARTTLS upgrade after the
	// plaintext connection handshake.
	IMAPImportTLSModeSTARTTLS IMAPImportTLSMode = "starttls"
	// IMAPImportTLSModeImplicit connects with TLS from the first byte
	// (port 993 / IMAPS).
	IMAPImportTLSModeImplicit IMAPImportTLSMode = "implicit"
)

// IMAPImportAuthMethod encodes the credential type for an upstream
// IMAP account.
type IMAPImportAuthMethod string

const (
	// IMAPImportAuthMethodPassword is a plaintext password (cpanel /
	// bare-IMAP flow). REQ-IMAP-IMP-04.
	IMAPImportAuthMethodPassword IMAPImportAuthMethod = "password"
	// IMAPImportAuthMethodAppPassword is an app-specific password
	// (fastmail / gmail-with-2FA flow). REQ-IMAP-IMP-04.
	IMAPImportAuthMethodAppPassword IMAPImportAuthMethod = "app_password"
	// IMAPImportAuthMethodXOAuth2 is the OAuth2 XOAUTH2 SASL mechanism
	// (gmail / microsoft). REQ-IMAP-IMP-03.
	IMAPImportAuthMethodXOAuth2 IMAPImportAuthMethod = "xoauth2"
)

// IMAPImportAccountState is the lifecycle state of an upstream account
// worker.
type IMAPImportAccountState string

const (
	// IMAPImportAccountStateEnabled is "worker should be running".
	IMAPImportAccountStateEnabled IMAPImportAccountState = "enabled"
	// IMAPImportAccountStateDisabled is "worker stopped by user request".
	IMAPImportAccountStateDisabled IMAPImportAccountState = "disabled"
	// IMAPImportAccountStateErrored is "worker hit M consecutive failures
	// and has given up; user must re-enable" (REQ-IMAP-IMP-25).
	IMAPImportAccountStateErrored IMAPImportAccountState = "errored"
)

// IMAPImportSyncedFlags is a small bitfield that records the \Seen /
// \Flagged upstream state at the last sync (REQ-IMAP-IMP-34 /
// REQ-IMAP-IMP-42). Stored as an INTEGER column in both backends.
type IMAPImportSyncedFlags int32

const (
	// IMAPImportFlagSeen corresponds to the IMAP \Seen flag.
	IMAPImportFlagSeen IMAPImportSyncedFlags = 1 << iota
	// IMAPImportFlagFlagged corresponds to the IMAP \Flagged flag.
	IMAPImportFlagFlagged
)

// HasSeen reports whether the \Seen flag is set.
func (f IMAPImportSyncedFlags) HasSeen() bool { return f&IMAPImportFlagSeen != 0 }

// HasFlagged reports whether the \Flagged flag is set.
func (f IMAPImportSyncedFlags) HasFlagged() bool { return f&IMAPImportFlagFlagged != 0 }

// IMAPImportAccount is one row in the imapimport_account table. It
// carries the non-secret configuration for one upstream IMAP account
// plus a single sealed credential column (REQ-IMAP-IMP-02, decision 2).
//
// The non-secret/secret split mirrors store.IdentitySubmission.
type IMAPImportAccount struct {
	// ID is the opaque string primary key (32-hex UUID-shaped, generated
	// by the store on create).
	ID string
	// IdentityID is the owning JMAP Identity (decision 10,
	// REQ-IMAP-IMP-01/02). Empty for legacy principal-scoped rows that
	// predate the per-identity re-scope. Deleting the Identity cascades
	// to this row. An import account is 0-or-1 per identity (enforced by a
	// partial unique index).
	IdentityID string
	// PrincipalID is the owning principal, denormalised from the identity
	// for query convenience and the worker-pool fan-out (REQ-IMAP-IMP-02).
	PrincipalID PrincipalID
	// AccountName is the operator/user-visible label.
	AccountName string
	// Host is the upstream IMAP server hostname.
	Host string
	// Port is the upstream port (typically 993 for implicit, 143 for
	// starttls).
	Port int
	// TLSMode is the connection TLS strategy.
	TLSMode IMAPImportTLSMode
	// Username is the IMAP LOGIN / AUTHENTICATE username.
	Username string
	// AuthMethod encodes the credential type.
	AuthMethod IMAPImportAuthMethod
	// BackfillFloorDate is the optional floor for the initial historical
	// backfill (REQ-IMAP-IMP-15..16). nil means "all" (no floor).
	BackfillFloorDate *time.Time
	// CredentialCT is the AEAD-sealed credential (password / app-password /
	// OAuth refresh token) produced by internal/secrets.Seal. It carries
	// the "v1:" prefix (REQ-IMAP-IMP-70 / ValidateIMAPImportCredentialCT).
	CredentialCT []byte
	// State is the worker lifecycle state.
	State IMAPImportAccountState
	// LastSuccessAt is the most recent instant the worker completed a
	// fetch round successfully. nil when the account has never connected.
	LastSuccessAt *time.Time
	// LastError is the most recent connection or sync error string. Empty
	// when State == enabled and no errors have occurred.
	LastError string
	// DeletePropagates controls whether herold-side message destroys are
	// propagated upstream via IMAP EXPUNGE (REQ-IMAP-IMP-44). Default
	// true.
	DeletePropagates bool
	// CreatedAt / UpdatedAt are the row lifecycle timestamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IMAPImportAccountCreate carries the fields needed to create a new
// imapimport_account row. CredentialCT must carry the "v1:" prefix
// (validated by CreateIMAPImportAccount before insert).
type IMAPImportAccountCreate struct {
	// IdentityID is the owning JMAP Identity (decision 10). Optional: when
	// empty the row is principal-scoped only. When set it must reference an
	// existing jmap_identities row owned by PrincipalID; the store enforces
	// 0-or-1 per identity and returns ErrConflict on a duplicate.
	IdentityID        string
	PrincipalID       PrincipalID
	AccountName       string
	Host              string
	Port              int
	TLSMode           IMAPImportTLSMode
	Username          string
	AuthMethod        IMAPImportAuthMethod
	BackfillFloorDate *time.Time
	CredentialCT      []byte
	// State defaults to IMAPImportAccountStateEnabled when zero-valued.
	State            IMAPImportAccountState
	DeletePropagates bool
}

// IMAPImportAccountUpdate carries the fields that may be changed on an
// existing account row. CredentialCT, if non-nil, replaces the stored
// ciphertext and is re-validated before the write.
type IMAPImportAccountUpdate struct {
	// ID is the account to update (required).
	ID string
	// PrincipalID scopes the update to the owning principal (required).
	// The store rejects updates where the stored principal_id does not
	// match, returning ErrNotFound.
	PrincipalID PrincipalID
	// IdentityID is the owning JMAP Identity (decision 10). Carried so a
	// re-home (domain cutover) can move an account between identities; the
	// JMAP/admin patch paths preserve the existing value.
	IdentityID        string
	AccountName       string
	Host              string
	Port              int
	TLSMode           IMAPImportTLSMode
	Username          string
	AuthMethod        IMAPImportAuthMethod
	BackfillFloorDate *time.Time
	// CredentialCT, when non-nil, replaces the sealed credential. Must
	// carry the "v1:" prefix.
	CredentialCT     []byte
	State            IMAPImportAccountState
	DeletePropagates bool
}

// IMAPImportFolderMapEntry is one row in the imapimport_folder_map
// table: a mapping from one upstream IMAP folder name to a herold
// mailbox name for a given account (REQ-IMAP-IMP-10).
type IMAPImportFolderMapEntry struct {
	// AccountID is the owning imapimport_account.id.
	AccountID string
	// UpstreamFolder is the upstream IMAP folder name (verbatim,
	// case-sensitive as the upstream presents it).
	UpstreamFolder string
	// HeroldMailboxName is the herold mailbox to mirror messages into.
	HeroldMailboxName string
}

// IMAPImportFolderCursor tracks the IMAP synchronisation state for one
// (account, folder) pair. All cursor fields are read back from the
// upstream mailbox on every connect; durable persistence lets the worker
// resume incrementally after a restart (REQ-IMAP-IMP-74).
type IMAPImportFolderCursor struct {
	// AccountID is the owning imapimport_account.id.
	AccountID string
	// UpstreamFolder is the upstream IMAP folder name.
	UpstreamFolder string
	// UIDValidity is the IMAP UIDVALIDITY value. On mismatch the worker
	// forces a re-sync (REQ-IMAP-IMP-35).
	UIDValidity uint64
	// UIDNext is the last-known UIDNEXT from the upstream SELECT response.
	UIDNext uint64
	// LowWaterUID is the lowest UID fetched so far (the backfill floor
	// cursor, REQ-IMAP-IMP-17). Zero means the backfill has not started.
	LowWaterUID uint64
	// HighWaterUID is the highest UID fully synced forward.
	HighWaterUID uint64
	// HighestModSeq is the CONDSTORE HIGHESTMODSEQ at the last sync
	// (REQ-IMAP-IMP-24). Zero when the upstream does not advertise
	// CONDSTORE.
	HighestModSeq uint64
}

// IMAPImportMessageState maps one upstream (account, folder, UID)
// triple to the corresponding herold-side message and records the flags
// at the time of the last sync for conflict resolution
// (REQ-IMAP-IMP-34 / REQ-IMAP-IMP-42).
type IMAPImportMessageState struct {
	// AccountID is the owning imapimport_account.id.
	AccountID string
	// UpstreamFolder is the upstream IMAP folder name.
	UpstreamFolder string
	// UpstreamUID is the IMAP UID of the message in UpstreamFolder.
	UpstreamUID uint32
	// HeroldMessageID is the store MessageID of the mirrored message.
	HeroldMessageID MessageID
	// HeroldMailboxID is the store MailboxID into which the message was
	// placed.
	HeroldMailboxID MailboxID
	// LastSyncedFlags is the snapshot of \Seen / \Flagged at the time of
	// the last successful sync. Used as the "base" in upstream-authoritative
	// three-way conflict resolution (REQ-IMAP-IMP-42).
	LastSyncedFlags IMAPImportSyncedFlags
}

// ValidateIMAPImportCredentialCT returns an error when ct is non-nil
// but does not carry the "v1:" sealed-ciphertext prefix produced by
// internal/secrets.Seal. Nil / empty ct is allowed (the field is absent
// for this call). Mirrors ValidateIdentitySubmissionCTs.
//
// REQ-IMAP-IMP-70.
func ValidateIMAPImportCredentialCT(ct []byte) error {
	if len(ct) == 0 {
		return nil
	}
	if !bytes.HasPrefix(ct, v1CTPrefix) {
		return fmt.Errorf("%w: imapimport_account: credential_ct does not look like a sealed ciphertext (missing v1: prefix); refusing to store",
			ErrInvalidArgument)
	}
	return nil
}

// ParseBackfillHorizon converts the backfillHorizon string to an absolute
// floor date pointer (nil means "all" — no floor) using now as the
// reference time for relative forms. It is the single authoritative
// implementation used by both the admin REST handlers and the JMAP
// IMAPImport/set path. (REQ-IMAP-IMP-15, -16.)
//
// Accepted forms:
//   - "all"             — no floor; returns (nil, nil).
//   - "30d"/"90d"/"1y"  — relative durations; resolved to an absolute
//     truncated-to-midnight UTC date at now-duration.
//   - "YYYY-MM-DD"      — absolute date; parsed and returned UTC.
//
// Any other value returns a descriptive error.
func ParseBackfillHorizon(s string, now time.Time) (*time.Time, error) {
	if s == "all" {
		return nil, nil
	}
	// Relative forms: strings ending in "d" or "y".
	if strings.HasSuffix(s, "d") || strings.HasSuffix(s, "y") {
		var dur time.Duration
		switch s {
		case "30d":
			dur = 30 * 24 * time.Hour
		case "90d":
			dur = 90 * 24 * time.Hour
		case "180d":
			dur = 180 * 24 * time.Hour
		case "365d", "1y":
			dur = 365 * 24 * time.Hour
		case "2y":
			dur = 2 * 365 * 24 * time.Hour
		default:
			// Unrecognised relative token; fall through to absolute parse below.
			goto parseAbsolute
		}
		t := now.Add(-dur).UTC().Truncate(24 * time.Hour)
		return &t, nil
	}
parseAbsolute:
	// Absolute date "YYYY-MM-DD".
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf(
			"backfill_horizon must be %q, a relative form (30d/90d/1y), or an absolute date (YYYY-MM-DD); got %q",
			"all", s)
	}
	t = t.UTC()
	return &t, nil
}
