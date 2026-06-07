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

	// UIDSearchSince returns UIDs of messages whose INTERNALDATE is on
	// or after `since`. If since is zero the entire mailbox is returned
	// (no SINCE criterion). REQ-IMAP-IMP-17/19.
	UIDSearchSince(ctx context.Context, since time.Time) ([]imap.UID, error)

	// UIDFetch fetches FLAGS, INTERNALDATE, and BODY.PEEK[] for each UID
	// in uids. Uses BODY.PEEK so the upstream \Seen flag is NOT set.
	// REQ-IMAP-IMP-31.
	UIDFetch(ctx context.Context, uids []imap.UID) ([]fetchedMessage, error)
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
