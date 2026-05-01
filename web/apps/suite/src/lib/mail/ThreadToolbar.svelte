<script lang="ts">
  /**
   * Sticky thread-action toolbar (REQ-UI-19a..d, REQ-UI-20). Renders
   * over the scrollable thread reader so the actions stay visible as
   * the user scrolls through long conversations. Every action operates
   * on the *whole thread* via the bulk-op helpers (REQ-MAIL-51..54
   * thread expansion); the seed id is the thread's most-recent email.
   *
   * Only actions that are actually wired to a working handler are
   * shown — Gmail-style "report spam" and "add to tasks" are deferred
   * until the underlying support lands.
   */
  import { mail } from './store.svelte';
  import { router } from '../router/router.svelte';
  import { movePicker } from './move-picker.svelte';
  import { labelPicker } from './label-picker.svelte';
  import { snoozePicker } from './snooze-picker.svelte';
  import { t } from '../i18n/i18n.svelte';
  import ArchiveIcon from '../icons/ArchiveIcon.svelte';
  import TrashIcon from '../icons/TrashIcon.svelte';
  import MarkUnreadIcon from '../icons/MarkUnreadIcon.svelte';
  import SnoozeIcon from '../icons/SnoozeIcon.svelte';
  import MoveIcon from '../icons/MoveIcon.svelte';
  import LabelIcon from '../icons/LabelIcon.svelte';
  import PrintIcon from '../icons/PrintIcon.svelte';
  import type { Email } from './types';

  interface Props {
    threadId: string;
    /** Most recent email in the thread; used as the bulk-op seed. */
    latest: Email;
    /** Handler that opens the print dialog. Lives in ThreadReader so
     *  the message-expansion side effect can be coordinated. */
    onPrint: () => void;
  }
  let { threadId, latest, onPrint }: Props = $props();

  let inboxId = $derived(mail.inbox?.id);
  let trashId = $derived(mail.trash?.id);
  let isInInbox = $derived(Boolean(inboxId && latest.mailboxIds[inboxId]));
  let isInTrash = $derived(Boolean(trashId && latest.mailboxIds[trashId]));

  function back(): void {
    // Hash navigation pushes a history entry per route change, so
    // history.back returns to the prior list view in the common case.
    if (window.history.length > 1) {
      window.history.back();
      return;
    }
    // Fallback: navigate to the active list folder. listFolder is a
    // role name for system folders or a custom mailbox id.
    const folder = mail.listFolder;
    if (folder === 'inbox') router.navigate('/mail');
    else router.navigate(`/mail/folder/${encodeURIComponent(folder)}`);
  }

  function archive(): void {
    void mail.bulkArchive([latest.id]);
    back();
  }

  function deleteThread(): void {
    void mail.bulkDelete([latest.id]);
    back();
  }

  function markUnread(): void {
    void mail.markThreadSeen(threadId, false);
  }

  function snooze(): void {
    snoozePicker.open(latest.id);
  }

  function move(): void {
    movePicker.openBulk([latest.id]);
  }

  function applyLabels(): void {
    labelPicker.openBulk([latest.id]);
  }
</script>

<div class="thread-toolbar" role="toolbar" aria-label={t('thread.back')}>
  <button
    type="button"
    class="icon-btn back"
    aria-label={t('thread.back')}
    title={t('thread.back')}
    onclick={back}
  >
    <svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true">
      <path d="M15.5 4 8 12l7.5 8 1.5-1.5L11 12l6-6.5z" />
    </svg>
  </button>

  <span class="divider" aria-hidden="true"></span>

  {#if isInInbox}
    <button
      type="button"
      class="icon-btn"
      aria-label={t('bulk.archive')}
      title={t('bulk.archive')}
      onclick={archive}
    ><ArchiveIcon size={18} /></button>
  {/if}
  {#if !isInTrash}
    <button
      type="button"
      class="icon-btn danger"
      aria-label={t('bulk.delete')}
      title={t('bulk.delete')}
      onclick={deleteThread}
    ><TrashIcon size={18} /></button>
  {/if}
  <button
    type="button"
    class="icon-btn"
    aria-label={t('bulk.markUnread')}
    title={t('bulk.markUnread')}
    onclick={markUnread}
  ><MarkUnreadIcon size={18} /></button>
  <button
    type="button"
    class="icon-btn"
    aria-label={t('msg.snooze')}
    title={t('msg.snooze')}
    onclick={snooze}
  ><SnoozeIcon size={18} /></button>
  <button
    type="button"
    class="icon-btn"
    aria-label={t('bulk.move')}
    title={t('bulk.move')}
    onclick={move}
  ><MoveIcon size={18} /></button>
  <button
    type="button"
    class="icon-btn"
    aria-label={t('bulk.label')}
    title={t('bulk.label')}
    onclick={applyLabels}
  ><LabelIcon size={18} /></button>

  <span class="spacer" aria-hidden="true"></span>

  <button
    type="button"
    class="icon-btn"
    aria-label={t('thread.print')}
    title={t('thread.print')}
    onclick={onPrint}
  ><PrintIcon size={18} /></button>
</div>

<style>
  .thread-toolbar {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--background);
    border-bottom: 1px solid var(--border-subtle-01);
    flex-shrink: 0;
  }
  .divider {
    width: 1px;
    height: 24px;
    background: var(--border-subtle-02);
    margin: 0 var(--spacing-02);
  }
  .spacer {
    flex: 1;
  }
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    background: transparent;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .icon-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .icon-btn.danger:hover {
    color: var(--support-error);
  }
  .icon-btn.back {
    color: var(--text-primary);
  }

  @media print {
    .thread-toolbar {
      display: none;
    }
  }
</style>
