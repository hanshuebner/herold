package admin

// imap_import_spam.go — bridges the imapimport.SpamClassifier seam
// (REQ-FILT-02, issue #300) to the same spam.Classifier + plugin name
// instance the SMTP delivery path uses (internal/protosmtp), so a single
// classifier plugin backs both paths.
//
// Unlike the Categoriser adapter (imap_import_categoriser.go), this adapter
// does not re-fetch the blob: imapimport.SpamClassifier.Classify runs BEFORE
// InsertMessage, so the worker already holds the parsed message. Classify
// therefore just forwards to spam.Classifier.Classify with a nil AuthResults
// -- imported mail does not carry a fresh DKIM/SPF/DMARC verification the
// way SMTP delivery does (the worker does NOT re-verify the upstream's
// signatures, REQ-IMAP-IMP-33), so the request projection omits the
// auth-derived fields exactly as internal/categorise's adapter already does
// for LLM categorisation of imported mail.
//
// RecordVerdict persists the llm_classifications transparency row
// (REQ-FILT-66) once InsertMessage has assigned a message id, mirroring
// protosmtp's persistLLMRecord. It never rewrites the stored message bytes
// to add an Authentication-Results "x-herold-spam=<verdict>" stamp: unlike
// SMTP delivery, the import path stores the upstream bytes byte-identical
// (REQ-IMAP-IMP-32/33), so there is no rewrite point to stamp into.

import (
	"context"
	"log/slog"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/spam"
	"github.com/hanshuebner/herold/internal/store"
)

// imapImportSpamAdapter implements imapimport.SpamClassifier using a real
// spam.Classifier. Construct via newIMAPImportSpamAdapter.
type imapImportSpamAdapter struct {
	cls    *spam.Classifier
	plugin string
	st     store.Store
	clk    clock.Clock
	logger *slog.Logger
}

// newIMAPImportSpamAdapter returns an adapter backed by cls/plugin -- the
// same spam.Classifier instance and plugin name protosmtp.Config.Spam /
// SpamPluginName are constructed from in internal/admin/server.go. An empty
// plugin name degrades to spam.Unclassified on every call (the same
// behaviour protosmtp's classify() gets when no [[plugin]] of type "spam" is
// configured), so this adapter is always safe to wire even when spam
// classification is not configured.
func newIMAPImportSpamAdapter(cls *spam.Classifier, plugin string, st store.Store, clk clock.Clock, logger *slog.Logger) *imapImportSpamAdapter {
	return &imapImportSpamAdapter{cls: cls, plugin: plugin, st: st, clk: clk, logger: logger}
}

// Classify implements imapimport.SpamClassifier. A nil classifier or a
// plugin timeout/error both collapse to spam.Classification{Verdict:
// spam.Unclassified}, matching protosmtp's classify() helper -- the import
// worker's caller treats Unclassified exactly like Ham (stays in INBOX).
func (a *imapImportSpamAdapter) Classify(ctx context.Context, msg mailparse.Message) spam.Classification {
	if a.cls == nil {
		return spam.Classification{Verdict: spam.Unclassified, Score: -1}
	}
	cls, err := a.cls.Classify(ctx, msg, nil /* authResults: REQ-IMAP-IMP-33, no re-verification on import */, a.plugin)
	if err != nil {
		// spam.Classifier.Classify already logs a warn with the plugin name
		// and error before returning; logging again here would duplicate
		// the line at a lower level for no benefit.
		return spam.Classification{Verdict: spam.Unclassified, Score: -1}
	}
	return cls
}

// RecordVerdict implements imapimport.SpamClassifier. A no-op when
// classification.Verdict is spam.Unclassified (REQ-FILT-66 records
// verdicts a classifier actually reached, not "did not run"). Persist
// failures are logged at warn and otherwise swallowed -- the import must
// never fail because the transparency record could not be written.
func (a *imapImportSpamAdapter) RecordVerdict(ctx context.Context, principalID store.PrincipalID, messageID store.MessageID, msg mailparse.Message, classification spam.Classification) {
	if classification.Verdict == spam.Unclassified || messageID == 0 {
		return
	}
	v := classification.Verdict.String()
	score := classification.Score
	rec := store.LLMClassificationRecord{
		MessageID:      messageID,
		PrincipalID:    principalID,
		SpamVerdict:    &v,
		SpamConfidence: &score,
	}
	if raw := classification.RawResponse; raw != nil {
		if reason, ok := raw["reason"].(string); ok && reason != "" {
			rec.SpamReason = &reason
		}
		if mdl, ok := raw["model"].(string); ok && mdl != "" {
			rec.SpamModel = &mdl
		}
	}
	// Build the user-visible prompt-as-applied the same way protosmtp's
	// persistLLMRecord does: the structured spam.Request context sent to
	// the plugin, not the plugin's system prompt.
	req := spam.BuildRequest(msg, nil)
	if b, jerr := req.Canonical(); jerr == nil {
		s := string(b)
		rec.SpamPromptApplied = &s
	}
	t := a.clk.Now()
	rec.SpamClassifiedAt = &t

	if err := a.st.Meta().SetLLMClassification(ctx, rec); err != nil {
		a.logger.WarnContext(ctx, "imap-import spam: persist classification record",
			slog.Uint64("message_id", uint64(messageID)),
			slog.String("err", err.Error()),
		)
	}
}
