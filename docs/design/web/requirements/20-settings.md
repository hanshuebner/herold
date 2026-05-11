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
| REQ-SET-03b | **Per-identity profile picture.** The Account section exposes an avatar editor for each `Identity` next to its "Display name". **Picture sources:** (a) **File picker** — choose a PNG / JPEG / WebP / GIF from disk; (b) **Camera capture** — when the browser exposes `navigator.mediaDevices.getUserMedia({ video: true })`, the editor offers a "Take photo" button that opens an in-page preview of the front-facing camera (`facingMode: 'user'`); a shutter button captures the current frame to a canvas. Permission denial / unavailable hardware falls back to the file picker silently. The MediaStream is stopped immediately after the capture (or when the user cancels) so the camera light goes off. (c) **Drag-and-drop** an image into the editor area also produces a source image. Once a source is in hand, the editor shows it under an **interactive square crop overlay**: a draggable selection square with eight resize handles, snapping to the smaller of width/height as a maximum and a 64-px minimum. The square is constrained inside the image bounds; mouse drag pans, corner/edge handles resize, and a centred reset button restores the centred max-square selection. Touch is supported (single-finger drag, pinch-resize). On confirm the cropped region is downscaled to a 512×512 max bounding box (≤ 1 MB) and uploaded via `Blob/upload`; the resulting `blobId` is persisted on the identity through the extension property `Identity.avatarBlobId`. **The default identity's avatar is writethrough-promoted to the owning `principals` row** (migration 0036: `principals.avatar_blob_hash`, `avatar_blob_size`, `xface_enabled`) so cross-user lookups (REQ-MAIL-44 tier 2) and `Principal/get` can resolve the picture without leaking the per-Identity overlay; the same writethrough applies to the display-name update (REQ-SET-03a) so any chat or thread surface that consults `principals.display_name` sees the latest value. **Apply-to-all prompt:** when a user attaches an avatar to one identity and **none** of their other identities currently carries one, the suite asks "Use this picture for all your identities?" before the upload commits — accepting writes the same `avatarBlobId` to every identity in one batched `Identity/set update`. **Reuse picker:** when at least one other identity already has an avatar, the editor offers a small picker of the user's existing distinct avatars (deduplicated client-side by `avatarBlobId`) so picking the same picture across identities is a single click. **X-Face opt-in (REQ-MAIL-45):** beneath the avatar editor sits a checkbox "Add X-Face / Face headers to outbound mail from this identity"; toggling it sets `Identity.xFaceEnabled` via `Identity/set`. Default off. The picker explains the ~1 KB / message overhead. Removing the picture clears `avatarBlobId` and forces `xFaceEnabled` off; the blob is decref'd and the server GC's it on refcount=0. The same avatar is rendered on every "self" avatar surface — thread message headers (REQ-MAIL-40), the chat sidebar avatars, and the suite chrome's user-avatar menu. Avatars for *other* principals come via REQ-MAIL-44's tiered resolver. | Server-side via the `Identity` extension properties `avatarBlobId` (nullable string) and `xFaceEnabled` (bool); the blob lives in herold's blob store and is fetched through `/jmap/download/...?disposition=inline`. |
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

## Identity maintenance (v1)

Server-side counterpart: `../../server/requirements/02-identity-and-auth.md` § Identity creation and verification (v1) (REQ-IDENT-01..91). This section specifies how the suite exposes Identity creation, editing, verification, and removal in the Settings panel.

The Account section's identity area becomes a **list of identities** (one row per identity) plus an "Add identity" button. The existing inline display of name + signature + avatar disappears in favour of per-identity edit dialogs reached from the list — the current cramped layout is replaced with explicit per-identity isolation.

### List

