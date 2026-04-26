package sesinbound

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/hanshuebner/herold/internal/netguard"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// Pipeline is the seam between the SES inbound handler and the delivery
// pipeline.  The adapter in internal/admin/server.go satisfies this with
// a *protosmtp.Server; tests supply a stub.
type Pipeline interface {
	// Ingest runs the inbound delivery pipeline for the given message.
	Ingest(ctx context.Context, req IngestMsg) error
}

// IngestMsg carries the minimal context the pipeline needs from the SES
// notification.  The adapter translates this to protosmtp.IngestRequest.
type IngestMsg struct {
	// Body is the raw RFC 5322 bytes fetched from S3.
	Body []byte
	// MailFrom is the SMTP MAIL FROM address.  For SES inbound this is
	// extracted from the SES receipt's mail.source field.
	MailFrom string
	// SourceIP is the sending MTA's IP address as reported by SES
	// (used for SPF per REQ-HOOK-SES-04).
	SourceIP string
	// EnvelopeTo is the list of RCPT TO addresses SES accepted.
	EnvelopeTo []string
}

// AuditLogger is the minimal audit interface sesinbound needs from the
// store layer.  Production code wires store.Metadata; tests use a stub.
type AuditLogger interface {
	AppendAuditLog(ctx context.Context, entry store.AuditLogEntry) error
}

// Handler is the HTTP handler for POST /hooks/ses/inbound (REQ-HOOK-SES-01).
type Handler struct {
	cfg      Config
	ver      *verifier
	ded      *deduper
	fetcher  *s3Fetcher
	pipeline Pipeline
	log      *slog.Logger
	audit    AuditLogger
}

// Config is the runtime configuration extracted from sysconfig.SESInboundConfig
// after secret resolution.
type Config struct {
	// AWSRegion is the S3/SNS region.
	AWSRegion string
	// S3BucketAllowlist is the set of S3 buckets the handler may fetch
	// from (REQ-HOOK-SES-03).
	S3BucketAllowlist []string
	// SNSTopicARNAllowlist is the set of SNS topic ARNs for which
	// SubscriptionConfirmation is auto-confirmed (REQ-HOOK-SES-03).
	SNSTopicARNAllowlist []string
	// SignatureCertHostAllowlist is the set of allowed hosts in
	// SigningCertURL (REQ-HOOK-SES-06).
	SignatureCertHostAllowlist []string
	// AWSAccessKeyID / AWSSecretAccessKey / AWSSessionToken are the
	// already-resolved (expanded) AWS credentials.
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
}

// New constructs a Handler.  seenStore and audit must be non-nil.
func New(
	cfg Config,
	pipeline Pipeline,
	seenStore SeenStore,
	audit AuditLogger,
	log *slog.Logger,
) *Handler {
	observe.RegisterSESMetrics()
	return &Handler{
		cfg:      cfg,
		ver:      newVerifier(cfg.SignatureCertHostAllowlist),
		ded:      newDeduper(seenStore, 8192, 25*time.Hour),
		fetcher:  newS3Fetcher(cfg.AWSRegion, cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, cfg.AWSSessionToken, cfg.S3BucketAllowlist),
		pipeline: pipeline,
		log:      log,
		audit:    audit,
	}
}

// ServeHTTP implements http.Handler for POST /hooks/ses/inbound.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	const maxEnvelope = 256 << 10 // 256 KiB — the SNS envelope is tiny
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEnvelope))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	var m snsMessage
	if err := json.Unmarshal(body, &m); err != nil {
		h.log.WarnContext(r.Context(), "ses_inbound: JSON parse error",
			slog.String("err", err.Error()))
		observe.SESReceivedTotal.WithLabelValues("rejected_signature").Inc()
		w.WriteHeader(http.StatusOK) // always 200 to SNS
		return
	}

	ctx := r.Context()

	// Verify the SNS message signature for ALL message types
	// (REQ-HOOK-SES-02).
	verOut := h.ver.verifySNSSignature(ctx, &m)
	observe.SESSignatureVerifyTotal.WithLabelValues(string(verOut)).Inc()
	if verOut != VerifyOutcomeValid {
		h.log.WarnContext(ctx, "ses_inbound: signature verification failed",
			slog.String("type", m.Type),
			slog.String("message_id", m.MessageID),
			slog.String("outcome", string(verOut)))
		observe.SESReceivedTotal.WithLabelValues("rejected_signature").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}

	switch m.Type {
	case "SubscriptionConfirmation":
		h.handleConfirmation(ctx, w, &m)
	case "UnsubscribeConfirmation":
		h.log.InfoContext(ctx, "ses_inbound: UnsubscribeConfirmation received; ignoring",
			slog.String("topic_arn", m.TopicArn))
		w.WriteHeader(http.StatusOK)
	case "Notification":
		h.handleNotification(ctx, w, &m)
	default:
		h.log.WarnContext(ctx, "ses_inbound: unknown SNS Type",
			slog.String("type", m.Type))
		w.WriteHeader(http.StatusOK)
	}
}

