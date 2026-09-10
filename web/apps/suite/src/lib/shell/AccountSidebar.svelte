<!--
  Scope-aware sidebar for a drilled-into sub-account (issue #212,
  REQ-MAIL-SUB-04). Rendered by App.svelte instead of the combined
  sidebar-inner block whenever the route is /account/<id>[/folder/<id>]:
  shows ONLY that account's own Mailbox tree, and every navigation link
  and the Compose button here target that account exclusively.

  "Back to All mail" returns to the combined /mail route (REQ-MAIL-SUB-02:
  "All mail" restores the combined view).
-->
<script lang="ts">
  import { subAccounts } from '../mail/sub-accounts.svelte';
  import { compose } from '../compose/compose.svelte';
  import { router } from '../router/router.svelte';
  import { t } from '../i18n/i18n.svelte';

  interface Props {
    accountId: string;
  }
  let { accountId }: Props = $props();

  let entry = $derived(subAccounts.find(accountId));

  $effect(() => {
    void subAccounts.load();
  });

  let inboxMailboxId = $derived(
    entry?.mailboxes.find((m) => m.role === 'inbox')?.id ?? null,
  );

  /** Top-level mailboxes, system-role ones first, matching the combined sidebar's feel. */
  let sortedMailboxes = $derived.by(() => {
    const list = entry?.mailboxes ?? [];
    const roleOrder = ['inbox', 'sent', 'drafts', 'trash', 'junk', 'archive'];
    return [...list].sort((a, b) => {
      const ra = a.role ? roleOrder.indexOf(a.role) : -1;
      const rb = b.role ? roleOrder.indexOf(b.role) : -1;
      if (ra !== -1 || rb !== -1) {
        if (ra === -1) return 1;
        if (rb === -1) return -1;
        return ra - rb;
      }
      return a.name.localeCompare(b.name);
    });
  });

  function isActive(mailboxId: string): boolean {
    if (router.parts[2] === 'folder') return router.parts[3] === mailboxId;
    return mailboxId === inboxMailboxId && router.parts.length <= 2;
  }

  function navigateToMailbox(mailboxId: string): void {
    if (mailboxId === inboxMailboxId) {
      router.navigate(`/account/${encodeURIComponent(accountId)}`);
    } else {
      router.navigate(
        `/account/${encodeURIComponent(accountId)}/folder/${encodeURIComponent(mailboxId)}`,
      );
    }
  }

  function composeFromHere(): void {
    compose.openWith({
      to: '',
      cc: '',
      bcc: '',
      subject: '',
      body: '',
      identity: entry?.identity ?? null,
    });
  }

  function backToAllMail(): void {
    router.navigate('/mail');
  }
</script>

<div class="sidebar-inner">
  <button type="button" class="back-link" onclick={backToAllMail} data-testid="account-sidebar-back">
    <span aria-hidden="true">&larr;</span> {t('shell.profile.allMail')}
  </button>

  <div class="account-heading" data-testid="account-sidebar-heading">
    {entry?.name ?? accountId}
  </div>

  <button type="button" class="compose" onclick={composeFromHere}>
    <span aria-hidden="true">&#x270E;</span> {t('sidebar.compose')}
  </button>

  <ul class="mailbox-list">
    {#each sortedMailboxes as m (m.id)}
      <li class:active={isActive(m.id)}>
        <button type="button" onclick={() => navigateToMailbox(m.id)}>
          {#if m.color}
            <span class="label-dot" style="background:{m.color};" aria-hidden="true"></span>
          {/if}
          <span>{m.name}</span>
          {#if m.unreadThreads > 0}
            <span class="count">{m.unreadThreads}</span>
          {/if}
        </button>
      </li>
    {:else}
      <li class="empty"><span>{t('sidebar.noCustom')}</span></li>
    {/each}
  </ul>
</div>

<style>
  .sidebar-inner {
    padding: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    height: 100%;
  }

  .back-link {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    color: var(--text-secondary);
    padding: var(--spacing-02) 0;
    font-size: var(--type-body-compact-01-size);
    align-self: flex-start;
  }
  .back-link:hover {
    color: var(--text-primary);
  }

  .account-heading {
    font-weight: 600;
    color: var(--text-primary);
    padding: var(--spacing-02) 0 var(--spacing-03);
    word-break: break-word;
  }

  .compose {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    margin-bottom: var(--spacing-04);
    min-height: var(--touch-min);
  }
  .compose:hover {
    filter: brightness(1.08);
  }

  .mailbox-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .mailbox-list li button {
    width: 100%;
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-03);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    min-height: var(--touch-min);
  }
  .mailbox-list li button:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .mailbox-list li.active button {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }
  .mailbox-list li.empty {
    padding: var(--spacing-02) var(--spacing-03);
    color: var(--text-helper);
    font-style: italic;
  }
  .mailbox-list .count {
    margin-left: auto;
    font-size: var(--type-helper-text-01-size);
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    padding: 0 6px;
    min-width: 18px;
    text-align: center;
  }
  .mailbox-list .label-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }
</style>
