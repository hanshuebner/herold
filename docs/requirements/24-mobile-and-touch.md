# 24 — Mobile and touch

Tabard's mobile web experience is first-class. Phones and tablets get a complete, idiomatic UI — not a desktop fallback. Native iOS / Android applications remain out of scope (`00-scope.md` NG1 amended); a Progressive Web App (PWA) installable from Safari / Chrome is the deployment shape.

Touch input is treated as a peer of mouse and keyboard. Gestures (swipe, long-press, pinch) coexist with the desktop conventions; the same actions are reachable across all input modalities though the affordances differ.

This doc describes how the suite-shell layout, the mail app, the chat panel, and component patterns adapt across breakpoints, plus the touch-specific interactions and the PWA shell.

## Scope

| ID | Requirement |
|----|-------------|
| REQ-MOB-01 | Mobile web is in scope as a first-class experience for both phone (≥320 px viewport) and tablet (≥768 px viewport). The desktop pane-based layout is one of three responsive variants, not the canonical form. |
| REQ-MOB-02 | Native iOS and Android applications are NOT in scope (`00-scope.md` NG1). The PWA delivers app-icon + standalone-window UX without a native build pipeline. |
| REQ-MOB-03 | Touchscreen-equipped desktops (Surface, MacBook with touch displays in the future, Linux laptops with touch panels) are treated as desktop with touch enabled — they get the desktop layout but accept touch gestures. |
| REQ-MOB-04 | Foldables (Galaxy Z Fold-class devices) are treated by the responsive breakpoints based on current viewport size. No foldable-specific split-screen handling in v1. |

## Breakpoints

Three layout variants. Boundaries by viewport width:

| Variant | Width | Primary devices |
|---------|-------|-----------------|
| **Phone** | < 768 px | iPhone 13/14/15/16, Pixel, mid-range Android |
| **Tablet** | 768 px ≤ w < 1280 px | iPad mini / Air / Pro portrait, Android tablets, small laptop windows |
| **Desktop** | ≥ 1280 px | Laptops, desktops, iPad Pro landscape with external keyboard |

| ID | Requirement |
|----|-------------|
| REQ-MOB-10 | Layout adapts at the breakpoints above. Transitions between variants are reflow-only (no jarring restructure animation); switching window size on desktop crosses the boundary smoothly. |
| REQ-MOB-11 | Tablet portrait (768–959 px) and tablet landscape (960–1279 px) are both in the tablet variant; the variant adapts further within itself for whether it shows two panes or three (REQ-MOB-30..33). |
| REQ-MOB-12 | The active variant is computed from `window.innerWidth` (and updates on `resize` / `visualViewport` resize). The variant choice is plumbed to layout components via a Svelte context; components don't read window size directly. |

## Touch interactions

| ID | Requirement |
|----|-------------|
| REQ-MOB-20 | Minimum touch-target size is 44 × 44 CSS pixels on phone and tablet (Apple HIG / Material 48dp ≈ ours 44px when DPR is 1). Hit areas may extend beyond visible bounds via padding when the visual element is smaller. |
| REQ-MOB-21 | Tap = click. Activated on `pointerup` within the element's bounds with no significant pointer movement (< 10 px drift). |
| REQ-MOB-22 | Long-press = context menu. 500 ms hold opens the per-message context menu (`02-mail-basics.md` REQ-MAIL-130..142), same items as the desktop three-dot menu. Long-press triggers a haptic tick on platforms that expose `navigator.vibrate`. |
| REQ-MOB-23 | Swipe-left on a thread-list row reveals a quick-action affordance for archive (default; configurable in settings — see REQ-MOB-24). Swiping past 50% of the row width and releasing commits the action. Releasing before 50% snaps back. The gesture exposes Undo via the standard toast (REQ-OPT-10). |
| REQ-MOB-24 | Swipe-right on a thread-list row reveals a configurable quick-action (default: snooze). Same threshold semantics as left-swipe. The configurable mappings live in settings (`20-settings.md` REQ-SET-13). |
| REQ-MOB-25 | Pull-to-refresh on the thread list: pulling down past 60 px and releasing triggers `Email/changes` for the active view. Visual indicator: a circular progress ring at the top of the list. |
| REQ-MOB-26 | Pinch on an HTML message body (inside the iframe) is permitted — the iframe lets the user zoom emails with tiny fonts. The reading pane around the iframe does not respond to pinch. |
| REQ-MOB-27 | Native text selection is preserved: long-press on selectable text (subject, body, sender name) uses the platform's native selection menu (Copy / Look up / Share). Tabard does not intercept text selection. |
| REQ-MOB-28 | Two-finger horizontal swipe in the reading pane: when a thread is open, swipe-left moves to the next thread in the list, swipe-right to the previous. Same semantics as `j` / `k` on desktop. |
| REQ-MOB-29 | Hover-only affordances (REQ-UI-13: hover-revealed checkbox) are surfaced differently on touch: long-press to enter selection mode (then taps select rows; matches the iOS Mail / Gmail mobile pattern). |

