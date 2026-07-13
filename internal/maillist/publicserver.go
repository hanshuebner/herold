package maillist

// publicserver.go implements the REQ-MLIST-57/58/59/61/63 public,
// unauthenticated HTTP routes on the public listener
// (docs/design/server/architecture/14-mailing-lists.md "Subscription
// endpoints (public listener)"):
//
//	GET  /lists/{id}/unsubscribe?token=...   self-service management page
//	POST /lists/{id}/unsubscribe?token=...   one-click unsubscribe (RFC 8058)
//	                                          or the page's own unsubscribe /
//	                                          resubscribe-request buttons
//	POST /lists/{id}/subscribe               subscribe request (Stage 3,
//	                                          subscribe.go)
//	GET  /lists/{id}/confirm?token=...       double opt-in landing page
//	                                          (renders only, never mutates
//	                                          -- see subscribe.go)
//	POST /lists/{id}/confirm?token=...       double opt-in confirmation
//	                                          (subscribe.go)
//
// {id} is the mailing_list row id; token is a signed member-scoped
// token (token.go) carried either in the fanned-out copy's
// List-Unsubscribe header (TokenPurposeUnsubscribe) or in a freshly
// mailed subscribe/resubscribe-confirmation email
// (TokenPurposeConfirm). No session, cookie, or API key is involved
// anywhere on this surface -- the token IS the auth, and it authorises
// exactly the (list, member) pair it was minted for (REQ-MLIST-59):
// TokenSigner.Verify already rejects a token minted for a different
// list id (the caller-supplied {id}) or a different purpose;
// resolveToken below re-checks the decoded member's own ListID against
// the addressed list as a second, independent guard, and rejects a
// TokenPurposeConfirm token presented to /unsubscribe or vice versa.
//
// GET SAFETY (REQ-MLIST-58/61): handleGet and handleConfirmGet never
// call a store mutation method. MUAs, corporate mail-gateway link
// scanners (Microsoft Safe Links, Proofpoint URL rewriting, etc.) and
// browser link-prefetch all issue GET on a URL found in an email
// BEFORE any human necessarily reads it. A GET that mutated membership
// would let a "subscription-bombing" attacker's target get silently
// auto-confirmed onto lists they never asked to join, the instant
// their mail infrastructure processes the confirmation email --
// defeating the entire point of double opt-in as a defense against
// that abuse pattern. So confirmation, like unsubscribe, only happens
// on POST: the confirm landing page (GET) renders a plain HTML page
// with one deliberate button whose form submits a POST; only that
// human-initiated POST mutates. TestPublicServer_ConfirmGetIsSafe and
// TestPublicServer_ConfirmRequiresPOST (subscribe_test.go) assert
// this directly.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// ConfirmTokenTTL bounds every REQ-MLIST-61/63 double opt-in
// TokenPurposeConfirm token minted by sendConfirmToken (subscribe.go):
// a fresh subscribe request, a resubscribe request from the management
// page (REQ-MLIST-59), or a resend of either. Short-lived by design:
// unlike the unsubscribe token (which sits inert in already-delivered
// mail for as long as that mail exists), this token is freshly minted
// and emailed the instant the member subscribes or clicks
// "resubscribe", so there is no reason for it to outlive a normal
// double-opt-in confirmation window.
const ConfirmTokenTTL = 7 * 24 * time.Hour

// problemTypeBase mirrors protoadmin's own constant (internal/protoadmin/
// problem.go); duplicated rather than imported because protoadmin already
// imports this package (mlist_dto.go) and this package must not import
// protoadmin back.
const problemTypeBase = "https://netzhansa.com/problems/"

// PublicServer serves the public, token-authorised mailing-list
// subscriber routes described above.
type PublicServer struct {
	Meta   store.Metadata
	Tokens *TokenSigner
	// Blobs persists the subscribe/resubscribe-confirmation email
	// body. Required for the subscribe and resubscribe actions; a nil
	// Blobs makes them fail (logged, no email sent) rather than
	// silently no-op-succeed.
	Blobs store.Blobs
	Clock clock.Clock
	// PublicBaseURL is used to build the confirm-link URL embedded in
	// the subscribe/resubscribe-confirmation email: the same host:port
	// GET/POST /lists/{id}/confirm below is served from (REQ-MLIST-61/63).
	PublicBaseURL string
	Logger        *slog.Logger

	// confirmLimiterOnce/confirmLimiter lazily build the Stage 3
	// per-(list,address) confirmation-send rate limiter (ratelimit.go,
	// REQ-MLIST-61 amplification concern) against p.Clock the first
	// time it is needed, rather than at construction time, so a test
	// that overwrites Clock after zero-value struct construction still
	// gets a limiter keyed to the clock it actually uses.
	confirmLimiterOnce sync.Once
	confirmLimiter     *rateLimiter
}

