<script lang="ts">
  /**
   * Sticky thread-action toolbar (REQ-UI-19a..d, REQ-UI-20). Renders
   * over the scrollable thread reader so the actions stay visible as
   * the user scrolls through long conversations. Every action operates
   * on the *whole thread* via the bulk-op helpers (REQ-MAIL-51..54
   * thread expansion); the seed id is the thread's most-recent email.
   *
   * Per re #98 actions live exclusively at thread scope. The per-message
   * action row was removed; reactions moved into the message header;
   * reply / forward live in the fixed reply bar at the bottom. The toolbar
   * uses the THREAD_ACTIONS registry directly with a fixed visible count.
   */
  import { mail } from './store.svelte';
  import { router } from '../router/router.svelte';
  import { movePicker } from './move-picker.svelte';
  import { labelPicker } from './label-picker.svelte';
  import { snoozePicker } from './snooze-picker.svelte';
  import { managedRules } from '../settings/managed-rules.svelte';
  import { THREAD_ACTIONS, DEFAULT_THREAD_VISIBLE } from './actions';
  import ActionOverflowMenu from './ActionOverflowMenu.svelte';
  import { t } from '../i18n/i18n.svelte';
  import ArchiveIcon from '../icons/ArchiveIcon.svelte';
  import TrashIcon from '../icons/TrashIcon.svelte';
  import RestoreIcon from '../icons/RestoreIcon.svelte';
  import MarkUnreadIcon from '../icons/MarkUnreadIcon.svelte';
  import SnoozeIcon from '../icons/SnoozeIcon.svelte';
  import MoveIcon from '../icons/MoveIcon.svelte';
  import LabelIcon from '../icons/LabelIcon.svelte';
  import PrintIcon from '../icons/PrintIcon.svelte';
  import MuteIcon from '../icons/MuteIcon.svelte';
  import UnmuteIcon from '../icons/UnmuteIcon.svelte';
  import SpamIcon from '../icons/SpamIcon.svelte';
  import PhishingIcon from '../icons/PhishingIcon.svelte';
  import BlockIcon from '../icons/BlockIcon.svelte';
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

  // Mute state for the thread — used by the mute/unmute action.
  let isMuted = $derived(managedRules.isThreadMuted(threadId));

  // Sender email from the most-recent message in the thread (for block sender).
  let senderEmail = $derived(latest.from?.[0]?.email ?? '');

  // Block sender confirmation state.
  let blockConfirmOpen = $state(false);
  let blockError = $state<string | null>(null);
  let blockInProgress = $state(false);

  function back(): void {
    if (window.history.length > 1) {
      window.history.back();
      return;
    }
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

  // Restore the thread out of Trash. Mirrors the previous per-message
  // restore (re #29) at thread scope: restoring the latest email rehomes
  // the conversation; the user is sent back to the listing afterwards.
  function restoreThread(): void {
    void mail.restoreFromTrash(latest.id);
    back();
  }

  function markUnread(): void {
    void mail.markThreadSeen(threadId, false);
    back();
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

  async function handleMuteToggle(): Promise<void> {
    if (isMuted) {
      await managedRules.unmuteThread(threadId);
    } else {
      await managedRules.muteThread(threadId);
    }
  }

  async function handleReportSpam(): Promise<void> {
    await mail.reportSpam(latest.id, 'spam');
  }

  async function handleReportPhishing(): Promise<void> {
    await mail.reportSpam(latest.id, 'phishing');
  }

  function openBlockConfirm(): void {
    blockError = null;
    blockConfirmOpen = true;
  }

  function closeBlockConfirm(): void {
    blockConfirmOpen = false;
    blockError = null;
  }

  async function confirmBlock(): Promise<void> {
    if (!senderEmail) return;
    blockInProgress = true;
    blockError = null;
    try {
      await managedRules.blockSender(senderEmail);
      blockConfirmOpen = false;
    } catch (err) {
      blockError = err instanceof Error ? err.message : 'Block failed';
    } finally {
      blockInProgress = false;
    }
  }

  // ── Thread action descriptors ─────────────────────────────────────────

  type ThreadActionKey =
    | 'archive'
    | 'deleteThread'
    | 'restoreThread'
    | 'markUnread'
    | 'snoozeThread'
    | 'moveThread'
    | 'labelThread'
    | 'muteThread'
    | 'reportSpam'
    | 'reportPhishing'
    | 'blockSender'
    | 'print';

  interface ThreadActionDesc {
    visible: boolean;
    label: string;
    shortcut?: string;
    onclick: () => void;
    ariaPressed?: boolean;
  }

  let allThreadActions = $derived.by((): Record<ThreadActionKey, ThreadActionDesc> => ({
    archive: {
      visible: isInInbox,
      label: t('thread.archive'),
      shortcut: 'e',
      onclick: archive,
    },
    deleteThread: {
      visible: !isInTrash,
      label: t('thread.delete'),
      shortcut: '#',
      onclick: deleteThread,
    },
    restoreThread: {
      visible: isInTrash,
      label: t('thread.restore'),
      onclick: restoreThread,
    },
    markUnread: {
      visible: true,
      label: t('thread.markUnread'),
      shortcut: 'u',
      onclick: markUnread,
    },
    snoozeThread: {
      visible: true,
      label: t('thread.snooze'),
      onclick: snooze,
    },
    moveThread: {
      visible: true,
      label: t('thread.move'),
      onclick: move,
    },
    labelThread: {
      visible: true,
      label: t('thread.label'),
      onclick: applyLabels,
    },
    muteThread: {
      visible: true,
      label: isMuted ? t('msg.unmuteThread') : t('msg.muteThread'),
      onclick: () => void handleMuteToggle(),
      ariaPressed: isMuted,
    },
    reportSpam: {
      visible: true,
      label: t('msg.reportSpam'),
      onclick: () => void handleReportSpam(),
    },
    reportPhishing: {
      visible: true,
      label: t('msg.reportPhishing'),
      onclick: () => void handleReportPhishing(),
    },
    blockSender: {
      visible: Boolean(senderEmail),
      label: t('msg.blockSender'),
      onclick: openBlockConfirm,
    },
    print: {
      visible: true,
      label: t('thread.print'),
      onclick: onPrint,
    },
  }));

  let orderedThreadActions = $derived.by(() => {
    const result: Array<{
      id: ThreadActionKey;
      desc: ThreadActionDesc;
      isPrimary: boolean;
    }> = [];

    let primaryCount = 0;
    for (const def of THREAD_ACTIONS) {
      const id = def.id as ThreadActionKey;
      const desc = allThreadActions[id];
      if (!desc || !desc.visible) continue;
      const isPrimary = primaryCount < DEFAULT_THREAD_VISIBLE;
      if (isPrimary) primaryCount++;
      result.push({ id, desc, isPrimary });
    }
    return result;
  });

  let primaryThreadActions = $derived(orderedThreadActions.filter((a) => a.isPrimary));
  let overflowThreadActions = $derived(orderedThreadActions.filter((a) => !a.isPrimary));
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

  {#each primaryThreadActions as { id, desc } (id)}
    <button
      type="button"
      class="action-btn"
      class:danger={id === 'deleteThread'}
      class:muted={id === 'muteThread' && isMuted}
      aria-label={desc.label}
      title={desc.label}
      aria-pressed={desc.ariaPressed}
      onclick={desc.onclick}
    >
      {#if id === 'archive'}
        <ArchiveIcon size={16} />
      {:else if id === 'deleteThread'}
        <TrashIcon size={16} />
      {:else if id === 'restoreThread'}
        <RestoreIcon size={16} />
      {:else if id === 'markUnread'}
        <MarkUnreadIcon size={16} />
      {:else if id === 'snoozeThread'}
        <SnoozeIcon size={16} />
      {:else if id === 'moveThread'}
        <MoveIcon size={16} />
      {:else if id === 'labelThread'}
        <LabelIcon size={16} />
      {:else if id === 'muteThread'}
        {#if isMuted}<UnmuteIcon size={16} />{:else}<MuteIcon size={16} />{/if}
      {:else if id === 'reportSpam'}
        <SpamIcon size={16} />
      {:else if id === 'reportPhishing'}
        <PhishingIcon size={16} />
      {:else if id === 'blockSender'}
        <BlockIcon size={16} />
      {:else if id === 'print'}
        <PrintIcon size={16} />
      {/if}
      <span class="btn-label">{desc.label}</span>
    </button>
  {/each}

  {#if overflowThreadActions.length > 0}
    <ActionOverflowMenu
      items={overflowThreadActions.map(({ id, desc }) => ({
        id,
        label: desc.label,
        shortcut: desc.shortcut,
        onclick: desc.onclick,
      }))}
    />
  {/if}

  <span class="spacer" aria-hidden="true"></span>
</div>

<!-- Block sender confirmation modal for thread toolbar. -->
{#if blockConfirmOpen}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="block-modal"
    role="dialog"
    aria-modal="true"
    aria-label="Block sender"
    tabindex="-1"
    onkeydown={(e) => { if (e.key === 'Escape') closeBlockConfirm(); }}
  >
    <p class="block-modal-body">
      Block all messages from <strong>{senderEmail}</strong>?
      Existing messages stay; future messages go to Trash.
      You can unblock them later in Settings &rarr; Filters.
    </p>
    {#if blockError}
      <p class="block-modal-error" role="alert">{blockError}</p>
    {/if}
    <div class="block-modal-actions">
      <button
        type="button"
        onclick={() => void confirmBlock()}
        disabled={blockInProgress}
      >
        {blockInProgress ? 'Blocking...' : 'Block sender'}
      </button>
      <button type="button" onclick={closeBlockConfirm}>
        Cancel
      </button>
    </div>
  </div>
{/if}

<style>
  .thread-toolbar {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--background);
    border-bottom: 1px solid var(--border-subtle-01);
    flex-shrink: 0;
    flex-wrap: wrap;
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

  /* Back button: icon-only, always visible. */
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
  .icon-btn.back {
    color: var(--text-primary);
  }

  /* Primary thread action buttons: compact labeled pills.
     The fixed reply bar at the bottom of the reader uses the same
     visual language so the two action surfaces feel like one set. */
  .action-btn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    background: transparent;
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: 32px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .action-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .action-btn.danger:hover {
    color: var(--support-error);
  }
  .action-btn.muted {
    color: var(--text-helper);
  }

  .btn-label {
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }

  /* Block-sender modal (shown below the toolbar). */
  .block-modal {
    margin: 0 var(--spacing-05) var(--spacing-04);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }
  .block-modal-body {
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .block-modal-error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .block-modal-actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }
  .block-modal-actions button {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
  }
  .block-modal-actions button:hover {
    background: var(--layer-03);
  }
  .block-modal-actions button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  @media print {
    .thread-toolbar,
    .block-modal {
      display: none;
    }
  }
</style>
