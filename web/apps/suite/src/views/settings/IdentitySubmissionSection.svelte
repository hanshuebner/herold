<script lang="ts">
  /**
   * External SMTP submission section embedded inside the Identity edit dialog.
   *
   * Renders:
   *   - A radio toggle: "Use this server (recommended)" vs
   *     "Use an external SMTP server" (REQ-MAIL-SUBMIT-01).
   *   - OAuth one-click buttons (Gmail / Microsoft 365) when visible
   *     (REQ-MAIL-SUBMIT-02). The suite defaults to showing both;
   *     if the server returns 503 (provider not configured by operator),
   *     an inline error is shown instead of navigating away.
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
    type SubmissionPutBody,
    type SubmitSecurity,
    type SubmitAuthMethod,
    type OAuthProvider,
  } from '../../lib/api/identity-submission';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import { ApiError } from '../../lib/api/client';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import type { Identity } from '../../lib/mail/types';

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

  // ── Local form state ─────────────────────────────────────────────────────

  /**
   * Whether the user has toggled "Use an external SMTP server".
   * Initialised from handle.data.configured once loaded.
   */
  let useExternal = $state(false);
  $effect(() => {
    if (handle.status === 'ready' && handle.data) {
      useExternal = handle.data.configured;
    }
  });

  // Manual entry fields.
  let host = $state('');
  let port = $state(587);
  let security = $state<SubmitSecurity>('starttls');
  let authMethod = $state<SubmitAuthMethod>('password');
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
  let oauthStarting = $state<OAuthProvider | null>(null);

  // ── Handlers ─────────────────────────────────────────────────────────────

  function onToggle(value: boolean): void {
    useExternal = value;
    probeError = null;
    saveError = null;
    oauthError = null;
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
    if (!host.trim()) {
      saveError = 'Host is required.';
      return;
    }
    saving = true;
    const body: SubmissionPutBody = {
      auth_method: authMethod,
      host: host.trim(),
      port,
      security,
      ...(authMethod === 'password' ? { password } : {}),
    };
    try {
      await putSubmission(identity.id, body);
      submissionStore.evict(identity.id);
      await handle.refresh();
      toast.show({ message: 'External submission saved.', timeoutMs: 4000 });
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
      title: 'Remove external submission config?',
      message:
        'This identity will revert to sending through herold\'s outbound queue.',
      confirmLabel: 'Remove',
      cancelLabel: 'Cancel',
      kind: 'danger',
    });
    if (!ok) return;
    removing = true;
    try {
      await deleteSubmission(identity.id);
      submissionStore.evict(identity.id);
      useExternal = false;
      host = '';
      port = 587;
      security = 'starttls';
      authMethod = 'password';
      password = '';
      probeError = null;
      saveError = null;
      await handle.refresh();
      toast.show({ message: 'External submission removed.', timeoutMs: 4000 });
      onchange?.();
    } catch (err) {
      saveError = err instanceof Error ? err.message : String(err);
    } finally {
      removing = false;
    }
  }

  async function startOAuthFlow(provider: OAuthProvider): Promise<void> {
    if (oauthStarting) return;
    oauthError = null;
    oauthStarting = provider;
    try {
      await startOAuth(identity.id, provider);
      // The browser navigates away; we only reach here if startOAuth threw
      // (e.g. 503 provider not configured).
    } catch (err) {
      if (err instanceof ApiError && err.status === 503) {
        oauthError = `The ${providerLabel(provider)} provider is not configured on this server. Use manual entry instead.`;
      } else {
        oauthError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      oauthStarting = null;
    }
  }

  function probeCategoryLabel(category: string): string {
    switch (category) {
      case 'auth-failed':
        return 'Authentication failed';
      case 'unreachable':
        return 'Server unreachable';
      case 'permanent':
        return 'Rejected by external server';
      case 'transient':
        return 'Temporary failure';
      default:
        return 'Probe failed';
    }
  }

  function providerLabel(provider: OAuthProvider): string {
    return provider === 'gmail' ? 'Gmail' : 'Microsoft 365';
  }

  let isConfigured = $derived(handle.data?.configured === true);
</script>

<div class="submission-section">
  <h4 class="section-title">External SMTP submission</h4>
  <p class="section-hint">
    By default, mail sent as this identity goes through herold's outbound queue.
    You can instead route it through an external SMTP server (e.g. Gmail,
    Microsoft 365, or a corporate relay).
  </p>

  {#if handle.status === 'loading' || handle.status === 'idle'}
    <div class="spinner" role="status" aria-label="Loading submission config"></div>
  {:else if handle.status === 'error'}
    <p class="form-error" role="alert">{handle.error}</p>
  {:else}
    <!-- Toggle -->
    <div class="radio-group" role="radiogroup" aria-label="Submission routing">
      <label class="radio-label">
        <input
          type="radio"
          name="submission-mode-{identity.id}"
          checked={!useExternal}
          onchange={() => onToggle(false)}
          disabled={saving || removing}
        />
        <span>
          Use this server <span class="recommended">(recommended)</span>
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
        <span>Use an external SMTP server</span>
      </label>
    </div>

    {#if useExternal}
      <!-- External config panel -->
      <div class="external-panel">

        <!-- OAuth one-click buttons -->
        <div class="oauth-section">
          <p class="oauth-hint">
            For Gmail or Microsoft 365, sign in with one click to configure
            submission automatically:
          </p>
          <div class="oauth-buttons">
            <button
              type="button"
              class="btn-oauth"
              onclick={() => void startOAuthFlow('gmail')}
              disabled={oauthStarting !== null || saving}
            >
              {oauthStarting === 'gmail' ? 'Starting...' : 'Sign in with Google'}
            </button>
            <button
              type="button"
              class="btn-oauth"
              onclick={() => void startOAuthFlow('m365')}
              disabled={oauthStarting !== null || saving}
            >
              {oauthStarting === 'm365' ? 'Starting...' : 'Sign in with Microsoft'}
            </button>
          </div>
          {#if oauthError}
            <p class="form-error" role="alert">{oauthError}</p>
          {/if}
          <p class="or-divider">or enter server details manually:</p>
        </div>

        <!-- Manual entry form -->
        <form class="manual-form" onsubmit={save} novalidate>
          <div class="field">
            <label for="sub-host-{identity.id}" class="field-label">Host</label>
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
              <label for="sub-port-{identity.id}" class="field-label">Port</label>
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
              <label for="sub-security-{identity.id}" class="field-label">Security</label>
              <select
                id="sub-security-{identity.id}"
                class="select"
                bind:value={security}
                disabled={saving}
              >
                <option value="implicit_tls">Implicit TLS (port 465)</option>
                <option value="starttls">STARTTLS (port 587)</option>
                <option value="none">None (plaintext)</option>
              </select>
            </div>
          </div>

          <div class="field">
            <label for="sub-authmethod-{identity.id}" class="field-label">
              Authentication method
            </label>
            <select
              id="sub-authmethod-{identity.id}"
              class="select"
              bind:value={authMethod}
              disabled={saving}
            >
              <option value="password">Password / app-specific password</option>
              <option value="oauth2">OAuth 2.0 token</option>
            </select>
          </div>

          {#if authMethod === 'password'}
            <div class="field">
              <label for="sub-password-{identity.id}" class="field-label">
                Password
                {#if isConfigured}
                  <span class="muted">(leave blank to keep existing)</span>
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
            <p class="hint">
              OAuth 2.0 token-based auth requires the access and refresh tokens
              from a completed OAuth flow. Use the "Sign in with Google" or
              "Sign in with Microsoft" buttons above for a guided flow.
            </p>
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
              <button
                type="button"
                class="btn-remove"
                onclick={() => void remove()}
                disabled={removing || saving}
              >
                {removing ? 'Removing...' : 'Remove external configuration'}
              </button>
            {/if}
            <span class="spacer"></span>
            <button
              type="submit"
              class="btn-primary"
              disabled={saving || removing || authMethod === 'oauth2'}
              title={authMethod === 'oauth2'
                ? 'Use the OAuth buttons above to configure OAuth 2.0'
                : ''}
            >
              {saving ? 'Saving and testing...' : 'Save and test connection'}
            </button>
          </div>
        </form>
      </div>
    {:else if isConfigured}
      <!-- Was configured but user toggled back -->
      <div class="revert-hint">
        <p class="hint">
          External submission is currently configured. To remove it and revert to
          herold's outbound queue, expand the external option and click
          "Remove external configuration".
        </p>
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

  .btn-oauth {
    padding: var(--spacing-02) var(--spacing-05);
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }

  .btn-oauth:hover:not(:disabled) {
    background: var(--layer-03);
  }

  .btn-oauth:disabled {
    opacity: 0.6;
    cursor: not-allowed;
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

  .btn-primary {
    padding: var(--spacing-02) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }

  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-remove {
    padding: var(--spacing-02) var(--spacing-04);
    background: none;
    color: var(--support-error);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }

  .btn-remove:hover:not(:disabled) {
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
  }

  .btn-remove:disabled {
    opacity: 0.6;
    cursor: not-allowed;
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
</style>
