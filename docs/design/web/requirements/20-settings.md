# 20 — Settings

What's exposed in the settings panel for v1, what's deferred, and where each setting lives.

The principle: settings are the place users go to *change* defaults. They are not a feature catalogue. A long settings page is a sign of indecision — every setting is a tiny fork in product behaviour. v1 ships a deliberately small set.

## v1 scope

| ID | Requirement | Storage |
|----|-------------|---------|
| REQ-SET-01 | Theme: `dark` / `light` / `system`. Default: `system` — the suite follows the OS-level `prefers-color-scheme` and tracks live changes when the user toggles their OS theme. The setting is exposed via the `data-theme` attribute on `<html>` and read by the design system's token variants per `../architecture/06-design-system.md`. | `localStorage` per account |
| REQ-SET-02 | Default From identity. Selected from the user's `Identity` objects. | Server-side (`Identity` list ordering — a Suite-defined convention; the first Identity is the default) |
| REQ-SET-03 | Per-identity signature. Plain text in v1; HTML signatures cut to phase 2. | Server-side via a Suite-defined custom property on `Identity` (`signature`); see `../notes/server-contract.md` § Future suite-level capabilities — pending herold support |
| REQ-SET-03a | **Per-identity display name.** The Account section exposes a "Display name" text field above the existing signature editor for each `Identity`. Submitting issues `Identity/set update <id> { name }` and optimistically patches the in-memory identity cache so subsequent compose / reply flows pick up the new name immediately. The display name flows into the outbound `From: "Name" <addr>` header rendered by `Email/set` for that identity. Success surfaces `settings.saved` toast; failure surfaces `settings.saveFailed` and keeps the form dirty for retry. | Server-side via `Identity/set` (the canonical RFC 8621 `name` property). |
| REQ-SET-03b | **Per-identity profile picture.** The Account section exposes an avatar editor for each `Identity` next to its "Display name". Picking flow: the user picks an image file (PNG / JPEG / WebP / GIF, ≤ 1 MB after client-side downscaling to a 512×512 max bounding box); the suite uploads via `Blob/upload` and persists the resulting `blobId` on the identity through the extension property `Identity.avatarBlobId`. **Apply-to-all prompt:** when a user attaches an avatar to one identity and **none** of their other identities currently carries one, the suite asks "Use this picture for all your identities?" before the upload commits — accepting writes the same `avatarBlobId` to every identity in one batched `Identity/set update`. **Reuse picker:** when at least one other identity already has an avatar, the editor offers a small picker of the user's existing distinct avatars (deduplicated client-side by `avatarBlobId`) so picking the same picture across identities is a single click. **X-Face opt-in (REQ-MAIL-45):** beneath the avatar editor sits a checkbox "Add X-Face / Face headers to outbound mail from this identity"; toggling it sets `Identity.xFaceEnabled` via `Identity/set`. Default off. The picker explains the ~1 KB / message overhead. Removing the picture clears `avatarBlobId` and forces `xFaceEnabled` off; the blob is decref'd and the server GC's it on refcount=0. The same avatar is rendered on every "self" avatar surface — thread message headers (REQ-MAIL-40), the chat sidebar avatars, and the suite chrome's user-avatar menu. Avatars for *other* principals come via REQ-MAIL-44's tiered resolver. | Server-side via the `Identity` extension properties `avatarBlobId` (nullable string) and `xFaceEnabled` (bool); the blob lives in herold's blob store and is fetched through `/jmap/download/...?disposition=inline`. |
| REQ-SET-04 | External-image loading default: never / per-sender / always. Default: never. | `localStorage` per account |
| REQ-SET-05 | Per-sender allow-list for external images. Maintained from the "Always load images from <sender>" affordance in the reading pane. | `localStorage` per account |
| REQ-SET-06 | Undo window duration in seconds. User-configurable. Default: 5. Range: 0–30. 0 disables Undo (sends are immediate; `EmailSubmission` is created with `sendAt: null`). For non-zero values, `EmailSubmission` is created with `sendAt = now + <window>` per `02-mail-basics.md` REQ-MAIL-14. | `localStorage` per account |
| REQ-SET-07 | Token persistence: session-only (default) vs persisted across browser restarts. The opt-in here is the inverse of `13-nonfunctional.md` REQ-SEC-01. Toggle warns explicitly: "the token stays on this device until you log out". | `localStorage` for the toggle; the token follows. |
| REQ-SET-08 | Mailing-list mute list (read-only display, with per-list "Unmute" buttons). Source: `16-mailing-lists.md` REQ-LIST-40. | `localStorage` per account |
| REQ-SET-09 | Vacation responder. Status (on/off), date range, message body. Backed by JMAP `VacationResponse` (RFC 8621 §8). Hidden if the server doesn't advertise the relevant capability. | Server-side via `VacationResponse/set` |
| REQ-SET-12 | Shortcut coach: enable / disable. Default: enabled. Disabling suppresses observation, hint generation, and server-side flushes (`23-shortcut-coach.md` REQ-COACH-71). Companion control: "Reset coach data" (REQ-COACH-72). | `localStorage` per account for the toggle; coach data itself is server-side. |
| REQ-SET-13 | Swipe action mapping (mobile / touch only). Two settings — left-swipe action (default: archive) and right-swipe action (default: snooze) — chosen from `{archive, snooze, delete, mark_read, label, none}`. See `24-mobile-and-touch.md` REQ-MOB-23..24. | `localStorage` per account |
| REQ-SET-14 | Push notification preferences (`25-push-notifications.md` REQ-PUSH-80..84): master enable/disable + per-event-type rules (mail by category / mail by sender VIP / chat DMs vs Spaces / calendar invites / incoming calls / missed calls / reactions) + quiet-hours range + sender-VIP allow-list. Defaults per REQ-PUSH-81. | Master toggle + quiet hours: server-side via `PushSubscription/set` (per device); per-event-type rules and VIP list: client-local `localStorage`. |
| REQ-SET-15 | "Remember recently-used addresses" toggle. Controls the seen-addresses history that supplements recipient autocomplete (`02-mail-basics.md` REQ-MAIL-11e..m). Default: `true`. When the user sets it to `false`, the server immediately purges every `SeenAddress` row for the principal and stops seeding; setting it back to `true` resumes seeding (the purged history is not restored). | Server-side via the principal app-config (`internal/appconfig`). Cross-device. |
| REQ-SET-16 | "Notification sounds" master toggle. Controls in-app audio cues for incoming video calls, chat messages, and new email (`25-push-notifications.md` REQ-PUSH-95..99). Default: `true`. When `false`, no cue plays for any of the three event types. The toggle does not affect Web Push or OS-level notifications. | `localStorage` per account |
| REQ-SET-17 | "Look up sender avatars from email metadata" toggle (Privacy section). Gates REQ-MAIL-44 tier 2: when off, the suite never queries Gravatar and never decodes `Face:` / `X-Face:` headers — only the user's own identity avatars and the initial-letter fallback render. Default: server-configurable (`appconfig: avatar.email_metadata_default = true|false`); the example config ships `true`. Flipping the toggle off invalidates the in-memory + persisted Gravatar cache. The OFF→ON transition prompts a one-shot privacy confirm dialog that explains "the suite will contact Gravatar with a one-way hash of each sender's email address". | `localStorage['herold:avatar:emailMetadata']` per account; default seeded from server `appconfig`. |

