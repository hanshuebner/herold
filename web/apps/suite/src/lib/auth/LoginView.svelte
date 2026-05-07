<script lang="ts">
  import { auth } from './auth.svelte';
  import { t } from '../i18n/i18n.svelte';

  let email = $state('');
  let password = $state('');
  let totpCode = $state('');
  let submitting = $state(false);
  let errorMessage = $state<string | null>(null);

  async function handleSubmit(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (submitting) return;
    submitting = true;
    errorMessage = null;

    try {
      await auth.login({
        email,
        password,
        totpCode: auth.needsStepUp && totpCode ? totpCode : undefined,
      });
      // On success auth.bootstrap() ran inside auth.login(); status is now 'ready'.
    } catch (err) {
      // auth.login() sets auth.errorMessage; mirror it locally for display.
      errorMessage = auth.errorMessage ?? (err instanceof Error ? err.message : t('login.signInFailed'));
    } finally {
      submitting = false;
    }
  }
</script>

<div class="login-page">
  <div class="login-card">
    <h1 class="wordmark">Herold</h1>

    <form class="form" onsubmit={handleSubmit} novalidate>
      <div class="field">
        <label for="email" class="label">{t('login.email')}</label>
        <input
          id="email"
          type="email"
          name="email"
          class="input"
          autocomplete="username"
          required
          bind:value={email}
          disabled={submitting}
        />
      </div>

      <div class="field">
        <label for="password" class="label">{t('login.password')}</label>
        <input
          id="password"
          type="password"
          name="password"
          class="input"
          autocomplete="current-password"
          required
          bind:value={password}
          disabled={submitting}
        />
      </div>

      {#if auth.needsStepUp}
        <div class="field">
          <label for="totp-code" class="label">{t('login.totpCode')}</label>
          <input
            id="totp-code"
            type="text"
            name="totp_code"
            class="input"
            inputmode="numeric"
            autocomplete="one-time-code"
            pattern="[0-9]*"
            placeholder={t('login.totpPlaceholder')}
            bind:value={totpCode}
            disabled={submitting}
          />
        </div>
      {/if}

      {#if errorMessage}
        <p class="error" role="alert">{errorMessage}</p>
      {/if}

      <button type="submit" class="submit-btn" disabled={submitting}>
        {submitting ? t('login.signingIn') : t('login.signIn')}
      </button>
    </form>
  </div>
</div>

<style>
  .login-page {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    min-height: 100dvh;
    background: var(--background);
    padding: var(--spacing-06);
  }

  .login-card {
    width: 100%;
    max-width: 400px;
  }

  .wordmark {
    font-family: var(--font-sans);
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: 600;
    letter-spacing: -0.02em;
    color: var(--text-primary);
    margin: 0 0 var(--spacing-07);
    text-align: center;
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-secondary);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    min-height: var(--touch-min);
    transition:
      border-color var(--duration-fast-02) var(--easing-productive-enter),
      background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }
  .input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .submit-btn {
    width: 100%;
    padding: var(--spacing-04) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    transition:
      filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .submit-btn:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .submit-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
