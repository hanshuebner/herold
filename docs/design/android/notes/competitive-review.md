# Competitive review — Android email clients

Design input for the mobile client. Reviews how existing Android email apps
solve each surface, and records our adopt / adapt / reject decision per surface,
tied to a `REQ-AND-*` or Suite `REQ-*`. Owned by the `mobile` agent; kept current
as decisions firm up.

## Method and evidence quality

Three passes fed this doc:

1. A verified deep-research pass (fan-out search, source fetch, 3-vote
   adversarial verification): 23 sources, 83 candidate claims, 24 confirmed.
   Strong for Thunderbird/K-9, FairEmail, Proton, and Android/Material 3
   platform docs; thin on the commercial apps (their roundup claims did not
   survive verification).
2. An **emulator visual pass** (2026-07-11): Gmail, Outlook, and Spark installed
   on an Android 16 (`Medium_Phone_API_36.0`) Play-enabled emulator, signed into
   a real account, and driven through the ten surfaces. This is direct
   observation — it closes the commercial-app gap the research left open. The
   captures live in `./competitive-shots/` (kept local, gitignored: they contain
   real mailbox content + copyrighted UI; this repo mirrors to public GitHub).
3. Screenshot analysis of those captures against the rubric.

Evidence is now first-hand for the three commercial anchors plus verified-source
for the open-source/offline references. Where a claim rests on the platform docs
or an open-source project rather than our own observation, the source is named.

## Anchor references

- **Gmail** — the interaction baseline (the Suite calibrates to the Gmail subset
  the user knows). Governs swipe defaults, list gestures, compose flow, category
  presentation. Observed directly (`gmail-*.png`).
- **Thunderbird for Android / K-9 Mail** — the native-realization reference:
  open-source, offline-first, Material 3, with documented design decisions and
  readable Compose source. Verified-source only (not on the emulator).
- **Outlook** — the feature-rich contrast (Focused Inbox, account-rail drawer,
  bottom-bar reading actions). Observed directly (`outlook-*.png`).
- **Spark** — the aggressive-smart-inbox and best-in-class-widget reference.
  Observed directly (`spark-*.png`).

## Cross-app consensus (what the pass established)

These held across the apps we observed and are high-confidence inputs:

- **Bidirectional, per-direction-configurable swipe; default = Archive.** Gmail
  (`gmail-swipe.png`, `gmail-settings-1.png`) and Outlook
  (`outlook-swipe-actions.png`) both. Spark defaults to Mark-done. → our
  `REQ-AND-NAV-13`: archive one way, snooze/configurable the other.
- **Density is a shipped setting.** Gmail "Conversation list density: Default"
  (`gmail-settings-1.png`); Outlook "System / Blue / **Roomy**"
  (`outlook-settings-1.png`). → ship a density preference; start dense.
- **Theme follows system by default** (Gmail "System default", Outlook "System").
- **Undo, not confirmation dialogs.** Gmail ships with Confirm-before-delete /
  archive / send all **off** (`gmail-settings-2.png`) and relies on the Undo
  snackbar (`gmail-swipe.png`); Spark's mark-done has a ~3 s undo. → optimistic
  writes + undo, no interstitial confirms.
