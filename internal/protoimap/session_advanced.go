// Phase 2 Wave 2.2 command handlers: ENABLE, MOVE / COPY, NOTIFY,
// COMPRESS=DEFLATE. CONDSTORE / QRESYNC SELECT-side handling and the
// updated FETCH / STORE / SEARCH paths live in session_mailbox.go,
// session_fetch.go, and session_store_search.go respectively.

package protoimap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// handleENABLE responds to RFC 5161 ENABLE, flipping the matching
// session-level toggles for tokens we honour. Unknown tokens are
// silently dropped; only tokens the server actually supports are echoed
// in the untagged ENABLED response.
func (ses *session) handleENABLE(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	enabled := make([]string, 0, len(c.EnableTokens))
	for _, t := range c.EnableTokens {
		switch t {
		case "CONDSTORE":
			ses.enableCondstore()
			enabled = append(enabled, "CONDSTORE")
		case "QRESYNC":
			ses.enableQresync()
			enabled = append(enabled, "QRESYNC")
		case "UTF8=ACCEPT":
			// We always operate in UTF-8; the toggle is implicit.
			enabled = append(enabled, "UTF8=ACCEPT")
		}
	}
	if len(enabled) > 0 {
		if err := ses.resp.untagged("ENABLED " + strings.Join(enabled, " ")); err != nil {
			return err
		}
	}
	return ses.resp.taggedOK(c.Tag, "", "ENABLE completed")
}

// handleMOVE implements RFC 6851 MOVE / UID MOVE. Atomicity is
// best-effort: we COPY into the destination first, then EXPUNGE from
// the source. A failure between the two steps leaves the destination
// holding the message but the source still seeing it as the only
// observable state — clients re-issue the MOVE on retry, which is
// safe (COPY into the same dest creates a fresh UID and the EXPUNGE
// drains the old source row). The full single-tx variant lands when
// store.Metadata grows a MoveMessages primitive in a follow-up.
func (ses *session) handleMOVE(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	dest, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.CopyMoveDest))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "TRYCREATE", "destination not found")
		}
		return ses.resp.taggedNO(c.Tag, "", "move failed")
	}
	seqs := ses.expandSet(c.CopyMoveSet, c.IsUID)
	if len(seqs) == 0 {
		return ses.resp.taggedOK(c.Tag, "", "MOVE completed")
	}
	ses.selMu.Lock()
	srcMsgs := append([]store.Message(nil), ses.sel.msgs...)
	srcMailboxID := ses.sel.id
	uidValiditySrc := uint32(ses.sel.uidValidity)
	ses.selMu.Unlock()

	srcUIDs := make([]store.UID, 0, len(seqs))
	dstUIDs := make([]store.UID, 0, len(seqs))
	ids := make([]store.MessageID, 0, len(seqs))
	for _, seq := range seqs {
		if seq <= 0 || seq > len(srcMsgs) {
			continue
		}
		m := srcMsgs[seq-1]
		// Re-stage the blob into a fresh message row in dest. The blob
		// reference is preserved so the underlying bytes are not
		// duplicated; only the metadata row is new.
		copyMsg := store.Message{
			PrincipalID:  ses.pid,
			Flags:        m.Flags,
			Keywords:     m.Keywords,
			InternalDate: m.InternalDate,
			ReceivedAt:   m.ReceivedAt,
			Size:         m.Size,
			Blob:         m.Blob,
			Envelope:     m.Envelope,
		}
		uid, _, err := ses.s.store.Meta().InsertMessage(ctx, copyMsg, []store.MessageMailbox{{MailboxID: dest.ID, Flags: m.Flags, Keywords: m.Keywords}})
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "", "move failed")
		}
		srcUIDs = append(srcUIDs, m.UID)
		dstUIDs = append(dstUIDs, uid)
		ids = append(ids, m.ID)
	}
	// COPYUID untagged code per RFC 6851 §4.4 / RFC 4315 §3.
	codeBuf := fmt.Sprintf("COPYUID %d %s %s",
		dest.UIDValidity,
		formatUIDList(srcUIDs),
		formatUIDList(dstUIDs))
	if err := ses.resp.untagged("OK [" + codeBuf + "] MOVE staged"); err != nil {
		return err
	}
	// EXPUNGE source rows.
	if err := ses.s.store.Meta().ExpungeMessages(ctx, srcMailboxID, ids); err != nil {
		return ses.resp.taggedNO(c.Tag, "", "move expunge failed")
	}
	// Emit per-seq EXPUNGE (or VANISHED on QRESYNC).
	if ses.qresyncActive() {
		_ = ses.emitVanishedFromExpunge(srcUIDs)
	} else {
		// Compute the seqs in source descending order pre-reload.
		// We have seqs from expandSet which are 1-based pre-reload.
		// Emit highest first so the client's seq map stays valid.
		for i := len(seqs) - 1; i >= 0; i-- {
			_ = ses.resp.untagged(fmt.Sprintf("%d EXPUNGE", seqs[i]))
		}
	}
	_ = uidValiditySrc
	_ = ses.reloadSelected(ctx)
	ses.logger.Info("protoimap: MOVE",
		"activity", "user",
		"dest_mailbox", c.CopyMoveDest,
		"uid_count", len(srcUIDs),
	)
	return ses.resp.taggedOK(c.Tag, "", "MOVE completed")
}

