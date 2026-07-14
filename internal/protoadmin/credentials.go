package protoadmin

// credentials.go implements the unified "active credentials" self-service
// view (issue #224): one REST surface enumerating everything currently
// able to authenticate as the calling principal -- web sessions
// (REQ-AUTH-77), device tokens (issue #199 IssueDeviceToken), and OAuth2
// native-client grants (issue #199 authorization-code + PKCE) -- with a
// uniform revoke-by-(kind,id) action for each.
//
//	GET    /api/v1/auth/credentials              list the caller's own credentials
//	DELETE /api/v1/auth/credentials/{kind}/{id}   revoke one credential
//
// This is additive to the existing kind-specific surfaces (GET/DELETE
// /api/v1/auth/sessions, GET/DELETE /api/v1/api-keys, POST
// /api/v1/auth/device-token, POST /oauth2/token): those remain the
// canonical per-kind CRUD. This endpoint composes a read-only summary
// across all three plus a uniform delete, delegating to the same store
// calls (TombstoneSession / DeleteAPIKey / RevokeOAuthRefreshTokenFamily)
// those surfaces already use, so revocation semantics -- in particular
// "takes effect on the very next request, not at next expiry" -- are
// identical to the kind-specific endpoints.
//
// Kind mapping to underlying storage:
//   - "session": a sessions row (authsession / REQ-AUTH-73).
//   - "device_token": an api_keys row minted by IssueDeviceToken -- the
//     device-token name prefix with a zero ExpiresAt (device tokens do
//     not expire on their own; revocation is the sole lifecycle
//     control, directory/devicetoken.go). An OAuth2 access-token row
//     shares the same name-prefix shape but always carries a non-zero
//     ExpiresAt (REQ-AND-AUTH-02's 1-hour access-token TTL) and is
//     therefore excluded here -- it is surfaced instead, once, through
//     its owning "oauth2_grant" entry below, so the same underlying
//     access token is never listed under two kinds.
//   - "oauth2_grant": the currently-active row of one refresh-token
//     rotation family (store.OAuthRefreshToken.FamilyID). Revoking it
//     revokes the whole family (RevokeOAuthRefreshTokenFamily) --
//     refresh token and any still-live paired access token together --
//     matching the issue's "revoking an OAuth2 access token revokes its
//     refresh-token family" requirement.
//
// Elevation: revocation is NOT gated behind step-up. REQ-AUTH-78's
// self-service elevation list is deliberately: create an API key,
// create an app password, create/update external-submission
// credentials, change password, disable TOTP -- every one of those
// *acquires* a new credential or *weakens* the account's protection.
// Revocation is the opposite: it only ever narrows what can currently
// act as the caller. REQ-AUTH-77's own DELETE /api/v1/auth/sessions/{id}
// already establishes this precedent for the session kind; this
// endpoint keeps device-token and OAuth2-grant revocation on the same
// unelevated footing for consistency. The trade-off: an attacker who
// has stolen a live session could in principle use it to revoke the
// victim's OTHER credentials as a denial-of-service / lock-out. Gating
// revocation behind TOTP step-up would not close that hole either (the
// same stolen session can attempt step-up, and a successful login for a
// TOTP-enrolled principal already begins elevated per REQ-AUTH-74(a) --
// so a session worth stealing is very often already elevated). Requiring
// step-up would instead cost the *legitimate* case: a user who has just
// noticed a suspicious session, possibly without their authenticator to
// hand, needs to be able to cut it off immediately. Fail-open on
// convenience, fail-closed on scope (own credentials only, enforced
// below) is the chosen trade-off.

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

// credentialKindSession / credentialKindDeviceToken / credentialKindOAuth2Grant
// are the closed enum of "kind" values accepted by the unified endpoint.
const (
	credentialKindSession     = "session"
	credentialKindDeviceToken = "device_token"
	credentialKindOAuth2Grant = "oauth2_grant"
)

// credentialDTO is the unified JSON shape for one entry in the
// active-credentials list, covering all three kinds. Fields that do not
// apply to a given kind are omitted (empty string / false zero value).
// Never carries token, cookie, or secret material -- ID is an opaque
// identifier scoped to Kind (a session_id, an api_keys row id, or an
// OAuth2 refresh-token family id), not a credential itself.
type credentialDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Label identifies the credential to the user: the device label for
	// device_token, the registered OAuth2 client's display name (falling
	// back to its client_id when the client registration has since been
	// deleted) for oauth2_grant. Empty for session -- the suite derives a
	// human-readable device/browser label from UserAgent client-side
	// (web REQ-AS-31); the server does not parse UA strings.
	Label      string `json:"label,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	LastSeenIP string `json:"last_seen_ip,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	IsCurrent  bool   `json:"is_current"`
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}

