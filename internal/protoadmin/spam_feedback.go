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
// either "spam" or "phishing"; phishing is a stricter variant that also
// signals operator-side escalation when configured.
type spamFeedbackRequest struct {
	EmailID string `json:"emailId"`
	Kind    string `json:"kind"`
}

// handleSpamFeedback implements POST /api/v1/spam-feedback (Wave 3.15).
//
// The Suite SPA calls this endpoint when a user reports a message as
// spam or phishing through the per-message context menu (REQ-MAIL-135,
// REQ-MAIL-136). The signal is recorded in the audit log so the
// operator can surface it for tuning the spam classifier; the suite's
// own Email/set + mailboxIds patch handles moving the message to Spam
// and applying the $junk / $phishing keyword.
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
	switch req.Kind {
	case "spam", "phishing":
	default:
		writeProblem(w, r, http.StatusBadRequest, "validation_failed",
			"kind must be 'spam' or 'phishing'", "")
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
	s.appendAudit(r.Context(), "mail.spam.feedback",
		fmt.Sprintf("message:%d", rawID),
		store.OutcomeSuccess, "",
		map[string]string{
			"kind":         req.Kind,
			"email_id":     req.EmailID,
			"principal_id": strconv.FormatUint(uint64(caller.ID), 10),
		})
	w.WriteHeader(http.StatusNoContent)
}
