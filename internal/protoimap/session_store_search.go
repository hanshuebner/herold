package protoimap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

func (ses *session) handleSTORE(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	seqs := ses.expandSet(c.StoreSet, c.IsUID)
	addFlags := flagMaskFromImapFlags(c.StoreFlags.Flags)
	addKW := keywordsFromImapFlags(c.StoreFlags.Flags)
	var clearFlags store.MessageFlags
	var clearKW []string
	switch c.StoreFlags.Op {
	case imap.StoreFlagsSet:
		// SET replaces: first compute clear mask as "all known minus add".
		clearFlags = allKnownFlags() &^ addFlags
		// Keywords: fetch existing and subtract new.
		// This is handled per-message below.
	case imap.StoreFlagsAdd:
		// addFlags, addKW additive.
	case imap.StoreFlagsDel:
		clearFlags = addFlags
		clearKW = addKW
		addFlags = 0
		addKW = nil
	}
	for _, seq := range seqs {
		ses.selMu.Lock()
		if seq <= 0 || seq > len(ses.sel.msgs) {
			ses.selMu.Unlock()
			continue
		}
		m := ses.sel.msgs[seq-1]
		ses.selMu.Unlock()
		myClearKW := clearKW
		if c.StoreFlags.Op == imap.StoreFlagsSet {
			// Compute keywords to clear: existing minus new.
			current := map[string]bool{}
			for _, k := range m.Keywords {
				current[k] = true
			}
			for _, k := range addKW {
				delete(current, strings.ToLower(k))
			}
			myClearKW = nil
			for k := range current {
				myClearKW = append(myClearKW, k)
			}
		}
		newSeq, err := ses.s.store.Meta().UpdateMessageFlags(ctx, m.ID, addFlags, clearFlags, addKW, myClearKW, store.ModSeq(c.StoreOptions.UnchangedSince))
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				return ses.resp.taggedOK(c.Tag, "MODIFIED", "STORE condstore conflict")
			}
			return ses.resp.taggedNO(c.Tag, "", "store failed")
		}
		_ = newSeq
		// Re-read to get updated flags.
		updated, err := ses.s.store.Meta().GetMessage(ctx, m.ID)
		if err == nil && !c.StoreFlags.Silent {
			ses.selMu.Lock()
			ses.sel.msgs[seq-1] = updated
			ses.selMu.Unlock()
			parts := []string{"FLAGS " + flagListString(flagNamesFromMask(updated.Flags, updated.Keywords))}
			if c.IsUID {
				parts = append([]string{fmt.Sprintf("UID %d", updated.UID)}, parts...)
			}
			_ = ses.resp.untagged(fmt.Sprintf("%d FETCH (%s)", seq, strings.Join(parts, " ")))
		} else if err == nil {
			ses.selMu.Lock()
			ses.sel.msgs[seq-1] = updated
			ses.selMu.Unlock()
		}
	}
	return ses.resp.taggedOK(c.Tag, "", c.Op+" completed")
}

func allKnownFlags() store.MessageFlags {
	return store.MessageFlagSeen | store.MessageFlagAnswered | store.MessageFlagFlagged |
		store.MessageFlagDeleted | store.MessageFlagDraft
}

func flagMaskFromImapFlags(fs []imap.Flag) store.MessageFlags {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = string(f)
	}
	return flagMaskFromNames(names)
}

func keywordsFromImapFlags(fs []imap.Flag) []string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = string(f)
	}
	return keywordsFromNames(names)
}

// ----- SEARCH -----

