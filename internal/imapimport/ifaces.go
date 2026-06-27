package imapimport

import (
	"context"
	"time"

	imap "github.com/emersion/go-imap/v2"
)

// folderInfo carries the minimal per-folder data returned by LIST.
type folderInfo struct {
	// Name is the upstream folder name.
	Name string
	// Attrs is the set of IMAP mailbox attributes (e.g. \NoSelect,
	// \HasNoChildren).
	Attrs []imap.MailboxAttr
}

// selectInfo carries the key fields from a SELECT / EXAMINE response.
type selectInfo struct {
	// UIDValidity is the upstream UIDVALIDITY value.
	UIDValidity uint32
	// UIDNext is the next UID that will be assigned.
	UIDNext imap.UID
	// HighestModSeq is the CONDSTORE HIGHESTMODSEQ (0 when unsupported).
	HighestModSeq uint64
	// NumMessages is the number of messages in the mailbox.
	NumMessages uint32
}

// fetchedMessage is one message returned by UIDFetch.
type fetchedMessage struct {
	// UID is the upstream IMAP UID.
	UID imap.UID
	// Flags is the upstream system-flag set.
	Flags []imap.Flag
	// InternalDate is the upstream INTERNALDATE.
	InternalDate time.Time
	// RFC822 is the raw message bytes. Using BODY.PEEK[] so \Seen is
	// NOT set upstream (REQ-IMAP-IMP-31 byte-fidelity + non-mutation).
	RFC822 []byte
}

// uidFlags carries the flag state of one upstream message, optionally with its
// CONDSTORE MODSEQ. Returned by the flag down-sync fetches
// (UIDFetchFlagsMulti / UIDFetchFlagsChangedSince) used to apply upstream-only
// \Seen / \Flagged changes back to herold (REQ-IMAP-IMP-40/24).
type uidFlags struct {
	// UID is the upstream IMAP UID.
	UID imap.UID
	// Flags is the upstream system-flag set for the message.
	Flags []imap.Flag
	// ModSeq is the message's per-message MODSEQ (CONDSTORE). Zero when the
	// upstream does not advertise CONDSTORE.
	ModSeq uint64
}

// idleHandle is the handle returned by Conn.Idle. The worker calls
// Wait to block until an unsolicited update arrives, and Close to stop
// IDLE and return to the command-capable state.
type idleHandle interface {
	// Wait blocks until the IDLE command ends — either because Close
	// was called (nil return) or because the server sent a BYE / the
	// connection dropped (non-nil return).
	Wait() error

	// Close stops the IDLE command. Blocks until DONE has been written.
	// Returns nil on a clean stop. After Close returns, the connection is
	// in the command-capable (authenticated) state again.
	Close() error
}

