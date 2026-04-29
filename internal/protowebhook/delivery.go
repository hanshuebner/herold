package protowebhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// HTTP header names used on every webhook delivery POST.
const (
	HeaderEvent      = "Herold-Event"
	HeaderWebhookID  = "Herold-Webhook-ID"
	HeaderDeliveryID = "Herold-Delivery-ID"
	HeaderSignature  = "Herold-Signature"
	HeaderTimestamp  = "Herold-Timestamp"
)

// deliver runs the per-job lifecycle: build payload, POST, and on
// transient failure schedule a retry per the webhook's RetryPolicy.
//
// Per-attempt context is the dispatcher's parent ctx so shutdown
// cancels in-flight requests; retry waits are gated on ctx as well.
func (d *Dispatcher) deliver(ctx context.Context, hook store.Webhook, p store.Principal, mb store.Mailbox, msg store.Message) {
	deliveryID, err := newDeliveryID()
	if err != nil {
		d.logger.Warn("protowebhook: generate delivery id",
			"activity", observe.ActivityInternal, "err", err.Error())
		return
	}
	payload, dropped, err := d.buildPayload(ctx, hook, deliveryID, p, mb, msg)
	if err != nil {
		d.logger.Warn("protowebhook: build payload",
			"activity", observe.ActivityInternal,
			"webhook_id", uint64(hook.ID),
			"message_id", uint64(msg.ID),
			"err", err.Error())
		return
	}
	if dropped {
		// REQ-HOOK-EXTRACTED-03: text_required + origin=none drops the
		// delivery without retry.  No HTTP POST is issued; the audit
		// log + metric + admin hook log carry the operator-visible
		// signal.
		d.recordOutcome(hook, "dropped_no_text")
		d.recordDropAudit(ctx, hook, deliveryID, msg.ID, msg.Envelope.MessageID)
		d.logger.Info("protowebhook: dropped no_text",
			"activity", observe.ActivitySystem,
			"webhook_id", uint64(hook.ID),
			"delivery_id", deliveryID,
			"message_id", uint64(msg.ID))
		return
	}
	d.dispatchPayload(ctx, hook, deliveryID, payload, uint64(msg.ID))
}

// dispatchPayload runs the per-attempt POST + retry loop for an
// already-built payload. It is the shared pipeline behind both the
// change-feed-driven principal-bound deliver path and the synthetic-
// recipient direct dispatch path (REQ-DIR-RCPT-07 / REQ-HOOK-02).
//
// messageID is logged for correlation; pass 0 for synthetic dispatches
// where no store.MessageID exists.
func (d *Dispatcher) dispatchPayload(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	payload Payload,
	messageID uint64,
) {
	body, err := json.Marshal(payload)
	if err != nil {
		d.logger.Warn("protowebhook: marshal payload",
			"activity", observe.ActivityInternal,
			"webhook_id", uint64(hook.ID),
			"err", err.Error())
		return
	}

	schedule := d.retrySchedule
	if hook.RetryPolicy.MaxAttempts > 0 && hook.RetryPolicy.MaxAttempts-1 < len(schedule) {
		schedule = schedule[:hook.RetryPolicy.MaxAttempts-1]
	}

	attempt := 0
	for {
		attempt++
		status, ok, transient := d.postOnce(ctx, hook, deliveryID, body)
		if ok {
			d.recordOutcome(hook, status)
			d.logger.LogAttrs(ctx, slog.LevelInfo, "protowebhook: delivered",
				slog.String("activity", observe.ActivitySystem),
				slog.Uint64("webhook_id", uint64(hook.ID)),
				slog.String("delivery_id", deliveryID),
				slog.Uint64("message_id", messageID),
				slog.Int("attempt", attempt))
			return
		}
		if !transient {
			d.recordOutcome(hook, status)
			d.logger.LogAttrs(ctx, slog.LevelWarn, "protowebhook: permanent failure",
				slog.String("activity", observe.ActivitySystem),
				slog.Uint64("webhook_id", uint64(hook.ID)),
				slog.String("delivery_id", deliveryID),
				slog.Uint64("message_id", messageID),
				slog.Int("attempt", attempt))
			return
		}
		if attempt > len(schedule) {
			d.recordOutcome(hook, status)
			d.logger.LogAttrs(ctx, slog.LevelError, "protowebhook: retries exhausted",
				slog.String("activity", observe.ActivitySystem),
				slog.Uint64("webhook_id", uint64(hook.ID)),
				slog.String("delivery_id", deliveryID),
				slog.Uint64("message_id", messageID),
				slog.Int("attempts", attempt))
			return
		}
		wait := schedule[attempt-1]
		wait = applyJitter(wait, hook.RetryPolicy.JitterMS)
		select {
		case <-ctx.Done():
			return
		case <-d.clock.After(wait):
		}
	}
}

