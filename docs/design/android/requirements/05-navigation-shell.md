# 05 — Navigation and shell

The native navigation model, back behaviour, and app lifecycle. The Suite's
phone layout (`docs/design/web/requirements/24-mobile-and-touch.md`
§ Layout - phone) is the reference for structure; this file records the native
realisation with Compose navigation. Behaviour (thread accordion, optimistic
actions, touch gestures) is cited from the Suite, not restated.

## Shell structure

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-01 | The shell is single-pane stack-based on phone: a root pane (thread list) onto which the reading pane, compose, search, and settings are pushed, matching Suite `REQ-MOB-30`. |
| REQ-AND-NAV-02 | A top app bar carries a context-sensitive navigation icon (hamburger / back), the title, and an overflow menu (Suite `REQ-MOB-31`). Search expands from an icon over the app bar. |
| REQ-AND-NAV-03 | Bottom navigation switches suite-app context (Mail; later Chat, Settings) per Suite `REQ-MOB-32`. A compose FAB anchors bottom-right above the bottom navigation (Suite `REQ-MOB-33`). |
| REQ-AND-NAV-04 | The mailbox/label tree opens as a navigation drawer (Suite `REQ-MOB-34`). Pickers (label, snooze, from-identity) render as bottom sheets (Suite `REQ-MOB-37`). |
| REQ-AND-NAV-05 | The tablet two/three-pane layouts (Suite `REQ-MOB-40..47`) are a later adaptation of the same navigation graph; phone is the v1 delivery target. |

## Inbox category lanes

The inbox is divided into the account's category lanes, whose dispositions and
priorities come from the server's `Mailbox` objects (Suite
`docs/design/web/requirements/05-categorisation.md` REQ-CAT-01..11, issues #399,
#404, #427).

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-30 | The inbox carries a tab row of the account's lanes and nothing besides: one tab per `pinned` category, in the server's priority order, with the labels that state a disposition ahead of the categories that take the pinned default. An account with no lanes shows no tab row and one undivided list. |
| REQ-AND-NAV-31 | The inbox opens on the `primary`-role lane (Suite REQ-CAT-03), which is also where a message carrying no category lands. That lane carries what no other tab claims: a `bundled` category's collapsed row and a conversation whose category has no lane of its own. An account without a `primary` lane opens on the leading tab, which carries the same. |
| REQ-AND-NAV-32 | The lane the reader picks holds across a sync that adds or reorders lanes; a lane that the server takes away returns the selection to the `primary` lane. |
| REQ-AND-NAV-33 | A tab whose lane holds unread mail carries a badge with that lane's unread conversation count, the conversations inside a bundled row included; past 99 the badge reads `99+`. The count is read from the local store's rows, so it follows a message being read or arriving without waiting for a sync to end. The badge sits in a slot each tab keeps in every state, so the row's geometry does not change when a count appears, changes width, or goes. |

## Back and gesture navigation

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-10 | The system back gesture / button pops the navigation stack in the Suite's documented order (Suite `REQ-MOB-54`): close picker -> close compose -> leave thread -> close drawer -> home. |
| REQ-AND-NAV-11 | Predictive back is supported: back gestures show the platform's predictive-back preview of the destination. |
| REQ-AND-NAV-12 | A dirty compose intercepts back with a discard/keep-draft prompt; keeping the draft persists it to the outbox (`02-offline-and-sync.md` REQ-AND-SYNC-21) rather than losing it. |
| REQ-AND-NAV-13 | Touch gestures parallel the Suite's: swipe-left/right quick actions on thread rows (Suite `REQ-MOB-23/24`, configurable), long-press for context menu / selection mode (Suite `REQ-MOB-22/29`), pull-to-refresh triggering reconciliation (Suite `REQ-MOB-25`). Haptics on long-press where the device supports it. |

## Lifecycle

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-20 | Process death and restore reconstruct UI state from saved instance state plus the local store; the user returns to their view without a "lost work" state (parallels Suite `REQ-MOB-82`, realised natively via the persistent store rather than a re-bootstrap). |
| REQ-AND-NAV-21 | On foreground the shell reopens EventSource and triggers reconciliation (`02-offline-and-sync.md` REQ-AND-SYNC-11); on background it releases the connection and relies on FCM. |
| REQ-AND-NAV-22 | The virtual keyboard is handled via window insets (IME insets): the focused input stays visible above the keyboard and bottom-anchored controls reposition (parallels Suite `REQ-MOB-60/61`). IME composition, autocorrect, and swipe-typing are not intercepted (Suite `REQ-MOB-63`). |
| REQ-AND-NAV-23 | Safe-area / display-cutout insets are respected so content is not clipped by notches or the navigation bar (parallels Suite `REQ-MOB-56`). |

## Out of scope (v1)

- Foldable dual-pane awareness (Suite `REQ-MOB-04`; viewport-driven only).
- Tablet-specific layouts as a v1 deliverable (phone-first; REQ-AND-NAV-05).
