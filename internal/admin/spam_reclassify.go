package admin

// spam_reclassify.go — `herold spam reclassify`, the online counterpart to
// `herold spam apply-verdicts` (issue #318): where apply-verdicts applies an
// offline batch-classification CSV, reclassify re-runs the configured
// classifier plugin itself over a principal's already-stored mail, for
// messages that missed classification during a plugin outage (#317) or on a
// freshly enabled classifier. It uses the same spam.BuildRequest projection
// SMTP delivery and the IMAP import path use, but -- unlike the IMAP import
// path, which never has a herold-side auth verdict to reuse
// (internal/admin/imap_import_spam.go, REQ-IMAP-IMP-33) -- a message that
// was itself delivered by SMTP carries herold's own delivery-time
// Authentication-Results header on its stored blob (buildHeaderPrefix,
// internal/protosmtp/deliver.go): deliveryAuthResults recovers that
// verdict so the classifier request's spf/dkim/dmarc fields agree with the
// rendered auth_results string instead of contradicting it (re #385). It
// shares the same REQ-FILT-02 routing / undo-log format apply-verdicts
// already implements (moveMessageToJunk / applyMailboxSetDiff /
// writeSpamUndoRow), so `--undo` restores either command's moves
// interchangeably. delivery_disposition is never touched: it is set once
// at ingest and is not this command's concern.

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// SpamReclassifySummary totals one `spam reclassify` run.
type SpamReclassifySummary struct {
	Selected   int `json:"selected"`
	Classified int `json:"classified"`
	Spam       int `json:"spam"`
	Suspect    int `json:"suspect"`
	Ham        int `json:"ham"`
	Moved      int `json:"moved"`
	Errors     int `json:"errors"`
	Skipped    int `json:"skipped"`
}

// spamReclassifyOptions configures reclassifySpam.
type spamReclassifyOptions struct {
	// Since, when non-nil, restricts selection to messages whose
	// ReceivedAt is at or after this instant.
	Since *time.Time
	// UnclassifiedOnly, when true (the default), skips a selected
	// message that already carries a recorded SpamVerdict.
	UnclassifiedOnly bool
	// Limit caps the number of selected messages processed. 0 means
	// unlimited.
	Limit int
	// DryRun reports what would happen without writing the
	// classification record, moving/keywording any message, or
	// writing UndoLog.
	DryRun bool
	// UndoLog, when non-nil, receives one row (message id, ';'-joined
	// previous mailbox ids) per moved message, written before the
	// move is applied -- the same format spam_apply_verdicts.go
	// writes, so `spam apply-verdicts --undo` restores it too.
	UndoLog *csv.Writer
}

