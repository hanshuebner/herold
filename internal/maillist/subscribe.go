package maillist

// subscribe.go implements Stage 3 self-subscription (issue #185,
// docs/design/server/requirements/28-mailing-lists.md REQ-MLIST-60..63):
//
//	POST /lists/{id}/subscribe             subscribe request
//	GET  /lists/{id}/confirm?token=...     double opt-in landing page (safe)
//	POST /lists/{id}/confirm?token=...     double opt-in confirmation (mutates)
//
// Policy (REQ-MLIST-60):
//   - closed:            no public subscribe endpoint. handleSubscribe
//     responds 404 exactly like an unknown list, so a probe cannot even
//     tell "this list exists but does not accept subscribers" from
//     "this list id does not exist".
//   - open:               subscribe -> pending member -> confirm -> active.
//   - request-approval:   subscribe -> pending member -> confirm ->
//     awaiting-approval, surfaced to the list's owner_principal for an
//     admin REST/SPA approve or reject (REQ-MLIST-62). The address still
//     proves control of its own mailbox before an owner ever sees the
//     request -- request-approval is an ADDITIONAL gate on top of double
//     opt-in, not a substitute for it.
//
// NO MEMBER ENUMERATION (mirrors the S2 unsubscribe/manage surface,
// publicserver.go's own header comment): handleSubscribe returns the
// same 202 response, with the same body, in every one of these cases --
// a brand new address, an address that is already active, one that is
// already pending, and one that is rate-limited into silence. Whether a
// given address is already a member of this list is never observable
// from the HTTP response. TestPublicServer_SubscribeNoEnumeration
// (subscribe_test.go) asserts the response is identical across all of
// these.
//
// CONFIRM-SEND RATE BOUND (REQ-MLIST-61's "no mail is delivered to a
// pending member" plus the general amplification concern): every
// TokenPurposeConfirm mint+send, whether from a fresh subscribe, a
// repeated subscribe of an already-pending/suspended/unsubscribed
// address, or a resubscribe-request from the S2 management page, goes
// through sendConfirmToken, which is gated by PublicServer.limiter() --
// confirmSendLimit sends per (list, address) per confirmSendWindow
// (ratelimit.go). A caller cannot force herold to mail an arbitrary
// victim address more than that many times an hour by any combination of
// these entry points.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// confirmVerb distinguishes the wording of the confirmation email
// between a fresh subscribe and a resubscribe-from-the-management-page
// request; the mechanics (token, TTL, rate limit, confirm endpoint) are
// otherwise identical.
type confirmVerb string

const (
	confirmVerbSubscribe   confirmVerb = "subscribe"
	confirmVerbResubscribe confirmVerb = "resubscribe"
)

// subscribeRequestBody is the POST /lists/{id}/subscribe wire shape.
// Accepted as either application/json ({"address": "..."}) or a plain
// HTML form POST (address=...), so a bare <form method=post> on a
// static subscribe page works with no JavaScript.
type subscribeRequestBody struct {
	Address string `json:"address"`
}

// parseSubscribeAddress reads the address field from either an
// application/json or form-encoded request body. Returns ok=false
// after writing its own 400 problem response on a malformed body --
// this is a client-side format error, not a member-enumeration signal
// (it fires identically regardless of whether any address was even
// syntactically present to check against the roster).
func parseSubscribeAddress(w http.ResponseWriter, r *http.Request) (string, bool) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body subscribeRequestBody
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err != nil {
			writeProblem(w, http.StatusBadRequest, "bad_request", "could not parse request body", err.Error())
			return "", false
		}
		return body.Address, true
	}
	if err := r.ParseForm(); err != nil {
		writeProblem(w, http.StatusBadRequest, "bad_request", "could not parse request body", err.Error())
		return "", false
	}
	return r.PostForm.Get("address"), true
}

