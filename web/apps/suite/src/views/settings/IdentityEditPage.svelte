<script lang="ts">
  /**
   * Per-identity editor (REQ-SET-IDENT-10..13).
   *
   * Item 6: this is a dedicated settings sub-page rendered at the hash
   * route `/#/settings/identities/:id`, NOT a modal. It renders inside
   * the settings content column with a back affordance to the identity
   * list. There is no backdrop and no modal machinery.
   *
   * Item 7: name / display name / avatar / signature / reply-to / bcc
   * autosave on blur — there are no per-section Save buttons. A single
   * shared `AutosaveController` drives the subtle "Saving…/Saved"
   * indicator in the page header. The external SMTP submission section
   * is the documented exception: it keeps an explicit Save button
   * because it runs a server-side connectivity probe and takes a
   * password.
   *
   * Hosts:
   *   - Avatar editor (REQ-SET-03b) via IdentityAvatarForm.
   *   - Display-name field (REQ-SET-03a) via IdentityDisplayNameForm.
   *   - Reply-To / Bcc fields edited locally and committed via
   *     `Identity/set update` on blur (REQ-SET-IDENT-11).
   *   - Plain-text signature editor (REQ-SET-03) via
   *     IdentitySignatureForm.
   *   - External-submission section (REQ-MAIL-SUBMIT-01..06), only when
   *     the capability is advertised AND the identity is verified
   *     (REQ-SET-IDENT-13).
   */

  import { onDestroy } from 'svelte';
  import { hasExternalSubmission } from '../../lib/auth/capabilities';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import { mail } from '../../lib/mail/store.svelte';
  import { jmap, strict } from '../../lib/jmap/client';
  import { Capability, type Invocation } from '../../lib/jmap/types';
  import { identityStatus } from '../../lib/identities/identity-status';
  import IdentitySubmissionSection from './IdentitySubmissionSection.svelte';
  import IdentityAvatarForm from './IdentityAvatarForm.svelte';
  import IdentityDisplayNameForm from './IdentityDisplayNameForm.svelte';
  import IdentitySignatureForm from './IdentitySignatureForm.svelte';
  import SaveStatus from './SaveStatus.svelte';
  import { AutosaveController } from './autosave.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import ChevronLeftIcon from '../../lib/icons/ChevronLeftIcon.svelte';
  import type { Address, Identity } from '../../lib/mail/types';

  interface Props {
    identity: Identity;
    /** Navigate back to the identity list. */
    onback: () => void;
    /**
     * When true, scroll the page to the submission section after mount.
     * Used by the compose-failure "Re-authenticate" deep link
     * (REQ-MAIL-SUBMIT-04).
     */
    scrollToSubmission?: boolean;
  }

  let { identity, onback, scrollToSubmission = false }: Props = $props();

  let showExternal = $derived(hasExternalSubmission());
  let verified = $derived(identityStatus(identity) === 'verified');

  // One shared autosave indicator for every autosaving field on the page.
  const autosave = new AutosaveController();
  onDestroy(() => autosave.dispose());

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

  // ── Reply-To / Bcc — autosave on blur (REQ-SET-IDENT-11) ────────────

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

  // Mirror the prop into the working copy. Self-referential reads are
  // avoided so a keystroke does not re-run this effect (Svelte 5 footgun,
  // see web/CLAUDE.md "Patterns to avoid").
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

  /** Persist whichever of replyTo / bcc changed; called on field blur. */
  async function commitAddresses(): Promise<void> {
    if (replyToDraft === replyToSaved && bccDraft === bccSaved) return;
    if (!replyToValid || !bccValid) return; // wait for a valid value
    const accountId = mail.mailAccountId;
    if (!accountId) return;

    const update: Record<string, unknown> = {};
    const nextReplyTo = replyToDraft;
    const nextBcc = bccDraft;
    if (nextReplyTo !== replyToSaved) {
      update.replyTo = inputToAddresses(nextReplyTo);
    }
    if (nextBcc !== bccSaved) {
      update.bcc = inputToAddresses(nextBcc);
    }

    const ok = await autosave.run(async () => {
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
        if ('replyTo' in update) merged.replyTo = inputToAddresses(nextReplyTo);
        if ('bcc' in update) merged.bcc = inputToAddresses(nextBcc);
        next.set(identity.id, merged);
      }
      mail.identities = next;
    });

    if (ok) {
      replyToSaved = nextReplyTo;
      bccSaved = nextBcc;
    }
  }

  function invocationArgs<T>(inv: Invocation | undefined): T {
    if (!inv) throw new Error('Expected method invocation, got undefined');
    return inv[1] as T;
  }
</script>

<div class="identity-editor" data-testid="identity-edit-page">
  <header class="editor-header">
    <Button
      variant="ghost"
      compact
      testid="identity-edit-back"
      onclick={onback}
    >
      {#snippet icon()}<ChevronLeftIcon size={16} />{/snippet}
      {t('settings.identityEdit.back')}
    </Button>
    <SaveStatus state={autosave.state} errorMessage={autosave.errorMessage} />
  </header>

  <h3 class="editor-title">{t('settings.identityEdit.title')}</h3>

  <div class="identity-row">
    <span class="identity-name">
      {identity.name ? `${identity.name}` : identity.email}
    </span>
    <span class="identity-email">{identity.email}</span>
  </div>

  <!-- Avatar + display name editors autosave on change / blur. -->
  <IdentityAvatarForm {identity} {autosave} />
  <IdentityDisplayNameForm {identity} {autosave} />

  <!-- Reply-To + Bcc — autosave on blur (REQ-SET-IDENT-11). -->
  <div class="addr-form">
    <h4 class="group-title">{t('settings.identityEdit.replyToHeading')}</h4>
    <label class="field-label">
      <span class="label-text">{t('settings.identityEdit.replyTo')}</span>
      <input
        type="email"
        bind:value={replyToDraft}
        placeholder={identity.email}
        autocomplete="off"
        aria-invalid={!replyToValid}
        onblur={() => void commitAddresses()}
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
        autocomplete="off"
        aria-invalid={!bccValid}
        onblur={() => void commitAddresses()}
        data-testid="identity-edit-bcc"
      />
      <span class="helper">{t('settings.identityEdit.bccHelper')}</span>
    </label>

    {#if (!replyToValid && replyToDraft.trim() !== '') || (!bccValid && bccDraft.trim() !== '')}
      <p class="error" role="alert" data-testid="identity-edit-validation-error">
        {t('settings.identityEdit.invalidEmail')}
      </p>
    {/if}
  </div>

  <!-- Plain-text signature editor (REQ-SET-03) — autosaves on blur. -->
  <IdentitySignatureForm {identity} {autosave} />

  <!-- External-submission section. REQ-SET-IDENT-13 hides it on
       unverified / pending rows (server-side gate at REQ-IDENT-60).
       This section keeps its own explicit Save button — autosave is
       wrong for a connectivity probe that takes a password. -->
  {#if showExternal && verified}
    <div bind:this={submissionEl}>
      <IdentitySubmissionSection
        {identity}
        onchange={onSubmissionChange}
      />
    </div>
  {/if}
</div>

<style>
  .identity-editor {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .editor-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  .editor-title {
    margin: 0;
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
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

  .group-title {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
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

  .error {
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: color-mix(in srgb, var(--support-error) 12%, transparent);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }
</style>
