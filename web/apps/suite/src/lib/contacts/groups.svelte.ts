/**
 * Groups store — manages JSContact group cards (kind:"group").
 *
 * Groups are regular JSContact cards with kind:"group" and a `members` map
 * whose keys are JMAP contact IDs (not UIDs). This store:
 *   - Loads all group cards from the server (Contact/query + Contact/get,
 *     filtered client-side by kind==="group")
 *   - Tracks group IDs so the list view can filter them out of the normal view
 *   - Provides create/rename/delete operations (REQ-CONT-72)
 *   - Provides add/remove member operations (REQ-CONT-71)
 *   - Subscribes to the Contact sync channel and reloads on changes
 *
 * Confirmed wire format (round-trip, 2026-07-11):
 *   members: { "<jmapContactId>": true, ... }
 *   Keys are JMAP contact IDs, NOT UIDs.
 */

import { jmap } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';

export interface GroupCard {
  id: string;
  name: string;
  /** JMAP contact ID -> true; keys are JMAP IDs, not UIDs. */
  members: Record<string, boolean>;
  addressBookId: string;
}

class GroupsStore {
  groups = $state<GroupCard[]>([]);
  /** Set of all group-card JMAP IDs; used by the list view to filter them out. */
  groupIds = $state<Set<string>>(new Set());
  status = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');

  #unregisterSync: (() => void) | null = null;

