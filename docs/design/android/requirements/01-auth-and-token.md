# 01 — Authentication and token

How the mobile client signs in and holds credentials. The session *semantics*
(expiry, revocation, TOTP step-up, session management) are the Suite's
`docs/design/web/requirements/26-authentication-and-sessions.md` — this file
records only the native divergences. Server prerequisite: the token grant,
`../notes/server-prerequisites.md` (#199).

## Token acquisition

| ID | Requirement |
|----|-------------|
| REQ-AND-AUTH-01 | The client authenticates by obtaining a bearer token via herold's OAuth2 authorization-code grant. Sign-in opens herold's login surface in a system browser / Custom Tab (never an in-app WebView), so password + TOTP and OIDC federation (G8) work with the platform's password manager and passkey autofill. |
| REQ-AND-AUTH-02 | On redirect back to the app's registered callback, the client exchanges the authorization code for a bearer token and a refresh token, using PKCE. The client holds no long-lived password. |
| REQ-AND-AUTH-03 | The bearer token is attached as `Authorization: Bearer <token>` to every JMAP method call, blob upload/download, and the EventSource connection (`../notes/server-contract.md` § Authentication). |
| REQ-AND-AUTH-04 | On token expiry (a `401` from any endpoint), the client silently refreshes using the refresh token and retries the request once. If refresh fails, it transitions to forced-login, mirroring Suite `REQ-AS-10`. |

## Credential storage and unlock

| ID | Requirement |
|----|-------------|
| REQ-AND-AUTH-10 | Tokens are stored in Android Keystore-backed encrypted storage (EncryptedSharedPreferences / encrypted DataStore). Tokens are never written to plaintext preferences, logs, or the local database. |
| REQ-AND-AUTH-11 | App-launch unlock via BiometricPrompt (fingerprint / face) is offered and, when the user enables it in settings, required before the token is released to the network layer. A device-credential (PIN/pattern) fallback is offered per platform convention. Default: off; the user opts in. |
| REQ-AND-AUTH-12 | Credential Manager is used for the sign-in surface where the platform routes it (passkey / saved-password autofill on the Custom Tab). The app does not implement its own password entry. |

## Session lifecycle parity

| ID | Requirement |
|----|-------------|
| REQ-AND-AUTH-20 | The client honours the same session responses as the Suite: `session_expired` and `session_revoked` (`REQ-AS-10`, `REQ-AS-34`) transition to forced-login; `step_up_required` (`REQ-AS-20..23`) presents a native TOTP step-up sheet (a single 6-digit input, no password field) and resubmits the queued action once on success. |
| REQ-AND-AUTH-21 | Sign-out (`POST /logout`-equivalent for tokens) revokes the token server-side and clears local encrypted storage and the local database for that account. |
| REQ-AND-AUTH-22 | The client surfaces the account's active sessions (Suite `REQ-AS-30..33`) in settings, including this device, with remote-revoke. Revoking this device's own session signs the app out. |

## Out of scope

- Multiple accounts on one install (`../00-scope.md` NG1).
- In-app WebView login (routed to the system browser for autofill and passkey support).
- The admin scope and admin endpoints (`../00-scope.md` NG3).
