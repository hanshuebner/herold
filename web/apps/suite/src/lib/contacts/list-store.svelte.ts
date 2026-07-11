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

export type SortProp = 'displayName' | 'created' | 'updated';

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
        if (obj.type === 'photo' && typeof obj.blobId === 'string') return obj.blobId;
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
  sort = $state<SortProp>('displayName');
  searchText = $state('');

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
    if (this.activeBookId === id) return;
    this.activeBookId = id;
    void this.#reload();
  }

  /** Load the next page of results. No-op if no more pages or already loading. */
  async loadMore(): Promise<void> {
    if (this.status !== 'ready') return;
    if (!this.hasMore) return;
    await this.#loadPage(this.rows.length, true);
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
    await this.#loadPage(0, false);
  }

  async #loadPage(position: number, append: boolean): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) {
      this.status = 'error';
      this.errorMessage = 'no-account';
      return;
    }

    this.status = append ? 'loading-more' : 'loading';

    const text = this.searchText.trim();
    const sort = this.sort;
    const bookId = this.activeBookId;

    const filter: Record<string, unknown> = {};
    if (text) filter['text'] = text;
    if (bookId) filter['addressBookId'] = bookId;

    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Contact/query',
          {
            accountId,
            filter: Object.keys(filter).length > 0 ? filter : undefined,
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

      this.rows = append ? [...this.rows, ...newRows] : newRows;
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
