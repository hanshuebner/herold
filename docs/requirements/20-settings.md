# 20 — Settings

What's exposed in the settings panel for v1, what's deferred, and where each setting lives.

The principle: settings are the place users go to *change* defaults. They are not a feature catalogue. A long settings page is a sign of indecision — every setting is a tiny fork in product behaviour. v1 ships a deliberately small set.

## v1 scope

| ID | Requirement | Storage |
|----|-------------|---------|
| REQ-SET-01 | Theme: dark / light / follow-system. Default: follow-system. | `localStorage` per account |
| REQ-SET-02 | Default From identity. Selected from the user's `Identity` objects. | Server-side (`Identity` list ordering — a tabard convention; the first Identity is the default) |
| REQ-SET-03 | Per-identity signature. Plain text in v1; HTML signatures cut to phase 2. | Server-side via a tabard custom property on `Identity` (`signature`); see `../notes/server-contract.md` § Future suite-level capabilities — pending herold support |
| REQ-SET-04 | External-image loading default: never / per-sender / always. Default: never. | `localStorage` per account |
| REQ-SET-05 | Per-sender allow-list for external images. Maintained from the "Always load images from <sender>" affordance in the reading pane. | `localStorage` per account |
| REQ-SET-06 | Undo window duration in seconds. User-configurable. Default: 5. Range: 0–30. 0 disables Undo (sends are immediate; `EmailSubmission` is created with `sendAt: null`). For non-zero values, `EmailSubmission` is created with `sendAt = now + <window>` per `02-mail-basics.md` REQ-MAIL-14. | `localStorage` per account |
| REQ-SET-07 | Token persistence: session-only (default) vs persisted across browser restarts. The opt-in here is the inverse of `13-nonfunctional.md` REQ-SEC-01. Toggle warns explicitly: "the token stays on this device until you log out". | `localStorage` for the toggle; the token follows. |
| REQ-SET-08 | Mailing-list mute list (read-only display, with per-list "Unmute" buttons). Source: `16-mailing-lists.md` REQ-LIST-40. | `localStorage` per account |
| REQ-SET-09 | Vacation responder. Status (on/off), date range, message body. Backed by JMAP `VacationResponse` (RFC 8621 §8). Hidden if the server doesn't advertise the relevant capability. | Server-side via `VacationResponse/set` |
| REQ-SET-12 | Shortcut coach: enable / disable. Default: enabled. Disabling suppresses observation, hint generation, and server-side flushes (`23-shortcut-coach.md` REQ-COACH-71). Companion control: "Reset coach data" (REQ-COACH-72). | `localStorage` per account for the toggle; coach data itself is server-side. |
| REQ-SET-13 | Swipe action mapping (mobile / touch only). Two settings — left-swipe action (default: archive) and right-swipe action (default: snooze) — chosen from `{archive, snooze, delete, mark_read, label, none}`. See `24-mobile-and-touch.md` REQ-MOB-23..24. | `localStorage` per account |

## Layout

| ID | Requirement |
|----|-------------|
| REQ-SET-20 | The settings panel is a route, not a modal. Entered from the user-avatar menu in the top-right of the chrome. URL: `/#settings`. |
| REQ-SET-21 | The panel is split into sections (left-side nav): Account / Appearance / Mail / Privacy / Vacation / About. |
| REQ-SET-22 | Section "About" shows tabard version, the connected JMAP server URL and version, the active capability set (with a footnote showing which features are gated by which capability), and a link to the source. |

## Cut for v1

The following are intentionally cut. Each is a defensible decision; the cut keeps the surface area small.

| Setting | Why cut |
|---------|---------|
| Density (Comfortable / Compact / Cosy) | Single density in v1 (`implementation/04-simplifications-and-cuts.md`). |
| Custom keyboard shortcuts | The shortcut engine supports remapping (`requirements/10-keyboard.md` REQ-KEY-04); the settings UI for it is phase 4. The engine reads overrides from `localStorage` if they exist; v1 settings panel exposes no editor for them. |
| Filters management | Lives in `04-filters.md`'s own UI; not duplicated in settings. (The settings panel can carry a "Manage filters →" link.) |
| Labels management | Same — inline in the sidebar plus a dedicated label-management dialog from `03-labels.md`. |
| Notifications | Browser-push notifications cut entirely for v1 (NG2-adjacent: tabard-mail is online-only; push notifications require service worker). Tab-title unread count is on always; cannot be disabled. |
| Per-account preferences (multi-account) | Single account in v1 (NG3). |
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
