package email

// Email/unsubscribe (issue #412, docs/design/web/requirements/14-
// unsubscribe.md REQ-UNS-02/04/20, capability
// https://netzhansa.com/jmap/unsubscribe). A browser fetch() of a
// sender's RFC 8058 one-click List-Unsubscribe URL is a cross-origin
// request: the sender's endpoint never sends
// Access-Control-Allow-Origin for an arbitrary webmail origin, so the
// browser discards the response even when the POST reached the
// sender, and the Suite can never observe success. This method
// performs the POST server-side, on the principal's behalf, and
// reports the upstream outcome back over JMAP where no CORS policy
// applies.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/extimg"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// unsubscribeTimeout bounds the one-click POST end to end (connect +
// headers + body).
const unsubscribeTimeout = 10 * time.Second

// unsubscribeOneClickBody is the RFC 8058 §3.1 mandated POST body.
const unsubscribeOneClickBody = "List-Unsubscribe=One-Click"

// unsubscribeUserAgent identifies the server-side requester. Distinct
// from the image fetcher's UA string so operator logs can tell the two
// guarded-outbound-fetch call sites apart.
const unsubscribeUserAgent = "herold-unsubscribe/1.0 (+https://github.com/hanshuebner/herold)"

// unsubscribeStatus enumerates the Email/unsubscribe response's status
// field.
type unsubscribeStatus string

const (
	// unsubscribeOK: the upstream responded with a 2xx status.
	unsubscribeOK unsubscribeStatus = "ok"
	// unsubscribeFailed: the message qualified for one-click (both
	// header checks passed) but the POST itself did not succeed --
	// non-2xx response, timeout, transport error, or SSRF-guard block.
	unsubscribeFailed unsubscribeStatus = "failed"
	// unsubscribeUnsupported: the message does not carry a valid RFC
	// 8058 one-click pair (missing/mismatched List-Unsubscribe-Post,
	// or no HTTPS List-Unsubscribe URL -- REQ-UNS-02/04). No network
	// request is attempted.
	unsubscribeUnsupported unsubscribeStatus = "unsupported"
)

// unsubscribeRequest is the wire-form Email/unsubscribe request.
type unsubscribeRequest struct {
	AccountID jmapID `json:"accountId"`
	EmailID   jmapID `json:"emailId"`
}

// unsubscribeResponse is the wire-form Email/unsubscribe response.
type unsubscribeResponse struct {
	EmailID jmapID            `json:"emailId"`
	Status  unsubscribeStatus `json:"status"`
	// HTTPStatus is the upstream HTTP status code, present only when a
	// request actually reached the upstream and returned a response
	// (i.e. never set for "unsupported", and never set for a
	// transport-level failure such as a timeout or an SSRF-guard
	// block).
	HTTPStatus int `json:"httpStatus,omitempty"`
	// Error is a short, human-readable diagnostic. Present on "failed"
	// and "unsupported" outcomes; omitted on success.
	Error string `json:"error,omitempty"`
}

// unsubscribeHandler implements Email/unsubscribe.
type unsubscribeHandler struct{ h *handlerSet }

func (u unsubscribeHandler) Method() string { return "Email/unsubscribe" }

func (u unsubscribeHandler) Execute(ctx context.Context, args json.RawMessage) (any, *protojmap.MethodError) {
	callerPID, merr := principalFromCtx(ctx)
	if merr != nil {
		return nil, merr
	}
	var req unsubscribeRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, protojmap.NewMethodError("invalidArguments", err.Error())
		}
	}
	ownerPID, merr := resolveAccount(ctx, u.h.store.Meta(), callerPID, req.AccountID)
	if merr != nil {
		return nil, merr
	}
	mid, ok := emailIDFromJMAP(req.EmailID)
	if !ok {
		return nil, protojmap.NewMethodError("invalidArguments", "emailId is not a valid Email id")
	}

	m, err := loadMessageForPrincipal(ctx, u.h.store.Meta(), callerPID, mid)
	if err != nil {
		if errors.Is(err, errMessageMissing) {
			return nil, protojmap.NewMethodError("notFound", "no such Email")
		}
		return nil, serverFail(fmt.Errorf("email: unsubscribe: load message: %w", err))
	}
	mb, err := u.h.store.Meta().GetMailboxByID(ctx, m.MailboxID)
	if err != nil {
		return nil, serverFail(fmt.Errorf("email: unsubscribe: get mailbox: %w", err))
	}
	if mb.PrincipalID != ownerPID {
		// Visible to the caller but not under the requested account --
		// treat as absent for this account (REQ-PROTO-33), same as
		// every other Email/* method in this package.
		return nil, protojmap.NewMethodError("notFound", "no such Email")
	}

	resp := unsubscribeResponse{EmailID: req.EmailID}

	rc, err := u.h.store.Blobs().Get(ctx, m.Blob.Hash)
	if err != nil {
		return nil, serverFail(fmt.Errorf("email: unsubscribe: blob get: %w", err))
	}
	raw, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		return nil, serverFail(fmt.Errorf("email: unsubscribe: read blob: %w", rerr))
	}
	parsed, perr := u.h.parseFn(bytes.NewReader(raw))
	if perr != nil {
		resp.Status = unsubscribeUnsupported
		resp.Error = "could not parse message"
		u.logOutcome(ctx, mid, resp)
		return resp, nil
	}

	rawURL, unsupportedReason := oneClickURL(parsed.Headers.Get("List-Unsubscribe-Post"), parsed.Headers.Get("List-Unsubscribe"))
	if unsupportedReason != "" {
		resp.Status = unsubscribeUnsupported
		resp.Error = unsupportedReason
		u.logOutcome(ctx, mid, resp)
		return resp, nil
	}

	resp.Status, resp.HTTPStatus, resp.Error = u.postOneClick(ctx, rawURL)
	u.logOutcome(ctx, mid, resp)
	return resp, nil
}

