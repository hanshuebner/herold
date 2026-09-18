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
| REQ-AND-SYS-33 | An image attached to a compose that is larger than 1 MB is offered at four sizes - Small (1024 px), Medium (1600 px), Large (2048 px) and Original - bounding the longer edge. Large is the default and the last choice is remembered. The client downscales and re-encodes on the device before the upload, so the message carries the chosen size and not the camera's original (issue #341). |
| REQ-AND-SYS-34 | An image in a received message is decoded at the size it is drawn: an attachment gets a bounded thumbnail from a sampled decode of the cached blob and opens full-screen on tap, and an inline `cid:` image is handed to the reading pane's WebView re-encoded at the display width. A full-resolution decode is never used to paint a phone-sized view (Suite `REQ-ATT-20/21`, issue #341). |
| REQ-AND-SYS-35 | The app ships its own launcher icon: an adaptive icon with a monochrome layer, so the launcher can theme it, and a matching status-bar icon for notifications. |

## Platform conventions

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-40 | Material 3 dynamic colour (Material You) is applied; light and dark follow the system setting with a fixed-theme override (`../00-scope.md` Defaults, Suite `REQ-SET-01`). |
| REQ-AND-SYS-41 | System font scaling and display size are respected; layouts reflow at scaled sizes without fixed-pixel widths (parallels Suite `REQ-MOB-100`). |
| REQ-AND-SYS-42 | Per-app language preference (Android 13+) is supported over the client's localisation set (`../00-scope.md` Defaults). |
| REQ-AND-SYS-43 | TalkBack navigates every surface, including swipe actions and bottom sheets, with appropriate announcements (parallels Suite `REQ-MOB-101`). No action is gesture-only; each has a reachable button/menu equivalent (Suite `REQ-MOB-102`). |

## Diagnostics and bug reporting

The phone-side counterpart of the herold-triage browser panel (issue #407).
Transport is the existing outbox, which posts the bundle to the server's
bug-reports endpoint (issue #416), so a report queued with no connection
leaves when there is one.

| ID | Requirement |
|----|-------------|
| REQ-AND-SYS-50 | "Report a problem" is reachable from the navigation drawer, from every screen's overflow menu and from settings. It opens one sheet, whichever entry point raised it. |
| REQ-AND-SYS-51 | Shaking the device opens the same sheet. Detection runs only while the shell is resumed, counts a sample as accelerating past 13 m/s^2, declares a shake when at least four of the last 500 ms are accelerating and three quarters of that window is, and holds a cooldown so one shake is one report. A settings toggle "Shake to report" controls it, default on and remembered when turned off. |
| REQ-AND-SYS-52 | The app keeps a bounded in-memory ring of a few hundred lines fed by its own loggers (shell, sync, outbox, push, auth, the reporter). It holds no credential and no message content: every line is redacted on the way in, and a subject carried by an outbox label is dropped. The ring is never written to disk and does not outlive the process, with one exception: an uncaught exception writes a crash record to the app's private storage before the process goes, holding the trace, the thread, the build, the route the shell was on and the tail of the ring. A settings toggle "Keep diagnostic log" controls the ring, default on; turning it off empties it. After an abnormal exit the shell opens the inbox rather than restoring the screen the app died on, so a screen that crashes is not re-entered unattended. |
| REQ-AND-SYS-53 | A report captures, before the sheet opens, the window as a PNG, the route and its arguments, the account in scope and the thread, the app version and commit, the Android version and device, the reconciler's state and last error, the outbox by state with subjects dropped, the push transport and registration, and the log ring. The sheet shows the capture and sends it with no typing required: the title and the note are optional and the description is added on the desktop (`herold bug-fetch`, issue #408). The bundle goes to the account server's `POST /api/v1/bug-reports` (REQ-ADM-320) on the account's bearer token, through the outbox under the undo window, as one multipart request whose parts are the drop the triage tooling expands (`report.json` carrying the report's `title`, `report.md`, `logs.txt`, `screenshot-N.png`, and `private.json` only when the maintainer asks for the session details). A queued report reads "Bug report: <title>" in the outbox and is taken back the way a send is; a refusal leaves it failed with the server's reason. Nothing of a report is written to the user's mailboxes. A report stays open across screens: "Add another capture" leaves the sheet with the report pending - its screenshots, each capture's route and arguments and the text typed so far - and a persistent marker names it with its capture count. The next gesture or menu entry captures the window and offers to add it to the open report or to start a new one, which drops the open one after a confirmation; the sheet shows the captures as a strip, each removable. `report.json` carries `captures[]` (`{index, route, routeArguments, threadId, capturedAt}`) beside `context`, with the report's `route` and `page` staying the first capture's, `report.md` lists the captures with their routes, `screenshot-N.png` is the picture of capture N, and the log ring is the one held at send time. A pending report is kept in the app's private storage - the ring and the session details are not - so it survives process death, and it is dropped 24 hours after it was started or when the account signs out. A held crash record (REQ-AND-SYS-52) travels with the next report as `crash.txt`, as a `## Crash` section of `report.md` and as `crash` in `report.json`, and is dropped once that report is queued, so one crash is reported once. |

## Out of scope

- Wear OS / watch companions, Android Auto (`../00-scope.md` mobile out-of-scope; revisit post-v1).
- Home-screen widget write actions beyond navigation (widgets navigate; the app acts).
