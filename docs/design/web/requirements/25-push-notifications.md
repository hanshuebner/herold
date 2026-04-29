# 25 — Push notifications

Browser-level push notifications for new mail, new chat messages, calendar invites, missed video calls, and reaction events. Delivered via the standard Web Push stack (RFC 8030 + RFC 8620 §7.2 `PushSubscription`); rendered by the suite's service worker; actionable directly from the notification (archive / reply / mark-read for mail, etc.) without opening the app.

Push is essential for email. A user whose mail client doesn't notify them when mail arrives is a user who keeps the tab open all the time, which mobile users can't do, which makes the suite unusable for them. Phase 1 — ships with the suite v1.

This doc covers the user-facing behaviour, the JMAP wire shape, and the service worker contract. The encryption and transport mechanics are RFC 8030 verbatim.

## Scope

| ID | Requirement |
|----|-------------|
| REQ-PUSH-01 | The suite supports browser-level push notifications via the W3C Push API + Notifications API. Permission requested at appropriate moments (REQ-PUSH-30); rendered by the suite's service worker (already registered for PWA install per `24-mobile-and-touch.md` REQ-MOB-74). |
| REQ-PUSH-02 | Notifications are first-class on phone (PWA-installed iOS Safari ≥ 16.4, Android Chrome) and on desktop (Chrome / Firefox / Safari with notifications permission). The same machinery serves all platforms. |
| REQ-PUSH-03 | The suite does not depend on Web Push for active sessions: when the tab is open and EventSource is connected, state changes flow over EventSource and the in-app indicators (tab title unread count, toast on new chat — `08-chat.md` REQ-CHAT-91) are how the user finds out. Push is for inactive / closed / backgrounded sessions. |

## What gets pushed

| ID | Requirement |
|----|-------------|
| REQ-PUSH-10 | New mail in the inbox triggers a push, subject to the user's notification rules (REQ-PUSH-50..56). The default rule: notify for Primary-category mail in Inbox; silent for Promotions / Updates / Forums; silent for spam. |
| REQ-PUSH-11 | New chat message triggers a push when the user is not actively in that conversation. DMs notify by default; Space messages notify only on @mention; muted conversations never notify (`08-chat.md` REQ-CHAT-07, REQ-CHAT-70). |
| REQ-PUSH-12 | Incoming calendar invite (iMIP REQUEST) triggers a push. iMIP CANCEL also triggers a push (the user needs to know an event was cancelled). REPLY / COUNTER / REFRESH do NOT push (those are responses to actions the user already took). |
| REQ-PUSH-13 | Incoming video call (chat call.invite signal — `08-chat.md`, `21-video-calls.md`) triggers a push immediately, with high priority and a ring-style notification UX. The push includes the call's invitation; the user accepts from the notification or opens the app. |
| REQ-PUSH-14 | Missed video call: when a video call invite times out (no accept within 30s; REQ-CALL-04), the callee gets a push noting the missed call. Once. |
| REQ-PUSH-15 | Reaction events (`02-mail-basics.md` § Reactions, `08-chat.md` REQ-CHAT-30..33): when someone reacts to the user's message, a push is sent — but coalesced (REQ-PUSH-40). Default: enabled; user can disable in settings. |
| REQ-PUSH-16 | Server-generated events (vacation auto-responses going out, send confirmations) do NOT push. Only inbound events that need user attention push. |

## Permission lifecycle

