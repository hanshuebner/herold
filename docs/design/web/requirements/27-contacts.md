# 27 — Contacts

The contacts app is the suite's address book: browse, search, view, and edit
contacts; manage contact photos; import and export vCard; detect and merge
duplicates. It runs over herold's JMAP for Contacts datatype
(`urn:ietf:params:jmap:contacts`, RFC 9553 JSContact + the JMAP-Contacts
binding), which the server already implements (`../../server/requirements/01-protocols.md`
REQ-PROTO-55; methods `AddressBook/*`, `Contact/*`).

The contacts app is a package in the single suite SPA shell
(`00-scope.md` § The suite), reachable at `#/contacts`. It supplies the data
behind the mail app's recipient autocomplete, recipient hover card, and
"view contact" links (`00-scope.md` § Cross-app integration points).

Server-side extensions this app depends on — contact-photo blob wiring, the
vCard converter, and the import/export path — are specified in
`../../server/requirements/27-contacts.md`.

## The JSContact model, as the suite sees it

The server stores each contact as a full RFC 9553 JSContact `Card` and returns
it verbatim on `Contact/get`. It parses and indexes a typed subset
(`name`, `emails`, `phones`, `addresses`, `organizations`, `titles`, `kind`,
`uid`, `version`) and round-trips every other RFC 9553 property unchanged. The
suite therefore edits the whole `Card` object: fields the server does not type
are still persisted and returned, they are simply not independently searchable
or sortable (REQ-CONT-40).

| ID | Requirement |
|----|-------------|
| REQ-CONT-01 | The suite treats the JSContact `Card` (RFC 9553) as the contact's canonical representation. It reads the full object from `Contact/get` and writes changes with `Contact/set` (JSON Merge Patch on `update`, whole object on `create`). It never discards properties it does not itself render — an edit round-trips unknown properties intact. |
| REQ-CONT-02 | `Contact/set create` mints a `Card` with `version: "1.0"` and `kind` defaulting to `"individual"`. `uid` is left for the server to assign; the suite does not fabricate one. |
| REQ-CONT-03 | The suite pins to the JMAP-Contacts binding-draft revision advertised by the server's capability object and does not feature-detect method-by-method. If the `urn:ietf:params:jmap:contacts` capability is absent, the contacts app renders an unavailable state rather than erroring per request. |

## Navigation and app shell

| ID | Requirement |
|----|-------------|
| REQ-CONT-10 | The suite shell exposes a top-level Contacts destination. Activating it routes to `#/contacts` (the list view). The destination is reachable by pointer and by a keyboard shortcut consistent with the shell's app-switch model (`10-keyboard.md`). |
| REQ-CONT-11 | Routes: `#/contacts` (list), `#/contacts/<contactId>` (detail/view), `#/contacts/<contactId>/edit` (edit), `#/contacts/new` (create). The active route is reflected in the URL so views are bookmarkable and the back action restores the prior list scroll position and search. |
| REQ-CONT-12 | The detail route accepts the same `contactId` the mail app links with (recipient hover card "view contact", sender-name link). Navigating to an unknown or destroyed `contactId` shows a not-found state with a link back to the list, not a crash. |

## List view

| ID | Requirement |
|----|-------------|
| REQ-CONT-20 | The list view enumerates the principal's contacts via `Contact/query` + `Contact/get`, sorted by `displayName` ascending by default. It shows, per row: photo (or a generated monogram fallback, REQ-CONT-62), display name, and a secondary line (primary email, falling back to organization or primary phone). |
| REQ-CONT-21 | The list is virtualised: it renders only visible rows and pages further results through `Contact/query` `position`/`limit` as the user scrolls. It does not fetch every contact up front. |
| REQ-CONT-22 | A search field filters the list live via `Contact/query` `filter: { text: <query> }` (server substring match over name, emails, phone numbers, organizations, titles, and UID). Typing debounces before issuing the query; clearing the field restores the full list. |
| REQ-CONT-23 | Sort options: display name (default), most recently created, most recently updated — mapped to `Contact/query` `sort` on `displayName`, `created`, `updated`. The chosen sort is reflected in the URL. |
| REQ-CONT-24 | The list is scoped to an address book (REQ-CONT-70). With a single address book, the scope selector is hidden; with more than one, the list header offers an address-book filter plus "All contacts". |
| REQ-CONT-25 | Empty states are explicit: no contacts at all offers "Add contact" and "Import"; a search with no matches offers to create a contact from the typed text (if it parses as a name or email) or to clear the search. |
| REQ-CONT-26 | The list updates live from the push channel: `Contact/changes` driven by the EventSource `Contact` state (`11-optimistic-ui.md`, `../architecture/03-sync-and-state.md`) adds, removes, and re-sorts rows without a manual refresh. |

## Detail view