func (ses *session) handleSEARCH(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	ses.selMu.Lock()
	msgs := append([]store.Message(nil), ses.sel.msgs...)
	ses.selMu.Unlock()

	// If there is a text predicate, consult FTS once to narrow candidates.
	// Fall back to in-memory filtering when FTS is empty (REQ-STORE-66:
	// new mail may not be indexed yet) or errors.
	ftsHits := map[store.MessageID]bool{}
	ftsUsed := false
	if hasTextPredicate(c.SearchCriteria) {
		q := buildFTSQuery(c.SearchCriteria)
		q.MailboxID = ses.sel.id
		results, err := ses.s.store.FTS().Query(ctx, ses.pid, q)
		if err == nil && len(results) > 0 {
			ftsUsed = true
			for _, r := range results {
				ftsHits[r.MessageID] = true
			}
		}
	}

	matching := []int{} // sequence numbers (1-based)
	for i, m := range msgs {
		if ftsUsed && !ftsHits[m.ID] {
			continue
		}
		if !evalSearch(c.SearchCriteria, &m, i+1) {
			continue
		}
		matching = append(matching, i+1)
	}

	opts := c.SearchOptions
	if opts == nil {
		opts = &imap.SearchOptions{}
	}
	wantESEARCH := opts.ReturnMin || opts.ReturnMax || opts.ReturnAll || opts.ReturnCount || opts.ReturnSave
	if wantESEARCH {
		var sb strings.Builder
		sb.WriteString(`ESEARCH (TAG "`)
		sb.WriteString(c.Tag)
		sb.WriteString(`")`)
		if c.IsUID {
			sb.WriteString(" UID")
		}
		if opts.ReturnMin && len(matching) > 0 {
			sb.WriteString(fmt.Sprintf(" MIN %d", seqToOut(matching[0], msgs, c.IsUID)))
		}
		if opts.ReturnMax && len(matching) > 0 {
			sb.WriteString(fmt.Sprintf(" MAX %d", seqToOut(matching[len(matching)-1], msgs, c.IsUID)))
		}
		if opts.ReturnAll && len(matching) > 0 {
			sb.WriteString(" ALL " + formatSeqList(matching, msgs, c.IsUID))
		}
		if opts.ReturnCount {
			sb.WriteString(fmt.Sprintf(" COUNT %d", len(matching)))
		}
		_ = ses.resp.untagged(sb.String())
	} else {
		var sb strings.Builder
		sb.WriteString("SEARCH")
		for _, seq := range matching {
			sb.WriteByte(' ')
			sb.WriteString(fmt.Sprintf("%d", seqToOut(seq, msgs, c.IsUID)))
		}
		_ = ses.resp.untagged(sb.String())
	}
	return ses.resp.taggedOK(c.Tag, "", c.Op+" completed")
}

func seqToOut(seq int, msgs []store.Message, uid bool) uint32 {
	if !uid {
		return uint32(seq)
	}
	if seq-1 < 0 || seq-1 >= len(msgs) {
		return 0
	}
	return uint32(msgs[seq-1].UID)
}

func formatSeqList(seqs []int, msgs []store.Message, uid bool) string {
	// Simple canonical output: comma-list of numbers; range collapsing
	// deferred to Phase 2 (clients accept the expanded form).
	parts := make([]string, len(seqs))
	for i, s := range seqs {
		parts[i] = fmt.Sprintf("%d", seqToOut(s, msgs, uid))
	}
	return strings.Join(parts, ",")
}

// hasTextPredicate returns true if any BODY/TEXT/SUBJECT/FROM/TO search
// field is set (i.e., the criteria benefit from FTS narrowing).
func hasTextPredicate(c *imap.SearchCriteria) bool {
	if c == nil {
		return false
	}
	if len(c.Body) > 0 || len(c.Text) > 0 {
		return true
	}
	for _, h := range c.Header {
		k := strings.ToLower(h.Key)
		if k == "subject" || k == "from" || k == "to" || k == "cc" || k == "bcc" {
			return true
		}
	}
	for _, sub := range c.Not {
		if hasTextPredicate(&sub) {
			return true
		}
	}
	for _, pair := range c.Or {
		if hasTextPredicate(&pair[0]) || hasTextPredicate(&pair[1]) {
			return true
		}
	}
	return false
}

