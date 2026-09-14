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