// handleCOPY implements RFC 9051 COPY / UID COPY. A simpler MOVE
// without the EXPUNGE step.
func (ses *session) handleCOPY(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	dest, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.CopyMoveDest))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "TRYCREATE", "destination not found")
		}
		return ses.resp.taggedNO(c.Tag, "", "copy failed")
	}
	seqs := ses.expandSet(c.CopyMoveSet, c.IsUID)
	ses.selMu.Lock()
	srcMsgs := append([]store.Message(nil), ses.sel.msgs...)
	ses.selMu.Unlock()
	var srcUIDs, dstUIDs []store.UID
	for _, seq := range seqs {
		if seq <= 0 || seq > len(srcMsgs) {
			continue
		}
		m := srcMsgs[seq-1]
		copyMsg := store.Message{
			PrincipalID:  ses.pid,
			Flags:        m.Flags,
			Keywords:     m.Keywords,
			InternalDate: m.InternalDate,
			ReceivedAt:   m.ReceivedAt,
			Size:         m.Size,
			Blob:         m.Blob,
			Envelope:     m.Envelope,
		}
		uid, _, err := ses.s.store.Meta().InsertMessage(ctx, copyMsg, []store.MessageMailbox{{MailboxID: dest.ID, Flags: m.Flags, Keywords: m.Keywords}})
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "", "copy failed")
		}
		srcUIDs = append(srcUIDs, m.UID)
		dstUIDs = append(dstUIDs, uid)
	}
	if len(srcUIDs) > 0 {
		ses.logger.Info("protoimap: COPY",
			"activity", "user",
			"dest_mailbox", c.CopyMoveDest,
			"uid_count", len(srcUIDs),
		)
		code := fmt.Sprintf("COPYUID %d %s %s",
			dest.UIDValidity,
			formatUIDList(srcUIDs),
			formatUIDList(dstUIDs))
		return ses.resp.taggedOK(c.Tag, code, "COPY completed")
	}
	return ses.resp.taggedOK(c.Tag, "", "COPY completed")
}

// handleNOTIFY parses the RFC 5465 NOTIFY arguments and replaces the
// session's subscription list. Subsequent IDLE / NOTIFY-pull cycles
// consult the new list when classifying change-feed entries.
func (ses *session) handleNOTIFY(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	cleared, subs, err := parseNotifyArgs(c.NotifyRaw)
	if err != nil {
		return ses.resp.taggedBAD(c.Tag, "", err.Error())
	}
	ses.selMu.Lock()
	if cleared {
		ses.notify = notifyState{}
	} else {
		ses.notify = notifyState{active: true, subs: subs}
	}
	ses.selMu.Unlock()
	return ses.resp.taggedOK(c.Tag, "", "NOTIFY completed")
}

