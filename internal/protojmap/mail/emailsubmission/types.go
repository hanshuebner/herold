package emailsubmission

import (
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// jmapID is the wire form of a JMAP id.
type jmapID = string

// jmapAddress is the JMAP "Address" object inside Envelope (RFC 8621 §5.1).
type jmapAddress struct {
	Email      string         `json:"email"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// jmapEnvelope is the SMTP envelope override (RFC 8621 §5.1).
type jmapEnvelope struct {
	MailFrom jmapAddress   `json:"mailFrom"`
	RcptTo   []jmapAddress `json:"rcptTo"`
}

// undoStatus maps the queue lifecycle to the JMAP undoStatus vocabulary
// (RFC 8621 §5.1.1: pending / final / canceled).
type undoStatus string

const (
	undoStatusPending  undoStatus = "pending"
	undoStatusFinal    undoStatus = "final"
	undoStatusCanceled undoStatus = "canceled"
)

// jmapDeliveryStatus is the per-recipient delivery summary (RFC 8621
// §5.1.1 deliveryStatus).
type jmapDeliveryStatus struct {
	SMTPReply string `json:"smtpReply"`
	Delivered string `json:"delivered"` // queued / yes / no / unknown
	Displayed string `json:"displayed"` // unknown / yes
}

// jmapEmailSubmission is the wire-form EmailSubmission object.
type jmapEmailSubmission struct {
	ID             jmapID                        `json:"id"`
	IdentityID     jmapID                        `json:"identityId"`
	EmailID        jmapID                        `json:"emailId"`
	ThreadID       jmapID                        `json:"threadId,omitempty"`
	Envelope       *jmapEnvelope                 `json:"envelope,omitempty"`
	SendAt         string                        `json:"sendAt"`
	UndoStatus     undoStatus                    `json:"undoStatus"`
	DeliveryStatus map[string]jmapDeliveryStatus `json:"deliveryStatus,omitempty"`
	DSNBlobIDs     []jmapID                      `json:"dsnBlobIds"`
	MDNBlobIDs     []jmapID                      `json:"mdnBlobIds"`
}

// rowToJMAP folds a list of QueueItems sharing one EnvelopeID into a
// single jmapEmailSubmission. The SendAt is the earliest CreatedAt;
// undoStatus is "final" when every row reached a terminal state and
// "pending" otherwise. deliveryStatus reflects the per-recipient
// QueueState.
func rowToJMAP(rows []store.QueueItem, identityID jmapID, emailID jmapID, threadID jmapID) jmapEmailSubmission {
	if len(rows) == 0 {
		return jmapEmailSubmission{}
	}
	sub := jmapEmailSubmission{
		ID:             renderSubmissionID(rows[0].EnvelopeID),
		IdentityID:     identityID,
		EmailID:        emailID,
		ThreadID:       threadID,
		SendAt:         rows[0].CreatedAt.UTC().Format(time.RFC3339),
		DeliveryStatus: map[string]jmapDeliveryStatus{},
		DSNBlobIDs:     []jmapID{},
		MDNBlobIDs:     []jmapID{},
	}
	envelope := jmapEnvelope{
		MailFrom: jmapAddress{Email: rows[0].MailFrom},
	}
	allFinal := true
	anyDeferred := false
	for _, r := range rows {
		if r.CreatedAt.Before(rows[0].CreatedAt) {
			sub.SendAt = r.CreatedAt.UTC().Format(time.RFC3339)
		}
		envelope.RcptTo = append(envelope.RcptTo, jmapAddress{Email: r.RcptTo})
		ds := jmapDeliveryStatus{Displayed: "unknown"}
		switch r.State {
		case store.QueueStateDone:
			ds.Delivered = "yes"
			ds.SMTPReply = "250 ok"
		case store.QueueStateFailed:
			ds.Delivered = "no"
			ds.SMTPReply = r.LastError
			if ds.SMTPReply == "" {
				ds.SMTPReply = "550 failed"
			}
		case store.QueueStateHeld:
			ds.Delivered = "queued"
			ds.SMTPReply = "held"
			allFinal = false
		case store.QueueStateInflight, store.QueueStateQueued:
			ds.Delivered = "queued"
			ds.SMTPReply = "queued"
			allFinal = false
		case store.QueueStateDeferred:
			ds.Delivered = "queued"
			ds.SMTPReply = r.LastError
			if ds.SMTPReply == "" {
				ds.SMTPReply = "deferred"
			}
			allFinal = false
			anyDeferred = true
		default:
			ds.Delivered = "unknown"
			ds.SMTPReply = "unknown"
			allFinal = false
		}
		sub.DeliveryStatus[r.RcptTo] = ds
	}
	sub.Envelope = &envelope
	switch {
	case allFinal:
		sub.UndoStatus = undoStatusFinal
	case anyDeferred:
		sub.UndoStatus = undoStatusPending
	default:
		sub.UndoStatus = undoStatusPending
	}
	return sub
}

// renderSubmissionID stringifies an EnvelopeID for the JMAP wire.
// EnvelopeIDs are already opaque hex strings; we surface them
// verbatim.
func renderSubmissionID(env store.EnvelopeID) jmapID {
	if env == "" {
		return ""
	}
	return string(env)
}

// parseSubmissionID inverts renderSubmissionID.
func parseSubmissionID(id jmapID) (store.EnvelopeID, bool) {
	if id == "" {
		return "", false
	}
	return store.EnvelopeID(id), true
}

// renderEmailID stringifies a MessageID.
func renderEmailID(id store.MessageID) jmapID {
	return strconv.FormatUint(uint64(id), 10)
}

// parseEmailID inverts renderEmailID.
func parseEmailID(id jmapID) (store.MessageID, bool) {
	if id == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return store.MessageID(v), true
}
