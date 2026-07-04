# 26 — Authentication and sessions

How the suite handles session lifecycle, expired-session recovery, admin step-up authorization, and session management. Server-side counterparts: `../../server/requirements/02-identity-and-auth.md` REQ-AUTH-72..78, REQ-AUTH-SCOPE-01..04, REQ-AUTH-JSON-LOGIN, REQ-AUTH-JSON-LOGOUT, REQ-AUTH-JSON-WHOAMI.

## Forced re-login on session expiry

A session expires when the idle deadline elapses (REQ-AUTH-72). The suite proactively tracks the rolling idle deadline published by `GET /api/v1/auth/whoami` in `session_idle_deadline` (REQ-AUTH-75) and transitions to forced-login state before or upon receiving a 401.

| ID | Requirement |
|----|-------------|
| REQ-AS-10 | When the suite receives a 401 response with `"type": "session_expired"` or `"type": "session_revoked"` (REQ-AUTH-76) from **any** authenticated endpoint, it MUST immediately transition to forced-login state. No per-flow local handling of this 401 is permitted; the response is intercepted globally and preempts any in-progress navigation or action. |
| REQ-AS-11 | The suite reads `session_idle_deadline` from the `GET /api/v1/auth/whoami` response on every page load and stores it in application state. If the deadline has already elapsed at load time, the suite transitions to forced-login state before issuing any further authenticated requests. The suite also re-evaluates the stored deadline when the page returns from background (the `visibilitychange` event after the tab was hidden longer than the remaining idle window) and transitions to forced-login state if the deadline has passed. |
| REQ-AS-12 | Forced-login state renders a full-page or blocking modal login UI that covers every route. Navigation to any authenticated route while in forced-login state is blocked; the intended destination URL is stored so the suite can resume there after successful re-login. The blocked navigation MAY complete after re-login without the user repeating their navigation gesture. |
| REQ-AS-13 | The forced-login UI MUST NOT appear as an inline banner or per-page error on an otherwise-usable page. It MUST communicate that the session has ended ("Your session has expired — please sign in to continue.") and MUST prevent interaction with the underlying content. A `session_revoked` type presents "Your session was signed out from another device." to distinguish remote revocation from idle expiry. |
| REQ-AS-14 | After successful re-login the suite issues a fresh `GET /api/v1/auth/whoami`, refreshes the JMAP session descriptor, and resumes at the stored destination URL. An in-flight action that triggered the expiry is NOT automatically retried; the user returns to a clean application state at the intended route. |

## TOTP at login

A principal with TOTP enrolled must supply a code to create a session (server REQ-AUTH-JSON-LOGIN, REQ-AUTH-42). A successful enrolled login yields an initially elevated session.

| ID | Requirement |
|----|-------------|
| REQ-AS-15 | The login view submits `{email, password}` to `POST /api/v1/auth/login`. When the server responds `401 {"type": "step_up_required"}` (the principal has TOTP enrolled), the view reveals a 6-digit TOTP code field and resubmits `{email, password, totp_code}` without asking the user to re-enter the password. An incorrect code re-renders the field with an inline error ("Incorrect code — try again"). On success the view stores `elevation_expires_at` from the login response (server REQ-AUTH-JSON-LOGIN) so the admin SPA's proactive elevation timer (REQ-AS-24) is armed from first load. |

## TOTP step-up for admin and destructive operations

Entering the admin UI or performing a mutating admin operation requires an active elevation record on the server (REQ-AUTH-74, REQ-AUTH-SCOPE-03). The suite presents a TOTP-only modal on demand; it never re-prompts for password. These client requirements (REQ-AS-20..25) apply equally to the consumer suite (`web/apps/suite`) and the operator admin SPA (`web/apps/admin`) served at `/admin/`: a `403 step_up_required` from any endpoint MUST drive the TOTP modal, never a re-login prompt or a raw error rendered into page content. The same modal also satisfies the self-service step-up of REQ-AUTH-78. The operator admin SPA additionally requires a *continuously* elevated session: it renders admin content only while elevation is active and re-prompts TOTP the moment elevation expires (REQ-AS-21, REQ-AS-24), not only at the next action.