| ID | Requirement |
|----|-------------|
| REQ-SET-IDENT-01 | The Account section renders one row per `Identity` returned by `Identity/get`. Each row shows: a default-selector radio button (REQ-SET-IDENT-04), the avatar thumbnail (REQ-SET-03b), the display name (REQ-SET-03a) with the email in subdued type below, a verification-status chip (REQ-SET-IDENT-02), and an external-submission badge (REQ-MAIL-SUBMIT-04) when applicable. Clicking anywhere on the row (except the radio button) opens the per-identity edit dialog (REQ-SET-IDENT-10). |
| REQ-SET-IDENT-02 | **Verification-status chip.** Three states surface on each row: (a) **Verified** — no chip (silent normal); (b) **Verification pending** — yellow chip "Verification pending" + a "Resend" button (subject to REQ-IDENT-36 rate limit); (c) **Unverified** — red chip "Unverified" + a primary "Verify" button that opens the verify dialog (REQ-SET-IDENT-22). The pending state is entered the moment `Identity/set { create }` returns a row with `verifiedAt = null` AND a token is live server-side; it transitions to unverified when the token expires (server emits an `Identity/changes` push at expiry). The synthesised default identity has no chip — it is verified-by-construction (REQ-IDENT-02). |
| REQ-SET-IDENT-03 | **Sort order.** Identities are sorted: (1) the default identity first, (2) other verified identities alphabetically by email, (3) pending identities by `createdAt` descending, (4) unverified identities last. The order is stable across refreshes; the user cannot reorder via drag (default selection is the only ordering knob — REQ-SET-IDENT-04). |
| REQ-SET-IDENT-04 | **Default-identity selector.** Each row carries a radio button at the leading edge. Exactly one is selected at any time; selecting a different row issues `Identity/set update { <new>: { ... }, <old>: { ... } }` to flip the herold-namespaced extension property `Identity.isDefault` (server-side: REQ-IDENT-70, TBD). Only verified identities are selectable as default; the radio is disabled on unverified / pending rows. Switching default also flips the compose-default for future composes (REQ-MAIL-12) and is reflected in the `mail.identities` cache immediately. |
| REQ-SET-IDENT-05 | **Add-identity affordance.** A primary "Add identity" button sits above the list. Clicking opens the add-identity wizard (REQ-SET-IDENT-30). The button is hidden when the JMAP session does not advertise `https://netzhansa.com/jmap/identity-verification` (server-side opt-out). |
| REQ-SET-IDENT-06 | **Layout responsiveness.** On wide screens the list renders in a single column with full-width rows. On mobile the rows compress (display name on top line, email + chip below, kebab on the right for resend / verify / edit). The edit dialog (REQ-SET-IDENT-10) takes full screen on mobile. |

### Edit dialog (per-identity)

