/**
 * ManagedRule store — Wave 3.15, REQ-FLT-01..31.
 *
 * Manages the JMAP `ManagedRule` datatype (multi-row per principal) exposed
 * by the `https://netzhansa.com/jmap/managed-rules` capability.
 *
 * Pattern mirrors category-settings.svelte.ts: reactive $state, actions for
 * CRUD + reorder + enable/disable, optimistic UI for enabled/order changes,
 * non-optimistic (server-first) for create/update/delete.
 *
 * Wire capability: `https://netzhansa.com/jmap/managed-rules`
 * Methods: ManagedRule/{get, set, query, changes}
 *          Thread/mute, Thread/unmute
 *          BlockedSender/set
 */

import { jmap, strict } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';
import { toast } from '../toast/toast.svelte';
import { Capability } from '../jmap/types';

// ── Wire types ─────────────────────────────────────────────────────────────

export interface RuleCondition {
  field: string;
  op: string;
  value: string;
}

export interface RuleAction {
  kind: string;
  params?: Record<string, unknown>;
}

export interface ManagedRule {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
  conditions: RuleCondition[];
  actions: RuleAction[];
}

// Valid condition field values per the server contract.
export type ConditionField =
  | 'from'
  | 'to'
  | 'subject'
  | 'has-attachment'
  | 'thread-id'
  | 'from-domain';

// Valid condition op values.
export type ConditionOp = 'contains' | 'equals' | 'wildcard-match';

// Valid action kind values.
export type ActionKind =
  | 'apply-label'
  | 'skip-inbox'
  | 'mark-read'
  | 'delete'
  | 'forward';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

// ── Helpers ────────────────────────────────────────────────────────────────

/**
 * Returns true when the action set contains both `delete` and `apply-label`.
 * Per REQ-FLT-13: delete short-circuits, making apply-label meaningless —
 * the editor surfaces this as a validation error.
 */
export function hasDeleteApplyLabelConflict(actions: RuleAction[]): boolean {
  const kinds = new Set(actions.map((a) => a.kind));
  return kinds.has('delete') && kinds.has('apply-label');
}

/**
 * True when the given rule is a thread-mute rule (single thread-id condition,
 * skip-inbox + mark-read actions). Used by isThreadMuted.
 */
export function isThreadMuteRule(rule: ManagedRule, threadId: string): boolean {
  return (
    rule.enabled &&
    rule.conditions.length === 1 &&
    rule.conditions[0]!.field === 'thread-id' &&
    rule.conditions[0]!.value === threadId
  );
}

/**
 * True when the given rule is a block-sender rule (single from-equals
 * condition, delete action). Used to populate the Blocked Senders view.
 */
export function isBlockedSenderRule(rule: ManagedRule): boolean {
  return (
    rule.conditions.length === 1 &&
    rule.conditions[0]!.field === 'from' &&
    rule.conditions[0]!.op === 'equals' &&
    rule.actions.length === 1 &&
    rule.actions[0]!.kind === 'delete'
  );
}

/**
 * Extract a readable sender address from a blocked-sender rule.
 * Returns null for rules that don't match the blocked-sender shape.
 */
export function blockedSenderAddress(rule: ManagedRule): string | null {
  if (!isBlockedSenderRule(rule)) return null;
  return rule.conditions[0]!.value;
}

// ── Store ──────────────────────────────────────────────────────────────────

class ManagedRulesStore {
  loadStatus = $state<LoadStatus>('idle');
  loadError = $state<string | null>(null);

  /** All managed rules in sort-order order. */
  rules = $state<ManagedRule[]>([]);

  #state = $state<string | null>(null);

  constructor() {
    sync.on('ManagedRule', (newState) => {
      void this.#onStateChange(newState);
    });
  }

  async #onStateChange(newState: string): Promise<void> {
    if (newState === this.#state) return;
    try {
      await this.load(true);
    } catch (err) {
      console.error('ManagedRule reload after state change failed', err);
    }
    this.#state = newState;
  }

  /** True when the server advertises the managed-rules capability. */
  get available(): boolean {
    return jmap.hasCapability(Capability.HeroldManagedRules);
  }