| ID | Requirement |
|----|-------------|
| REQ-CONT-30 | The detail view renders every populated section of the `Card`: photo, formatted name, nicknames, each organization + unit + title, every email (with its contexts/label and preferred marker), every phone (with contexts, features, label), every postal address (formatted from components or `full`), every URL/online service, notes, and significant dates (birthday, anniversary). Empty sections are omitted, not shown blank. |
| REQ-CONT-31 | Actionable values are live: emails open compose pre-addressed, phones are `tel:` links, URLs open in a new tab, postal addresses link to a map. These mirror the affordances the recipient hover card already exposes (`../requirements/02-mail-basics.md`, `RecipientHoverCard`). |
| REQ-CONT-32 | The detail view links to "all mail with this person" — an `Email/query` filtered by the contact's email addresses (`00-scope.md` § Cross-app integration points). |
| REQ-CONT-33 | The detail view offers Edit, Delete, Export (single-contact vCard, REQ-CONT-81), and — when duplicate candidates exist for this contact — a "merge duplicates" entry point (REQ-CONT-92). |
| REQ-CONT-34 | Delete requires confirmation and is optimistic (`11-optimistic-ui.md`): the row disappears immediately, a failed `Contact/set destroy` reverts it with an error and a retry affordance. |

## Create and edit

The edit form covers every JSContact property the address book needs for
Google-Contacts parity. Typed properties (name, emails, phones, addresses,
organizations, titles) are server-indexed; the remainder round-trip through the
server unchanged (REQ-CONT-01).

| ID | Requirement |
|----|-------------|
| REQ-CONT-40 | The edit form writes a valid RFC 9553 `Card`. Fields the server does not independently index (nicknames, URLs/online services, notes, birthday/anniversary, free-form relations) are still editable and persisted; the form must not drop them on save. |
| REQ-CONT-41 | Name: separate inputs for the structured `name.components` (given, surname, and, behind a "more" affordance, prefix/title, middle, suffix, credential) plus an optional explicit `name.full`. When `full` is empty the suite derives the display name from components in the locale's order; when the user edits `full` directly that value wins. `sortAs` is set when the user overrides sort order. |
| REQ-CONT-42 | Emails: a repeatable list of typed entries. Each entry has an address, an optional context (home/work/other) and label, and a "preferred" flag mapped to JSContact `pref`. At most one entry is preferred; the preferred (or first) address becomes the server's `primaryEmail`. |
| REQ-CONT-43 | Phones: a repeatable list of typed entries with number, contexts, features (voice/cell/fax/text/video), optional label, and preferred flag — mapped to JSContact `phones`. |
| REQ-CONT-44 | Postal addresses: a repeatable list. Each address edits the JSContact `components` (street, locality, region, postcode, country) plus `countryCode`, with contexts/label and preferred flag. A `full` fallback is accepted for addresses that do not decompose. |
| REQ-CONT-45 | Organizations and titles: repeatable organization entries (name + units) and repeatable titles (name + kind), with a title optionally bound to an organization via `organizationId`. |
| REQ-CONT-46 | URLs / online services, nicknames, and notes are editable as repeatable free-form entries mapped to the corresponding RFC 9553 properties. |
| REQ-CONT-47 | Significant dates: birthday and anniversary editable as partial-date-capable inputs (year optional), mapped to JSContact `anniversaries`. |
| REQ-CONT-48 | `kind` is selectable (individual / group / org / location); it defaults to individual and is not surfaced prominently for the common case. |
| REQ-CONT-49 | Add/remove controls for every repeatable list; removing the last row of a section removes the section from the emitted `Card`. Saving with no changes issues no `Contact/set`. |
| REQ-CONT-50 | Client-side validation before save: email syntax, at most one preferred entry per multi-valued property, non-empty contact (a `Card` must carry at least a name or one email/phone). Validation errors are shown inline and block save. |
| REQ-CONT-51 | Save is optimistic: the detail/list reflects the change before the server acknowledges; a `Contact/set` `notCreated`/`notUpdated` error reverts and surfaces the server's `SetError` (`11-optimistic-ui.md`). |
| REQ-CONT-52 | Unsaved-changes guard: navigating away from a dirty form prompts to discard or keep editing. |

## Photos

| ID | Requirement |
|----|-------------|
| REQ-CONT-60 | A contact photo is uploaded to the JMAP blob store (`Blob/upload`) and referenced from the `Card` via a JSContact `media`/`photos` entry carrying the returned `blobId` and media type (`../../server/requirements/27-contacts.md` REQ-CTS-01..05). The suite does not embed image bytes inline in the `Card`. |
| REQ-CONT-61 | The photo editor lets the user pick or drop an image, crop it to a square, and set it; and lets the user remove the current photo. Oversized or unsupported images are rejected client-side with a clear message before upload. |
| REQ-CONT-62 | When a contact has no photo, the suite renders a deterministic monogram/color avatar derived from the display name — the same fallback the recipient hover card uses — so list and detail views never show a broken image. |
| REQ-CONT-63 | Photos are fetched through the JMAP download path for the referenced `blobId` and are cached in-session; they are never fetched from third-party avatar services (`00-scope.md` NG9). |

## Address books and groups

The server models `AddressBook` objects; each contact belongs to one address
book. Groups (Google "labels") are JSContact group cards (`kind: "group"`)
whose members reference other cards.

