package protoadmin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanshuebner/herold/internal/store"
)

// spamFeedbackRequest is the body shape for POST /api/v1/spam-feedback.
//
// EmailID is the numeric message id (matching JMAP Email.id). Kind is
// "spam", "phishing", or "ham": spam/phishing record a user correcting
// a message INTO Junk (phishing additionally signals operator-side
// escalation when configured); ham records a user correcting a message
// OUT of Junk (the Suite's "Not spam" action, issue #382).
type spamFeedbackRequest struct {
	EmailID string `json:"emailId"`
	Kind    string `json:"kind"`
}

// handleSpamFeedback implements POST /api/v1/spam-feedback (Wave 3.15,
// extended for the "ham" kind in issue #382).
//
// The Suite SPA calls this endpoint when a user reports a message as
// spam/phishing (per-message context menu, REQ-MAIL-135, REQ-MAIL-136)
// or marks a Junk message as not spam ("Not spam" action). This is the
// REQ-FILT-70 feedback record: timestamp, verdict given (the
// classifier's recorded verdict for the message, "unclassified" when
// none was ever recorded), corrected verdict (the user's correction:
// "spam" for kind spam/phishing, "ham" for kind ham), and headers
// (email_id, from which an operator can look up the message). It is
// recorded in the audit log — REQ-FILT-71's operator surface, readable
// via `herold audit list --action mail.spam.feedback` or
// `GET /api/v1/audit?action=mail.spam.feedback` — so the operator can
// export the corpus for tuning the spam classifier. The suite's own
// Email/set + mailboxIds patch handles the actual mailbox move and
// keyword change; this endpoint only records the correction signal.
//
// Auth: user-scope session cookie (mounted from the public listener via
// RegisterSelfServiceRoutes). The reporting principal MUST own the
// referenced email; the handler rejects cross-account reports with 403.
func (s *Server) handleSpamFeedback(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	if caller.ID == 0 {
		writeProblem(w, r, http.StatusUnauthorized, "auth/unauthenticated",
			"authentication is required", "")
		return
	}
	var req spamFeedbackRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.EmailID == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"emailId is required", "")
		return
	}
	var correctedVerdict string
	switch req.Kind {
	case "spam", "phishing":
		correctedVerdict = "spam"
	case "ham":
		correctedVerdict = "ham"
	default:
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"kind must be 'spam', 'phishing', or 'ham'", "")
		return
	}
	rawID, err := strconv.ParseUint(req.EmailID, 10, 64)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"emailId must be a numeric message id", "")
		return
	}
	msg, err := s.store.Meta().GetMessage(r.Context(), store.MessageID(rawID))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, r, http.StatusNotFound, "message_not_found",
				"the referenced email is not visible to the caller", "")
			return
		}
		writeProblem(w, r, http.StatusInternalServerError, "internal-error",
			"could not read message", "")
		return
	}
	if msg.PrincipalID != caller.ID {
		writeProblem(w, r, http.StatusForbidden, "forbidden",
			"the referenced email belongs to a different principal", "")
		return
	}
	verdictGiven := "unclassified"
	if rec, err := s.store.Meta().GetLLMClassification(r.Context(), store.MessageID(rawID)); err == nil {
		if rec.SpamVerdict != nil && *rec.SpamVerdict != "" {
			verdictGiven = *rec.SpamVerdict
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		writeProblem(w, r, http.StatusInternalServerError, "internal-error",
			"could not read the classification record", "")
		return
	}
	s.appendAudit(r.Context(), "mail.spam.feedback",
		fmt.Sprintf("message:%d", rawID),
		store.OutcomeSuccess, "",
		map[string]string{
			"kind":              req.Kind,
			"email_id":          req.EmailID,
			"principal_id":      strconv.FormatUint(uint64(caller.ID), 10),
			"verdict_given":     verdictGiven,
			"corrected_verdict": correctedVerdict,
		})
	w.WriteHeader(http.StatusNoContent)
}
