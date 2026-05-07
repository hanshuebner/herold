<script lang="ts">
  /**
   * Security settings panel — Phase 4 (REQ-ADM-203).
   *
   * Three subsections:
   *   1. Change password  -- PUT /api/v1/principals/{pid}/password
   *   2. TOTP             -- POST/DELETE /api/v1/principals/{pid}/totp/...
   *
   * The principal object (which carries totp_enabled) is fetched once on
   * mount via GET /api/v1/principals/{pid} so the TOTP section renders
   * correctly without depending on the JMAP session. Renders eagerly with
   * a loading spinner while the fetch is in flight (per Phase 4 requirement).
   */
  import QRCode from 'qrcode-svg';
  import { auth } from '../../lib/auth/auth.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { get, put, post, del, ApiError, UnauthenticatedError } from '../../lib/api/client';
  import { t } from '../../lib/i18n/i18n.svelte';

  // --- Principal fetch ---

  interface PrincipalDTO {
    id: string;
    canonical_email: string;
    display_name: string;
    totp_enabled: boolean;
    flags: string[];
  }

  let principal = $state<PrincipalDTO | null>(null);
  let principalLoading = $state(true);
  let principalError = $state<string | null>(null);

  $effect(() => {
    const pid = auth.principalId;
    if (!pid) return;
    principalLoading = true;
    principalError = null;
    get<PrincipalDTO>(`/api/v1/principals/${pid}`)
      .then((p) => {
        principal = p;
        principalLoading = false;
      })
      .catch((err) => {
        principalError = errorMessage(err);
        principalLoading = false;
      });
  });

  // --- Change password ---

  let pwCurrent = $state('');
  let pwNew = $state('');
  let pwConfirm = $state('');
  let pwSaving = $state(false);
  let pwError = $state<string | null>(null);

  async function changePassword(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    pwError = null;

    if (pwNew !== pwConfirm) {
      pwError = t('settings.security.passwordMismatch');
      return;
    }
    if (pwNew.length < 12) {
      pwError = t('settings.security.passwordTooShort');
      return;
    }

    const pid = auth.principalId;
    if (!pid) {
      pwError = t('settings.security.sessionNotReady');
      return;
    }

    pwSaving = true;
    try {
      await put<void>(`/api/v1/principals/${pid}/password`, {
        current_password: pwCurrent,
        new_password: pwNew,
      });
      toast.show({ message: t('settings.security.passwordChanged'), timeoutMs: 4000 });
      pwCurrent = '';
      pwNew = '';
      pwConfirm = '';
    } catch (err) {
      if (err instanceof UnauthenticatedError) {
        pwError = t('settings.security.currentPasswordWrong');
      } else {
        pwError = errorMessage(err);
      }
    } finally {
      pwSaving = false;
    }
  }

  // --- TOTP ---

  let totpLoading = $state(false);
  let totpProvisioningUri = $state<string | null>(null);
  let totpSecret = $state<string | null>(null);
  let totpQrSvg = $state<string | null>(null);
  let totpCode = $state('');
  let totpConfirmError = $state<string | null>(null);
  let totpDisablePassword = $state('');
  let totpDisableError = $state<string | null>(null);

  // Derived from the fetched principal so it stays in sync after
  // enrol/disable without re-fetching the principal.
  let totpEnabled = $derived(principal?.totp_enabled ?? false);

  async function startEnroll(): Promise<void> {
    const pid = auth.principalId;
    if (!pid) return;
    totpLoading = true;
    totpConfirmError = null;
    try {
      const result = await post<{ secret: string; provisioning_uri: string }>(
        `/api/v1/principals/${pid}/totp/enroll`,
      );
      totpSecret = result.secret;
      totpProvisioningUri = result.provisioning_uri;
      try {
        const qr = new QRCode({
          content: result.provisioning_uri,
          width: 200,
          height: 200,
          color: '#000000',
          background: '#ffffff',
          ecl: 'M',
          padding: 2,
        });
        totpQrSvg = qr.svg();
      } catch {
        // QR generation failure is non-fatal; the provisioning URI is still shown.
        totpQrSvg = null;
      }
    } catch (err) {
      totpConfirmError = errorMessage(err);
    } finally {
      totpLoading = false;
    }
  }

  async function confirmEnroll(): Promise<void> {
    if (!totpCode.trim()) return;
    const pid = auth.principalId;
    if (!pid) return;
    totpLoading = true;
    totpConfirmError = null;
    try {
      await post<void>(`/api/v1/principals/${pid}/totp/confirm`, {
        code: totpCode.trim(),
      });
      // Update local state without re-fetching.
      if (principal) principal = { ...principal, totp_enabled: true };
      totpProvisioningUri = null;
      totpSecret = null;
      totpQrSvg = null;
      totpCode = '';
      toast.show({ message: t('settings.security.twoFactorEnabledToast'), timeoutMs: 4000 });
    } catch (err) {
      totpConfirmError = errorMessage(err);
    } finally {
      totpLoading = false;
    }
  }

  async function disableTOTP(): Promise<void> {
    if (!totpDisablePassword) return;
    const ok = await confirm.ask({
      title: t('settings.security.disable2faTitle'),
      message: t('settings.security.disable2faMessage'),
      confirmLabel: t('settings.security.disable2faConfirm'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    const pid = auth.principalId;
    if (!pid) return;
    totpLoading = true;
    totpDisableError = null;
    try {
      await del<void>(`/api/v1/principals/${pid}/totp`, {
        current_password: totpDisablePassword,
      });
      if (principal) principal = { ...principal, totp_enabled: false };
      totpDisablePassword = '';
      toast.show({ message: t('settings.security.twoFactorDisabledToast'), timeoutMs: 4000 });
    } catch (err) {
      totpDisableError = errorMessage(err);
    } finally {
      totpLoading = false;
    }
  }

  // --- Helpers ---

  function errorMessage(err: unknown): string {
    if (err instanceof ApiError) return err.message;
    if (err instanceof Error) return err.message;
    return String(err);
  }
</script>

{#if !auth.principalId}
  <p class="muted">{t('settings.security.loadingSession')}</p>
{:else if principalLoading}
  <div class="spinner" role="status" aria-label={t('settings.security.loadingAria')}></div>
{:else if principalError}
  <p class="form-error" role="alert">{principalError}</p>
{:else}

  <!-- Introductory copy -->
  <div class="intro">
    <p>
      {t('settings.security.intro')}
    </p>
    <p class="intro-hint">
      {t('settings.security.introHint')}
    </p>
  </div>

  <!-- Change password -->
  <h3>{t('settings.security.changePassword')}</h3>
  <form class="sec-form" onsubmit={changePassword} novalidate>
    <div class="field">
      <label for="sec-pw-current" class="label">{t('settings.security.currentPassword')}</label>
      <input
        id="sec-pw-current"
        type="password"
        class="input"
        autocomplete="current-password"
        bind:value={pwCurrent}
        disabled={pwSaving}
        required
      />
    </div>

    <div class="field">
      <label for="sec-pw-new" class="label">{t('settings.security.newPassword')}</label>
      <input
        id="sec-pw-new"
        type="password"
        class="input"
        autocomplete="new-password"
        bind:value={pwNew}
        disabled={pwSaving}
        required
      />
    </div>

    <div class="field">
      <label for="sec-pw-confirm" class="label">{t('settings.security.confirmNewPassword')}</label>
      <input
        id="sec-pw-confirm"
        type="password"
        class="input"
        autocomplete="new-password"
        bind:value={pwConfirm}
        disabled={pwSaving}
        required
      />
    </div>

    {#if pwError}
      <p class="form-error" role="alert">{pwError}</p>
    {/if}

    <div class="form-actions">
      <button type="submit" class="btn-primary" disabled={pwSaving || !pwCurrent || !pwNew || !pwConfirm}>
        {pwSaving ? t('common.saving') : t('settings.security.changePwSubmit')}
      </button>
    </div>
  </form>

  <!-- Two-factor authentication -->
  <h3>{t('settings.security.twoFactorHeading')}</h3>

  {#if totpEnabled}
    <!-- Enrolled state -->
    <div class="totp-status totp-on">
      {t('settings.security.twoFactorEnabled')}
    </div>

    <div class="field totp-disable-form">
      <label for="sec-totp-disable-pw" class="label">{t('settings.security.disable2faLabel')}</label>
      <div class="input-row">
        <input
          id="sec-totp-disable-pw"
          type="password"
          class="input"
          autocomplete="current-password"
          bind:value={totpDisablePassword}
          disabled={totpLoading}
        />
        <button
          type="button"
          class="btn-danger"
          onclick={disableTOTP}
          disabled={totpLoading || !totpDisablePassword}
        >
          {totpLoading ? t('settings.security.disabling') : t('settings.security.disable2fa')}
        </button>
      </div>
      {#if totpDisableError}
        <p class="form-error" role="alert">{totpDisableError}</p>
      {/if}
    </div>
  {:else}
    <!-- Not enrolled state -->
    <div class="totp-status">
      {t('settings.security.twoFactorDisabled')}
    </div>

    {#if !totpProvisioningUri}
      <div class="form-actions">
        <button
          type="button"
          class="btn-primary"
          onclick={startEnroll}
          disabled={totpLoading}
        >
          {totpLoading ? t('settings.security.starting') : t('settings.security.enable2fa')}
        </button>
      </div>
      {#if totpConfirmError}
        <p class="form-error" role="alert">{totpConfirmError}</p>
      {/if}
    {:else}
      <!-- Enrolment flow: QR + secret + confirm code -->
      <div class="totp-enroll">
        <p class="hint">
          {t('settings.security.scanHint')}
        </p>

        {#if totpQrSvg}
          <div class="totp-qr" aria-label={t('settings.security.qrAriaLabel')}>
            <!-- eslint-disable-next-line svelte/no-at-html-tags -->
            {@html totpQrSvg}
          </div>
        {/if}

        {#if totpSecret}
          <div class="field">
            <span class="label">{t('settings.security.manualEntryKey')}</span>
            <input
              type="text"
              class="input mono"
              readonly
              value={totpSecret}
              onclick={(e) => (e.currentTarget as HTMLInputElement).select()}
              aria-label={t('settings.security.totpSecretAria')}
            />
          </div>
        {/if}

        <div class="field">
          <label for="sec-totp-uri" class="label">{t('settings.security.provisioningUri')}</label>
          <input
            id="sec-totp-uri"
            type="text"
            class="input mono"
            readonly
            value={totpProvisioningUri}
            onclick={(e) => (e.currentTarget as HTMLInputElement).select()}
            aria-label={t('settings.security.provisioningUriAria')}
          />
        </div>

        <div class="totp-confirm-row">
          <input
            type="text"
            class="input input-narrow"
            inputmode="numeric"
            autocomplete="one-time-code"
            pattern="[0-9]*"
            placeholder={t('settings.security.codePlaceholder')}
            bind:value={totpCode}
            disabled={totpLoading}
            aria-label={t('settings.security.codeAria')}
          />
          <button
            type="button"
            class="btn-primary"
            onclick={confirmEnroll}
            disabled={totpLoading || !totpCode}
          >
            {totpLoading ? t('settings.security.confirming') : t('settings.security.confirmEnroll')}
          </button>
        </div>

        {#if totpConfirmError}
          <p class="form-error" role="alert">{totpConfirmError}</p>
        {/if}
      </div>
    {/if}
  {/if}

{/if}

<style>
  .sec-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 480px;
    margin-bottom: var(--spacing-06);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
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
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
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
  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    letter-spacing: 0.03em;
  }
  .input-narrow {
    width: 140px;
    min-width: 0;
  }

  .form-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .form-actions {
    display: flex;
    justify-content: flex-start;
    gap: var(--spacing-04);
    margin-top: var(--spacing-02);
  }

  .totp-status {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    margin-bottom: var(--spacing-04);
  }
  .totp-on {
    border-color: var(--support-success);
    color: var(--support-success);
    background: color-mix(in srgb, var(--support-success) 8%, transparent);
  }

  .totp-disable-form {
    max-width: 480px;
    margin-bottom: var(--spacing-04);
  }

  .totp-enroll {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 480px;
  }

  .totp-qr {
    display: inline-block;
    background: #fff;
    padding: var(--spacing-03);
    border-radius: var(--radius-md);
    width: 200px;
    height: 200px;
    overflow: hidden;
  }
  :global(.totp-qr svg) {
    display: block;
    width: 100%;
    height: 100%;
  }

  .totp-confirm-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .input-row {
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .intro {
    max-width: 720px;
    margin-bottom: var(--spacing-05);
    padding: var(--spacing-04) var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }
  .intro p {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
  }
  .intro p + p {
    margin-top: var(--spacing-03);
  }
  .intro .intro-hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
  }

  .muted {
    color: var(--text-helper);
    font-style: italic;
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  /* Shared buttons */
  .btn-primary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--support-error);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-danger:hover:not(:disabled) {
    filter: brightness(0.9);
  }
  .btn-danger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
