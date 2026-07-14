/**
 * Contacts list store — paged Contact/query + Contact/get for the list view.
 *
 * Provides the data layer for ContactsListView.svelte (REQ-CONT-20..26):
 *   - Paged Contact/query + narrow Contact/get (LIST_ROW_PROPERTIES only).
 *   - Debounced search via Contact/query filter.text.
 *   - Sort by displayName / created / updated.
 *   - Address book scope (single book stays implicit; multiple books shown).
 *   - Live updates via sync.on('Contact') → Contact/changes reconciliation.
 */

import { jmap } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';
import { computeShiftClickRange } from '../list-selection/range-select';
import { appendPage, canLoadMore } from '../list-selection/paging';
import {
  selectAllVisible as sharedSelectAllVisible,
  toggleSelectAllVisible as sharedToggleSelectAllVisible,
} from '../list-selection/whole-set-selection';

export type SortProp = 'displayName' | 'created' | 'updated';

/** Scope the list to a specific group (by JMAP contact IDs of its members). */
export interface GroupScope {
  groupId: string;
  memberIds: string[];
}

export interface ContactListRow {
  id: string;
  displayName: string;
  /** Primary email, or org name, or primary phone — whichever is present first. */
  secondary: string;
  photoBlobId: string | null;
}

export interface AddressBook {
  id: string;
  name: string;
}

const PAGE_SIZE = 30;

/**
 * Max ids in the `destroy` array of a single `Contact/set` call, mirroring
 * the server's default `maxObjectsInSet` (internal/protojmap/server.go).
 * A whole-set delete (`bulkDelete` given a filter-scoped id enumeration,
 * re #221) can exceed that in one shot, so ids are destroyed in chunks of
 * this size, issued as separate `Contact/set` calls within a single JMAP
 * batch (one HTTP round-trip regardless of chunk count).
 */
const MAX_OBJECTS_IN_SET = 500;

/**
 * Page size used by `#enumerateAllMatchingIds` (re #221) to walk the full
 * filter-scoped id set for a "select all N matching" bulk action. Kept
 * separate from `PAGE_SIZE` -- this call fetches ids only (no `Contact/get`
 * row hydration), so a larger page is cheap.
 */
const ENUMERATE_PAGE_SIZE = 500;

/** Narrow property list fetched for each list row. Full Card fetched separately for detail. */
const LIST_ROW_PROPERTIES = ['id', 'name', 'emails', 'phones', 'organizations', 'media'];

// ── Pure helpers ─────────────────────────────────────────────────────────────

/**
 * Derive the display name from a JSContact Card object.
 * Preference order: name.full -> joined name.components -> primary email.
 */
export function deriveDisplayName(card: Record<string, unknown>): string {
  const nameObj = card.name as Record<string, unknown> | undefined;
  if (nameObj && typeof nameObj.full === 'string' && nameObj.full.trim()) {
    return nameObj.full.trim();
  }
  if (nameObj && Array.isArray(nameObj.components)) {
    const parts: string[] = [];
    for (const c of nameObj.components) {
      if (typeof c === 'object' && c !== null) {
        const v = (c as Record<string, unknown>).value;
        if (typeof v === 'string' && v.trim()) parts.push(v.trim());
      }
    }
    const joined = parts.join(' ').trim();
    if (joined) return joined;
  }
  // Fallback: use primary email as the display name.
  const emailMap = card.emails as Record<string, unknown> | undefined;
  if (emailMap) {
    for (const v of Object.values(emailMap)) {
      if (typeof v === 'object' && v !== null) {
        const addr = (v as Record<string, unknown>).address;
        if (typeof addr === 'string' && addr.includes('@')) return addr;
      }
    }
  }
  return '';
}

/**
 * Derive the secondary line for a list row.
 * Preference order: primary (pref=1 or first) email -> first org name -> first phone number.
 */
