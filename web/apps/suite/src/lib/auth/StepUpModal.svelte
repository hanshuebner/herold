<script lang="ts">
  /**
   * TOTP step-up modal (REQ-AS-20..25, re #79).
   *
   * Mounted once in Shell.svelte. Visible when stepUp.visible is true.
   * The component drives the modal lifecycle; actual state lives in
   * the step-up singleton (step-up.svelte.ts).
   *
   * Variants:
   *  - Normal: segmented 6-digit code input + Confirm/Cancel (REQ-AS-20).
   *  - Enrollment required: "Set up TOTP now?" prompt (REQ-AS-25).
   *  - Lockout: countdown chip + disabled input (REQ-AS-23).
   */
  import { untrack } from 'svelte';
  import { stepUp } from './step-up.svelte';
  import { t } from '../i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import CodeInput from '@herold/design-system/CodeInput.svelte';

  let code = $state('');
  let lockoutSeconds = $state(0);
  let lockoutInterval: ReturnType<typeof setInterval> | null = null;

  // Reset the code when the modal opens; CodeInput handles autofocus via its
  // autofocus prop (it is inside the {#if stepUp.visible} block and remounts
  // on each open).
  $effect(() => {
    if (stepUp.visible && !stepUp.enrollRequired) {
      code = '';
    }
  });

  // Drive the lockout countdown.
  $effect(() => {
    const until = stepUp.lockoutUntil;
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
        stepUp.clearLockout();
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

  // Capture-phase Escape handler so the modal closes before the global
  // keyboard engine handles the event.
  $effect(() => {
    if (!stepUp.visible) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        stepUp.cancel();
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });

  // Auto-submit when all six digits are entered (re #79, re #120).
  // Reads `code` as a reactive dep so the effect fires on each digit change.
  // The submit action is untracked to prevent re-entrant effect runs.
  //
  // `code` is cleared inside the `.then()` callback rather than before the
  // call: `submitCode` sets `submitting = true` synchronously, so clearing
  // `code` before the call fires CodeInput's reset-path `focusBox(0)` while
  // the inputs are still disabled -- and a disabled input cannot receive
  // focus. Clearing after the promise settles ensures `submitting = false`
  // (set in the `finally` block) before `focusBox(0)` runs (re #120).
  $effect(() => {
    const currentCode = code;
    if (currentCode.length !== 6) return;
    untrack(() => {
      if (!stepUp.submitting && lockoutSeconds <= 0) {
        void stepUp.submitCode(currentCode).then(() => {
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
    if (code.length !== 6 || stepUp.submitting || lockoutSeconds > 0) return;
    void stepUp.submitCode(code);
  }
</script>

{#if stepUp.visible}
  <div class="backdrop" aria-hidden="true" onclick={() => stepUp.cancel()}></div>
  <div class="modal-wrapper">
    <div
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="stepup-title"
      aria-describedby="stepup-desc"
    >
      {#if stepUp.enrollRequired}
        <!-- Enrollment required variant (REQ-AS-25) -->
        <h2 id="stepup-title" class="title">{t('stepup.title')}</h2>
        <p id="stepup-desc" class="body">{t('stepup.enroll.prompt')}</p>
        <div class="actions">
          <Button variant="secondary" onclick={() => stepUp.cancel()}>
            {t('stepup.cancel')}
          </Button>
          <Button
            variant="primary"
            onclick={() => {
              stepUp.cancel();
              window.location.hash = '#/settings/security';
            }}
          >
            {t('stepup.enroll.setup')}
          </Button>
        </div>
      {:else}
        <!-- Code-entry variant (REQ-AS-20) -->
        <h2 id="stepup-title" class="title">{t('stepup.title')}</h2>
        <p id="stepup-desc" class="body">{t('stepup.prompt')}</p>

        <form onsubmit={handleSubmit} class="form" novalidate>
          <div class="field">
            <span class="label">{t('stepup.code.label')}</span>
            <CodeInput
              bind:value={code}
              disabled={stepUp.submitting || lockoutSeconds > 0}
              invalid={stepUp.error !== null}
              ariaLabel={t('stepup.code.label')}
              testid="stepup-code"
              autofocus
            />
            {#if stepUp.error}
              <p id="stepup-error" class="error-text" role="alert" aria-live="polite">
                {stepUp.error}
              </p>
            {/if}
            {#if lockoutSeconds > 0}
              <p class="lockout-text" role="status" aria-live="polite">
                {t('stepup.error.lockout', { remaining: formatCountdown(lockoutSeconds) })}
              </p>
            {/if}
          </div>

          <div class="actions">
            <Button
              variant="secondary"
              type="button"
              onclick={() => stepUp.cancel()}
              disabled={stepUp.submitting}
            >
              {t('stepup.cancel')}
            </Button>
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
</style>