- **Reading-view swipe between messages** (Gmail; Outlook "Swipe between
  conversations" on, `outlook-mail-settings.png`). → Suite `REQ-MOB-28`.
- **Single-account simplifies the shell.** Outlook's drawer complexity is the
  multi-account **account rail** (`outlook-nav-drawer-clean.png`); Gmail's
  single-account drawer has none (`gmail-nav.png`). → drop the rail, keep the
  folder drawer + bottom nav.
- **AI is the resist column.** Gmail Smart Compose / Smart Reply / Smart features
  / Nudges (`gmail-account-settings-2/3.png`) and Spark's AI FAB
  (`spark-inbox.png`) are all features our scope excludes (Suite NG7 / our NG5).

## Surface-by-surface

### 1. Thread list — density, unread, chips, multi-select

- **Gmail** (`gmail-inbox.png`): pill search bar (hamburger + avatar); category
  **bundles** inline (Promotions / Updates rows with a "1 new" pill); message
  rows = circular avatar + blue importance chevron + sender (bold=unread) +
  subject + snippet + right-aligned date + trailing star; comfortable density.
- **Outlook** (`outlook-inbox.png`): **Focused / Other** tab split + Filter;
  colored monogram avatars; attachment paperclip on rows; comfortable density.
- **Spark** (`spark-inbox.png`): **Smart Inbox** — type bundles (Notifications
  320 / Newsletters 248 / Invitations 8) each collapsing many senders with a
  count + sender icons; time buckets (Today / Yesterday / This week); **unread =
  leading filled blue dot** (clearest unread signal of the three).
- **Verified (Thunderbird/K-9)**: dark-mode unread legibility needed deliberate
  tuning; density regressed on a naive M3 rebuild and was fixed with a density
  setting; account chip only in multi-account.
- **Decision (adopt):** single-account → **no account chip**, reserve that space
  for unread; **leading unread dot** (Spark) is clearer than bold-only; start
  denser than raw M3 and ship a density preference (Gmail + Outlook both do).
  Ties Suite `REQ-MOB-36`.

### 2. Swipe actions

- **Gmail** (`gmail-swipe.png`, `gmail-settings-1.png`): default swipe = Archive,
  Undo snackbar; "Swipe actions" configurable.
- **Outlook** (`outlook-swipe-actions.png`): per-direction config — Swipe left =
  Archive (green + archive icon reveal), Swipe right = unset; each reassignable.
- **Verified (Android/M3)**: `SwipeToDismissBox` — one action per direction,
  colored `backgroundContent` revealed on swipe.
- **Decision (adopt):** `SwipeToDismissBox`, default archive / configurable
  other direction, colored background icon, Undo toast. `REQ-AND-NAV-13`.

### 3. Reading / conversation view

- **Gmail** (`gmail-reading-reply-menu.png`): top-bar action icons (back /
  archive / delete / mark-unread / overflow); subject heading + "Inbox" chip +
  star; sender block with "to … ▾" expander + inline **Unsubscribe** +
  per-message overflow; bottom **reply bar** with a reply-type menu (Reply all /
  Reply / Forward / Change recipients).
- **Outlook** (`outlook-reading-attachment.png`): minimal top bar (back only);
  **all actions in a bottom floating bar** (Reply + reply-type caret,
  mark-unread, delete, archive, overflow) — more thumb-reachable; subject + "📎 1"
  count chip; inline **attachment card** (PDF icon, name, "PDF · 25 KB",
  overflow); external images loaded (proxied).
- **Verified (Thunderbird)**: swipe (not arrows) between messages; expandable
  header. **(FairEmail/Proton)**: block-by-default or server-proxy remote images.
- **Decision (adopt):** thread accordion (Suite `REQ-UI-20..25`); swipe between
  threads; **bottom-anchored action bar** (Outlook's ergonomics beat Gmail's
  top-bar icons on phone); attachment cards (Outlook model); inline Unsubscribe
  (Gmail) → Suite `14-unsubscribe`; **server-proxied images** (herold image
  proxy — Gmail defaults to always-display *because* it proxies,
  `gmail-account-settings-4.png`; matches the Proton posture we adopted).

### 4. Compose

- **Gmail** (`gmail-compose.png`): **full-screen**; top bar back / attach / send
  / overflow; stacked From (identity dropdown) / To (Cc/Bcc chevron) / Subject /
  body; formatting toolbar hidden until invoked.
- **Decision (adopt):** full-screen compose on phone (Suite `REQ-MOB-35`),
  identity dropdown, Cc/Bcc expander, collapsed formatting toolbar, inline-vs-
  attach (Suite G15 / `REQ-AND-SYS-32`). Surface 4 was unverified in research;
  the Gmail capture now grounds it.

### 5. Navigation shell

- **Gmail** (`gmail-nav.png`): **modal drawer + scrim** for folders (categories
  + Starred / Snoozed / Important / Sent / Scheduled / Outbox / Drafts / All mail
  / Spam / Trash + IMAP labels); selected = filled pill; avatar top-right for
  account switch; **bottom nav = Mail / Meet**; extended "Compose" FAB.
- **Outlook** (`outlook-nav-drawer-clean.png`): **account rail** (avatars + "＋")
  + folder-list drawer (system folders with distinct icons + custom alphabetical);
  **bottom nav = Mail / Calendar / Apps**; icon compose FAB.
- **Verified (M3)**: drawer = larger-device view-switcher; phones steered to
  bottom navigation; M3 Expressive de-emphasizes drawers further.
- **Decision (adapt):** modal **folder** drawer (no account rail — single
  account) + **bottom nav** for app-switch (Mail; later Chat/Settings) + compose
  FAB. `REQ-AND-NAV-01..04`. Rebuild the drawer in Compose (MaterialDrawer is a
  dead end per Thunderbird).

### 6. Search

- **Gmail** (`gmail-search.png`): full-width field + horizontally-scrolling
  **filter chips** (Labels / From / To / Attachment / …).
- **Decision (adopt):** full-width search (Suite `REQ-MOB-31`) + scope/filter
  chips; offline returns locally-synced results scoped as such, full-corpus FTS
  needs connectivity (`REQ-AND-SYNC-13`). Surface 6 was unverified in research;
  the Gmail capture grounds the chip pattern.

### 7. Offline + outbox UX

- **Gmail** (`gmail-account-settings-4.png`): **Sync Gmail** on; **Days of mail
  to sync: 30 days** (time-bounded local window); **Download attachments** to
  recent messages via Wi-Fi; images always-display (proxied).
- **Verified (FairEmail)**: local Room store as source of truth, visible outbox,
  retry on connectivity-change and pull-to-refresh.
- **Decision (adopt):** local store as UI source of truth + visible outbox with
  queued/sending/failed states (`REQ-AND-SYNC-20..31`). Add a **time-window sync
  bound** (Gmail's "days of mail to sync") alongside our size-based LRU
  (`REQ-AND-SYNC-12`), and **Wi-Fi-gated attachment prefetch**. Differentiation
  opportunity: none of the three commercial apps surface outbox/pending state as
  richly as our model intends.

### 8. Notifications

- **Verified (Android platform)**: group summary (`setGroupSummary`) + per-
  message children; Direct Reply (`RemoteInput`, API 24+) without opening the
  app; ≤3 action buttons; `MessagingStyle` for threads. (Notifications were
  disabled on the emulator, so this rests on platform docs, not capture — Gmail's
  "Default notification action: Archive" and per-label notification controls were
  observed in `gmail-settings-1.png` / `gmail-account-settings-1.png`.)
- **Decision (adopt):** group summary + children; ≤3 actions (Archive / Delete /
  Reply); Reply via `RemoteInput` enqueued to the offline outbox; `MessagingStyle`
  threading. `REQ-AND-PUSH-10..22`.

### 9. Widgets / tiles / share

- **Spark** (`spark-widget.png`): a **functional home-screen mini-inbox** —
  header "Inbox (999+)" (folder + unread count), scrollable recent-message list
  (avatar / address / subject / date), and a **quick-action row** (Search /
  Calendar / Pin / Compose). Renders without the app open.
- **Decision (adopt):** Glance widget matching Spark's bar — recent inbox from
  the **local store** + inline quick actions, not a passive counter
  (`REQ-AND-SYS-20`). Quick Settings tile + share target + App Links per
  `REQ-AND-SYS-01..22`.

### 10. Material 3 theming + dark mode

- **Gmail** `gmail-settings-1.png` (Theme: System default) and **Outlook**
  `outlook-settings-1.png` (System / Blue / Roomy — theme + **accent** + density).
- **Verified (Thunderbird)**: unread legibility in dark mode is a deliberate M3
  tuning problem, not automatic.
- **Decision (adopt):** M3 dynamic color; light/dark follow system with a fixed
  override (`REQ-AND-SYS-40`, Suite `REQ-SET-01`); **validate unread-vs-read
  contrast explicitly in both schemes** (don't trust default tonal roles). An
  accent-color option (Outlook) is worth considering.

## Categorisation presentation (cross-cut)

Two observed models for showing categories/bundles:

- **Gmail**: four categories (Primary / Promotions / Social / Updates,
  `gmail-account-settings-1.png`) shown as **inline collapsed bundle rows** with
  a "N new" pill at the top of the list (`gmail-inbox.png`).
- **Spark**: aggressive **type bundles** (Notifications / Newsletters /
  Invitations) with per-bundle counts and **representative sender icons**
  (`spark-inbox.png`).

Our categorisation uses herold's `$category-*` keywords (Suite
`05-categorisation.md`). For display, Gmail's inline-bundle-with-count is the
closer fit to our category model; Spark's sender-icon preview is a richer
presentation worth prototyping for the bundle rows.

## Auth (cross-cut)

**Outlook App lock** (`outlook-settings-2.png`) — biometric/PIN app lock is a
shipped mainstream feature, confirming `REQ-AND-AUTH-11` (biometric unlock) as an
expected norm, not a nicety.

## Anti-patterns to avoid

- **The M3 density regression** (verified, Thunderbird) — start dense, ship a
  density setting (Gmail + Outlook both do).
- **Trusting default M3 tonal roles for unread** (verified, Thunderbird) — tune
  and test in dark mode.
- **AI surface creep** — Smart Compose/Reply/Nudges (Gmail) and AI assistant
  (Spark) are exactly what our single-user scope excludes; resist it (NG5).
- **Confirmation dialogs** — Gmail ships them off in favor of undo; interstitial
  confirms on every action are friction we avoid.

## Open questions (now largely closed)

The research's open questions on Gmail/Outlook/Spark swipe/density/search/compose
are answered by the emulator pass above. Remaining:

1. What Thunderbird **shipped** for its Compose drawer post-Sept-2024, given M3
   Expressive's drawer de-emphasis (verified-source, not re-checked).
2. Notification presentation was not observed (disabled on the emulator) — rests
   on platform docs + Gmail's settings; verify against a live-notification
   capture when the client's own notifications exist.

## Captured screenshots

Local only (`./competitive-shots/`, gitignored — real mailbox content):

- Gmail: `gmail-inbox`, `gmail-swipe`, `gmail-compose`, `gmail-nav`,
  `gmail-search`, `gmail-reading-reply-menu`, `gmail-settings-1/2`,
  `gmail-account-settings-1..4`.
- Outlook: `outlook-inbox`, `outlook-reading-attachment`, `outlook-swipe-actions`,
  `outlook-nav-drawer(-clean)`, `outlook-edit-folders`, `outlook-settings-1/2`,
  `outlook-mail-settings`.
- Spark: `spark-inbox`, `spark-widget`.

## Sources (verified research pass)

Primary / high-confidence:

- Thunderbird for Android — [Modern Message Redesign](https://blog.thunderbird.net/2022/12/thunderbird-for-android-preview-modern-message-redesign/); [Jul-Aug 2024 M3 progress report](https://blog.thunderbird.net/2024/09/thunderbird-for-android-k-9-mail-july-aug-2024-progress-report/); [Jan-Feb 2025 progress report](https://blog.thunderbird.net/2025/03/thunderbird-for-android-january-february-2025-progress-report/); drawer rebuild issues [#8100](https://github.com/thunderbird/thunderbird-android/issues/8100), [#1859](https://github.com/thunderbird/thunderbird-android/issues/1859).
- Material 3 — [navigation drawer](https://m3.material.io/components/navigation-drawer/overview), [navigation bar](https://m3.material.io/components/navigation-bar/guidelines), [lists](https://m3.material.io/components/lists/guidelines), [color system](https://m3.material.io/styles/color/system/overview).
- Android developers — [swipe-to-dismiss (Compose)](https://developer.android.com/develop/ui/compose/touch-input/user-interactions/swipe-to-dismiss); [notification groups](https://developer.android.com/develop/ui/views/notifications/group); [build notification](https://developer.android.com/develop/ui/views/notifications/build-notification); [MessagingStyle](https://developer.android.com/reference/android/app/Notification.MessagingStyle); [Notifications in Android N (Direct Reply)](https://android-developers.googleblog.com/2016/06/notifications-in-android-n.html).
- Privacy posture — [FairEmail](https://github.com/M66B/FairEmail); Proton [tracker protection](https://proton.me/support/email-tracker-protection) and [images](https://proton.me/support/protonmail-images).

Practitioner roundups (context only; specifics did NOT survive verification):
[Android Authority](https://www.androidauthority.com/best-android-email-apps-579368/),
[Zapier](https://zapier.com/blog/best-android-email-app/),
[Android Police](https://www.androidpolice.com/best-email-apps-for-android/).

Curated screen library (login-walled; not used directly):
[Mobbin — email & messages](https://mobbin.com/explore/mobile/screens/emails-messages).
