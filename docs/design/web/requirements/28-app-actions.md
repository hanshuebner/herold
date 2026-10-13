# 28 — App actions

*(Added 2026-10-13.)*

How the suite offers actions contributed by registered apps, how invoking one hands the user over to the app in a new tab, and how the operator manages apps in the admin SPA. The server side — the app object, manifest, signed invocation and the thread export it builds on — is specified in `../../server/requirements/29-apps.md` (REQ-APP-*, REQ-TEXP-*). The plain thread download is REQ-MAIL-143 in `02-mail-basics.md`.

The suite never talks to an app's origin. It learns the available actions from the JMAP session, calls `AppAction/invoke` on the same-origin backend, and opens the URL the server hands back (`STANDARDS.md` §4.1).

## Where actions appear

| ID | Requirement |
|----|-------------|
| REQ-APPS-01 | App actions with point `thread.action` appear in the thread more-menu (REQ-UI-19c), after the built-in items and a separator, in the order the capability lists them. Each item shows the app-action icon (REQ-APPS-04) and the label. When the capability `https://netzhansa.com/jmap/apps` (server REQ-APP-50) is absent or lists no `thread.action`, the separator and the section are not rendered. |
| REQ-APPS-02 | The label is taken from the action's localized `label` map in the suite's current language (REQ-I18N), falling back to `en`. A tooltip shows `<label> — <appName>` so two apps with similar labels stay distinguishable. |
| REQ-APPS-03 | Actions are offered for the thread currently open in the reading pane. v1 offers no app actions in the thread list's multi-select toolbar. |
| REQ-APPS-04 | The app-action icon set is a closed list owned by the suite: `app` (generic, also the fallback), `gift`, `ticket`, `calendar-plus`, `user-plus`, `archive`, `send`, `link`, `file-plus`. Icons are rendered from the suite's own sprite; an app cannot supply image data. |
| REQ-APPS-05 | When the session `state` changes (server REQ-APP-50), the suite refetches the session and re-derives the action list; an open more-menu re-renders without a reload. |

## Invoking an action

| ID | Requirement |
|----|-------------|
| REQ-APPS-10 | Activating an action opens a new browser tab **synchronously inside the click handler** (so popup blockers treat it as user-initiated) on the same-origin handoff route `#/app-handoff`, which shows the app name and "Handing over to <appName>…" with a spinner. The suite keeps the window reference, then calls `AppAction/invoke {accountId, appId, extensionId, threadId}`. |
| REQ-APPS-11 | On `{"type": "openUrl", "url"}` the suite sets the handoff tab's `opener` to `null` and navigates it to `url`. The URL is used as given by the server (the server already checked it lies under the app's origin, REQ-APP-44); the suite still refuses any scheme other than `https:`, and `http:` only for origins on `localhost`/`127.0.0.1` in dev builds. |
| REQ-APPS-12 | On `{"type": "notice", "text"}` the suite closes the handoff tab and shows the text in an informational toast, rendered as plain text. |
| REQ-APPS-13 | On a method error the suite closes the handoff tab and shows an error toast: `appRejected` shows the app's text (plain text) prefixed with the app name; `appUnavailable` shows "<appName> is not reachable right now. Try again later."; `tooLarge` shows "This conversation is too large to hand over to <appName>."; `notFound`/`forbidden` show "This action is no longer available." and trigger a session refetch. |
| REQ-APPS-14 | If the browser blocked the tab (the `window.open` call returned `null`), the suite does not invoke the action and shows a toast explaining that pop-ups must be allowed for herold, with a "Try again" button that repeats the click path. |
| REQ-APPS-15 | While an invocation is in flight the menu item shows a spinner and is disabled; a second activation of the same action on the same thread is ignored until the first completes. Navigating away from the thread does not cancel the invocation. |
| REQ-APPS-16 | Invocation is not optimistic (`11-optimistic-ui.md`): nothing in the mailbox changes, and no undo is offered. |

## Admin SPA: Apps page

| ID | Requirement |
|----|-------------|
| REQ-APPS-30 | The admin SPA has an **Apps** page listing every app: name, `app_key`, origin, state (`pending approval`, `active`, `disabled`), a "manifest update pending" badge when `pending_manifest` is set, number of enabled principals (or "all"), and last invocation time. |
| REQ-APPS-31 | **Add app**: the admin enters an origin; the SPA calls `POST /api/v1/apps`. On success it shows the signing secret once in a copy field with the warning that it will not be shown again, then opens the review view. Validation errors from the manifest fetch (REQ-APP-20) are listed verbatim. |
| REQ-APPS-32 | **Review view**: name, `app_key`, origin, each extension with point, label (all languages), icon, endpoint (shown as the full URL), and requested scopes with a one-line plain-language explanation per scope (`thread.raw`: "Receives every message of a conversation, including attachments, when a user picks this action on it"). Dropped extensions with unknown points are listed as warnings. **Approve** sends the digest shown (REQ-APP-11); a `409` reloads the view with a notice that the manifest changed. |
| REQ-APPS-33 | **Manifest update**: when `pending_manifest` is set, the review view shows the approved and pending manifests side by side with added/removed extensions, scopes and endpoint changes highlighted, and an **Approve update** action. **Refresh manifest** triggers `POST /api/v1/apps/{id}/refresh`. |
| REQ-APPS-34 | **Access**: a toggle "All users" plus a principal picker for the explicit set (REQ-APP-13). |
| REQ-APPS-35 | **Rotate secret**, **Disable/Enable** and **Remove** are available on the app's page. Rotate and Remove ask for confirmation; Rotate shows the new secret once, as in REQ-APPS-31. All of them require elevation (REQ-AUTH-74) and use the SPA's existing step-up flow. |
| REQ-APPS-36 | When the apps REST surface returns `503` for a missing data key (server REQ-APP-14), the page shows that apps need `[server.secrets].data_key_ref` configured and links the operations doc. |

## Strings and tests

| ID | Requirement |
|----|-------------|
| REQ-APPS-40 | Every string above exists in `en` and `de` (REQ-I18N); German strings use real umlauts. |
| REQ-APPS-41 | Vitest + `@testing-library/svelte` cover: action-list derivation from the capability (language fallback, unknown icon fallback, empty list), the invoke state machine (openUrl, notice, each error, blocked pop-up), and the admin review/diff rendering. |
| REQ-APPS-42 | Puppeteer verification against `scripts/dev-instance.sh` with the fake app (server REQ-APP-70..71): alice opens the seeded thread, picks the fake action from the more-menu, a new tab lands on the fake app's `/opened/<id>` page showing the received message count; the notice and error variants show their toasts; the thread download (REQ-MAIL-143) yields a zip with both messages. Screenshots of the menu, the handoff tab and the admin review view accompany the change. |
