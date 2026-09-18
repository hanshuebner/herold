package admin

// spam_undo_recipient_only.go — `herold spam reclassify
// --undo-recipient-only` (re #396, third round, required outcome 3):
// repairs messages the second round's decisive-signal resolution moved
// to Junk on a standalone recipient_not_own match, now that
// recipient_not_own alone is no longer a decisive rule
// (internal/spam/classifier.go's DefaultDecisiveSpamSignals).
//
// Unlike spam_apply_verdicts.go / spam_reclassify.go's --undo, which
// replays a CSV log of moves a PRIOR reclassify/apply-verdicts run wrote,
// these messages were routed straight into Junk at delivery/import time
// -- there is no prior membership to restore, and no undo log exists for
// them at all. The repair instead applies what a Ham verdict would have
// produced (REQ-FILT-02): out of Junk, into the principal's Inbox, with
// the stored verdict corrected to match.
//
// Selection recomputes today's decisiveness against each candidate's
// already-stored spam_signals (spam.MatchesDecisiveSignal), rather than
// hard-coding "signals == [recipient_not_own]": a record that still
// matches a current decisive rule (e.g. recipient_not_own combined with
// bulk_list_relay, or an independently decisive unsolicited_bulk_
// marketing) is left alone -- only a row whose resolution depended
// solely on the second round's now-retired standalone rule is repaired.

import (
	"context"
	"fmt"

	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// SpamUndoRecipientOnlySummary totals one `--undo-recipient-only` run.
type SpamUndoRecipientOnlySummary struct {
	// Scanned counts every message that carries a recorded spam
	// sub-record (applied verdict + model verdict both present).
	Scanned int `json:"scanned"`
	// Repaired counts messages moved out of Junk (or, under --dry-run,
	// that would be).
	Repaired int `json:"repaired"`
	// Skipped counts scanned messages that did not match the repair
	// criteria (already ham, still decisive under today's rules, no
	// recipient_not_own signal, or no longer in Junk).
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
}

// undoRecipientOnlyOverrides scans pid's messages for an llm_classifications
// record whose applied verdict is spam, whose recorded model verdict is
// ham, whose stored spam_signals name recipient_not_own, and whose
// decisiveness does NOT survive a recompute against decisiveRules (the
// classifier's current [spam].decisive_spam_signals, or
// spam.DefaultDecisiveSpamSignals when unconfigured) -- i.e. a row whose
// only historical decisive match was the second round's now-retired
// standalone recipient_not_own rule. Each match is moved out of Junk into
// the principal's Inbox (keeping any Sent/Drafts membership alongside it,
// mirroring moveMessageToJunk's inverse) and the stored verdict is
// corrected to ham via CorrectLLMClassificationVerdict, which also clears
// the now-stale spam_model_verdict divergence marker. Idempotent: a
// message no longer in Junk, or already recorded ham, is skipped rather
// than erroring. dryRun reports the candidate count without writing
// anything.
func undoRecipientOnlyOverrides(
	ctx context.Context,
	st store.Store,
	pid store.PrincipalID,
	decisiveRules []string,
	dryRun bool,
) (SpamUndoRecipientOnlySummary, error) {
	var sum SpamUndoRecipientOnlySummary

	mailboxes, err := st.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return sum, fmt.Errorf("list mailboxes for principal %d: %w", pid, err)
	}
	mbByID := make(map[store.MailboxID]store.Mailbox, len(mailboxes))
	for _, mb := range mailboxes {
		mbByID[mb.ID] = mb
	}
	inbox := store.ResolveInboxMailbox(mailboxes)
	if inbox == nil {
		return sum, fmt.Errorf("principal %d has no Inbox mailbox", pid)
	}

	candidates, err := selectReclassifyCandidates(ctx, st, mailboxes, nil)
	if err != nil {
		return sum, err
	}
	recs, err := st.Meta().BatchGetLLMClassifications(ctx, candidates)
	if err != nil {
		return sum, fmt.Errorf("batch get llm classifications for principal %d: %w", pid, err)
	}

	for _, mid := range candidates {
		rec, ok := recs[mid]
		if !ok || rec.SpamVerdict == nil || rec.SpamModelVerdict == nil {
			continue
		}
		sum.Scanned++

		if *rec.SpamVerdict != spam.Spam.String() || *rec.SpamModelVerdict != spam.Ham.String() {
			sum.Skipped++
			continue
		}
		var signals []string
		if rec.SpamSignals != nil {
			signals = *rec.SpamSignals
		}
		if !spam.HasSignal(signals, "recipient_not_own") {
			sum.Skipped++
			continue
		}
		if _, stillDecisive := spam.MatchesDecisiveSignal(signals, decisiveRules); stillDecisive {
			// Still decisive under today's rules -- e.g. combined with
			// bulk_list_relay, or independently via a signal like
			// unsolicited_bulk_marketing that also happens to be
			// present. Not one of the false positives to repair.
			sum.Skipped++
			continue
		}

		msg, err := st.Meta().GetMessage(ctx, mid)
		if err != nil {
			sum.Errors++
			continue
		}
		inJunk := false
		for _, mm := range msg.Mailboxes {
			if mb, ok := mbByID[mm.MailboxID]; ok && mb.Attributes&store.MailboxAttrJunk != 0 {
				inJunk = true
			}
		}
		if !inJunk {
			sum.Skipped++
			continue
		}

		if dryRun {
			sum.Repaired++
			continue
		}
		if err := moveMessageOutOfJunk(ctx, st.Meta(), msg, mbByID, inbox.ID); err != nil {
			sum.Errors++
			continue
		}
		if err := st.Meta().CorrectLLMClassificationVerdict(ctx, mid, spam.Ham.String()); err != nil {
			sum.Errors++
			continue
		}
		sum.Repaired++
	}

	return sum, nil
}

// moveMessageOutOfJunk moves msg out of the Junk mailbox into inboxID,
// keeping any Sent/Drafts membership alongside it -- the inverse of
// moveMessageToJunk (spam_reclassify.go), applied here because these
// messages were routed straight into Junk at delivery/import time and so
// have no prior membership to restore: the repair applies what the
// corrected Ham verdict would have produced instead (REQ-FILT-02).
func moveMessageOutOfJunk(ctx context.Context, meta store.Metadata, msg store.Message, mbByID map[store.MailboxID]store.Mailbox, inboxID store.MailboxID) error {
	current := make(map[store.MailboxID]struct{}, len(msg.Mailboxes))
	for _, mm := range msg.Mailboxes {
		current[mm.MailboxID] = struct{}{}
	}
	desired := map[store.MailboxID]struct{}{inboxID: {}}
	for id := range current {
		if mb, ok := mbByID[id]; ok && mb.Attributes&(store.MailboxAttrSent|store.MailboxAttrDrafts) != 0 {
			desired[id] = struct{}{}
		}
	}
	return applyMailboxSetDiff(ctx, meta, msg.ID, current, desired)
}
