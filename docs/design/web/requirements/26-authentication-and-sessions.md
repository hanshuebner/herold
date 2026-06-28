# 26 — Authentication and sessions

How the suite handles session lifecycle, expired-session recovery, admin step-up authorization, and session management. Server-side counterparts: `../../server/requirements/02-identity-and-auth.md` REQ-AUTH-72..77, REQ-AUTH-SCOPE-01..03, REQ-AUTH-JSON-LOGIN, REQ-AUTH-JSON-LOGOUT, REQ-AUTH-JSON-WHOAMI.

## Forced re-login on session expiry

A session expires when the idle deadline elapses (REQ-AUTH-72). The suite proactively tracks the rolling idle deadline published by `GET /api/v1/auth/whoami` in `session_idle_deadline` (REQ-AUTH-75) and transitions to forced-login state before or upon receiving a 401.

| ID | Requirement |
|----|-------------|
| REQ-AS-10 | When the suite receives a 401 response with `"type": "session_expired"` or `"type": "session_revoked"` (REQ-AUTH-76) from **any** authenticated endpoint, it MUST immediately transition to forced-login state. No per-flow local handling of this 401 is permitted; the response is intercepted globally and preempts any in-progress navigation or action. |
| REQ-AS-11 | The suite reads `session_idle_deadline` from the `GET /api/v1/auth/whoami` response on every page load and stores it in application state. If the deadline has already elapsed at load time, the suite transitions to forced-login state before issuing any further authenticated requests. The suite also re-evaluates the stored deadline when the page returns from background (the `visibilitychange` event after the tab was hidden longer than the remaining idle window) and transitions to forced-login state if the deadline has passed. |
| REQ-AS-12 | Forced-login state renders a full-page or blocking modal login UI that covers every route. Navigation to any authenticated route while in forced-login state is blocked; the intended destination URL is stored so the suite can resume there after successful re-login. The blocked navigation MAY complete after re-login without the user repeating their navigation gesture. |
| REQ-AS-13 | The forced-login UI MUST NOT appear as an inline banner or per-page error on an otherwise-usable page. It MUST communicate that the session has ended ("Your session has expired — please sign in to continue.") and MUST prevent interaction with the underlying content. A `session_revoked` type presents "Your session was signed out from another device." to distinguish remote revocation from idle expiry. |
| REQ-AS-14 | After successful re-login the suite issues a fresh `GET /api/v1/auth/whoami`, refreshes the JMAP session descriptor, and resumes at the stored destination URL. An in-flight action that triggered the expiry is NOT automatically retried; the user returns to a clean application state at the intended route. |

## TOTP step-up for admin and destructive operations

Entering the admin UI or performing a mutating admin operation requires an active elevation record on the server (REQ-AUTH-74, REQ-AUTH-SCOPE-03). The suite presents a TOTP-only modal on demand; it never re-prompts for password.

| ID | Requirement |
|----|-------------|
| REQ-AS-20 | When any authenticated request returns `403` with `"type": "step_up_required"` (REQ-AUTH-74), the suite presents a TOTP step-up modal. The modal contains: a single 6-digit code input, a short explanatory prompt ("Confirm your identity to continue — enter your authenticator code"), a "Confirm" button, and a "Cancel" button. No password field. No other form content. |
| REQ-AS-21 | On navigating to any admin UI route (the `/admin` path prefix), the suite checks `elevation_expires_at` from the most recent `whoami` response (REQ-AUTH-75). If no active elevation is present (`elevation_expires_at` is null or in the past), the suite presents the TOTP step-up modal before completing the navigation. A successful step-up allows the navigation to proceed. Cancelling the modal sends the user back to the previous route. |
| REQ-AS-22 | A mutating request that returns `403 step_up_required` is queued. The step-up modal is presented; on success the original action is resubmitted once. On cancel the action is abandoned and the user receives a brief inline notice ("Action cancelled — authentication required"). |
| REQ-AS-23 | On correct TOTP code the suite POSTs to `POST /api/v1/auth/step-up`. On success (200) it updates `elevation_expires_at` in application state from the response body, closes the modal, and resubmits any queued action (REQ-AS-22). On wrong code (401) the modal re-renders with an inline error ("Incorrect code — try again"). After five consecutive wrong codes within the lockout window the server returns 429 and the modal renders the lockout countdown (formatted as "Try again in X:XX") with the code input disabled. |
| REQ-AS-24 | When `elevation_expires_at` is present and the remaining time is under 5 minutes, the suite renders a small countdown chip at a consistent location in the chrome (near the user-avatar menu, or in the admin-UI top bar). The chip shows the remaining time in `M:SS` format and disappears when the elevation expires. When the elevation expires between requests, the next admin or destructive action re-triggers the modal per REQ-AS-20. |
| REQ-AS-25 | When `403 step_up_required` carries `enroll_required: true` (REQ-AUTH-44), the step-up modal is replaced by an enrollment prompt: "Admin access requires two-factor authentication. Set up TOTP now?" with "Set up now" (routes to TOTP enrollment in Settings) and "Cancel". |

