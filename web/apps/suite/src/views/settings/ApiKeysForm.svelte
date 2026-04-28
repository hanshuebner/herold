<script lang="ts">
  /**
   * API key management panel -- Phase 4 (REQ-ADM-203).
   *
   * Lists all API keys belonging to the current user via
   * GET /api/v1/api-keys, allows creation via
   * POST /api/v1/principals/{pid}/api-keys, and revocation via
   * DELETE /api/v1/api-keys/{id}.
   *
   * After creation the plaintext token is shown exactly once in a
   * copy-to-clipboard panel. Dismissing the panel zeroes the in-memory
   * reference so the plaintext is not retained.
   *
   * Scope vocabulary is restricted to AllEndUserScopes (REQ-AUTH-SCOPE-01):
   * admin scope is not offered here because the Suite runs on the public
   * listener; creating admin-scoped keys requires the admin SPA.
   */
  import { auth } from '../../lib/auth/auth.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { get, post, del, ApiError } from '../../lib/api/client';
  import { localeTag } from '../../lib/i18n/i18n.svelte';

  // End-user scopes available for key creation. Sorted to match AllEndUserScopes
  // canonical order (auth/scope.go). Admin and webhook.publish are deliberately
  // omitted: the former requires AllowAdminScope acknowledgement that belongs on
  // the admin surface; the latter is an operator-issued scope for transactional
  // senders.
  const END_USER_SCOPES: { value: string; label: string }[] = [
    { value: 'end-user', label: 'End-user' },
    { value: 'mail.send', label: 'Mail: send' },
    { value: 'mail.receive', label: 'Mail: receive' },
    { value: 'chat.read', label: 'Chat: read' },
    { value: 'chat.write', label: 'Chat: write' },
    { value: 'cal.read', label: 'Calendar: read' },
    { value: 'cal.write', label: 'Calendar: write' },
    { value: 'contacts.read', label: 'Contacts: read' },
    { value: 'contacts.write', label: 'Contacts: write' },
  ];

  interface APIKeyDTO {
    id: number;
    principal_id: number;
    label: string;
    created_at: string;
    last_used_at?: string;
  }

  interface CreateAPIKeyResponse {
    id: number;
    principal_id: number;
    label: string;
    key: string;
    created_at: string;
    scope: string[];
  }

  interface PageDTO<T> {
    items: T[];
    next: string | null;
  }

  // --- Key list ---

  let keys = $state<APIKeyDTO[]>([]);
  let listLoading = $state(true);
  let listError = $state<string | null>(null);

  async function loadKeys(): Promise<void> {
    listLoading = true;
    listError = null;
    try {
      const result = await get<PageDTO<APIKeyDTO>>('/api/v1/api-keys');
      keys = result.items ?? [];
    } catch (err) {
      listError = errorMessage(err);
    } finally {
      listLoading = false;
    }
  }

  $effect(() => {
    if (auth.principalId) {
      void loadKeys();
    }
  });

  async function revokeKey(id: number): Promise<void> {
    const ok = await confirm.ask({
      title: 'Revoke this API key?',
      message: 'Any applications using it will stop working immediately.',
      confirmLabel: 'Revoke',
      cancelLabel: 'Cancel',
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await del<void>(`/api/v1/api-keys/${String(id)}`);
      keys = keys.filter((k) => k.id !== id);
      toast.show({ message: 'API key revoked.', timeoutMs: 4000 });
    } catch (err) {
      toast.show({ message: errorMessage(err), kind: 'error', timeoutMs: 0 });
    }
  }

  // --- Key creation ---

  let createOpen = $state(false);
  let createLabel = $state('');
  let createScopes = $state<string[]>([]);
  let creating = $state(false);
  let createError = $state<string | null>(null);

  // Plaintext token reveal. Cleared by dismissReveal() to ensure the secret
  // is not retained in memory beyond the user's explicit acknowledgement.
  let revealToken = $state<string | null>(null);
  let revealCopied = $state(false);

  function openCreate(): void {
    createLabel = '';
    createScopes = [];
    createError = null;
    createOpen = true;
  }

  function cancelCreate(): void {
    createOpen = false;
    createError = null;
  }

  function dismissReveal(): void {
    revealToken = null;
    revealCopied = false;
  }

  function toggleScope(scope: string): void {
    if (createScopes.includes(scope)) {
      createScopes = createScopes.filter((s) => s !== scope);
    } else {
      createScopes = [...createScopes, scope];
    }
  }

  async function createKey(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (creating) return;
    const pid = auth.principalId;
    if (!pid) {
      createError = 'Session not ready. Please reload.';
      return;
    }
    createError = null;
    creating = true;
    try {
      const result = await post<CreateAPIKeyResponse>(
        `/api/v1/principals/${pid}/api-keys`,
        { label: createLabel || 'my-key', scope: createScopes },
      );
      // Add to the list (without the plaintext token).
      keys = [
        ...keys,
        {
          id: result.id,
          principal_id: result.principal_id,
          label: result.label,
          created_at: result.created_at,
        },
      ];
      // Show the token once. The reference is cleared by dismissReveal().
      revealToken = result.key;
      revealCopied = false;
      createOpen = false;
    } catch (err) {
      createError = errorMessage(err);
    } finally {
      creating = false;
    }
  }

  function copyToken(): void {
    if (revealToken) {
      void navigator.clipboard.writeText(revealToken);
      revealCopied = true;
    }
  }

  // --- Helpers ---

  function errorMessage(err: unknown): string {
    if (err instanceof ApiError) return err.message;
    if (err instanceof Error) return err.message;
    return String(err);
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString(localeTag(), {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  }
</script>

<!-- Plaintext token reveal panel (shown immediately after creation) -->
{#if revealToken}
  <div class="reveal-panel">
    <p class="reveal-warning">
      Copy this key now. It will not be shown again.
    </p>
    <div class="reveal-row">
      <input
        type="text"
        class="input mono"
        readonly
        value={revealToken}
        onclick={(e) => (e.currentTarget as HTMLInputElement).select()}
        aria-label="New API key"
      />
      <button type="button" class="btn-secondary" onclick={copyToken}>
        {revealCopied ? 'Copied' : 'Copy'}
      </button>
    </div>
    <div class="reveal-actions">
      <button type="button" class="btn-primary" onclick={dismissReveal}>
        I have saved this key
      </button>
    </div>
  </div>
{/if}

<!-- Create form (inline collapsible) -->
{#if createOpen}
  <form class="create-form" onsubmit={createKey} novalidate>
    <h3>New API key</h3>

    <div class="field">
      <label for="key-label" class="label">Label</label>
      <input
        id="key-label"
        type="text"
        class="input"
        placeholder="e.g. my-script"
        bind:value={createLabel}
        disabled={creating}
      />
    </div>

    <div class="field">
      <span class="label">Scopes</span>
      <p class="hint">Select the permissions this key grants. Leave empty for mail.send only (default).</p>
      <div class="scope-grid">
        {#each END_USER_SCOPES as scope (scope.value)}
          <label class="check-label">
            <input
              type="checkbox"
              checked={createScopes.includes(scope.value)}
              onchange={() => toggleScope(scope.value)}
              disabled={creating}
            />
            {scope.label}
          </label>
        {/each}
      </div>
    </div>

    {#if createError}
      <p class="form-error" role="alert">{createError}</p>
    {/if}

    <div class="form-actions">
      <button type="button" class="btn-secondary" onclick={cancelCreate} disabled={creating}>
        Cancel
      </button>
      <button type="submit" class="btn-primary" disabled={creating}>
        {creating ? 'Creating...' : 'Create key'}
      </button>
    </div>
  </form>
{:else}
  <div class="list-header">
    <button type="button" class="btn-primary" onclick={openCreate}>
      Create new key
    </button>
  </div>
{/if}

<!-- Key list -->
{#if listLoading}
  <div class="spinner" role="status" aria-label="Loading API keys"></div>
{:else if listError}
  <p class="form-error" role="alert">{listError}</p>
{:else if keys.length === 0}
  <p class="muted">No API keys yet.</p>
{:else}
  <div class="keys-list">
    {#each keys as key (key.id)}
      <div class="key-row">
        <div class="key-info">
          <span class="key-name">{key.label}</span>
          <span class="key-meta">Created {formatDate(key.created_at)}</span>
          {#if key.last_used_at}
            <span class="key-meta">Last used {formatDate(key.last_used_at)}</span>
          {/if}
        </div>
        <button
          type="button"
          class="btn-revoke"
          onclick={() => void revokeKey(key.id)}
        >
          Revoke
        </button>
      </div>
    {/each}
  </div>
{/if}

<style>
  .list-header {
    margin-bottom: var(--spacing-05);
  }

  .create-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 520px;
    padding: var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-05);
  }

  .create-form h3 {
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    margin: 0;
    color: var(--text-primary);
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
  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    letter-spacing: 0.03em;
  }

  .scope-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: var(--spacing-03);
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
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
    gap: var(--spacing-04);
  }

  /* Token reveal panel */
  .reveal-panel {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 520px;
    padding: var(--spacing-05);
    background: color-mix(in srgb, var(--support-warning) 8%, transparent);
    border: 1px solid var(--support-warning);
    border-radius: var(--radius-md);
    margin-bottom: var(--spacing-05);
  }

  .reveal-warning {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-warning);
    margin: 0;
    font-weight: 600;
  }

  .reveal-row {
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
  }

  .reveal-actions {
    display: flex;
    justify-content: flex-start;
  }

  /* Key list */
  .keys-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .key-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-05);
    padding: var(--spacing-04) var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .key-info {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    min-width: 0;
  }

  .key-name {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .key-meta {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .muted {
    color: var(--text-helper);
    font-style: italic;
    margin: 0;
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

  /* Buttons */
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

  .btn-secondary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--layer-02);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-revoke {
    padding: var(--spacing-02) var(--spacing-04);
    background: none;
    color: var(--support-error);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    cursor: pointer;
    border: 1px solid var(--support-error);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    flex-shrink: 0;
    white-space: nowrap;
  }
  .btn-revoke:hover {
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
  }
</style>
