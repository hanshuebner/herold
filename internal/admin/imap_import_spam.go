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
	"fmt"
	"log/slog"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
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
//
// The category context (REQ-FILT-210) is built from principalID's own
// CategorisationConfig, mirroring protosmtp's buildClassifyContext
// (internal/protosmtp/deliver.go) field-for-field; the structural
// fallback (ADR-0002) and the spam-verdict category drop (ADR-0004) are
// applied here too, so the returned Classification.Category is already
// the fully-resolved value the import worker keywords the message with
// (resolveImportSpamTarget).
func (a *imapImportSpamAdapter) Classify(ctx context.Context, principalID store.PrincipalID, msg mailparse.Message) spam.Classification {
	clsCtx, categorisationEnabled := a.buildClassifyContext(ctx, principalID)
	// cls starts as the "no plugin verdict" default and is overwritten by
	// a successful classifier call below. A nil Classifier, a Classify
	// error (no plugin configured, timeout, crash), and a real verdict
	// all fall through to the same switch: REQ-FILT-214/ADR-0002 requires
	// the structural fallback categoriser to run whenever the plugin's
	// category is empty OR no classifier plugin is installed, mirroring
	// protosmtp.classifyMessage.
	cls := spam.Classification{Verdict: spam.Unclassified, Score: -1}
	var attempted bool
	var clsErr error
	var elapsed time.Duration
	if a.cls != nil && a.plugin != "" {
		attempted = true
		start := time.Now()
		// authResults: REQ-IMAP-IMP-33, no re-verification on import.
		cls, clsErr = a.cls.Classify(ctx, msg, nil, a.plugin, clsCtx)
		elapsed = time.Since(start)
		if clsErr != nil {
			// spam.Classifier.Classify already logs a warn with the plugin
			// name and error before returning; logging again here would
			// duplicate the line at a lower level for no benefit. Reason
			// survives from cls.Reason (re #326) for the INFO summary
			// line below and the persisted record.
			cls = spam.Classification{Verdict: spam.Unclassified, Score: -1, Reason: cls.Reason}
		}
	} else {
		clsErr = spam.ErrNotConfigured
	}
	a.logClassifyOutcome(ctx, principalID, msg, cls, attempted, clsErr, elapsed)
	switch {
	case cls.Verdict == spam.Spam:
		cls.Category = "" // ADR-0004
	case !categorisationEnabled:
		cls.Category = ""
	case cls.Category == "":
		cls.Category = spam.StructuralCategory(msg) // ADR-0002
	}
	return cls
}

// logClassifyOutcome emits the one INFO-level "spam classification
// outcome" line per classified message the ticket requires (re #326),
// mirroring internal/protosmtp/deliver.go's logClassifyOutcome: visible
// at production log level regardless of whether the classifier ran,
// succeeded, or was never configured. The import worker calls Classify
// before InsertMessage, so no store-assigned MessageID exists yet; the
// Message-ID header is logged instead, the same key RecordVerdict's
// caller (internal/imapimport) uses to assign the row afterward.
func (a *imapImportSpamAdapter) logClassifyOutcome(ctx context.Context, principalID store.PrincipalID, msg mailparse.Message, cls spam.Classification, attempted bool, clsErr error, elapsed time.Duration) {
	attrs := []any{
		"activity", observe.ActivitySystem,
		"subsystem", "spam",
		"msg_id_header", msg.Envelope.MessageID,
		"principal_id", uint64(principalID),
		"verdict", cls.Verdict.String(),
		"score", cls.Score,
		"category", cls.Category,
		"attempted", attempted,
		"elapsed_ms", elapsed.Milliseconds(),
	}
	if cls.Verdict == spam.Unclassified {
		attrs = append(attrs, "reason_class", spam.ReasonClass(clsErr))
		if clsErr != nil {
			attrs = append(attrs, "err", clsErr.Error())
		}
	}
	a.logger.InfoContext(ctx, "spam classification outcome", attrs...)
}

// buildClassifyContext loads principalID's CategorisationConfig
// (REQ-FILT-211) and translates it into the classifier's ClassifyContext
// (REQ-FILT-210). The second return value is false when categorisation
// is disabled for this principal or the config could not be loaded.
// RecipientDomain is left empty: the IMAP import path has no SMTP
// envelope recipient to derive one from.
func (a *imapImportSpamAdapter) buildClassifyContext(ctx context.Context, principalID store.PrincipalID) (spam.ClassifyContext, bool) {
	base := spam.ClassifyContext{Principal: fmt.Sprint(principalID)}
	cfg, err := a.st.Meta().GetCategorisationConfig(ctx, principalID)
	if err != nil {
		a.logger.WarnContext(ctx, "imap-import spam: load categorisation config",
			slog.Uint64("principal_id", uint64(principalID)),
			slog.String("err", err.Error()))
		return base, false
	}
	if !cfg.Enabled {
		return base, false
	}
	prompt := cfg.Prompt
	if cfg.Guardrail != "" {
		prompt = cfg.Guardrail + "\n\n" + prompt
	}
	base.Prompt = prompt
	if len(cfg.CategorySet) > 0 {
		base.Categories = make([]spam.CategoryOption, len(cfg.CategorySet))
		for i, c := range cfg.CategorySet {
			base.Categories[i] = spam.CategoryOption{Name: c.Name, Description: c.Description}
		}
	}
	return base, true
}

// RecordVerdict implements imapimport.SpamClassifier. A no-op when
// classification is a Verdict of Unclassified with no Reason: that is
// the "no plugin configured, no attempt made" case (re #326), which
// stays silent per message rather than growing the table on every
// import for an account with no classifier configured. An Unclassified
// verdict WITH a Reason means an attempt was made and failed (timeout /
// plugin error / unparseable output) and IS recorded, verdict
// "unclassified", so `spam reclassify --unclassified-only` and the
// message-research surface can see it (REQ-FILT-66 transparency).
// Persist failures are logged at warn and otherwise swallowed -- the
// import must never fail because the transparency record could not be
// written.
func (a *imapImportSpamAdapter) RecordVerdict(ctx context.Context, principalID store.PrincipalID, messageID store.MessageID, msg mailparse.Message, classification spam.Classification) {
	if (classification.Verdict == spam.Unclassified && classification.Reason == "") || messageID == 0 {
		return
	}
	v := classification.Verdict.String()
	rec := store.LLMClassificationRecord{
		MessageID:   messageID,
		PrincipalID: principalID,
		SpamVerdict: &v,
	}
	// SpamConfidence is left nil for an Unclassified outcome (re #326):
	// no score was ever produced, so -1 (the in-memory
	// Classification.Score sentinel for "no score") is not a
	// confidence value worth persisting or rendering.
	if classification.Verdict != spam.Unclassified {
		score := classification.Score
		rec.SpamConfidence = &score
	}
	if classification.Reason != "" {
		reason := classification.Reason
		rec.SpamReason = &reason
	} else if raw := classification.RawResponse; raw != nil {
		if reason, ok := raw["reason"].(string); ok && reason != "" {
			rec.SpamReason = &reason
		}
	}
	if raw := classification.RawResponse; raw != nil {
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