| ID | Requirement |
|----|-------------|
| REQ-CONT-70 | The suite reads address books via `AddressBook/get`. With one address book it stays implicit; the create/edit form targets it automatically. The suite does not require the user to manage address books to use the app. |
| REQ-CONT-71 | Groups are represented as `kind: "group"` cards. The list view can filter to a group's members; the detail/edit of a member can add/remove the contact from a group. Group membership edits are `Contact/set` operations on the group card. |
| REQ-CONT-72 | Creating, renaming, and deleting groups is supported. Deleting a group deletes only the group card, never its member contacts. |

## vCard import and export

The vCard 4.0 (RFC 6350) converter and the import/export transport are
server-side (`../../server/requirements/27-contacts.md` REQ-CTS-10..20); the
suite drives them and presents progress and results.

| ID | Requirement |
|----|-------------|
| REQ-CONT-80 | Import: the user selects a `.vcf` file (one or many vCards). The suite hands the file to the server import path, which parses each vCard to a JSContact `Card` and creates contacts. The suite shows a summary — created, skipped, failed with reasons — and does not silently drop malformed cards. |
| REQ-CONT-81 | Export: the user exports a single contact, the current selection/filter, or the whole address book to a `.vcf` download produced by the server converter. |
| REQ-CONT-82 | Import surfaces likely duplicates against existing contacts before committing, offering skip / create-anyway / merge per incoming card (REQ-CONT-90). A large import reports progress and can be cancelled; already-created contacts from a cancelled run remain. |
| REQ-CONT-83 | Round-trip fidelity: a contact exported and re-imported yields an equivalent `Card`. Properties the converter cannot represent in vCard are reported in the export summary rather than dropped silently. |

## Duplicate detection and merge

| ID | Requirement |
|----|-------------|
| REQ-CONT-90 | The suite detects likely-duplicate contacts by matching on shared email address, shared phone number (normalised), and close display-name match. Detection runs over `Contact/query` results and does not require a server-side dedup method. |
| REQ-CONT-91 | A "duplicates" review surfaces candidate clusters; the user confirms or dismisses each. Dismissed pairs are remembered (client-side, per principal) so the same non-duplicate is not re-flagged every visit. |
| REQ-CONT-92 | Assisted merge: for a confirmed cluster the suite presents a field-by-field merge — union of emails/phones/addresses, chosen name, chosen photo — and lets the user adjust before committing. |
| REQ-CONT-93 | A merge is a single `Contact/set` that creates (or updates the surviving card to) the merged `Card` and destroys the other members atomically, so a partial failure leaves the pre-merge contacts intact (`11-optimistic-ui.md`). The merged result preserves every value the user kept; nothing is lost to the merge that the user did not explicitly drop. |
| REQ-CONT-94 | Merge is reachable from the duplicates review, from the detail view (REQ-CONT-33), and from the import flow (REQ-CONT-82). |

## Mail integration

These are the hooks `00-scope.md` § Cross-app integration points reserves for
the contacts app; this app now backs them.

| ID | Requirement |
|----|-------------|
| REQ-CONT-100 | Recipient autocomplete in compose (`02-mail-basics.md` REQ-MAIL-11) sources from the contacts app's data — `Contact/query`/`Contact/get` results — merged with the client-local seen-address history and, when advertised, `Directory/search`. The existing `lib/contacts/store` cache is the merge point. |
| REQ-CONT-101 | The recipient hover card's "Add contact" / "Edit contact" / "View contact" actions target the contacts app: "Add" creates via `Contact/set`, "View"/"Edit" route to `#/contacts/<contactId>` and its edit route. |
| REQ-CONT-102 | Adding a contact from mail (hover card or compose) captures the display name and address into a new `Card`; the user can enrich it later in the contacts app. |

## Keyboard, i18n, accessibility, non-functional

| ID | Requirement |
|----|-------------|
| REQ-CONT-110 | The contacts app is keyboard-navigable end to end: move through the list, open detail, enter edit, save (with a shortcut), and cancel — consistent with the shell's keyboard model (`10-keyboard.md`, `../architecture/05-keyboard-engine.md`). |
| REQ-CONT-111 | All contacts UI strings are localised (`22-internationalization.md`); name-order, date, and address formatting follow the active locale. Phone-type and address-context labels use the existing localisation keys. |
| REQ-CONT-112 | The contacts app meets the suite's accessibility baseline: labelled form controls, focus management on route change, and a non-visual fallback for the monogram avatar. |
| REQ-CONT-113 | The contacts app is responsive across the three breakpoints (`24-mobile-and-touch.md`): a single-pane list-then-detail flow on phone, list+detail split on tablet/desktop; touch targets and drag interactions (photo drop) work on touch. UI changes are verified in a real browser per the project's puppeteer requirement. |
| REQ-CONT-114 | Performance: list first paint and search-result update stay within the suite's interaction budgets (`13-nonfunctional.md`); virtualisation and paged `Contact/query` keep a large address book responsive. |
