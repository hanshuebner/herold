package imapimport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	imap "github.com/emersion/go-imap/v2"
	imapclient "github.com/emersion/go-imap/v2/imapclient"
	gosql "github.com/emersion/go-sasl"

	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// productionDialer is the Dialer implementation that uses the
// emersion/go-imap/v2 imapclient package. It is the only file that
// imports imapclient; the rest of the package tests against fakeDialer.
type productionDialer struct {
	cfg sysconfig.IMAPImportConfig
}

func newProductionDialer(cfg sysconfig.IMAPImportConfig) *productionDialer {
	return &productionDialer{cfg: cfg}
}

// Dial connects to the upstream IMAP server as described in p, completes
// the TLS handshake and IMAP authentication, then returns an open Conn.
// The CredentialPlaintext field is consumed during authentication and
// never stored (REQ-IMAP-IMP-70).
func (d *productionDialer) Dial(ctx context.Context, p dialParams) (Conn, error) {
	// Defensive: tls_mode=none must never reach here (REQ-IMAP-IMP-06).
	if p.TLSMode != string(store.IMAPImportTLSModeImplicit) &&
		p.TLSMode != string(store.IMAPImportTLSModeSTARTTLS) {
		return nil, fmt.Errorf("imapimport: tls_mode=%q is not permitted; connection must use TLS (REQ-IMAP-IMP-06)", p.TLSMode)
	}

	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	// Wire a UnilateralDataHandler before dialing so unsolicited EXISTS /
	// EXPUNGE / FETCH updates during IDLE are forwarded to the notify
	// channel.  The channel is consumed by the idle loop in idle.go.
	notify := make(chan struct{}, 1)
	signalNotify := func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: p.Host},
		// DebugWriter intentionally left nil — wire-level tracing is
		// only enabled via a separate debug flag and must redact auth
		// payloads (REQ-IMAP-IMP-71).
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(_ *imapclient.UnilateralDataMailbox) { signalNotify() },
			Expunge: func(_ uint32) { signalNotify() },
			Fetch:   func(_ *imapclient.FetchMessageData) { signalNotify() },
		},
	}

	var client *imapclient.Client
	var err error

	switch p.TLSMode {
	case string(store.IMAPImportTLSModeImplicit):
		client, err = imapclient.DialTLS(addr, opts)
	case string(store.IMAPImportTLSModeSTARTTLS):
		client, err = imapclient.DialStartTLS(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("imapimport: dial %s: %w", addr, err)
	}

	// Authenticate — credentials used here and then discarded.
	if authErr := d.authenticate(client, p); authErr != nil {
		client.Close()
		return nil, authErr
	}

	return &prodConn{client: client, notify: notify}, nil
}

// authenticate performs the IMAP authentication exchange using the
// appropriate mechanism. Credentials are consumed here and never stored
// in the returned Conn. (REQ-IMAP-IMP-70, REQ-IMAP-IMP-71.)
func (d *productionDialer) authenticate(client *imapclient.Client, p dialParams) error {
	switch p.AuthMethod {
	case string(store.IMAPImportAuthMethodPassword),
		string(store.IMAPImportAuthMethodAppPassword):
		// Both use the same IMAP mechanism (LOGIN or AUTHENTICATE PLAIN).
		// Prefer AUTHENTICATE PLAIN when the server advertises AUTH=PLAIN,
		// otherwise fall back to LOGIN.
		caps := client.Caps()
		if caps.Has(imap.AuthCap("PLAIN")) {
			saslClient := gosql.NewPlainClient("", p.Username, p.CredentialPlaintext)
			if err := client.Authenticate(saslClient); err != nil {
				return fmt.Errorf("imapimport: AUTHENTICATE PLAIN: %w", err)
			}
			return nil
		}
		if err := client.Login(p.Username, p.CredentialPlaintext).Wait(); err != nil {
			return fmt.Errorf("imapimport: LOGIN: %w", err)
		}
		return nil

	case string(store.IMAPImportAuthMethodXOAuth2):
		// CredentialPlaintext is a short-lived access token at this point
		// (the accountWorker already called oauthTokenSource.Token before
		// calling Dial). The XOAUTH2 SASL payload is:
		//   base64("user=" + username + "\x01auth=Bearer " + token + "\x01\x01")
		saslClient := newXOAuth2Client(p.Username, p.CredentialPlaintext)
		if err := client.Authenticate(saslClient); err != nil {
			return fmt.Errorf("imapimport: AUTHENTICATE XOAUTH2: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("imapimport: unknown auth_method=%q", p.AuthMethod)
	}
}

// prodConn wraps *imapclient.Client and satisfies the Conn interface.
type prodConn struct {
	client *imapclient.Client
	// notify is a buffered channel that receives a signal whenever the
	// server delivers an unsolicited mailbox update during IDLE (EXISTS,
	// EXPUNGE, or FETCH). The write happens in the UnilateralDataHandler
	// wired at dial time. The IDLE loop in idle.go reads from it.
	notify chan struct{}
}

func (c *prodConn) Caps() imap.CapSet {
	return c.client.Caps()
}

func (c *prodConn) Logout() error {
	cmd := c.client.Logout()
	return cmd.Wait()
}

func (c *prodConn) Close() error {
	return c.client.Close()
}

// List issues LIST "" "*" and collects all mailboxes into folderInfo
// slices. The ctx is used for cancellation only; the imapclient
// command itself does not accept a context directly.
func (c *prodConn) List(_ context.Context) ([]folderInfo, error) {
	cmd := c.client.List("", "*", nil)
	listData, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("imapimport: LIST: %w", err)
	}
	out := make([]folderInfo, 0, len(listData))
	for _, d := range listData {
		out = append(out, folderInfo{Name: d.Mailbox, Attrs: d.Attrs})
	}
	return out, nil
}

// Select opens the mailbox read-only (EXAMINE) and returns the key
// SELECT response fields. REQ-IMAP-IMP-24/35.
func (c *prodConn) Select(_ context.Context, mailbox string) (selectInfo, error) {
	data, err := c.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return selectInfo{}, fmt.Errorf("imapimport: EXAMINE %q: %w", mailbox, err)
	}
	return selectInfo{
		UIDValidity:   data.UIDValidity,
		UIDNext:       data.UIDNext,
		HighestModSeq: data.HighestModSeq,
		NumMessages:   data.NumMessages,
	}, nil
}