// handleSubscribe implements POST /lists/{id}/subscribe (REQ-MLIST-60/61/62).
func (p *PublicServer) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	listID, ok := mailingListIDFromPathValue(r.PathValue("id"))
	if !ok {
		writeProblem(w, http.StatusNotFound, "not_found", "list not found", "")
		return
	}
	ml, err := p.Meta.GetMailingList(r.Context(), listID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not_found", "list not found", "")
		return
	}
	if ml.SubscribePolicy == store.MailingListSubscribeClosed {
		// REQ-MLIST-60: "Only open and request-approval expose a public
		// subscribe endpoint." A closed list must not even reveal that
		// it exists to a subscribe probe, so this is identical to the
		// list-not-found response above.
		writeProblem(w, http.StatusNotFound, "not_found", "list not found", "")
		return
	}

	raw, ok := parseSubscribeAddress(w, r)
	if !ok {
		return
	}
	address, _, err := store.NormalizeMailingListAddress(raw)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "validation_failed", "address must be a valid email address", "")
		return
	}

	p.processSubscribeRequest(r.Context(), ml, address)
	writeSubscribeAccepted(w)
}

// writeSubscribeAccepted writes the ONE response body handleSubscribe
// ever returns on the no-enumeration path, regardless of what
// processSubscribeRequest actually did internally.
func writeSubscribeAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "pending",
		"message": "If this address can be subscribed, a confirmation email has been sent.",
	})
}