  /**
   * Load all rules from the server via ManagedRule/get.
   * Idempotent when already 'ready'; force=true bypasses the check.
   */
  async load(force = false): Promise<void> {
    if (!this.available) return;
    if (!force && this.loadStatus === 'ready') return;
    if (this.loadStatus === 'loading') return;

    const accountId = this.#accountId;
    if (!accountId) {
      this.loadStatus = 'error';
      this.loadError = 'No Mail account on this session';
      return;
    }

    this.loadStatus = 'loading';
    this.loadError = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'ManagedRule/get',
          { accountId, ids: null },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      const args = (responses[0] as [string, { list: ManagedRule[]; state: string }, string])[1];
      const list: ManagedRule[] = args.list ?? [];
      this.rules = [...list].sort((a, b) => a.order - b.order);
      if (typeof args.state === 'string') this.#state = args.state;
      this.loadStatus = 'ready';
    } catch (err) {
      this.loadStatus = 'error';
      this.loadError = err instanceof Error ? err.message : String(err);
    }
  }

  // ── CRUD actions ────────────────────────────────────────────────────────

  /**
   * Create a new managed rule. Non-optimistic: dispatches to the server and
   * reloads on success. Returns the created rule on success, null on failure.
   */
  async create(
    rule: Omit<ManagedRule, 'id'>,
  ): Promise<ManagedRule | null> {
    const accountId = this.#accountId;
    if (!accountId) return null;
    const tempKey = 'new';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'ManagedRule/set',
          {
            accountId,
            create: { [tempKey]: { ...rule } },
          },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      const result = (responses[0] as [string, {
        created?: Record<string, ManagedRule>;
        notCreated?: Record<string, { type: string; description?: string }>;
      }, string])[1];
      const failure = result.notCreated?.[tempKey];
      if (failure) {
        toast.show({
          message: failure.description ?? `Create failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return null;
      }
      const created = result.created?.[tempKey];
      if (created) {
        this.rules = [...this.rules, created].sort((a, b) => a.order - b.order);
        return created;
      }
      await this.load(true);
      return null;
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : 'Create failed',
        kind: 'error',
        timeoutMs: 6000,
      });
      return null;
    }
  }

  /**
   * Update an existing rule. Non-optimistic. Returns true on success.
   */
  async update(id: string, patches: Partial<Omit<ManagedRule, 'id'>>): Promise<boolean> {
    const accountId = this.#accountId;
    if (!accountId) return false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'ManagedRule/set',
          { accountId, update: { [id]: patches } },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      const result = (responses[0] as [string, {
        updated?: Record<string, ManagedRule | null>;
        notUpdated?: Record<string, { type: string; description?: string }>;
      }, string])[1];
      const failure = result.notUpdated?.[id];
      if (failure) {
        toast.show({
          message: failure.description ?? `Update failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return false;
      }
      // Merge the updated rule into local state.
      this.rules = this.rules.map((r) => {
        if (r.id !== id) return r;
        const server = result.updated?.[id];
        return server ? server : { ...r, ...patches };
      });
      return true;
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : 'Update failed',
        kind: 'error',
        timeoutMs: 6000,
      });
      return false;
    }
  }

  /** Destroy a rule. Non-optimistic. Returns true on success. */
  async delete(id: string): Promise<boolean> {
    const accountId = this.#accountId;
    if (!accountId) return false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'ManagedRule/set',
          { accountId, destroy: [id] },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      const result = (responses[0] as [string, {
        destroyed?: string[];
        notDestroyed?: Record<string, { type: string; description?: string }>;
      }, string])[1];
      const failure = result.notDestroyed?.[id];
      if (failure) {
        toast.show({
          message: failure.description ?? `Delete failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return false;
      }
      this.rules = this.rules.filter((r) => r.id !== id);
      return true;
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : 'Delete failed',
        kind: 'error',
        timeoutMs: 6000,
      });
      return false;
    }
  }

  /**
   * Toggle a rule's enabled state. Optimistic: applies locally immediately,
   * reverts on failure.
   */
  async setEnabled(id: string, enabled: boolean): Promise<void> {
    const rule = this.rules.find((r) => r.id === id);
    if (!rule) return;
    const prev = rule.enabled;
    this.rules = this.rules.map((r) => (r.id === id ? { ...r, enabled } : r));
    const ok = await this.update(id, { enabled });
    if (!ok) {
      this.rules = this.rules.map((r) => (r.id === id ? { ...r, enabled: prev } : r));
    }
  }

  /**
   * Set the sort order on a rule. Optimistic locally; persisted to server.
   */
  async setOrder(id: string, order: number): Promise<void> {
    const rule = this.rules.find((r) => r.id === id);
    if (!rule) return;
    const prev = rule.order;
    this.rules = this.rules
      .map((r) => (r.id === id ? { ...r, order } : r))
      .sort((a, b) => a.order - b.order);
    const ok = await this.update(id, { order });
    if (!ok) {
      this.rules = this.rules
        .map((r) => (r.id === id ? { ...r, order: prev } : r))
        .sort((a, b) => a.order - b.order);
    }
  }

  // ── Mute thread ─────────────────────────────────────────────────────────

  /** True when the given thread has an active mute rule. */
  isThreadMuted(threadId: string): boolean {
    return this.rules.some((r) => isThreadMuteRule(r, threadId));
  }

  /**
   * Mute a thread: calls Thread/mute on the server, which creates a
   * ManagedRule with condition thread-id == threadId and actions
   * skip-inbox + mark-read. Reloads the rules list on success.
   */
  async muteThread(threadId: string): Promise<void> {
    const accountId = this.#accountId;
    if (!accountId) return;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Thread/mute',
          { accountId, threadId },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      await this.load(true);
      toast.show({ message: 'Thread muted' });
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : 'Mute failed',
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /**
   * Unmute a thread: calls Thread/unmute, which disables the matching rule.
   */
  async unmuteThread(threadId: string): Promise<void> {
    const accountId = this.#accountId;
    if (!accountId) return;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Thread/unmute',
          { accountId, threadId },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      await this.load(true);
      toast.show({ message: 'Thread unmuted' });
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : 'Unmute failed',
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  // ── Block sender ─────────────────────────────────────────────────────────

  /**
   * Block a sender address: calls BlockedSender/set. The server creates (or
   * re-enables) a ManagedRule with condition from == address and action delete.
   * Returns true on success.
   */
  async blockSender(address: string): Promise<boolean> {
    const accountId = this.#accountId;
    if (!accountId) return false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'BlockedSender/set',
          { accountId, address },
          [Capability.HeroldManagedRules],
        );
      });
      strict(responses);
      await this.load(true);
      toast.show({ message: `Blocked ${address}` });
      return true;
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Block failed';
      // Surface inline errors without a generic toast — callers show the
      // error in the confirmation modal.
      throw new Error(msg);
    }
  }

  /**
   * Unblock a sender. Finds the matching blocked-sender rule and deletes it.
   * Returns true on success.
   */
  async unblockSender(address: string): Promise<boolean> {
    const rule = this.rules.find(
      (r) => isBlockedSenderRule(r) && r.conditions[0]!.value === address,
    );
    if (!rule) {
      toast.show({
        message: `No block rule found for ${address}`,
        kind: 'error',
        timeoutMs: 4000,
      });
      return false;
    }
    const ok = await this.delete(rule.id);
    if (ok) toast.show({ message: `Unblocked ${address}` });
    return ok;
  }

  // ── Test filter ──────────────────────────────────────────────────────────

  /**
   * Run Email/query matching the given conditions against the user's existing
   * mail, returning the count of matching threads. Used by the "Test against
   * existing mail" affordance per REQ-FLT-21. Informational only; no mutation.
   */
  async testFilter(conditions: RuleCondition[]): Promise<number> {
    const accountId = this.#accountId;
    if (!accountId) return 0;

    const filter = buildEmailQueryFilter(conditions);
    if (!filter) return 0;

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Email/query',
        {
          accountId,
          filter,
          collapseThreads: true,
          calculateTotal: true,
          limit: 0,
        },
        [Capability.Mail],
      );
    });
    strict(responses);
    const result = (responses[0] as [string, { total?: number; ids: string[] }, string])[1];
    return result.total ?? 0;
  }

  // ── Internals ────────────────────────────────────────────────────────────

  get #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Mail] ?? null;
  }
}

/**
 * Convert a structured condition list to an Email/query filter object.
 * Returns null when no conditions are present (would match all mail).
 */
export function buildEmailQueryFilter(
  conditions: RuleCondition[],
): Record<string, unknown> | null {
  if (conditions.length === 0) return null;

  const filters: Record<string, unknown>[] = [];
  for (const c of conditions) {
    const f = conditionToFilter(c);
    if (f) filters.push(f);
  }
  if (filters.length === 0) return null;
  if (filters.length === 1) return filters[0]!;
  return { operator: 'AND', conditions: filters };
}

function conditionToFilter(c: RuleCondition): Record<string, unknown> | null {
  switch (c.field) {
    case 'from':
      return { from: c.value };
    case 'to':
      return { to: c.value };
    case 'subject':
      if (c.op === 'contains') return { subject: c.value };
      if (c.op === 'equals') return { subject: c.value };
      return null;
    case 'has-attachment':
      return { hasAttachment: c.value === 'true' };
    case 'thread-id':
      return { inThread: c.value };
    case 'from-domain': {
      // No direct JMAP filter for from-domain; approximate with from wildcard.
      const domain = c.value.replace(/^@/, '');
      return { from: `@${domain}` };
    }
    default:
      return null;
  }
}

export const managedRules = new ManagedRulesStore();

export const _internals_forTest = {
  hasDeleteApplyLabelConflict,
  isThreadMuteRule,
  isBlockedSenderRule,
  blockedSenderAddress,
  buildEmailQueryFilter,
};