// recordOutcome bumps the dispatcher metric.  The metric collector is
// only registered when observe.RegisterHookMetrics has been called by
// the server lifecycle; tests that do not register skip the bump.
func (d *Dispatcher) recordOutcome(hook store.Webhook, status string) {
	if observe.HookDeliveriesTotal == nil {
		return
	}
	observe.HookDeliveriesTotal.WithLabelValues(hookMetricName(hook), status).Inc()
}

// recordTruncated bumps the truncation metric for extracted-mode
// deliveries that hit the per-subscription cap.
func (d *Dispatcher) recordTruncated(hook store.Webhook) {
	if observe.HookExtractedTruncatedTotal == nil {
		return
	}
	observe.HookExtractedTruncatedTotal.WithLabelValues(hookMetricName(hook)).Inc()
}

// hookMetricName returns the operator-visible label used by the
// dispatcher metrics.  The webhook row does not yet carry a `name`
// column; we derive a stable id-based label until that field lands.
func hookMetricName(hook store.Webhook) string {
	return strconv.FormatUint(uint64(hook.ID), 10)
}

// recordDropAudit appends the REQ-HOOK-EXTRACTED-03 audit row.  The
// audit message is intentionally short and machine-grep-friendly; the
// hook id and message id appear in Metadata for filterable replay.
//
// messageID is the store-side row id (0 for synthetic dispatches with no
// messages-row); messageIDHeader is the RFC 5322 Message-ID header. Both
// are recorded for correlation.
func (d *Dispatcher) recordDropAudit(ctx context.Context, hook store.Webhook, deliveryID string, messageID store.MessageID, messageIDHeader string) {
	md := map[string]string{
		"delivery_id": deliveryID,
		"reason":      "dropped_no_text",
	}
	if messageID != 0 {
		md["message_id"] = strconv.FormatUint(uint64(messageID), 10)
	}
	if messageIDHeader != "" {
		md["message_id_email"] = messageIDHeader
	}
	entry := store.AuditLogEntry{
		At:        d.clock.Now().UTC(),
		ActorKind: store.ActorSystem,
		ActorID:   "system",
		Action:    "hook.dispatch.dropped_no_text",
		Subject:   fmt.Sprintf("webhook:%d", hook.ID),
		Outcome:   store.OutcomeSuccess,
		Message:   "extracted-mode webhook delivery dropped: text_required and body.text_origin=none",
		Metadata:  md,
	}
	if err := d.store.Meta().AppendAuditLog(ctx, entry); err != nil {
		// Never blocks delivery; the log warn surfaces store-side
		// problems for the operator.
		d.logger.Warn("protowebhook: append audit log",
			"activity", observe.ActivityInternal,
			"webhook_id", uint64(hook.ID),
			"err", err.Error())
	}
}

