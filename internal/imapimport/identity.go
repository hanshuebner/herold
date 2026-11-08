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
// message has none) and the RFC822.SIZE of the fetched upstream message: the
// size the server reported with the fetch (the body length when it reported
// none), the same source write-back compares against.
func upstreamIdentityOf(fm fetchedMessage) (messageID string, size int64) {
	size = fm.Size
	if size <= 0 {
		size = int64(len(fm.RFC822))
	}
	hdr, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(fm.RFC822))).ReadMIMEHeader()
	if hdr == nil && err != nil {
		return "", size
	}
	return mailparse.NormalizeMessageID(hdr.Get("Message-Id")), size
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
// UID and records the upstream Message-ID and RFC822.SIZE only when both
// equal what the row's herold message carries: its Message-ID and its stored
// size (the herold copy was mirrored from that upstream message, so a resend
// with the same Message-ID but different content does not qualify). A row
// whose UID names a different message, or no message, is left without an
// identity and so is never expunged through. The pass is skipped when the
// folder's UIDVALIDITY differs from the cursor's. Each row is tried once per
// worker lifetime; a bounded batch runs per folder pass. The write is
// column-scoped (SetIMAPImportMessageStateIdentity): it never rewrites or
// re-inserts a row that write-back changed in the meantime. Best-effort.
func (w *accountWorker) backfillUpstreamIdentity(ctx context.Context, conn Conn, upstreamFolder string, uidValidity uint32) {
	account := w.opts.account
	log := w.opts.log
	meta := w.opts.store.Meta()

	cur, found, err := meta.GetIMAPImportFolderCursor(ctx, account.ID, upstreamFolder)
	if err != nil || !found || cur.UIDNext == 0 {
		return
	}
	if cur.UIDValidity != uint64(uidValidity) {
		log.Info("imapimport: identity backfill: UIDVALIDITY differs from the cursor; skipping folder",
			slog.String("account_id", account.ID),
			slog.String("upstream_folder", upstreamFolder),
		)
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
	if w.testBeforeIdentityWrite != nil {
		w.testBeforeIdentityWrite()
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
		if mailparse.NormalizeMessageID(msg.Envelope.MessageID) != env.MessageID || msg.Size != env.Size {
			continue
		}
		applied, uerr := meta.SetIMAPImportMessageStateIdentity(ctx, ms.AccountID, ms.UpstreamFolder, ms.UpstreamUID, ms.HeroldMessageID, env.MessageID, env.Size)
		if uerr != nil {
			log.Warn("imapimport: identity backfill: store write failed",
				slog.String("account_id", account.ID),
				slog.String("error", uerr.Error()),
			)
			continue
		}
		if applied {
			recorded++
		}
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
