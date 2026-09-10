package imapimport

// spam.go implements the merged spam+category classification seam for
// newly imported messages that map to INBOX (REQ-FILT-02/13, issues #300
// and #304). It mirrors the SMTP delivery path's verdict routing
// (internal/protosmtp/deliver.go resolveSieveTargets / classifyMessage):
// spam files into the principal's Junk mailbox instead of INBOX with no
// category (ADR-0004), suspect stays in INBOX with the "$Junk" keyword,
// and ham/unclassified stays in INBOX; whichever of those applies, a
// non-empty Classification.Category earns a "$category-<name>" keyword.
// One Classify call answers both questions (Wave 4.3): there is no
// second, post-insert categorisation call on this path. Sieve itself is
// never run against imported mail (REQ-IMAP-IMP-31 decision 1), so this
// is the only routing decision the import worker makes off the
// classifier's result.
//
// Classification runs only for messages that are:
//   - newly inserted, not a dedup hit -- a message already known to herold
//     was classified, if at all, the first time it was mirrored or
//     delivered, so re-classifying a dedup hit would both duplicate the
//     llm_classifications row and could reroute an already-placed message;
//   - mapped to INBOX by the folder mapping -- a message the source already
//     filed in its own Junk-attributed folder is never routed through
//     INBOX and so is never classified (REQ-IMAP-IMP-31: "not already
//     filed in the account's own Junk folder by the source server"); and
//   - a genuine live arrival, not part of the historical backfill,
//     mirroring the LLM categoriser gate in sync.go (REQ-IMAP-IMP-31 / D1:
//     "re-running spam/webhooks on a historical backfill is redundant and
//     dangerous" applies equally to spam classification).
//
// The stored message bytes are never rewritten to add an
// Authentication-Results "x-herold-spam=<verdict>" stamp the way SMTP
// delivery does: REQ-IMAP-IMP-32/33 require the mirrored bytes to stay
// byte-identical to the upstream so the upstream's own DKIM signatures and
// Authentication-Results remain independently verifiable, and protosmtp's
// stamping helpers (buildHeaderPrefix / renderAuthResults) work by
// rewriting the blob that is about to be stored -- there is no byte-
// preserving way to reuse them here. The verdict is instead recorded via
// RecordVerdict (llm_classifications, REQ-FILT-66), the same transparency
// record the SMTP path writes, just without the header stamp.

import (
	"context"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// SpamClassifier is the seam for optional spam classification of newly
// imported messages that map to INBOX. Unlike Categoriser, this seam runs
// BEFORE InsertMessage: the worker already holds the parsed message, so no
// blob re-fetch is needed.
//
// Implementations must never block or fail an import: a plugin timeout or
// error must be swallowed internally and reported as
// spam.Classification{Verdict: spam.Unclassified}, exactly like
// protosmtp's classify() helper.
type SpamClassifier interface {
	// Classify runs the configured classifier plugin against msg using
	// the same request projection as SMTP delivery (spam.BuildRequest),
	// plus the owning principal's category context (REQ-FILT-210): the
	// returned Classification.Category is already fully resolved
	// (validated against the principal's stored set, structural fallback
	// applied, dropped for a spam verdict) -- see
	// internal/admin/imap_import_spam.go, which builds the same
	// spam.ClassifyContext protosmtp.classifyMessage does.
	Classify(ctx context.Context, principalID store.PrincipalID, msg mailparse.Message) spam.Classification

	// RecordVerdict persists the classification for principalID/messageID
	// (REQ-FILT-66), once the message id is known post-insert. A no-op
	// when classification.Verdict is spam.Unclassified. Never returns an
	// error; implementations log failures themselves and proceed.
	RecordVerdict(ctx context.Context, principalID store.PrincipalID, messageID store.MessageID, msg mailparse.Message, classification spam.Classification)
}

// noopSpamClassifier is a SpamClassifier that never classifies. It is the
// Pool default when no spam plugin is configured, keeping accountWorker
// logic free of nil checks against opts.spamClassifier -- ingestMessage
// still guards with a nil check for the tests that construct
// accountWorkerOpts directly without going through Pool.
type noopSpamClassifier struct{}

func (noopSpamClassifier) Classify(context.Context, store.PrincipalID, mailparse.Message) spam.Classification {
	return spam.Classification{Verdict: spam.Unclassified, Score: -1}
}

func (noopSpamClassifier) RecordVerdict(context.Context, store.PrincipalID, store.MessageID, mailparse.Message, spam.Classification) {
}

// importSpamTarget is the routing decision resolveImportSpamTarget returns.
type importSpamTarget struct {
	// mailbox is the herold mailbox name to insert into instead of the
	// folder-mapped "INBOX" target.
	mailbox string
	// keywords is appended to the inserted message's keyword set.
	keywords []string
}

// resolveImportSpamTarget maps a classification to the import worker's
// routing decision (REQ-FILT-02/13) -- the same mapping SMTP delivery's
// resolveSieveTargets/classifyMessage apply. classification.Category is
// expected to already be fully resolved (plugin category validated
// against the principal's set, structural fallback applied, dropped for
// a spam verdict) by the caller -- see
// internal/admin/imap_import_spam.go's Classify adapter, which builds
// the same spam.ClassifyContext protosmtp.classifyMessage does.
func resolveImportSpamTarget(classification spam.Classification) importSpamTarget {
	switch classification.Verdict {
	case spam.Spam:
		// ADR-0004: \Junk is exempt from categorisation even when the
		// plugin returned one -- classification.Category is expected to
		// already be "" here, but the routing decision does not rely on
		// that invariant holding upstream.
		return importSpamTarget{mailbox: "Junk"}
	case spam.Suspect:
		return importSpamTarget{mailbox: "INBOX", keywords: categoryKeywords(classification.Category, "$Junk")}
	default:
		return importSpamTarget{mailbox: "INBOX", keywords: categoryKeywords(classification.Category)}
	}
}

// categoryKeywords appends "$category-<name>" (when category is
// non-empty) to extra, returning nil rather than an empty slice when
// there is nothing to add.
func categoryKeywords(category string, extra ...string) []string {
	kw := append([]string(nil), extra...)
	if category != "" {
		kw = append(kw, "$category-"+category)
	}
	if len(kw) == 0 {
		return nil
	}
	return kw
}