## Session management

The suite exposes the principal's active sessions in Settings so they can monitor concurrent access and revoke sessions remotely (REQ-AUTH-77).

| ID | Requirement |
|----|-------------|
| REQ-AS-30 | A "Sessions" section is added to the Settings panel (server counterpart: REQ-AUTH-77). It is accessible at `/#/settings/sessions` and linked from the Settings left-nav under the "Account" group (alongside the existing account-level items per `20-settings.md` REQ-SET-21). |
| REQ-AS-31 | The section lists every active session returned by `GET /api/v1/auth/sessions`. Each row displays: a device/browser label derived from the `user_agent` string (formatted as "Chrome on macOS" / "Firefox on Windows" / "Safari on iPhone"; unknown UAs render as "Unknown browser") using a client-side UA parser, the `last_seen_ip`, `created_at` formatted as relative time ("Started 3 days ago"), `last_seen_at` formatted as relative time ("Active 2 minutes ago"), and a "This session" chip on the row whose `is_current` is true. |
| REQ-AS-32 | Each session row has a revocation action. Rows where `is_current` is false show a "Revoke" button; clicking it issues `DELETE /api/v1/auth/sessions/{session_id}` (REQ-AUTH-77) without a confirmation step (the sessions list is already a deliberate action; the consequence is bounded to a single session). On success the row disappears. The current session's row shows a "Sign out" button instead; clicking it prompts "Sign out of this session?" before issuing the same DELETE, which also clears cookies server-side (REQ-AUTH-77) and client-side, then transitions the suite to the logged-out landing page. |
| REQ-AS-33 | The session list is loaded fresh on each route entry (`GET /api/v1/auth/sessions` on mount). A "Revoke all other sessions" action sits below the list and is disabled when only the current session exists. Clicking it prompts "Sign out of all other sessions? They cannot be re-authorised without signing in again." On confirm, the suite calls `DELETE /api/v1/auth/sessions/{session_id}` for each non-current session in the list in sequence; successfully revoked rows disappear as responses arrive. |
| REQ-AS-34 | When the suite receives a server-pushed session-revocation event for its own session (surfaced as a JMAP `state` change on a `SessionRevoked` type, or as the `session_revoked` 401 on the next request), it transitions to forced-login state per REQ-AS-10 and renders the "signed out from another device" message per REQ-AS-13. |

## Architecture notes

Implementation areas flagged for architecture-doc updates:

- `docs/design/web/architecture/02-jmap-client.md`: Add a section covering the global 401/403 interceptor in the fetch layer, elevation-state tracking in the JMAP client module, and the `whoami`-poll-on-visibility strategy for proactive expiry detection (REQ-AS-10, REQ-AS-11).
- `docs/design/web/architecture/01-system-overview.md` §Auth: Update the session-boot sequence (step 3) to reflect forced-login modal replacing the redirect to `/login`; update the storage table row for "Auth" to note the rolling idle deadline.
- `docs/design/server/architecture/01-system-overview.md` §Directory: Add a session-store subsection covering the session record lifecycle, the elevation record, the per-minute revocation-check hot-path cache, and the `POST /api/v1/auth/step-up` endpoint placement.
