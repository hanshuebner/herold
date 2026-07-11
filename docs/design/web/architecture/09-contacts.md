# 09 — Contacts

How the contacts app is built inside the suite shell. Requirements live in
`../requirements/27-contacts.md`; server extensions in
`../../server/requirements/27-contacts.md`.

The contacts app is a lazy-loaded module in the single suite SPA
(`01-system-overview.md`). It reuses the shell's JMAP client
(`02-jmap-client.md`), the change-feed/state machinery (`03-sync-and-state.md`),
the design system (`06-design-system.md`), and the keyboard engine
(`05-keyboard-engine.md`). It owns three things the mail app does not: a
list/detail/edit surface over the `Contact` datatype, contact-photo blob
handling, and the client-side vCard-driver / duplicate-merge flows.

## Data model in the client

The client's canonical object is the JSContact `Card` returned by
`Contact/get` — held verbatim, edited as a whole, written back with
`Contact/set` (`../requirements/27-contacts.md` REQ-CONT-01). The server types
`name`, `emails`, `phones`, `addresses`, `organizations`, `titles`, `kind`,
`uid`, `version` and round-trips everything else; the client mirrors that: a
typed view model for the fields the forms edit structurally, plus a
pass-through of any property the client did not touch, so an edit never drops
an unknown property.

Two derived shapes sit on top of the `Card`:

- **List row** — `{ id, displayName, secondary, photoBlobId }`, projected from
  `Contact/query` + a narrow `Contact/get` (the properties the row renders),
  never the full card. Feeds the virtualised list.
- **Suggestion** — the existing `lib/contacts/store` `{ id, name, email }`
  cache that backs compose autocomplete. The contacts app is now the authoritative
  writer into this cache; it merges `Contact/query` results with seen-address
  history and `Directory/search` (`../requirements/27-contacts.md` REQ-CONT-100).

## Fetch and query

All reads go through the shared JMAP client's batch wrapper. The list view is a
back-referenced `Contact/query` -> `Contact/get`:

```
const [rows] = await jmap.batch(b => {
  const q = b.call('Contact/query', {
    filter: text ? { text } : undefined,
    sort: [{ property: sortProp, isAscending: sortProp === 'displayName' }],
    position, limit,
    calculateTotal: true,
  });
  const g = b.call('Contact/get', {
    '#ids': { resultOf: q.id, name: 'Contact/query', path: '/ids' },
    properties: LIST_ROW_PROPERTIES,
  });
  return [g];
});
```

- Search maps directly to the server `text` filter (substring over name,
  emails, phones, orgs, titles, uid — `search_blob`). No client-side full
  scan for search.
- Sort maps to `displayName` / `created` / `updated`. `position`/`limit` page
  the virtualised list; `queryState` is retained to reconcile against pushed
  changes.
- Detail view fetches the full `Card` (all properties) for one id.

## Sync and live updates

The contacts app subscribes to the `Contact` type on the same EventSource push
channel the mail app uses (`03-sync-and-state.md`). On a pushed `Contact` state
change it runs `Contact/changes` from the last-seen state, then a targeted
`Contact/get` for `created`/`updated` ids, and drops `destroyed` ids from the
list and any open detail. The list re-sorts locally within the loaded window
and re-issues the paged `Contact/query` when the change falls outside it. There
is no polling (`00-scope.md` G2).

State strings (`contact_state` / `queryState`) are held per the sync doc's
rules; the contacts app does not invent its own persistence and stays
online-first (`00-scope.md` NG2).

## Editing and optimistic writes

The edit form builds a JSContact `Card` from the typed view model and the
preserved pass-through, then writes it:

- **Create** — `Contact/set { create: { tmp: card } }`; the `Card` carries
  `version: "1.0"` and `kind` (default `individual`), no client-minted `uid`
  (`../requirements/27-contacts.md` REQ-CONT-02).
- **Update** — `Contact/set { update: { [id]: patch } }` as a JSON Merge Patch
  over changed top-level properties, matching the server's merge-patch update
  semantics.
- **Destroy** — `Contact/set { destroy: [id] }`.

All three are optimistic (`11-optimistic-ui.md`): the store applies the change
locally, then reconciles against the `Contact/set` response. A
`notCreated`/`notUpdated`/`notDestroyed` `SetError` reverts the local change and
surfaces the server error inline. The dirty-form guard (REQ-CONT-52) lives in
the edit route component.

## Photos

Photo upload is a two-step the app orchestrates through the shared client
(`../../server/requirements/27-contacts.md` REQ-CTS-01..05):

1. Crop client-side to a square, then `Blob/upload` the image; keep the
   returned `blobId` + media type.
2. Write a JSContact photo/media entry referencing that `blobId` into the
   `Card` via `Contact/set`. The server validates the blob and roots it against
   GC; removing/replacing the photo drops the reference and lets the old blob
   be collected.

Display fetches the photo through the JMAP blob download path for the
referenced `blobId`, cached in-session. With no photo, the app renders the
shared deterministic monogram avatar (`06-design-system.md`,
`RecipientHoverCard` fallback) — never a third-party avatar service
(`00-scope.md` NG9).

## vCard import / export

The vCard converter is server-side (`../../server/requirements/27-contacts.md`
REQ-CTS-10..24); the client is the driver and the UI:

- **Import** — upload the `.vcf` to the server import path; render its per-card
  result (created / skipped / failed-with-reason) and its duplicate-candidate
  report. For flagged candidates the app runs the merge flow (below) before
  committing. Progress and cancel are driven from the import summary; a
  cancelled run keeps already-created contacts.
- **Export** — request a `.vcf` for one contact, the current
  selection/filter, or the whole address book; present the download and any
  unrepresentable-property warnings the server returns.

The client does not parse or generate vCard itself — keeping the wire parser
(and its fuzz target) in one place on the server.

## Duplicate detection and merge

Detection is client-side over `Contact/query` results
(`../requirements/27-contacts.md` REQ-CONT-90): cluster by shared email, by
normalised phone, and by close display-name match, using the server's
`primary_email` exact-match and `text` filters to keep candidate lookup cheap
rather than pulling the whole book. Dismissed non-duplicate pairs are
remembered client-side per principal so they are not re-flagged.

Merge is a single atomic `Contact/set` (`../../server/requirements/27-contacts.md`
REQ-CTS-30): the app builds the merged `Card` (union of multi-valued
properties, user-chosen name/photo), then in one call writes the surviving card
and destroys the other cluster members. `/set` atomicity means a partial
failure leaves every pre-merge contact intact; the UI reconciles optimistically
like any other write.

## Routing and layout

Routes (`../requirements/27-contacts.md` REQ-CONT-11) hang off the shell
router: `#/contacts`, `#/contacts/<id>`, `#/contacts/<id>/edit`,
`#/contacts/new`. Layout follows the responsive model (`24-mobile-and-touch.md`):
list-then-detail single pane on phone, list+detail split on tablet/desktop. The
list is virtualised and pages `Contact/query`; the detail and edit routes
operate on one full `Card`.

## Groups and address books

Address books are read via `AddressBook/get` and stay implicit when there is
one (`../requirements/27-contacts.md` REQ-CONT-70). Groups are `kind: "group"`
cards whose membership the client resolves; group membership edits and
group create/rename/delete are ordinary `Contact/set` operations on the group
card, and deleting a group never touches its members
(`../../server/requirements/27-contacts.md` REQ-CTS-32).
