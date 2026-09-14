// spam_feedback_test.go exercises POST /api/v1/spam-feedback
// (spam_feedback.go), the REQ-FILT-70 feedback-record endpoint the
// Suite calls both when a user reports a message as spam/phishing and,
// since issue #382, when a user marks a Junk message "Not spam" (kind
// "ham"). Runs on both SQLite and (when HEROLD_PG_DSN is set) Postgres
// via openSubmissionBackends, matching the dual-backend pattern used by
// the mailing-list and identity-submission tests in this package.
//
// Coverage:
//   - kind "ham" is accepted (previously only "spam"/"phishing" were).
//   - the resulting audit entry records verdict_given (the message's
//     recorded classifier verdict, "unclassified" when none was ever
//     recorded) and corrected_verdict ("ham" for kind ham, "spam" for
//     kind spam/phishing) alongside the existing kind/email_id fields.
//   - an unknown kind is still rejected with 400.
//   - a caller reporting on a message they do not own is rejected
//     with 403 and produces no audit entry.
package protoadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// insertSpamFeedbackTestMessage inserts a bare message into pid's INBOX
// and, when verdict is non-empty, an LLMClassificationRecord recording
// verdict as the classifier's spam verdict. Returns the store message id
// as it appears on the wire (decimal string, matching JMAP Email.id).
func insertSpamFeedbackTestMessage(t *testing.T, h *harness, pid store.PrincipalID, verdict string) string {
	t.Helper()
	ctx := context.Background()
	mboxes, err := h.h.Store.Meta().ListMailboxes(ctx, pid)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	var inboxID store.MailboxID
	for _, m := range mboxes {
		if m.Name == "INBOX" {
			inboxID = m.ID
			break
		}
	}
	if inboxID == 0 {
		t.Fatalf("INBOX not found for principal %d", pid)
	}
	ref, err := h.h.Store.Blobs().Put(ctx, strings.NewReader("From: notify@example.test\r\n\r\nbody"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	now := h.clk.Now().UTC()
	uid, _, err := h.h.Store.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  pid,
		Blob:         ref,
		Size:         ref.Size,
		InternalDate: now,
		ReceivedAt:   now,
	}, []store.MessageMailbox{{MailboxID: inboxID}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// InsertMessage's first return value is the mailbox-scoped UID, not
	// the message id; re-read the mailbox listing to learn the
	// assigned MessageID.
	msgs, err := h.h.Store.Meta().ListMessages(ctx, inboxID, store.MessageFilter{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var id store.MessageID
	for _, m := range msgs {
		if m.UID == uid {
			id = m.ID
			break
		}
	}
	if id == 0 {
		t.Fatalf("could not resolve inserted message's id (uid=%d)", uid)
	}
	if verdict != "" {
		if err := h.h.Store.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
			MessageID:        id,
			PrincipalID:      pid,
			SpamVerdict:      &verdict,
			SpamClassifiedAt: &now,
		}); err != nil {
			t.Fatalf("SetLLMClassification: %v", err)
		}
	}
	return strconv.FormatUint(uint64(id), 10)
}

// lastSpamFeedbackAuditEntry returns the most recent mail.spam.feedback
// audit entry for subject "message:<emailID>", failing the test if none
// is found.
func lastSpamFeedbackAuditEntry(t *testing.T, h *harness, emailID string) store.AuditLogEntry {
	t.Helper()
	entries, err := h.h.Store.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "mail.spam.feedback",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	subject := "message:" + emailID
	for _, e := range entries {
		if e.Subject == subject {
			return e
		}
	}
	t.Fatalf("no mail.spam.feedback audit entry for %s (have %d entries)", subject, len(entries))
	return store.AuditLogEntry{}
}

// TestSpamFeedback_HamKind_RecordsVerdictGivenAndCorrectedVerdict pins
// REQ-FILT-70's "verdict given / corrected verdict" feedback record for
// the "Not spam" flow (issue #382): a message the classifier had marked
// spam, corrected by the user to ham, produces an audit entry with
// verdict_given=spam and corrected_verdict=ham.
func TestSpamFeedback_HamKind_RecordsVerdictGivenAndCorrectedVerdict(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			h := newHarnessWithStore(t, be.fs, be.clk)
			pid, key := h.bootstrap("ham-feedback@example.com")

			emailID := insertSpamFeedbackTestMessage(t, h, store.PrincipalID(pid), "spam")

			res, buf := h.doRequest("POST", "/api/v1/spam-feedback", key, map[string]any{
				"emailId": emailID,
				"kind":    "ham",
			})
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("spam-feedback ham: status=%d body=%s", res.StatusCode, buf)
			}

			entry := lastSpamFeedbackAuditEntry(t, h, emailID)
			if got := entry.Metadata["kind"]; got != "ham" {
				t.Errorf("kind = %q, want %q", got, "ham")
			}
			if got := entry.Metadata["verdict_given"]; got != "spam" {
				t.Errorf("verdict_given = %q, want %q", got, "spam")
			}
			if got := entry.Metadata["corrected_verdict"]; got != "ham" {
				t.Errorf("corrected_verdict = %q, want %q", got, "ham")
			}
			if got := entry.Metadata["email_id"]; got != emailID {
				t.Errorf("email_id = %q, want %q", got, emailID)
			}
		})
	}
}