// processSubscribeRequest performs the actual subscribe-or-resend
// side effect for address on ml. Every branch is best-effort: a store
// or mail error is logged, never surfaced to the HTTP caller (the
// no-enumeration response has already been decided independently of
// this function's outcome).
func (p *PublicServer) processSubscribeRequest(ctx context.Context, ml store.MailingList, address string) {
	existing, err := p.Meta.GetMailingListMemberByAddress(ctx, ml.ID, address)
	switch {
	case err == nil:
		p.resendOrIgnore(ctx, ml, existing, address)
		return
	case errors.Is(err, store.ErrNotFound):
		// fall through to insert
	default:
		p.Logger.WarnContext(ctx, "maillist: subscribe: look up existing member failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}

	member, err := p.Meta.AddMailingListMember(ctx, store.MailingListMember{
		ListID:          ml.ID,
		ExternalAddress: &address,
		State:           store.MailingListMemberPending,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Lost a race against a concurrent subscribe request for the
			// same address between the lookup above and this insert:
			// re-fetch and treat it exactly like the "already exists"
			// branch rather than erroring.
			if again, gerr := p.Meta.GetMailingListMemberByAddress(ctx, ml.ID, address); gerr == nil {
				p.resendOrIgnore(ctx, ml, again, address)
			}
			return
		}
		p.Logger.WarnContext(ctx, "maillist: subscribe: add pending member failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	p.audit(ctx, ml, "maillist.member.subscribe_requested",
		fmt.Sprintf("member_id=%d via=public_self_service", member.ID))
	if err := p.sendConfirmToken(ctx, ml, member, address, confirmVerbSubscribe); err != nil {
		p.Logger.WarnContext(ctx, "maillist: subscribe: send confirm token failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
}

// resendOrIgnore handles a subscribe request for an address that
// already has a roster row: active and awaiting-approval members need
// nothing further (they are already fully in-flight); pending,
// suspended, and unsubscribed rows get a fresh confirmation email
// (REQ-MLIST-63: "Re-subscribe follows the same confirmation path as a
// new subscription"), subject to the same rate limit as a fresh
// subscribe.
func (p *PublicServer) resendOrIgnore(ctx context.Context, ml store.MailingList, member store.MailingListMember, address string) {
	switch member.State {
	case store.MailingListMemberActive, store.MailingListMemberAwaitingApproval:
		return
	default:
		if err := p.sendConfirmToken(ctx, ml, member, address, confirmVerbSubscribe); err != nil {
			p.Logger.WarnContext(ctx, "maillist: subscribe: resend confirm token failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", ml.PostingAddress),
				slog.String("err", err.Error()))
		}
	}
}

// sendConfirmToken mints a fresh TokenPurposeConfirm token for member
// and mails a confirmation link built from it, worded per verb. Shared
// by the subscribe endpoint above and doResubscribeRequest
// (publicserver.go) so both entry points debit the same per-(list,
// address) rate-limit bucket (ratelimit.go). Returns nil (a silent,
// logged no-op) when the rate limit is exhausted -- from the caller's
// perspective this is indistinguishable from "the confirmation email
// is in flight", matching the no-enumeration posture.
func (p *PublicServer) sendConfirmToken(ctx context.Context, ml store.MailingList, member store.MailingListMember, address string, verb confirmVerb) error {
	if p.Blobs == nil || p.Tokens == nil {
		return errors.New("maillist: send confirm token: no Blobs/Tokens wired")
	}
	key := fmt.Sprintf("%d:%s", ml.ID, address)
	if !p.limiter().allow(key) {
		p.Logger.InfoContext(ctx, "maillist: confirm-send rate limit reached; suppressing send",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.Uint64("member_id", uint64(member.ID)))
		return nil
	}

	now := p.Clock.Now()
	token, err := p.Tokens.Sign(TokenPurposeConfirm, ml.ID, member.ID, now.Add(ConfirmTokenTTL))
	if err != nil {
		return fmt.Errorf("sign confirm token: %w", err)
	}
	confirmURL := strings.TrimRight(p.PublicBaseURL, "/") + fmt.Sprintf("/lists/%d/confirm?token=%s", uint64(ml.ID), token)
	body := buildConfirmationEmail(ml, address, confirmURL, now, verb)
	ref, err := p.Blobs.Put(ctx, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("persist confirmation email: %w", err)
	}
	item := store.QueueItem{
		MailFrom:      "postmaster@" + ml.Domain,
		RcptTo:        address,
		BodyBlobHash:  ref.Hash,
		State:         store.QueueStateQueued,
		NextAttemptAt: now,
		DSNNotify:     store.DSNNotifyNever,
		CreatedAt:     now,
	}
	if _, err := p.Meta.EnqueueMessage(ctx, item); err != nil {
		return fmt.Errorf("enqueue confirmation email: %w", err)
	}
	p.audit(ctx, ml, "maillist.member.confirm_sent",
		fmt.Sprintf("member_id=%d via=public_self_service verb=%s", member.ID, verb))
	p.Logger.InfoContext(ctx, "maillist: confirmation email enqueued",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", ml.PostingAddress),
		slog.Uint64("member_id", uint64(member.ID)),
		slog.String("verb", string(verb)))
	return nil
}

// buildConfirmationEmail renders the RFC 5322 plain-text double opt-in
// confirmation email, mirroring notify.go's buildSuspendNotice style.
func buildConfirmationEmail(ml store.MailingList, addr, confirmURL string, now time.Time, verb confirmVerb) []byte {
	var b strings.Builder
	b.WriteString("From: postmaster@" + ml.Domain + "\r\n")
	b.WriteString("To: " + addr + "\r\n")
	fmt.Fprintf(&b, "Subject: Confirm your %s to %s\r\n", verb, ml.PostingAddress)
	b.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b,
		"You (or someone using your address) asked to %s %s to the\r\n"+
			"mailing list %s.\r\n"+
			"\r\n"+
			"Confirm by opening this link:\r\n"+
			"\r\n"+
			"  %s\r\n"+
			"\r\n"+
			"If you did not request this, no action is needed -- ignoring this\r\n"+
			"message leaves your subscription unchanged.\r\n",
		verb, addr, ml.PostingAddress, confirmURL)
	return []byte(b.String())
}

// handleConfirmGet implements GET /lists/{id}/confirm (REQ-MLIST-61/63).
// SAFE: renders a landing page and never mutates -- see the package's
// GET-safety header comment in publicserver.go.
func (p *PublicServer) handleConfirmGet(w http.ResponseWriter, r *http.Request) {
	ml, member, ok := p.resolveToken(w, r, TokenPurposeConfirm)
	if !ok {
		return
	}
	p.renderConfirmPage(w, r, ml, member, "")
}

// handleConfirmPost implements POST /lists/{id}/confirm: the one
// mutating step of the double opt-in flow (REQ-MLIST-61/63).
func (p *PublicServer) handleConfirmPost(w http.ResponseWriter, r *http.Request) {
	ml, member, ok := p.resolveToken(w, r, TokenPurposeConfirm)
	if !ok {
		return
	}
	notice, err := p.doConfirm(r.Context(), ml, member)
	if err != nil {
		p.Logger.ErrorContext(r.Context(), "maillist: confirm failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		writeProblem(w, http.StatusInternalServerError, "internal_error", "could not process the request", "")
		return
	}
	updated, err := p.Meta.GetMailingListMember(r.Context(), member.ID)
	if err != nil {
		updated = member
	}
	p.renderConfirmPage(w, r, ml, updated, notice)
}

// doConfirm performs the one state transition the whole Stage 3 flow
// exists to gate: a TokenPurposeConfirm token has just been verified
// (proving control of the mailbox, REQ-MLIST-61), so member moves out
// of "not yet confirmed" per the list's CURRENT subscribe policy --
// deliberately the current policy, not whatever it was when the token
// was minted, since an operator may have changed it in the interim and
// the current policy is the one that should govern a confirmation
// landing right now.
func (p *PublicServer) doConfirm(ctx context.Context, ml store.MailingList, member store.MailingListMember) (string, error) {
	switch member.State {
	case store.MailingListMemberActive:
		return "Your subscription is already confirmed and active.", nil
	case store.MailingListMemberAwaitingApproval:
		return "Your address is already confirmed. The list owner has not yet reviewed your request.", nil
	}

	switch ml.SubscribePolicy {
	case store.MailingListSubscribeOpen:
		// ReactivateMailingListMember sets state=active and resets
		// bounce_score/last_bounce_at (REQ-MLIST-55), which is exactly
		// right here too: a bounce-suspended member regaining control of
		// their mailbox and re-confirming should not carry forward a
		// bounce history from before they proved they can receive mail
		// again.
		if err := p.Meta.ReactivateMailingListMember(ctx, member.ID); err != nil {
			return "", err
		}
		p.audit(ctx, ml, "maillist.member.subscribe_confirmed",
			fmt.Sprintf("member_id=%d", member.ID))
		return "Your subscription is confirmed. You will now receive messages from this list.", nil

	case store.MailingListSubscribeRequestApproval:
		if err := p.Meta.UpdateMailingListMemberState(ctx, member.ID, store.MailingListMemberAwaitingApproval); err != nil {
			return "", err
		}
		p.audit(ctx, ml, "maillist.member.subscribe_awaiting_approval",
			fmt.Sprintf("member_id=%d", member.ID))
		p.notifyOwnerApprovalPending(ctx, ml, member)
		return "Thanks -- your address is confirmed. The list owner will review your request before delivery starts.", nil

	default:
		// The list's policy was changed to closed after this token was
		// minted; refuse rather than silently activating against a
		// policy that no longer allows self-service subscription.
		return "Self-service subscription is currently disabled for this list.", nil
	}
}

// notifyOwnerApprovalPending enqueues a plain-text notice to ml's
// owner_principal (REQ-MLIST-62: "surfaced to the owner_principal for
// approval"). Best-effort: a failure here is logged, never returned --
// the confirmation itself has already succeeded and must not be undone
// because the notification email could not be sent.
func (p *PublicServer) notifyOwnerApprovalPending(ctx context.Context, ml store.MailingList, member store.MailingListMember) {
	if p.Blobs == nil {
		return
	}
	owner, err := p.Meta.GetPrincipalByID(ctx, ml.OwnerID)
	if err != nil {
		p.Logger.WarnContext(ctx, "maillist: resolve owner_principal failed; skipping approval-pending notification",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	addr, err := resolveMemberAddress(ctx, p.Meta, member)
	if err != nil {
		addr = fmt.Sprintf("member:%d", member.ID)
	}
	now := p.Clock.Now()
	var b strings.Builder
	b.WriteString("From: postmaster@" + ml.Domain + "\r\n")
	b.WriteString("To: " + owner.CanonicalEmail + "\r\n")
	fmt.Fprintf(&b, "Subject: [%s] subscription request awaiting approval: %s\r\n", ml.PostingAddress, addr)
	b.WriteString("Date: " + now.UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b,
		"%s confirmed a subscription request to mailing list %s.\r\n"+
			"\r\n"+
			"This list requires owner approval before delivery starts\r\n"+
			"(REQ-MLIST-62). Review and approve or reject it in the herold\r\n"+
			"admin UI (list %d, member %d), or via the admin REST PATCH\r\n"+
			"/api/v1/lists/%d/members/%d.\r\n",
		addr, ml.PostingAddress, ml.ID, member.ID, ml.ID, member.ID)
	ref, err := p.Blobs.Put(ctx, bytes.NewReader([]byte(b.String())))
	if err != nil {
		p.Logger.WarnContext(ctx, "maillist: persist approval-pending notification failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
		return
	}
	item := store.QueueItem{
		MailFrom:      "postmaster@" + ml.Domain,
		RcptTo:        owner.CanonicalEmail,
		BodyBlobHash:  ref.Hash,
		State:         store.QueueStateQueued,
		NextAttemptAt: now,
		DSNNotify:     store.DSNNotifyNever,
		CreatedAt:     now,
	}
	if _, err := p.Meta.EnqueueMessage(ctx, item); err != nil {
		p.Logger.WarnContext(ctx, "maillist: enqueue approval-pending notification failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
}

// confirmParams is the html/template data for confirmPageTemplate.
type confirmParams struct {
	ListDisplayName string
	ListAddress     string
	MemberAddress   string
	Notice          string
	CanConfirm      bool
	ActionURL       string
}

// confirmPageTemplate is the REQ-MLIST-61/63 double opt-in landing
// page: GET renders this with CanConfirm true (a fresh pending/
// suspended/unsubscribed member, an accepting subscribe policy) and an
// empty Notice; the button's form POSTs to the same URL, which performs
// the actual transition and re-renders with CanConfirm false and a
// result Notice. Static English text, mirroring publicserver.go's
// managePageTemplate (no SPA involved on this public, sessionless
// surface).
var confirmPageTemplate = template.Must(template.New("mlist-confirm").Parse(
	`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Confirm mailing list subscription</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; max-width: 36rem; margin: 4rem auto; padding: 0 1rem; color: #222; }
  h1 { font-size: 1.4rem; margin: 0 0 1rem; }
  p { line-height: 1.5; margin: 0 0 1rem; }
  dl { margin: 0 0 1.5rem; }
  dt { font-weight: 600; }
  dd { margin: 0 0 0.75rem; }
  .notice { padding: 0.75rem 1rem; background: #eef6ff; border-radius: 4px; margin-bottom: 1.5rem; }
  button { padding: 0.6rem 1.2rem; border: none; border-radius: 4px; background: #2a6fdb; color: #fff; cursor: pointer; font-size: 1rem; }
  button:hover { background: #1f57b3; }
</style>
</head>
<body>
<h1>Confirm your subscription</h1>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
<dl>
  <dt>List</dt><dd>{{.ListDisplayName}} &lt;{{.ListAddress}}&gt;</dd>
  <dt>Address</dt><dd>{{.MemberAddress}}</dd>
</dl>
{{if .CanConfirm}}
<p>Click the button below to confirm this subscription.</p>
<form method="post" action="{{.ActionURL}}">
  <button type="submit">Confirm subscription</button>
</form>
{{end}}
</body>
</html>
`))

// renderConfirmPage writes the confirm landing page for member's
// CURRENT state -- this function itself never mutates anything;
// handleConfirmPost passes the already-updated member/notice after
// performing the transition.
func (p *PublicServer) renderConfirmPage(w http.ResponseWriter, r *http.Request, ml store.MailingList, member store.MailingListMember, notice string) {
	addr, err := resolveMemberAddress(r.Context(), p.Meta, member)
	if err != nil {
		addr = "(unresolvable address)"
	}
	canConfirm := ml.SubscribePolicy != store.MailingListSubscribeClosed &&
		member.State != store.MailingListMemberActive &&
		member.State != store.MailingListMemberAwaitingApproval
	params := confirmParams{
		ListDisplayName: ml.DisplayName,
		ListAddress:     ml.PostingAddress,
		MemberAddress:   addr,
		Notice:          notice,
		CanConfirm:      canConfirm,
		ActionURL:       r.URL.RequestURI(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = confirmPageTemplate.Execute(w, params)
}