export function deriveSecondary(card: Record<string, unknown>): string {
  // Primary email: prefer pref=1, else first.
  const emailMap = card.emails as Record<string, unknown> | undefined;
  if (emailMap) {
    let firstEmail = '';
    let prefEmail = '';
    for (const v of Object.values(emailMap)) {
      if (typeof v === 'object' && v !== null) {
        const obj = v as Record<string, unknown>;
        const addr = typeof obj.address === 'string' ? obj.address : '';
        if (addr.includes('@')) {
          if (!firstEmail) firstEmail = addr;
          if (typeof obj.pref === 'number' && obj.pref === 1 && !prefEmail) prefEmail = addr;
        }
      }
    }
    const email = prefEmail || firstEmail;
    if (email) return email;
  }

  // Organization name.
  const orgsMap = card.organizations as Record<string, unknown> | undefined;
  if (orgsMap) {
    for (const v of Object.values(orgsMap)) {
      if (typeof v === 'object' && v !== null) {
        const obj = v as Record<string, unknown>;
        if (typeof obj.name === 'string' && obj.name.trim()) return obj.name.trim();
      }
    }
  }

  // Primary phone (phones is a map, RFC 9553 §2.5.3).
  const phoneMap = card.phones as Record<string, unknown> | undefined;
  if (phoneMap) {
    for (const v of Object.values(phoneMap)) {
      if (typeof v === 'object' && v !== null) {
        const obj = v as Record<string, unknown>;
        if (typeof obj.number === 'string' && obj.number.trim()) return obj.number.trim();
      }
    }
  }

  return '';
}

/**
 * Derive the fallback initial character for the avatar monogram.
 * Uses the first non-space character of the display name.
 */
export function deriveFallbackInitial(displayName: string): string {
  for (const ch of displayName) {
    if (ch.trim()) return ch.toUpperCase();
  }
  return '?';
}

/**
 * Derive the photo blob ID from a JSContact Card, if any.
 * Photos are stored under the `media` map with `type: "photo"`.
 */
function derivePhotoBlobId(card: Record<string, unknown>): string | null {
  const media = card.media as Record<string, unknown> | undefined;
  if (media) {
    for (const v of Object.values(media)) {
      if (typeof v === 'object' && v !== null) {
        const obj = v as Record<string, unknown>;
        if (obj.kind === 'photo' && typeof obj.blobId === 'string') return obj.blobId;
      }
    }
  }
  return null;
}

function parseRow(card: Record<string, unknown>): ContactListRow {
  return {
    id: String(card.id ?? ''),
    displayName: deriveDisplayName(card),
    secondary: deriveSecondary(card),
    photoBlobId: derivePhotoBlobId(card),
  };
}

// ── Store ─────────────────────────────────────────────────────────────────────

class ContactsListStore {
  rows = $state<ContactListRow[]>([]);
  total = $state<number | null>(null);
  status = $state<'idle' | 'loading' | 'loading-more' | 'ready' | 'error'>('idle');
  errorMessage = $state<string | null>(null);

  addressBooks = $state<AddressBook[]>([]);
  activeBookId = $state<string | null>(null);
  activeGroupId = $state<string | null>(null);
  sort = $state<SortProp>('displayName');
  searchText = $state('');

  /**
   * Multi-select state for bulk actions (delete, export). Cleared whenever
   * the visible row set changes wholesale (scope switch, search, sort, or
   * reload) so a stale selection never survives onto a different list.
   */
  selectedIds = $state<Set<string>>(new Set());
  /**
   * Id of the row a plain (non-shift) selection click last targeted --
   * the shift-click range anchor (re #202). Reset alongside
   * `selectedIds` whenever the visible row set changes wholesale.
   */
  selectAnchorId = $state<string | null>(null);
  /**
   * True while the user has accepted the "select all N matching" offer
   * (re #221 -- the contacts counterpart of the mail store's
   * `listWholeMailboxSelected`, issue #149). While true, `resolveSelectionIds`
   * resolves bulk actions against the full filter-scoped id set (fetched via
   * `#enumerateAllMatchingIds`) instead of just `selectedIds`, the currently
   * loaded window. Reset by `clearSelection`, `selectAllVisible`, and
   * `toggleSelectAllVisible`.
   */
  wholeSetSelected = $state(false);

  /** True when there are more pages to load. */
  get hasMore(): boolean {
    return this.total !== null && this.rows.length < this.total;
  }

  /** True when the contacts capability is available in the current session. */
  get hasCapability(): boolean {
    return jmap.hasCapability(Capability.Contacts);
  }

  #queryState: string | null = null;
  #debounceTimer: ReturnType<typeof setTimeout> | null = null;
  #unregisterSync: (() => void) | null = null;
  #activeGroupMemberIds: string[] | null = null;