| ID | Requirement |
|----|-------------|
| REQ-AS-20 | When any authenticated request returns `403` with `"type": "step_up_required"` (REQ-AUTH-74), the suite presents a TOTP step-up modal. The modal contains: a single 6-digit code input, a short explanatory prompt ("Confirm your identity to continue — enter your authenticator code"), a "Confirm" button, and a "Cancel" button. No password field. No other form content. |
| REQ-AS-21 | The admin SPA renders admin content only while an active elevation is present (`elevation_expires_at` in the future, from the most recent `whoami`/`server/status` — REQ-AUTH-75). On entry to any admin UI route (the `/admin` path prefix) with no active elevation (`elevation_expires_at` null or in the past), the suite presents the TOTP step-up modal before rendering any admin content; a successful step-up reveals the content, cancelling returns the user to the previous route. Confidential admin content is never rendered behind an absent or expired elevation. |
| REQ-AS-22 | A mutating request that returns `403 step_up_required` is queued. The step-up modal is presented; on success the original action is resubmitted once. On cancel the action is abandoned and the user receives a brief inline notice ("Action cancelled — authentication required"). |
| REQ-AS-23 | On correct TOTP code the suite POSTs to `POST /api/v1/auth/step-up`. On success (200) it updates `elevation_expires_at` in application state from the response body, closes the modal, and resubmits any queued action (REQ-AS-22). On wrong code (401) the modal re-renders with an inline error ("Incorrect code — try again"). After five consecutive wrong codes within the lockout window the server returns 429 and the modal renders the lockout countdown (formatted as "Try again in X:XX") with the code input disabled. |
| REQ-AS-24 | The admin SPA arms a proactive timer against `elevation_expires_at`, re-arming it from every fresh value in a `whoami`/`server/status`/`step-up` response (the effective deadline slides as the server extends the idle window — server REQ-AUTH-74/75), and re-checks on `visibilitychange` when the tab returns from background. The moment the elevation expires, the admin SPA MUST immediately hide all admin content and present the TOTP step-up modal, without waiting for the next request to return `403` (REQ-AS-21). A small countdown chip MAY be shown in the admin-UI top bar when under 5 minutes remain, formatted `M:SS`. In the consumer suite there is no proactive lock: an expired elevation is re-requested by the next admin or destructive action per REQ-AS-20. |
| REQ-AS-25 | When `403 step_up_required` carries `enroll_required: true` (REQ-AUTH-44), the step-up modal is replaced by an enrollment prompt: "Admin access requires two-factor authentication. Set up TOTP now?" with "Set up now" (routes to TOTP enrollment in Settings) and "Cancel". |

## Admin entry-point visibility

The admin UI is reachable only by principals who hold an admin role. Because `admin` is no longer carried as a session-cookie scope (REQ-AUTH-SCOPE-01), the client decides visibility from the principal's `roles` (REQ-AUTH-75), not from scopes.

| ID | Requirement |
|----|-------------|
| REQ-AS-26 | The admin entry point (the admin link in the suite's burger/avatar menu, and any in-suite admin navigation affordance) is shown only when the most recent `GET /api/v1/auth/whoami` response's `roles` includes `admin` or `superadmin`. Visibility is gated on role membership, not on the cookie scope set (which never carries `admin`) and not on the current elevation state: the link is shown to admin-capable principals whether or not a step-up elevation is active, and activating it triggers step-up per REQ-AS-21. Principals whose `roles` contain neither `admin` nor `superadmin` never see the admin entry point. The suite reads `roles` from the `whoami` (REQ-AS-11) it already issues on load, and from the `POST /api/v1/auth/login` response (REQ-AUTH-JSON-LOGIN) so the entry point renders correctly immediately after sign-in. |

## Self-service step-up

Operations that mint or change a long-lived credential or security setting on the principal's own account are gated by step-up when the principal has TOTP (server REQ-AUTH-78).

| ID | Requirement |
|----|-------------|
| REQ-AS-27 | API-key creation, app-password creation, external-submission-credential creation/update, password change, and TOTP disable use the standard mutating-request step-up flow (REQ-AS-22): on a `403 step_up_required` the request is queued, the TOTP modal is presented, and on success the action is resubmitted once. When the server permits the operation without elevation (the principal has no TOTP enrolled, server REQ-AUTH-78), the operation succeeds on the first request and no modal appears. The client does not pre-gate these on TOTP-enrollment state; it reacts to the server's `403 step_up_required` so the enrolled/not-enrolled decision stays server-authoritative. |

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

- `docs/design/web/architecture/02-jmap-client.md`: Add a section covering the global 401/403 interceptor in the fetch layer, elevation-state tracking in the JMAP client module, and the `whoami`-poll-on-visibility strategy for proactive expiry detection (REQ-AS-10, REQ-AS-11); include re-arming the elevation-expiry timer from the sliding `elevation_expires_at` and the admin SPA's proactive content-lock on expiry (REQ-AS-21, REQ-AS-24).
- `docs/design/web/architecture/01-system-overview.md` §Auth: Update the session-boot sequence (step 3) to reflect forced-login modal replacing the redirect to `/login`; update the storage table row for "Auth" to note the rolling idle deadline.
- `docs/design/server/architecture/01-system-overview.md` §Directory: Add a session-store subsection covering the session record lifecycle, the elevation record, the per-minute revocation-check hot-path cache, and the `POST /api/v1/auth/step-up` endpoint placement.