  /**
   * Load all group cards from the server. Safe to call repeatedly.
   * Registers a Contact sync handler to stay up-to-date.
   */
  async load(): Promise<void> {
    if (this.status === 'loading') return;
    this.#unregisterSync?.();
    this.#unregisterSync = sync.on('Contact', (_newState, accountId) => {
      const myAccountId = this.#accountId();
      if (accountId === myAccountId) void this.#reload();
    });
    await this.#reload();
  }

  /** Unregister sync handler. Call when the view unmounts. */
  destroy(): void {
    this.#unregisterSync?.();
    this.#unregisterSync = null;
  }

  /** Find a group by its JMAP ID. */
  getGroup(id: string): GroupCard | undefined {
    return this.groups.find((g) => g.id === id);
  }

  /** Return all groups of which this contact (by JMAP ID) is a member. */
  getGroupsForContact(contactId: string): GroupCard[] {
    return this.groups.filter((g) => g.members[contactId] === true);
  }

  /**
   * Create a new group card with the given name and return its JMAP ID,
   * or null on failure.
   */
  async createGroup(name: string, addressBookId: string): Promise<string | null> {
    const accountId = this.#accountId();
    if (!accountId) return null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          {
            accountId,
            create: {
              g: {
                version: '1.0',
                kind: 'group',
                addressBookId,
                name: { full: name.trim() },
                members: {},
              },
            },
          },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') return null;
      const args = resp[1] as { created?: Record<string, { id?: string }> };
      const newId = args.created?.['g']?.id ?? null;
      if (newId) await this.#reload();
      return newId;
    } catch {
      return null;
    }
  }

  /** Rename a group. Returns true on success. */
  async renameGroup(groupId: string, newName: string): Promise<boolean> {
    const accountId = this.#accountId();
    if (!accountId) return false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          {
            accountId,
            update: { [groupId]: { name: { full: newName.trim() } } },
          },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') return false;
      const args = resp[1] as { notUpdated?: Record<string, unknown> };
      if (args.notUpdated?.[groupId]) return false;
      // Optimistic local update; confirmed on next sync.
      this.groups = this.groups.map((g) =>
        g.id === groupId ? { ...g, name: newName.trim() } : g,
      );
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Delete a group card. Destroys ONLY the group; member contacts are
   * unchanged (REQ-CONT-72 / server REQ-CTS-32). Returns true on success.
   */
  async deleteGroup(groupId: string): Promise<boolean> {
    const accountId = this.#accountId();
    if (!accountId) return false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          { accountId, destroy: [groupId] },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') return false;
      const args = resp[1] as { notDestroyed?: Record<string, unknown> };
      if (args.notDestroyed?.[groupId]) return false;
      // Remove from local state immediately.
      this.groups = this.groups.filter((g) => g.id !== groupId);
      this.groupIds = new Set([...this.groupIds].filter((id) => id !== groupId));
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Add a contact to a group by updating the group card's members map.
   * Returns true on success.
   */
  async addMember(groupId: string, contactId: string): Promise<boolean> {
    const group = this.getGroup(groupId);
    if (!group) return false;
    const accountId = this.#accountId();
    if (!accountId) return false;
    const newMembers = buildAddMemberPatch(group.members, contactId);
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          { accountId, update: { [groupId]: { members: newMembers } } },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') return false;
      const args = resp[1] as { notUpdated?: Record<string, unknown> };
      if (args.notUpdated?.[groupId]) return false;
      this.groups = this.groups.map((g) =>
        g.id === groupId ? { ...g, members: newMembers } : g,
      );
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Remove a contact from a group by updating the group card's members map.
   * Returns true on success.
   */
  async removeMember(groupId: string, contactId: string): Promise<boolean> {
    const group = this.getGroup(groupId);
    if (!group) return false;
    const accountId = this.#accountId();
    if (!accountId) return false;
    const newMembers = buildRemoveMemberPatch(group.members, contactId);
    // null tells the server to remove the members property entirely when empty.
    const updatePayload: Record<string, unknown> = {
      members: Object.keys(newMembers).length > 0 ? newMembers : null,
    };
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          { accountId, update: { [groupId]: updatePayload } },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') return false;
      const args = resp[1] as { notUpdated?: Record<string, unknown> };
      if (args.notUpdated?.[groupId]) return false;
      this.groups = this.groups.map((g) =>
        g.id === groupId ? { ...g, members: newMembers } : g,
      );
      return true;
    } catch {
      return false;
    }
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Contacts] ?? null;
  }

  async #reload(): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;
    this.status = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Contact/query',
          {
            accountId,
            sort: [{ property: 'displayName', isAscending: true }],
            limit: 1000,
          },
          [Capability.Contacts],
        );
        b.call(
          'Contact/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: ['id', 'kind', 'name', 'members', 'addressBookId'],
          },
          [Capability.Contacts],
        );
      });

      const gResp = responses[1];
      if (!gResp || gResp[0] === 'error') {
        this.status = 'error';
        return;
      }
      const gArgs = gResp[1] as { list?: unknown[] };
      const all = gArgs.list ?? [];

      const newGroupIds = new Set<string>();
      const newGroups: GroupCard[] = [];

      for (const card of all) {
        if (typeof card !== 'object' || card === null) continue;
        const c = card as Record<string, unknown>;
        const id = String(c.id ?? '');
        if (!id) continue;
        if (c.kind === 'group') {
          newGroupIds.add(id);
          const nameObj = c.name as Record<string, unknown> | undefined;
          const name =
            typeof nameObj?.full === 'string' && nameObj.full.trim()
              ? nameObj.full.trim()
              : id;
          newGroups.push({
            id,
            name,
            members: (c.members as Record<string, boolean>) ?? {},
            addressBookId: String(c.addressBookId ?? ''),
          });
        }
      }

      this.groups = newGroups;
      this.groupIds = newGroupIds;
      this.status = 'ready';
    } catch {
      this.status = 'error';
    }
  }
}

export const groupsStore = new GroupsStore();

// ── Member patch helpers ──────────────────────────────────────────────────────

/**
 * Build an updated members map with the given contact added.
 * Preserves all existing members.
 */
export function buildAddMemberPatch(
  currentMembers: Record<string, boolean>,
  contactId: string,
): Record<string, boolean> {
  return { ...currentMembers, [contactId]: true };
}

/**
 * Build an updated members map with the given contact removed.
 * Returns an empty object if this was the last member.
 */
export function buildRemoveMemberPatch(
  currentMembers: Record<string, boolean>,
  contactId: string,
): Record<string, boolean> {
  const updated = { ...currentMembers };
  delete updated[contactId];
  return updated;
}

// ── Test surface ──────────────────────────────────────────────────────────────

export const _internals_forTest = {
  buildAddMemberPatch,
  buildRemoveMemberPatch,
};