// NewPublicServer constructs a PublicServer. clk defaults to
// clock.NewReal and logger to slog.Default when nil.
func NewPublicServer(meta store.Metadata, tokens *TokenSigner, blobs store.Blobs, publicBaseURL string, clk clock.Clock, logger *slog.Logger) *PublicServer {
	if clk == nil {
		clk = clock.NewReal()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PublicServer{Meta: meta, Tokens: tokens, Blobs: blobs, PublicBaseURL: publicBaseURL, Clock: clk, Logger: logger}
}

// limiter returns the lazily constructed confirm-send rate limiter.
func (p *PublicServer) limiter() *rateLimiter {
	p.confirmLimiterOnce.Do(func() {
		p.confirmLimiter = newRateLimiter(p.Clock, confirmSendLimit, confirmSendWindow)
	})
	return p.confirmLimiter
}

// Handler returns the http.Handler to mount on the public listener at
// the root ("/"), or at least covering the "/lists/" prefix.
func (p *PublicServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /lists/{id}/unsubscribe", p.handleGet)
	mux.HandleFunc("POST /lists/{id}/unsubscribe", p.handlePost)
	mux.HandleFunc("POST /lists/{id}/subscribe", p.handleSubscribe)
	mux.HandleFunc("GET /lists/{id}/confirm", p.handleConfirmGet)
	mux.HandleFunc("POST /lists/{id}/confirm", p.handleConfirmPost)
	return mux
}

// problemDoc is the RFC 7807 "application/problem+json" body, mirroring
// protoadmin's own problemDoc (internal/protoadmin/problem.go).
type problemDoc struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// writeProblem renders a minimal RFC 7807 application/problem+json body.
func writeProblem(w http.ResponseWriter, status int, typeSlug, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDoc{
		Type:   problemTypeBase + typeSlug,
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

// resolveToken parses the {id} path value and ?token= query parameter,
// verifies the token against exactly that list id AND purpose
// (REQ-MLIST-59), and loads the addressed member row, re-checking that
// the decoded member actually belongs to the addressed list. Every
// failure writes its own response and returns ok=false; callers just
// return.
//
// Fail-closed and uniform: as with the VERP bounce token (bounce.go),
// a caller MUST NOT branch on which specific check failed beyond the
// coarse "list id malformed" vs "token/member invalid" split below --
// no response here ever reveals whether a given member id or address
// exists on the list (REQ-MLIST-59: "MUST NOT enumerate ... any other
// member or list"). Passing the wrong purpose (e.g. a
// TokenPurposeUnsubscribe token presented to /confirm) fails exactly
// like a tampered or expired token: TokenSigner.Verify rejects a
// purpose mismatch before this function ever sees a decoded member id.
func (p *PublicServer) resolveToken(w http.ResponseWriter, r *http.Request, purpose TokenPurpose) (store.MailingList, store.MailingListMember, bool) {
	listID, ok := mailingListIDFromPathValue(r.PathValue("id"))
	if !ok {
		writeProblem(w, http.StatusNotFound, "not_found", "list not found", "")
		return store.MailingList{}, store.MailingListMember{}, false
	}
	ml, err := p.Meta.GetMailingList(r.Context(), listID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not_found", "list not found", "")
		return store.MailingList{}, store.MailingListMember{}, false
	}

	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" || p.Tokens == nil {
		writeProblem(w, http.StatusForbidden, "invalid_token", "invalid or expired token", "")
		return store.MailingList{}, store.MailingListMember{}, false
	}
	memberID, verr := p.Tokens.Verify(purpose, ml.ID, token, p.Clock.Now())
	if verr != nil {
		writeProblem(w, http.StatusForbidden, "invalid_token", "invalid or expired token", "")
		return store.MailingList{}, store.MailingListMember{}, false
	}
	member, err := p.Meta.GetMailingListMember(r.Context(), memberID)
	if err != nil || member.ListID != ml.ID {
		// A decoded member id that no longer exists, or (should be
		// unreachable given Verify's own list-id check) belongs to a
		// different list: refuse identically to a bad signature rather
		// than distinguishing the cases in the response.
		writeProblem(w, http.StatusForbidden, "invalid_token", "invalid or expired token", "")
		return store.MailingList{}, store.MailingListMember{}, false
	}
	return ml, member, true
}

// handleGet implements the REQ-MLIST-58 self-service management page.
// SAFE: no store mutation on any path through this function.
func (p *PublicServer) handleGet(w http.ResponseWriter, r *http.Request) {
	ml, member, ok := p.resolveToken(w, r, TokenPurposeUnsubscribe)
	if !ok {
		return
	}
	p.renderManagePage(w, r, ml, member, "")
}

// handlePost implements REQ-MLIST-57 (one-click unsubscribe) and the
// management page's own unsubscribe / resubscribe-request buttons
// (REQ-MLIST-58/59).
func (p *PublicServer) handlePost(w http.ResponseWriter, r *http.Request) {
	ml, member, ok := p.resolveToken(w, r, TokenPurposeUnsubscribe)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeProblem(w, http.StatusBadRequest, "bad_request", "could not parse request body", err.Error())
		return
	}

	switch {
	case r.PostForm.Get("List-Unsubscribe") == "One-Click", r.PostForm.Get("action") == "unsubscribe":
		notice, err := p.doUnsubscribe(r.Context(), ml, member)
		if err != nil {
			p.Logger.ErrorContext(r.Context(), "maillist: public unsubscribe failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", ml.PostingAddress),
				slog.String("err", err.Error()))
			writeProblem(w, http.StatusInternalServerError, "internal_error", "could not process the request", "")
			return
		}
		member.State = store.MailingListMemberUnsubscribed
		p.renderManagePage(w, r, ml, member, notice)

	case r.PostForm.Get("action") == "resubscribe":
		notice, err := p.doResubscribeRequest(r.Context(), ml, member)
		if err != nil {
			p.Logger.ErrorContext(r.Context(), "maillist: public resubscribe request failed",
				slog.String("activity", observe.ActivitySystem),
				slog.String("list", ml.PostingAddress),
				slog.String("err", err.Error()))
			writeProblem(w, http.StatusInternalServerError, "internal_error", "could not process the request", "")
			return
		}
		p.renderManagePage(w, r, ml, member, notice)

	default:
		writeProblem(w, http.StatusBadRequest, "bad_request", "unrecognized action", "")
	}
}