// handleConfirmation processes a SubscriptionConfirmation SNS message.
func (h *Handler) handleConfirmation(ctx context.Context, w http.ResponseWriter, m *snsMessage) {
	if !h.topicAllowed(m.TopicArn) {
		h.log.WarnContext(ctx, "ses_inbound: SubscriptionConfirmation from non-allowlisted topic; dropping",
			slog.String("topic_arn", m.TopicArn))
		observe.SESReceivedTotal.WithLabelValues("rejected_topic").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}

	// Guard the SubscribeURL against SSRF (REQ-HOOK-SES-06).
	if err := checkSSRFURL(ctx, m.SubscribeURL, h.cfg.SignatureCertHostAllowlist); err != nil {
		h.log.WarnContext(ctx, "ses_inbound: SubscribeURL failed SSRF check",
			slog.String("url", m.SubscribeURL),
			slog.String("err", err.Error()))
		observe.SESReceivedTotal.WithLabelValues("rejected_topic").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.SubscribeURL, nil)
	if err != nil {
		h.log.WarnContext(ctx, "ses_inbound: SubscribeURL request build failed",
			slog.String("err", err.Error()))
		w.WriteHeader(http.StatusOK)
		return
	}
	resp, err := h.ver.httpClient.Do(req)
	if err != nil {
		h.log.WarnContext(ctx, "ses_inbound: SubscribeURL GET failed",
			slog.String("err", err.Error()))
		w.WriteHeader(http.StatusOK)
		return
	}
	resp.Body.Close()
	h.log.InfoContext(ctx, "ses_inbound: SubscriptionConfirmation completed",
		slog.String("topic_arn", m.TopicArn),
		slog.Int("http_status", resp.StatusCode))
	w.WriteHeader(http.StatusOK)
}