| ID | Requirement |
|----|-------------|
| REQ-PUSH-30 | The suite does NOT prompt for notification permission on first load. The browser's permission prompt is intrusive and a "denied" answer is hard to undo. Instead: the suite surfaces an in-app non-modal banner ("Get notified about new mail — [Enable]") in suite-shell chrome the first time the user has been in the app for ≥ 60 s with no current modal open. |
| REQ-PUSH-31 | Clicking "Enable" calls `Notification.requestPermission()`. Granted → register `PushSubscription` (REQ-PUSH-32). Denied → the suite remembers the denial, doesn't re-prompt for 30 days. The user can re-enable later from settings (REQ-SET-14). |
| REQ-PUSH-32 | On grant, the suite calls `serviceWorker.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: <VAPID-public-key> })`, then issues `PushSubscription/set` to herold with the resulting endpoint, p256dh key, and auth secret. The subscription is per (principal, browser-instance); each device the user logs into gets its own. |
| REQ-PUSH-33 | The VAPID public key is fetched from the JMAP session descriptor (added to `urn:ietf:params:jmap:core` capability data; see `notes/server-contract.md`). The private key lives on herold; the suite never sees it. |
| REQ-PUSH-34 | If the browser revokes the subscription (cleared site data, browser update, etc.), the next push attempt fails with 410; herold removes the dead subscription. On next the suite launch, the suite detects no active subscription and re-registers if permission is still granted. |

## Notification content

