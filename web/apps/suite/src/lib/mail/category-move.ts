/**
 * Move messages to a category and offer the per-sender rule (re #555).
 *
 * Shared by every category-move entry point: the picker (`m`, toolbar,
 * message menu) and a drop on a category tab. After a successful move to
 * a named category, a non-blocking toast offers "Always categorize mail
 * from <sender> as <category>" when the moved messages share one sender.
 */

import { mail } from './store.svelte';
import {
  categorySettings,
  categoryKeyword,
  categoryLabel,
} from '../settings/category-settings.svelte';
import { toast } from '../toast/toast.svelte';
import { t } from '../i18n/i18n.svelte';

/** Time the rule offer stays up before it dismisses itself. */
const OFFER_TIMEOUT_MS = 6000;

/**
 * The single lower-cased From address shared by `emailIds`, or null when
 * the messages have no From address or more than one distinct sender.
 */
export function singleSender(emailIds: string[]): string | null {
  let sender: string | null = null;
  for (const id of emailIds) {
    const addr = mail.emails.get(id)?.from?.[0]?.email?.trim().toLowerCase();
    if (!addr) return null;
    if (sender === null) sender = addr;
    else if (sender !== addr) return null;
  }
  return sender;
}

/**
 * Apply `categoryName` (null clears the category) to each email id, then
 * show the rule offer. Resolves true when every move succeeded.
 */
export async function moveEmailsToCategory(
  emailIds: string[],
  categoryName: string | null,
  threadGranular = true,
): Promise<boolean> {
  if (emailIds.length === 0) return false;
  const sender = singleSender(emailIds);
  const kw = categoryName ? categoryKeyword(categoryName) : null;
  let ok = true;
  for (const id of emailIds) {
    if (!(await mail.setCategoryKeyword(id, kw, threadGranular))) ok = false;
  }
  if (ok && categoryName && sender) offerSenderRule(sender, categoryName);
  return ok;
}

/** Show the "Always categorize" toast unless the same rule already exists. */
export function offerSenderRule(sender: string, categoryName: string): void {
  if (!categorySettings.available) return;
  const existing = categorySettings.senderRules.find((r) => r.sender === sender);
  if (existing && existing.category.toLowerCase() === categoryName.toLowerCase()) return;
  const label = categoryLabel(categoryName);
  toast.show({
    message: t('cat.move.moved', { category: label }),
    actionLabel: t('cat.rule.offer', { sender, category: label }),
    undo: async () => {
      await categorySettings.addSenderRule(sender, categoryName);
    },
    timeoutMs: OFFER_TIMEOUT_MS,
  });
}