// handleNotification processes an SNS Notification carrying a SES mail receipt.
func (h *Handler) handleNotification(ctx context.Context, w http.ResponseWriter, m *snsMessage) {
	// Replay deduplication (REQ-HOOK-SES-02).
	seen, err := h.ded.IsSeen(ctx, m.MessageID)
	if err != nil {
		h.log.WarnContext(ctx, "ses_inbound: dedupe check error; proceeding",
			slog.String("message_id", m.MessageID),
			slog.String("err", err.Error()))
	}
	if seen {
		h.log.InfoContext(ctx, "ses_inbound: duplicate MessageId; dropping",
			slog.String("message_id", m.MessageID))
		observe.SESReceivedTotal.WithLabelValues("rejected_replay").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}

	// Parse the SES notification nested in m.Message.
	var sesMsg sesNotification
	if err := json.Unmarshal([]byte(m.Message), &sesMsg); err != nil {
		h.log.WarnContext(ctx, "ses_inbound: SES notification JSON parse failed",
			slog.String("message_id", m.MessageID),
			slog.String("err", err.Error()))
		observe.SESReceivedTotal.WithLabelValues("pipeline_error").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}

	bucket := sesMsg.Receipt.Action.BucketName
	key := sesMsg.Receipt.Action.ObjectKey

	// Fetch the raw RFC 5322 bytes from S3 (REQ-HOOK-SES-01).
	rawBytes, s3out, s3err := h.fetcher.Fetch(ctx, bucket, key)
	observe.SESS3FetchTotal.WithLabelValues(string(s3out)).Inc()
	if s3err != nil {
		h.log.WarnContext(ctx, "ses_inbound: S3 fetch failed",
			slog.String("message_id", m.MessageID),
			slog.String("bucket", bucket),
			slog.String("key", key),
			slog.String("outcome", string(s3out)),
			slog.String("err", s3err.Error()))
		switch s3out {
		case S3FetchOutcomeBucketDisallowed:
			observe.SESReceivedTotal.WithLabelValues("rejected_bucket").Inc()
		default:
			observe.SESReceivedTotal.WithLabelValues("pipeline_error").Inc()
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Mark seen before pipeline injection so a crash mid-delivery still
	// leaves the dedupe row for the retry (REQ-HOOK-SES-02).
	if err := h.ded.MarkSeen(ctx, m.MessageID, time.Now()); err != nil {
		h.log.WarnContext(ctx, "ses_inbound: MarkSeen error; continuing",
			slog.String("message_id", m.MessageID),
			slog.String("err", err.Error()))
	}

	// Audit log entry per REQ-ADM-300.
	_ = h.audit.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        time.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "ses_inbound",
		Action:    "ses_inbound_received",
		Subject:   "message:" + m.MessageID,
		Outcome:   store.OutcomeSuccess,
		Message: fmt.Sprintf("sns_message_id=%s bucket=%s key=%s",
			m.MessageID, bucket, key),
		Metadata: map[string]string{
			"sns_message_id": m.MessageID,
			"s3_bucket":      bucket,
			"s3_key":         key,
			"topic_arn":      m.TopicArn,
			"source_ip":      sesMsg.Mail.Source,
		},
	})

	// Inject into the delivery pipeline (REQ-HOOK-SES-04).
	ingestErr := h.pipeline.Ingest(ctx, IngestMsg{
		Body:       rawBytes,
		MailFrom:   sesMsg.Mail.Source,
		SourceIP:   sesMsg.Mail.Source,
		EnvelopeTo: sesMsg.Mail.Destination,
	})
	if ingestErr != nil {
		h.log.WarnContext(ctx, "ses_inbound: pipeline ingest error",
			slog.String("message_id", m.MessageID),
			slog.String("err", ingestErr.Error()))
		observe.SESReceivedTotal.WithLabelValues("pipeline_error").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}

	observe.SESReceivedTotal.WithLabelValues("accepted").Inc()
	w.WriteHeader(http.StatusOK)
}

// topicAllowed returns true if topicArn is in the allowlist.
func (h *Handler) topicAllowed(topicArn string) bool {
	for _, a := range h.cfg.SNSTopicARNAllowlist {
		if a == topicArn {
			return true
		}
	}
	return false
}

// checkSSRFURL validates that rawURL's host passes the netguard predicate.
// We do NOT require the host to be in the cert-host allowlist here —
// SubscribeURL domains are the SNS service endpoints, which are the same
// as the cert hosts in practice but the spec only requires the SSRF guard
// (REQ-HOOK-SES-06).
func checkSSRFURL(ctx context.Context, rawURL string, _ []string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid URL %q", rawURL)
	}
	host := u.Hostname()
	// Resolve all IPs and run through the netguard predicate.
	return netguard.CheckHost(ctx, net.DefaultResolver, host)
}

// snsMessage is the top-level SNS HTTPS notification envelope.
// Only fields required for processing are decoded; extras are ignored.
type snsMessage struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	// SubscriptionConfirmation fields.
	Token        string `json:"Token"`
	SubscribeURL string `json:"SubscribeURL"`
}

// sesNotification is the JSON body of an SNS Notification for a SES
// receipt event.  Only the fields sesinbound uses are decoded.
type sesNotification struct {
	Mail struct {
		// Source is the SES-reported MAIL FROM / source IP string.
		// SES puts the sending IP address here for SPF evaluation.
		Source      string   `json:"source"`
		Destination []string `json:"destination"`
		MessageID   string   `json:"messageId"`
	} `json:"mail"`
	Receipt struct {
		Action struct {
			Type       string `json:"type"`
			BucketName string `json:"bucketName"`
			ObjectKey  string `json:"objectKey"`
		} `json:"action"`
	} `json:"receipt"`
}
