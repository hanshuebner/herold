<script lang="ts">
  /**
   * Inline reply / forward composer mounted at the bottom of the thread
   * scroll region when compose.inlineMode is true (re #38).
   *
   * Provides the same capabilities as the floating ComposeWindow — rich
   * text, recipient editing, attachments, draft autosave — but renders
   * inline so the user can see the prior conversation while composing.
   *
   * Send actions (re #34):
   *   - "Send + Archive" (primary, Mod+Enter): send then archive all
   *     inbox-member emails in the thread and navigate to the list.
   *   - "Send" (secondary): send and leave the thread open; the just-
   *     sent reply is auto-expanded by ThreadReader's new-message effect.
   *
   * Pop-out: sets compose.inlineMode = false, which causes ComposeWindow
   * to appear with the same compose state and this panel to unmount.
   *
   * Archive logic mirrors ThreadToolbar.archive: archives every email in
   * the thread that is currently in the Inbox mailbox (re #35), not
   * only the latest message.
   */
  import { untrack } from 'svelte';
  import { compose } from '../compose/compose.svelte';
  import { mail } from './store.svelte';
  import { navigateBackFromThread } from './navigate-back';
  import { keyboard } from '../keyboard/engine.svelte';
  import { confirm } from '../dialog/confirm.svelte';
  import { t } from '../i18n/i18n.svelte';
  import RichEditor from '../compose/RichEditor.svelte';
  import ComposeToolbar from '../compose/ComposeToolbar.svelte';
  import RecipientField from '../compose/RecipientField.svelte';
  import {
    EMPTY_ACTIVE,
    type ActiveState,
    insertHtmlBlockAtEnd,
    removeHtmlBlockByShareUrl,
  } from '../compose/editor';
  import type { EditorView } from 'prosemirror-view';
  import { recipientToString } from '../compose/recipient-parse';
  import { attachmentBadge } from './attachment-icon';

  interface Props {
    /** Thread ID: used to load emails and compute inbox membership. */
    threadId: string;
  }
  let { threadId }: Props = $props();

  // Thread emails and inbox membership for Send + Archive.
  let threadEmails = $derived(mail.threadEmails(threadId));
  let inboxId = $derived(mail.inbox?.id);
  let inboxEmailIds = $derived(
    inboxId
      ? threadEmails.filter((e) => Boolean(e.mailboxIds[inboxId])).map((e) => e.id)
      : [],
  );

  // Scroll the composer into view when it mounts.
  let containerEl = $state<HTMLElement | null>(null);
  $effect(() => {
    const el = containerEl;
    if (!el) return;
    requestAnimationFrame(() => {
      el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    });
  });

  // Per-compose keyboard layer while inline: Mod+Enter = Send + Archive,
  // Escape = discard. Registered only when this panel is mounted (i.e.
  // compose.isOpen && compose.inlineMode), so it never conflicts with
  // ComposeWindow's layer which guards on !compose.inlineMode.
  $effect(() => {
    if (!compose.isOpen) return;
    const pop = keyboard.pushLayer([
      {
        key: 'Mod+Enter',
        description: t('compose.sendAndArchive'),
        action: () => void sendAndArchive(),
      },
      {
        key: 'Escape',
        description: t('compose.discard'),
        action: () => void discardWithConfirm(),
      },
    ]);
    return pop;
  });

  async function discardWithConfirm(): Promise<void> {
    const dirty = compose.hasContent || compose.editingDraftId !== null;
    if (dirty && compose.status !== 'sending') {
      const ok = await confirm.ask({
        title: t('compose.discardConfirm.title'),
        message: t('compose.discardConfirm.message'),
        confirmLabel: t('compose.discardConfirm.confirm'),
        cancelLabel: t('compose.discardConfirm.cancel'),
        kind: 'danger',
      });
      if (!ok) return;
    }
    void compose.discard();
  }

  /** Switch to the floating ComposeWindow with the current compose state intact. */
  function popOut(): void {
    compose.inlineMode = false;
  }

  /**
   * Send + Archive: send the reply, then archive every inbox-member
   * email in the thread and navigate to the list. Captures inboxEmailIds
   * before send() calls close() and clears compose state (re #34, #35).
   */
  async function sendAndArchive(): Promise<void> {
    if (compose.status === 'sending') return;
    if (!canSend) return;
    const ids = untrack(() => inboxEmailIds);
    await compose.send();
    if (!compose.isOpen) {
      if (ids.length > 0) void mail.bulkArchive(ids);
      navigateBackFromThread();
    }
  }

  /**
   * Send only: send the reply and leave the thread open. The just-sent
   * message appears expanded via ThreadReader's new-message auto-expand
   * effect (re #34).
   */
  async function sendOnly(): Promise<void> {
    if (compose.status === 'sending') return;
    if (!canSend) return;
    await compose.send();
  }

  // Recipient warning state — gates the send buttons.
  let toWarning = $state<string | null>(null);
  let ccWarning = $state<string | null>(null);
  let bccWarning = $state<string | null>(null);
  let hasRecipientWarnings = $derived(
    toWarning !== null || ccWarning !== null || bccWarning !== null,
  );

  let canSend = $derived(
    compose.status !== 'sending' && !compose.attachmentsBusy && !hasRecipientWarnings,
  );

  // Editor view bridge for the formatting toolbar.
  let editorView = $state<EditorView | null>(null);
  let active = $state<ActiveState>(EMPTY_ACTIVE);

  // Non-inline attachments shown in the chip strip (inline images live in
  // the editor body and do not appear here, same convention as ComposeWindow).
  let nonInlineAttachments = $derived(
    compose.attachments.filter((a) => !a.inline),
  );

  // Uploading inline image blob: URLs — passed to RichEditor so it can
  // render an uploading overlay while the JMAP blob upload is in flight.
  let uploadingSrcs = $derived(
    new Set(
      compose.attachments
        .filter((a) => a.inline && a.status === 'uploading' && a.objectURL)
        .map((a) => a.objectURL as string),
    ),
  );

  function onEditorUpdate(html: string, _text: string): void {
    compose.body = html;
  }

  function onImageRemoved(src: string): void {
    const att = compose.attachments.find(
      (a) => a.inline && (a.objectURL === src || (a.cid && `cid:${a.cid}` === src)),
    );
    if (att) compose.removeAttachment(att.key);
  }

  // Snapshot compose.body once per open so RichEditor doesn't remount
  // on every keystroke. Same pattern and rationale as ComposeWindow.
  // The {#key} on RichEditor below ensures it remounts only when the
  // reply target changes.
  let initialHtml = $state('');
  $effect(() => {
    if (compose.status === 'editing') {
      initialHtml = untrack(() => compose.body);
    } else {
      initialHtml = '';
    }
  });

  // Register share-link editor fns when the editor view is available, so
  // offloaded file-share links can be inserted and removed live (REQ-ATT-63/64).
  $effect(() => {
    const view = editorView;
    if (!view) {
      compose.setShareEditorFns(null, null);
      return;
    }
    compose.setShareEditorFns(
      (html) => insertHtmlBlockAtEnd(view, html),
      (url) => removeHtmlBlockByShareUrl(view, url),
    );
    return () => compose.setShareEditorFns(null, null);
  });

  // File attachment picker.
  let fileInput = $state<HTMLInputElement | null>(null);
  function onFilePick(e: Event): void {
    const input = e.currentTarget as HTMLInputElement;
    if (!input.files || input.files.length === 0) return;
    void compose.addAttachments(input.files);
    input.value = '';
  }

  // Compose title based on reply context.
  let composeTitle = $derived.by(() => {
    const kw = compose.replyContext.parentKeyword;
    if (kw === '$forwarded') return t('compose.title.forward');
    return t('compose.title.reply');
  });

  function formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  function formatExpiry(expiresAt: string): string {
    const diffMs = new Date(expiresAt).getTime() - Date.now();
    if (diffMs <= 0) return 'expired';
    const diffDays = Math.floor(diffMs / 86400000);
    if (diffDays >= 2) return `expires in ${diffDays} days`;
    if (diffDays === 1) return 'expires in 1 day';
    const diffHours = Math.floor(diffMs / 3600000);
    if (diffHours >= 1) return `expires in ${diffHours} hour${diffHours > 1 ? 's' : ''}`;
    return 'expires in less than 1 hour';
  }