| ID | Requirement |
|----|-------------|
| REQ-SET-IDENT-10 | The per-identity edit dialog (extending the existing `IdentityEditDialog.svelte` used today for external-submission setup) hosts the full editor for ONE identity at a time: avatar editor (REQ-SET-03b), display-name field (REQ-SET-03a), signature editor (REQ-SET-03), X-Face opt-in (REQ-MAIL-45), and the external-submission section (REQ-MAIL-SUBMIT-01..06). The dialog is reached from the list by clicking the row or by deep-linking via `?identity=<id>` (REQ-MAIL-SUBMIT-06's re-auth path already uses this). Closing the dialog returns the user to the list with no scroll loss. |
| REQ-SET-IDENT-11 | The dialog operates on a working copy of the identity's editable fields; the user MUST click "Save" to commit. Unsaved changes prompt on close ("Discard unsaved changes?"). Optimistic save: on success the list updates immediately; on `Identity/set` rejection the dialog re-opens with the offending field highlighted. |
| REQ-SET-IDENT-12 | **Remove identity** action lives at the bottom of the dialog, visually de-emphasised, with a confirmation modal: "Remove <name> <<email>>? Mail already sent from this identity is unaffected. This cannot be undone." On confirm the dialog closes and the row disappears from the list (`Identity/set { destroy }`). The synthesised default identity has no Remove button. |
| REQ-SET-IDENT-13 | The dialog adapts based on verification state. For an unverified or pending Identity, all editable fields except the email itself are available — the user can prep avatar/signature/name before verification completes — but the external-submission section is hidden until `verifiedAt` is set (mirrors the server-side constraint at REQ-IDENT-60: an unverified Identity cannot send, so submission setup is moot). |

### Verification flow (in-dialog and standalone)

| ID | Requirement |
|----|-------------|
| REQ-SET-IDENT-20 | **Verify dialog.** Opens from the "Verify" button on an unverified row (REQ-SET-IDENT-02) and from the "Verify" inline link in the compose From picker (REQ-MAIL-12). Renders the Identity's email and a two-input form: "I clicked the link in the verification email" (no action — the link does the work) and "or enter the 6-digit code from the email" with a code-input field. The code input POSTs to `/api/v1/identities/{id}/verify` (server-side: REQ-IDENT-41); success closes the dialog and emits a success toast ("Verified <email>"). Failure shows an inline error and re-renders the field. |
| REQ-SET-IDENT-21 | **Resend** button inside the verify dialog. Subject to the server-side rate-limit (REQ-IDENT-36); on `429 Too Many Requests` the suite renders the `Retry-After` countdown inline ("Try again in 47 s"). |
| REQ-SET-IDENT-22 | **Link-redirect arrival.** When the user clicks the verification link in the email, the server validates and redirects to `/#/settings` (REQ-IDENT-40). On arrival the suite detects the just-verified state via the JMAP `Identity/changes` push and renders a success toast ("Verified <email>") — no special URL parameter is needed because the row's `verifiedAt` transition is the canonical signal. If the user is not logged in when they click the link, the server-rendered confirmation page handles the success message; the suite is bypassed entirely in that case. |
| REQ-SET-IDENT-23 | **Failure UX from email.** When the server-rendered page returns a failure (token invalid / expired / consumed — REQ-IDENT-40), the user lands on a static HTML page with a "Go to Settings" button. From Settings, the suite shows the row in its current state (unverified — token is gone) so the user can hit Resend. |

### Add-identity wizard

| ID | Requirement |
|----|-------------|
| REQ-SET-IDENT-30 | The add-identity wizard is a dialog with three steps. **Step 1 — Address.** Email + optional display name. The email is validated against the operator's domain policy (REQ-IDENT-20): an external domain that the policy rejects is surfaced with an explanatory inline error ("This server does not allow Identities for example.com — contact your administrator"). |
| REQ-SET-IDENT-31 | **Step 2 — Verification pending.** On step 1 submit, `Identity/set { create }` is issued. The wizard transitions to a pending pane showing the email, a "Resend" button (with the same rate-limit handling as REQ-SET-IDENT-21), and a code-input field. The user may close the wizard; the Identity row exists in the list in the pending state, and verification can be completed later from there (REQ-SET-IDENT-20). |
| REQ-SET-IDENT-32 | **Step 3 — Optional configuration.** After successful verification, the wizard offers a "Configure external SMTP" step IF the new Identity's domain is not hosted on this herold. Skipping leads directly to the list. For hosted-domain Identities, the wizard terminates at success (no submission step). |
| REQ-SET-IDENT-33 | **Cancel.** Cancelling step 1 closes the wizard cleanly (no row created). Cancelling at step 2 (after `Identity/set { create }` has committed) leaves the unverified row in the list; an inline notice on the cancel button warns "Closing will keep this identity in 'Verification pending' state — you can verify later from Settings". |

### Cache coherence

| ID | Requirement |
|----|-------------|
| REQ-SET-IDENT-40 | The `mail.identities` cache is updated optimistically on every `Identity/set` create / update / destroy in this flow, mirroring the existing pattern (REQ-SET-03a). The cache is the source of truth for the compose From picker (REQ-MAIL-12, REQ-MAIL-12a) and for the avatar-resolver tier 1 (REQ-MAIL-44); both surfaces MUST reflect verification changes within one push round-trip. |

## Tagged addresses (v1)

Server-side counterpart: `../../server/requirements/24-tagged-addresses.md` (REQ-TAG-01..91). This section specifies the SPA surfaces for the tagged-address feature: the in-Settings management view and the cross-references to the per-message banner (REQ-MAIL-12c in `02-mail-basics.md`).

The feature ships under the JMAP capability `https://netzhansa.com/jmap/tagged-addresses` advertised by the server when `[server.tagged_addresses].enabled = true`. When the capability is absent, the Settings section below is hidden and the per-message banner does not render.

### Management list

| ID | Requirement |
|----|-------------|
| REQ-SET-TAG-01 | A new Settings section "Tagged addresses" is added under section `Mail` (or its own top-level section if the layout grows further). The section lists every `TaggedAddressFilter` belonging to the principal (across all of their verified Identities), one row per filter. Each row shows: the base Identity's email + `+suffix` rendered together as a single chip (e.g. "alice+amazon@example.local"), the filter action ("Label as Shopping", "Label as Shopping + archive", "Label as Shopping + archive + mark read"), and an edit / delete affordance. |
| REQ-SET-TAG-02 | **Sort order**: by base Identity (alphabetical by email), then within each Identity by suffix (alphabetical). Filters are NOT user-reorderable — order does not affect routing semantics (each filter applies to its own unique `(identity, suffix)` pair). |
| REQ-SET-TAG-03 | **Edit row** opens an inline editor (or dialog on narrow viewports) exposing: the action (radio choice between `label`, `label_archive`, `label_archive_read`), the label name (text field with typeahead over existing mailboxes/labels), and a "Convert to Sieve" button. Save issues `TaggedAddressFilter/set update` and re-renders the row optimistically. The suffix and base Identity are NOT editable — to change those, the user must delete and recreate. |
| REQ-SET-TAG-04 | **Delete row** prompts "Stop auto-sorting mail to +<suffix>? Future mail will go to your inbox as normal." On confirm, issues `TaggedAddressFilter/set destroy` and also resets the per-suffix dismissal state (REQ-TAG-61 on the server is atomic with the destroy). The row disappears. Mail that has already been auto-sorted is unaffected — only future routing changes. |
| REQ-SET-TAG-05 | **Convert to Sieve** action on a row issues `POST /api/v1/tagged-address-filters/{id}/convert-to-sieve` (REQ-TAG-50). On success the row disappears from the list and a toast confirms "Filter copied to your Sieve script. Edit it in Settings → Mail → Filtering." On failure (Sieve validation error from the server), the row stays in the list and the failure surfaces inline. |
| REQ-SET-TAG-06 | **Dismissals view**. Below the filters list, a collapsed "Dismissed suffixes" subsection lists every `(base_identity, suffix)` the user has dismissed via the per-message banner (REQ-MAIL-12c). Each row offers a single "Allow prompts again" action that DELETEs the dismissal (REQ-TAG-62). The subsection is collapsed by default and exposes a count chip ("3 dismissed"); empty list hides the subsection entirely. |
| REQ-SET-TAG-07 | **Cap visibility**. When the principal is within 10 of the server-side filter cap (`max_filters_per_principal`, default 100), the section header shows an inline notice "98 of 100 filter slots used". At the cap, the Settings panel does NOT block deletions or `Convert to Sieve`; only new filter creation (via the banner) is rejected by the server, and the banner surfaces the cap-reached error inline. |

### Empty state

| ID | Requirement |
|----|-------------|
| REQ-SET-TAG-10 | An empty filters list shows a short explanatory block: "Tagged addresses let you give out variants of your email — like alice+amazon@example.local — and auto-sort future replies into folders. When you open a message addressed to a tagged variant, the suite will offer to set up a filter." This block hides itself once the user has ≥ 1 filter. |

### Interactions

| ID | Requirement |
|----|-------------|
| REQ-SET-TAG-20 | The Settings section refreshes via `TaggedAddressFilter/changes` and `Identity/changes` (the latter also fires on dismissal mutations per REQ-TAG-72). Optimistic updates on local mutations; reconciled by the next push event. |
| REQ-SET-TAG-21 | **Cross-link from the banner.** The per-message banner's filter-creating actions deep-link into the Settings section after success: a small "Manage tagged addresses" link appears in the success toast for 6 seconds. |

## Import from Gmail

Self-service entry point for the Gmail Takeout importer. Server contract is
in `../../server/requirements/16-import.md` (REQ-IMPORT-70..74).

| ID | Requirement |
|----|-------------|
| REQ-SET-IMPORT-1 | The Settings panel exposes an "Import from Gmail" entry under section `Account` (or a new `Import` section if Account grows further). The entry is hidden if the server does not advertise the `urn:herold:params:jmap:import` capability. |
| REQ-SET-IMPORT-2 | The flow is a three-step wizard: (1) **Upload** — file picker for the Takeout `.tgz` / `.zip`, capped at the server-advertised `import.max_archive_size_bytes` (REQ-IMPORT-71); (2) **Preview** — renders the dry-run plan returned by the server (counts of messages by destination mailbox, new mailboxes that would be created, count of filters / blocked addresses / forwarding addresses / send-as identities, and the first 20 untranslatable filter rules with their original Gmail query text); (3) **Confirm** — user clicks "Import" and the wizard switches to a live progress view polling `GET /api/v1/import/jobs/{id}` once per second. The user may navigate away; returning to Settings resumes the progress view. |
| REQ-SET-IMPORT-3 | Imported **forwarding addresses** are surfaced as a list with a separate "Create forwarding rules" call-to-action that hands off to the existing forwarding settings UI; the importer never creates active forwarding rules on its own (REQ-IMPORT-45 / REQ-IMPORT-74). The same display pattern applies to imported send-as identities — they appear as disabled `Identity` rows with a "Set up sending" affordance per identity. |
| REQ-SET-IMPORT-4 | The **locale** of the takeout is auto-detected by the server (REQ-IMPORT-14). The wizard surfaces the detected locale as a confirmation step ("This takeout looks like it was exported from a German Gmail account — `de-DE`. Continue?") with an override dropdown listing the locales in REQ-IMPORT-13. This is the only locale-related touchpoint the user sees; everything downstream is canonicalised. |
| REQ-SET-IMPORT-5 | Errors (parse failures, oversize archive, quota exceeded mid-import) surface inline with the actionable next step. Quota-exceeded shows the current quota and the principal admin email; oversize shows the configured limit and links to the operator-facing CLI path for larger imports. Per-message parse errors are NOT shown inline (the count would dwarf the UI); the user gets a "X messages skipped — view details" link that opens the error list paginated. |
| REQ-SET-IMPORT-6 | The wizard is keyboard-accessible (REQ-KEY-* applies). Drag-and-drop upload is offered as a convenience; the file picker is the primary path. |

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
