/**
 * Contacts store — caches the principal's address book for use by the
 * compose-window autocomplete. Each entry is a flat
 * {name, email, contactId} record so the suggestion list can be filtered
 * cheaply without re-walking the JSContact JSON on every keystroke.
 *
 * Phase 1: load all contacts once on first compose (Contact/get with
 * ids: null), reload on AddressBook/Contact state-change events.
 */

import { jmap, strict } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';

export interface ContactSuggestion {
  /** JMAP Contact id (string). */
  id: string;
  /** Display name — JSContact name.full || derived from name components. */
  name: string;
  /** Single email address. A contact with N emails produces N suggestions. */
  email: string;
}

class Contacts {
  /** Status: idle / loading / ready / error. */
  status = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  /** Flattened (name, email) suggestions. */
  suggestions = $state<ContactSuggestion[]>([]);

  /** Idempotent: no-op when ready / loading. */
  async load(): Promise<void> {
    if (this.status === 'loading' || this.status === 'ready') return;
    const accountId = this.#accountId();
    if (!accountId) return;
    this.status = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/get',
          { accountId, ids: null },
          [Capability.Contacts],
        );
      });
      strict(responses);
      const args = responses[0]![1] as { list: unknown[] };
      const flat: ContactSuggestion[] = [];
      for (const card of args.list ?? []) {
        if (typeof card !== 'object' || card === null) continue;
        const obj = card as Record<string, unknown>;
        const id = String(obj.id ?? '');
        if (!id) continue;
        const name = extractName(obj);
        for (const email of extractEmails(obj)) {
          flat.push({ id, name, email });
        }
      }
      this.suggestions = flat;
      this.status = 'ready';
    } catch {
      // Soft-fail: contact autocomplete is a nice-to-have. The compose
      // form still works without it.
      this.status = 'error';
      this.suggestions = [];
    }
  }

  /**
   * Filter contacts by case-insensitive substring match across name and
   * email; cap at `limit`. Returns input order (alphabetical by name)
   * when the query is empty.
   */
  filter(query: string, limit = 8): ContactSuggestion[] {
    const q = query.trim().toLowerCase();
    if (!q) return this.suggestions.slice(0, limit);
    const out: ContactSuggestion[] = [];
    for (const s of this.suggestions) {
      if (
        s.name.toLowerCase().includes(q) ||
        s.email.toLowerCase().includes(q)
      ) {
        out.push(s);
        if (out.length >= limit) break;
      }
    }
    return out;
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Contacts] ?? null;
  }
}

export const contacts = new Contacts();

/**
 * Extract a display name from a JSContact card — prefer name.full,
 * otherwise concatenate ordered name.components, otherwise fall back
 * to the empty string.
 */
function extractName(card: Record<string, unknown>): string {
  const nameObj = card.name as Record<string, unknown> | undefined;
  if (nameObj && typeof nameObj.full === 'string' && nameObj.full.trim()) {
    return nameObj.full.trim();
  }
  if (nameObj && Array.isArray(nameObj.components)) {
    const parts: string[] = [];
    for (const c of nameObj.components) {
      if (typeof c === 'object' && c !== null) {
        const v = (c as Record<string, unknown>).value;
        if (typeof v === 'string') parts.push(v);
      }
    }
    return parts.join(' ').trim();
  }
  return '';
}

/**
 * Extract every email address from a JSContact card. Emails are stored
 * as a map keyed by the client's opaque id; values have an `address`
 * field (RFC 9553 §2.5.2).
 */
function extractEmails(card: Record<string, unknown>): string[] {
  const map = card.emails as Record<string, unknown> | undefined;
  if (!map) return [];
  const out: string[] = [];
  for (const v of Object.values(map)) {
    if (typeof v === 'object' && v !== null) {
      const addr = (v as Record<string, unknown>).address;
      if (typeof addr === 'string' && addr.includes('@')) out.push(addr);
    }
  }
  return out;
}
