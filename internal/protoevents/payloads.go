package protoevents

import "time"

// MailReceivedPayload is the body of an EventMailReceived event. Carries
// the routing keys a consumer typically needs (msg id, recipients,
// sender, classification verdict, byte size). Bodies are referenced by
// id; the dispatcher does not embed message contents (REQ-EVT-03).
type MailReceivedPayload struct {
	MessageID  string   `json:"message_id"`
	Sender     string   `json:"sender"`
	Recipient  string   `json:"rcpt"`
	Recipients []string `json:"rcpts,omitempty"`
	Verdict    string   `json:"verdict,omitempty"`
	Size       int64    `json:"size,omitempty"`
	Domain     string   `json:"domain,omitempty"`
}

// MailSentPayload accompanies EventMailSent (queue insert acknowledgement).
type MailSentPayload struct {
	QueueID   string `json:"queue_id"`
	Sender    string `json:"sender"`
	Recipient string `json:"rcpt"`
	Via       string `json:"via,omitempty"` // "smtp" / "api"
	Domain    string `json:"domain,omitempty"`
}

// MailDeliveredPayload accompanies EventMailDelivered (remote 2xx ack).
type MailDeliveredPayload struct {
	QueueID    string `json:"queue_id"`
	Recipient  string `json:"rcpt"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	RelayHost  string `json:"relay_host,omitempty"`
	Domain     string `json:"domain,omitempty"`
}

// MailDeferredPayload accompanies EventMailDeferred (transient failure).
type MailDeferredPayload struct {
	QueueID   string    `json:"queue_id"`
	Recipient string    `json:"rcpt"`
	Attempt   int       `json:"attempt,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	NextAt    time.Time `json:"next_at,omitempty"`
	Domain    string    `json:"domain,omitempty"`
}

// MailFailedPayload accompanies EventMailFailed (terminal failure / bounce).
type MailFailedPayload struct {
	QueueID     string `json:"queue_id"`
	Recipient   string `json:"rcpt"`
	FinalReason string `json:"final_reason,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	Domain      string `json:"domain,omitempty"`
}

// MailSpamVerdictPayload accompanies EventMailSpamVerdict.
type MailSpamVerdictPayload struct {
	MessageID  string  `json:"message_id"`
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Domain     string  `json:"domain,omitempty"`
}

// AuthSuccessPayload accompanies EventAuthSuccess.
type AuthSuccessPayload struct {
	PrincipalID uint64 `json:"principal_id"`
	Protocol    string `json:"protocol"`
	SourceIP    string `json:"source_ip,omitempty"`
}

// AuthFailurePayload accompanies EventAuthFailure.
type AuthFailurePayload struct {
	PrincipalHint string `json:"principal_hint,omitempty"`
	Protocol      string `json:"protocol"`
	SourceIP      string `json:"source_ip,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// AuthTOTPEnrollPayload accompanies EventAuthTOTPEnroll.
type AuthTOTPEnrollPayload struct {
	PrincipalID uint64 `json:"principal_id"`
	SourceIP    string `json:"source_ip,omitempty"`
}

// AuthOIDCLinkPayload accompanies EventAuthOIDCLink.
type AuthOIDCLinkPayload struct {
	PrincipalID uint64 `json:"principal_id"`
	Provider    string `json:"provider"`
	Subject     string `json:"subject,omitempty"`
}

// QueueRetryPayload accompanies EventQueueRetry (operator-visible
// summary of the next retry decision; not every retry emits one).
type QueueRetryPayload struct {
	QueueID   string    `json:"queue_id"`
	Recipient string    `json:"rcpt"`
	Attempt   int       `json:"attempt"`
	NextAt    time.Time `json:"next_at"`
	Reason    string    `json:"reason,omitempty"`
}

// ACMECertPayload accompanies acme.cert.* events.
type ACMECertPayload struct {
	Hostname  string    `json:"hostname"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Reason    string    `json:"reason,omitempty"` // populated on cert.failed
}

// DKIMKeyRotatedPayload accompanies EventDKIMKeyRotated.
type DKIMKeyRotatedPayload struct {
	Domain      string `json:"domain"`
	NewSelector string `json:"new_selector"`
	OldSelector string `json:"old_selector,omitempty"`
}

// PluginLifecyclePayload accompanies EventPluginLifecycle. Phase string
// captures supervisor transitions {"started", "restarted", "disabled",
// "stopped"} per docs/design/architecture/07-plugin-architecture.md.
type PluginLifecyclePayload struct {
	PluginName string `json:"plugin_name"`
	Phase      string `json:"phase"`
	Cause      string `json:"cause,omitempty"`
}

// WebhookFailurePayload accompanies EventWebhookFailure.
type WebhookFailurePayload struct {
	WebhookID string `json:"webhook_id"`
	Target    string `json:"target,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
}

// PublishFailedPayload accompanies EventPublishFailed (the dispatcher's
// retry-budget-exhausted signal). Plugins MUST NOT re-emit on receipt.
type PublishFailedPayload struct {
	PluginName string `json:"plugin_name"`
	OrigID     string `json:"orig_event_id"`
	OrigKind   string `json:"orig_kind"`
	Reason     string `json:"reason,omitempty"`
}