## Layout — phone (< 768 px)

One pane visible at a time. Navigation is stack-based: the user pushes onto the stack to drill in, pops to back out.

| ID | Requirement |
|----|-------------|
| REQ-MOB-30 | The phone layout has one root pane: the active app's main view (mail thread list, chat conversation list, etc.). Navigation drawers, search, settings, and modal flows push onto the navigation stack. |
| REQ-MOB-31 | Top app bar: full-width, fixed at the top of the active pane. Contains, left-to-right: hamburger / back arrow (context-sensitive), the page title, an overflow menu (3-dot) for less-frequent actions. The global bar's search becomes a search icon that, when tapped, expands the search input full-width over the app bar. |
| REQ-MOB-32 | Bottom navigation: a fixed bar with up to 3 items — Mail, Chat, Settings (or whichever apps are deployed). Tapping switches the suite-app context, replacing the active pane. The chat panel doesn't apply on phone (the panel pattern is desktop-only); chat is one of the bottom-nav apps instead. |
| REQ-MOB-33 | Floating action button (FAB) for compose: bottom-right, anchored above the bottom navigation. Tap opens compose full-screen. The FAB is hidden when an action is contextually impossible (e.g., during a video call). |
| REQ-MOB-34 | Sidebar (mailbox tree, labels) opens as a left drawer via the hamburger button. Tap outside the drawer or swipe-back to dismiss. The drawer is full-height and overlays the active pane with a scrim. |
| REQ-MOB-35 | Compose: full-screen modal. The recipient row, subject, and body fill the viewport vertically. The formatting toolbar is collapsed by default; a "+" button (or formatting icon) expands it inline above the virtual keyboard. Send is a top-app-bar action. |
| REQ-MOB-36 | Reading pane: full-width. The thread accordion still applies (`09-ui-layout.md` REQ-UI-20..25); message density tightens (smaller avatars, less padding). The thread-action toolbar (REQ-UI-19a..19d) sits in the top app bar; the per-message context menu is reached via long-press or a per-message overflow. |
| REQ-MOB-37 | Pickers (label, snooze, From-identity, search-suggestions): bottom sheets, dragged up from the bottom edge. Drag-down dismisses; tap outside the sheet (on the scrim) dismisses. |
| REQ-MOB-38 | Toasts: bottom-anchored ABOVE the bottom navigation (so they don't get clipped). |
| REQ-MOB-39 | The shortcut-coach strip (`23-shortcut-coach.md`) is **hidden** on phone — keyboard shortcuts don't apply when there's no keyboard. |

## Layout — tablet (768 px ≤ w < 1280 px)

Two-pane in portrait, three-pane in landscape. The split shifts based on viewport width within the tablet range.

| ID | Requirement |
|----|-------------|
| REQ-MOB-40 | Tablet portrait (768–959 px): two panes simultaneously visible — sidebar + thread list, OR thread list + reading pane. The user toggles between "list-focus" (sidebar + list visible, reading-pane hidden) and "reading-focus" (list + reading-pane visible, sidebar collapsed to rail). The default mode is list-focus when no thread is open; opens a thread → switches to reading-focus. |
| REQ-MOB-41 | Tablet landscape (960–1279 px): three panes simultaneously, identical to desktop layout in that respect. The chat panel is collapsible to keep the three-pane proportions usable. |
| REQ-MOB-42 | The sidebar collapse state on tablet is toggleable via the hamburger; collapsed = rail-only (icons), expanded = full sidebar with system mailboxes and label tree. Persists per-account. |
| REQ-MOB-43 | Compose on tablet: opens as a modal centered on screen, not full-screen. Width: 80% of viewport, capped at 720 px. Stacking still applies (cap at 3 windows; below that the older windows minimise as a bottom strip). |
| REQ-MOB-44 | The chat panel on tablet portrait: bottom sheet (initially collapsed to a 48 px-tall bar showing unread + presence). Tap or swipe-up expands. On tablet landscape: side panel anchored right. |
| REQ-MOB-45 | The shortcut-coach strip is **shown** on tablet — many users pair tablets with hardware keyboards (Smart Keyboard, Bluetooth). The strip auto-hides if no keyboard interaction has been detected for 5 minutes (a heuristic that the user is touch-only this session). |
| REQ-MOB-46 | Pickers on tablet: popover anchored to the trigger (matching desktop). Bottom-sheet variants are reachable by long-press on the trigger (e.g., long-press the label-picker button → bottom sheet for thumb-friendly use). |
| REQ-MOB-47 | Toasts on tablet: bottom-centre, same as desktop. |

## Layout — desktop (≥ 1280 px)

The full layout per `09-ui-layout.md`. No touch-specific changes beyond preserving the gestures that work alongside mouse (long-press to enter selection mode also works; swipe gestures only fire on `pointerType === "touch"`).

## System integrations

| ID | Requirement |
|----|-------------|
| REQ-MOB-50 | iOS Safari Web Share API: a "Share" affordance in the per-message overflow menu uses `navigator.share` to invoke the system share sheet (passing subject + a URL like `https://mail.example.com/thread/<id>` if installable, or a `mailto:` fallback otherwise). |
| REQ-MOB-51 | Android intents: same `navigator.share` path; Android's share sheet is the same web API. |
| REQ-MOB-52 | File picker: attachment + paste + drag-and-drop on desktop; `<input type="file" accept="...">` on mobile, optionally with `capture` for camera direct-shoot. |
| REQ-MOB-53 | Clipboard: paste of an image works on iOS Safari and Chrome Android via the standard Clipboard API. |
| REQ-MOB-54 | System back button (Android): pops the current navigation stack entry — close picker → close compose → leave thread → close drawer. Tabard listens to `popstate` and manages the History API to align with this behaviour. |
| REQ-MOB-55 | iOS swipe-back gesture: same nav-stack pop. Tabard's History API integration handles both. |
| REQ-MOB-56 | Status bar / safe areas: tabard respects `env(safe-area-inset-*)` so notches, home indicators, and rounded-corner cutouts don't clip content. The PWA manifest declares `theme_color` matching the active light/dark theme. |

## Virtual keyboard

When the on-screen keyboard appears, the viewport effectively shrinks. Layout must reflow without losing the focused input.

| ID | Requirement |
|----|-------------|
| REQ-MOB-60 | Tabard listens to `visualViewport.resize` events and adjusts layout: bottom-anchored elements (compose toolbar, FAB, bottom navigation) reposition above the keyboard. The currently focused input MUST remain visible — never obscured by the keyboard. |
| REQ-MOB-61 | Compose's body editor adjusts its scroll so the cursor row is in the visible viewport when the keyboard opens. |
| REQ-MOB-62 | The chat panel's compose input on tablet portrait: when focused, the bottom-sheet panel auto-expands to give the input room above the keyboard. |
| REQ-MOB-63 | Tabard does NOT prevent IME composition or autocorrect (typing Japanese, autocorrect on iOS, swipe-typing on Android all work as the platform provides). Compose's ProseMirror editor handles `compositionstart`/`compositionend` correctly. |

## PWA install

| ID | Requirement |
|----|-------------|
| REQ-MOB-70 | Tabard ships a Web App Manifest (`/manifest.webmanifest`) referenced from every HTML entry point. Fields: `name`, `short_name`, `start_url`, `scope`, `display: "standalone"`, `theme_color` (light + dark variants via media queries), `background_color`, `icons` (192×192, 512×512 PNG plus 1024×1024 maskable), `categories: ["productivity", "communication"]`. |
| REQ-MOB-71 | App icons follow the design-system colour palette and use a tabard mark — design TBD; placeholder geometric icon ships with v1. |
| REQ-MOB-72 | Tabard does NOT prompt the user to install. The install affordance is the platform's own (Safari "Add to Home Screen"; Chrome "Install app"). Tabard does NOT show a custom install banner. |
| REQ-MOB-73 | Once installed, the PWA opens in standalone display mode (no browser chrome). The suite-shell global bar takes over the role of "title / app chrome". |
| REQ-MOB-74 | A minimal service worker is registered for PWA installability — its only job is to satisfy the install criterion (modern browsers require at least one fetch handler for PWA install). The worker uses **network-first, no-cache** semantics — every request goes through to the network; nothing is cached for offline use. The network-first stance preserves NG2 (no offline mode); we ship the worker only to enable installability, not to mediate request behaviour. |
| REQ-MOB-75 | The service worker handles app updates: when a new version of tabard ships, the worker prompts a soft refresh ("A new version is available — Reload"). Same as a desktop browser tab; mobile users just see the prompt in-app. |

## Connectivity

| ID | Requirement |
|----|-------------|
| REQ-MOB-80 | Mobile users on cellular / spotty connections experience the same reconnect-and-resync resilience as desktop (`11-optimistic-ui.md` REQ-OPT-30..52). The "Reconnecting…" indicator is more prominent on phone (a bar under the top app bar, not just a chrome dot) — phone users care more about whether actions are committing. |
| REQ-MOB-81 | Optimistic-write queueing during disconnect (REQ-OPT-31) works on mobile the same as desktop. The bounded queue is in-memory only (NG2 — no IndexedDB persistence). Tab unload during disconnect drops the queue; this is acknowledged limitation, not a bug. |
| REQ-MOB-82 | Background tab unload behaviour on mobile: iOS Safari and Chrome Android aggressively unload backgrounded tabs to free memory. Tabard treats tab restore as a fresh bootstrap (cache rebuilds via JMAP `Foo/get` calls). The user does not see this as a "lost work" state — it's a fresh fetch with the same view state. |

## Performance

| ID | Requirement |
|----|-------------|
| REQ-MOB-90 | First Contentful Paint ≤ 2.0 s on a representative phone (Galaxy Pixel-class CPU, 4G connection) from cold cache. Desktop's 2-second budget (REQ-PERF-01) is the same number; mobile achieves it through aggressive code-splitting (REQ-MOB-91) and progressive enhancement of lower-priority routes. |
| REQ-MOB-91 | Tabard's bundle is route-split: the suite shell + mail-app code load eagerly; chat, settings, calendar (when it ships), and contacts (when it ships) lazy-load on first navigation to their routes. Same code-split graph applies to desktop, but mobile feels the savings more. |
| REQ-MOB-92 | The image proxy (REQ-SEC-07) is even more critical on cellular: a single newsletter with 30 inline images proxied through herold means herold mediates bandwidth (cap, dedupe via cache) rather than the user's data plan paying 30 separate fetches to 30 senders. |
| REQ-MOB-93 | Animations on mobile respect `prefers-reduced-motion`. When set, transitions collapse to instant changes; no decorative motion. |

## Accessibility on touch

| ID | Requirement |
|----|-------------|
| REQ-MOB-100 | Tabard supports system font scaling: the suite-wide type scale is in `rem` units; `<html>` font-size respects the user's system text-size preference (iOS Dynamic Type, Android system font scaling). Layouts reflow at scaled sizes; we don't fix component widths in pixels. |
| REQ-MOB-101 | Screen readers: VoiceOver (iOS) and TalkBack (Android) navigate tabard the same way assistive tech navigates desktop (REQ-A11Y-02). All touch-specific patterns (long-press menus, swipe actions, bottom sheets) are reachable via the screen-reader rotor / explore-by-touch interaction with appropriate announcements. |
| REQ-MOB-102 | Swipe gestures have keyboard / button equivalents reachable from the same screen — swipe-left = archive is also reachable via the per-row overflow menu's Archive item. No action is gesture-only. |
| REQ-MOB-103 | Touch-target audits: every interactive element on phone breakpoint passes the 44×44 px minimum. Tested in CI via Playwright touch device emulation. |

## Settings on mobile

| ID | Requirement |
|----|-------------|
| REQ-MOB-110 | The settings panel renders as a single-column flow on phone, with the section nav (REQ-SET-21) becoming a top-level list that drills in to each section. Back arrow returns to the section list. |
| REQ-MOB-111 | New mobile-specific preferences (REQ-SET-13): swipe action mapping. Two settings — left-swipe action (default: archive) and right-swipe action (default: snooze) — selectable from {archive, snooze, delete, mark-read, label, none}. |

## Coach behaviour on touch

| ID | Requirement |
|----|-------------|
| REQ-MOB-120 | The shortcut coach is suppressed on phone (REQ-MOB-39) and auto-hides on tablet after 5 minutes of touch-only interaction (REQ-MOB-45). When the user pairs a hardware keyboard and starts using shortcuts, the coach re-activates within the same session. |
| REQ-MOB-121 | Coach observation (REQ-COACH-10..15) is paused on phone; mouse-vs-keyboard distinctions don't apply. The user's existing per-action stats are preserved (a desktop user who occasionally uses tabard on phone keeps their accumulated stats from desktop). |

## Out of scope (mobile)

- Native iOS / Android applications. NG1 stays.
- Full offline mode (read mail / queue actions while disconnected for hours). Reconnect-and-resync is the resilience model. NG2 stays. Worth revisiting if user feedback on cellular usability indicates need.
- Background sync (a service worker performing periodic JMAP fetches while the app is closed). NG2-adjacent.
- Web push notifications when the tab is closed. Requires Push API + service worker + VAPID + herold-side push integration. Worth revisiting in a future phase; not v1.
- Native widgets (iOS home-screen widgets, Android app widgets) — depend on native apps. NG1.
- Watch / wearable companions. NG1.
- Tablet split-screen multitasking with another app sharing tabard's data. The browser sandbox doesn't enable that anyway; if the user runs tabard alongside another app via OS-level split-screen, both windows of tabard run independently as separate sessions.
- Foldable-specific dual-pane layouts (e.g., outer screen one pane, inner screen another). The responsive breakpoints adapt to the visible viewport size; richer foldable awareness is deferred.
- Camera-direct-attachment for shooting a photo as an attachment from compose. The standard `<input type="file" accept="image/*" capture="environment">` works; no custom camera UI in v1.
