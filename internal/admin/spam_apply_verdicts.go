package admin

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

// spamVerdictRow is one parsed row of a batch-classification CSV: a
// message id, the verdict a batch classifier assigned it, the
// normalised [0,1] confidence, and the free-text reason.
type spamVerdictRow struct {
	LineNo     int
	MessageID  uint64
	Verdict    string
	Confidence float64
	Reason     string
}

// parseSpamVerdictsCSV reads a batch-classification CSV: a header row
// naming at least id, verdict, confidence, reason (case-insensitive,
// any order; extra columns are ignored), followed by one data row per
// classified message.
//
// verdict must be "spam" or "ham" (case-insensitive). confidence is
// accepted either as a 0..100 integer or a 0..1 float and normalised
// to 0..1 here so downstream code always sees the normalised value: a
// parsed value > 1 is assumed to be on the 0..100 scale and divided by
// 100.
func parseSpamVerdictsCSV(r io.Reader) ([]spamVerdictRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, want := range []string{"id", "verdict", "confidence", "reason"} {
		if _, ok := col[want]; !ok {
			return nil, fmt.Errorf("csv header missing required column %q", want)
		}
	}

	var rows []spamVerdictRow
	lineNo := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		lineNo++

		idStr := strings.TrimSpace(rec[col["id"]])
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid id %q: %w", lineNo, idStr, err)
		}

		verdict := strings.ToLower(strings.TrimSpace(rec[col["verdict"]]))
		if verdict != "spam" && verdict != "ham" {
			return nil, fmt.Errorf("line %d: verdict must be \"spam\" or \"ham\", got %q", lineNo, verdict)
		}

		confStr := strings.TrimSpace(rec[col["confidence"]])
		conf, err := strconv.ParseFloat(confStr, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid confidence %q: %w", lineNo, confStr, err)
		}
		if conf < 0 || conf > 100 {
			return nil, fmt.Errorf("line %d: confidence %v out of range [0,100]", lineNo, conf)
		}
		if conf > 1 {
			conf /= 100
		}

		rows = append(rows, spamVerdictRow{
			LineNo:     lineNo,
			MessageID:  id,
			Verdict:    verdict,
			Confidence: conf,
			Reason:     strings.TrimSpace(rec[col["reason"]]),
		})
	}
	return rows, nil
}

// SpamApplyVerdictsSummary totals one `spam apply-verdicts` run.
type SpamApplyVerdictsSummary struct {
	RowsRead              int `json:"rows_read"`
	Applied               int `json:"applied"`
	Moved                 int `json:"moved"`
	AlreadyJunk           int `json:"already_junk"`
	SkippedUnknown        int `json:"skipped_unknown"`
	SkippedOtherPrincipal int `json:"skipped_other_principal"`
}

// spamApplyVerdictsOptions configures applySpamVerdicts.
type spamApplyVerdictsOptions struct {
	// Engine is recorded as the llm_classifications spam_model value.
	Engine string
	// DryRun reports what would change without writing the
	// classification record, moving any message, or writing UndoLog.
	DryRun bool
	// MinConfidence gates the Junk move for spam rows: a spam row
	// whose Confidence is below MinConfidence is still recorded but
	// left in place.
	MinConfidence float64
	// UndoLog, when non-nil, receives one row (message id, ';'-joined
	// previous mailbox ids) per moved message, written before the
	// move is applied.
	UndoLog *csv.Writer
}