// postOnce executes a single HTTP attempt. Returns (status, ok, transient)
// where status is the metric label ("2xx" | "4xx" | "5xx" | "timeout" |
// "network"), ok==true on 2xx, transient==true on 5xx / 429 / network or
// context errors that warrant a retry, transient==false on any other 4xx
// that should be treated as permanent (REQ-HOOK-11).
func (d *Dispatcher) postOnce(ctx context.Context, hook store.Webhook, deliveryID string, body []byte) (status string, ok, transient bool) {
	now := d.clock.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.TargetURL, bytes.NewReader(body))
	if err != nil {
		d.logger.Warn("protowebhook: build request",
			"activity", observe.ActivityInternal,
			"webhook_id", uint64(hook.ID),
			"err", err.Error())
		return "network", false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderEvent, EventMailArrived)
	req.Header.Set(HeaderWebhookID, strconv.FormatUint(uint64(hook.ID), 10))
	req.Header.Set(HeaderDeliveryID, deliveryID)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(HeaderSignature, signatureHex(hook.HMACSecret, body))

	resp, err := d.httpClient.Do(req)
	if err != nil {
		// Network or context error: transient unless ctx has been
		// cancelled in which case we should not loop further.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "timeout", false, false
		}
		return "network", false, true
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "2xx", true, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return "5xx", false, true
	default:
		return "4xx", false, false
	}
}

