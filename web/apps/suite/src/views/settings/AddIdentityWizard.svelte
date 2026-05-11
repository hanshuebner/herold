<script lang="ts">
  /**
   * Three-step add-identity wizard (REQ-SET-IDENT-30..33).
   *
   *   Step 1 — Address (email + optional display name).
   *   Step 2 — Verification pending (code input + resend, same code-entry
   *            semantics as the standalone IdentityVerifyDialog).
   *   Step 3 — Configure external SMTP submission (only when the new
   *            identity's domain is not hosted on this herold).
   *
   * The wizard is a controlled dialog: it stays open across step
   * transitions and the parent SettingsView owns the mount.
   *
   * Cancel semantics (REQ-SET-IDENT-33):
   *   - Cancelling Step 1 closes the wizard cleanly (no `Identity/set
   *     { create }` was issued yet).
   *   - Cancelling Step 2 (after create has committed) leaves the
   *     unverified row in the list. The cancel button surfaces an
   *     explanatory notice "Closing will keep this identity in
   *     'Verification pending' state".
   *   - Cancelling Step 3 closes the wizard; the identity is verified
   *     but no external SMTP is configured (the user can finish later
   *     from the identity edit dialog).
   */

  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { hasExternalSubmission } from '../../lib/auth/capabilities';
  import { ApiError } from '../../lib/api/client';
  import {
    postVerifyCode,
    postVerifyResend,
    retryAfterOf,
  } from '../../lib/api/identity-verify';
  import { putSubmission } from '../../lib/api/identity-submission';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import {
    isValidEmail,
    isValidCode,
    isValidPort,
    domainOf,
    isHostedDomain,
  } from '../../lib/identities/wizard-validators';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';
  import type {
    SubmitSecurity,
    SubmissionPutBody,
  } from '../../lib/api/identity-submission';

  interface Props {
    /** Lower-cased set of hosted (local) email domains; used to decide
     *  whether Step 3 (external SMTP) shows. Pass an empty set for a
     *  legacy server that does not surface the local-domains list. */
    hostedDomains: ReadonlySet<string>;
    /** Called when the wizard should close. */
    onclose: () => void;
  }

  let { hostedDomains, onclose }: Props = $props();

  type Step = 1 | 2 | 3;
  let step = $state<Step>(1);

  // Step 1 — Address.
  let email = $state('');
  let displayName = $state('');
  let createError = $state<string | null>(null);
  let creating = $state(false);

  // Step 2 — Verification.
  let createdIdentity = $state<Identity | null>(null);
  let code = $state('');
  let codeError = $state<string | null>(null);
  let verifying = $state(false);
  let resending = $state(false);
  let cooldown = $state(0);
  let resendNotice = $state<string | null>(null);

  // Step 3 — External SMTP submission.
  let smtpHost = $state('');
  let smtpPort = $state(587);
  let smtpUser = $state('');
  let smtpPassword = $state('');
  let smtpSecurity = $state<SubmitSecurity>('starttls');
  let smtpError = $state<string | null>(null);
  let smtpSaving = $state(false);

  let closing = $state(false);
  let showCancelNotice = $state(false);

  // Tick down the resend cooldown once per second.
  $effect(() => {
    if (cooldown <= 0) return;
    const handle = setInterval(() => {
      cooldown = Math.max(0, cooldown - 1);
      if (cooldown <= 0) clearInterval(handle);
    }, 1000);
    return () => clearInterval(handle);
  });

  // Whether the new identity's domain is hosted on this herold. Hosted
  // identities terminate at Step 2 success; external identities offer
  // Step 3.
  let domainIsHosted = $derived(
    createdIdentity ? isHostedDomain(createdIdentity.email, hostedDomains) : true,
  );

  let showStep3 = $derived(!domainIsHosted && hasExternalSubmission());

  function close(): void {
    if (closing) return;
    closing = true;
    onclose();
  }

  function onBackdropClick(e: MouseEvent): void {
    if (e.target === e.currentTarget) requestCancel();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      requestCancel();
    }
  }

  /**
   * Cancel button handler. On Step 2 (after create committed), the
   * first click surfaces an inline notice; the second click closes.
   * REQ-SET-IDENT-33: the row stays in the list in verification-
   * pending state.
   */
  function requestCancel(): void {
    if (step === 2 && createdIdentity && !showCancelNotice) {
      showCancelNotice = true;
      return;
    }
    close();
  }

  /** Email validity for Step 1's Next button. */
  let emailValid = $derived(isValidEmail(email));
  let canCreate = $derived(emailValid && !creating);

  async function onStep1Submit(): Promise<void> {
    createError = null;
    if (!canCreate) return;
    creating = true;
    try {
      const created = await mail.createIdentity(email, displayName);
      createdIdentity = created;
      // The store mirrored the row already; show toast and advance.
      toast.show({
        message: t('settings.identityWizard.createdToast', { email: created.email }),
        timeoutMs: 4000,
      });
      step = 2;
      code = '';
      codeError = null;
      resendNotice = null;
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      // Detect the forbiddenFrom shape — the server's setError carries
      // "domain not permitted" / similar. We render the explicit
      // domain-blocked message when the message looks like it.
      const dom = domainOf(email);
      if (dom && /domain|forbidden/i.test(msg)) {
        createError = t('settings.identityWizard.domainBlocked', { domain: dom });
      } else {
        createError = msg;
      }
    } finally {
      creating = false;
    }
  }

  function onCodeInput(value: string): void {
    const stripped = value.replace(/\D/g, '').slice(0, 6);
    code = stripped;
    if (codeError && isValidCode(stripped)) codeError = null;
  }

  let verifyDisabled = $derived(verifying || !isValidCode(code));
  let resendDisabled = $derived(resending || cooldown > 0);

  async function onVerify(): Promise<void> {
    if (!createdIdentity) return;
    codeError = null;
    if (!isValidCode(code)) {
      codeError = t('settings.identityWizard.codeInvalid');
      return;
    }
    verifying = true;
    try {
      await postVerifyCode(createdIdentity.id, code);
      try {
        await mail.loadIdentities();
      } catch {
        // Best-effort refresh.
      }
      toast.show({
        message: t('settings.identityWizard.successToast', { email: createdIdentity.email }),
        timeoutMs: 4000,
      });
      // Re-read the freshly-verified row from the cache so domain
      // detection (hosted vs external) operates on the canonical
      // server-side email.
      const refreshed = mail.identities.get(createdIdentity.id);
      if (refreshed) createdIdentity = refreshed;
      if (showStep3) {
        step = 3;
      } else {
        close();
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 400) {
        codeError = t('settings.identityWizard.codeWrong');
      } else {
        codeError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      verifying = false;
    }
  }

  async function onResend(): Promise<void> {
    if (!createdIdentity) return;
    resendNotice = null;
    resending = true;
    try {
      await postVerifyResend(createdIdentity.id);
      resendNotice = t('settings.identityWizard.resendOk');
      cooldown = 60;
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        const seconds = retryAfterOf(err);
        if (seconds !== null && seconds > 0) {
          cooldown = seconds;
          resendNotice = t('settings.identityWizard.resendRateLimited', { seconds });
        } else {
          resendNotice = t('settings.identityWizard.resendRateLimitedShort');
          cooldown = 30;
        }
      } else {
        resendNotice = err instanceof Error ? err.message : String(err);
      }
    } finally {
      resending = false;
    }
  }

  // Step 3 validation.
  let smtpHostValid = $derived(smtpHost.trim() !== '');
  let smtpUserValid = $derived(smtpUser.trim() !== '');
  let smtpPasswordValid = $derived(smtpPassword !== '');
  let smtpPortValid = $derived(isValidPort(smtpPort));
  let smtpSaveDisabled = $derived(
    smtpSaving ||
      !smtpHostValid ||
      !smtpUserValid ||
      !smtpPasswordValid ||
      !smtpPortValid,
  );

  async function onStep3Save(): Promise<void> {
    if (!createdIdentity) return;
    if (smtpSaveDisabled) return;
    smtpError = null;
    smtpSaving = true;
    try {
      const body: SubmissionPutBody = {
        auth_method: 'password',
        host: smtpHost.trim(),
        port: smtpPort,
        security: smtpSecurity,
        password: smtpPassword,
      };
      await putSubmission(createdIdentity.id, body);
      // Invalidate the submission store entry so the list / edit
      // dialog re-fetches the fresh config.
      void submissionStore.forIdentity(createdIdentity.id).refresh();
      close();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      smtpError = t('settings.identityWizard.smtpSaveError', { message: msg });
    } finally {
      smtpSaving = false;
    }
  }

  function onStep3Skip(): void {
    close();
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
  aria-labelledby="wizard-title"
  tabindex="-1"
  data-testid="identity-wizard"
  data-wizard-step={step}
>
  <header class="dialog-header">
    <h3 id="wizard-title" class="dialog-title">
      {t('settings.identityWizard.title')}
    </h3>
    <span class="step-indicator" data-testid="identity-wizard-step-indicator">
      {step}/{showStep3 ? 3 : 2}
    </span>
    <button
      type="button"
      class="close"
      onclick={requestCancel}
      aria-label={t('settings.identityWizard.close')}
      data-testid="identity-wizard-close"
    >
      ×
    </button>
  </header>

  <div class="dialog-body">
    {#if step === 1}
      <section data-testid="identity-wizard-step-1">
        <h4 class="step-title">{t('settings.identityWizard.step1Title')}</h4>
        <p class="step-intro">{t('settings.identityWizard.step1Intro')}</p>

        <form
          class="form"
          onsubmit={(e) => {
            e.preventDefault();
            void onStep1Submit();
          }}
        >
          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.emailLabel')}</span>
            <input
              type="email"
              autocomplete="email"
              bind:value={email}
              disabled={creating}
              aria-invalid={email.trim() !== '' && !emailValid}
              data-testid="identity-wizard-email"
            />
            <span class="helper">{t('settings.identityWizard.emailHelper')}</span>
            {#if email.trim() !== '' && !emailValid}
              <span class="error" role="alert" data-testid="identity-wizard-email-error">
                {t('settings.identityWizard.emailInvalid')}
              </span>
            {/if}
          </label>

          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.displayNameLabel')}</span>
            <input
              type="text"
              autocomplete="name"
              bind:value={displayName}
              disabled={creating}
              data-testid="identity-wizard-display-name"
            />
            <span class="helper">{t('settings.identityWizard.displayNameHelper')}</span>
          </label>

          {#if createError}
            <p class="error-block" role="alert" data-testid="identity-wizard-create-error">
              {createError}
            </p>
          {/if}

          <div class="actions">
            <button
              type="button"
              class="ghost"
              onclick={close}
              disabled={creating}
              data-testid="identity-wizard-cancel-step1"
            >
              {t('settings.identityWizard.cancel')}
            </button>
            <button
              type="submit"
              class="primary"
              disabled={!canCreate}
              data-testid="identity-wizard-next-step1"
            >
              {creating
                ? t('settings.identityWizard.creating')
                : t('settings.identityWizard.create')}
            </button>
          </div>
        </form>
      </section>
    {:else if step === 2 && createdIdentity}
      <section data-testid="identity-wizard-step-2">
        <h4 class="step-title">{t('settings.identityWizard.step2Title')}</h4>
        <p class="step-intro">
          {t('settings.identityWizard.step2Intro', { email: createdIdentity.email })}
        </p>

        <form
          class="form"
          onsubmit={(e) => {
            e.preventDefault();
            void onVerify();
          }}
        >
          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.codeLabel')}</span>
            <input
              type="text"
              inputmode="numeric"
              autocomplete="one-time-code"
              maxlength="6"
              pattern="[0-9]*"
              value={code}
              oninput={(e) => onCodeInput((e.target as HTMLInputElement).value)}
              disabled={verifying}
              aria-invalid={!!codeError}
              data-testid="identity-wizard-code"
            />
            <span class="helper">{t('settings.identityWizard.codeHelper')}</span>
            {#if codeError}
              <span class="error" role="alert" data-testid="identity-wizard-code-error">
                {codeError}
              </span>
            {/if}
          </label>

          <div class="actions">
            <button
              type="button"
              class="ghost"
              onclick={requestCancel}
              disabled={verifying}
              data-testid="identity-wizard-cancel-step2"
            >
              {t('settings.identityWizard.cancel')}
            </button>
            <button
              type="submit"
              class="primary"
              disabled={verifyDisabled}
              data-testid="identity-wizard-verify"
            >
              {verifying
                ? t('settings.identityWizard.verifying')
                : t('settings.identityWizard.verify')}
            </button>
          </div>

          {#if showCancelNotice}
            <p class="notice" role="note" data-testid="identity-wizard-cancel-notice">
              {t('settings.identityWizard.cancelPendingNotice')}
            </p>
          {/if}
        </form>

        <div class="resend-row">
          <button
            type="button"
            class="ghost"
            onclick={() => void onResend()}
            disabled={resendDisabled}
            data-testid="identity-wizard-resend"
          >
            {resending
              ? t('settings.identityWizard.resending')
              : t('settings.identityWizard.resend')}
          </button>
          {#if cooldown > 0}
            <span class="resend-cooldown" data-testid="identity-wizard-cooldown">
              {t('settings.identityWizard.resendRateLimited', { seconds: cooldown })}
            </span>
          {:else if resendNotice}
            <span class="resend-notice" data-testid="identity-wizard-resend-notice">
              {resendNotice}
            </span>
          {/if}
        </div>
      </section>
    {:else if step === 3 && createdIdentity}
      <section data-testid="identity-wizard-step-3">
        <h4 class="step-title">{t('settings.identityWizard.step3Title')}</h4>
        <p class="step-intro">
          {t('settings.identityWizard.step3Intro', {
            domain: domainOf(createdIdentity.email) ?? '',
          })}
        </p>
        <p class="step-intro">
          {t('settings.identityWizard.step3SkipNote')}
        </p>

        <form
          class="form"
          onsubmit={(e) => {
            e.preventDefault();
            void onStep3Save();
          }}
        >
          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.smtpHost')}</span>
            <input
              type="text"
              autocomplete="off"
              bind:value={smtpHost}
              disabled={smtpSaving}
              aria-invalid={smtpHost !== '' && !smtpHostValid}
              data-testid="identity-wizard-smtp-host"
            />
            {#if smtpHost !== '' && !smtpHostValid}
              <span class="error" role="alert">
                {t('settings.identityWizard.smtpHostRequired')}
              </span>
            {/if}
          </label>

          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.smtpPort')}</span>
            <input
              type="number"
              min="1"
              max="65535"
              bind:value={smtpPort}
              disabled={smtpSaving}
              aria-invalid={!smtpPortValid}
              data-testid="identity-wizard-smtp-port"
            />
            {#if !smtpPortValid}
              <span class="error" role="alert">
                {t('settings.identityWizard.smtpPortInvalid')}
              </span>
            {/if}
          </label>

          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.smtpSecurity')}</span>
            <select
              bind:value={smtpSecurity}
              disabled={smtpSaving}
              data-testid="identity-wizard-smtp-security"
            >
              <option value="starttls">{t('settings.identityWizard.smtpSecurityStartTLS')}</option>
              <option value="implicit_tls">
                {t('settings.identityWizard.smtpSecurityImplicit')}
              </option>
              <option value="none">{t('settings.identityWizard.smtpSecurityNone')}</option>
            </select>
          </label>

          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.smtpUser')}</span>
            <input
              type="text"
              autocomplete="username"
              bind:value={smtpUser}
              disabled={smtpSaving}
              aria-invalid={smtpUser !== '' && !smtpUserValid}
              data-testid="identity-wizard-smtp-user"
            />
            {#if smtpUser !== '' && !smtpUserValid}
              <span class="error" role="alert">
                {t('settings.identityWizard.smtpUserRequired')}
              </span>
            {/if}
          </label>

          <label class="field-label">
            <span class="label-text">{t('settings.identityWizard.smtpPassword')}</span>
            <input
              type="password"
              autocomplete="new-password"
              bind:value={smtpPassword}
              disabled={smtpSaving}
              aria-invalid={smtpPassword !== '' && !smtpPasswordValid}
              data-testid="identity-wizard-smtp-password"
            />
            {#if smtpPassword === '' && smtpUserValid && smtpHostValid}
              <span class="helper">
                {t('settings.identityWizard.smtpPasswordRequired')}
              </span>
            {/if}
          </label>

          {#if smtpError}
            <p class="error-block" role="alert" data-testid="identity-wizard-smtp-error">
              {smtpError}
            </p>
          {/if}

          <div class="actions">
            <button
              type="button"
              class="ghost"
              onclick={onStep3Skip}
              disabled={smtpSaving}
              data-testid="identity-wizard-skip-step3"
            >
              {t('settings.identityWizard.skip')}
            </button>
            <button
              type="submit"
              class="primary"
              disabled={smtpSaveDisabled}
              data-testid="identity-wizard-save-step3"
            >
              {smtpSaving
                ? t('settings.identityWizard.creating')
                : t('settings.identityWizard.done')}
            </button>
          </div>
        </form>
      </section>
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
    width: min(600px, calc(100vw - 2 * var(--spacing-05)));
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

  .step-indicator {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    background: var(--layer-01);
    border-radius: var(--radius-pill);
    padding: 2px var(--spacing-03);
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

  .step-title {
    margin: 0;
    font-size: var(--type-heading-compact-01-size);
    color: var(--text-primary);
  }

  .step-intro {
    margin: var(--spacing-02) 0 var(--spacing-04);
    color: var(--text-secondary);
    line-height: var(--type-body-01-line);
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
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

  input[type='email'],
  input[type='text'],
  input[type='password'],
  input[type='number'],
  select {
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: var(--spacing-03);
  }

  input[data-testid='identity-wizard-code'] {
    font-family: var(--font-mono);
    letter-spacing: 0.2em;
    text-align: center;
  }

  input:focus,
  select:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  input[aria-invalid='true'] {
    border-color: var(--support-error);
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
    margin-top: var(--spacing-04);
  }

  .resend-cooldown,
  .resend-notice {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
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
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }

  .error-block {
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }

  .notice {
    margin: var(--spacing-02) 0 0;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    background: var(--layer-01);
    border-radius: var(--radius-sm);
    padding: var(--spacing-02) var(--spacing-03);
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