// reclassifySpam selects pid's messages (every mailbox, deduplicated) that
// satisfy opts.Since / opts.UnclassifiedOnly, re-runs cls against each
// selected message's stored blob under pluginName, records the verdict
// (engine = pluginName), and applies the REQ-FILT-02 routing: spam moves
// into the principal's Junk mailbox (unless already Junk/Trash, mirroring
// applySpamVerdicts' AlreadyJunk handling), suspect gains the "$Junk"
// keyword on every mailbox the message currently sits in, ham is left in
// place. Any error while processing one message (store read/parse
// failure, plugin error, write failure) is counted in Errors and the loop
// continues to the next message.
func reclassifySpam(
	ctx context.Context,
	st store.Store,
	clk clock.Clock,
	cls *spam.Classifier,
	pluginName string,
	pid store.PrincipalID,
	opts spamReclassifyOptions,
) (SpamReclassifySummary, error) {
	var sum SpamReclassifySummary

	mailboxes, err := st.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		return sum, fmt.Errorf("list mailboxes for principal %d: %w", pid, err)
	}
	mbByID := make(map[store.MailboxID]store.Mailbox, len(mailboxes))
	var junkID store.MailboxID
	for _, mb := range mailboxes {
		mbByID[mb.ID] = mb
		if mb.Attributes&store.MailboxAttrJunk != 0 && junkID == 0 {
			junkID = mb.ID
		}
	}

	candidates, err := selectReclassifyCandidates(ctx, st, mailboxes, opts.Since)
	if err != nil {
		return sum, err
	}
	if opts.Limit > 0 && len(candidates) > opts.Limit {
		candidates = candidates[:opts.Limit]
	}

	// re #386: own_addresses is the same for every candidate message in
	// this run (they all belong to pid); resolve it once and reuse
	// across the loop instead of a store round-trip per message.
	ownAddrCache := make(map[store.PrincipalID][]string)

	existing, err := st.Meta().BatchGetLLMClassifications(ctx, candidates)
	if err != nil {
		return sum, fmt.Errorf("batch get llm classifications for principal %d: %w", pid, err)
	}

	for _, mid := range candidates {
		sum.Selected++

		if opts.UnclassifiedOnly {
			// A recorded verdict of "unclassified" (re #326: a timeout /
			// plugin-error / unparseable-output outcome, now persisted
			// instead of leaving no row at all) counts as missed, same
			// as no row: --unclassified-only exists to catch exactly
			// these, so only a genuine ham/spam/suspect verdict skips.
			if rec, ok := existing[mid]; ok && rec.SpamVerdict != nil && *rec.SpamVerdict != spam.Unclassified.String() {
				sum.Skipped++
				continue
			}
		}

		parsed, msg, err := loadReclassifyMessage(ctx, st, mid)
		if err != nil {
			sum.Errors++
			continue
		}
		auth := deliveryAuthResults(msg, parsed)

		ownAddresses, oerr := spam.ResolveOwnAddresses(ctx, st.Meta(), pid, ownAddrCache)
		if oerr != nil {
			sum.Errors++
			continue
		}

		// Complete: true (re #396, third round) -- reclassify's ownAddresses
		// is spam.ResolveOwnAddresses's full principal-wide set, the same
		// as SMTP delivery; see internal/protosmtp/deliver.go's Classify
		// call site for the parallel reasoning.
		cl, err := cls.Classify(ctx, parsed, auth, pluginName, spam.ClassifyContext{},
			spam.OwnAddressInfo{Addresses: ownAddresses, Complete: true})
		if err != nil {
			sum.Errors++
			continue
		}

		if err := recordReclassifyVerdict(ctx, st, clk, pid, mid, pluginName, parsed, auth, ownAddresses, cl, opts.DryRun); err != nil {
			sum.Errors++
			continue
		}
		sum.Classified++

		switch cl.Verdict {
		case spam.Spam:
			sum.Spam++
			moved, err := moveMessageToJunk(ctx, st.Meta(), msg, mbByID, junkID, opts.DryRun, opts.UndoLog)
			if err != nil {
				sum.Errors++
				continue
			}
			if moved {
				sum.Moved++
			}
		case spam.Suspect:
			sum.Suspect++
			if !opts.DryRun {
				if err := addSuspectJunkKeyword(ctx, st.Meta(), msg); err != nil {
					sum.Errors++
					continue
				}
			}
		case spam.Ham:
			sum.Ham++
		}
	}

	return sum, nil
}

