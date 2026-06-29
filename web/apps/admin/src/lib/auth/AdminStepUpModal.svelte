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
  import { auth } from './auth.svelte';
  import { adminStepUp } from './step-up.svelte';

  let isBootstrap = $derived(auth.status === 'step_up_pending');
  let visible = $derived(isBootstrap || adminStepUp.visible);

  let code = $state('');
  let inputEl = $state<HTMLInputElement | null>(null);
  let lockoutSeconds = $state(0);
  let lockoutInterval: ReturnType<typeof setInterval> | null = null;

  // Focus the code input when the modal opens.
  $effect(() => {
    if (visible && !adminStepUp.enrollRequired) {
      code = '';
      requestAnimationFrame(() => inputEl?.focus());
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

  function handleCodeInput(e: Event): void {
    const v = (e.target as HTMLInputElement).value.replace(/\D/g, '').slice(0, 6);
    code = v;
    (e.target as HTMLInputElement).value = v;
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
        <h2 id="su-title" class="title">Two-factor authentication required</h2>
        <p id="su-desc" class="body">
          Admin access requires TOTP two-factor authentication. Enrol a TOTP
          authenticator app to continue.
        </p>
        <div class="actions">
          <button
            type="button"
            class="btn btn-secondary"
            onclick={() => adminStepUp.cancel(isBootstrap)}
          >
            Cancel
          </button>
        </div>
      {:else}
        <!-- Code-entry variant (REQ-AS-20) -->
        <h2 id="su-title" class="title">Confirm your identity</h2>
        <p id="su-desc" class="body">
          Confirm your identity to continue — enter your authenticator code.
        </p>

        <form onsubmit={handleSubmit} class="form" novalidate>
          <div class="field">
            <label for="su-code" class="label">Authenticator code</label>
            <input
              bind:this={inputEl}
              id="su-code"
              type="text"
              inputmode="numeric"
              autocomplete="one-time-code"
              maxlength={6}
              placeholder="6-digit code"
              value={code}
              oninput={handleCodeInput}
              disabled={adminStepUp.submitting || lockoutSeconds > 0}
              aria-invalid={adminStepUp.error !== null}
              aria-describedby={adminStepUp.error ? 'su-error' : undefined}
              class="code-input"
            />
            {#if adminStepUp.error}
              <p id="su-error" class="error-text" role="alert" aria-live="polite">
                {adminStepUp.error}
              </p>
            {/if}
            {#if lockoutSeconds > 0}
              <p class="lockout-text" role="status" aria-live="polite">
                Too many attempts. Try again in {formatCountdown(lockoutSeconds)}.
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
              Cancel
            </button>
            <button
              type="submit"
              class="btn btn-primary"
              disabled={code.length !== 6 || adminStepUp.submitting || lockoutSeconds > 0}
            >
              {adminStepUp.submitting ? 'Confirming...' : 'Confirm'}
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

  .code-input {
    font-family: var(--font-mono);
    font-size: var(--type-heading-02-size);
    font-weight: 400;
    letter-spacing: 0.25em;
    text-align: center;
    width: 100%;
    padding: var(--spacing-03) var(--spacing-04);
    border: 1px solid var(--border-strong-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
    box-sizing: border-box;
  }

  .code-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--focus);
  }

  .code-input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .code-input[aria-invalid='true'] {
    border-color: var(--support-error);
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

  .btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .btn-primary:hover:not(:disabled) {
    background: var(--interactive);
    filter: brightness(0.92);
  }
</style>
