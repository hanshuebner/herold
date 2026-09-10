<script lang="ts">
  /**
   * Accounts settings section (issue #212, REQ-MAIL-SUB-01).
   *
   * Lists every identity that has been separated into its own sub-
   * account, distinct from the From-address Identity list in the
   * Account section above: state (migrating / separated), a progress
   * readout while migrating, last-sync (from the underlying IMAP-import
   * account, when one exists), and per-account pause / remove actions.
   * Separating a new identity happens from that identity's edit page
   * (IdentityEditPage.svelte's "Separate account" section); this section
   * only manages identities that are already separated.
   *
   * Polls while any entry is migrating (REQ-MAIL-SUB-07: "reflects the
   * migration as in-progress until the server reports it complete") --
   * the server does not push a dedicated Identity state-change event
   * this store subscribes to, so a short poll is the simplest correct
   * way to converge the UI without inventing a bespoke push channel for
   * a lifecycle that runs to completion in seconds to low minutes.
   */
  import { subAccounts, type SubAccountEntry } from '../../lib/mail/sub-accounts.svelte';
  import { mail } from '../../lib/mail/store.svelte';
  import { auth } from '../../lib/auth/auth.svelte';
  import { imapImportStore, type IMAPImportHandle } from '../../lib/jmap/imap-import-store.svelte';
  import { separationState, separationProgress, separationOf } from '../../lib/identities/identity-separation';
  import { accountNotificationMute } from '../../lib/notifications/account-mute.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t, localeTag } from '../../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';

  // Notifications toggle persistence needs the hydrated mute set; this is
  // a cheap idempotent call (App.svelte's ready effect also calls it, so
  // this is only load-bearing for a user who opens Settings before that
  // effect has had a chance to run).
  accountNotificationMute.hydrate();

  $effect(() => {
    void subAccounts.load();
  });

  // Poll while any entry is still migrating so the state / progress
  // readout advances without the user needing to leave and re-enter the
  // section.
  $effect(() => {
    const anyMigrating = subAccounts.list.some(
      (e) => e.identity && separationState(e.identity) === 'migrating',
    );
    if (!anyMigrating) return;
    const timer = setInterval(() => void subAccounts.refresh(), 2000);
    return () => clearInterval(timer);
  });

  function importHandleFor(entry: SubAccountEntry): IMAPImportHandle | null {
    if (!entry.identity) return null;
    return imapImportStore.forIdentity(entry.identity.id, entry.accountId);
  }

  // Pre-load each entry's IMAP-import status so last-sync / pause render
  // without a per-row lazy load.
  $effect(() => {
    for (const entry of subAccounts.list) {
      void importHandleFor(entry)?.load();
    }
  });

  function formatLastSync(iso: string | null | undefined): string {
    if (!iso) return t('settings.accounts.lastSyncNever');
    const d = new Date(iso);
    return d.toLocaleString(localeTag(), { dateStyle: 'medium', timeStyle: 'short' });
  }

  let pausingAccountId = $state<string | null>(null);

  async function togglePause(entry: SubAccountEntry): Promise<void> {
    const handle = importHandleFor(entry);
    const account = handle?.account;
    if (!handle || !account) return;
    pausingAccountId = entry.accountId;
    try {
      const nextState = account.state === 'disabled' ? 'enabled' : 'disabled';
      await handle.update(account.id, { state: nextState });
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : t('settings.accounts.actionFailed'),
        kind: 'error',
        timeoutMs: 5000,
      });
    } finally {
      pausingAccountId = null;
    }
  }

  function toggleNotifications(accountId: string, currentlyMuted: boolean): void {
    accountNotificationMute.setMuted(accountId, !currentlyMuted);
  }

  let removingAccountId = $state<string | null>(null);

  async function removeAccount(entry: SubAccountEntry): Promise<void> {
    if (!entry.identity) return;
    const displayName = entry.identity.name || entry.identity.email;

    const ok = await confirm.ask({
      title: t('settings.accounts.removeTitle', { name: displayName }),
      message: t('settings.accounts.removeMessage'),
      confirmLabel: t('settings.accounts.removeConfirmKeep'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (!ok) return;

    const wantPurge = await confirm.ask({
      title: t('settings.accounts.purgeTitle'),
      message: t('settings.accounts.purgeMessage'),
      confirmLabel: t('settings.accounts.purgeConfirm'),
      cancelLabel: t('settings.accounts.purgeCancel'),
      kind: 'danger',
    });

    removingAccountId = entry.accountId;
    try {
      await subAccounts.removeSeparation(entry.accountId, entry.identity.id, !wantPurge);
      toast.show({
        message: t('settings.accounts.removed', { name: displayName }),
        timeoutMs: 4000,
      });
      // A keepMail:true reversal moves the Identity back to the caller's
      // own account; refresh so it reappears in the Account section's
      // From-address list without waiting for the next full reload.
      void mail.loadIdentities();
      // The SPA's in-memory session descriptor still lists the
      // now-removed sub-account (nothing else re-fetches /.well-known/jmap
      // on this timeline); refresh it, then re-derive the sub-accounts
      // list against the corrected accounts map so the removed row does
      // not linger with a stale accountId (see the matching fix in
      // IdentityEditPage.svelte's separateThisIdentity).
      await auth.refreshSession();
      await subAccounts.refresh();
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : t('settings.accounts.actionFailed'),
        kind: 'error',
        timeoutMs: 5000,
      });
    } finally {
      removingAccountId = null;
    }
  }
</script>

<p class="hint">{t('settings.accounts.hint')}</p>

{#if subAccounts.list.length === 0}
  <p class="muted" data-testid="accounts-empty">{t('settings.accounts.empty')}</p>
{:else}
  <ul class="accounts-list">
    {#each subAccounts.list as entry (entry.accountId)}
      {@const state = entry.identity ? separationState(entry.identity) : 'separated'}
      {@const migrating = state === 'migrating'}
      {@const handle = importHandleFor(entry)}
      {@const account = handle?.account ?? null}
      <li class="account-row" data-testid="account-row-{entry.accountId}">
        <div class="account-main">
          <span class="account-name">{entry.name}</span>
          <span class="state-chip" class:migrating>
            {migrating ? t('settings.accounts.stateMigrating') : t('settings.accounts.stateSeparated')}
          </span>
        </div>
        {#if migrating && entry.identity}
          <p class="progress">
            {t('settings.accounts.progress', {
              moved: String(separationProgress(entry.identity)),
              total: String(separationOf(entry.identity).messagesTotal),
            })}
          </p>
        {/if}
        <div class="account-meta">
          <span class="label">{t('settings.accounts.lastSync')}</span>
          <span class="value">{formatLastSync(account?.lastSuccessAt)}</span>
        </div>
        {#if !migrating}
          {@const muted = accountNotificationMute.isMuted(entry.accountId)}
          <div class="account-notifications">
            <span class="label">{t('settings.accounts.notifications')}</span>
            <label class="switch" aria-label={t('settings.accounts.notificationsHint')}>
              <input
                type="checkbox"
                checked={!muted}
                onchange={() => toggleNotifications(entry.accountId, muted)}
                data-testid="account-notifications-{entry.accountId}"
              />
              <span class="track" aria-hidden="true"></span>
            </label>
          </div>
        {/if}
        <div class="account-actions">
          {#if account}
            <Button
              variant="secondary"
              compact
              onclick={() => void togglePause(entry)}
              disabled={pausingAccountId === entry.accountId}
              testid="account-pause-{entry.accountId}"
            >
              {account.state === 'disabled'
                ? t('settings.accounts.resume')
                : t('settings.accounts.pause')}
            </Button>
          {/if}
          <Button
            variant="danger"
            compact
            onclick={() => void removeAccount(entry)}
            disabled={removingAccountId === entry.accountId}
            testid="account-remove-{entry.accountId}"
          >
            {t('settings.accounts.remove')}
          </Button>
        </div>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0 0 var(--spacing-04);
  }
  .muted {
    color: var(--text-helper);
    font-style: italic;
  }

  .accounts-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .account-row {
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .account-main {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
  }

  .account-name {
    font-weight: 600;
    color: var(--text-primary);
  }

  .state-chip {
    padding: 2px 8px;
    border-radius: var(--radius-pill);
    font-size: var(--type-helper-text-01-size);
    background: var(--layer-02);
    color: var(--text-secondary);
  }
  .state-chip.migrating {
    background: color-mix(in srgb, var(--support-warning, #f1c21b) 25%, transparent);
    color: var(--text-primary);
  }

  .progress {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .account-meta {
    display: flex;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
  }
  .account-meta .label {
    color: var(--text-helper);
  }
  .account-meta .value {
    color: var(--text-primary);
  }

  .account-actions {
    display: flex;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  .account-notifications {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
  }
  .account-notifications .label {
    color: var(--text-helper);
  }

  .switch {
    position: relative;
    display: inline-flex;
    width: 40px;
    height: 22px;
    cursor: pointer;
  }
  .switch input {
    position: absolute;
    inset: 0;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  .switch .track {
    width: 100%;
    height: 100%;
    background: var(--border-strong-01);
    border-radius: var(--radius-pill);
    position: relative;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .switch .track::before {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 18px;
    height: 18px;
    background: var(--text-on-color);
    border-radius: var(--radius-pill);
    transition: transform var(--duration-fast-02) var(--easing-productive-enter);
  }
  .switch input:checked + .track {
    background: var(--interactive);
  }
  .switch input:checked + .track::before {
    transform: translateX(18px);
  }
</style>