// Conn is the minimal interface the accountWorker needs from an IMAP
// client connection. Each sub-step extends the set of methods the
// production dialer satisfies; the fake in tests implements the same
// interface.
type Conn interface {
	// Caps returns the capability set advertised by the upstream after
	// login. The result is stable for the connection lifetime.
	Caps() imap.CapSet

	// Logout sends IMAP LOGOUT and waits for the server's BYE.
	// Returns nil on a clean logout. Callers must still call Close.
	Logout() error

	// Close releases the underlying network connection. Idempotent;
	// safe to call after Logout or on an already-closed connection.
	Close() error

	// List issues LIST "" "*" and returns all folders with their attributes.
	// REQ-IMAP-IMP-10/12.
	List(ctx context.Context) ([]folderInfo, error)

	// Select opens the named mailbox read-only (EXAMINE). Returns the
	// key fields from the SELECT response: UIDVALIDITY, UIDNEXT,
	// HIGHESTMODSEQ, NUM_MESSAGES. REQ-IMAP-IMP-24/35.
	Select(ctx context.Context, mailbox string) (selectInfo, error)

	// SelectReadWrite opens the named mailbox read-write (SELECT, not
	// EXAMINE). Used by the write-back path which needs to issue STORE /
	// EXPUNGE. Returns the same selectInfo shape. REQ-IMAP-IMP-40..44.
	SelectReadWrite(ctx context.Context, mailbox string) (selectInfo, error)

	// UIDSearchSince returns UIDs of messages whose INTERNALDATE is on
	// or after `since`. If since is zero the entire mailbox is returned
	// (no SINCE criterion). REQ-IMAP-IMP-17/19.
	UIDSearchSince(ctx context.Context, since time.Time) ([]imap.UID, error)

	// UIDFetch fetches FLAGS, INTERNALDATE, and BODY.PEEK[] for each UID
	// in uids. Uses BODY.PEEK so the upstream \Seen flag is NOT set.
	// REQ-IMAP-IMP-31.
	UIDFetch(ctx context.Context, uids []imap.UID) ([]fetchedMessage, error)

	// UIDFetchEnvelope fetches ENVELOPE, UID, INTERNALDATE, and FLAGS for
	// the given UIDs WITHOUT fetching the message body. This is used by the
	// Gmail All Mail envelope-dedup path to cheaply determine which messages
	// are already mirrored before committing to a body download.
	// The returned MessageID is the normalized RFC 5322 Message-ID (without
	// angle brackets), or "" if the message has no Message-ID header.
	UIDFetchEnvelope(ctx context.Context, uids []imap.UID) ([]envelopeFetchResult, error)

	// UIDFetchFlags fetches FLAGS only for the given UID. Returns nil, nil
	// when the message does not exist in the currently-selected mailbox.
	// Used by the write-back reconcile path to read the upstream-current
	// flags before deciding whether to push. REQ-IMAP-IMP-42.
	UIDFetchFlags(ctx context.Context, uid imap.UID) ([]imap.Flag, error)

	// UIDFetchFlagsMulti fetches FLAGS (and MODSEQ when the upstream supports
	// CONDSTORE) for each UID in uids, in one round-trip. Returns one entry
	// per UID that still exists in the currently-selected mailbox. This is the
	// non-CONDSTORE down-sync path: a bounded re-fetch of the known UID set on
	// poll (REQ-IMAP-IMP-40/24).
	UIDFetchFlagsMulti(ctx context.Context, uids []imap.UID) ([]uidFlags, error)

	// UIDFetchFlagsChangedSince fetches FLAGS + MODSEQ for every message in the
	// currently-selected mailbox whose MODSEQ is greater than sinceModSeq, via
	// CONDSTORE "UID FETCH 1:* (FLAGS) (CHANGEDSINCE <sinceModSeq>)". The
	// caller must only use this when Caps().Has(imap.CapCondStore). Returns the
	// changed entries (REQ-IMAP-IMP-24/40).
	UIDFetchFlagsChangedSince(ctx context.Context, sinceModSeq uint64) ([]uidFlags, error)

	// UIDStoreFlags applies a flag delta to the message identified by uid
	// in the currently-selected (read-write) mailbox. op must be
	// imap.StoreFlagsAdd or imap.StoreFlagsDel. REQ-IMAP-IMP-40/42.
	UIDStoreFlags(ctx context.Context, uid imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error

	// UIDMove moves the message identified by uid to destMailbox. Uses
	// MOVE when the upstream advertises it; falls back to COPY + UID STORE
	// +FLAGS \Deleted + UID EXPUNGE per RFC 6851. REQ-IMAP-IMP-43.
	UIDMove(ctx context.Context, uid imap.UID, destMailbox string) error

	// UIDExpunge expunges the message identified by uid from the
	// currently-selected mailbox. Requires UID EXPUNGE (UIDPLUS). On
	// servers without UIDPLUS it is equivalent to EXPUNGE (which expunges
	// all \Deleted messages). REQ-IMAP-IMP-44.
	UIDExpunge(ctx context.Context, uid imap.UID) error

	// Noop issues the IMAP NOOP command. Used as the poll heartbeat when
	// the upstream does not advertise IDLE (REQ-IMAP-IMP-23). Also causes
	// the server to flush any pending unsolicited responses.
	Noop(ctx context.Context) error

	// Idle starts an IDLE command on the currently-selected mailbox.
	// Returns an idleHandle whose Wait blocks until an unsolicited
	// EXISTS / EXPUNGE / FETCH update is delivered or the handle is
	// closed. The connection cannot accept other commands until the handle
	// is closed. Callers must check Caps().Has(imap.CapIdle) before
	// calling; the behaviour on servers that don't advertise IDLE is
	// undefined. REQ-IMAP-IMP-20/23.
	Idle(ctx context.Context) (idleHandle, error)
}

// Dialer establishes an authenticated IMAP connection to an upstream
// server. CredentialPlaintext is the decrypted credential: a password
// string for password/app_password accounts, or a short-lived OAuth2
// access token for xoauth2 accounts (the refresh→access exchange
// happens in the accountWorker before it calls Dial).
//
// The Dialer must return a Conn that is in the authenticated state —
// TCP connect, TLS handshake, and IMAP LOGIN / AUTHENTICATE have all
// completed. Any error before that point is returned here.
//
// The production implementation is in dialer.go; tests inject a
// fakeDialer that wraps an in-process imapmemserver.
type Dialer interface {
	Dial(ctx context.Context, account dialParams) (Conn, error)
}

// dialParams bundles the per-connection parameters the Dialer needs.
// Fields are named after the IMAPImportAccount columns they mirror.
type dialParams struct {
	// AccountID is used in log lines and metrics; never in auth material.
	AccountID string
	// Host is the upstream IMAP server hostname.
	Host string
	// Port is the upstream port (993 for implicit, 143 for starttls).
	Port int
	// TLSMode is either IMAPImportTLSModeImplicit or
	// IMAPImportTLSModeSTARTTLS.  "none" is rejected defensively.
	TLSMode string // store.IMAPImportTLSMode values, imported as string to avoid circular import
	// Username is the IMAP authentication username.
	Username string
	// AuthMethod is one of "password", "app_password", "xoauth2".
	AuthMethod string // store.IMAPImportAuthMethod values
	// CredentialPlaintext is the plaintext credential for this attempt.
	// For password/app_password: the raw password string.
	// For xoauth2: the short-lived access token (the accountWorker
	// has already called oauthTokenSource.Token before calling Dial).
	// NEVER logged.
	CredentialPlaintext string
}

// oauthTokenSource exchanges an OAuth2 refresh token for a short-lived
// access token. The accountWorker calls this immediately before each
// connect attempt when auth_method = xoauth2.
//
// The production implementation in dialer.go performs a standard
// refresh_token grant POST to the provider's TokenEndpoint. Tests
// inject a fakeTokenSource backed by a local HTTP handler.
type oauthTokenSource interface {
	// Token exchanges refreshToken for an access token. Returns the
	// access token string or an error. NEVER logs refreshToken or the
	// returned access token.
	Token(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// Categoriser is the seam for optional LLM categorisation of newly-
// arrived INBOX-mapped messages (decision 1 / REQ-IMAP-IMP-31).
// Unused in sub-step 3a; defined here so the accountWorker constructor
// can accept it as a dependency without a later interface change.
//
// 3c will replace this stub with a real implementation.
type Categoriser interface {
	// Categorise attempts to categorise the message identified by
	// messageID and placed in mailboxName. It is called only for
	// messages mapped to INBOX that are not classified spam.
	// Implementations are expected to be fast-path-no-op when
	// categorisation is disabled or the message does not qualify.
	// ctx carries the deadline from the fetch round.
	Categorise(ctx context.Context, principalID, messageID string, mailboxName string) error
}

// noopCategoriser is a Categoriser that does nothing. It is used when
// the caller passes nil for the categoriser seam, keeping the
// accountWorker logic clean of nil checks.
type noopCategoriser struct{}

func (noopCategoriser) Categorise(_ context.Context, _, _, _ string) error { return nil }
