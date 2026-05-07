<script lang="ts">
  /**
   * Identity edit dialog.
   *
   * Shows identity details (name, email) and — when the external-submission
   * capability is present — the IdentitySubmissionSection for configuring
   * per-identity external SMTP (REQ-MAIL-SUBMIT-01).
   *
   * The dialog is rendered as a modal overlay; it does not use the
   * browser's native <dialog> element so it works uniformly across the
   * browser support floor (Chrome, Firefox, Safari, Edge, latest two
   * stable versions).
   */

  import { hasExternalSubmission } from '../../lib/auth/capabilities';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import IdentitySubmissionSection from './IdentitySubmissionSection.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';

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

  function onBackdropClick(e: MouseEvent): void {
    if (e.target === e.currentTarget) onclose();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      onclose();
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
>
  <header class="dialog-header">
    <h3 id="dialog-title-{identity.id}" class="dialog-title">
      {t('settings.identityEdit.title')}
    </h3>
    <button
      type="button"
      class="close"
      onclick={onclose}
      aria-label={t('common.close')}
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

    {#if showExternal}
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
    width: min(560px, calc(100vw - 2 * var(--spacing-05)));
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
