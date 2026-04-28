<script lang="ts">
  import { untrack } from 'svelte';
  import { compose } from './compose.svelte';
  import { composeStack } from './compose-stack.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { mail } from '../mail/store.svelte';
  import RichEditor from './RichEditor.svelte';
  import ComposeToolbar from './ComposeToolbar.svelte';
  import AddressAutocomplete from './AddressAutocomplete.svelte';
  import { confirm } from '../dialog/confirm.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { EMPTY_ACTIVE, type ActiveState } from './editor';
  import type { EditorView } from 'prosemirror-view';

  // Per-compose keyboard layer: Mod+Enter sends, Escape closes.
  // Both pass through input-focus carve-outs (see keyboard engine
  // shouldSkipForFocus: Escape and Mod+Enter always pass through).
  $effect(() => {
    if (!compose.isOpen) return;
    const pop = keyboard.pushLayer([
      {
        key: 'Mod+Enter',
        description: 'Send',
        action: () => {
          sendWithWarn();
        },
      },
      {
        key: 'Mod+M',
        description: 'Minimize compose',
        action: () => composeStack.minimizeCurrent(),
      },
      {
        key: 'Escape',
        description: 'Close compose',
        action: () => closeWithConfirm(),
      },
    ]);
    return pop;
  });

  // Confirm before throwing away in-progress content. The "sending" state
  // is the user's commit point — discarding while sending means cancelling
  // the request, which the close path doesn't currently do; we just route
  // close through the same prompt for consistency. When the auto-save
  // path has already created a server draft, route through compose.discard
  // so the row is removed instead of stranded.
  async function closeWithConfirm(): Promise<void> {
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

  // Focus the right field when compose opens — for reply / forward, the
  // ProseMirror editor; otherwise the To field. (The editor's own
  // autofocus handles cursor placement; replies pre-populate the body
  // such that the empty leading paragraphs put the caret at the top.)
  let modalEl = $state<HTMLElement | null>(null);
  $effect(() => {
    if (compose.status !== 'editing') return;
    requestAnimationFrame(() => {
      if (!compose.replyContext.parentId && modalEl) {
        // Focus the To-field input — the AddressAutocomplete's <input>
        // is the first text input inside the modal, so query for it.
        const first = modalEl.querySelector<HTMLInputElement>(
          '.row input[type="text"]',
        );
        first?.focus();
      }
    });
  });

  let identity = $derived(mail.primaryIdentity);

  // Auto-save the draft after a short period of typing inactivity so a
  // closed-tab / reload does not lose the user's work. We track every
  // user-edited field; the timer resets on each change and persists
  // when the form goes idle for AUTOSAVE_IDLE_MS. compose.persistDraft
  // is itself idempotent and a no-op for empty forms.
  const AUTOSAVE_IDLE_MS = 4000;
  let autosaveTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    // Reactive deps: every editable field on the compose form.
    const _to = compose.to;
    const _cc = compose.cc;
    const _bcc = compose.bcc;
    const _subj = compose.subject;
    const _body = compose.body;
    void _to;
    void _cc;
    void _bcc;
    void _subj;
    void _body;
    if (!compose.isOpen || compose.status === 'sending') return;
    if (autosaveTimer !== null) clearTimeout(autosaveTimer);
    autosaveTimer = setTimeout(() => {
      autosaveTimer = null;
      void compose.persistDraft();
    }, AUTOSAVE_IDLE_MS);
    return () => {
      if (autosaveTimer !== null) {
        clearTimeout(autosaveTimer);
        autosaveTimer = null;
      }
    };
  });

  // Editor view bridge for the toolbar.
  let editorView = $state<EditorView | null>(null);
  let active = $state<ActiveState>(EMPTY_ACTIVE);

  // File picker + drag-drop. The hidden input is triggered by the
  // toolbar Attach button; the modal-level drag handlers below show a
  // drop overlay and route File objects to compose.addAttachments.
  let fileInput = $state<HTMLInputElement | null>(null);
  let dragActive = $state(false);
  let dragDepth = 0;

  function onFilePick(e: Event): void {
    const input = e.currentTarget as HTMLInputElement;
    if (!input.files || input.files.length === 0) return;
    void compose.addAttachments(input.files);
    // Reset so the same file can be picked again immediately afterward.
    input.value = '';
  }
  function onDragEnter(e: DragEvent): void {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth++;
    dragActive = true;
  }
  function onDragOver(e: DragEvent): void {
    if (!hasFiles(e)) return;
    e.preventDefault();
  }
  function onDragLeave(): void {
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) dragActive = false;
  }
  function onDrop(e: DragEvent): void {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    dragActive = false;
    const files = e.dataTransfer?.files;
    if (files && files.length > 0) void compose.addAttachments(files);
  }
  function hasFiles(e: DragEvent): boolean {
    return Boolean(e.dataTransfer?.types.includes('Files'));
  }
  function formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  function onEditorUpdate(html: string, _text: string): void {
    compose.body = html;
  }

  // Warn the user before sending a message that's missing the subject
  // or body — easy to forget and easy to embarrass yourself with.
  // Empty body is detected by stripping HTML tags and checking for
  // visible characters; the editor renders one or more empty <p> tags
  // even when nothing has been typed.
  async function sendWithWarn(): Promise<void> {
    // No recipients — surface the error inline AND move the cursor to
    // the To field so the user can type immediately. compose.send()
    // would set the same error message but leaves focus on Send,
    // forcing an extra click to fix it.
    const noRecipients =
      compose.to.trim().length === 0 &&
      compose.cc.trim().length === 0 &&
      compose.bcc.trim().length === 0;
    if (noRecipients) {
      compose.errorMessage = 'At least one recipient is required';
      const toInput = modalEl?.querySelector<HTMLInputElement>(
        '.row input[type="text"]',
      );
      toInput?.focus();
      return;
    }
    const subjectEmpty = compose.subject.trim().length === 0;
    const bodyText = compose.body.replace(/<[^>]+>/g, '').trim();
    const bodyEmpty = bodyText.length === 0;
    if (subjectEmpty || bodyEmpty) {
      const missing = subjectEmpty && bodyEmpty
        ? 'subject and body'
        : subjectEmpty
          ? 'subject'
          : 'body';
      const ok = await confirm.ask({
        title: `Send without a ${missing}?`,
        message: 'You can keep editing instead.',
        confirmLabel: 'Send anyway',
        cancelLabel: 'Keep editing',
      });
      if (!ok) return;
    }
    void compose.send();
  }

  // Snapshot the initial body once per open so the editor doesn't
  // re-mount as the user types (which would write into compose.body and
  // re-trigger the prop). The key on RichEditor below ensures it remounts
  // only when compose transitions from idle -> editing.
  //
  // IMPORTANT: this must be $state, not $derived. A $derived that reads
  // compose.body would recompute on every keystroke (because onEditorUpdate
  // writes compose.body), causing $effect in RichEditor to destroy and
  // recreate the ProseMirror view on each character, which resets the cursor
  // to position 0 and produces reversed text. The $effect below reads
  // compose.status (not compose.body) so it only fires on open/close
  // transitions, capturing compose.body as a one-time snapshot.
  let initialHtml = $state('');
  $effect(() => {
    if (compose.status === 'editing') {
      // Read compose.body via untrack so this effect only re-runs when
      // compose.status changes, not on every keystroke. compose.body is
      // written by onEditorUpdate on every character; tracking it here would
      // recompute initialHtml on each keystroke, which would cause
      // RichEditor's $effect to recreate the ProseMirror view, resetting the
      // cursor to position 0 and producing reversed text.
      initialHtml = untrack(() => compose.body);
    } else {
      initialHtml = '';
    }
  });