// UIDSearchSince issues UID SEARCH (SINCE <date> when since is non-zero,
// ALL otherwise) and returns the matching UIDs.
func (c *prodConn) UIDSearchSince(_ context.Context, since time.Time) ([]imap.UID, error) {
	criteria := &imap.SearchCriteria{}
	if !since.IsZero() {
		// SINCE is INTERNALDATE-based (date only, per RFC 3501 §6.4.4).
		// Truncate to midnight UTC to match server-side date comparison.
		criteria.Since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	}
	data, err := c.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imapimport: UID SEARCH SINCE: %w", err)
	}
	return data.AllUIDs(), nil
}

// UIDFetch fetches FLAGS, INTERNALDATE, and BODY.PEEK[] for the given
// UIDs. Uses BODY.PEEK so the upstream \Seen flag is NOT set.
func (c *prodConn) UIDFetch(_ context.Context, uids []imap.UID) ([]fetchedMessage, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	var uidSet imap.UIDSet
	for _, uid := range uids {
		uidSet.AddNum(uid)
	}
	fetchOpts := &imap.FetchOptions{
		Flags:        true,
		InternalDate: true,
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true}, // BODY.PEEK[] — full RFC822, no \Seen side-effect
		},
	}
	cmd := c.client.Fetch(uidSet, fetchOpts)
	bufs, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("imapimport: UID FETCH: %w", err)
	}
	out := make([]fetchedMessage, 0, len(bufs))
	for _, buf := range bufs {
		fm := fetchedMessage{
			UID:          buf.UID,
			Flags:        buf.Flags,
			InternalDate: buf.InternalDate,
		}
		// Extract the body bytes from the first BODY.PEEK[] section.
		for _, bs := range buf.BodySection {
			fm.RFC822 = bs.Bytes
			break
		}
		out = append(out, fm)
	}
	return out, nil
}

// SelectReadWrite opens the mailbox for read-write access (SELECT, not EXAMINE).
// Used by the write-back path. REQ-IMAP-IMP-40..44.
func (c *prodConn) SelectReadWrite(_ context.Context, mailbox string) (selectInfo, error) {
	data, err := c.client.Select(mailbox, &imap.SelectOptions{ReadOnly: false}).Wait()
	if err != nil {
		return selectInfo{}, fmt.Errorf("imapimport: SELECT (rw) %q: %w", mailbox, err)
	}
	return selectInfo{
		UIDValidity:   data.UIDValidity,
		UIDNext:       data.UIDNext,
		HighestModSeq: data.HighestModSeq,
		NumMessages:   data.NumMessages,
	}, nil
}

