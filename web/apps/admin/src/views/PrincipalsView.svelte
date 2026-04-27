<script lang="ts">
  import { principals, FLAG_ADMIN, FLAG_TOTP_ENABLED, FLAG_DISABLED, type CreatePrincipalPayload } from '../lib/principals/principals.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';

  let createOpen = $state(false);
  let createEmail = $state('');
  let createPassword = $state('');
  let createAutoPassword = $state(false);
  let createDisplayName = $state('');
  let createAdmin = $state(false);
  let createError = $state<string | null>(null);
  let createSubmitting = $state(false);

  $effect(() => {
    if (principals.status === 'idle') {
      void principals.load();
    }
  });

  function openCreate(): void {
    createEmail = '';
    createPassword = '';
    createAutoPassword = false;
    createDisplayName = '';
    createAdmin = false;
    createError = null;
    createOpen = true;
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  }

  function generatePassword(): string {
    const chars = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*';
    const arr = new Uint8Array(20);
    crypto.getRandomValues(arr);
    return Array.from(arr, (b) => chars[b % chars.length]).join('');
  }

  $effect(() => {
    if (createAutoPassword) {
      createPassword = generatePassword();
    }
  });

  async function handleCreate(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (createSubmitting) return;
    createError = null;
    createSubmitting = true;

    const payload: CreatePrincipalPayload = {
      email: createEmail.trim(),
      password: createPassword,
      display_name: createDisplayName.trim() || undefined,
      admin: createAdmin || undefined,
    };

    const result = await principals.create(payload);
    createSubmitting = false;

    if (!result.ok) {
      createError = result.errorMessage;
      return;
    }

    // Keep modal open so the operator can create more principals.
    createEmail = '';
    createPassword = '';
    createAutoPassword = false;
    createDisplayName = '';
    createAdmin = false;
    createError = null;
  }
</script>

<div class="principals-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">Principals</h1>
      {#if principals.status === 'loading'}
        <div class="spinner" role="status" aria-label="Loading"></div>
      {/if}
    </div>
    <div class="page-header-right">
      <input
        type="search"
        class="search-input"
        placeholder="Filter by email..."
        bind:value={principals.search}
        aria-label="Filter principals"
      />
      <button type="button" class="btn-primary" onclick={openCreate}>
        New principal
      </button>
    </div>
  </div>

  {#if principals.errorMessage && principals.status === 'error'}
    <div class="page-error" role="alert">{principals.errorMessage}</div>
  {/if}

  {#if principals.status === 'ready' || principals.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-email">Email</th>
            <th class="col-name">Display name</th>
            <th class="col-flags">Flags</th>
            <th class="col-created">Created</th>
          </tr>
        </thead>
        <tbody>
          {#each principals.filtered as p (p.id)}
            <tr class="table-row" onclick={() => router.navigate(`/principals/${p.id}`)}>
              <td class="col-email">
                <span class="email-text">{p.email}</span>
              </td>
              <td class="col-name">{p.display_name || ''}</td>
              <td class="col-flags">
                <div class="chips">
                  {#if p.flags & FLAG_ADMIN}
                    <span class="chip chip-admin">admin</span>
                  {/if}
                  {#if p.flags & FLAG_TOTP_ENABLED}
                    <span class="chip chip-totp">2fa</span>
                  {/if}
                  {#if p.flags & FLAG_DISABLED}
                    <span class="chip chip-disabled">disabled</span>
                  {/if}
                </div>
              </td>
              <td class="col-created">{formatDate(p.created_at)}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="4" class="empty-row">
                {principals.search ? 'No principals match this filter.' : 'No principals found.'}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if principals.hasMore}
      <div class="load-more">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => void principals.loadMore()}
          disabled={principals.status === 'loading'}
        >
          {principals.status === 'loading' ? 'Loading...' : 'Load more'}
        </button>
      </div>
    {/if}
  {:else if principals.status !== 'loading' && principals.status !== 'idle'}
    <p class="empty-state">No principals found.</p>
  {/if}
</div>

<!-- Create principal dialog -->
<Dialog bind:open={createOpen} title="New principal" width="520px">
  <form class="create-form" onsubmit={handleCreate} novalidate>
    <div class="field">
      <label for="cp-email" class="label">Email address</label>
      <input
        id="cp-email"
        type="email"
        class="input"
        autocomplete="off"
        required
        bind:value={createEmail}
        disabled={createSubmitting}
      />
    </div>

    <div class="field">
      <label for="cp-display-name" class="label">Display name</label>
      <input
        id="cp-display-name"
        type="text"
        class="input"
        bind:value={createDisplayName}
        disabled={createSubmitting}
      />
    </div>

    <div class="field">
      <div class="password-header">
        <label for="cp-password" class="label">Password</label>
        <label class="auto-label">
          <input
            type="checkbox"
            bind:checked={createAutoPassword}
            disabled={createSubmitting}
          />
          Auto-generate
        </label>
      </div>
      <input
        id="cp-password"
        type={createAutoPassword ? 'text' : 'password'}
        class="input input-mono"
        autocomplete="new-password"
        required
        readonly={createAutoPassword}
        bind:value={createPassword}
        disabled={createSubmitting}
      />
    </div>

    <div class="field-check">
      <label class="check-label">
        <input
          type="checkbox"
          bind:checked={createAdmin}
          disabled={createSubmitting}
        />
        Grant admin privileges
      </label>
    </div>

    {#if createError}
      <p class="form-error" role="alert">{createError}</p>
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn-secondary"
        onclick={() => { createOpen = false; }}
        disabled={createSubmitting}
      >
        Cancel
      </button>
      <button type="submit" class="btn-primary" disabled={createSubmitting}>
        {createSubmitting ? 'Creating...' : 'Create principal'}
      </button>
    </div>
  </form>
</Dialog>

<style>
  .principals-page {
    max-width: 1100px;
  }

  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-06);
  }

  .page-header-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .page-header-right {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .search-input {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    width: 220px;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .search-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }

  .page-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-06);
  }

  .table-wrapper {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }

  .table {
    width: 100%;
    border-collapse: collapse;
  }

  .table thead tr {
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .table th {
    text-align: left;
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
    background: var(--layer-01);
  }

  .table-row {
    cursor: pointer;
    border-bottom: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .table-row:last-child {
    border-bottom: none;
  }
  .table-row:hover {
    background: var(--layer-02);
  }

  .table td {
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    vertical-align: middle;
  }

  .col-email { width: 40%; }
  .col-name { width: 25%; }
  .col-flags { width: 20%; }
  .col-created { width: 15%; white-space: nowrap; }

  .email-text {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .chips {
    display: flex;
    gap: var(--spacing-02);
    flex-wrap: wrap;
  }

  .chip {
    display: inline-block;
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    line-height: 18px;
  }

  .chip-admin {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
  }

  .chip-totp {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
  }

  .chip-disabled {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: var(--support-warning);
  }

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
  }

  .empty-state {
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
  }

  .load-more {
    margin-top: var(--spacing-05);
    text-align: center;
  }

  /* Create form */
  .create-form {
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
    font-weight: 600;
    color: var(--text-secondary);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-02);
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
  .input-mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    letter-spacing: 0.05em;
  }

  .password-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  .auto-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .field-check {
    display: flex;
    align-items: center;
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
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
    justify-content: flex-end;
    gap: var(--spacing-04);
    padding-top: var(--spacing-02);
  }

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
</style>
