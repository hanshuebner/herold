<script lang="ts">
  /**
   * Standalone identity verification dialog (REQ-SET-IDENT-20..21).
   *
   * Opened from the "Verify" button on an unverified / pending row in
   * IdentityList. Renders the identity's email and a 6-digit code-entry
   * field; submission POSTs to `/api/v1/identities/{id}/verify`
   * (REQ-IDENT-41). On success the dialog closes and a "Verified
   * <email>" toast fires; the list refreshes via `mail.loadIdentities`
   * so the row chip / radio enablement update without waiting for a
   * JMAP push.
   *
   * The Resend button fires `POST /api/v1/identities/{id}/verify-request`
   * (REQ-IDENT-36). A 429 with `Retry-After` is rendered inline as a
   * countdown ("Try again in 47 s"); other errors surface as a toast.
   *
   * Layout mirrors IdentityEditDialog (backdrop + centred dialog) so the
   * visual treatment is consistent with the rest of Settings.
   */

  import { postVerifyCode, postVerifyResend, retryAfterOf } from '../../lib/api/identity-verify';
  import { ApiError } from '../../lib/api/client';
  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { isValidCode } from '../../lib/identities/wizard-validators';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';
  import Button from '@herold/design-system/Button.svelte';
  import CodeInput from '@herold/design-system/CodeInput.svelte';

  interface Props {
    identity: Identity;
    onclose: () => void;
  }

  let { identity, onclose }: Props = $props();

  let code = $state('');
  let codeError = $state<string | null>(null);
  /** The `code` value at the moment `codeError` was last set. The
   *  inline error clears only once the user edits the code away from
   *  this snapshot — a wrong-but-syntactically-valid code keeps its
   *  error visible until it is actually changed. */
  let codeAtError = $state('');
  let verifying = $state(false);
  let resending = $state(false);
  /** Countdown remaining (seconds) until the next resend is permitted. */
  let cooldown = $state(0);
  /** Inline notice shown beneath the Resend button. */
  let resendNotice = $state<string | null>(null);
  /** Whether the dialog has emitted its onclose call (avoid double-fire). */
  let closing = $state(false);

  // Tick down the cooldown once per second.
  $effect(() => {
    if (cooldown <= 0) return;
    const handle = setInterval(() => {
      cooldown = Math.max(0, cooldown - 1);
      if (cooldown <= 0) clearInterval(handle);
    }, 1000);
    return () => clearInterval(handle);
  });

  function close(): void {
    if (closing) return;
    closing = true;
    onclose();
  }

  async function onVerifyClick(): Promise<void> {
    if (verifying) return;
    codeError = null;
    if (!isValidCode(code)) {
      codeError = t('settings.identityWizard.codeInvalid');
      codeAtError = code;
      return;
    }
    verifying = true;
    try {
      await postVerifyCode(identity.id, code);
      // Refresh the identities cache so the row chip flips.
      try {
        await mail.loadIdentities();
      } catch {
        // Best-effort refresh; the next push will reconcile.
      }
      toast.show({
        message: t('settings.identityVerify.successToast', { email: identity.email }),
        timeoutMs: 4000,
      });
      close();
    } catch (err) {
      if (err instanceof ApiError && (err.status === 400)) {
        // Wrong code: clear the boxes for re-entry and surface the error.
        // Setting code='' before codeAtError lets the $effect preserve the
        // error until the user begins typing the corrected code (re #93).
        // CodeInput's reset path focuses box 0 automatically.
        codeError = t('settings.identityWizard.codeWrong');
        code = '';
      } else {
        codeError = err instanceof Error ? err.message : String(err);
      }
      codeAtError = code;
    } finally {
      verifying = false;
    }
  }

  async function onResendClick(): Promise<void> {
    resendNotice = null;
    resending = true;
    try {
      await postVerifyResend(identity.id);
      resendNotice = t('settings.identityVerify.resendOk');
      // Soft local cooldown so the user doesn't double-click before the
      // server's rate-limit engages.
      cooldown = 60;
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        const seconds = retryAfterOf(err);
        if (seconds !== null && seconds > 0) {
          cooldown = seconds;
          resendNotice = t('settings.identityVerify.resendRateLimited', { seconds });
        } else {
          resendNotice = t('settings.identityVerify.resendRateLimitedShort');
          cooldown = 30;
        }
      } else {
        resendNotice = err instanceof Error ? err.message : String(err);
      }
    } finally {
      resending = false;
    }
  }

  // Clear the inline error once the user edits the code away from the
  // value that produced it. A wrong-but-valid code keeps its error.
  $effect(() => {
    if (codeError && code !== codeAtError) codeError = null;
  });

  function onBackdropClick(e: MouseEvent): void {
    if (e.target === e.currentTarget) close();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
    }
  }

  let resendDisabled = $derived(resending || cooldown > 0);
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
  aria-labelledby="verify-dialog-title-{identity.id}"
  tabindex="-1"
  data-testid="identity-verify-dialog"
>
  <header class="dialog-header">
    <h3 id="verify-dialog-title-{identity.id}" class="dialog-title">
      {t('settings.identityVerify.title')}
    </h3>
    <button
      type="button"
      class="close"
      onclick={close}
      aria-label={t('settings.identityVerify.close')}
      data-testid="identity-verify-close"
    >
      ×
    </button>
  </header>

  <div class="dialog-body">
    <p class="intro" data-testid="identity-verify-intro">
      {t('settings.identityVerify.intro', { email: identity.email })}
    </p>

    <form
      class="code-form"
      onsubmit={(e) => {
        e.preventDefault();
        void onVerifyClick();
      }}
    >
      <div class="field-label">
        <span class="label-text" id="verify-code-label-{identity.id}">
          {t('settings.identityVerify.codeLabel')}
        </span>
        <CodeInput
          bind:value={code}
          oncomplete={() => void onVerifyClick()}
          disabled={verifying}
          invalid={!!codeError}
          ariaLabel={t('settings.identityVerify.codeLabel')}
          testid="identity-verify-code"
          autofocus
        />
        {#if codeError}
          <span class="error" role="alert" data-testid="identity-verify-code-error">
            {codeError}
          </span>
        {/if}
      </div>

      <div class="actions">
        <Button
          variant="secondary"
          onclick={close}
          disabled={verifying}
          testid="identity-verify-cancel"
        >
          {t('settings.identityVerify.cancel')}
        </Button>
      </div>
    </form>

    <div class="resend-row">
      <Button
        variant="ghost"
        onclick={() => void onResendClick()}
        disabled={resendDisabled}
        testid="identity-verify-resend"
      >
        {resending
          ? t('settings.identityVerify.resending')
          : t('settings.identityVerify.resend')}
      </Button>
      {#if cooldown > 0}
        <span class="resend-cooldown" data-testid="identity-verify-cooldown">
          {t('settings.identityVerify.resendRateLimited', { seconds: cooldown })}
        </span>
      {:else if resendNotice}
        <span class="resend-notice" data-testid="identity-verify-resend-notice">
          {resendNotice}
        </span>
      {/if}
    </div>
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
    width: min(520px, calc(100vw - 2 * var(--spacing-05)));
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

  .intro {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
  }

  .code-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
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

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
  }

  .resend-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    border-top: 1px solid var(--border-subtle-01);
    padding-top: var(--spacing-04);
  }

  .resend-cooldown,
  .resend-notice {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .error {
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
