<script lang="ts">
  import { untrack } from 'svelte';
  import { compose, bodyTextWithoutSignature, type Recipient } from './compose.svelte';
  import { composeStack } from './compose-stack.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { mail } from '../mail/store.svelte';
  import RichEditor from './RichEditor.svelte';
  import ComposeToolbar from './ComposeToolbar.svelte';
  import RecipientField from './RecipientField.svelte';
  import { confirm } from '../dialog/confirm.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { EMPTY_ACTIVE, type ActiveState, applyImage } from './editor';
  import type { EditorView } from 'prosemirror-view';
  import { recipientToString } from './recipient-parse';
  import { hasExternalSubmission } from '../auth/capabilities';
  import { submissionStore } from '../identities/identity-submission.svelte';

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

  // Per-field recipient warnings (REQ-MAIL-11d). Each is set by the
  // RecipientField component when the input buffer has unrecognized text.
  // Send is blocked while any is non-null.
  let toWarning = $state<string | null>(null);
  let ccWarning = $state<string | null>(null);
  let bccWarning = $state<string | null>(null);
  let hasRecipientWarnings = $derived(
    toWarning !== null || ccWarning !== null || bccWarning !== null,
  );
  $effect(() => {
    if (compose.status !== 'editing') return;
    requestAnimationFrame(() => {
      if (!compose.replyContext.parentId && modalEl) {
        // Focus the To-field input — the RecipientField's <input> is
        // the first text input inside the modal.
        const first = modalEl.querySelector<HTMLInputElement>(
          '.recipient-field input[type="text"]',
        );
        first?.focus();
      }
    });
  });

  let identity = $derived(mail.primaryIdentity);

  // External submission indicator (REQ-MAIL-SUBMIT-05).
  // Cosmetic only — shows a small marker next to the From identity when
  // it has external submission configured.
  let showExtSub = $derived(hasExternalSubmission());
  let extSubHandle = $derived(
    identity && showExtSub ? submissionStore.forIdentity(identity.id) : null,
  );
  $effect(() => {
    if (extSubHandle) void extSubHandle.load();
  });
  let identityHasExternalConfig = $derived(
    extSubHandle?.data?.configured === true,
  );

  // Auto-save the draft after a short period of typing inactivity so a
  // closed-tab / reload does not lose the user's work. We track every
  // user-edited field; the timer resets on each change and persists
  // when the form goes idle for AUTOSAVE_IDLE_MS. compose.persistDraft
  // is itself idempotent and a no-op for empty forms.
  const AUTOSAVE_IDLE_MS = 4000;
  let autosaveTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    // Reactive deps: every editable field on the compose form. Track the
    // recipient arrays (which drive the string fields) rather than just
    // the strings so chip additions/removals trigger the autosave timer.
    const _to = compose.toRecipients;
    const _cc = compose.ccRecipients;
    const _bcc = compose.bccRecipients;
    const _toStr = compose.to;
    const _ccStr = compose.cc;
    const _bccStr = compose.bcc;
    const _subj = compose.subject;
    const _body = compose.body;
    void _to;
    void _cc;
    void _bcc;
    void _toStr;
    void _ccStr;
    void _bccStr;
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

  // File picker — triggered by the toolbar Attach button. Always attaches
  // (never inlines) per REQ-ATT-01.
  let fileInput = $state<HTMLInputElement | null>(null);

  function onFilePick(e: Event): void {
    const input = e.currentTarget as HTMLInputElement;
    if (!input.files || input.files.length === 0) return;
    void compose.addAttachments(input.files);
    // Reset so the same file can be picked again immediately afterward.
    input.value = '';
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
    // Recipient fields have stranded unparsed text (REQ-MAIL-11d).
    if (hasRecipientWarnings) {
      compose.errorMessage = 'Fix recipient warnings before sending';
      return;
    }

    // No recipients — surface the error inline AND move the cursor to
    // the To field so the user can type immediately. compose.send()
    // would set the same error message but leaves focus on Send,
    // forcing an extra click to fix it.
    const noRecipients =
      compose.toRecipients.length === 0 &&
      compose.ccRecipients.length === 0 &&
      compose.bccRecipients.length === 0 &&
      compose.to.trim().length === 0 &&
      compose.cc.trim().length === 0 &&
      compose.bcc.trim().length === 0;
    if (noRecipients) {
      compose.errorMessage = 'At least one recipient is required';
      const toInput = modalEl?.querySelector<HTMLInputElement>(
        '.recipient-field input[type="text"]',
      );
      toInput?.focus();
      return;
    }
    const subjectEmpty = compose.subject.trim().length === 0;
    // Treat the auto-inserted signature as not-user-authored, so a body
    // that contains only the signature still triggers the empty-body
    // warning (issue #21).
    const bodyEmpty = bodyTextWithoutSignature(compose.body).length === 0;
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

  // ── G15: Dual drop targets ─────────────────────────────────────────────
  //
  // Design: Two distinct drop zones become visible while a drag is active.
  //   - Inline drop zone: overlays the body editor (position: absolute inside
  //     body-stack); accepts image files, calls addInlineImage, inserts into
  //     ProseMirror editor at the current cursor position.
  //   - Attachment drop zone: rendered below the editor, above the chip strip;
  //     accepts any file, calls addAttachments.
  //
  // Drag tracking uses a depth counter on the modal root to handle
  // dragenter/dragleave bubbling correctly across child elements.
  // The two drop zones each track their own hover state independently.
  //
  // Disposition flip: each attachment chip carries a draggable handle that
  // sets a custom MIME type 'application/x-herold-compose-part' with the
  // chip key. Dropping on the opposite zone calls flipToInline /
  // flipToAttachment, which mutates the attachment record and (for
  // inline->attachment) removes the cid: img from the body.

  let dragActive = $state(false);
  let dragDepth = 0;

  // Hover state for each zone — drives the highlighted CSS class.
  let inlineZoneHover = $state(false);
  let attachZoneHover = $state(false);

  /** True if the drag event carries OS-level files (external drag). */
  function hasFiles(e: DragEvent): boolean {
    return Boolean(e.dataTransfer?.types.includes('Files'));
  }

  /** True if the drag event carries our custom compose-part type. */
  function hasComposePart(e: DragEvent): boolean {
    return Boolean(
      e.dataTransfer?.types.includes('application/x-herold-compose-part'),
    );
  }

  function isRelevantDrag(e: DragEvent): boolean {
    return hasFiles(e) || hasComposePart(e);
  }

  // Modal-level drag tracking.
  function onModalDragEnter(e: DragEvent): void {
    if (!isRelevantDrag(e)) return;
    e.preventDefault();
    dragDepth++;
    dragActive = true;
  }
  function onModalDragOver(e: DragEvent): void {
    if (!isRelevantDrag(e)) return;
    e.preventDefault();
  }
  function onModalDragLeave(): void {
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) {
      dragActive = false;
      inlineZoneHover = false;
      attachZoneHover = false;
    }
  }
  function onModalDrop(e: DragEvent): void {
    // If a drop lands somewhere inside the modal that is NOT one of our
    // two explicit zones (e.g. the subject field, a header row), ignore
    // the files rather than auto-routing. The two zone handlers call
    // stopPropagation() so they don't reach here.
    e.preventDefault();
    dragDepth = 0;
    dragActive = false;
    inlineZoneHover = false;
    attachZoneHover = false;
  }

  // Inline zone handlers.
  function onInlineZoneDragEnter(e: DragEvent): void {
    e.preventDefault();
    e.stopPropagation();
    inlineZoneHover = true;
  }
  function onInlineZoneDragOver(e: DragEvent): void {
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = hasComposePart(e) ? 'move' : 'copy';
    }
  }
  function onInlineZoneDragLeave(e: DragEvent): void {
    // Only clear hover when leaving the zone element itself (not a child).
    if (!(e.currentTarget as Element).contains(e.relatedTarget as Node | null)) {
      inlineZoneHover = false;
    }
  }
  function onInlineZoneDrop(e: DragEvent): void {
    e.preventDefault();
    e.stopPropagation();
    dragDepth = 0;
    dragActive = false;
    inlineZoneHover = false;
    attachZoneHover = false;

    // Disposition flip: compose-part key dropped from the attachment strip.
    const partKey = e.dataTransfer?.getData('application/x-herold-compose-part');
    if (partKey) {
      void compose.flipToInline(partKey, editorView);
      return;
    }

    // External file drop: inline the first image, ignore non-images.
    const files = e.dataTransfer?.files;
    if (!files || files.length === 0) return;
    const file = files[0];
    if (!file || !file.type.startsWith('image/')) return;
    void handleInlineDrop(file);
  }

  async function handleInlineDrop(file: File): Promise<void> {
    const result = await compose.addInlineImage(file);
    if (!result) return;
    // Insert <img src="<objectURL>" alt="<filename>"> at the current
    // cursor in the ProseMirror editor. The objectURL is used for the
    // in-composition preview; rewriteInlineImageURLs rewrites it to
    // cid: on send/save.
    applyImage(editorView, result.objectURL, file.name);
  }

  // Attachment zone handlers.
  function onAttachZoneDragEnter(e: DragEvent): void {
    e.preventDefault();
    e.stopPropagation();
    attachZoneHover = true;
  }
  function onAttachZoneDragOver(e: DragEvent): void {
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = hasComposePart(e) ? 'move' : 'copy';
    }
  }
  function onAttachZoneDragLeave(e: DragEvent): void {
    if (!(e.currentTarget as Element).contains(e.relatedTarget as Node | null)) {
      attachZoneHover = false;
    }
  }
  function onAttachZoneDrop(e: DragEvent): void {
    e.preventDefault();
    e.stopPropagation();
    dragDepth = 0;
    dragActive = false;
    inlineZoneHover = false;
    attachZoneHover = false;

    // Disposition flip: compose-part key dropped from the body (inline chip).
    const partKey = e.dataTransfer?.getData('application/x-herold-compose-part');
    if (partKey) {
      void compose.flipToAttachment(partKey);
      return;
    }

    // External file drop: attach normally.
    const files = e.dataTransfer?.files;
    if (files && files.length > 0) void compose.addAttachments(files);
  }

  // Chip drag-start: encode the attachment key as the custom MIME type so
  // drop targets can recognise an intra-compose drag.
  function onChipDragStart(e: DragEvent, key: string): void {
    e.stopPropagation();
    if (e.dataTransfer) {
      e.dataTransfer.setData('application/x-herold-compose-part', key);
      e.dataTransfer.effectAllowed = 'move';
    }
  }
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
    ondragenter={onModalDragEnter}
    ondragover={onModalDragOver}
    ondragleave={onModalDragLeave}
    ondrop={onModalDrop}
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
            {#if identityHasExternalConfig}
              <span
                class="ext-sub-indicator"
                title="Mail sent via external SMTP"
                aria-label="External SMTP"
              >[ext]</span>
            {/if}
          {:else}
            <span class="muted">Loading identity…</span>
          {/if}
        </span>
      </div>

      <div class="row recipient-row">
        <RecipientField
          label={t('compose.to')}
          chips={compose.toRecipients}
          onChipsChange={(chips) => {
            compose.toRecipients = chips;
            compose.to = chips.map(recipientToString).join(', ');
          }}
          onWarning={(w) => (toWarning = w)}
          placeholder="recipient@example.com"
          disabled={compose.status === 'sending'}
          autofocus={!compose.replyContext.parentId && compose.status === 'editing'}
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
        <div class="row recipient-row">
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
        <div class="row recipient-row">
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
          <!-- Inline drop zone: overlays the editor during drag. Positioned
               absolutely so it does not affect the editor layout at all when
               not visible (dragActive = false). -->
          <div
            class="zone-container"
          >
            {#key compose.replyContext.parentId ?? '__blank__'}
              <RichEditor
                {initialHtml}
                autofocus={Boolean(compose.replyContext.parentId)}
                onUpdate={onEditorUpdate}
                onActiveChange={(a) => (active = a)}
                onView={(v) => (editorView = v)}
              />
            {/key}
            {#if dragActive}
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div
                class="inline-drop-zone"
                class:hover={inlineZoneHover}
                aria-label={t('compose.dropInline')}
                ondragenter={onInlineZoneDragEnter}
                ondragover={onInlineZoneDragOver}
                ondragleave={onInlineZoneDragLeave}
                ondrop={onInlineZoneDrop}
              >
                <span class="zone-label">{t('compose.dropInline')}</span>
              </div>
            {/if}
          </div>
          <ComposeToolbar view={editorView} {active} />

          <!-- Attachment drop zone: always rendered below the toolbar;
               highlighted only during a drag. Quiet hint at rest. -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="attach-drop-zone"
            class:visible={dragActive}
            class:hover={attachZoneHover}
            aria-label={t('compose.dropAttach')}
            ondragenter={onAttachZoneDragEnter}
            ondragover={onAttachZoneDragOver}
            ondragleave={onAttachZoneDragLeave}
            ondrop={onAttachZoneDrop}
          >
            <span class="zone-label">{t('compose.dropAttach')}</span>
          </div>
        </div>
      </div>

      {#if compose.attachments.length > 0}
        {@const attaches = compose.attachments.filter((a) => !a.inline)}
        {@const inlines = compose.attachments.filter((a) => a.inline)}

        {#if attaches.length > 0}
          <div class="row attachments-row">
            <span class="label">{t('compose.attached')}</span>
            <ul class="attachments-list">
              {#each attaches as a (a.key)}
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <li
                  class:failed={a.status === 'failed'}
                  draggable="true"
                  ondragstart={(e) => onChipDragStart(e, a.key)}
                >
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

        {#if inlines.length > 0}
          <div class="row attachments-row">
            <span class="label">Inline</span>
            <ul class="attachments-list">
              {#each inlines as a (a.key)}
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <li
                  class:failed={a.status === 'failed'}
                  draggable="true"
                  ondragstart={(e) => onChipDragStart(e, a.key)}
                >
                  {#if a.objectURL}
                    <img class="att-thumb" src={a.objectURL} alt={a.name} />
                  {/if}
                  <span class="att-name">{a.name}</span>
                  <span class="att-size">{formatSize(a.size)}</span>
                  <span class="att-status">
                    {#if a.status === 'uploading'}
                      Uploading…
                    {:else if a.status === 'failed'}
                      {a.error ?? 'Upload failed'}
                    {:else}
                      Inline
                    {/if}
                  </span>
                  <button
                    type="button"
                    class="att-move"
                    aria-label={t('compose.moveToAttachments')}
                    title={t('compose.moveToAttachments')}
                    onclick={() => void compose.flipToAttachment(a.key)}
                  >
                    &#x2913;
                  </button>
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
      {/if}
    </div>

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
        disabled={compose.status === 'sending' || compose.attachmentsBusy || hasRecipientWarnings}
        title={compose.attachmentsBusy
          ? 'Attachments still uploading'
          : hasRecipientWarnings
            ? 'Fix recipient warnings before sending'
            : ''}
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
    display: inline-flex;
    align-items: baseline;
    gap: var(--spacing-02);
    flex-wrap: wrap;
  }

  /* Cosmetic indicator for external submission (REQ-MAIL-SUBMIT-05). */
  .ext-sub-indicator {
    display: inline-flex;
    align-items: center;
    padding: 1px var(--spacing-02);
    background: color-mix(in srgb, var(--interactive) 12%, transparent);
    color: var(--interactive);
    border: 1px solid color-mix(in srgb, var(--interactive) 35%, transparent);
    border-radius: var(--radius-sm);
    font-size: 10px;
    font-weight: 600;
    font-family: var(--font-mono);
    letter-spacing: 0.04em;
    line-height: 1;
    vertical-align: middle;
    cursor: default;
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

  /* Editor + inline drop zone wrapper */
  .zone-container {
    position: relative;
  }

  /* Inline drop zone: absolutely positioned over the editor; invisible
     at rest and when no drag is active. Becomes a visually distinct
     target only while dragActive is true (rendered conditionally in the
     template above). */
  .inline-drop-zone {
    position: absolute;
    inset: 0;
    z-index: 10;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 2px dashed var(--interactive);
    border-radius: var(--radius-md);
    background: rgba(15, 98, 254, 0.08);
    color: var(--interactive);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    pointer-events: all;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .inline-drop-zone.hover {
    background: rgba(15, 98, 254, 0.2);
    border-color: var(--interactive);
  }

  /* Attachment drop zone: shown below the editor only during a drag.
     Hidden (zero-height, no pointer events) when drag is not active. */
  .attach-drop-zone {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 0;
    overflow: hidden;
    opacity: 0;
    pointer-events: none;
    border: 2px dashed transparent;
    border-radius: var(--radius-md);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter),
      height var(--duration-fast-02) var(--easing-productive-enter),
      background var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .attach-drop-zone.visible {
    height: 48px;
    opacity: 1;
    pointer-events: all;
    border-color: var(--border-strong-01);
    background: var(--layer-01);
    color: var(--text-secondary);
    font-weight: 600;
  }
  .attach-drop-zone.hover {
    background: rgba(15, 98, 254, 0.08);
    border-color: var(--interactive);
    color: var(--interactive);
  }

  .zone-label {
    pointer-events: none;
    user-select: none;
  }

  .error {
    margin: 0 var(--spacing-05);
    padding: var(--spacing-03) var(--spacing-04);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }

  /* Inline field-level warning for unrecognized recipient text (REQ-MAIL-11d). */
  .field-warning {
    margin: 0;
    padding: var(--spacing-01) var(--spacing-05);
    color: var(--support-warning);
    font-size: var(--type-body-compact-01-size);
  }

  /* Recipient rows use RecipientField which draws its own border; suppress
     the duplicate bottom border that .row adds. */
  .recipient-row {
    padding: 0;
    border-bottom: none;
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
    cursor: grab;
  }
  /* Inline chips get a thumbnail column */
  .attachments-list li:has(.att-thumb) {
    grid-template-columns: auto 1fr auto auto auto auto;
  }
  .att-thumb {
    width: 32px;
    height: 32px;
    object-fit: cover;
    border-radius: var(--radius-sm);
    flex-shrink: 0;
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
  .att-move {
    width: 24px;
    height: 24px;
    color: var(--text-helper);
    border-radius: var(--radius-pill);
    line-height: 1;
    font-size: 14px;
  }
  .att-move:hover {
    background: var(--layer-03);
    color: var(--text-primary);
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