// signatureHex returns hex(HMAC-SHA256(secret, body)).
func signatureHex(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// applyJitter returns base +/- a uniformly random delta of up to
// jitterMS milliseconds. Zero or negative jitterMS returns base
// untouched.
func applyJitter(base time.Duration, jitterMS int) time.Duration {
	if jitterMS <= 0 {
		return base
	}
	span := big.NewInt(int64(jitterMS) * 2)
	n, err := rand.Int(rand.Reader, span)
	if err != nil {
		return base
	}
	delta := time.Duration(n.Int64()-int64(jitterMS)) * time.Millisecond
	out := base + delta
	if out < 0 {
		out = 0
	}
	return out
}

// newDeliveryID returns a 128-bit hex random identifier. Used as the
// Herold-Delivery-ID header and as part of the signed fetch URL.
func newDeliveryID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("protowebhook: random: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// buildPayload assembles the JSON wire shape per
// REQ-HOOK-10/20/30 + REQ-HOOK-EXTRACTED-01..03.
//
// Returns (payload, dropped, err).  When dropped == true the dispatcher
// suppresses the HTTP POST per REQ-HOOK-EXTRACTED-03; the caller still
// records metrics + audit log.
//
// Body-mode selection:
//
//   - extracted: parse the message, run mailparse.ExtractBodyText, cap
//     by hook.EffectiveExtractedTextMaxBytes(); when text_required is
//     set and origin == "none", return dropped=true.
//   - inline / url: existing Phase-2 contract preserved verbatim.
//
// Attachments are emitted with fetch URLs in extracted mode (REQ-HOOK-21).
// A raw_rfc822_url accompanies the extracted-mode payload so receivers
// that want the full message can still get to it.
func (d *Dispatcher) buildPayload(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	p store.Principal,
	mb store.Mailbox,
	msg store.Message,
) (Payload, bool, error) {
	pl := Payload{
		ID:          deliveryID,
		Event:       EventMailArrived,
		WebhookID:   strconv.FormatUint(uint64(hook.ID), 10),
		OccurredAt:  d.clock.Now().UTC().Format(time.RFC3339Nano),
		PrincipalID: strconv.FormatUint(uint64(p.ID), 10),
		MailboxID:   strconv.FormatUint(uint64(mb.ID), 10),
		MessageID:   strconv.FormatUint(uint64(msg.ID), 10),
		Envelope: Envelope{
			From:    msg.Envelope.From,
			Subject: msg.Envelope.Subject,
		},
		AuthResults: extractAuthResults(msg),
	}
	if to := splitAddrList(msg.Envelope.To); len(to) > 0 {
		pl.Envelope.To = to
	}

	switch hook.EffectiveBodyMode() {
	case store.WebhookBodyModeExtracted:
		dropped, err := d.fillExtractedBody(ctx, hook, deliveryID, &pl, msg)
		if err != nil {
			return Payload{}, false, err
		}
		if dropped {
			return Payload{}, true, nil
		}
		return pl, false, nil
	default:
		// inline / url / unspecified: Phase-2 contract.
		if err := d.fillLegacyBody(ctx, hook, deliveryID, &pl, msg); err != nil {
			return Payload{}, false, err
		}
		return pl, false, nil
	}
}

// fillLegacyBody implements the Phase-2 inline / fetch_url body shape.
// Pulled out of buildPayload for readability.
func (d *Dispatcher) fillLegacyBody(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	pl *Payload,
	msg store.Message,
) error {
	wantInline := hook.DeliveryMode == store.DeliveryModeInline
	tooBig := msg.Size > d.inlineMaxSize
	useFetch := !wantInline || tooBig

	if !useFetch {
		raw, err := readBlob(ctx, d.store, msg.Blob.Hash)
		if err != nil {
			// Fall through to fetch_url mode if the body cannot be
			// read inline; the receiver still gets a usable payload.
			d.logger.Warn("protowebhook: inline body read",
				"activity", observe.ActivityInternal,
				"webhook_id", uint64(hook.ID),
				"message_id", uint64(msg.ID),
				"err", err.Error())
			useFetch = true
		} else {
			pl.Body = Body{
				Mode: "inline",
				Inline: &InlineBody{
					RawBase64: encodeBase64(raw),
					Size:      int64(len(raw)),
				},
			}
		}
	}
	if useFetch {
		if d.fetchBase == "" || len(d.signingKey) == 0 {
			return errors.New("protowebhook: fetch URL requested but FetchURLBaseURL/SigningKey not configured")
		}
		expires := d.clock.Now().Add(d.fetchTTL).UTC()
		fu := buildFetchURL(d.fetchBase, deliveryID, msg.Blob.Hash, expires.Unix(), d.signingKey)
		pl.Body = Body{
			Mode: "fetch_url",
			FetchURL: &FetchURL{
				URL:       fu,
				ExpiresAt: expires.Format(time.RFC3339),
			},
		}
	}
	return nil
}

// buildSyntheticPayload assembles the JSON wire shape for a synthetic-
// recipient delivery (REQ-DIR-RCPT-07, REQ-HOOK-02). Mirrors
// buildPayload but sources its inputs from the SyntheticDispatch struct
// rather than store.Principal / store.Mailbox / store.Message: there is
// no mailbox-side row to look up.
//
// Returns (payload, dropped, err) with the same semantics as
// buildPayload.
func (d *Dispatcher) buildSyntheticPayload(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	in SyntheticDispatch,
) (Payload, bool, error) {
	pl := Payload{
		ID:         deliveryID,
		Event:      EventMailArrived,
		WebhookID:  strconv.FormatUint(uint64(hook.ID), 10),
		OccurredAt: d.clock.Now().UTC().Format(time.RFC3339Nano),
		// PrincipalID / MailboxID / MessageID are intentionally empty:
		// synthetic deliveries have no mailbox-side identity. Receivers
		// rely on RouteTag + envelope.to for correlation.
		Envelope: Envelope{
			Subject: in.Parsed.Envelope.Subject,
		},
		RouteTag: in.RouteTag,
	}
	if in.MailFrom != "" {
		pl.Envelope.From = in.MailFrom
	} else if len(in.Parsed.Envelope.From) > 0 {
		pl.Envelope.From = in.Parsed.Envelope.From[0].String()
	}
	if in.Recipient != "" {
		pl.Envelope.To = []string{in.Recipient}
	}

	switch hook.EffectiveBodyMode() {
	case store.WebhookBodyModeExtracted:
		dropped, err := d.fillExtractedBodySynthetic(ctx, hook, deliveryID, &pl, in)
		if err != nil {
			return Payload{}, false, err
		}
		if dropped {
			return Payload{}, true, nil
		}
		return pl, false, nil
	default:
		if err := d.fillLegacyBodySynthetic(ctx, hook, deliveryID, &pl, in); err != nil {
			return Payload{}, false, err
		}
		return pl, false, nil
	}
}

// fillLegacyBodySynthetic mirrors fillLegacyBody for the synthetic path.
func (d *Dispatcher) fillLegacyBodySynthetic(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	pl *Payload,
	in SyntheticDispatch,
) error {
	wantInline := hook.DeliveryMode == store.DeliveryModeInline
	tooBig := in.Size > d.inlineMaxSize
	useFetch := !wantInline || tooBig

	if !useFetch {
		raw, err := readBlob(ctx, d.store, in.BlobHash)
		if err != nil {
			d.logger.Warn("protowebhook: synthetic inline body read",
				"activity", observe.ActivityInternal,
				"webhook_id", uint64(hook.ID),
				"recipient", in.Recipient,
				"err", err.Error())
			useFetch = true
		} else {
			pl.Body = Body{
				Mode: "inline",
				Inline: &InlineBody{
					RawBase64: encodeBase64(raw),
					Size:      int64(len(raw)),
				},
			}
		}
	}
	if useFetch {
		if d.fetchBase == "" || len(d.signingKey) == 0 {
			return errors.New("protowebhook: fetch URL requested but FetchURLBaseURL/SigningKey not configured")
		}
		expires := d.clock.Now().Add(d.fetchTTL).UTC()
		fu := buildFetchURL(d.fetchBase, deliveryID, in.BlobHash, expires.Unix(), d.signingKey)
		pl.Body = Body{
			Mode: "fetch_url",
			FetchURL: &FetchURL{
				URL:       fu,
				ExpiresAt: expires.Format(time.RFC3339),
			},
		}
	}
	return nil
}

// fillExtractedBodySynthetic mirrors fillExtractedBody for the synthetic
// path. The parsed message is supplied verbatim (no re-parse).
func (d *Dispatcher) fillExtractedBodySynthetic(
	_ context.Context,
	hook store.Webhook,
	deliveryID string,
	pl *Payload,
	in SyntheticDispatch,
) (dropped bool, err error) {
	if d.fetchBase == "" || len(d.signingKey) == 0 {
		return false, errors.New("protowebhook: extracted mode requires FetchURLBaseURL/SigningKey")
	}
	text, origin := mailparse.ExtractBodyText(in.Parsed)
	if origin == mailparse.BodyTextOriginNone && hook.TextRequired {
		return true, nil
	}
	maxBytes := hook.EffectiveExtractedTextMaxBytes()
	truncated := false
	if int64(len(text)) > maxBytes {
		text = text[:maxBytes]
		truncated = true
		d.recordTruncated(hook)
	}

	expires := d.clock.Now().Add(d.fetchTTL).UTC()
	rawURL := buildFetchURL(d.fetchBase, deliveryID, in.BlobHash, expires.Unix(), d.signingKey)
	pl.Body = Body{
		Mode:          "extracted",
		Text:          text,
		TextOrigin:    string(origin),
		TextTruncated: truncated,
	}
	pl.RawRFC822URL = rawURL
	pl.Attachments = buildAttachments(in.Parsed, d.fetchBase, deliveryID, in.BlobHash, expires.Unix(), d.signingKey)
	return false, nil
}

// fillExtractedBody implements REQ-HOOK-EXTRACTED-01..03.  Returns
// dropped == true when text_required is set on the subscription and
// the extractor produced origin == "none".
func (d *Dispatcher) fillExtractedBody(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	pl *Payload,
	msg store.Message,
) (dropped bool, err error) {
	if d.fetchBase == "" || len(d.signingKey) == 0 {
		return false, errors.New("protowebhook: extracted mode requires FetchURLBaseURL/SigningKey")
	}
	raw, err := readBlob(ctx, d.store, msg.Blob.Hash)
	if err != nil {
		return false, fmt.Errorf("protowebhook: read blob: %w", err)
	}
	parsed, perr := mailparse.Parse(bytes.NewReader(raw), mailparse.NewParseOptions())
	var (
		text   string
		origin mailparse.BodyTextOrigin
	)
	if perr != nil {
		// On parse failure, the only safe origin claim is "none";
		// log and fall through so text_required still drops.
		d.logger.Warn("protowebhook: parse for extraction",
			"activity", observe.ActivityInternal,
			"webhook_id", uint64(hook.ID),
			"message_id", uint64(msg.ID),
			"err", perr.Error())
		origin = mailparse.BodyTextOriginNone
	} else {
		text, origin = mailparse.ExtractBodyText(parsed)
	}
	if origin == mailparse.BodyTextOriginNone && hook.TextRequired {
		return true, nil
	}

	maxBytes := hook.EffectiveExtractedTextMaxBytes()
	truncated := false
	if int64(len(text)) > maxBytes {
		text = text[:maxBytes]
		truncated = true
		d.recordTruncated(hook)
	}

	expires := d.clock.Now().Add(d.fetchTTL).UTC()
	rawURL := buildFetchURL(d.fetchBase, deliveryID, msg.Blob.Hash, expires.Unix(), d.signingKey)
	pl.Body = Body{
		Mode:          "extracted",
		Text:          text,
		TextOrigin:    string(origin),
		TextTruncated: truncated,
	}
	pl.RawRFC822URL = rawURL
	if perr == nil {
		pl.Attachments = buildAttachments(parsed, d.fetchBase, deliveryID, msg.Blob.Hash, expires.Unix(), d.signingKey)
	}
	return false, nil
}

// buildAttachments turns every non-text non-multipart leaf into an
// Attachment carrying a signed fetch URL.  The fetch URL points at the
// raw rfc822 message blob — Phase-3 v1 does not split each attachment
// into its own blob; receivers fetch the whole message and pull the
// part out themselves.  When a future wave stores per-attachment blobs
// the FetchURL can swap to a per-part signed token without breaking
// the receiver-side wire shape.
func buildAttachments(m mailparse.Message, base, deliveryID, blobHash string, expUnix int64, key []byte) []Attachment {
	parts := mailparse.Attachments(m)
	if len(parts) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(parts))
	url := buildFetchURL(base, deliveryID, blobHash, expUnix, key)
	for _, p := range parts {
		out = append(out, Attachment{
			Filename:    p.Filename,
			ContentType: p.ContentType,
			Size:        int64(len(p.Bytes)),
			FetchURL:    url,
		})
	}
	return out
}

// readBlob slurps a blob into memory. Used only when the message is
// known to be below the inline threshold.
func readBlob(ctx context.Context, st store.Store, hash string) ([]byte, error) {
	if hash == "" {
		return nil, nil
	}
	r, err := st.Blobs().Get(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("blob get: %w", err)
	}
	defer r.Close()
	return io.ReadAll(r)
}

// encodeBase64 returns the standard base64 encoding of b.
func encodeBase64(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// splitAddrList splits a comma-separated raw header value into
// per-address strings, trimming each. We do not parse RFC 5322 groups
// or comments here — the cached envelope already carries display-form
// addresses; we just split.
func splitAddrList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// buildFetchURL assembles the signed fetch-URL form used by the
// FetchHandler verifier. The URL embeds the delivery id, blob hash,
// expiry, and an HMAC over the triple. The query string layout is
// stable so receivers can re-encode it without breaking the signature.
func buildFetchURL(base, deliveryID, blobHash string, expUnix int64, key []byte) string {
	v := url.Values{}
	v.Set("blob", blobHash)
	v.Set("exp", strconv.FormatInt(expUnix, 10))
	token := fetchURLToken(deliveryID, blobHash, expUnix, key)
	v.Set("token", token)
	return base + "/webhook-fetch/" + deliveryID + "?" + v.Encode()
}

// fetchURLToken is the HMAC the FetchHandler verifies. Recipe:
// hex(HMAC-SHA256(key, "<delivery-id>:<blob-hash>:<exp-unix>")).
func fetchURLToken(deliveryID, blobHash string, expUnix int64, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, deliveryID)
	_, _ = io.WriteString(mac, ":")
	_, _ = io.WriteString(mac, blobHash)
	_, _ = io.WriteString(mac, ":")
	_, _ = io.WriteString(mac, strconv.FormatInt(expUnix, 10))
	return hex.EncodeToString(mac.Sum(nil))
}
