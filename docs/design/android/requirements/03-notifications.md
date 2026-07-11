# 03 — Notifications

Native push notifications for new mail (and, in later phases, chat / calls /
invites). The *what-gets-pushed*, rules, quiet hours, and payload contract are
the Suite's `docs/design/web/requirements/25-push-notifications.md`, evaluated
server-side; this file records the native delivery and rendering. Server
prerequisite: the FCM transport, `../notes/server-prerequisites.md` (#200).

## Registration

| ID | Requirement |
|----|-------------|
| REQ-AND-PUSH-01 | The client registers with FCM, obtains a device registration token, and registers it with herold as an FCM push subscription (`../notes/server-contract.md` § Push). The subscription carries the same `notificationRules` and `quietHours` extension properties the Suite sends (Suite `REQ-PUSH-32`, server-contract § Web Push subscription). |
| REQ-AND-PUSH-02 | On an FCM token rotation the client re-registers the new token and the server destroys the stale subscription (mirrors Suite `REQ-PUSH-34`). |
| REQ-AND-PUSH-03 | The Android 13+ runtime notification permission (`POST_NOTIFICATIONS`) is requested contextually, not on first launch — after the user has engaged, matching the Suite's deferred-prompt principle (Suite `REQ-PUSH-30`). A denial is remembered and re-offered from settings. |

## Rendering

| ID | Requirement |
|----|-------------|
| REQ-AND-PUSH-10 | Notifications are posted to per-kind notification channels (Mail, Chat, Calls, Calendar, Reactions) so the user tunes importance, sound, and vibration per channel in system settings. |
| REQ-AND-PUSH-11 | Mail notifications carry the Suite payload fields (Suite `REQ-PUSH-41`): sender as title, subject + ~80-char preview as body, sender avatar, thread id as the grouping key. Multiple messages on one thread coalesce into one notification via a shared group/tag, the body reflecting the latest state (Suite `REQ-PUSH-46`). Message bodies beyond the preview are never included (Suite `REQ-PUSH-92`). |
| REQ-AND-PUSH-12 | Notifications from the same account group under a summary notification (Android notification grouping), consistent with the platform's bundled-conversation UX. |
| REQ-AND-PUSH-13 | Tapping a notification body deep-links into the app at the referenced thread (`04-system-integration.md` deep links), fetching it from the local store or syncing it if absent. |

## Actions

| ID | Requirement |
|----|-------------|
| REQ-AND-PUSH-20 | Mail notification actions match the Suite set (Suite `REQ-PUSH-41`, `REQ-PUSH-60..63`): Archive, Mark Read, Reply. Archive and Mark Read apply as optimistic actions through the outbox (`02-offline-and-sync.md` REQ-AND-SYNC-20) without opening the app; on no connectivity they queue and drain later. |
| REQ-AND-PUSH-21 | Reply uses the platform inline direct-reply (`RemoteInput`): the user types in the notification shade and the reply is submitted as an `EmailSubmission` through the outbox. Where a full compose is needed the action deep-links into compose with the quote prepared (Suite `REQ-PUSH-63`). |
| REQ-AND-PUSH-22 | Conversation shortcuts / Bubbles: mail threads (and later chat conversations) publish conversation shortcuts so notifications render in the conversation style and, where the user enables it, as Bubbles. |

## Later phases

| ID | Requirement |
|----|-------------|
| REQ-AND-PUSH-30 | Chat, calendar-invite, and incoming-call notifications follow the Suite's `REQ-PUSH-42..45` payloads and action sets when the sibling surfaces ship (`../implementation-plan.md` Phase 4+). Incoming-call notifications use a full-screen intent (ring-style), matching the Suite's `requireInteraction` high-priority treatment (Suite `REQ-PUSH-44`). |

## Out of scope

- Digest / summary-only notifications (Suite defers these; per-thread coalescing covers the dominant case).
- AI-summarised notification bodies (`../00-scope.md` NG5).
- The shortcut coach on phone (`../notes/server-contract.md` § Capabilities divergence).
