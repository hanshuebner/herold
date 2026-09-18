package imapimport

// ownaddresses.go implements the per-account own-address learning that
// closes the false-positive gap re #396's third round reported:
// own_addresses (internal/spam/own_addresses.go) was built only from the
// principal's identities/aliases, so an IMAP-import account's upstream
// mailbox that also accepts mail at addresses herold never resolves as
// the principal's (a shared organisational mailbox's info@/vorstand@
// alias) made recipient_not_own true for every message delivered to
// those addresses -- turning legitimate transactional mail into
// false-positive Junk moves once recipient_not_own became decisive.
//
// Two mechanisms populate store.IMAPImportAccount.LearnedAddresses, both
// funnelling through learnOwnAddresses so they share one persistence
// path:
//
//   - Incremental: ingestMessage (sync.go) calls extractDeliveredAddresses
//     on every freshly inserted message (live arrival or historical
//     backfill) and merges any new address in. This is what keeps the
//     set current going forward.
//   - Startup backfill: runOwnAddressBackfill walks every message this
//     account has already imported -- reachable via its
//     imapimport_message_state rows (REQ-IMAP-IMP-34), the same
//     account-scoped enumeration seenbackfill.go uses -- exactly once
//     per account for its whole lifetime, gated on the persisted
//     AddressesLearnedAt column rather than an in-process flag (unlike
//     seenBackfillDone): the scan reads one blob header per historical
//     message, a cost worth paying once ever, not once per worker
//     restart. This is what makes info@ and vorstand@classic-computing.de
//     appear in the set for an already-imported account without any
//     manual configuration, the very first time the fixed binary runs a
//     session for that account.
//
// Both call learnOwnAddresses, which always calls
// store.SetIMAPImportLearnedAddresses at least once when the account's
// AddressesLearnedAt is still nil -- even with zero addresses found --
// because a completed-but-empty learning pass is what marks the
// account's own-address set "known complete"
// (store.IMAPImportAccount.OwnAddressesComplete): spam.Classifier's
// decisive-signal resolution (internal/spam/classifier.go) never lets a
// recipient_not_own fact from an incomplete account contribute to a
// decisive spam resolution.

