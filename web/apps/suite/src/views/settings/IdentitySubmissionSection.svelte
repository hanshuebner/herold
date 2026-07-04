<script lang="ts">
  /**
   * External SMTP submission section embedded inside the Identity edit dialog.
   *
   * Renders:
   *   - A radio toggle: "Use this server (recommended)" vs
   *     "Use an external SMTP server" (REQ-MAIL-SUBMIT-01).
   *     When the identity's domain is not authoritative on this server
   *     (domain_authoritative=false per re #74), the local-server option is
   *     omitted and external submission is required.
   *   - OAuth one-click buttons for each provider id listed in
   *     available_oauth_providers from the server (re #73). When the list
   *     is empty, no OAuth buttons are rendered.
   *   - Manual entry form: host, port, security, auth method, credential
   *     (REQ-MAIL-SUBMIT-03).
   *   - Inline probe-failure error from 422 responses (REQ-MAIL-SUBMIT-03).
   *   - A "Remove external configuration" affordance when already configured.
   *
   * This component does NOT close on failure — per REQ-MAIL-SUBMIT-03
   * probe failures render inline.
   */

  import {
    putSubmission,
    deleteSubmission,
    startOAuth,
    testSubmission,
    type SubmissionPutBody,
    type SubmitSecurity,
    type SubmitAuthMethod,
    type OAuthProvider,
    type TestSubmissionResult,
  } from '../../lib/api/identity-submission';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import { ApiError } from '../../lib/api/client';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import type { Identity } from '../../lib/mail/types';
  import Button from '@herold/design-system/Button.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import { auth } from '../../lib/auth/auth.svelte';

  interface Props {
    identity: Identity;
    /** Called after a successful PUT or DELETE so the parent can refresh. */
    onchange?: () => void;
  }

  let { identity, onchange }: Props = $props();

  const handle = $derived(submissionStore.forIdentity(identity.id));

  // Load submission status on mount (idempotent).
  $effect(() => {
    void handle.load();
  });

  // ── Derived server capabilities ───────────────────────────────────────────

  /**
   * True when the identity's domain is locally authoritative. Defaults to
   * true before the data loads so the UI shows a radio group, not the
   * forced-external notice, during the brief loading state.
   */
  let domainAuthoritative = $derived(handle.data?.domain_authoritative ?? true);

  /**
   * Sorted list of OAuth provider ids configured on this server. Empty
   * until the data loads, or when the operator has configured none (re #73).
   */
  let availableOAuthProviders = $derived(handle.data?.available_oauth_providers ?? []);

  // ── Local form state ─────────────────────────────────────────────────────

  /**
   * Whether the user has toggled "Use an external SMTP server".
   * Initialised from handle.data once loaded. When domain_authoritative
   * is false the value is forced to true and the toggle is hidden (re #74).
   */
  let useExternal = $state(false);
  $effect(() => {
    if (handle.status === 'ready' && handle.data) {
      if (!handle.data.domain_authoritative) {
        // External submission is required for non-authoritative domains.
        useExternal = true;
      } else {
        useExternal = handle.data.configured;
      }
    }
  });

  // Manual entry fields.
  let host = $state('');
  let port = $state(587);
  let security = $state<SubmitSecurity>('starttls');
  let authMethod = $state<SubmitAuthMethod>('password');
  /**
   * The SMTP AUTH username. The server never returns the stored value (no
   * credential material in GET responses), so we always seed from the
   * identity email address — the most common value — and let the user edit
   * it before saving.
   */
  let authUser = $state('');
  $effect(() => {
    const email = identity.email;
    if (!authUser) authUser = email;
  });
  let password = $state('');

  // Pre-fill from existing config when loaded.
  $effect(() => {
    if (handle.status === 'ready' && handle.data?.configured) {
      host = handle.data.submit_host ?? '';
      port = handle.data.submit_port ?? 587;
      security = handle.data.submit_security ?? 'starttls';
      authMethod = handle.data.submit_auth_method ?? 'password';
    }
  });

  let saving = $state(false);
  let removing = $state(false);

  /** Error from PUT 422 probe failure. */
  let probeError = $state<{ category: string; diagnostic: string } | null>(null);
  /** Generic save error (non-422). */
  let saveError = $state<string | null>(null);
  /** OAuth button-specific error (e.g. 503 provider not configured). */
  let oauthError = $state<string | null>(null);
  let oauthStarting = $state<string | null>(null);

  /** Pending state for the on-demand SMTP probe button (re #113, re #115). */
  let testingSmtp = $state(false);
  /**
   * Most recent result of the on-demand SMTP probe. Null until the user
   * has clicked "Send test message" at least once, or after a toggle/save
   * resets it.
   */
  let testSmtpResult = $state<TestSubmissionResult | null>(null);
  /**
   * The recipient address for the test message (re #122). Shown in the
   * inline prompt that appears when the user clicks "Send test message".
   * Pre-filled with the user's own primary email (auth.session.username),
   * falling back to the identity's address.
   */
  let testRecipient = $state('');
  /**
   * True while the inline recipient-address prompt is visible. The prompt
   * is shown when the user clicks "Send test message" and dismissed after
   * sending or cancelling.
   */
  let showTestPrompt = $state(false);
  /**
   * The recipient address used in the most recent SMTP test. Retained so
   * the success message can name the address without adding a `to` field
   * to TestSubmissionResult.
   */
  let lastTestRecipient = $state('');

  // ── Handlers ─────────────────────────────────────────────────────────────

  function onToggle(value: boolean): void {
    useExternal = value;
    probeError = null;
    saveError = null;
    oauthError = null;
    testSmtpResult = null;
    showTestPrompt = false;
    if (!value && handle.data?.configured) {
      // The user toggled back to "Use this server" with an existing config.
      // Offer to remove it (handled by the Remove button; this just reflects UI).
    }
  }

  async function save(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (saving) return;
    probeError = null;
    saveError = null;
    testSmtpResult = null;
    showTestPrompt = false;
    if (!host.trim()) {
      saveError = t('settings.submission.hostRequired');
      return;
    }
    saving = true;
    const body: SubmissionPutBody = {
      submit_auth_method: authMethod,
      submit_host: host.trim(),
      submit_port: port,
      submit_security: security,
      auth_user: authUser.trim() || undefined,
      ...(authMethod === 'password' ? { password } : {}),
    };
    try {
      await putSubmission(identity.id, body);
      await handle.refresh();
      toast.show({ message: t('settings.submission.toast.saved'), timeoutMs: 4000 });
      onchange?.();
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        // Probe failure — surface inline (REQ-MAIL-SUBMIT-03).
        const detail = err.detail as {
          type?: string;
          category?: string;
          diagnostic?: string;
        } | null;
        probeError = {
          category: detail?.category ?? 'permanent',
          diagnostic: detail?.diagnostic ?? err.message,
        };
      } else {
        saveError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      saving = false;
    }
  }

  async function remove(): Promise<void> {
    const ok = await confirm.ask({
      title: t('settings.submission.confirmRemoveTitle'),
      message: t('settings.submission.confirmRemoveMessage'),
      confirmLabel: t('settings.submission.confirmRemoveConfirm'),
      cancelLabel: t('settings.submission.confirmRemoveCancel'),
      kind: 'danger',
    });
    if (!ok) return;
    removing = true;
    try {
      await deleteSubmission(identity.id);
      if (domainAuthoritative) {
        useExternal = false;
      }
      host = '';
      port = 587;
      security = 'starttls';
      authMethod = 'password';
      authUser = identity.email;
      password = '';
      probeError = null;
      saveError = null;
      await handle.refresh();
      toast.show({ message: t('settings.submission.toast.removed'), timeoutMs: 4000 });
      onchange?.();
    } catch (err) {
      saveError = err instanceof Error ? err.message : String(err);
    } finally {
      removing = false;
    }
  }

  async function startOAuthFlow(provider: string): Promise<void> {
    if (oauthStarting) return;
    oauthError = null;
    oauthStarting = provider;
    try {
      await startOAuth(identity.id, provider as OAuthProvider);
      // The browser navigates away; we only reach here if startOAuth threw
      // (e.g. 503 provider not configured).
    } catch (err) {
      if (err instanceof ApiError && err.status === 503) {
        oauthError = t('settings.submission.oauthError.notConfigured', { provider: providerLabel(provider) });
      } else {
        oauthError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      oauthStarting = null;
    }
  }

  /**
   * Open the inline recipient-address prompt (re #122). Pre-fills testRecipient
   * with the user's own primary email (auth.session.username), falling back to
   * the identity's address when the session is not yet available.
   */
  function openTestPrompt(): void {
    testRecipient = auth.session?.username ?? identity.email;
    showTestPrompt = true;
    testSmtpResult = null;
  }

  /**
   * Run the on-demand SMTP probe (re #113, re #115, re #122).
   *
   * POSTs to /api/v1/identities/{id}/submission/test with the recipient
   * address the user confirmed in the inline prompt. Distinct from the
   * save-probe (PUT) and from the OAuth re-auth button: this exercises the
   * configured SMTP credentials without modifying them.
   */
  async function runSmtpTest(recipient: string): Promise<void> {
    if (testingSmtp) return;
    showTestPrompt = false;
    lastTestRecipient = recipient;
    testingSmtp = true;
    testSmtpResult = null;
    try {
      testSmtpResult = await testSubmission(identity.id, { to: recipient });
    } finally {
      testingSmtp = false;
    }
  }

  function probeCategoryLabel(category: string): string {
    switch (category) {
      case 'auth-failed':
        return t('settings.submission.probe.authFailed');
      case 'unreachable':
        return t('settings.submission.probe.unreachable');
      case 'permanent':
        return t('settings.submission.probe.permanent');
      case 'transient':
        return t('settings.submission.probe.transient');
      default:
        return t('settings.submission.probe.default');
    }
  }

  function providerLabel(provider: string): string {
    return provider === 'gmail' ? 'Gmail' : 'Microsoft 365';
  }

  /** Label for the OAuth sign-in button for a given provider id. */
  function providerSigninLabel(provider: string): string {
    if (provider === 'gmail') return t('settings.submission.signinGoogle');
    if (provider === 'm365') return t('settings.submission.signinMicrosoft');
    return provider;
  }

  let isConfigured = $derived(handle.data?.configured === true);

  /**
   * True when the current submission config uses OAuth2 tokens stored by a
   * previous OAuth flow. In this state the "Mit Google/Microsoft anmelden"
   * initial-setup CTA is hidden (not needed) and a dedicated
   * "Verbindung testen" button re-runs the OAuth flow to re-authorise and
   * verify connectivity (re #105).
   */
  let isOAuthConfigured = $derived(isConfigured && handle.data?.submit_auth_method === 'oauth2');

  /**
   * Maps a known SMTP submission host back to its OAuth provider id.
   * Returns null for custom or unknown hosts.
   */
  function providerFromHost(host: string): string | null {
    if (host === 'smtp.gmail.com') return 'gmail';
    if (host === 'smtp.office365.com') return 'm365';
    return null;
  }

  /**
   * The OAuth provider id inferred from the stored submit_host, or null when
   * the host is unknown or the submission is not oauth2-configured.
   * Used to wire the "Verbindung testen" button for oauth2 submissions.
   */
  let oauthConfiguredProvider = $derived(
    isOAuthConfigured && handle.data?.submit_host
      ? providerFromHost(handle.data.submit_host)
      : null,
  );
</script>

<div class="submission-section">
  <h4 class="section-title">{t('settings.submission.title')}</h4>
  <p class="section-hint">
    {domainAuthoritative
      ? t('settings.submission.hint')
      : t('settings.submission.foreignDomainHint')}
  </p>

  {#if (handle.status === 'loading' || handle.status === 'idle') && handle.data === null}
    <div class="spinner" role="status" aria-label={t('settings.submission.loadingAriaLabel')}></div>
  {:else if handle.status === 'error'}
    <p class="form-error" role="alert">{handle.error}</p>
  {:else}
    {#if domainAuthoritative}
      <!-- Toggle (only shown when this server can handle the domain) -->
      <div class="radio-group" role="radiogroup" aria-label={t('settings.submission.radioGroup')}>
        <label class="radio-label">
          <input
            type="radio"
            name="submission-mode-{identity.id}"
            checked={!useExternal}
            onchange={() => onToggle(false)}
            disabled={saving || removing}
          />
          <span>
            {t('settings.submission.useThisServer')} <span class="recommended">{t('settings.submission.recommended')}</span>
          </span>
        </label>
        <label class="radio-label">
          <input
            type="radio"
            name="submission-mode-{identity.id}"
            checked={useExternal}
            onchange={() => onToggle(true)}
            disabled={saving || removing}
          />
          <span>{t('settings.submission.useExternal')}</span>
        </label>
      </div>
    {/if}

    {#if useExternal}
      <!-- External config panel -->
      <div class="external-panel">

        <!-- OAuth one-click buttons: shown for initial setup only, hidden once
             any submission is configured (password or oauth2, re #121). When
             already oauth2-configured, a "Verbindung testen" button in the
             form-actions section re-runs the OAuth flow instead (re #105). -->
        {#if availableOAuthProviders.length > 0 && !isConfigured}
          <div class="oauth-section">
            <p class="oauth-hint">{t('settings.submission.oauthHint')}</p>
            <div class="oauth-buttons">
              {#each availableOAuthProviders as provider (provider)}
                <Button
                  variant="secondary"
                  onclick={() => void startOAuthFlow(provider)}
                  disabled={oauthStarting !== null || saving}
                >
                  {oauthStarting === provider ? t('settings.submission.starting') : providerSigninLabel(provider)}
                </Button>
              {/each}
            </div>
            {#if oauthError}
              <p class="form-error" role="alert">{oauthError}</p>
            {/if}
            <p class="or-divider">{t('settings.submission.orManual')}</p>
          </div>
        {:else if oauthError}
          <p class="form-error" role="alert">{oauthError}</p>
        {/if}

        <!-- Manual entry form -->
        <form class="manual-form" onsubmit={save} novalidate>
          <div class="field">
            <label for="sub-host-{identity.id}" class="field-label">{t('settings.submission.fieldHost')}</label>
            <input
              id="sub-host-{identity.id}"
              type="text"
              class="input"
              placeholder="smtp.gmail.com"
              bind:value={host}
              disabled={saving}
              autocomplete="off"
              spellcheck="false"
            />
          </div>

          <div class="field-row">
            <div class="field field-port">
              <label for="sub-port-{identity.id}" class="field-label">{t('settings.submission.fieldPort')}</label>
              <input
                id="sub-port-{identity.id}"
                type="number"
                class="input"
                min="1"
                max="65535"
                bind:value={port}
                disabled={saving}
              />
            </div>

            <div class="field field-security">
              <label for="sub-security-{identity.id}" class="field-label">{t('settings.submission.fieldSecurity')}</label>
              <select
                id="sub-security-{identity.id}"
                class="select"
                bind:value={security}
                disabled={saving}
              >
                <option value="implicit_tls">{t('settings.submission.securityImplicitTls')}</option>
                <option value="starttls">{t('settings.submission.securityStarttls')}</option>
                <option value="none">{t('settings.submission.securityNone')}</option>
              </select>
            </div>
          </div>

          <div class="field">
            <label for="sub-authmethod-{identity.id}" class="field-label">
              {t('settings.submission.fieldAuthMethod')}
            </label>
            <select
              id="sub-authmethod-{identity.id}"
              class="select"
              bind:value={authMethod}
              disabled={saving}
            >
              <option value="password">{t('settings.submission.authPassword')}</option>
              <option value="oauth2">{t('settings.submission.authOauth2')}</option>
            </select>
          </div>

          <div class="field">
            <label for="sub-username-{identity.id}" class="field-label">
              {t('settings.submission.fieldUsername')}
            </label>
            <input
              id="sub-username-{identity.id}"
              type="text"
              class="input"
              autocomplete="username"
              bind:value={authUser}
              disabled={saving}
              spellcheck="false"
            />
          </div>

          {#if authMethod === 'password'}
            <div class="field">
              <label for="sub-password-{identity.id}" class="field-label">
                {t('settings.submission.fieldPassword')}
                {#if isConfigured}
                  <span class="muted">{t('settings.submission.passwordKeepExisting')}</span>
                {/if}
              </label>
              <input
                id="sub-password-{identity.id}"
                type="password"
                class="input"
                autocomplete="new-password"
                bind:value={password}
                disabled={saving}
                placeholder={isConfigured ? '••••••••' : ''}
              />
            </div>
          {:else}
            <p class="hint">{t('settings.submission.oauth2Hint')}</p>
          {/if}

          {#if probeError}
            <div class="probe-error" role="alert">
              <strong>{probeCategoryLabel(probeError.category)}.</strong>
              {probeError.diagnostic}
            </div>
          {/if}

          {#if saveError}
            <p class="form-error" role="alert">{saveError}</p>
          {/if}

          <div class="form-actions">
            {#if isConfigured}
              <Button
                variant="danger"
                onclick={() => void remove()}
                disabled={removing || saving}
              >
                {removing ? t('settings.submission.removing') : t('settings.submission.removeConfig')}
              </Button>
            {/if}
            <span class="spacer"></span>
            {#if isConfigured}
              <!-- On-demand SMTP probe button (re #113, re #115, re #122).
                   Distinct from the save-probe (PUT/submit) and from the OAuth
                   re-auth button: exercises stored credentials without changing
                   them and delivers a test message to a recipient the user
                   chooses in the inline prompt. -->
              <Button
                variant="secondary"
                onclick={() => openTestPrompt()}
                disabled={testingSmtp || saving || removing || showTestPrompt}
              >
                {testingSmtp
                  ? t('settings.submission.sendTestPending')
                  : t('settings.submission.sendTest')}
              </Button>
            {/if}
            {#if isOAuthConfigured && oauthConfiguredProvider !== null}
              <!-- For oauth2-configured identities, "Verbindung testen"
                   re-runs the OAuth flow: the provider re-issues tokens and
                   herold re-probes the SMTP connection. For already-consented
                   providers (Gmail, Microsoft) this usually completes
                   instantly without user interaction (re #105). -->
              <Button
                variant="primary"
                onclick={() => void startOAuthFlow(oauthConfiguredProvider!)}
                disabled={oauthStarting !== null || removing}
              >
                {oauthStarting === oauthConfiguredProvider
                  ? t('settings.submission.starting')
                  : t('settings.submission.testConnection')}
              </Button>
            {:else}
              <Button
                type="submit"
                variant="primary"
                disabled={saving || removing || authMethod === 'oauth2'}
                title={authMethod === 'oauth2'
                  ? t('settings.submission.oauth2Title')
                  : ''}
              >
                {saving ? t('settings.submission.saving') : t('settings.submission.saveAndTest')}
              </Button>
            {/if}
          </div>

          {#if !isOAuthConfigured || oauthConfiguredProvider === null}
            <!-- Description for the manual save+test button: tells the user
                 what the action actually does (connection probe, no message
                 delivered) so the effect is not ambiguous (re #123). -->
            <p class="save-test-hint">{t('settings.submission.saveAndTestHint')}</p>
          {/if}

          {#if showTestPrompt}
            <!-- Inline recipient-address prompt (re #122). Shown when the user
                 clicks "Send test message"; allows them to confirm or change
                 the destination before the test is sent. -->
            <div class="test-prompt">
              <div class="field">
                <label for="test-recipient-{identity.id}" class="field-label">
                  {t('settings.submission.sendTestRecipientLabel')}
                </label>
                <input
                  id="test-recipient-{identity.id}"
                  type="email"
                  class="input"
                  bind:value={testRecipient}
                  disabled={testingSmtp}
                  spellcheck="false"
                  autocomplete="email"
                />
              </div>
              <div class="test-prompt-actions">
                <Button
                  variant="primary"
                  onclick={() => void runSmtpTest(testRecipient)}
                  disabled={testingSmtp || !testRecipient.trim()}
                >
                  {t('settings.submission.sendTestConfirm')}
                </Button>
                <button
                  type="button"
                  class="cancel-link"
                  onclick={() => { showTestPrompt = false; }}
                  disabled={testingSmtp}
                >
                  {t('settings.submission.confirmRemoveCancel')}
                </button>
              </div>
            </div>
          {/if}

          {#if testSmtpResult !== null}
            <div
              class={testSmtpResult.ok ? 'test-result-ok' : 'probe-error'}
              role={testSmtpResult.ok ? 'status' : 'alert'}
            >
              {#if testSmtpResult.ok}
                {t('settings.submission.sendTestOk', { to: lastTestRecipient })}
              {:else}
                {t('settings.submission.sendTestFail', { detail: testSmtpResult.detail })}
              {/if}
            </div>
          {/if}
        </form>
      </div>
    {:else if isConfigured}
      <!-- Was configured but user toggled back (only reachable when domainAuthoritative) -->
      <div class="revert-hint">
        <p class="hint">{t('settings.submission.revertHint')}</p>
      </div>
    {/if}
  {/if}
</div>

<style>
  .submission-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding-top: var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
    margin-top: var(--spacing-04);
  }

  .section-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .section-hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
    line-height: var(--type-body-compact-01-line);
  }

  .radio-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .radio-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    cursor: pointer;
  }

  .radio-label input[type='radio'] {
    accent-color: var(--interactive);
    width: 16px;
    height: 16px;
    flex-shrink: 0;
    cursor: pointer;
  }

  .recommended {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .external-panel {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  /* OAuth section */
  .oauth-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .oauth-hint {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .oauth-buttons {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .or-divider {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: var(--spacing-02) 0 0;
  }

  /* Manual form */
  .manual-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .field-row {
    display: flex;
    gap: var(--spacing-03);
  }

  .field-port {
    flex: 0 0 100px;
  }

  .field-security {
    flex: 1;
  }

  .field-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--background);
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
    opacity: 0.6;
    cursor: not-allowed;
  }

  .select {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
  }

  .select:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
    line-height: var(--type-body-compact-01-line);
  }

  .muted {
    color: var(--text-helper);
    font-weight: 400;
  }

  /* Inline recipient-address prompt (re #122). */
  .test-prompt {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .test-prompt-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
  }

  .cancel-link {
    background: none;
    border: none;
    padding: 0;
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    text-decoration: underline;
    text-underline-offset: 2px;
  }

  .cancel-link:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  /* On-demand SMTP probe result — success */
  .test-result-ok {
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-success) 10%, transparent);
    border-left: 3px solid var(--support-success);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
  }

  /* Probe failure */
  .probe-error {
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-left: 3px solid var(--support-error);
    border-radius: var(--radius-md);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
  }

  .probe-error strong {
    font-weight: 700;
    display: block;
    margin-bottom: var(--spacing-01);
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
    align-items: center;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  .spacer {
    flex: 1;
  }

  .revert-hint {
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .spinner {
    width: 16px;
    height: 16px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner {
      animation: none;
    }
  }

  /* Description below the save+test button explaining the probe (re #123). */
  .save-test-hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
    line-height: var(--type-body-compact-01-line);
  }
</style>
