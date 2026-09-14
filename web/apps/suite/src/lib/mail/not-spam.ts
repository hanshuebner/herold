/**
 * Pure helpers for the "Not spam" flow (issue #382, REQ-FLT-16 /
 * REQ-FILT-02a).
 *
 * NotSpamDialog.svelte offers, in one step alongside moving a Junk
 * message to Inbox, to create a never-spam ManagedRule scoped to the
 * sender's exact address or its domain. These helpers build that rule's
 * create payload without touching the store or the DOM, so the mapping
 * from a sender + scope choice to a ManagedRule is unit-testable on its
 * own.
 */

import type { ManagedRule, RuleAction, RuleCondition } from '../settings/managed-rules.svelte';

export type NeverSpamScope = 'address' | 'domain';

/**
 * The domain portion of an email address, lower-cased. Returns '' for an
 * address with no '@' or nothing after it -- callers treat that as "no
 * domain to scope a rule to".
 */
export function senderDomain(address: string): string {
  const at = address.lastIndexOf('@');
  if (at < 0 || at === address.length - 1) return '';
  return address.slice(at + 1).toLowerCase();
}

/**
 * Build the ManagedRule/set create payload for a never-spam allow rule
 * scoped to the sender address or its domain (REQ-FLT-01's from /
 * from-domain condition fields, REQ-FLT-16's never-spam action). Returns
 * null when the chosen scope has no usable value -- an empty address, or
 * a domain-less address for the 'domain' scope -- so the caller can skip
 * the ManagedRule/set call entirely rather than send an empty condition.
 */
export function buildNeverSpamRule(
  scope: NeverSpamScope,
  senderEmail: string,
  nextOrder: number,
  ruleName: string,
): Omit<ManagedRule, 'id'> | null {
  const trimmed = senderEmail.trim();
  const value = scope === 'address' ? trimmed : senderDomain(trimmed);
  if (!value) return null;

  const condition: RuleCondition = {
    field: scope === 'address' ? 'from' : 'from-domain',
    op: 'equals',
    value,
  };
  const action: RuleAction = { kind: 'never-spam' };

  return {
    name: ruleName,
    enabled: true,
    order: nextOrder,
    conditions: [condition],
    actions: [action],
  };
}

/**
 * What to do about a never-spam rule for a given scope + sender, having
 * checked the caller's existing rules for one that already matches the
 * same single condition (issue #382 retry: neither the client nor
 * ManagedRule/set rejected a second identical rule for the same
 * domain/address, so two "Not spam -> This domain" clicks on messages
 * from the same domain used to create two rules).
 *
 * - 'create': no matching rule exists; payload is the ManagedRule/set
 *   create body (same shape buildNeverSpamRule returns).
 * - 'reuse': a rule with the same condition already carries the
 *   never-spam action; nothing to do server-side.
 * - 'add-action': a rule with the same condition exists but lacks the
 *   never-spam action (e.g. it only applies a label); nextActions is
 *   rule.actions with never-spam appended, for a ManagedRule/set update.
 */
export type NeverSpamPlan =
  | { mode: 'create'; payload: Omit<ManagedRule, 'id'> }
  | { mode: 'reuse'; rule: ManagedRule }
  | { mode: 'add-action'; rule: ManagedRule; nextActions: RuleAction[] };

/**
 * Decide what buildNeverSpamRule's caller should do given the rules the
 * principal already has. Returns null under the same conditions
 * buildNeverSpamRule returns null (no usable condition value).
 */
export function planNeverSpamRule(
  rules: ManagedRule[],
  scope: NeverSpamScope,
  senderEmail: string,
  nextOrder: number,
  ruleName: string,
): NeverSpamPlan | null {
  const payload = buildNeverSpamRule(scope, senderEmail, nextOrder, ruleName);
  if (!payload) return null;
  const condition = payload.conditions[0]!;

  const existing = rules.find((r) => {
    if (r.conditions.length !== 1) return false;
    const c = r.conditions[0]!;
    return (
      c.field === condition.field &&
      c.op === condition.op &&
      c.value.toLowerCase() === condition.value.toLowerCase()
    );
  });
  if (!existing) return { mode: 'create', payload };
  if (existing.actions.some((a) => a.kind === 'never-spam')) {
    return { mode: 'reuse', rule: existing };
  }
  return {
    mode: 'add-action',
    rule: existing,
    nextActions: [...existing.actions, { kind: 'never-spam' }],
  };
}
