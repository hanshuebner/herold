# 04 — System integration

Android platform surfaces the client integrates with. These are net-native
(`REQ-AND-SYS-*`) with no Suite counterpart, though several parallel the Suite's
web-platform integrations in `docs/design/web/requirements/24-mobile-and-touch.md`
§ System integrations. Perfect platform feature support is `../00-scope.md` G5.

## Share and intents

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-01 | The client registers as a share target for text and files (`ACTION_SEND` / `ACTION_SEND_MULTIPLE`): sharing to the app opens compose with the shared text in the body and shared files as attachments, honouring the inline-vs-attach distinction (`../00-scope.md` G8 / Suite `17-attachments`). |
| REQ-AND-SYS-02 | The client exposes a share action from a message (parallels Suite `REQ-MOB-50/51`): sharing a message invokes the system share sheet with the subject and a deep link to the thread. |
| REQ-AND-SYS-03 | `mailto:` links open the client's compose with the address prefilled; the client registers as a `mailto:` handler. |

## Deep links

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-10 | The client handles deep links to a thread, to compose, and to a settings section, so notifications (`03-notifications.md` REQ-AND-PUSH-13), share targets, and home-screen shortcuts route into the right destination. Deep-link routes align with the Suite's URL shapes where meaningful (thread / compose). |
| REQ-AND-SYS-11 | App Links (verified https deep links to the deployment origin) are supported so a herold thread URL opened on the device offers the app. |

## Widgets and tiles

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-20 | A home-screen widget (Glance) shows unread inbox count and the top recent threads, tapping through to the thread or compose. It renders from the local store so it is populated without a live connection. |
| REQ-AND-SYS-21 | A Quick Settings tile toggles a fast action (compose, or mute-notifications), per the user's choice. |
| REQ-AND-SYS-22 | Dynamic app shortcuts (long-press launcher icon) expose Compose and the most-recent conversations as conversation shortcuts (shared with `03-notifications.md` REQ-AND-PUSH-22). |

## Files and media

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-30 | Attachments are picked via the Storage Access Framework (document picker) and, for images, the photo picker; camera capture uses the platform capture intent. This parallels Suite `REQ-MOB-52`. |
| REQ-AND-SYS-31 | Saving an attachment or an inline image writes through SAF to a user-chosen location; "Download all attachments" includes inline images by default (`../00-scope.md`, Suite G16). |
| REQ-AND-SYS-32 | Pasting an image into compose inlines it in the body; the document/photo picker attaches (Suite G15 / `REQ-MOB-52/53`). |

## Platform conventions

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-40 | Material 3 dynamic colour (Material You) is applied; light and dark follow the system setting with a fixed-theme override (`../00-scope.md` Defaults, Suite `REQ-SET-01`). |
| REQ-AND-SYS-41 | System font scaling and display size are respected; layouts reflow at scaled sizes without fixed-pixel widths (parallels Suite `REQ-MOB-100`). |
| REQ-AND-SYS-42 | Per-app language preference (Android 13+) is supported over the client's localisation set (`../00-scope.md` Defaults). |
| REQ-AND-SYS-43 | TalkBack navigates every surface, including swipe actions and bottom sheets, with appropriate announcements (parallels Suite `REQ-MOB-101`). No action is gesture-only; each has a reachable button/menu equivalent (Suite `REQ-MOB-102`). |

## Out of scope

- Wear OS / watch companions, Android Auto (`../00-scope.md` mobile out-of-scope; revisit post-v1).
- Home-screen widget write actions beyond navigation (widgets navigate; the app acts).
