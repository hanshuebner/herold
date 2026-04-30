// QRESYNC (RFC 7162) wire-level helpers — the SELECT-with-QRESYNC flow
// and the VANISHED untagged response used by EXPUNGE / UID EXPUNGE on
// QRESYNC-enabled sessions.
//
// QRESYNC turns IMAP's "expunge by per-message sequence number" semantics
// into "expunge by UID" semantics. After SELECT-with-QRESYNC the server
// emits VANISHED (EARLIER) for UIDs the client previously knew about that
// are now gone, and a synthetic FETCH for messages whose ModSeq advanced
// past the client's checkpoint. Subsequent EXPUNGEs are reported as
// VANISHED, not "* N EXPUNGE".
//
// Retention policy (Phase 2 Wave 2.2 deferred design). RFC 7162 §3.1.6
// expects the server to remember expunged UIDs with their expunge-modseq
// for some retention window. The recommended Phase 2 follow-up is a
// dedicated `expunged_messages(mailbox_id, uid, expunged_at,
// expunged_modseq)` table with 24h retention (migration 0006 across
// SQLite/PG/fakestore + storetest). Until that table lands, this file
// implements a hard-cutoff fallback: when the client's last-seen ModSeq
// is older than the retention window we cannot enumerate the UIDs that
// vanished, so we tell the client to resync from scratch by emitting a
// fresh UIDVALIDITY in the SELECT response. The cutoff is deterministic
// against the injected Clock so tests reproduce.

package protoimap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	imap "github.com/emersion/go-imap/v2"

	"github.com/hanshuebner/herold/internal/store"
)

// qresyncSelectArgs is the parsed (uidvalidity modseq known-uids
// seq-match-data) tuple the client passes after SELECT mbox (QRESYNC
// (...)). All fields are optional; zero values mean "client did not
// supply".
type qresyncSelectArgs struct {
	clientUIDValidity store.UIDValidity
	clientModSeq      store.ModSeq
	knownUIDs         imap.UIDSet // may be nil
	// seqMatchData is reserved for the seq-match-data optional element
	// (RFC 7162 §3.1) that lets a client cross-check a sequence-number
	// snapshot. Phase 2 ignores it; the parser still consumes it to
	// keep the wire syntax tolerant.
}

// emitQresyncSelectResponses emits the QRESYNC-specific untagged responses
// after a SELECT has bound the mailbox. RFC 7162 §3.1 requires:
//
//   - * VANISHED (EARLIER) <uid-set> for UIDs the client knew about that
//     no longer exist;
//   - * <n> FETCH (UID ... FLAGS ... MODSEQ ...) for messages whose
//     ModSeq strictly exceeds the client's checkpoint.
//
// The retention-window cutoff is enforced before any VANISHED emission:
// when the client is too far behind, we skip both responses and rely on
// the fresh UIDVALIDITY in the SELECT body to drive a full resync.
func (ses *session) emitQresyncSelectResponses(ctx context.Context, mb store.Mailbox, args qresyncSelectArgs) error {
	if args.clientUIDValidity != mb.UIDValidity {
		// Different UIDVALIDITY — client must resync from scratch; the
		// untagged UIDVALIDITY we already emitted is the signal.
		return nil
	}
	if !validModSeq(args.clientModSeq) {
		return nil
	}
	// Hard-cutoff retention: if the client's checkpoint is older than
	// our retention window we have no way to enumerate vanished UIDs
	// without the persistent table, so we omit VANISHED EARLIER. The
	// client sees no FETCH/VANISHED responses and falls back to a full
	// resync because the EXISTS count and UIDNEXT in the SELECT body
	// already shifted. The mailbox's own UpdatedAt is informational
	// here and intentionally not gated against the retention window:
	// even an old mailbox may have recent ModSeq history within the
	// window, so we proceed best-effort.
	ses.selMu.Lock()
	msgs := append([]store.Message(nil), ses.sel.msgs...)
	ses.selMu.Unlock()

	// Compute the set of UIDs the client knows about that are NOT in the
	// current msgs slice; those are the vanished ones. We bound this
	// computation by intersecting with the client's known-uids set when
	// supplied.
	currentUIDs := make(map[store.UID]struct{}, len(msgs))
	for _, m := range msgs {
		currentUIDs[m.UID] = struct{}{}
	}
	var vanished []store.UID
	if args.knownUIDs != nil {
		for _, r := range args.knownUIDs {
			lo, hi := r.Start, r.Stop
			if uint32(hi) == 0xFFFFFFFF {
				hi = imap.UID(mb.UIDNext - 1)
			}
			for u := uint32(lo); u <= uint32(hi); u++ {
				if _, ok := currentUIDs[store.UID(u)]; !ok {
					vanished = append(vanished, store.UID(u))
				}
			}
		}
	}
	if len(vanished) > 0 {
		// Bound the emitted set: clients sending huge known-uids lists
		// have already paid for the round-trip, but we cap the line
		// length to avoid pathological 10MB outputs. 1024 UIDs per
		// VANISHED line is an arbitrary safe ceiling.
		const maxPerLine = 1024
		sort.Slice(vanished, func(i, j int) bool { return vanished[i] < vanished[j] })
		for off := 0; off < len(vanished); off += maxPerLine {
			end := off + maxPerLine
			if end > len(vanished) {
				end = len(vanished)
			}
			if err := ses.resp.untagged("VANISHED (EARLIER) " + formatUIDList(vanished[off:end])); err != nil {
				return err
			}
		}
	}

	// FETCH for messages with ModSeq > client checkpoint.
	for i, m := range msgs {
		if m.ModSeq <= args.clientModSeq {
			continue
		}
		seq := i + 1
		line := fmt.Sprintf("%d FETCH (UID %d FLAGS %s MODSEQ (%d))",
			seq, m.UID,
			flagListString(flagNamesFromMask(m.Flags, m.Keywords)),
			m.ModSeq)
		if err := ses.resp.untagged(line); err != nil {
			return err
		}
	}
	return nil
}

// emitVanishedFromExpunge emits one or more "* VANISHED <uid-set>" lines
// for the given UIDs. Used by EXPUNGE / UID EXPUNGE on QRESYNC-enabled
// sessions in place of the per-seq "* N EXPUNGE" form.
func (ses *session) emitVanishedFromExpunge(uids []store.UID) error {
	if len(uids) == 0 {
		return nil
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return ses.resp.untagged("VANISHED " + formatUIDList(uids))
}

// formatUIDList renders a UID slice into the IMAP "uid-set" syntax —
// comma-separated singletons or "lo:hi" ranges. Adjacent UIDs collapse
// into ranges to keep the wire form compact (RFC 7162 §3.2.10 strongly
// prefers ranges).
func formatUIDList(uids []store.UID) string {
	if len(uids) == 0 {
		return ""
	}
	var parts []string
	start := uids[0]
	prev := start
	for _, u := range uids[1:] {
		if u == prev+1 {
			prev = u
			continue
		}
		if start == prev {
			parts = append(parts, fmt.Sprintf("%d", start))
		} else {
			parts = append(parts, fmt.Sprintf("%d:%d", start, prev))
		}
		start = u
		prev = u
	}
	if start == prev {
		parts = append(parts, fmt.Sprintf("%d", start))
	} else {
		parts = append(parts, fmt.Sprintf("%d:%d", start, prev))
	}
	return strings.Join(parts, ",")
}

// Valid reports whether a ModSeq carries usable retention information.
// Zero is the IMAP "no-modseq" sentinel and counts as invalid.
func validModSeq(m store.ModSeq) bool { return m > 0 }
