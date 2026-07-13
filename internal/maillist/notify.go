package maillist

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/dsn"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// notifyOwner enqueues a plain-text notice to ml's owner_principal
// reporting that member was auto-suspended (REQ-MLIST-54: "notifies the
// owner_principal"). Best-effort: any Meta/Blobs failure is logged, not
// returned -- a notification failure must never undo, retry, or block
// the suspension it is reporting on. The notice is enqueued through the
// normal outbound queue (store.Metadata.EnqueueMessage) exactly like a
// DSN (internal/queue/dsn.go emitDSN): the queue's own delivery worker
// resolves the owner's address, whether that lands as a local mailbox
// insert or (for an unusual non-local owner) an outbound send.
func (p *BounceProcessor) notifyOwner(ctx context.Context, ml store.MailingList, member store.MailingListMember, c dsn.Classification, score, threshold float64) {
	if p.Blobs == nil {
		p.Logger.WarnContext(ctx, "maillist: no blob store wired; skipping owner suspend notification",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress))
		return
	}
	owner, err := p.Meta.GetPrincipalByID(ctx, ml.OwnerID)
	if err != nil {
		p.Logger.WarnContext(ctx, "maillist: resolve owner_principal failed; skipping suspend notification",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	now := p.Clock.Now()
	body := buildSuspendNotice(ml, owner.CanonicalEmail, memberAddressLabel(member), c, score, threshold, now)
	ref, err := p.Blobs.Put(ctx, bytes.NewReader(body))
	if err != nil {
		p.Logger.WarnContext(ctx, "maillist: persist owner-notification body failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	item := store.QueueItem{
		MailFrom:      "postmaster@" + ml.Domain,
		RcptTo:        owner.CanonicalEmail,
		BodyBlobHash:  ref.Hash,
		State:         store.QueueStateQueued,
		NextAttemptAt: now,
		DSNNotify:     store.DSNNotifyNever,
		CreatedAt:     now,
	}
	if _, err := p.Meta.EnqueueMessage(ctx, item); err != nil {
		p.Logger.WarnContext(ctx, "maillist: enqueue owner-notification failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	p.Logger.InfoContext(ctx, "maillist: owner suspend notification enqueued",
		slog.String("activity", observe.ActivitySystem),
		slog.String("list", ml.PostingAddress),
		slog.String("owner", owner.CanonicalEmail))
}

// memberAddressLabel is the human-readable address for a roster row in
// an owner notification: the external address, or "principal:<id>" when
// the member is an internal principal whose address this package does
// not resolve without a store round trip the notification does not need.
func memberAddressLabel(m store.MailingListMember) string {
	if m.ExternalAddress != nil {
		return *m.ExternalAddress
	}
	if m.PrincipalID != nil {
		return fmt.Sprintf("principal:%d", *m.PrincipalID)
	}
	return "unknown"
}

// buildSuspendNotice renders the RFC 5322 plain-text notice sent to a
// list's owner_principal on auto-suspend. CRLF-terminated and ready to
// enqueue, mirroring internal/queue/dsn.go's buildDSN header style.
func buildSuspendNotice(ml store.MailingList, ownerAddr, memberAddr string, c dsn.Classification, score, threshold float64, now time.Time) []byte {
	var rb [12]byte
	_, _ = rand.Read(rb[:])
	msgID := fmt.Sprintf("<%s.mlist-suspend@%s>", hex.EncodeToString(rb[:]), ml.Domain)

	var buf bytes.Buffer
	buf.WriteString("From: postmaster@" + ml.Domain + "\r\n")
	buf.WriteString("To: " + ownerAddr + "\r\n")
	buf.WriteString("Subject: [" + ml.PostingAddress + "] member auto-suspended: " + memberAddr + "\r\n")
	buf.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	buf.WriteString("Message-ID: " + msgID + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Auto-Submitted: auto-generated\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("\r\n")
	fmt.Fprintf(&buf,
		"Member %s of mailing list %s was automatically suspended after a\r\n"+
			"%s bounce (REQ-MLIST-54): bounce score %.3f reached the configured\r\n"+
			"suspend threshold %.3f.\r\n"+
			"\r\n"+
			"Delivery to this member has stopped. Reactivate the member once the\r\n"+
			"underlying delivery problem is resolved (herold admin CLI: `herold\r\n"+
			"list member-set %d <member-id> --state active`, or the equivalent\r\n"+
			"admin REST PATCH), which also resets its bounce score.\r\n",
		memberAddr, ml.PostingAddress, c.String(), score, threshold, ml.ID)
	return buf.Bytes()
}
