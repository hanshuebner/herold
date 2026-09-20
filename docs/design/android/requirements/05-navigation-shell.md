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
| REQ-AND-NAV-02 | The inbox's top row carries three controls, left to right: the drawer button, a search field and the account avatar. The field is a target that opens the search screen with that screen's own field focused, because the query, its results and the place the results list was left belong to that destination (`REQ-AND-NAV-25`, issue #340). The avatar opens the account scope (Suite `REQ-MAIL-SUB-02/04`) and signing out. Reporting a problem is a drawer entry (`04-system-integration.md` REQ-AND-SYS-50). Under the top row stands a row of its own carrying the open destination's name, the inbox's category lanes (REQ-AND-NAV-30) and the status indicator (`02-offline-and-sync.md` REQ-AND-SYNC-30). A pushed screen keeps a back icon, its own title and its own actions (Suite `REQ-MOB-31`). |
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
| REQ-AND-NAV-30 | The inbox carries a tab row of the account's lanes and nothing besides, in the row under the top row (REQ-AND-NAV-02), beside the mailbox's name: one tab per `pinned` category, in the server's priority order, with the labels that state a disposition ahead of the categories that take the pinned default. The `primary` lane leads the row whenever the account gives it no lane of its own, so it is a tab even on an account whose categories outnumber the pinned budget. An account with no lanes shows no tab row and one undivided list. |
| REQ-AND-NAV-31 | The inbox opens on the `primary`-role lane (Suite REQ-CAT-03), which is also where a message carrying no category lands. That lane carries what no other tab claims: a `bundled` category's collapsed row, a conversation whose category has no lane of its own, and one whose category the pinned budget left out. An account that gives its `primary` category another lane opens on the leading tab, which carries the same. |
| REQ-AND-NAV-32 | The lane the reader picks holds across a sync that adds or reorders lanes; a lane that the server takes away returns the selection to the `primary` lane. |
| REQ-AND-NAV-33 | A tab whose lane holds unread mail carries a badge with that lane's unread conversation count, the conversations inside a bundled row included; past 99 the badge reads `99+`. The Suite badges its own tab strip the same way. The count is read from the local store's rows, so it follows a message being read or arriving without waiting for a sync to end. The badge sits in a slot each tab keeps in every state, so the row's geometry does not change when a count appears, changes width, or goes. |

## Conversation screen

The reading pane's arrangement, taken from Gmail's (issue #428): a short
action bar, the subject as a heading in the content, one card per message
carrying its own actions, and the answers below the conversation. Behaviour
of the accordion itself is the Suite's (`docs/design/web/requirements/09-ui-layout.md`
REQ-UI-20..25).

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-40 | The conversation's app bar carries back, archive, delete, mark-unread, the conversation overflow and the status indicator (`02-offline-and-sync.md` REQ-AND-SYNC-30), and nothing else. The overflow holds what applies to the whole conversation: mute, snooze, share and reporting a problem. |
| REQ-AND-NAV-41 | The subject is a heading in the scrolling content: at most two lines, ellipsised, with the mailboxes and labels the conversation sits in named beside it and the conversation's star at its right. It scrolls away with the messages. |
| REQ-AND-NAV-42 | A message card carries the sender's avatar - the initials over the colour derived from the address (Suite `REQ-MAIL-44` tier 4) - the display name in bold with the message's date on the same line, and a recipients line that a chevron opens onto the full from, to and cc addresses and the message's full timestamp. A collapsed message keeps its one-line preview. |
| REQ-AND-NAV-43 | Each message carries its own reply affordance and overflow: reply, reply all, forward, star this message, mark unread from here, block the sender, create a filter from this message, and "Why is this here?". They act on the message they sit on. |
| REQ-AND-NAV-44 | Reply, reply all and forward are pills pinned below the conversation and act on its newest message, the one a reply answers (Suite `REQ-MAIL-30`). |
| REQ-AND-NAV-45 | Marking unread - the whole conversation from the bar, the message and the ones after it from a card - leaves the conversation, so the reader lands on the list where the unread row is. Delete moves the conversation into the trash mailbox and out of the others, under the undo offer archive carries (`02-offline-and-sync.md` REQ-AND-SYNC-20). |
| REQ-AND-NAV-46 | A message body is laid out to the card's width at the text size the pane renders with. The body document declares `width=device-width, initial-scale=1`, so its text is the size the pane asks for; the widths a desktop template declares are what yields. Every `width`/`height` attribute and inline `width`/`min-width`/`max-width` wider than what the card gives the body is rewritten away before the document is rendered, padding and borders count inside the width a box is given, images and boxes are capped at the card's own width in pixels, and a long word or URL breaks where it has to. What no reflow can narrow keeps its width and scrolls sideways inside the body's own box; the conversation around it only ever scrolls up and down (issue #430). |
| REQ-AND-NAV-48 | A `multipart/alternative` whose HTML still needs more width than the card gives it - a row that states it does not wrap - is read as its `text/plain` alternative, with a one-tap control that shows the sender's own layout and one back to the text. A message whose HTML fits, and a message with no usable text alternative, is read as its HTML. The choice is made in the shared core from the sanitised document and the card's width (issue #430). |
| REQ-AND-NAV-49 | Remote images in a message body are held back until the reader asks for them (Suite `REQ-SEC-05`), behind a bar the body carries; an inline `cid:` image renders from the part the message holds. Asking for them loads them into the message being read, through herold's image proxy (Suite `REQ-SEC-07`), without leaving the conversation - and a part that reaches the store after the body was first shown renders once it is there. The body serves every image from what its resolvers can produce at the moment the request is made, so the choice and the parts that arrive after it reach the images on screen (issue #440). |
| REQ-AND-NAV-50 | An image the pane scales for display keeps a format that carries what the sender put in it. An image with an alpha channel is re-encoded losslessly and is never composited onto a background colour, so what shows through it is the message's own background - the cell the sender declared, or the pane's, in either theme; an image with no alpha channel may be re-encoded lossily, which is what bounds the decode of a camera photo. The content type handed to the body surface describes the bytes actually served (issue #445). |
| REQ-AND-NAV-47 | The trailing quoted history of a reply folds behind a chip the reader opens with one tap, on the Suite's contract (`docs/design/web/architecture/04-rendering.md`, `collapseQuotedRegions`): the fresh text always renders unfolded, a citation-introducer line and a trailing signature fold with the quote, a bottom-posted or interleaved reply stays expanded, and a plain-text body's `>`-prefixed citation folds the same way. The fold is a `<details>` element, so it opens with JavaScript off (issue #432). |

## Back and gesture navigation

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-10 | The system back gesture / button pops the navigation stack in the Suite's documented order (Suite `REQ-MOB-54`): close picker -> close compose -> leave thread -> close drawer -> home. |
| REQ-AND-NAV-11 | Predictive back is supported: back gestures show the platform's predictive-back preview of the destination. |
| REQ-AND-NAV-12 | A dirty compose intercepts back with a discard/keep-draft prompt; keeping the draft persists it to the outbox (`02-offline-and-sync.md` REQ-AND-SYNC-21) rather than losing it. |
| REQ-AND-NAV-13 | Touch gestures parallel the Suite's: swipe-left/right quick actions on thread rows (Suite `REQ-MOB-23/24`, configurable), long-press for context menu / selection mode (Suite `REQ-MOB-22/29`), pull-to-refresh triggering reconciliation (Suite `REQ-MOB-25`). Haptics on long-press where the device supports it. |
| REQ-AND-NAV-14 | Reloading a message list is the pull-down gesture on the list (Suite `REQ-MOB-25`, issue #444). It forces the reconciliation pass the sync loop runs, through that loop, so a refusal the loop is backing off from does not hold it up (`02-offline-and-sync.md` REQ-AND-SYNC-14, issue #436). The indicator is the platform's, in the app's colours, and it runs from the gesture until that pass has finished rather than for a fixed time. The list keeps the place the reader put it through the gesture, and a pull that leaves the list standing at its newest conversation asks for a sync rather than for a place (REQ-AND-NAV-25). |

## Lifecycle

| ID | Requirement |
|----|-------------|
| REQ-AND-NAV-20 | Process death and restore reconstruct UI state from saved instance state plus the local store; the user returns to their view without a "lost work" state (parallels Suite `REQ-MOB-82`, realised natively via the persistent store rather than a re-bootstrap). |
| REQ-AND-NAV-21 | On foreground the shell reopens EventSource and triggers reconciliation (`02-offline-and-sync.md` REQ-AND-SYNC-11); on background it releases the connection and relies on FCM. |
| REQ-AND-NAV-22 | The virtual keyboard is handled via window insets (IME insets): the focused input stays visible above the keyboard and bottom-anchored controls reposition (parallels Suite `REQ-MOB-60/61`). IME composition, autocorrect, and swipe-typing are not intercepted (Suite `REQ-MOB-63`). |
| REQ-AND-NAV-23 | Safe-area / display-cutout insets are respected so content is not clipped by notches or the navigation bar (parallels Suite `REQ-MOB-56`). |
| REQ-AND-NAV-24 | The shell draws edge to edge, so every surface pinned to the window's bottom edge - the conversation's reply pills, the inbox's compose button, the drawer's pinned entries, a sheet's action row - pads itself out of what the platform reports there (the navigation bar, and the strip a gesture-navigated device holds for its swipe), while its background carries on to the edge of the screen. The room comes from the report, so a three-button device gets a bar's height and a gesture-navigated one a handle's worth, with no band of empty screen under either (issue #428). |
| REQ-AND-NAV-25 | Every message list keeps the place the reader put it, one place per destination: each inbox lane (REQ-AND-NAV-30), each mailbox and label the drawer opens, the snoozed view and the search results. Leaving the list for a conversation and coming back returns it to the row it was left on, and switching destination and coming back restores that destination's own place rather than carrying one over. The places are part of the screen's saved state, so a shell rebuilt from that state puts each list back where it was, as it does with the selected lane and the open destination (REQ-AND-NAV-20). A list the reader has not touched is held at its newest conversation as mail arrives; one they have placed keeps its anchor under a refresh instead of jumping. The drag that places a list is one that leaves it off its top, so the pull-to-refresh gesture (REQ-AND-NAV-14) leaves a list at its newest conversation pinned there. Only the most recently used destinations are remembered, so a session that visits many labels does not accumulate places without bound; a destination that falls out of that window opens at its newest conversation, as it does on a fresh start (issue #439). |

## Out of scope (v1)

- Foldable dual-pane awareness (Suite `REQ-MOB-04`; viewport-driven only).
- Tablet-specific layouts as a v1 deliverable (phone-first; REQ-AND-NAV-05).