// UIDFetchFlags fetches only the flags for the given UID. Returns nil when
// the message does not exist. REQ-IMAP-IMP-42.
//
// Note: we must request UID:true in addition to Flags:true.  The
// imapclient routes FETCH responses to the matching pending command by
// checking the UID field in the response.  When only FLAGS are requested,
// the server omits the UID from the response, uid==0, and the client
// cannot match the response to the command — it falls through to the
// UnilateralDataHandler.Fetch callback instead.  Requesting UID:true
// ensures the server includes "UID N" in the response so the client
// can route it correctly.
// UIDFetchFlags fetches only the flags for the given UID. Returns nil when
// the message does not exist. REQ-IMAP-IMP-42.
//
// Note: we must request UID:true in addition to Flags:true.  The
// imapclient routes FETCH responses to the matching pending command by
// checking the UID field in the response.  When only FLAGS are requested,
// the server omits the UID from the response, uid==0, and the client
// cannot match the response to the command — it falls through to the
// UnilateralDataHandler.Fetch callback instead.  Requesting UID:true
// ensures the server includes "UID N" in the response so the client
// can route it correctly.
//
// Also: when a message has no flags set, the server returns FLAGS () and
// FetchMessageBuffer.Flags is nil (not []imap.Flag{}).  We normalise this
// to an empty non-nil slice so callers can distinguish "message found,
// no flags" from "message not found (nil return)".
func (c *prodConn) UIDFetchFlags(_ context.Context, uid imap.UID) ([]imap.Flag, error) {
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	cmd := c.client.Fetch(uidSet, &imap.FetchOptions{Flags: true, UID: true})
	bufs, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("imapimport: UID FETCH FLAGS: %w", err)
	}
	if len(bufs) == 0 {
		return nil, nil
	}
	if bufs[0].Flags == nil {
		return []imap.Flag{}, nil
	}
	return bufs[0].Flags, nil
}

// UIDStoreFlags applies a flag delta to the message identified by uid.
// op must be StoreFlagsAdd or StoreFlagsDel. REQ-IMAP-IMP-40/42.
func (c *prodConn) UIDStoreFlags(_ context.Context, uid imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error {
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	sf := &imap.StoreFlags{
		Op:     op,
		Silent: true,
		Flags:  flags,
	}
	cmd := c.client.Store(uidSet, sf, nil)
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("imapimport: UID STORE flags: %w", err)
	}
	return nil
}

// UIDMove moves the message to destMailbox. The imapclient.Move method
// already implements the COPY+STORE+EXPUNGE fallback for servers without
// the MOVE extension. REQ-IMAP-IMP-43.
func (c *prodConn) UIDMove(_ context.Context, uid imap.UID, destMailbox string) error {
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	_, err := c.client.Move(uidSet, destMailbox).Wait()
	if err != nil {
		return fmt.Errorf("imapimport: UID MOVE -> %q: %w", destMailbox, err)
	}
	return nil
}

// UIDExpunge expunges the given UID from the currently-selected mailbox.
// When the server advertises UIDPLUS, uses UID EXPUNGE; otherwise falls back
// to plain EXPUNGE. REQ-IMAP-IMP-44.
func (c *prodConn) UIDExpunge(_ context.Context, uid imap.UID) error {
	var uidSet imap.UIDSet
	uidSet.AddNum(uid)
	// imapclient.UIDExpunge emits UID EXPUNGE when the server supports UIDPLUS;
	// plain Expunge is the fallback.
	if c.client.Caps().Has(imap.CapUIDPlus) {
		if err := c.client.UIDExpunge(uidSet).Close(); err != nil {
			return fmt.Errorf("imapimport: UID EXPUNGE: %w", err)
		}
		return nil
	}
	if err := c.client.Expunge().Close(); err != nil {
		return fmt.Errorf("imapimport: EXPUNGE: %w", err)
	}
	return nil
}

// Noop issues an IMAP NOOP and waits for the server response.
// REQ-IMAP-IMP-23.
func (c *prodConn) Noop(_ context.Context) error {
	if err := c.client.Noop().Wait(); err != nil {
		return fmt.Errorf("imapimport: NOOP: %w", err)
	}
	return nil
}

// prodIdleHandle wraps imapclient.IdleCommand and satisfies idleHandle.
// The Wait method blocks until:
//   - an unsolicited update arrives via the notify channel (returns nil), or
//   - Close is called (returns nil), or
//   - the connection drops / IDLE ends unexpectedly (returns an error).
type prodIdleHandle struct {
	cmd    *imapclient.IdleCommand
	notify chan struct{}
	// done is closed when the underlying IdleCommand completes.
	done chan error
}

// Idle starts an IDLE command. The returned idleHandle.Wait blocks until an
// unsolicited EXISTS / EXPUNGE / FETCH update arrives or Close is called.
// REQ-IMAP-IMP-20.
func (c *prodConn) Idle(_ context.Context) (idleHandle, error) {
	cmd, err := c.client.Idle()
	if err != nil {
		return nil, fmt.Errorf("imapimport: IDLE: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	return &prodIdleHandle{cmd: cmd, notify: c.notify, done: done}, nil
}

// Wait blocks until an unsolicited update arrives or IDLE ends unexpectedly.
// Returns nil on a clean wake (update received or Close already called).
func (h *prodIdleHandle) Wait() error {
	select {
	case <-h.notify:
		return nil
	case err := <-h.done:
		// Re-queue so a subsequent Wait (if any) also sees the terminal state.
		h.done <- err
		return err
	}
}

// Close stops the IDLE command (sends DONE to the server).
func (h *prodIdleHandle) Close() error {
	err := h.cmd.Close()
	// Drain done so the goroutine in Idle() doesn't leak.
	select {
	case <-h.done:
	default:
	}
	return err
}