</script>

{#if compose.isOpen}
  <div class="backdrop" onclick={closeWithConfirm} aria-hidden="true"></div>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="compose-title"
    tabindex="-1"
    bind:this={modalEl}
    ondragenter={onDragEnter}
    ondragover={onDragOver}
    ondragleave={onDragLeave}
    ondrop={onDrop}
  >
    <header class="modal-header">
      <h2 id="compose-title">
        {#if compose.replyContext.parentKeyword === '$answered'}
          {t('compose.title.reply')}
        {:else if compose.replyContext.parentKeyword === '$forwarded'}
          {t('compose.title.forward')}
        {:else}
          {t('compose.title.new')}
        {/if}
      </h2>
      <button
        type="button"
        class="minimize"
        onclick={() => composeStack.minimizeCurrent()}
        aria-label={t('compose.minimize')}
        title={t('compose.minimize')}
      >
        —
      </button>
      <button
        type="button"
        class="close"
        onclick={closeWithConfirm}
        aria-label={t('compose.close')}
      >
        ×
      </button>
    </header>

    <div class="fields">
      <div class="row">
        <span class="label">{t('compose.from')}</span>
        <span class="from-display">
          {#if identity}
            {identity.name ? `${identity.name} <${identity.email}>` : identity.email}
          {:else}
            <span class="muted">Loading identity…</span>
          {/if}
        </span>
      </div>

      <div class="row">
        <span class="label">{t('compose.to')}</span>
        <AddressAutocomplete
          bind:value={compose.to}
          onChange={(v) => (compose.to = v)}
          placeholder="recipient@example.com"
          disabled={compose.status === 'sending'}
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

      {#if compose.ccBccVisible}
        <div class="row">
          <span class="label">{t('compose.cc')}</span>
          <AddressAutocomplete
            bind:value={compose.cc}
            onChange={(v) => (compose.cc = v)}
            disabled={compose.status === 'sending'}
          />
        </div>
        <div class="row">
          <span class="label">{t('compose.bcc')}</span>
          <AddressAutocomplete
            bind:value={compose.bcc}
            onChange={(v) => (compose.bcc = v)}
            disabled={compose.status === 'sending'}
          />
        </div>
      {/if}

      <label class="row">
        <span class="label">{t('compose.subject')}</span>
        <input
          bind:value={compose.subject}
          type="text"
          spellcheck="true"
          disabled={compose.status === 'sending'}
        />
      </label>

      <div class="row body-row">
        <span class="label">{t('compose.body')}</span>
        <div class="body-stack">
          {#key compose.replyContext.parentId ?? '__blank__'}
            <RichEditor
              {initialHtml}
              autofocus={Boolean(compose.replyContext.parentId)}
              onUpdate={onEditorUpdate}
              onActiveChange={(a) => (active = a)}
              onView={(v) => (editorView = v)}
            />
          {/key}
          <ComposeToolbar view={editorView} {active} />
        </div>
      </div>

      {#if compose.attachments.length > 0}
        <div class="row attachments-row">
          <span class="label">{t('compose.attached')}</span>
          <ul class="attachments-list">
            {#each compose.attachments as a (a.key)}
              <li class:failed={a.status === 'failed'}>
                <span class="att-name">{a.name}</span>
                <span class="att-size">{formatSize(a.size)}</span>
                <span class="att-status">
                  {#if a.status === 'uploading'}
                    Uploading…
                  {:else if a.status === 'failed'}
                    {a.error ?? 'Upload failed'}
                  {:else}
                    Ready
                  {/if}
                </span>
                <button
                  type="button"
                  class="att-remove"
                  aria-label="Remove {a.name}"
                  onclick={() => compose.removeAttachment(a.key)}
                >
                  ×
                </button>
              </li>
            {/each}
          </ul>
        </div>
      {/if}
    </div>

    {#if dragActive}
      <div class="drop-overlay" aria-hidden="true">
        <p>{t('compose.dropToAttach')}</p>
      </div>
    {/if}

    {#if compose.errorMessage}
      <p class="error" role="alert">{compose.errorMessage}</p>
    {/if}

    <footer class="modal-footer">
      <button
        type="button"
        class="attach"
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
        class="discard"
        onclick={closeWithConfirm}
        disabled={compose.status === 'sending'}
      >
        {t('compose.discard')}
      </button>
      <button
        type="button"
        class="send"
        onclick={sendWithWarn}
        disabled={compose.status === 'sending' || compose.attachmentsBusy}
        title={compose.attachmentsBusy ? 'Attachments still uploading' : ''}
      >
        {compose.status === 'sending' ? t('compose.sending') : t('compose.send')}
      </button>
    </footer>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 900;
    animation: fade-in var(--duration-fast-02) var(--easing-productive-enter);
  }
  .modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(720px, calc(100vw - 2 * var(--spacing-05)));
    max-height: calc(100vh - 2 * var(--spacing-05));
    display: flex;
    flex-direction: column;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 901;
    overflow: hidden;
    animation: rise var(--duration-moderate-01) var(--easing-productive-enter);
  }

  .modal-header {
    display: flex;
    align-items: center;
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }
  h2 {
    margin: 0;
    flex: 1;
    font-size: var(--type-heading-01-size);
    line-height: var(--type-heading-01-line);
    font-weight: var(--type-heading-01-weight);
  }
  .close,
  .minimize {
    color: var(--text-helper);
    font-size: 20px;
    line-height: 1;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
  }
  .minimize {
    font-weight: 600;
    margin-right: var(--spacing-01);
  }
  .close:hover,
  .minimize:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .fields {
    padding: var(--spacing-04) var(--spacing-05);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    overflow: auto;
    flex: 1;
  }
  .row {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-04);
    padding: var(--spacing-02) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .label {
    width: 6em;
    flex: 0 0 auto;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .from-display {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .muted {
    color: var(--text-helper);
    font-style: italic;
  }
  input[type='text'] {
    flex: 1;
    background: none;
    border: none;
    outline: none;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: 0;
  }
  input[type='text']::placeholder {
    color: var(--text-helper);
  }

  .body-row {
    align-items: flex-start;
    border-bottom: none;
  }
  .body-stack {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    min-width: 0;
  }
  input[type='text']:focus {
    box-shadow: 0 0 0 2px var(--focus);
    border-radius: var(--radius-sm);
  }

  .cc-bcc-toggle {
    flex: 0 0 auto;
    margin-left: var(--spacing-03);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    background: none;
    padding: var(--spacing-01) var(--spacing-02);
    border-radius: var(--radius-sm);
  }
  .cc-bcc-toggle:hover {
    color: var(--text-primary);
    background: var(--layer-03);
  }

  .error {
    margin: 0 var(--spacing-05);
    padding: var(--spacing-03) var(--spacing-04);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }

  .modal-footer {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-04) var(--spacing-05);
    border-top: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .footer-spacer {
    flex: 1;
  }
  .send,
  .discard,
  .attach {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .attach {
    color: var(--text-secondary);
  }
  .attach:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .attach:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .attachments-row {
    align-items: flex-start;
  }
  .attachments-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    flex: 1;
  }
  .attachments-list li {
    display: grid;
    grid-template-columns: 1fr auto auto auto;
    gap: var(--spacing-03);
    align-items: center;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }
  .attachments-list li.failed {
    border-color: var(--support-error);
  }
  .att-name {
    color: var(--text-primary);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .att-size,
  .att-status {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .attachments-list li.failed .att-status {
    color: var(--support-error);
  }
  .att-remove {
    width: 24px;
    height: 24px;
    color: var(--text-helper);
    border-radius: var(--radius-pill);
    line-height: 1;
  }
  .att-remove:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .drop-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(15, 98, 254, 0.15);
    border: 2px dashed var(--interactive);
    color: var(--interactive);
    font-weight: 600;
    font-size: var(--type-heading-01-size);
    pointer-events: none;
    z-index: 1;
  }
  .send {
    background: var(--interactive);
    color: var(--text-on-color);
  }
  .send:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .send:disabled,
  .discard:disabled {
    opacity: 0.5;
    cursor: progress;
  }
  .discard {
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
  }
  .discard:hover:not(:disabled) {
    background: var(--layer-03);
  }

  @keyframes fade-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  @keyframes rise {
    from {
      transform: translate(-50%, -45%);
      opacity: 0;
    }
    to {
      transform: translate(-50%, -50%);
      opacity: 1;
    }
  }
  @media (max-width: 640px) {
    .modal {
      top: 0;
      left: 0;
      transform: none;
      width: 100vw;
      max-height: 100vh;
      max-height: 100dvh;
      height: 100vh;
      height: 100dvh;
      border-radius: 0;
      border: none;
    }
    .modal-header {
      padding: var(--spacing-03) var(--spacing-04);
    }
    .fields {
      padding: var(--spacing-03) var(--spacing-04);
    }
    .row .label {
      min-width: 6em;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .backdrop,
    .modal {
      animation: none;
    }
  }
</style>
