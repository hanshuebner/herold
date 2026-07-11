<script lang="ts">
  /**
   * TOTP step-up modal for the admin SPA (REQ-AS-20..25, re #79).
   *
   * Handles two activation paths:
   *  - Bootstrap path (auth.status === 'step_up_pending'): blocks the entire
   *    page until the operator completes TOTP elevation (REQ-AS-21).
   *  - API interceptor path (adminStepUp.visible): shown over the active
   *    admin view when a post-bootstrap request returns 403 step_up_required.
   *
   * Mounted once in AuthGate.svelte so it is always available regardless of
   * auth state.
   */
  import { untrack } from 'svelte';
  import { auth } from './auth.svelte';
  import { adminStepUp } from './step-up.svelte';
  import CodeInput from '@herold/design-system/CodeInput.svelte';
  import { t } from '../i18n/i18n.svelte';

  let isBootstrap = $derived(auth.status === 'step_up_pending');
  let visible = $derived(isBootstrap || adminStepUp.visible);

  let code = $state('');
  let lockoutSeconds = $state(0);
  let lockoutInterval: ReturnType<typeof setInterval> | null = null;

  // Reset the code when the modal opens; CodeInput handles autofocus via its
  // autofocus prop (it is inside the {#if visible} block and remounts on
  // each open).
  $effect(() => {
    if (visible && !adminStepUp.enrollRequired) {
      code = '';
    }
  });

  // Drive the lockout countdown.
  $effect(() => {
    const until = adminStepUp.lockoutUntil;
    if (!until) {
      if (lockoutInterval !== null) {
        clearInterval(lockoutInterval);
        lockoutInterval = null;
      }
      lockoutSeconds = 0;
      return;
    }
    const tick = (): void => {
      const remaining = Math.ceil((until.getTime() - Date.now()) / 1000);
      if (remaining <= 0) {
        lockoutSeconds = 0;
        if (lockoutInterval !== null) {
          clearInterval(lockoutInterval);
          lockoutInterval = null;
        }
        adminStepUp.clearLockout();
      } else {
        lockoutSeconds = remaining;
      }
    };
    tick();
    lockoutInterval = setInterval(tick, 1000);
    return () => {
      if (lockoutInterval !== null) {
        clearInterval(lockoutInterval);
        lockoutInterval = null;
      }
    };
  });

  // Capture-phase Escape: close (or on bootstrap, cancel and sign out).
  $effect(() => {
    if (!visible) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        adminStepUp.cancel(isBootstrap);
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });

  // Auto-submit when all six digits are entered (re #79, re #133).
  // Reads `code` as a reactive dep so the effect fires on each digit change.
  // The submit action is untracked to prevent re-entrant effect runs.
  //
  // `code` is cleared inside the `.then()` callback rather than before the
  // call: `submitCode` sets `submitting = true` synchronously, so clearing
  // `code` before the call fires CodeInput's reset-path `focusBox(0)` while
  // the inputs are still disabled — a disabled input cannot receive focus.
  // Clearing after the promise settles ensures `submitting = false` (set in
  // the `finally` block) before `focusBox(0)` runs (mirrors re #120).
  $effect(() => {
    const currentCode = code;
    if (currentCode.length !== 6) return;
    untrack(() => {
      if (!adminStepUp.submitting && lockoutSeconds <= 0) {
        void adminStepUp.submitCode(currentCode, isBootstrap).then(() => {
          code = '';
        });
      }
    });
  });

  function formatCountdown(secs: number): string {
    const m = Math.floor(secs / 60);
    const s = secs % 60;
    return `${String(m)}:${String(s).padStart(2, '0')}`;
  }

  function handleSubmit(e: SubmitEvent): void {
    e.preventDefault();
    if (code.length !== 6 || adminStepUp.submitting || lockoutSeconds > 0) return;
    void adminStepUp.submitCode(code, isBootstrap);
  }
</script>

{#if visible}
  <div class="backdrop" aria-hidden="true" onclick={() => adminStepUp.cancel(isBootstrap)}></div>
  <div class="modal-wrapper">
    <div
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="su-title"
      aria-describedby="su-desc"
    >
      {#if adminStepUp.enrollRequired}
        <!-- Enrollment required variant (REQ-AS-25) -->
        <h2 id="su-title" class="title">{t('stepup.enroll.title')}</h2>
        <p id="su-desc" class="body">
          {t('stepup.enroll.body')}
        </p>
        <div class="actions">
          <button
            type="button"
            class="btn btn-secondary"
            onclick={() => adminStepUp.cancel(isBootstrap)}
          >
            {t('common.cancel')}
          </button>
        </div>
      {:else}
        <!-- Code-entry variant (REQ-AS-20) -->
        <h2 id="su-title" class="title">{t('stepup.code.title')}</h2>
        <p id="su-desc" class="body">
          {t('stepup.code.body')}
        </p>

        <form onsubmit={handleSubmit} class="form" novalidate>
          <div class="field">
            <span class="label">{t('stepup.code.label')}</span>
            <CodeInput
              bind:value={code}
              disabled={adminStepUp.submitting || lockoutSeconds > 0}
              invalid={adminStepUp.error !== null}
              ariaLabel={t('stepup.code.label')}
              testid="su-code"
              autofocus
            />
            {#if adminStepUp.error}
              <p id="su-error" class="error-text" role="alert" aria-live="polite">
                {adminStepUp.error}
              </p>
            {/if}
            {#if lockoutSeconds > 0}
              <p class="lockout-text" role="status" aria-live="polite">
                {t('stepup.lockout', { countdown: formatCountdown(lockoutSeconds) })}
              </p>
            {/if}
          </div>

          <div class="actions">
            <button
              type="button"
              class="btn btn-secondary"
              onclick={() => adminStepUp.cancel(isBootstrap)}
              disabled={adminStepUp.submitting}
            >
              {t('common.cancel')}
            </button>
          </div>
        </form>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 999;
    cursor: default;
  }

  .modal-wrapper {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    pointer-events: none;
    padding: var(--spacing-05);
  }

  .modal {
    pointer-events: auto;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    max-width: 380px;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
  }

  .title {
    margin: 0;
    font-size: var(--type-heading-01-size);
    font-weight: var(--type-heading-01-weight);
    line-height: var(--type-heading-01-line);
    color: var(--text-primary);
  }

  .body {
    margin: 0;
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    font-weight: var(--type-body-compact-01-weight);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-secondary);
  }

  .error-text {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--support-error);
  }

  .lockout-text {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-secondary);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  .btn {
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    border: none;
    cursor: pointer;
    min-height: var(--touch-min);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .btn-secondary {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
    filter: brightness(0.95);
  }


</style>