## Layout

| ID | Requirement |
|----|-------------|
| REQ-SET-20 | The settings panel is a route, not a modal. Entered from the user-avatar menu in the top-right of the chrome. URL: `/#settings`. |
| REQ-SET-21 | The panel is split into sections (left-side nav): Account / Appearance / Mail / Privacy / Vacation / About. |
| REQ-SET-22 | Section "About" shows the suite version, the connected JMAP server URL and version, the active capability set (with a footnote showing which features are gated by which capability), and a link to the source. |

## Cut for v1

The following are intentionally cut. Each is a defensible decision; the cut keeps the surface area small.

| Setting | Why cut |
|---------|---------|
| Density (Comfortable / Compact / Cosy) | Single density in v1 (`implementation/04-simplifications-and-cuts.md`). |
| Custom keyboard shortcuts | The shortcut engine supports remapping (`requirements/10-keyboard.md` REQ-KEY-04); the settings UI for it is phase 4. The engine reads overrides from `localStorage` if they exist; v1 settings panel exposes no editor for them. |
| Filters management | Lives in `04-filters.md`'s own UI; not duplicated in settings. (The settings panel can carry a "Manage filters →" link.) |
| Labels management | Same — inline in the sidebar plus a dedicated label-management dialog from `03-labels.md`. |
| Notifications | Browser-push notifications cut entirely for v1 (NG2-adjacent: the suite is online-only; push notifications require service worker). Tab-title unread count is on always; cannot be disabled. |
| Per-account preferences (multi-account) | Single account in v1 (NG3). Multi-account support via external mail accounts (`02-mail-basics.md` § External mail accounts) is spec'd-but-deferred — when implemented, it brings its own settings section per REQ-MAIL-EXT-13. |
| Reading-pane location toggle (right / below / off) | One layout in v1: three-pane with reading on right (`09-ui-layout.md`). Revisit if capture data shows users want it. |
| Compose default mode (plain / HTML) | Determined per-message by what the user types; if the body has no formatting on send, plain is sent; otherwise HTML. No global toggle. |
| Auto-advance after archive (next / list) | Pinned to "next thread" by gmail convention; cut the toggle. Revisit on user feedback. |

## Settings persistence shape

Where settings live tells you how they sync across devices and browsers.

- **Server-side** (`Identity`, `VacationResponse`): syncs across devices automatically; survives a fresh browser. The right place for "what would the user expect on a new device".
- **`localStorage` per account**: tied to this browser profile + this account. Survives sessions; doesn't sync across devices. Right for UI preferences (theme, image-load defaults) where cross-device consistency isn't worth the engineering cost.
- **`sessionStorage`**: tab-scoped; cleared on tab close. Used only for ephemeral state (token in default mode, draft recovery state — see `19-drafts.md`).

The split in REQ-SET-01..09 reflects this: identity / signature / vacation are server-side; theme / image defaults / mute list / Undo window are localStorage; the auth-token toggle decides how the token is persisted.

## Cross-device defaults at first login

| ID | Requirement |
|----|-------------|
| REQ-SET-30 | First login on a fresh browser inherits server-side settings (default identity, signature, vacation responder). Local-only settings (theme, image defaults) initialise to defaults. |
| REQ-SET-31 | There is no "import settings" / "export settings" feature in v1. The user reconfigures local-only settings on each new browser. (Acceptable cost for a single user.) |

## Settings panel as discoverability

A side-effect we accept: the settings panel doubles as a sanity-check for the operator. Section "About" exposes the connected server URL + capability set, which is the fastest way to verify "is my client talking to the herold I just deployed, and which features will be available?". This is intentional — it saves a future debug step.
