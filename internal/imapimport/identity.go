package imapimport

// identity.go records and checks the upstream identity of a state row (re
// #561). A row (account, folder, uid) names an upstream message by UID only;
// the Message-ID and RFC822 size captured when the row is created are what
// let write-back prove that the message at that UID is still the one the row
// was created from before it issues an irreversible EXPUNGE.

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"net/textproto"
	"sort"
	"strconv"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// identityBackfillBatch bounds how many legacy rows one folder pass tries to
// identify.
const identityBackfillBatch = 100

// upstreamIdentityOf returns the normalised Message-ID (empty when the
// message has none) and the size of the upstream message whose bytes are
// rfc822, exactly as an IMAP server reports them in ENVELOPE and
// RFC822.SIZE.
func upstreamIdentityOf(rfc822 []byte) (messageID string, size int64) {
	hdr, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(rfc822))).ReadMIMEHeader()
	if hdr == nil && err != nil {
		return "", int64(len(rfc822))
	}
	return mailparse.NormalizeMessageID(hdr.Get("Message-Id")), int64(len(rfc822))
}

// rowHasUpstreamIdentity reports whether ms carries a usable recorded
// identity: a Message-ID and a size.
func rowHasUpstreamIdentity(ms store.IMAPImportMessageState) bool {
	return ms.UpstreamMessageID != "" && ms.UpstreamSize > 0
}

// rowMatchesUpstream reports whether the upstream message described by env is
// positively the message ms was created from: both sides carry a Message-ID
// and a size, and both agree.
func rowMatchesUpstream(ms store.IMAPImportMessageState, env envelopeFetchResult) bool {
	return rowHasUpstreamIdentity(ms) &&
		env.MessageID != "" && env.Size > 0 &&
		env.MessageID == ms.UpstreamMessageID && env.Size == ms.UpstreamSize
}

// backfillUpstreamIdentity records the identity of rows that predate it. For
// the folder selected on conn it fetches the envelope of each legacy row's
// UID and records the upstream Message-ID and size only when the Message-ID
// equals the one the row's herold message carries. A row whose UID names a
// different message, or no message, is left without an identity and so is
// never expunged through. Each row is tried once per worker lifetime; a
// bounded batch runs per folder pass. Best-effort.
func (w *accountWorker) backfillUpstreamIdentity(ctx context.Context, conn Conn, upstreamFolder string) {
	account := w.opts.account
	log := w.opts.log
	meta := w.opts.store.Meta()

	cur, found, err := meta.GetIMAPImportFolderCursor(ctx, account.ID, upstreamFolder)
	if err != nil || !found || cur.UIDNext == 0 {
		return
	}
	states, err := meta.ListIMAPImportMessageStatesByFolder(ctx, account.ID, upstreamFolder)
	if err != nil {
		log.Warn("imapimport: identity backfill: list failed",
			slog.String("account_id", account.ID),
			slog.String("upstream_folder", upstreamFolder),
			slog.String("error", err.Error()),
		)
		return
	}
	var todo []store.IMAPImportMessageState
	for _, ms := range states {
		if rowHasUpstreamIdentity(ms) || rowDivergent(ms) || uint64(ms.UpstreamUID) >= cur.UIDNext {
			continue
		}
		if _, tried := w.identityBackfillTried[identityKey(ms)]; tried {
			continue
		}
		todo = append(todo, ms)
	}
	if len(todo) == 0 {
		return
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].UpstreamUID < todo[j].UpstreamUID })
	if len(todo) > identityBackfillBatch {
		todo = todo[:identityBackfillBatch]
	}
	if w.identityBackfillTried == nil {
		w.identityBackfillTried = make(map[string]struct{})
	}
	uids := make([]imap.UID, 0, len(todo))
	for _, ms := range todo {
		w.identityBackfillTried[identityKey(ms)] = struct{}{}
		uids = append(uids, imap.UID(ms.UpstreamUID))
	}
	envs, err := conn.UIDFetchEnvelope(ctx, uids)
	if err != nil {
		log.Warn("imapimport: identity backfill: envelope fetch failed",
			slog.String("account_id", account.ID),
			slog.String("upstream_folder", upstreamFolder),
			slog.String("error", err.Error()),
		)
		return
	}
	byUID := make(map[imap.UID]envelopeFetchResult, len(envs))
	for _, e := range envs {
		byUID[e.UID] = e
	}
	recorded := 0
	for _, ms := range todo {
		if ctx.Err() != nil {
			return
		}
		env, ok := byUID[imap.UID(ms.UpstreamUID)]
		if !ok || env.MessageID == "" || env.Size <= 0 {
			continue
		}
		msg, gerr := meta.GetMessage(ctx, ms.HeroldMessageID)
		if gerr != nil {
			continue
		}
		if mailparse.NormalizeMessageID(msg.Envelope.MessageID) != env.MessageID {
			continue
		}
		ms.UpstreamMessageID = env.MessageID
		ms.UpstreamSize = env.Size
		if uerr := meta.UpsertIMAPImportMessageState(ctx, ms); uerr != nil {
			log.Warn("imapimport: identity backfill: upsert failed",
				slog.String("account_id", account.ID),
				slog.String("error", uerr.Error()),
			)
			continue
		}
		recorded++
	}
	if recorded > 0 {
		log.Info("imapimport: identity backfill: recorded upstream identity",
			slog.String("account_id", account.ID),
			slog.String("upstream_folder", upstreamFolder),
			slog.Int("rows", recorded),
		)
	}
}

func identityKey(ms store.IMAPImportMessageState) string {
	return ms.UpstreamFolder + "\x00" + strconv.FormatUint(uint64(ms.UpstreamUID), 10)
}
