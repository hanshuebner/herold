# 04 — Push

Registration on the device and how it meets herold's push dispatcher, over
either of two transports. The server side is the Suite's push contract with
an FCM transport (`docs/design/web/notes/server-contract.md` § Web Push;
`../notes/server-prerequisites.md` #200) and a UnifiedPush transport (#236)
added. Behaviour and rendering are `../requirements/03-notifications.md`.

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

### UnifiedPush registration (REQ-AND-PUSH-04)

Where Play Services is absent a distributor the user already runs carries the
push instead. The connector discovers installed distributors by querying for
the `org.unifiedpush.android.distributor.REGISTER` broadcast; the user picks
one in settings when the device holds several, and the app asks it for an
endpoint. The endpoint arrives as a broadcast to `HeroldUnifiedPushReceiver`.

The client mints its own RFC 8291 P-256 key pair and 16-byte auth secret
(`WebPushKeys` in `mobile/shared`) and registers the endpoint with
`kind: "unifiedpush"`, `keys.p256dh` and `keys.auth` - the same shape the
Suite's Web Push registration sends, which is why the server reuses one code
path for both (`internal/protojmap/push/methods.go` buildEndpointCreateRow).
The private key and the auth secret sit in the Keystore-backed store beside
the bearer token; the local database holds only a fingerprint of the
endpoint, which is what notices a rotation.

The endpoint is subject to herold's netguard egress policy at registration
and at dial time, so a self-hosted distributor on a private network is
reached only where the operator allowlisted it in `[server.push.network]`.

### Choosing the transport (REQ-AND-PUSH-05)

Automatic prefers FCM where Play Services is installed and the build carries
a Firebase project, and falls back to UnifiedPush where a distributor is
installed; settings pins either explicitly. A switch destroys the
subscription held over the previous transport in the same
`PushSubscription/set` that creates the new one, so herold holds exactly one
target per install.

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

The UnifiedPush arm of the same dispatcher takes the Web Push path with the
VAPID `Authorization` header suppressed: herold POSTs the RFC 8291
`aes128gcm` envelope to the distributor endpoint, and a 410 or 404 from the
distributor unregisters the subscription.

```
  push dispatcher
     ▼
  UnifiedPush transport  (#236: Web Push delivery, no VAPID header)
     ├ encrypt to the subscription's p256dh + auth
     ├ POST the aes128gcm envelope to the distributor endpoint
     ▼
  distributor ──▶ device ──▶ HeroldUnifiedPushReceiver
                              ├ decrypt (RFC 8188 + 8291) in mobile/shared
                              └ hand the StateChange to the same delivery
                                path the FCM service feeds
```

The distributor carries bytes it cannot read: the envelope is encrypted to
keys only this install holds, so moving off Google's transport does not move
the payload's confidentiality to whoever runs the distributor. The client's
decryption is common code, verified against the RFC 8291 section 5 and RFC
8188 section 3.1 known answers, so the iOS client inherits it.

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
Web Push and UnifiedPush have (neither a Web Push gateway nor a UnifiedPush
distributor can read the payload; Google can), payloads to the FCM transport
are kept minimal to preserve the no-bodies property
(`../notes/server-prerequisites.md` #200). Enriched content the user has opted
into is bounded to the same preview limits as the Suite.

## Later phases

Chat, calendar-invite, and incoming-call pushes reuse this path with the Suite's
`REQ-PUSH-42..45` payloads when those surfaces ship
(`../requirements/03-notifications.md` REQ-AND-PUSH-30). Incoming calls use a
full-screen intent for the ring-style UX.