import (
	"bytes"
	"context"
	"io"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// ownAddressHeaderScanBytes bounds the blob prefix runOwnAddressBackfill
// reads per historical message: headers sit at the start of the RFC822
// blob, well under this limit, and net/mail.ReadMessage only needs the
// header block plus the blank-line separator -- it never reads the body
// -- so truncating here does not risk cutting a header short in any
// message this codebase actually stores.
const ownAddressHeaderScanBytes = 64 * 1024

// maxLearnedAddresses bounds the size of an account's persisted
// learned-address set (REQ-IMAP-IMP-36). A legitimate organisational
// mailbox has a handful of aliases (info@, vorstand@, ...); an upstream
// that stamps a new, effectively unbounded X-Original-To on every
// message (a mailing-list relay rewriting per-recipient, a broken
// upstream) must not be allowed to grow this set without limit --
// unbounded growth would bloat the stored account row and, since every
// entry feeds spam.ResolveOwnAddresses on every classification, the
// per-message own-address lookup. Once the set holds
// maxLearnedAddresses entries, learnOwnAddresses stops admitting new
// ones; already-learned addresses are never evicted to make room.
const maxLearnedAddresses = 256

// extractDeliveredAddresses returns the lower-cased addresses found in
// msg's Delivered-To and X-Original-To headers (re #396, third round):
// the upstream mailbox's own record of every address it accepted this
// specific message at, which is exactly the fact own_addresses needs
// and principal identities/aliases cannot supply for an address the
// principal never registered as their own but the upstream mailbox
// nonetheless delivers to (a shared organisational alias).
func extractDeliveredAddresses(msg mailparse.Message) []string {
	var values []string
	values = append(values, msg.Headers.GetAll("Delivered-To")...)
	values = append(values, msg.Headers.GetAll("X-Original-To")...)
	return parseHeaderAddresses(values)
}

// parseHeaderAddresses parses each header value as an RFC 5322 address
// list, falling back to a bare "look for an @" heuristic for a value
// mail.ParseAddressList rejects -- Delivered-To is often written as a
// bare local@domain with no <angle brackets> and no display name, which
// some upstreams render with characters ParseAddressList is strict
// about. Returns lower-cased, trimmed addresses; never nil for a
// non-empty, all-unparseable input (the fallback still yields the raw
// trimmed value when it contains "@").
func parseHeaderAddresses(values []string) []string {
	var out []string
	for _, v := range values {
		if addrs, err := mail.ParseAddressList(v); err == nil {
			for _, a := range addrs {
				out = append(out, strings.ToLower(strings.TrimSpace(a.Address)))
			}
			continue
		}
		trimmed := strings.ToLower(strings.TrimSpace(v))
		if strings.Contains(trimmed, "@") {
			out = append(out, trimmed)
		}
	}
	return out
}

// learnOwnAddresses merges addrs into the account's persisted
// learned-address set, writing only when something changed or the
// account has never completed a learning pass (AddressesLearnedAt nil)
// -- the common case (an account's messages keep arriving at the same
// handful of addresses) therefore costs no store write after the first
// sighting of each address. w.opts.account is updated in place so a
// later call in the same worker session sees the merged result without
// a store round-trip; safe because accountWorker's ingest path runs on
// a single supervising goroutine (see the backfillRemaining field's own
// no-synchronisation note in worker.go).
func (w *accountWorker) learnOwnAddresses(ctx context.Context, addrs []string) error {
	have := make(map[string]struct{}, len(w.opts.account.LearnedAddresses))
	merged := append([]string(nil), w.opts.account.LearnedAddresses...)
	for _, a := range w.opts.account.LearnedAddresses {
		have[a] = struct{}{}
	}
	changed := false
	for _, a := range addrs {
		if _, ok := have[a]; ok {
			continue
		}
		if len(merged) >= maxLearnedAddresses {
			// Bound reached (REQ-IMAP-IMP-36): keep what is already
			// learned, admit no more this pass or any later one.
			continue
		}
		have[a] = struct{}{}
		merged = append(merged, a)
		changed = true
	}
	if !changed && w.opts.account.AddressesLearnedAt != nil {
		return nil
	}
	sort.Strings(merged)
	if err := w.opts.store.Meta().SetIMAPImportLearnedAddresses(ctx, w.opts.account.ID, merged); err != nil {
		return err
	}
	w.opts.account.LearnedAddresses = merged
	now := w.opts.clk.Now()
	w.opts.account.AddressesLearnedAt = &now
	return nil
}

// runOwnAddressBackfill performs the one-shot, whole-account-lifetime
// backfill of the account's learned own-address set from headers of
// messages already mirrored before this fix existed (re #396, third
// round, required outcome 1). Gated on the persisted AddressesLearnedAt
// column (nil = never run) rather than an in-process flag like
// seenBackfillDone: unlike that pass, this one reads one blob header per
// historical message, so it must not repeat on every worker restart,
// only once ever per account. Runs at the start of the worker's first
// session for this account (worker.go, alongside runSeenBackfill) --
// not as a schema migration (a migration has no blob-store/mailparse
// access) and not deferred to "whenever the next message happens to
// arrive" (an account whose upstream has gone quiet would then never
// learn its historical addresses at all, and info@/vorstand@ would stay
// missing indefinitely on exactly the account #396 reported). Best
// effort: a single message's read/parse failure is skipped, never
// aborts the pass.
func (w *accountWorker) runOwnAddressBackfill(ctx context.Context) {
	if w.opts.account.AddressesLearnedAt != nil {
		return
	}

	states, err := w.opts.store.Meta().ListIMAPImportMessageStatesByAccount(ctx, w.opts.account.ID)
	if err != nil {
		w.opts.log.Warn("imapimport: own-address backfill: list message states",
			"account_id", w.opts.account.ID, "error", err.Error())
		return
	}

	seen := make(map[store.MessageID]struct{}, len(states))
	var addrs []string
	for _, ms := range states {
		if ctx.Err() != nil {
			return
		}
		if ms.HeroldMessageID == 0 {
			continue
		}
		if _, ok := seen[ms.HeroldMessageID]; ok {
			continue
		}
		seen[ms.HeroldMessageID] = struct{}{}

		msg, err := w.opts.store.Meta().GetMessage(ctx, ms.HeroldMessageID)
		if err != nil {
			continue
		}
		found, err := w.loadDeliveredAddressesFromBlob(ctx, msg.Blob.Hash)
		if err != nil {
			continue
		}
		addrs = append(addrs, found...)
	}

	// Always call learnOwnAddresses, even with zero addresses found: a
	// completed-but-empty pass is what marks AddressesLearnedAt non-nil,
	// which is the "known complete" fact spam.Classifier's
	// decisive-signal resolution requires (see the file doc comment).
	if err := w.learnOwnAddresses(ctx, addrs); err != nil {
		w.opts.log.Warn("imapimport: own-address backfill: persist learned addresses",
			"account_id", w.opts.account.ID, "error", err.Error())
		return
	}
	if len(addrs) > 0 {
		w.opts.log.Info("imapimport: own-address backfill learned addresses",
			"account_id", w.opts.account.ID, "count", len(addrs))
	}
}

// loadDeliveredAddressesFromBlob reads at most ownAddressHeaderScanBytes
// of the blob identified by hash and extracts its Delivered-To/
// X-Original-To addresses. net/mail.ReadMessage stops at the header
// block's blank-line terminator and never reads the (here, truncated)
// body, so a message far larger than the scan bound still parses
// correctly as long as its headers -- always at the very start of an
// RFC822 blob -- fit within it.
func (w *accountWorker) loadDeliveredAddressesFromBlob(ctx context.Context, hash string) ([]string, error) {
	rc, err := w.opts.store.Blobs().Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	head, err := io.ReadAll(io.LimitReader(rc, ownAddressHeaderScanBytes))
	if err != nil {
		return nil, err
	}
	m, err := mail.ReadMessage(bytes.NewReader(head))
	if err != nil {
		return nil, err
	}
	// net/mail.Header is a plain map[string][]string with no multi-value
	// accessor of its own; textproto.MIMEHeader (the same underlying
	// representation, canonicalised keys) provides Values.
	hdr := textproto.MIMEHeader(m.Header)
	values := append([]string(nil), hdr.Values("Delivered-To")...)
	values = append(values, hdr.Values("X-Original-To")...)
	return parseHeaderAddresses(values), nil
}