</script>

{#if compose.isOpen && compose.inlineMode}
  <div class="inline-composer" bind:this={containerEl} data-testid="inline-composer">
    <header class="composer-header">
      <span class="composer-title">{composeTitle}</span>
      <button
        type="button"
        class="header-btn"
        onclick={popOut}
        title={t('compose.popOut')}
        aria-label={t('compose.popOut')}
      >
        <!-- Expand / pop-out icon: two overlapping squares -->
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor"
          stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <polyline points="15 3 21 3 21 9" />
          <polyline points="9 21 3 21 3 15" />
          <line x1="21" y1="3" x2="14" y2="10" />
          <line x1="3" y1="21" x2="10" y2="14" />
        </svg>
      </button>
      <button
        type="button"
        class="header-btn"
        onclick={() => void discardWithConfirm()}
        title={t('compose.close')}
        aria-label={t('compose.close')}
      >
        &#215;
      </button>
    </header>

    <div class="composer-fields">
      <div class="field-row recipient-row">
        <RecipientField
          label={t('compose.to')}
          chips={compose.toRecipients}
          onChipsChange={(chips) => {
            compose.toRecipients = chips;
            compose.to = chips.map(recipientToString).join(', ');
          }}
          onWarning={(w) => (toWarning = w)}
          disabled={compose.status === 'sending'}
          autofocus={!compose.replyContext.parentId}
        />
        {#if !compose.ccBccVisible}
          <button
            type="button"
            class="cc-bcc-toggle"
            onclick={() => (compose.ccBccVisible = true)}
          >
            {t('compose.toggleCcBcc')}
          </button>
        {/if}
      </div>
      {#if toWarning}
        <p class="field-warning" role="alert">{toWarning}</p>
      {/if}

      {#if compose.ccBccVisible}
        <div class="field-row recipient-row">
          <RecipientField
            label={t('compose.cc')}
            chips={compose.ccRecipients}
            onChipsChange={(chips) => {
              compose.ccRecipients = chips;
              compose.cc = chips.map(recipientToString).join(', ');
            }}
            onWarning={(w) => (ccWarning = w)}
            disabled={compose.status === 'sending'}
          />
        </div>
        {#if ccWarning}
          <p class="field-warning" role="alert">{ccWarning}</p>
        {/if}
        <div class="field-row recipient-row">
          <RecipientField
            label={t('compose.bcc')}
            chips={compose.bccRecipients}
            onChipsChange={(chips) => {
              compose.bccRecipients = chips;
              compose.bcc = chips.map(recipientToString).join(', ');
            }}
            onWarning={(w) => (bccWarning = w)}
            disabled={compose.status === 'sending'}
          />
        </div>
        {#if bccWarning}
          <p class="field-warning" role="alert">{bccWarning}</p>
        {/if}
      {/if}
    </div>

    <div class="composer-body">
      {#key compose.replyContext.parentId ?? '__blank__'}
        <RichEditor
          {initialHtml}
          autofocus={Boolean(compose.replyContext.parentId)}
          onUpdate={onEditorUpdate}
          onActiveChange={(a) => (active = a)}
          onView={(v) => (editorView = v)}
          {onImageRemoved}
          {uploadingSrcs}
        />
      {/key}
      <ComposeToolbar view={editorView} {active} />
    </div>

    {#if nonInlineAttachments.length > 0}
      <div class="attachment-strip">
        <ul class="attachments-list">
          {#each nonInlineAttachments as a (a.key)}
            <li class:failed={a.status === 'failed'}>
              <span class="att-name">{a.name}</span>
              <span class="att-size">{formatSize(a.size)}</span>
              <span class="att-status">
                {#if a.status === 'uploading'}Uploading...{:else if a.status === 'failed'}{a.error ?? 'Upload failed'}{:else}Ready{/if}
              </span>
              <button
                type="button"
                class="att-remove"
                aria-label="Remove {a.name}"
                onclick={() => compose.removeAttachment(a.key)}
              >&#215;</button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if compose.shares.length > 0}
      <div class="shares-strip">
        <ul class="shares-list">
          {#each compose.shares as s (s.key)}
            {@const badge = attachmentBadge(s.type, s.name)}
            <li class="share-item">
              <span
                class="share-type-badge"
                style="--icon-bg: {badge.bg}"
                aria-hidden="true"
              >{badge.label}</span>
              <span class="share-name">{s.name}</span>
              <span class="share-size">{formatSize(s.size)}</span>
              <span class="share-expiry">{formatExpiry(s.expiresAt)}</span>
              {#if s.maxDownloads !== null}
                <span class="share-cap" title="Download limit">{s.maxDownloads} dl max</span>
              {/if}
              <button
                type="button"
                class="share-remove"
                aria-label="Remove shared link for {s.name}"
                onclick={() => void compose.removeShare(s.key)}
              >&#215;</button>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if compose.errorMessage}
      <p class="composer-error" role="alert">{compose.errorMessage}</p>
    {/if}

    <footer class="composer-footer">
      <button
        type="button"
        class="attach-btn"
        onclick={() => fileInput?.click()}
        disabled={compose.status === 'sending'}
        title={t('compose.attach')}
      >
        {t('compose.attach')}
      </button>
      <input
        bind:this={fileInput}
        type="file"
        multiple
        hidden
        onchange={onFilePick}
      />
      <span class="footer-spacer"></span>
      <button
        type="button"
        class="send-btn secondary"
        onclick={() => void sendOnly()}
        disabled={!canSend}
        title={compose.attachmentsBusy
          ? 'Attachments still uploading'
          : hasRecipientWarnings
            ? 'Fix recipient warnings before sending'
            : undefined}
        data-testid="inline-send"
      >
        {compose.status === 'sending' ? t('compose.sending') : t('compose.send')}
      </button>
      <button
        type="button"
        class="send-btn primary"
        onclick={() => void sendAndArchive()}
        disabled={!canSend}
        data-testid="inline-send-archive"
      >
        {compose.status === 'sending' ? t('compose.sending') : t('compose.sendAndArchive')}
      </button>
    </footer>
  </div>
{/if}

<style>
  .inline-composer {
    margin: var(--spacing-05);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--layer-01);
    display: flex;
    flex-direction: column;
    /* Ensure the composer doesn't squash below a minimum usable height. */
    min-height: 240px;
  }

  /* ── Header ─────────────────────────────────────────────────────────── */
  .composer-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-02);
    border-radius: var(--radius-md) var(--radius-md) 0 0;
  }
  .composer-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
    flex: 1;
  }
  .header-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    background: transparent;
    font-size: 18px;
    line-height: 1;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .header-btn:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  /* ── Fields (recipients) ─────────────────────────────────────────────── */
  .composer-fields {
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .field-row {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-03);
    padding: 0 var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
    min-height: 40px;
  }
  .field-row:last-child {
    border-bottom: none;
  }
  .field-warning {
    padding: var(--spacing-01) var(--spacing-04);
    color: var(--support-warning);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .cc-bcc-toggle {
    flex-shrink: 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    background: transparent;
    padding: 0;
  }
  .cc-bcc-toggle:hover {
    color: var(--text-primary);
  }

  /* ── Body (editor + toolbar) ─────────────────────────────────────────── */
  .composer-body {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-height: 120px;
    overflow: hidden;
  }

  /* ── Attachment strip ────────────────────────────────────────────────── */
  .attachment-strip,
  .shares-strip {
    padding: var(--spacing-02) var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
  }
  .attachments-list,
  .shares-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
  }
  .attachments-list li {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }
  .attachments-list li.failed {
    border-color: var(--support-error);
  }
  .att-name {
    font-weight: 500;
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .att-size,
  .att-status {
    color: var(--text-helper);
    white-space: nowrap;
  }
  .att-remove,
  .share-remove {
    background: transparent;
    color: var(--text-helper);
    font-size: 16px;
    line-height: 1;
    padding: 0 2px;
  }
  .att-remove:hover,
  .share-remove:hover {
    color: var(--support-error);
  }
  .share-item {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }
  .share-type-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    background: var(--icon-bg, var(--layer-03));
    border-radius: 2px;
    font-size: 10px;
    font-weight: 700;
    color: #fff;
  }
  .share-name {
    font-weight: 500;
    max-width: 140px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .share-size,
  .share-expiry,
  .share-cap {
    color: var(--text-helper);
    white-space: nowrap;
  }

  /* ── Error ───────────────────────────────────────────────────────────── */
  .composer-error {
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
    border-top: 1px solid var(--border-subtle-01);
  }

  /* ── Footer ──────────────────────────────────────────────────────────── */
  .composer-footer {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
    background: var(--layer-02);
    border-radius: 0 0 var(--radius-md) var(--radius-md);
    flex-wrap: wrap;
  }
  .footer-spacer {
    flex: 1;
  }
  .attach-btn {
    color: var(--text-secondary);
    background: transparent;
    font-size: var(--type-body-compact-01-size);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-pill);
    min-height: 32px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .attach-btn:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .attach-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .send-btn {
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: 36px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .send-btn.secondary {
    background: transparent;
    border: 1px solid var(--border-strong-01);
    color: var(--text-primary);
  }
  .send-btn.secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .send-btn.primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border: none;
  }
  .send-btn.primary:hover:not(:disabled) {
    background: var(--interactive-hover);
  }
  .send-btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  @media print {
    .inline-composer {
      display: none;
    }
  }
</style>