  /**
   * Call once when the list view mounts: loads address books and the first
   * page of contacts. Registers the Contact sync handler.
   * Safe to call again (re-registers sync and reloads).
   */
  async init(): Promise<void> {
    this.#unregisterSync?.();
    this.#unregisterSync = sync.on('Contact', (newState, accountId) => {
      void this.#handleContactChange(newState, accountId);
    });
    await this.#loadAddressBooks();
    await this.#reload();
  }

  /** Unregister sync handler. Call when the list view unmounts. */
  destroy(): void {
    this.#unregisterSync?.();
    this.#unregisterSync = null;
    if (this.#debounceTimer !== null) {
      clearTimeout(this.#debounceTimer);
      this.#debounceTimer = null;
    }
  }

  /** Change sort property and reload from scratch. */
  setSort(s: SortProp): void {
    if (this.sort === s) return;
    this.sort = s;
    void this.#reload();
  }

  /**
   * Update search text with debounce. Issues a fresh Contact/query after
   * 300 ms of silence (REQ-CONT-22). Clearing text restores the full list.
   */
  setSearch(text: string): void {
    this.searchText = text;
    if (this.#debounceTimer !== null) clearTimeout(this.#debounceTimer);
    this.#debounceTimer = setTimeout(() => {
      this.#debounceTimer = null;
      void this.#reload();
    }, 300);
  }

  /** Scope the list to a specific address book (or null = all). Reloads. */
  setAddressBook(id: string | null): void {
    if (this.activeBookId === id && this.activeGroupId === null) return;
    this.activeBookId = id;
    this.activeGroupId = null;
    this.#activeGroupMemberIds = null;
    void this.#reload();
  }

  /**
   * Scope the list to a specific group's members. Pass null/null to clear
   * group scope and return to the normal (address-book-scoped) view.
   */
  setGroup(groupId: string | null, memberIds: string[] | null): void {
    if (this.activeGroupId === groupId) return;
    this.activeGroupId = groupId;
    this.#activeGroupMemberIds = memberIds;
    if (groupId !== null) this.activeBookId = null;
    void this.#reload();
  }

  /** Load the next page of results. No-op if no more pages or already loading. */
  async loadMore(): Promise<void> {
    if (
      !canLoadMore({
        isReady: this.status === 'ready',
        hasMore: this.hasMore,
        loadingMore: this.status === 'loading-more',
      })
    ) {
      return;
    }
    await this.#loadPage(this.rows.length, true);
  }

  // ── Multi-select (bulk actions) ────────────────────────────────────────

  /** Toggle whether `id` is in the selection set. */
  toggleSelected(id: string): void {
    const next = new Set(this.selectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.selectedIds = next;
    this.selectAnchorId = id;
  }

  /**
   * Handle a row-selection click on the bulk-select checkbox (re #202).
   * Shift-click, with a prior anchor still present in `visibleIds`,
   * replaces the selection with the contiguous range between the anchor
   * and `id`, inclusive, and leaves the anchor unchanged so repeated
   * shift-clicks keep extending from the same starting point. A plain
   * click falls back to the existing per-row toggle and moves the anchor
   * to `id`.
   */
  selectRowClick(id: string, shiftKey: boolean, visibleIds: string[]): void {
    if (shiftKey && this.selectAnchorId !== null) {
      this.wholeSetSelected = false;
      this.selectedIds = computeShiftClickRange(visibleIds, this.selectAnchorId, id);
      return;
    }
    this.toggleSelected(id);
  }

  /** Select every id currently in `ids` (typically the visible rows). */
  selectAllVisible(ids: string[]): void {
    this.wholeSetSelected = false;
    this.selectedIds = sharedSelectAllVisible(ids);
  }

  /**
   * Toggle select-all for the visible rows (re #221, mirrors the mail
   * store's `toggleSelectAllVisible`, issue #36). If every id in
   * `visibleIds` is already selected, clears the selection; otherwise
   * selects all of them.
   */
  toggleSelectAllVisible(visibleIds: string[]): void {
    this.wholeSetSelected = false;
    this.selectedIds = sharedToggleSelectAllVisible(visibleIds, this.selectedIds);
  }

  /**
   * Activate whole-set selection mode (re #221, mirrors the mail store's
   * `selectWholeMailbox`, issue #149): every contact matching the current
   * search/address-book scope, not just the loaded window. `selectedIds`
   * keeps showing the loaded window as checked; `resolveSelectionIds`
   * expands to the full filter-scoped id set when a bulk action runs.
   */
  selectAllMatching(): void {
    this.wholeSetSelected = true;
  }

  /** Clear the selection set, the shift-click anchor, and whole-set mode. */
  clearSelection(): void {
    this.selectAnchorId = null;
    this.wholeSetSelected = false;
    if (this.selectedIds.size === 0) return;
    this.selectedIds = new Set();
  }

  /**
   * Resolve the ids a bulk action should target (re #221): the loaded
   * selection normally, or -- while `wholeSetSelected` is true -- every id
   * matching the current filter scope, freshly enumerated via
   * `#enumerateAllMatchingIds`. Group scope is already the complete
   * matching set (its member list has no further pages), so whole-set mode
   * resolves to the plain selection there too.
   */
  async resolveSelectionIds(): Promise<string[]> {
    if (!this.wholeSetSelected || this.activeGroupId !== null) {
      return [...this.selectedIds];
    }
    return this.#enumerateAllMatchingIds();
  }

  /**
   * Enumerate every contact id matching the current search/address-book
   * filter, independent of what has loaded into `rows` (re #221). JMAP
   * Contacts has no bulk-by-query primitive, so a whole-set bulk action
   * resolves to this filter-scoped id enumeration and then acts on the
   * full list directly (`bulkDelete` chunks it into `Contact/set` calls
   * within `MAX_OBJECTS_IN_SET`).
   */
  async #enumerateAllMatchingIds(): Promise<string[]> {
    const accountId = this.#accountId();
    if (!accountId) return [];

    const filter = this.#buildFilter();
    const ids: string[] = [];
    let position = 0;
    for (;;) {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/query',
          {
            accountId,
            filter,
            sort: [{ property: this.sort, isAscending: this.sort === 'displayName' }],
            position,
            limit: ENUMERATE_PAGE_SIZE,
          },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') break;
      const args = resp[1] as { ids?: string[] };
      const page = args.ids ?? [];
      ids.push(...page);
      position += page.length;
      if (page.length < ENUMERATE_PAGE_SIZE) break;
    }
    return ids;
  }

  /** Build the `Contact/query` filter for the current search/address-book scope. */
  #buildFilter(): Record<string, unknown> | undefined {
    const text = this.searchText.trim();
    const bookId = this.activeBookId;
    const filter: Record<string, unknown> = {};
    if (text) filter['text'] = text;
    if (bookId) filter['addressBookId'] = bookId;
    return Object.keys(filter).length > 0 ? filter : undefined;
  }

  /**
   * Destroy the given contacts via `Contact/set`, chunked into groups of
   * at most `MAX_OBJECTS_IN_SET` (re #221 -- a whole-set selection can
   * enumerate far more ids than the server accepts in one `destroy` array),
   * issued as separate calls within a single JMAP batch. Removes destroyed
   * rows from the list and the selection set immediately rather than
   * waiting for the Contact/changes sync round-trip. Returns the ids that
   * could not be destroyed (empty on full success).
   *
   * If the destroy empties the visible window, re-runs the list query
   * (re #222) rather than leaving the view on a spliced-to-empty `rows`
   * array while the server still holds further matching pages -- the
   * contacts counterpart of the mail store's bulk-empty-refresh fix
   * (re #148).
   */
  async bulkDelete(ids: string[]): Promise<string[]> {
    if (ids.length === 0) return [];
    const accountId = this.#accountId();
    if (!accountId) return ids;

    const chunks: string[][] = [];
    for (let i = 0; i < ids.length; i += MAX_OBJECTS_IN_SET) {
      chunks.push(ids.slice(i, i + MAX_OBJECTS_IN_SET));
    }

    const { responses } = await jmap.batch((b) => {
      for (const chunk of chunks) {
        b.call('Contact/set', { accountId, destroy: chunk }, [Capability.Contacts]);
      }
    });

    const destroyed = new Set<string>();
    for (const resp of responses) {
      if (!resp || resp[0] === 'error') continue;
      const args = resp[1] as { destroyed?: string[] };
      for (const id of args.destroyed ?? []) destroyed.add(id);
    }
    const failed = ids.filter((id) => !destroyed.has(id));

    if (destroyed.size > 0) {
      this.rows = this.rows.filter((r) => !destroyed.has(r.id));
      if (this.total !== null) this.total = Math.max(0, this.total - destroyed.size);
      const nextSelected = new Set(this.selectedIds);
      for (const id of destroyed) nextSelected.delete(id);
      this.selectedIds = nextSelected;
      this.#refillIfEmptied();
    }

    return failed;
  }

  /**
   * Re-run the list query from the first page if a bulk delete just
   * emptied the visible window while the list is still loaded. The window
   * model has no discrete "page" state -- `rows` is always a growing slice
   * starting at position 0 -- so re-querying from scratch both backfills
   * the view and never strands the user on an out-of-range position, even
   * when the destroy emptied the last loaded page. Fire-and-forget, like
   * the mail store's #refreshFolderIfEmptied (re #148, re #222).
   */
  #refillIfEmptied(): void {
    if (this.rows.length === 0 && this.status === 'ready') {
      void this.#reload().catch((err) => {
        console.warn('contacts list refresh after bulk-empty failed', err);
      });
    }
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Contacts] ?? null;
  }

  async #loadAddressBooks(): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('AddressBook/get', { accountId, ids: null }, [Capability.Contacts]);
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') return;
      const args = resp[1] as { list?: unknown[] };
      this.addressBooks = (args.list ?? []).flatMap((book) => {
        if (typeof book !== 'object' || book === null) return [];
        const obj = book as Record<string, unknown>;
        const id = String(obj.id ?? '');
        const name = String(obj.name ?? '');
        return id ? [{ id, name }] : [];
      });
    } catch {
      // Soft fail — address books are not critical to show the list.
    }
  }

  async #reload(): Promise<void> {
    this.rows = [];
    this.total = null;
    this.#queryState = null;
    this.errorMessage = null;
    this.clearSelection();
    await this.#loadPage(0, false);
  }

  async #loadPage(position: number, append: boolean): Promise<void> {
    // Group scope: fetch only the group's member contacts directly.
    if (this.activeGroupId !== null) {
      await this.#loadGroupMembers();
      return;
    }

    const accountId = this.#accountId();
    if (!accountId) {
      this.status = 'error';
      this.errorMessage = 'no-account';
      return;
    }

    this.status = append ? 'loading-more' : 'loading';

    const sort = this.sort;
    const filter = this.#buildFilter();

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Contact/query',
          {
            accountId,
            filter,
            sort: [{ property: sort, isAscending: sort === 'displayName' }],
            position,
            limit: PAGE_SIZE,
            calculateTotal: true,
          },
          [Capability.Contacts],
        );
        b.call(
          'Contact/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: LIST_ROW_PROPERTIES,
          },
          [Capability.Contacts],
        );
      });

      const qResp = responses[0];
      const gResp = responses[1];

      if (!qResp || qResp[0] === 'error' || !gResp || gResp[0] === 'error') {
        this.status = 'error';
        this.errorMessage = 'fetch-failed';
        return;
      }

      const qArgs = qResp[1] as { total?: number; queryState?: string };
      const gArgs = gResp[1] as { list?: unknown[] };

      if (typeof qArgs.total === 'number') this.total = qArgs.total;
      this.#queryState = qArgs.queryState ?? null;

      const newRows = (gArgs.list ?? []).flatMap((card) => {
        if (typeof card !== 'object' || card === null) return [];
        const row = parseRow(card as Record<string, unknown>);
        return row.id ? [row] : [];
      });

      this.rows = append
        ? appendPage(this.rows, newRows, { pageSize: PAGE_SIZE, idOf: (r) => r.id }).items
        : newRows;
      this.status = 'ready';
    } catch {
      this.status = 'error';
      this.errorMessage = 'fetch-failed';
    }
  }

  /**
   * Group scope load: fetch member contacts directly by ID, bypassing
   * Contact/query. No pagination — group member counts are expected to be
   * small relative to maxObjectsInGet (500).
   */
  async #loadGroupMembers(): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) {
      this.status = 'error';
      this.errorMessage = 'no-account';
      return;
    }

    const memberIds = this.#activeGroupMemberIds ?? [];
    if (memberIds.length === 0) {
      this.rows = [];
      this.total = 0;
      this.#queryState = null;
      this.status = 'ready';
      return;
    }

    this.status = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/get',
          { accountId, ids: memberIds, properties: LIST_ROW_PROPERTIES },
          [Capability.Contacts],
        );
      });

      const gResp = responses[0];
      if (!gResp || gResp[0] === 'error') {
        this.status = 'error';
        this.errorMessage = 'fetch-failed';
        return;
      }
      const gArgs = gResp[1] as { list?: unknown[] };

      const newRows = (gArgs.list ?? []).flatMap((card) => {
        if (typeof card !== 'object' || card === null) return [];
        const row = parseRow(card as Record<string, unknown>);
        return row.id ? [row] : [];
      });

      // Sort by display name in group view.
      newRows.sort((a, b) => a.displayName.localeCompare(b.displayName));

      this.rows = newRows;
      this.total = newRows.length;
      this.#queryState = null;
      this.status = 'ready';
    } catch {
      this.status = 'error';
      this.errorMessage = 'fetch-failed';
    }
  }

  async #handleContactChange(_newState: string, accountId: string): Promise<void> {
    const myAccountId = this.#accountId();
    if (accountId !== myAccountId) return;
    if (this.status !== 'ready') return;

    // In group mode, reload the member list directly (queryState is not tracked).
    if (this.activeGroupId !== null) {
      void this.#reload();
      return;
    }

    const prevState = this.#queryState;
    if (!prevState) {
      void this.#reload();
      return;
    }

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/changes',
          { accountId, sinceState: prevState, maxChanges: 50 },
          [Capability.Contacts],
        );
      });

      const resp = responses[0];
      if (!resp || resp[0] === 'error') {
        void this.#reload();
        return;
      }

      const chArgs = resp[1] as {
        created?: string[];
        updated?: string[];
        destroyed?: string[];
        newState?: string;
        hasMoreChanges?: boolean;
      };

      if (chArgs.newState) this.#queryState = chArgs.newState;

      // Too many changes: full reload is simpler and avoids paging issues.
      if (chArgs.hasMoreChanges) {
        void this.#reload();
        return;
      }

      const created = chArgs.created ?? [];
      const updated = chArgs.updated ?? [];
      const destroyed = chArgs.destroyed ?? [];

      // Drop destroyed rows immediately.
      if (destroyed.length > 0) {
        const destroyedSet = new Set(destroyed);
        this.rows = this.rows.filter((r) => !destroyedSet.has(r.id));
        if (this.total !== null) this.total = Math.max(0, this.total - destroyed.length);
        if (this.selectedIds.size > 0) {
          const nextSelected = new Set(this.selectedIds);
          for (const id of destroyedSet) nextSelected.delete(id);
          this.selectedIds = nextSelected;
        }
      }

      const toFetch = [...created, ...updated];
      if (toFetch.length === 0) return;

      const { responses: getResponses } = await jmap.batch((b) => {
        b.call(
          'Contact/get',
          { accountId, ids: toFetch, properties: LIST_ROW_PROPERTIES },
          [Capability.Contacts],
        );
      });

      const gResp = getResponses[0];
      if (!gResp || gResp[0] === 'error') return;
      const gArgs = gResp[1] as { list?: unknown[] };

      const fetchedRows = (gArgs.list ?? []).flatMap((card) => {
        if (typeof card !== 'object' || card === null) return [];
        const row = parseRow(card as Record<string, unknown>);
        return row.id ? [row] : [];
      });

      const updatedSet = new Set(updated);
      const createdSet = new Set(created);

      // Replace updated rows in-place.
      let rows = this.rows.map((r) => {
        if (updatedSet.has(r.id)) {
          return fetchedRows.find((fr) => fr.id === r.id) ?? r;
        }
        return r;
      });

      // Prepend newly created rows.
      for (const newRow of fetchedRows) {
        if (createdSet.has(newRow.id) && !rows.some((r) => r.id === newRow.id)) {
          rows = [newRow, ...rows];
          if (this.total !== null) this.total += 1;
        }
      }

      // Re-sort alphabetically when the active sort is displayName.
      if (this.sort === 'displayName') {
        rows = [...rows].sort((a, b) => a.displayName.localeCompare(b.displayName));
      }

      this.rows = rows;
    } catch {
      // Soft fail on sync error — list stays at last-known state.
    }
  }
}

export const contactsListStore = new ContactsListStore();

// ── Test surface ──────────────────────────────────────────────────────────────

export const _internals_forTest = {
  deriveDisplayName,
  deriveSecondary,
  deriveFallbackInitial,
  parseRow,
};