// applySpamVerdicts applies rows -- the parsed contents of a batch
// classification CSV -- to principal pid's stored mail:
//
//   - Every row belonging to pid gets its llm_classifications spam
//     sub-record written (or overwritten), ham and spam alike.
//   - A spam row whose message already sits in a Junk- or
//     Trash-attributed mailbox is left in place.
//   - Any other spam row at or above MinConfidence is moved into the
//     principal's Junk mailbox: every membership is replaced with
//     Junk except memberships in Sent- or Drafts-attributed mailboxes,
//     which are preserved. The move goes through the same
//     MoveMessage / AddMessageToMailbox / RemoveMessageFromMailbox
//     primitives a client-driven JMAP Email/set move uses, so UID,
//     ModSeq, and the mailbox's HighestModSeq all advance normally.
//
// Rows referencing an unknown message id or a message owned by a
// different principal are counted and skipped; delivery_disposition
// is never touched (it is set once at ingest and is not the concern
// of this after-the-fact reclassification).
func applySpamVerdicts(
	ctx context.Context,
	st store.Store,
	clk clock.Clock,
	pid store.PrincipalID,
	rows []spamVerdictRow,
	opts spamApplyVerdictsOptions,
) (SpamApplyVerdictsSummary, error) {
	var sum SpamApplyVerdictsSummary

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

	for _, row := range rows {
		sum.RowsRead++

		msg, err := st.Meta().GetMessage(ctx, store.MessageID(row.MessageID))
		if errors.Is(err, store.ErrNotFound) {
			sum.SkippedUnknown++
			continue
		}
		if err != nil {
			return sum, fmt.Errorf("get message %d (line %d): %w", row.MessageID, row.LineNo, err)
		}
		if msg.PrincipalID != pid {
			sum.SkippedOtherPrincipal++
			continue
		}

		verdict := row.Verdict
		conf := row.Confidence
		rec := store.LLMClassificationRecord{
			MessageID:      msg.ID,
			PrincipalID:    pid,
			SpamVerdict:    &verdict,
			SpamConfidence: &conf,
		}
		if row.Reason != "" {
			reason := row.Reason
			rec.SpamReason = &reason
		}
		engine := opts.Engine
		rec.SpamModel = &engine
		classifiedAt := clk.Now()
		rec.SpamClassifiedAt = &classifiedAt

		if !opts.DryRun {
			if err := st.Meta().SetLLMClassification(ctx, rec); err != nil {
				return sum, fmt.Errorf("set llm classification for message %d (line %d): %w", row.MessageID, row.LineNo, err)
			}
		}
		sum.Applied++

		if verdict != "spam" {
			continue
		}

		current := make(map[store.MailboxID]struct{}, len(msg.Mailboxes))
		alreadyPlaced := false
		for _, mm := range msg.Mailboxes {
			current[mm.MailboxID] = struct{}{}
			if mb, ok := mbByID[mm.MailboxID]; ok && mb.Attributes&(store.MailboxAttrJunk|store.MailboxAttrTrash) != 0 {
				alreadyPlaced = true
			}
		}
		if alreadyPlaced {
			sum.AlreadyJunk++
			continue
		}
		if conf < opts.MinConfidence {
			continue
		}
		if junkID == 0 {
			return sum, fmt.Errorf("principal %d has no Junk mailbox provisioned", pid)
		}

		desired := map[store.MailboxID]struct{}{junkID: {}}
		for id := range current {
			if mb, ok := mbByID[id]; ok && mb.Attributes&(store.MailboxAttrSent|store.MailboxAttrDrafts) != 0 {
				desired[id] = struct{}{}
			}
		}

		if !opts.DryRun {
			if opts.UndoLog != nil {
				if err := writeSpamUndoRow(opts.UndoLog, msg.ID, current); err != nil {
					return sum, fmt.Errorf("write undo log for message %d: %w", row.MessageID, err)
				}
			}
			if err := applyMailboxSetDiff(ctx, st.Meta(), msg.ID, current, desired); err != nil {
				return sum, fmt.Errorf("move message %d to Junk: %w", row.MessageID, err)
			}
		}
		sum.Moved++
	}
	return sum, nil
}