| ID | Requirement |
|----|-------------|
| REQ-PUSH-40 | Notification payloads are JSON, encrypted per RFC 8291 (Web Push encryption with the subscription's keys). Maximum payload size after encryption: ~4 KB; the suite's payload contents are bounded to ~2.5 KB plaintext to leave headroom. |
| REQ-PUSH-41 | Mail notification: `title` = sender display name; `body` = subject + " — " + first ~80 chars of preview; `icon` = sender avatar URL (proxied via the image proxy); `badge` = the suite icon; `tag` = thread id (so newer messages on the same thread replace earlier notifications); `data` = `{ kind: "mail", threadId, emailId, accountId }`; `actions` = `[Archive, Reply, Mark Read]`. |
| REQ-PUSH-42 | Chat notification: `title` = sender display name + (if Space) " in <Space name>"; `body` = first ~80 chars of message text or "[image]" / "[reaction]" placeholders; `tag` = conversation id; `data` = `{ kind: "chat", conversationId, messageId }`; `actions` = `[Reply, Mark Read]`. |
| REQ-PUSH-43 | Calendar invite notification: `title` = sender display name + " invited you to <event>"; `body` = formatted date + location; `tag` = event UID; `data` = `{ kind: "calendar-invite", emailId, eventUID }`; `actions` = `[Accept, Decline]`. |
| REQ-PUSH-44 | Incoming-call notification: `title` = "Incoming video call from <caller>"; `body` = absent; `tag` = "call-" + callId; `data` = `{ kind: "call", callId, conversationId }`; `requireInteraction: true`; `actions` = `[Accept, Decline]`. The `requireInteraction` flag keeps the notification on screen until the user acts (or the call times out, REQ-CALL-04). |
| REQ-PUSH-45 | Reaction notification: `title` = "<reactor> reacted with <emoji>"; `body` = ` "<your message subject>" `; coalesced — multiple reactions on the same message within 60 s replace earlier notifications via shared `tag`; `actions` = `[View]`. |
| REQ-PUSH-46 | Coalescing: notifications with the same `tag` replace prior notifications atomically. Used for: thread updates (multiple new messages on same thread), reactions to same message, repeated chat messages in same conversation. The body of the replacement notification reflects the latest state ("3 new messages from Alice on Re: Project X"). |
| REQ-PUSH-47 | Privacy: notification content is encrypted on the wire to the push gateway (Apple, Google, Mozilla, self-hosted). The push service sees only the encrypted blob and the VAPID claim identifying the suite's server. The user's content is not visible to the push provider. |

## Actions on notifications

The service worker handles action buttons without opening the app. The user gets one-tap mail / chat operations from their lock screen.

| ID | Requirement |
|----|-------------|
| REQ-PUSH-60 | The service worker handles the `notificationclick` event. If the user clicked an action button, the SW dispatches based on `event.action` and `event.notification.data.kind`. If they clicked the body, the SW opens the app at the relevant route. |
| REQ-PUSH-61 | Mail "Archive" action: SW issues `Email/set` removing the inbox mailbox from `mailboxIds` for the email referenced in `data.emailId`. Cookie auth attaches via `credentials: 'include'`. The notification dismisses on success; on failure, the SW re-shows it with " — failed to archive" suffix and a Retry action. |
| REQ-PUSH-62 | Mail "Mark Read" action: SW issues `Email/set` with `keywords/$seen: true`. Same success/failure UX as Archive. |
| REQ-PUSH-63 | Mail "Reply" action: SW opens the app at a compose-quick-reply URL (`/mail/compose?inReplyTo=<emailId>&quick=1`). The app's compose flow detects the URL, opens compose with quoted body, and focuses the body editor — the user types and sends. (Reply via the notification's inline-reply text input, where supported by the platform, is a future refinement; v1 is "open-app and reply".) |
| REQ-PUSH-64 | Chat "Reply" action: where the platform supports inline reply (Android Chrome's `<input type="text">` notification action), the user types directly in the notification and the SW issues `Message/set { create }` to herold. Otherwise opens the app at `/chat/<conversationId>` with the input focused. |
| REQ-PUSH-65 | Chat "Mark Read" action: SW issues `Membership/set { update: { readThrough: <messageId> } }` for the user's membership row. |
| REQ-PUSH-66 | Calendar-invite "Accept" / "Decline" actions: SW issues the iMIP REPLY path (`15-calendar-invites.md` REQ-CAL-20..24) — built `EmailSubmission/set` with the structured iCalendar REPLY. Done in the background; notification dismisses on success. |
| REQ-PUSH-67 | Call "Accept" / "Decline" actions: SW dispatches a postMessage to any open the suite tab (or opens a fresh tab) routing to the call modal. The call signaling lives over the chat WebSocket which requires an active tab anyway — the SW's role is to wake / open the app, not handle WebRTC itself. |

## Service worker scope

The service worker registered for PWA install (`24-mobile-and-touch.md` REQ-MOB-74) gains push and notification responsibilities. Its scope is small and well-bounded.

| ID | Requirement |
|----|-------------|
| REQ-PUSH-70 | The service worker registers `push` and `notificationclick` and `notificationclose` event handlers. It does NOT cache, does NOT intercept navigation requests, does NOT do background sync — it remains "network-first, no-cache" for fetches (NG2 stays). |
| REQ-PUSH-71 | The SW imports a tiny shared module exposing the JMAP request shape so action handlers can call `Email/set`, `Message/set`, `EmailSubmission/set`, `Membership/set` without re-implementing JMAP framing. The module is the same as the SPA's JMAP client at the wire level; the SW imports a slimmer subset. |
| REQ-PUSH-72 | SW updates: when a new the suite version ships, the SW lifecycle (install → waiting → activating) replaces the old SW. The user sees an in-app prompt as already specified in REQ-MOB-75 ("A new version is available — Reload"). |
| REQ-PUSH-73 | The SW logs nothing remotely (no telemetry endpoints). Errors during action handling are surfaced to the user via the failed-notification re-display path (REQ-PUSH-61). |

## Settings

| ID | Requirement |
|----|-------------|
| REQ-PUSH-80 | A "Notifications" section in settings (`20-settings.md`) exposes: master enable/disable, per-event-type toggles, quiet hours, sender-VIP list, per-Space chat-notification rules. |
| REQ-PUSH-81 | Default rules (REQ-SET-14): mail = Primary category in Inbox only; chat DMs = always; chat Spaces = on @mention only; calendar invite = always; incoming call = always; missed call = always; reaction = always. The user can edit each. |
| REQ-PUSH-82 | Quiet hours: per-account, two times — start and end. During quiet hours, no notifications surface (still delivered to the SW, but not shown). High-priority items override (incoming video call, calendar invite for an event starting soon). User can override the override per-event-type. |
| REQ-PUSH-83 | Sender VIP list: addresses or domains marked "always notify" — overrides category-based silence. Stored client-local (`localStorage`) since the list typically depends on the device's notification context (work device vs personal device). |
| REQ-PUSH-84 | Resetting notification permission ("Forget my decision"): the user can force-clear the suite's "user denied notifications" memory and re-prompt next opportunity. Useful if the user previously declined and changed their mind. Available in settings. |

## Privacy

| ID | Requirement |
|----|-------------|
| REQ-PUSH-90 | The push payload is end-to-end encrypted between herold and the user's browser per RFC 8291. The push service (Apple Push Notification service via Web Push Bridge, Google FCM, Mozilla autopush, etc.) cannot read the content. |
| REQ-PUSH-91 | The push service does see metadata: the existence of a push, its arrival time, and the VAPID `aud` claim identifying herold's origin. This is unavoidable for routing; it's the same metadata Web Push exposes to all push services for all senders. |
| REQ-PUSH-92 | The suite never includes message bodies beyond the preview-truncation in payloads. A user reading their notification sees subject + ~80-char preview, never the full message. |
| REQ-PUSH-93 | Subscription endpoint URLs are stored on herold and rotated as the browser issues new endpoints (typical lifetime: weeks to months). Old endpoints are deleted on first 410 response from the push service (REQ-PUSH-34). |
| REQ-PUSH-94 | A "Forget all my notification subscriptions" affordance in settings (REQ-PUSH-84-adjacent) issues `PushSubscription/set { destroy: <all> }` to herold and unregisters the local SW subscription. Useful for selling / decommissioning a device. |

## In-app audio cues

These are short sounds played by the SPA itself while the tab is open. They are distinct from Web Push notifications (which the OS owns). The two channels can fire for the same event when the device is busy elsewhere — that is fine.

| ID | Requirement |
|----|-------------|
| REQ-PUSH-95 | The suite plays a short audio cue for three event types while the tab is open: incoming video call (`/sounds/sound-call.wav`), incoming chat message (`/sounds/sound-chat.wav`), and new email arrival (`/sounds/sound-mail.wav`). Files are bundled with the SPA and served from `public/sounds/`. |
| REQ-PUSH-96 | An audio cue plays only when ALL of the following hold: master toggle is on (REQ-SET-16); the sender is not the user; the relevant conversation / thread is not muted (`08-chat.md` REQ-CHAT-07 for chat); the user is not currently focused on a view that already surfaces the event inline. The "already focused" check is: chat — the conversation overlay or fullscreen `/chat/<id>` is open AND `document.visibilityState === 'visible'`; mail — the route is `/mail` (or a `/mail/folder/inbox`-equivalent) AND `document.visibilityState === 'visible'`; call — never suppressed, an incoming call is high-priority and always plays. |
| REQ-PUSH-97 | Quiet hours (REQ-PUSH-82) suppress chat and mail audio cues. Incoming call cues bypass quiet hours per the existing high-priority override. |
| REQ-PUSH-98 | Browser autoplay policy: the suite catches `play()` rejections silently and continues. The first user interaction with the tab primes the audio context; cues that would have played before that are dropped (no queueing). The user is not warned. |
| REQ-PUSH-99 | The call cue plays once on `call.invite` arrival and stops as soon as the user accepts, declines, or the 30-second auto-decline window elapses (`21-video-calls.md` REQ-CALL-04). It is not looped — the visible IncomingCall modal carries the persistent affordance. |

- Digest notifications ("You have 5 new messages — see all"). Coalescing per-thread covers the dominant case; daily-digest-style summaries deferred to phase 2.
- AI-summarised notification body ("This message says you should call your bank"). NG7-adjacent; cut.
- Custom notification sounds in the Web Push payload's `sound` field. The platform default carries every Web Push notification; custom sounds at the SW level are a long tail. (In-app audio cues — played by the SPA itself while the tab is open — are in scope and specified in REQ-PUSH-95..99.)
- Notifications when the device is offline and queued for delivery on reconnect. Push services handle this opaquely; the suite relies on their behaviour, doesn't second-guess.
- Notifications for sent mail confirmation / delivery success. EventSource carries that for active sessions; nobody wants a "your message was sent" notification.
- Per-conversation push rules in chat (this conversation always vs muted). Conversation-mute already covers it (`08-chat.md` REQ-CHAT-07); a separate "always notify" doesn't add value over the inverse.
- Web Push from non-herold sources (e.g., a third-party calendar service pushing reminders). The suite speaks to herold only; cross-source push aggregation is out.