// TestSpamFeedback_HamKind_NoClassificationRecord verifies verdict_given
// falls back to "unclassified" when the message was delivered with no
// spam classifier run at all (REQ-FILT-45's degrade-open case), rather
// than being left empty or crashing.
func TestSpamFeedback_HamKind_NoClassificationRecord(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			h := newHarnessWithStore(t, be.fs, be.clk)
			pid, key := h.bootstrap("ham-unclassified@example.com")

			emailID := insertSpamFeedbackTestMessage(t, h, store.PrincipalID(pid), "")

			res, buf := h.doRequest("POST", "/api/v1/spam-feedback", key, map[string]any{
				"emailId": emailID,
				"kind":    "ham",
			})
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("spam-feedback ham: status=%d body=%s", res.StatusCode, buf)
			}

			entry := lastSpamFeedbackAuditEntry(t, h, emailID)
			if got := entry.Metadata["verdict_given"]; got != "unclassified" {
				t.Errorf("verdict_given = %q, want %q", got, "unclassified")
			}
		})
	}
}

// TestSpamFeedback_SpamKind_RecordsCorrectedVerdictSpam pins the
// existing spam-report path's corrected_verdict now that the field is
// populated for every kind, not only ham.
func TestSpamFeedback_SpamKind_RecordsCorrectedVerdictSpam(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			h := newHarnessWithStore(t, be.fs, be.clk)
			pid, key := h.bootstrap("spam-feedback@example.com")

			emailID := insertSpamFeedbackTestMessage(t, h, store.PrincipalID(pid), "ham")

			res, buf := h.doRequest("POST", "/api/v1/spam-feedback", key, map[string]any{
				"emailId": emailID,
				"kind":    "spam",
			})
			if res.StatusCode != http.StatusNoContent {
				t.Fatalf("spam-feedback spam: status=%d body=%s", res.StatusCode, buf)
			}

			entry := lastSpamFeedbackAuditEntry(t, h, emailID)
			if got := entry.Metadata["verdict_given"]; got != "ham" {
				t.Errorf("verdict_given = %q, want %q", got, "ham")
			}
			if got := entry.Metadata["corrected_verdict"]; got != "spam" {
				t.Errorf("corrected_verdict = %q, want %q", got, "spam")
			}
		})
	}
}

// TestSpamFeedback_UnknownKind_Rejected verifies the validation error
// message still names all three accepted kinds.
func TestSpamFeedback_UnknownKind_Rejected(t *testing.T) {
	be := openSubmissionBackends(t)[0]
	h := newHarnessWithStore(t, be.fs, be.clk)
	pid, key := h.bootstrap("bad-kind@example.com")
	emailID := insertSpamFeedbackTestMessage(t, h, store.PrincipalID(pid), "")

	res, buf := h.doRequest("POST", "/api/v1/spam-feedback", key, map[string]any{
		"emailId": emailID,
		"kind":    "bogus",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("spam-feedback bogus kind: status=%d body=%s", res.StatusCode, buf)
	}
	var problem struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(buf, &problem); err != nil {
		t.Fatalf("unmarshal problem: %v: %s", err, buf)
	}
	if !strings.Contains(problem.Title, "ham") {
		t.Errorf("error message %q should mention the ham kind", problem.Title)
	}
}

// TestSpamFeedback_CrossPrincipal_Forbidden verifies a caller cannot post
// feedback for a message owned by a different principal, and that no
// audit entry is written for the attempt.
func TestSpamFeedback_CrossPrincipal_Forbidden(t *testing.T) {
	be := openSubmissionBackends(t)[0]
	h := newHarnessWithStore(t, be.fs, be.clk)
	adminPID, adminKey := h.bootstrap("cross-admin@example.com")
	_ = adminPID

	ownerPID := h.createPrincipal(adminKey, "owner@example.com")
	emailID := insertSpamFeedbackTestMessage(t, h, store.PrincipalID(ownerPID), "spam")

	attackerPID := h.createPrincipal(adminKey, "attacker@example.com")
	_, attackerKey := h.createAPIKey(adminKey, attackerPID)

	res, buf := h.doRequest("POST", "/api/v1/spam-feedback", attackerKey, map[string]any{
		"emailId": emailID,
		"kind":    "ham",
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("spam-feedback cross-principal: status=%d body=%s", res.StatusCode, buf)
	}

	entries, err := h.h.Store.Meta().ListAuditLog(context.Background(), store.AuditLogFilter{
		Action: "mail.spam.feedback",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	subject := "message:" + emailID
	for _, e := range entries {
		if e.Subject == subject {
			t.Fatalf("unexpected audit entry for a forbidden cross-principal report: %+v", e)
		}
	}
}