// applyMailboxSetDiff reconciles a message's current mailbox
// membership set with the desired set using the same primitives (and
// the same single-add/single-remove MoveMessage fast path) a
// client-driven JMAP Email/set mailboxIds patch uses, so UID, ModSeq,
// and each mailbox's HighestModSeq advance exactly as they would for
// a real client move. New memberships are added before old ones are
// removed so the message is never transiently without a membership
// (which would delete its row).
func applyMailboxSetDiff(ctx context.Context, meta store.Metadata, msgID store.MessageID, current, desired map[store.MailboxID]struct{}) error {
	var add, remove []store.MailboxID
	for id := range desired {
		if _, ok := current[id]; !ok {
			add = append(add, id)
		}
	}
	for id := range current {
		if _, ok := desired[id]; !ok {
			remove = append(remove, id)
		}
	}
	sort.Slice(add, func(i, j int) bool { return add[i] < add[j] })
	sort.Slice(remove, func(i, j int) bool { return remove[i] < remove[j] })

	if len(add) == 1 && len(remove) == 1 {
		if err := meta.MoveMessage(ctx, msgID, remove[0], add[0]); err != nil {
			return err
		}
		return nil
	}
	for _, id := range add {
		if _, _, err := meta.AddMessageToMailbox(ctx, msgID, id); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	for _, id := range remove {
		if err := meta.RemoveMessageFromMailbox(ctx, msgID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// writeSpamUndoRow appends one undo-log row: the message id and the
// ';'-joined, numerically-sorted mailbox ids the message belonged to
// immediately before a move.
func writeSpamUndoRow(w *csv.Writer, msgID store.MessageID, current map[store.MailboxID]struct{}) error {
	ids := make([]uint64, 0, len(current))
	for id := range current {
		ids = append(ids, uint64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10)
	}
	return w.Write([]string{
		strconv.FormatUint(uint64(msgID), 10),
		strings.Join(parts, ";"),
	})
}

// spamUndoRow is one parsed row of an undo log written by a prior
// `spam apply-verdicts --undo-log` run.
type spamUndoRow struct {
	MessageID          uint64
	PreviousMailboxIDs []uint64
}

// parseSpamUndoCSV reads an undo log: a header row naming message_id
// and previous_mailbox_ids (case-insensitive, any order), followed by
// one row per moved message.
func parseSpamUndoCSV(r io.Reader) ([]spamUndoRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, want := range []string{"message_id", "previous_mailbox_ids"} {
		if _, ok := col[want]; !ok {
			return nil, fmt.Errorf("undo log header missing required column %q", want)
		}
	}

	var rows []spamUndoRow
	lineNo := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		lineNo++

		idStr := strings.TrimSpace(rec[col["message_id"]])
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid message_id %q: %w", lineNo, idStr, err)
		}

		idsStr := strings.TrimSpace(rec[col["previous_mailbox_ids"]])
		var prev []uint64
		if idsStr != "" {
			for _, p := range strings.Split(idsStr, ";") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				mbID, err := strconv.ParseUint(p, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("line %d: invalid previous_mailbox_ids entry %q: %w", lineNo, p, err)
				}
				prev = append(prev, mbID)
			}
		}
		rows = append(rows, spamUndoRow{MessageID: id, PreviousMailboxIDs: prev})
	}
	return rows, nil
}

// SpamUndoVerdictsSummary totals one `spam apply-verdicts --undo` run.
type SpamUndoVerdictsSummary struct {
	RowsRead              int `json:"rows_read"`
	Restored              int `json:"restored"`
	SkippedUnknown        int `json:"skipped_unknown"`
	SkippedOtherPrincipal int `json:"skipped_other_principal"`
}

// undoSpamVerdicts restores each message named in rows to the mailbox
// membership set recorded before applySpamVerdicts moved it: it diffs
// the message's current memberships against PreviousMailboxIDs and
// applies exactly that diff (through applyMailboxSetDiff, the same
// primitives the forward move used). The llm_classifications record
// written by the original apply run is intentionally left in place --
// undo reverts placement, not the classification transparency record.
func undoSpamVerdicts(ctx context.Context, st store.Store, pid store.PrincipalID, rows []spamUndoRow, dryRun bool) (SpamUndoVerdictsSummary, error) {
	var sum SpamUndoVerdictsSummary
	for _, row := range rows {
		sum.RowsRead++

		msg, err := st.Meta().GetMessage(ctx, store.MessageID(row.MessageID))
		if errors.Is(err, store.ErrNotFound) {
			sum.SkippedUnknown++
			continue
		}
		if err != nil {
			return sum, fmt.Errorf("get message %d: %w", row.MessageID, err)
		}
		if msg.PrincipalID != pid {
			sum.SkippedOtherPrincipal++
			continue
		}

		current := make(map[store.MailboxID]struct{}, len(msg.Mailboxes))
		for _, mm := range msg.Mailboxes {
			current[mm.MailboxID] = struct{}{}
		}
		desired := make(map[store.MailboxID]struct{}, len(row.PreviousMailboxIDs))
		for _, id := range row.PreviousMailboxIDs {
			desired[store.MailboxID(id)] = struct{}{}
		}

		if !dryRun {
			if err := applyMailboxSetDiff(ctx, st.Meta(), msg.ID, current, desired); err != nil {
				return sum, fmt.Errorf("restore message %d: %w", row.MessageID, err)
			}
		}
		sum.Restored++
	}
	return sum, nil
}

// resolveStorePrincipal resolves ref (a numeric principal id or a
// canonical email address) directly against the store, without going
// through the admin REST API -- `spam apply-verdicts` is a store-backed
// maintenance command in the family of `diag reparse-envelopes` /
// `diag recompute-bodymeta`, not a REST client.
func resolveStorePrincipal(ctx context.Context, st store.Store, ref string) (store.Principal, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return store.Principal{}, errors.New("principal reference required")
	}
	if id, err := strconv.ParseUint(ref, 10, 64); err == nil {
		p, err := st.Meta().GetPrincipalByID(ctx, store.PrincipalID(id))
		if err != nil {
			return store.Principal{}, fmt.Errorf("principal %q: %w", ref, err)
		}
		return p, nil
	}
	p, err := st.Meta().GetPrincipalByEmail(ctx, strings.ToLower(ref))
	if err != nil {
		return store.Principal{}, fmt.Errorf("principal %q: %w", ref, err)
	}
	return p, nil
}