// doUnsubscribe sets member to unsubscribed (REQ-MLIST-57), auditing the
// transition. Idempotent: an already-unsubscribed member is a no-op
// (no error, no duplicate audit row), so a retried one-click POST or a
// second click on the page's own button behaves identically to the
// first.
func (p *PublicServer) doUnsubscribe(ctx context.Context, ml store.MailingList, member store.MailingListMember) (string, error) {
	if member.State == store.MailingListMemberUnsubscribed {
		return "You are already unsubscribed from this list.", nil
	}
	if err := p.Meta.UpdateMailingListMemberState(ctx, member.ID, store.MailingListMemberUnsubscribed); err != nil {
		return "", err
	}
	p.audit(ctx, ml, "maillist.member.unsubscribe",
		fmt.Sprintf("member_id=%d via=public_self_service", member.ID))
	p.Logger.InfoContext(ctx, "maillist: member unsubscribed via public token URL",
		slog.String("activity", observe.ActivityUser),
		slog.String("list", ml.PostingAddress),
		slog.Uint64("member_id", uint64(member.ID)))
	return "You have been unsubscribed from this list.", nil
}

// doResubscribeRequest implements the REQ-MLIST-59/63 constraint that
// re-subscribe from the management page NEVER reactivates directly: it
// only sends a confirmation email to the member's own on-file address,
// carrying a fresh TokenPurposeConfirm token (sendConfirmToken,
// subscribe.go -- the same helper the Stage 3 subscribe endpoint uses,
// so both entry points share the confirmation-send rate limit).  No
// member row is mutated here -- state changes only once the address
// confirms via GET/POST /lists/{id}/confirm (subscribe.go).
func (p *PublicServer) doResubscribeRequest(ctx context.Context, ml store.MailingList, member store.MailingListMember) (string, error) {
	if ml.SubscribePolicy == store.MailingListSubscribeClosed {
		return "This list does not offer self-service resubscription.", nil
	}
	if member.State == store.MailingListMemberActive {
		return "You are already an active subscriber of this list.", nil
	}
	if p.Blobs == nil || p.Tokens == nil {
		return "", errors.New("maillist: resubscribe: no Blobs/Tokens wired")
	}
	addr, err := resolveMemberAddress(ctx, p.Meta, member)
	if err != nil {
		return "", fmt.Errorf("resolve member address: %w", err)
	}
	if err := p.sendConfirmToken(ctx, ml, member, addr, confirmVerbResubscribe); err != nil {
		return "", err
	}
	p.audit(ctx, ml, "maillist.member.resubscribe_requested",
		fmt.Sprintf("member_id=%d via=public_self_service", member.ID))
	return "A confirmation email has been sent to your address. Delivery resumes only once you confirm.", nil
}