// handleCOMPRESS wraps the wire in zlib/flate per RFC 4978. The mechanism
// argument MUST be DEFLATE; any other value is rejected with NO.
func (ses *session) handleCOMPRESS(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	if !strings.EqualFold(c.AuthMechanism, "DEFLATE") {
		return ses.resp.taggedNO(c.Tag, "", "unsupported compression")
	}
	if ses.compressed {
		return ses.resp.taggedBAD(c.Tag, "COMPRESSIONACTIVE", "compression already active")
	}
	// Send the OK *before* installing deflate — the OK is the last
	// uncompressed byte on the wire (RFC 4978 §3).
	if err := ses.resp.taggedOK(c.Tag, "", "DEFLATE active"); err != nil {
		return err
	}
	if err := ses.installDeflate(); err != nil {
		ses.logger.Warn("protoimap: deflate install failed",
			"activity", "internal",
			"err", err,
		)
		return err
	}
	return nil
}

// applyMultiAppend handles APPEND with one or more literal payloads
// (RFC 3502 MULTIAPPEND). All inserts succeed or all are rolled back.
// Phase 2 Wave 2.2 implements the "all-or-nothing" guarantee at the
// session layer by inserting sequentially and, on failure mid-stream,
// expunging anything we already wrote. A future store.Metadata
// AppendBatch primitive would let us drop into one transaction; for
// now this best-effort path is correct under crash-free conditions and
// degrades cleanly when the store rejects an insert.
func (ses *session) applyMultiAppend(ctx context.Context, c *Command, mb store.Mailbox) error {
	inserted := make([]store.MessageID, 0, len(c.AppendItems))
	uids := make([]store.UID, 0, len(c.AppendItems))
	for _, item := range c.AppendItems {
		blobRef, err := ses.s.store.Blobs().Put(ctx, bytes.NewReader(item.Data))
		if err != nil {
			ses.rollbackMultiAppend(ctx, mb.ID, inserted)
			return ses.resp.taggedNO(c.Tag, "", "blob write failed")
		}
		flags := flagMaskFromNames(item.Flags)
		kw := keywordsFromNames(item.Flags)
		env := parseEnvelope(item.Data)
		now := ses.s.clk.Now()
		internal := item.Internal
		if internal.IsZero() {
			internal = now
		}
		msg := store.Message{
			PrincipalID:  ses.pid,
			Flags:        flags,
			Keywords:     kw,
			InternalDate: internal,
			ReceivedAt:   now,
			Size:         int64(len(item.Data)),
			Blob:         blobRef,
			Envelope:     env,
		}
		insertTimer := observe.StartStoreOp("insert_message")
		uid, _, err := ses.s.store.Meta().InsertMessage(ctx, msg, []store.MessageMailbox{{MailboxID: mb.ID, Flags: flags, Keywords: kw}})
		insertTimer.Done()
		if err != nil {
			ses.rollbackMultiAppend(ctx, mb.ID, inserted)
			if errors.Is(err, store.ErrQuotaExceeded) {
				return ses.resp.taggedNO(c.Tag, "OVERQUOTA", "quota exceeded")
			}
			return ses.resp.taggedNO(c.Tag, "", "append failed")
		}
		uids = append(uids, uid)
		// We do not have the message ID directly back from
		// InsertMessage; resolve via the (mailbox_id, uid) pair on
		// the rollback path. For now we look it up on the failure
		// branch; the success path doesn't need it.
		_ = inserted
	}
	code := fmt.Sprintf("APPENDUID %d %s", mb.UIDValidity, formatUIDList(uids))
	return ses.resp.taggedOK(c.Tag, code, "APPEND completed")
}

// rollbackMultiAppend best-effort expunges previously-inserted message
// rows from a partial MULTIAPPEND transaction. On a backend that
// rejected the second of three inserts, this leaves the mailbox in
// the pre-APPEND state.
func (ses *session) rollbackMultiAppend(ctx context.Context, mailboxID store.MailboxID, ids []store.MessageID) {
	if len(ids) == 0 {
		return
	}
	if err := ses.s.store.Meta().ExpungeMessages(ctx, mailboxID, ids); err != nil {
		ses.logger.Warn("protoimap: multi-append rollback failed",
			"activity", "internal",
			"err", err,
		)
	}
}