func buildFTSQuery(c *imap.SearchCriteria) store.Query {
	q := store.Query{}
	collectText(c, &q)
	return q
}

func collectText(c *imap.SearchCriteria, q *store.Query) {
	if c == nil {
		return
	}
	q.Body = append(q.Body, c.Body...)
	q.Body = append(q.Body, c.Text...)
	for _, h := range c.Header {
		switch strings.ToLower(h.Key) {
		case "subject":
			q.Subject = append(q.Subject, h.Value)
		case "from":
			q.From = append(q.From, h.Value)
		case "to", "cc", "bcc":
			q.To = append(q.To, h.Value)
		}
	}
	for _, sub := range c.Not {
		collectText(&sub, q)
	}
	for _, pair := range c.Or {
		collectText(&pair[0], q)
		collectText(&pair[1], q)
	}
}

// evalSearch evaluates the full criteria against a single message (+seq).
// Every criterion field is AND-combined, matching the semantics of
// imap.SearchCriteria.And.
func evalSearch(c *imap.SearchCriteria, m *store.Message, seq int) bool {
	if c == nil {
		return true
	}
	if len(c.SeqNum) > 0 {
		ok := false
		for _, set := range c.SeqNum {
			if set.Contains(uint32(seq)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(c.UID) > 0 {
		ok := false
		for _, set := range c.UID {
			if set.Contains(imap.UID(m.UID)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if !c.Since.IsZero() && m.InternalDate.Before(c.Since) {
		return false
	}
	if !c.Before.IsZero() && !m.InternalDate.Before(c.Before) {
		return false
	}
	if !c.SentSince.IsZero() && m.Envelope.Date.Before(c.SentSince) {
		return false
	}
	if !c.SentBefore.IsZero() && !m.Envelope.Date.Before(c.SentBefore) {
		return false
	}
	for _, f := range c.Flag {
		if !hasFlag(m, f) {
			return false
		}
	}
	for _, f := range c.NotFlag {
		if hasFlag(m, f) {
			return false
		}
	}
	if c.Larger > 0 && m.Size <= c.Larger {
		return false
	}
	if c.Smaller > 0 && m.Size >= c.Smaller {
		return false
	}
	for _, h := range c.Header {
		val := envelopeField(m.Envelope, h.Key)
		if h.Value == "" {
			if val == "" {
				return false
			}
			continue
		}
		if !strings.Contains(strings.ToLower(val), strings.ToLower(h.Value)) {
			return false
		}
	}
	// BODY/TEXT predicates: Phase 1 checks envelope fields + header fields
	// linearly; full-body scan is handled by FTS when a caller opts in, but
	// fallback match here is kept permissive (substring across subject).
	for _, s := range c.Body {
		if !searchSubstring(m, s) {
			return false
		}
	}
	for _, s := range c.Text {
		if !searchSubstring(m, s) {
			return false
		}
	}
	for _, sub := range c.Not {
		if evalSearch(&sub, m, seq) {
			return false
		}
	}
	for _, pair := range c.Or {
		a := evalSearch(&pair[0], m, seq)
		b := evalSearch(&pair[1], m, seq)
		if !a && !b {
			return false
		}
	}
	return true
}

func hasFlag(m *store.Message, f imap.Flag) bool {
	switch f {
	case imap.FlagSeen:
		return m.Flags&store.MessageFlagSeen != 0
	case imap.FlagAnswered:
		return m.Flags&store.MessageFlagAnswered != 0
	case imap.FlagFlagged:
		return m.Flags&store.MessageFlagFlagged != 0
	case imap.FlagDeleted:
		return m.Flags&store.MessageFlagDeleted != 0
	case imap.FlagDraft:
		return m.Flags&store.MessageFlagDraft != 0
	}
	kw := strings.ToLower(string(f))
	for _, k := range m.Keywords {
		if strings.ToLower(k) == kw {
			return true
		}
	}
	return false
}

func envelopeField(e store.Envelope, key string) string {
	switch strings.ToLower(key) {
	case "subject":
		return e.Subject
	case "from":
		return e.From
	case "to":
		return e.To
	case "cc":
		return e.Cc
	case "bcc":
		return e.Bcc
	case "reply-to":
		return e.ReplyTo
	case "message-id":
		return e.MessageID
	case "in-reply-to":
		return e.InReplyTo
	}
	return ""
}

// searchSubstring is the conservative fallback for BODY/TEXT when the FTS
// backend is not consulted: check envelope subject + size-bounded header
// substrings. Documented under SEARCH predicate subset.
func searchSubstring(m *store.Message, s string) bool {
	l := strings.ToLower(s)
	if strings.Contains(strings.ToLower(m.Envelope.Subject), l) {
		return true
	}
	if strings.Contains(strings.ToLower(m.Envelope.From), l) {
		return true
	}
	if strings.Contains(strings.ToLower(m.Envelope.To), l) {
		return true
	}
	return false
}

// ----- EXPUNGE -----

func (ses *session) handleEXPUNGE(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	ses.selMu.Lock()
	msgs := append([]store.Message(nil), ses.sel.msgs...)
	ses.selMu.Unlock()

	uidFilter := map[store.UID]bool{}
	if c.IsUID && c.ExpungeSet != nil {
		set, ok := c.ExpungeSet.(imap.UIDSet)
		if ok {
			for _, r := range set {
				lo, hi := r.Start, r.Stop
				if uint32(hi) == 0xFFFFFFFF {
					hi = imap.UID(ses.sel.uidNext - 1)
				}
				for _, m := range msgs {
					if imap.UID(m.UID) >= lo && imap.UID(m.UID) <= hi {
						uidFilter[m.UID] = true
					}
				}
			}
		}
	}

	// Collect messages to expunge (\Deleted set) and their seq numbers.
	var ids []store.MessageID
	var seqs []int
	for i, m := range msgs {
		if m.Flags&store.MessageFlagDeleted == 0 {
			continue
		}
		if len(uidFilter) > 0 && !uidFilter[m.UID] {
			continue
		}
		ids = append(ids, m.ID)
		seqs = append(seqs, i+1)
	}
	if len(ids) > 0 {
		expungeTimer := observe.StartStoreOp("expunge")
		err := ses.s.store.Meta().ExpungeMessages(ctx, ses.sel.id, ids)
		expungeTimer.Done()
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "", "expunge failed")
		}
	}
	// Emit untagged EXPUNGEs in descending seq order (RFC 3501 §6.4.3).
	for i := len(seqs) - 1; i >= 0; i-- {
		_ = ses.resp.untagged(fmt.Sprintf("%d EXPUNGE", seqs[i]))
	}
	_ = ses.reloadSelected(ctx)
	return ses.resp.taggedOK(c.Tag, "", c.Op+" completed")
}

// ----- IDLE -----

// handleIDLE enters the IDLE flow: send continuation, poll change feed,
// write untagged updates, wait for DONE / ctx cancel / timeout.
func (ses *session) handleIDLE(ctx context.Context, c *Command) error {
	if !ses.requireSelected(c.Tag) {
		return nil
	}
	if err := ses.resp.continuation("idling"); err != nil {
		return err
	}
	observe.IMAPIdleActive.Inc()
	defer observe.IMAPIdleActive.Dec()
	idleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start a goroutine to read DONE from the client.
	doneCh := make(chan error, 1)
	go func() {
		for {
			_ = ses.conn.SetReadDeadline(time.Time{}) // no deadline during IDLE read wait
			line, err := readLine(ses.br)
			if err != nil {
				doneCh <- err
				return
			}
			if strings.EqualFold(strings.TrimSpace(line), "DONE") {
				doneCh <- nil
				return
			}
		}
	}()

	// Change-feed poller.
	ses.selMu.Lock()
	mailboxID := ses.sel.id
	ses.selMu.Unlock()

	pollInterval := 200 * time.Millisecond
	maxDuration := ses.s.opts.IdleMaxDuration
	deadline := ses.s.clk.Now().Add(maxDuration)
	var cursor store.ChangeSeq // start from current highest-observed

	// Get the starting cursor so we only report new events.
	rcfTimer := observe.StartStoreOp("read_change_feed")
	changes, err := ses.s.store.Meta().ReadChangeFeed(idleCtx, ses.pid, 0, 10000)
	rcfTimer.Done()
	if err == nil && len(changes) > 0 {
		cursor = changes[len(changes)-1].Seq
	}

	for {
		select {
		case rerr := <-doneCh:
			_ = ses.conn.SetReadDeadline(ses.s.clk.Now().Add(30 * time.Minute))
			if rerr != nil {
				return rerr
			}
			return ses.resp.taggedOK(c.Tag, "", "IDLE terminated")
		case <-idleCtx.Done():
			return idleCtx.Err()
		case <-ses.s.clk.After(pollInterval):
		}
		if ses.s.clk.Now().After(deadline) {
			_ = ses.resp.writeLine("* BYE IDLE timeout")
			return io.EOF
		}
		rcfTimer := observe.StartStoreOp("read_change_feed")
		changes, err := ses.s.store.Meta().ReadChangeFeed(idleCtx, ses.pid, cursor, 1024)
		rcfTimer.Done()
		if err != nil {
			continue
		}
		newMessages := false
		expunged := []store.UID{}
		flagsChanged := []store.MessageID{}
		for _, ch := range changes {
			cursor = ch.Seq
			if ch.MailboxID != 0 && ch.MailboxID != mailboxID {
				continue
			}
			switch ch.Kind {
			case store.ChangeKindMessageCreated:
				newMessages = true
			case store.ChangeKindMessageDestroyed:
				expunged = append(expunged, ch.MessageUID)
			case store.ChangeKindMessageUpdated:
				flagsChanged = append(flagsChanged, ch.MessageID)
			}
		}
		if newMessages {
			if err := ses.reloadSelected(idleCtx); err == nil {
				ses.selMu.Lock()
				n := len(ses.sel.msgs)
				ses.selMu.Unlock()
				_ = ses.resp.untagged(fmt.Sprintf("%d EXISTS", n))
			}
		}
		if len(expunged) > 0 {
			if err := ses.reloadSelected(idleCtx); err == nil {
				// After reload, the expunged UIDs are gone; seq numbers
				// for remaining messages may have shifted. We emit one
				// untagged EXPUNGE per gone UID at its former position,
				// which requires looking up the pre-expunge sequence —
				// Phase 1 simplification: emit the current EXISTS so the
				// client resyncs rather than tracking exact seq numbers.
				ses.selMu.Lock()
				n := len(ses.sel.msgs)
				ses.selMu.Unlock()
				_ = ses.resp.untagged(fmt.Sprintf("%d EXISTS", n))
			}
		}
		for _, id := range flagsChanged {
			// Emit an untagged FETCH with the updated flags for clients
			// watching this mailbox.
			m, err := ses.s.store.Meta().GetMessage(idleCtx, id)
			if err != nil || m.MailboxID != mailboxID {
				continue
			}
			ses.selMu.Lock()
			seq := 0
			for i, mm := range ses.sel.msgs {
				if mm.ID == id {
					seq = i + 1
					ses.sel.msgs[i] = m
					break
				}
			}
			ses.selMu.Unlock()
			if seq > 0 {
				_ = ses.resp.untagged(fmt.Sprintf("%d FETCH (UID %d FLAGS %s)",
					seq, m.UID, flagListString(flagNamesFromMask(m.Flags, m.Keywords))))
			}
		}
	}
}
