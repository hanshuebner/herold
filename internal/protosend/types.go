package protosend

import (
	"context"

	"github.com/hanshuebner/herold/internal/queue"
)

// Submitter is the narrow surface protosend needs from the outbound
// queue: a single Submit call. Defined as an interface (rather than
// taking a concrete *queue.Queue) so tests can mock the boundary
// without spinning up a real Queue. Production wiring passes a
// *queue.Queue; see cmd/herold for the bind site (TODO).
type Submitter interface {
	Submit(ctx context.Context, msg queue.Submission) (queue.EnvelopeID, error)
}

// sendRequest is the structured /send body. SES-portable shape: SES
// SendEmail uses Source/Destination/Message; we mirror that 1:1 so an
// app coded for SES ports its request shape over with renames only.
//
// Fields deliberately NOT mirrored from SES:
//   - ReplyToAddresses: SES's []string of Reply-To headers; we accept
//     it via Message.Body.Headers["Reply-To"] so the structured form
//     stays small. A future v1.1 may promote it to a top-level field.
//   - ReturnPath / SourceArn / FromArn / ConfigurationSetName ARN
//     forms: SES uses ARN strings; we use plain names. ARNs are an
//     AWS-IAM detail outside our identity model.
//   - SES tag pairs are encoded as a list of {Name,Value} objects
//     identical to SES; the wire shape matches.
type sendRequest struct {
	Source           string       `json:"source"`
	Destination      destination  `json:"destination"`
	Message          messageBody  `json:"message"`
	ConfigurationSet string       `json:"configurationSet,omitempty"`
	Tags             []messageTag `json:"tags,omitempty"`
	IdempotencyKey   string       `json:"idempotencyKey,omitempty"`
	DSNNotify        []string     `json:"dsnNotify,omitempty"`
}

type destination struct {
	ToAddresses  []string `json:"toAddresses,omitempty"`
	CCAddresses  []string `json:"ccAddresses,omitempty"`
	BCCAddresses []string `json:"bccAddresses,omitempty"`
}

type messageBody struct {
	Subject string `json:"subject"`
	Body    body   `json:"body"`
}

type body struct {
	Text    string            `json:"text,omitempty"`
	HTML    string            `json:"html,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type messageTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// sendResponse is returned for a successful structured / raw send.
type sendResponse struct {
	MessageID    string `json:"messageId"`
	SubmissionID string `json:"submissionId"`
}

// sendRawRequest is the /send-raw body. RawMessage is base64-encoded
// RFC 5322 bytes; we decode and submit verbatim.
type sendRawRequest struct {
	Destinations     []string `json:"destinations"`
	RawMessage       string   `json:"rawMessage"`
	ConfigurationSet string   `json:"configurationSet,omitempty"`
	IdempotencyKey   string   `json:"idempotencyKey,omitempty"`
	DSNNotify        []string `json:"dsnNotify,omitempty"`
}

// batchItem is one element of /send-batch. Exactly one of Send or
// SendRaw must be present.
type batchItem struct {
	Send    *sendRequest    `json:"send,omitempty"`
	SendRaw *sendRawRequest `json:"sendRaw,omitempty"`
}

// batchResponse is the /send-batch response: one entry per input.
// On per-item failure, Problem carries the RFC 7807 detail.
type batchEntry struct {
	MessageID    string      `json:"messageId,omitempty"`
	SubmissionID string      `json:"submissionId,omitempty"`
	Problem      *problemDoc `json:"problem,omitempty"`
}

type batchResponse struct {
	Items []batchEntry `json:"items"`
}

// quotaResponse is the /quota body.
type quotaResponse struct {
	DailyLimit      int64 `json:"dailyLimit"`
	DailyUsed       int64 `json:"dailyUsed"`
	DailyRemaining  int64 `json:"dailyRemaining"`
	PerMinuteLimit  int   `json:"perMinuteLimit"`
	PerMinuteUsed   int   `json:"perMinuteUsed"`
	PerMinuteRemain int   `json:"perMinuteRemaining"`
}

// statsResponse is the /stats body.
type statsResponse struct {
	WindowSeconds int64 `json:"windowSeconds"`
	Submitted     int   `json:"submitted"`
	Delivered     int   `json:"delivered"`
	Failed        int   `json:"failed"`
	InFlight      int   `json:"inFlight"`
	Bounced       int   `json:"bounced"`
}
