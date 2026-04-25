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
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
		d.logger.Warn("protowebhook: generate delivery id", "err", err.Error())
		return
	}
	payload, err := d.buildPayload(ctx, hook, deliveryID, p, mb, msg)
	if err != nil {
		d.logger.Warn("protowebhook: build payload",
			"webhook_id", uint64(hook.ID),
			"message_id", uint64(msg.ID),
			"err", err.Error())
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		d.logger.Warn("protowebhook: marshal payload",
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
		ok, transient := d.postOnce(ctx, hook, deliveryID, body)
		if ok {
			d.logger.Info("protowebhook: delivered",
				"webhook_id", uint64(hook.ID),
				"delivery_id", deliveryID,
				"message_id", uint64(msg.ID),
				"attempt", attempt)
			return
		}
		if !transient {
			d.logger.Warn("protowebhook: permanent failure",
				"webhook_id", uint64(hook.ID),
				"delivery_id", deliveryID,
				"message_id", uint64(msg.ID),
				"attempt", attempt)
			return
		}
		if attempt > len(schedule) {
			d.logger.Warn("protowebhook: retries exhausted",
				"webhook_id", uint64(hook.ID),
				"delivery_id", deliveryID,
				"message_id", uint64(msg.ID),
				"attempts", attempt)
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

// postOnce executes a single HTTP attempt. Returns (ok, transient) where
// ok==true on 2xx, transient==true on 5xx / 429 / network or context
// errors that warrant a retry, transient==false on any other 4xx that
// should be treated as permanent (REQ-HOOK-11).
func (d *Dispatcher) postOnce(ctx context.Context, hook store.Webhook, deliveryID string, body []byte) (ok, transient bool) {
	now := d.clock.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.TargetURL, bytes.NewReader(body))
	if err != nil {
		d.logger.Warn("protowebhook: build request",
			"webhook_id", uint64(hook.ID),
			"err", err.Error())
		return false, false
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
			return false, false
		}
		return false, true
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true
	default:
		return false, false
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

// buildPayload assembles the JSON wire shape per REQ-HOOK-10/20/30.
// When the message body is below InlineBodyMaxSize we embed it
// base64-encoded; otherwise we substitute a signed fetch URL valid for
// FetchURLTTL. The hook's DeliveryMode preference is honoured but
// "inline" is overridden to fetch_url when the size threshold is
// exceeded (REQ-HOOK-20).
func (d *Dispatcher) buildPayload(
	ctx context.Context,
	hook store.Webhook,
	deliveryID string,
	p store.Principal,
	mb store.Mailbox,
	msg store.Message,
) (Payload, error) {
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

	wantInline := hook.DeliveryMode == store.DeliveryModeInline
	tooBig := msg.Size > d.inlineMaxSize
	useFetch := !wantInline || tooBig

	if !useFetch {
		raw, err := readBlob(ctx, d.store, msg.Blob.Hash)
		if err != nil {
			// Fall through to fetch_url mode if the body cannot be
			// read inline; the receiver still gets a usable payload.
			d.logger.Warn("protowebhook: inline body read",
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
			return Payload{}, errors.New("protowebhook: fetch URL requested but FetchURLBaseURL/SigningKey not configured")
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
	return pl, nil
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
