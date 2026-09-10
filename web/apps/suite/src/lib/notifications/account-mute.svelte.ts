/**
 * Per-separated-identity notification mute (issue #212, REQ-MAIL-SUB-06):
 * "each separated identity has its own unread badge and its own
 * notification channel, mutable independently." The unread badge is
 * already independent (lib/mail/sub-accounts.svelte.ts's own per-account
 * `unreadThreads`); this store is the independent mute switch for the
 * desktop-notification half of that channel.
 *
 * Persisted per-principal (not globally) via the same account-scoped
 * localStorage helpers `lib/mail/store.svelte.ts` uses for search
 * history, so muting a sub-account's notifications on one principal's
 * session never bleeds into another principal's session in the same
 * browser profile.
 *
 * A sub-account is unmuted by default (absent from the muted set) --
 * matches the primary account's own default of desktop notifications
 * being an opt-in the user enables once, not a per-source default-off.
 */

import { readAccountJson, writeAccountJson } from '../storage/account-scoped';
import { registerAccountResetCallback } from '../auth/auth.svelte';

const STORAGE_KEY = 'sub-account-notifications-muted';

class AccountNotificationMuteStore {
  muted = $state(new Set<string>());
  #hydrated = false;

  /**
   * Load the muted set from localStorage. Idempotent; call once after
   * auth is ready (accountKey() needs the session's username to scope
   * the storage key correctly) -- mirrors settings.hydrate()'s pattern.
   */
  hydrate(): void {
    if (this.#hydrated) return;
    this.#hydrated = true;
    const ids = readAccountJson<string[]>(STORAGE_KEY, []);
    this.muted = new Set(ids);
  }

  isMuted(accountId: string): boolean {
    return this.muted.has(accountId);
  }

  setMuted(accountId: string, muted: boolean): void {
    const next = new Set(this.muted);
    if (muted) next.add(accountId);
    else next.delete(accountId);
    this.muted = next;
    writeAccountJson(STORAGE_KEY, [...next]);
  }

  /** Test/logout hook: drop the hydrated flag so the next hydrate() re-reads. */
  reset(): void {
    this.muted = new Set();
    this.#hydrated = false;
  }
}

export const accountNotificationMute = new AccountNotificationMuteStore();
registerAccountResetCallback(() => accountNotificationMute.reset());