// audit records a public self-service action against ml. Best-effort:
// a store error here is logged but never blocks (or unwinds) the
// action it accompanies, matching every other maillist audit helper.
func (p *PublicServer) audit(ctx context.Context, ml store.MailingList, action, msg string) {
	if p.Meta == nil {
		return
	}
	if err := p.Meta.AppendAuditLog(ctx, store.AuditLogEntry{
		At:        p.Clock.Now(),
		ActorKind: store.ActorSystem,
		ActorID:   "maillist-public",
		Action:    action,
		Subject:   fmt.Sprintf("mailing_list:%d", ml.ID),
		Outcome:   store.OutcomeSuccess,
		Message:   msg,
		Domain:    ml.Domain,
	}); err != nil {
		p.Logger.WarnContext(ctx, "maillist: public-action audit log append failed",
			slog.String("activity", observe.ActivitySystem),
			slog.String("list", ml.PostingAddress),
			slog.String("err", err.Error()))
	}
}

// manageParams is the html/template data for managePageTemplate.
type manageParams struct {
	ListDisplayName string
	ListAddress     string
	MemberAddress   string
	StateLabel      string
	Notice          string
	CanUnsubscribe  bool
	CanResubscribe  bool
	ActionURL       string
}

// managePageTemplate is the REQ-MLIST-58 self-service management page:
// list, subscribed address, membership state, and the unsubscribe /
// resubscribe actions the current state and the list's subscribe
// policy permit. Static English text; herold's admin/suite SPAs are not
// involved (REQ-MLIST-58: "Server-rendered minimal HTML ... No SPA").
var managePageTemplate = template.Must(template.New("mlist-manage").Parse(
	`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Mailing list subscription</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; max-width: 36rem; margin: 4rem auto; padding: 0 1rem; color: #222; }
  h1 { font-size: 1.4rem; margin: 0 0 1rem; }
  p { line-height: 1.5; margin: 0 0 1rem; }
  dl { margin: 0 0 1.5rem; }
  dt { font-weight: 600; }
  dd { margin: 0 0 0.75rem; }
  .notice { padding: 0.75rem 1rem; background: #eef6ff; border-radius: 4px; margin-bottom: 1.5rem; }
  form { display: inline-block; margin: 0 0.5rem 0 0; }
  button { padding: 0.6rem 1.2rem; border: none; border-radius: 4px; background: #2a6fdb; color: #fff; cursor: pointer; font-size: 1rem; }
  button:hover { background: #1f57b3; }
</style>
</head>
<body>
<h1>Mailing list subscription</h1>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
<dl>
  <dt>List</dt><dd>{{.ListDisplayName}} &lt;{{.ListAddress}}&gt;</dd>
  <dt>Subscribed address</dt><dd>{{.MemberAddress}}</dd>
  <dt>Status</dt><dd>{{.StateLabel}}</dd>
</dl>
{{if .CanUnsubscribe}}
<form method="post" action="{{.ActionURL}}">
  <input type="hidden" name="action" value="unsubscribe">
  <button type="submit">Unsubscribe</button>
</form>
{{end}}
{{if .CanResubscribe}}
<form method="post" action="{{.ActionURL}}">
  <input type="hidden" name="action" value="resubscribe">
  <button type="submit">Resubscribe</button>
</form>
{{end}}
</body>
</html>
`))

// stateLabels maps a member state to the human-readable word the
// management page shows.
var stateLabels = map[store.MailingListMemberState]string{
	store.MailingListMemberActive:           "subscribed",
	store.MailingListMemberSuspended:        "suspended (delivery paused)",
	store.MailingListMemberUnsubscribed:     "unsubscribed",
	store.MailingListMemberPending:          "pending confirmation",
	store.MailingListMemberAwaitingApproval: "confirmed, awaiting owner approval",
}

// renderManagePage writes the REQ-MLIST-58 management page for member's
// CURRENT state -- this function itself never mutates anything; callers
// that just performed a mutation (handlePost) pass the already-updated
// member/notice.
func (p *PublicServer) renderManagePage(w http.ResponseWriter, r *http.Request, ml store.MailingList, member store.MailingListMember, notice string) {
	addr, err := resolveMemberAddress(r.Context(), p.Meta, member)
	if err != nil {
		addr = "(unresolvable address)"
	}
	label := stateLabels[member.State]
	if label == "" {
		label = string(member.State)
	}
	params := manageParams{
		ListDisplayName: ml.DisplayName,
		ListAddress:     ml.PostingAddress,
		MemberAddress:   addr,
		StateLabel:      label,
		Notice:          notice,
		CanUnsubscribe:  member.State != store.MailingListMemberUnsubscribed,
		CanResubscribe: ml.SubscribePolicy != store.MailingListSubscribeClosed &&
			(member.State == store.MailingListMemberSuspended || member.State == store.MailingListMemberUnsubscribed),
		ActionURL: r.URL.RequestURI(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = managePageTemplate.Execute(w, params)
}