// handleListCredentials handles GET /api/v1/auth/credentials. Returns
// every session, device token, and OAuth2 grant belonging to the
// calling principal -- derived entirely from the authenticated
// principal in request context; no request input (query parameter,
// header, or body) can widen the result to another principal's rows.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	ctx := r.Context()
	now := s.clk.Now()

	items := make([]credentialDTO, 0, 8)

	sessions, err := s.store.Meta().ListSessionsByPrincipal(ctx, caller.ID, now.UnixMicro())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	currentSessionID := s.sessionIDFromRequest(r)
	for _, row := range sessions {
		items = append(items, credentialDTO{
			Kind:       credentialKindSession,
			ID:         row.SessionID,
			CreatedAt:  rfc3339(row.CreatedAt),
			LastUsedAt: rfc3339(row.LastSeenAt),
			LastSeenIP: row.LastSeenIP,
			UserAgent:  row.UserAgent,
			IsCurrent:  row.SessionID == currentSessionID,
		})
	}

	keys, err := s.store.Meta().ListAPIKeysByPrincipal(ctx, caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	// Index by ID so the oauth2_grant loop below can resolve each
	// grant's paired access-token row (AccessKeyID) for a last-used
	// timestamp without a second store round trip per grant.
	keysByID := make(map[store.APIKeyID]store.APIKey, len(keys))
	for _, k := range keys {
		keysByID[k.ID] = k
	}
	for _, k := range keys {
		label, ok := directory.DeviceTokenLabel(k.Name)
		if !ok || !k.ExpiresAt.IsZero() {
			// Not a device token, or an OAuth2 access-token row (always
			// carries a non-zero ExpiresAt) -- the latter is surfaced
			// via its owning oauth2_grant entry instead, below.
			continue
		}
		items = append(items, credentialDTO{
			Kind:       credentialKindDeviceToken,
			ID:         strconv.FormatUint(uint64(k.ID), 10),
			Label:      label,
			CreatedAt:  rfc3339(k.CreatedAt),
			LastUsedAt: rfc3339(k.LastUsedAt),
		})
	}

	grants, err := s.store.Meta().ListOAuthRefreshTokensByPrincipal(ctx, caller.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	for _, g := range grants {
		label := g.ClientID
		if c, cerr := s.dir.LookupOAuthClient(ctx, g.ClientID); cerr == nil {
			label = c.Name
		}
		var lastUsed string
		if ak, ok := keysByID[g.AccessKeyID]; ok {
			lastUsed = rfc3339(ak.LastUsedAt)
		}
		items = append(items, credentialDTO{
			Kind:       credentialKindOAuth2Grant,
			ID:         g.FamilyID,
			Label:      label,
			ClientID:   g.ClientID,
			CreatedAt:  rfc3339(g.CreatedAt),
			LastUsedAt: lastUsed,
			ExpiresAt:  rfc3339(g.ExpiresAt),
		})
	}

	writeJSON(w, http.StatusOK, pageDTO[credentialDTO]{Items: items, Next: nil})
}

// handleRevokeCredential handles DELETE /api/v1/auth/credentials/{kind}/{id}.
// Every branch verifies ownership by the calling principal before taking
// any action and responds 404 (never 403) on a mismatch, matching
// handleRevokeSession / handleDeleteAPIKey's existing anti-enumeration
// posture: a caller cannot distinguish "not yours" from "does not exist".
func (s *Server) handleRevokeCredential(w http.ResponseWriter, r *http.Request) {
	caller, _ := principalFrom(r.Context())
	ctx := r.Context()
	kind := r.PathValue("kind")
	id := r.PathValue("id")
	if id == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_id", "id path parameter is required", "")
		return
	}

	switch kind {
	case credentialKindSession:
		if !s.revokeOwnSession(w, r, caller, id) {
			return
		}

	case credentialKindDeviceToken:
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil || n == 0 {
			writeProblem(w, r, http.StatusNotFound, "not_found", "credential not found", "")
			return
		}
		keys, err := s.store.Meta().ListAPIKeysByPrincipal(ctx, caller.ID)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		var target store.APIKey
		var found bool
		for _, k := range keys {
			if uint64(k.ID) == n {
				target = k
				found = true
				break
			}
		}
		// Only a genuine device-token row (device-prefixed name, zero
		// ExpiresAt) may be revoked through this kind -- an id that
		// resolves to a plain operator-issued API key or to an OAuth2
		// access-token row is reported as not-found rather than
		// silently deleting a different kind of credential under the
		// caller's mistaken belief about what it was.
		if !found {
			writeProblem(w, r, http.StatusNotFound, "not_found", "credential not found", "")
			return
		}
		if _, ok := directory.DeviceTokenLabel(target.Name); !ok || !target.ExpiresAt.IsZero() {
			writeProblem(w, r, http.StatusNotFound, "not_found", "credential not found", "")
			return
		}
		if err := s.store.Meta().DeleteAPIKey(ctx, target.ID); err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		s.appendAudit(ctx, "auth.credential.revoke",
			fmt.Sprintf("apikey:%d", target.ID),
			store.OutcomeSuccess, "", map[string]string{
				"kind":         credentialKindDeviceToken,
				"principal_id": strconv.FormatUint(uint64(caller.ID), 10),
			})

	case credentialKindOAuth2Grant:
		grants, err := s.store.Meta().ListOAuthRefreshTokensByPrincipal(ctx, caller.ID)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		var owned bool
		for _, g := range grants {
			if g.FamilyID == id {
				owned = true
				break
			}
		}
		// Scoping the ownership check to the caller's OWN active-grant
		// list (rather than an unscoped lookup by family id) is what
		// makes this fail closed: a family id belonging to another
		// principal, or already fully revoked/rotated away, is
		// indistinguishable from an id that never existed.
		if !owned {
			writeProblem(w, r, http.StatusNotFound, "not_found", "credential not found", "")
			return
		}
		if _, err := s.store.Meta().RevokeOAuthRefreshTokenFamily(ctx, id, s.clk.Now()); err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		s.appendAudit(ctx, "auth.credential.revoke",
			fmt.Sprintf("oauth2_grant:%s", id),
			store.OutcomeSuccess, "", map[string]string{
				"kind":         credentialKindOAuth2Grant,
				"principal_id": strconv.FormatUint(uint64(caller.ID), 10),
			})

	default:
		writeProblem(w, r, http.StatusNotFound, "not_found", "unknown credential kind", "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