// selectReclassifyCandidates pages through every mailbox in mailboxes via
// ListMessages, collects the deduplicated set of message ids whose
// ReceivedAt is at or after since (nil since means no cutoff), and returns
// them in ascending MessageID order so a run's processing order -- and
// therefore its undo log -- is deterministic regardless of mailbox
// iteration order.
func selectReclassifyCandidates(ctx context.Context, st store.Store, mailboxes []store.Mailbox, since *time.Time) ([]store.MessageID, error) {
	const pageSize = 500
	seen := make(map[store.MessageID]struct{})
	var candidates []store.MessageID
	for _, mb := range mailboxes {
		var after store.UID
		for {
			msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{AfterUID: after, Limit: pageSize})
			if err != nil {
				return nil, fmt.Errorf("list messages in mailbox %d: %w", mb.ID, err)
			}
			if len(msgs) == 0 {
				break
			}
			for _, m := range msgs {
				after = m.UID
				if since != nil && m.ReceivedAt.Before(*since) {
					continue
				}
				if _, ok := seen[m.ID]; ok {
					continue
				}
				seen[m.ID] = struct{}{}
				candidates = append(candidates, m.ID)
			}
			if len(msgs) < pageSize {
				break
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	return candidates, nil
}

// loadReclassifyMessage loads mid's full store row (Mailboxes populated,
// for the routing step) and its parsed RFC 822 form (for
// spam.BuildRequest / cls.Classify), reading the blob under the same
// bounded read as internal/admin's IMAP-import categoriser adapter.
func loadReclassifyMessage(ctx context.Context, st store.Store, mid store.MessageID) (mailparse.Message, store.Message, error) {
	msg, err := st.Meta().GetMessage(ctx, mid)
	if err != nil {
		return mailparse.Message{}, store.Message{}, fmt.Errorf("get message %d: %w", mid, err)
	}
	parsed, err := parseStoredMessageBlob(ctx, st, msg)
	if err != nil {
		return mailparse.Message{}, store.Message{}, err
	}
	return parsed, msg, nil
}

// parseStoredMessageBlob reads msg's blob under the same bounded read as
// internal/admin's IMAP-import categoriser adapter and returns its parsed
// RFC 822 form, for a caller (applySpamVerdicts) that already holds msg
// from an earlier GetMessage call and would otherwise re-fetch it.
func parseStoredMessageBlob(ctx context.Context, st store.Store, msg store.Message) (mailparse.Message, error) {
	rc, err := st.Blobs().Get(ctx, msg.Blob.Hash)
	if err != nil {
		return mailparse.Message{}, fmt.Errorf("get blob for message %d: %w", msg.ID, err)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, maxImportMessageBytes+1))
	_ = rc.Close()
	if err != nil {
		return mailparse.Message{}, fmt.Errorf("read blob for message %d: %w", msg.ID, err)
	}
	parsed, err := mailparse.Parse(bytes.NewReader(raw), mailparse.NewLenientParseOptions())
	if err != nil {
		return mailparse.Message{}, fmt.Errorf("parse message %d: %w", msg.ID, err)
	}
	return parsed, nil
}

// recordReclassifyVerdict writes mid's llm_classifications spam
// sub-record from cl, mirroring internal/admin/imap_import_spam.go's
// RecordVerdict field-for-field (engine name = pluginName,
// SpamPromptApplied = the canonical spam.Request JSON built from the
// same auth argument the classify call used). A no-op under dryRun.
func recordReclassifyVerdict(
	ctx context.Context,
	st store.Store,
	clk clock.Clock,
	pid store.PrincipalID,
	mid store.MessageID,
	pluginName string,
	parsed mailparse.Message,
	auth *mailauth.AuthResults,
	ownAddresses []string,
	cl spam.Classification,
	dryRun bool,
) error {
	verdict := cl.Verdict.String()
	score := cl.Score
	rec := store.LLMClassificationRecord{
		MessageID:      mid,
		PrincipalID:    pid,
		SpamVerdict:    &verdict,
		SpamConfidence: &score,
	}
	if cl.Reason != "" {
		reason := cl.Reason
		rec.SpamReason = &reason
	}
	engine := pluginName
	rec.SpamModel = &engine
	rec.SpamSignals = spam.OptStringSlice(cl.SpamSignals)
	rec.HamSignals = spam.OptStringSlice(cl.HamSignals)
	inconsistent := cl.Inconsistent
	rec.SpamInconsistent = &inconsistent
	// SpamModelVerdict (re #396, second round) preserves the plugin's own
	// verdict when Classify server-resolved a Ham verdict to Spam/Suspect
	// on a decisive spam signal; nil (untouched) when no resolution
	// happened.
	if cl.ModelVerdict != spam.Unclassified {
		mv := cl.ModelVerdict.String()
		rec.SpamModelVerdict = &mv
	}
	classifiedAt := clk.Now()
	rec.SpamClassifiedAt = &classifiedAt
	req := spam.BuildRequest(parsed, auth)
	req.OwnAddresses = ownAddresses
	req.RecipientNotOwn = spam.RecipientNotOwn(parsed, ownAddresses)
	req.OwnAddressesComplete = true // re #396, third round: see the Classify call site above.
	if raw, err := req.Canonical(); err == nil {
		s := string(raw)
		rec.SpamPromptApplied = &s
	}
	if dryRun {
		return nil
	}
	if err := st.Meta().SetLLMClassification(ctx, rec); err != nil {
		return fmt.Errorf("set llm classification for message %d: %w", mid, err)
	}
	return nil
}

// deliveryAuthResults recovers the delivery-time Authentication-Results
// verdict herold itself stamped onto msg's stored blob, so reclassify and
// apply-verdicts build the same spam.Request SMTP delivery would have
// built rather than falling back to "none" for a message that already
// has a real verdict on file (re #385).
//
// It applies only to msg.IngestSource == store.IngestSourceSMTP: herold
// prepends its own "Authentication-Results:" header as the very first
// header of the stored blob only for SMTP-delivered inbound mail
// (buildHeaderPrefix, internal/protosmtp/deliver.go); an IMAP-imported,
// JMAP-imported, or APPENDed message's first Authentication-Results
// header (if any) is the foreign upstream's own, unverified claim, and
// reusing it would resurrect exactly the forgeable-header risk
// spam.BuildRequest's doc comment warns against (re #298). For any other
// ingest source, or an SMTP-delivered message whose first header does not
// parse as one of herold's own methods, this returns nil -- the same "no
// auth data" state spam.BuildRequest already renders as "none" on every
// method, never a false "fail".
func deliveryAuthResults(msg store.Message, parsed mailparse.Message) *mailauth.AuthResults {
	if msg.IngestSource != store.IngestSourceSMTP {
		return nil
	}
	vals := parsed.Headers.GetAll("Authentication-Results")
	if len(vals) == 0 {
		return nil
	}
	res, ok := mailauth.ParseAuthResults(vals[0])
	if !ok {
		return nil
	}
	return &res
}

// moveMessageToJunk applies applySpamVerdicts' spam-routing rule to a
// single already-loaded message: a message already sitting in a Junk- or
// Trash-attributed mailbox is left in place (returns false, nil);
// otherwise it is moved into the principal's Junk mailbox, keeping only
// Sent/Drafts memberships alongside it, through applyMailboxSetDiff --
// the same primitives spam_apply_verdicts.go uses, so UID/ModSeq/
// HighestModSeq advance exactly as a client-driven move would. Under
// dryRun, or when undo is non-nil, writeSpamUndoRow records the pre-move
// membership set before the diff is applied.
func moveMessageToJunk(
	ctx context.Context,
	meta store.Metadata,
	msg store.Message,
	mbByID map[store.MailboxID]store.Mailbox,
	junkID store.MailboxID,
	dryRun bool,
	undo *csv.Writer,
) (bool, error) {
	current := make(map[store.MailboxID]struct{}, len(msg.Mailboxes))
	alreadyPlaced := false
	for _, mm := range msg.Mailboxes {
		current[mm.MailboxID] = struct{}{}
		if mb, ok := mbByID[mm.MailboxID]; ok && mb.Attributes&(store.MailboxAttrJunk|store.MailboxAttrTrash) != 0 {
			alreadyPlaced = true
		}
	}
	if alreadyPlaced {
		return false, nil
	}
	if junkID == 0 {
		return false, fmt.Errorf("principal has no Junk mailbox provisioned")
	}

	desired := map[store.MailboxID]struct{}{junkID: {}}
	for id := range current {
		if mb, ok := mbByID[id]; ok && mb.Attributes&(store.MailboxAttrSent|store.MailboxAttrDrafts) != 0 {
			desired[id] = struct{}{}
		}
	}

	if dryRun {
		return true, nil
	}
	if undo != nil {
		if err := writeSpamUndoRow(undo, msg.ID, current); err != nil {
			return false, fmt.Errorf("write undo log for message %d: %w", msg.ID, err)
		}
	}
	if err := applyMailboxSetDiff(ctx, meta, msg.ID, current, desired); err != nil {
		return false, fmt.Errorf("move message %d to Junk: %w", msg.ID, err)
	}
	return true, nil
}

// addSuspectJunkKeyword applies REQ-FILT-02's suspect mapping ("stays in
// place, gains the $Junk keyword") to an already-stored message: the
// keyword is added to every mailbox the message currently belongs to, via
// UpdateMessageFlags, mirroring the per-mailbox keyword row model
// (REQ-STORE-36).
func addSuspectJunkKeyword(ctx context.Context, meta store.Metadata, msg store.Message) error {
	for _, mm := range msg.Mailboxes {
		if _, err := meta.UpdateMessageFlags(ctx, msg.ID, mm.MailboxID, 0, 0, []string{"$Junk"}, nil, 0); err != nil {
			return fmt.Errorf("add $Junk keyword to message %d in mailbox %d: %w", msg.ID, mm.MailboxID, err)
		}
	}
	return nil
}