// oneClickURLPattern extracts every angle-bracketed URL from a
// List-Unsubscribe header value, mirroring the Suite's client-side
// parseAngleBracketUrls (web/apps/suite/src/lib/mail/list-headers.ts)
// so both sides agree on what counts as an advertised URL.
var oneClickURLPattern = regexp.MustCompile(`<([^>]+)>`)

// oneClickURL validates the RFC 8058 one-click pair (REQ-UNS-02) and
// returns the HTTPS List-Unsubscribe URL to POST to. When the pair does
// not validate it returns ("", reason) with a short human-readable
// reason suitable for the response's Error field.
func oneClickURL(listUnsubscribePost, listUnsubscribe string) (url string, unsupportedReason string) {
	if !strings.EqualFold(strings.TrimSpace(listUnsubscribePost), unsubscribeOneClickBody) {
		return "", "message does not advertise List-Unsubscribe-Post: List-Unsubscribe=One-Click"
	}
	for _, m := range oneClickURLPattern.FindAllStringSubmatch(listUnsubscribe, -1) {
		candidate := strings.TrimSpace(m[1])
		if strings.HasPrefix(strings.ToLower(candidate), "https://") {
			return candidate, ""
		}
	}
	return "", "no HTTPS List-Unsubscribe URL present (REQ-UNS-04: cleartext http: URLs are not honoured)"
}

// postOneClick issues the RFC 8058 POST through the SSRF-guarded
// client and classifies the outcome. Never returns a status of "" --
// the returned unsubscribeStatus is always ok or failed (unsupported
// is decided earlier, before any network access).
func (u unsubscribeHandler) postOneClick(ctx context.Context, rawURL string) (unsubscribeStatus, int, string) {
	reqCtx, cancel := context.WithTimeout(ctx, unsubscribeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, strings.NewReader(unsubscribeOneClickBody))
	if err != nil {
		return unsubscribeFailed, 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", unsubscribeUserAgent)
	// No cookies: the client below carries no CookieJar. No Referer:
	// never set explicitly, and Go's http.Client does not add one on
	// its own.

	client := u.h.unsubscribeClient
	if client == nil {
		client = extimg.NewGuardedClient(u.h.extImg, unsubscribeTimeout)
	}
	resp, err := client.Do(req)
	if err != nil {
		if extimg.IsBlockedSSRF(err) {
			// Do not echo the resolved IP / matching CIDR back to the
			// caller: the List-Unsubscribe URL is sender-controlled,
			// so reflecting guard internals here would hand a
			// malicious sender an SSRF oracle against the principal's
			// own click.
			return unsubscribeFailed, 0, "destination not permitted"
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return unsubscribeFailed, 0, "timeout"
		}
		return unsubscribeFailed, 0, err.Error()
	}
	defer resp.Body.Close()
	// Drain (bounded) so the connection can be reused; the response
	// body content is not meaningful to the caller.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return unsubscribeOK, resp.StatusCode, ""
	}
	return unsubscribeFailed, resp.StatusCode, fmt.Sprintf("upstream returned %s", resp.Status)
}

// logOutcome emits one INFO log line per Email/unsubscribe call,
// recording the message id and outcome (the ticket's "logged at INFO
// with the message id and status" requirement). Before this, an
// operator debugging a "unsubscribe says failed" report had no
// server-side signal at all -- the whole point of this method is to
// move the observable failure point from the browser's console (never
// seen by the operator) to the server's own log.
func (u unsubscribeHandler) logOutcome(ctx context.Context, mid store.MessageID, resp unsubscribeResponse) {
	attrs := []slog.Attr{
		slog.String("activity", observe.ActivityUser),
		slog.String("subsystem", "protojmap"),
		slog.Uint64("message_id", uint64(mid)),
		slog.String("status", string(resp.Status)),
	}
	if resp.HTTPStatus != 0 {
		attrs = append(attrs, slog.Int("http_status", resp.HTTPStatus))
	}
	if resp.Error != "" {
		attrs = append(attrs, slog.String("error", resp.Error))
	}
	u.h.logger.LogAttrs(ctx, slog.LevelInfo, "email: unsubscribe outcome", attrs...)
}
