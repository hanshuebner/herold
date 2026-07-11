# 04 — Push

FCM registration on the device and how it meets herold's push dispatcher. The
server side is the Suite's push contract with an FCM transport added
(`docs/design/web/notes/server-contract.md` § Web Push;
`../notes/server-prerequisites.md` #200). Behaviour and rendering are
`../requirements/03-notifications.md`.

## Registration

1. The app obtains an FCM registration token from the Firebase SDK.
2. It registers the token with herold as a push subscription of the FCM kind
   (`../notes/server-contract.md` § Push), carrying the same `notificationRules`
   and `quietHours` extension properties the Suite sends (Suite `REQ-PUSH-32`).
3. On FCM token rotation the app re-registers the new token; herold destroys the
   superseded subscription (Suite `REQ-PUSH-34` parity).

The registration call carries the bearer token
(`../requirements/01-auth-and-token.md`); a push subscription is bound to the
authenticated principal.

## Delivery path

```
  herold state change
     │
     ▼
  push dispatcher  (unchanged shared logic)
     ├ evaluate notificationRules + quietHours for the subscription
     ├ build enriched-or-minimal payload (REQ-PUSH-40..47)
     ▼
  FCM transport  (new, #200)
     ├ map payload → FCM message
     ├ POST FCM HTTP v1 (service-account auth)
     ▼
  Google FCM ──▶ device ──▶ app FirebaseMessagingService
                              ├ foregrounded: hand event to sync engine;
                              │               EventSource already reconciling
                              └ backgrounded: run a bounded reconcile pass,
                                              post the notification
```

The dispatcher's decision logic (rules, quiet hours, enriched-vs-minimal,
per-thread coalescing) is the same code that serves Web Push; FCM is only a
delivery backend beneath it.

## On-device handling

- **Foregrounded:** EventSource is the live channel; an FCM message is redundant
  for state and is used only to ensure a reconcile pass runs. The notification is
  suppressed if the relevant view is already showing the event (Suite
  `REQ-PUSH-03` parity).
- **Backgrounded / killed:** the message wakes `FirebaseMessagingService`, which
  runs a bounded reconcile pass (so the local store reflects the new mail) and
  posts the notification on the appropriate channel
  (`../requirements/03-notifications.md` REQ-AND-PUSH-10). Tap deep-links to the
  thread; notification actions (Archive, Mark Read, inline Reply) go through the
  outbox and drain even if connectivity is momentarily absent.

## Payload and privacy

Payloads carry the Suite's bounded fields — sender, subject, ~80-char preview,
thread id, ids for routing — and never full bodies (Suite `REQ-PUSH-92`).
Because FCM's transport does not give herold the end-to-end payload encryption
Web Push has (a Web Push provider cannot read the payload; Google can), payloads
to the FCM transport are kept minimal to preserve the no-bodies property
(`../notes/server-prerequisites.md` #200). Enriched content the user has opted
into is bounded to the same preview limits as the Suite.

## Later phases

Chat, calendar-invite, and incoming-call pushes reuse this path with the Suite's
`REQ-PUSH-42..45` payloads when those surfaces ship
(`../requirements/03-notifications.md` REQ-AND-PUSH-30). Incoming calls use a
full-screen intent for the ring-style UX.
