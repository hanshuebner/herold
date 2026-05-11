<script lang="ts">
  /**
   * Per-identity edit dialog (REQ-SET-IDENT-10..13).
   *
   * Hosts the full editor for one Identity at a time:
   *   - Avatar editor (REQ-SET-03b) via IdentityAvatarForm.
   *   - Display-name field (REQ-SET-03a) via IdentityDisplayNameForm.
   *   - Reply-To / Bcc working copies edited locally and committed via
   *     `Identity/set update` on Save (REQ-SET-IDENT-11).
   *   - Plain-text signature editor (REQ-SET-03) via
   *     IdentitySignatureForm.
   *   - External-submission section (REQ-MAIL-SUBMIT-01..06), only when
   *     the capability is advertised AND the identity is verified
   *     (REQ-SET-IDENT-13).
   *
   * Reply-To and Bcc operate on a working copy — the user MUST click
   * "Save" to commit. Closing with unsaved local edits prompts a
   * discard confirmation. The cached `mail.identities` map is updated
   * optimistically on success.
   */

  import { hasExternalSubmission } from '../../lib/auth/capabilities';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import { mail } from '../../lib/mail/store.svelte';
  import { jmap, strict } from '../../lib/jmap/client';
  import { Capability, type Invocation } from '../../lib/jmap/types';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { identityStatus } from '../../lib/identities/identity-status';
  import IdentitySubmissionSection from './IdentitySubmissionSection.svelte';
  import IdentityAvatarForm from './IdentityAvatarForm.svelte';
  import IdentityDisplayNameForm from './IdentityDisplayNameForm.svelte';
  import IdentitySignatureForm from './IdentitySignatureForm.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Address, Identity } from '../../lib/mail/types';

  interface Props {
    identity: Identity;
    /** Called when the dialog should close. */
    onclose: () => void;
    /**
     * When true, scroll the dialog to the submission section after open.
     * Used when the state badge in the identity list is clicked
     * (REQ-MAIL-SUBMIT-04).
     */
    scrollToSubmission?: boolean;
  }

  let { identity, onclose, scrollToSubmission = false }: Props = $props();

  let showExternal = $derived(hasExternalSubmission());
  let verified = $derived(identityStatus(identity) === 'verified');

  /** Ref to the submission section for scrollIntoView. */
  let submissionEl = $state<HTMLElement | null>(null);

  $effect(() => {
    if (scrollToSubmission && submissionEl) {
      submissionEl.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
  });

  function onSubmissionChange(): void {
    // Refresh the store entry so the badge in the settings list updates.
    const handle = submissionStore.forIdentity(identity.id);
    void handle.refresh();
  }

  // ── Working copy for Reply-To / Bcc (REQ-SET-IDENT-11) ─────────────

  function addressesToInput(addrs: Address[] | null): string {
    if (!addrs || addrs.length === 0) return '';
    return addrs[0]?.email ?? '';
  }

  function inputToAddresses(value: string): Address[] | null {
    const trimmed = value.trim();
    if (trimmed === '') return null;
    return [{ name: null, email: trimmed }];
  }

  function isValidEmail(value: string): boolean {
    const trimmed = value.trim();
    if (trimmed === '') return true; // blank is allowed (clears the value)
    // Minimal RFC 5321 syntactic gate; the server validates fully.
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed);
  }

  let replyToDraft = $state('');
  let bccDraft = $state('');
  let replyToSaved = $state('');
  let bccSaved = $state('');
  let saving = $state(false);
  let saveError = $state<string | null>(null);

  // Reset the working copy when the `identity` prop changes. Note the
  // careful avoidance of self-referential reads in the effect body —
  // assigning `savedValue = draft` would track `draft` as a dep, which
  // would re-run the effect on every keystroke and reset the input back
  // to the initial value (subtle Svelte 5 footgun, see web/CLAUDE.md
  // "Patterns to avoid").
  $effect(() => {
    const r = addressesToInput(identity.replyTo ?? null);
    const b = addressesToInput(identity.bcc ?? null);
    replyToDraft = r;
    replyToSaved = r;
    bccDraft = b;
    bccSaved = b;
  });

  let replyToValid = $derived(isValidEmail(replyToDraft));
  let bccValid = $derived(isValidEmail(bccDraft));
  let dirty = $derived(replyToDraft !== replyToSaved || bccDraft !== bccSaved);
  let canSave = $derived(dirty && replyToValid && bccValid && !saving);

  async function save(): Promise<void> {
    if (!canSave) return;
    const accountId = mail.mailAccountId;
    if (!accountId) {
      saveError = t('settings.identity.signatureNoAccount');
      return;
    }
    saving = true;
    saveError = null;
    const update: Record<string, unknown> = {};
    if (replyToDraft !== replyToSaved) {
      update.replyTo = inputToAddresses(replyToDraft);
    }
    if (bccDraft !== bccSaved) {
      update.bcc = inputToAddresses(bccDraft);
    }
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Identity/set',
          { accountId, update: { [identity.id]: update } },
          [Capability.Submission],
        );
      });
      strict(responses);
      const result = invocationArgs<{
        notUpdated?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      const failure = result.notUpdated?.[identity.id];
      if (failure) throw new Error(failure.description ?? failure.type);
      // Mirror into the cache so the row re-renders without a refetch.
      const next = new Map(mail.identities);
      const cur = next.get(identity.id);
      if (cur) {
        const merged: Identity = { ...cur };
        if ('replyTo' in update) merged.replyTo = inputToAddresses(replyToDraft);
        if ('bcc' in update) merged.bcc = inputToAddresses(bccDraft);
        next.set(identity.id, merged);
      }
      mail.identities = next;
      replyToSaved = replyToDraft;
      bccSaved = bccDraft;
      toast.show({ message: t('settings.saved'), timeoutMs: 3000 });
    } catch (err) {
      saveError = err instanceof Error ? err.message : String(err);
      toast.show({
        message: t('settings.saveFailed'),
        kind: 'error',
        timeoutMs: 5000,
      });
    } finally {
      saving = false;
    }
  }

  async function requestClose(): Promise<void> {
    if (!dirty) {
      onclose();
      return;
    }
    const ok = await confirm.ask({
      message: t('settings.identityEdit.discardConfirm'),
    });
    if (ok) onclose();
  }

  function invocationArgs<T>(inv: Invocation | undefined): T {
    if (!inv) throw new Error('Expected method invocation, got undefined');
    return inv[1] as T;
  }

  function onBackdropClick(e: MouseEvent): void {
    if (e.target === e.currentTarget) void requestClose();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      void requestClose();
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="backdrop"
  role="presentation"
  onclick={onBackdropClick}
  onkeydown={onKeyDown}
  aria-hidden="true"
></div>

<div
  class="dialog"
  role="dialog"
  aria-modal="true"
  aria-labelledby="dialog-title-{identity.id}"
  tabindex="-1"
  data-testid="identity-edit-dialog"
>
  <header class="dialog-header">
    <h3 id="dialog-title-{identity.id}" class="dialog-title">
      {t('settings.identityEdit.title')}
    </h3>
    <button
      type="button"
      class="close"
      onclick={() => void requestClose()}
      aria-label={t('common.close')}
      data-testid="identity-edit-close"
    >
      ×
    </button>
  </header>

  <div class="dialog-body">
    <!-- Identity details (read-only; name/email edits are via the JMAP surface) -->
    <div class="identity-row">
      <span class="identity-name">
        {identity.name ? `${identity.name}` : identity.email}
      </span>
      <span class="identity-email">{identity.email}</span>
    </div>

    <!-- Avatar + display name editors. These commit via their own
         per-form save buttons (REQ-SET-03a, REQ-SET-03b); they live
         inside the dialog body so the user only sees one Identity at
         a time per REQ-SET-IDENT-10. -->
    <IdentityAvatarForm {identity} />
    <IdentityDisplayNameForm {identity} />

    <!-- Reply-To + Bcc working copies — saved together via the Save
         button at the bottom of the dialog (REQ-SET-IDENT-11). -->
    <form
      class="addr-form"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <label class="field-label">
        <span class="label-text">{t('settings.identityEdit.replyTo')}</span>
        <input
          type="email"
          bind:value={replyToDraft}
          placeholder={identity.email}
          disabled={saving}
          autocomplete="off"
          aria-invalid={!replyToValid}
          data-testid="identity-edit-replyto"
        />
        <span class="helper">{t('settings.identityEdit.replyToHelper')}</span>
      </label>

      <label class="field-label">
        <span class="label-text">{t('settings.identityEdit.bcc')}</span>
        <input
          type="email"
          bind:value={bccDraft}
          placeholder="archive@example.com"
          disabled={saving}
          autocomplete="off"
          aria-invalid={!bccValid}
          data-testid="identity-edit-bcc"
        />
        <span class="helper">{t('settings.identityEdit.bccHelper')}</span>
      </label>

      {#if (!replyToValid && replyToDraft.trim() !== '') || (!bccValid && bccDraft.trim() !== '')}
        <p class="error" role="alert" data-testid="identity-edit-validation-error">
          {t('settings.identityEdit.invalidEmail')}
        </p>
      {/if}
      {#if saveError}
        <p class="error" role="alert" data-testid="identity-edit-save-error">
          {saveError}
        </p>
      {/if}

      <div class="actions">
        <button
          type="button"
          class="ghost"
          onclick={() => void requestClose()}
          disabled={saving}
          data-testid="identity-edit-cancel"
        >
          {t('settings.identityEdit.cancel')}
        </button>
        <button
          type="submit"
          class="primary"
          disabled={!canSave}
          data-testid="identity-edit-save"
        >
          {saving ? t('common.saving') : t('settings.identityEdit.saveAll')}
        </button>
      </div>
    </form>

    <!-- Plain-text signature editor (REQ-SET-03). Has its own internal
         save flow; kept separate so the per-field optimistic updates
         remain independent of the Reply-To/Bcc batch. -->
    <IdentitySignatureForm {identity} />

    <!-- External-submission section. REQ-SET-IDENT-13 hides it on
         unverified / pending rows (server-side gate at REQ-IDENT-60). -->
    {#if showExternal && verified}
      <div bind:this={submissionEl}>
        <IdentitySubmissionSection
          {identity}
          onchange={onSubmissionChange}
        />
      </div>
    {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 800;
    animation: fade-in var(--duration-fast-02) var(--easing-productive-enter);
  }

  .dialog {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(640px, calc(100vw - 2 * var(--spacing-05)));
    max-height: calc(100vh - 2 * var(--spacing-07));
    display: flex;
    flex-direction: column;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 801;
    overflow: hidden;
    animation: rise var(--duration-moderate-01) var(--easing-productive-enter);
  }

  .dialog-header {
    display: flex;
    align-items: center;
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    gap: var(--spacing-04);
  }

  .dialog-title {
    margin: 0;
    flex: 1;
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
  }

  .close {
    color: var(--text-helper);
    font-size: 20px;
    line-height: 1;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
    flex-shrink: 0;
  }

  .close:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .dialog-body {
    padding: var(--spacing-05);
    overflow-y: auto;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .identity-row {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .identity-name {
    font-size: var(--type-body-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .identity-email {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    font-family: var(--font-mono);
  }

  .addr-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .field-label {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label-text {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  .helper {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  input[type='email'] {
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: var(--spacing-03);
  }

  input[type='email']:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  input[type='email'][aria-invalid='true'] {
    border-color: var(--support-error);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
  }

  .primary,
  .ghost {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }

  .primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .primary:disabled,
  .ghost:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .ghost {
    color: var(--text-secondary);
  }

  .ghost:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .error {
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
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

  @media (prefers-reduced-motion: reduce) {
    .backdrop,
    .dialog {
      animation: none;
    }
  }

  @media (max-width: 640px) {
    .dialog {
      top: 0;
      left: 0;
      transform: none;
      width: 100vw;
      max-height: 100vh;
      max-height: 100dvh;
      border-radius: 0;
      border: none;
    }
  }
</style>
