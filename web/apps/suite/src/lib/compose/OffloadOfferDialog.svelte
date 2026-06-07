<script lang="ts">
  /**
   * Offload offer dialog (REQ-ATT-61 / REQ-ATT-65).
   *
   * Shown when an attachment is an offload candidate. The user can choose:
   *   - "Upload as link" — offload the file to a share link.
   *   - "Attach anyway" — include as a regular MIME attachment (disabled
   *     when the file exceeds the hard maxSizeUpload limit).
   *
   * Behind an "Options" disclosure: expiry preset picker, optional download
   * cap, optional password.
   */
  import { untrack } from 'svelte';
  import { maxTtlSeconds, defaultTtlSeconds } from '../jmap/file-shares';
  import type { ShareOptions } from './compose.svelte';

  interface Props {
    file: File;
    forced: boolean;
    opts: ShareOptions;
    onoffload: (opts: ShareOptions) => void;
    onattach: () => void;
    oncancel: () => void;
  }

  let { file, forced, opts: initialOpts, onoffload, onattach, oncancel }: Props = $props();

  function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  // ── Expiry presets ───────────────────────────────────────────────────────

  /** Standard presets; filtered to those within max_ttl. */
  const ALL_PRESETS = [
    { label: '1 day', seconds: 1 * 24 * 3600 },
    { label: '2 days', seconds: 2 * 24 * 3600 },
    { label: '7 days', seconds: 7 * 24 * 3600 },
    { label: '14 days', seconds: 14 * 24 * 3600 },
    { label: '30 days', seconds: 30 * 24 * 3600 },
    { label: '60 days', seconds: 60 * 24 * 3600 },
    { label: '90 days', seconds: 90 * 24 * 3600 },
  ];

  let maxTtl = $derived(maxTtlSeconds());
  let presets = $derived(ALL_PRESETS.filter((p) => p.seconds <= maxTtl));

  // ── Form state ───────────────────────────────────────────────────────────

  // Intentionally capture the initial expiry from props; this dialog
  // is mounted once per file offer and the initial value does not need
  // to track prop updates (the user edits selectedExpiry interactively).
  let selectedExpiry = $state(untrack(() => initialOpts.expiresIn));
  let maxDownloadsStr = $state('');
  let password = $state('');
  let showPassword = $state(false);
  let passwordCopied = $state(false);
  let optionsOpen = $state(false);

  // Clamp selected expiry to the allowed presets on capability load.
  $effect(() => {
    const presetSeconds = presets.map((p) => p.seconds);
    if (!presetSeconds.includes(selectedExpiry)) {
      const def = defaultTtlSeconds();
      selectedExpiry = presetSeconds.includes(def) ? def : (presets[0]?.seconds ?? def);
    }
  });

  let currentOpts = $derived<ShareOptions>({
    expiresIn: selectedExpiry,
    ...(maxDownloadsStr && parseInt(maxDownloadsStr, 10) > 0
      ? { maxDownloads: parseInt(maxDownloadsStr, 10) }
      : {}),
    ...(password ? { password } : {}),
  });

  function handleOffload(): void {
    onoffload(currentOpts);
  }

  function handleAttach(): void {
    onattach();
  }

  function handleCancel(): void {
    oncancel();
  }

  async function copyPassword(): Promise<void> {
    if (!password) return;
    try {
      await navigator.clipboard.writeText(password);
      passwordCopied = true;
      setTimeout(() => (passwordCopied = false), 2000);
    } catch {
      // clipboard API unavailable
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="backdrop" onclick={handleCancel} aria-hidden="true"></div>
<div
  class="dialog"
  role="dialog"
  aria-modal="true"
  aria-labelledby="offload-title"
>
  <h2 id="offload-title">Share "{file.name}" as a link?</h2>
  <p class="info">
    <strong>{file.name}</strong>
    {' '}({formatBytes(file.size)})
    {#if forced}
      — this file is too large to attach directly. It must be uploaded as a
      download link.
    {:else}
      — {file.name.includes('.') ? 'this file type is flagged as potentially unsafe or is large' : 'this file is large'}. Upload as a link instead of attaching?
    {/if}
  </p>

  <!-- Options disclosure (REQ-ATT-65) -->
  <details
    class="options-details"
    bind:open={optionsOpen}
  >
    <summary class="options-toggle">Options</summary>

    <div class="options-body">
      <div class="option-row">
        <label for="expiry-select">Expires after</label>
        <select
          id="expiry-select"
          bind:value={selectedExpiry}
        >
          {#each presets as preset (preset.seconds)}
            <option value={preset.seconds}>{preset.label}</option>
          {/each}
        </select>
      </div>

      <div class="option-row">
        <label for="max-downloads">Download limit</label>
        <input
          id="max-downloads"
          type="number"
          min="1"
          placeholder="Unlimited"
          bind:value={maxDownloadsStr}
          class="number-input"
        />
      </div>

      <div class="option-row password-row">
        <label for="share-password">Password (optional)</label>
        <div class="password-field">
          <input
            id="share-password"
            type={showPassword ? 'text' : 'password'}
            placeholder="Leave blank for no password"
            bind:value={password}
            autocomplete="new-password"
          />
          <button
            type="button"
            class="toggle-pw"
            onclick={() => (showPassword = !showPassword)}
            aria-label={showPassword ? 'Hide password' : 'Show password'}
          >
            {showPassword ? 'Hide' : 'Show'}
          </button>
        </div>
        {#if password}
          <div class="pw-note">
            <button
              type="button"
              class="copy-pw"
              onclick={() => void copyPassword()}
            >
              {passwordCopied ? 'Copied' : 'Copy'}
            </button>
            <span class="pw-hint">
              Share this password with your recipient out of band — it cannot
              be recovered later.
            </span>
          </div>
        {/if}
      </div>
    </div>
  </details>

  <div class="actions">
    <button
      type="button"
      class="offload-btn primary"
      onclick={handleOffload}
    >
      Upload as link
    </button>
    {#if !forced}
      <button
        type="button"
        class="attach-btn"
        onclick={handleAttach}
      >
        Attach anyway
      </button>
    {/if}
    <button
      type="button"
      class="cancel-btn"
      onclick={handleCancel}
    >
      Cancel
    </button>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 950;
  }

  .dialog {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(480px, calc(100vw - 2 * var(--spacing-05)));
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 951;
    padding: var(--spacing-06);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  h2 {
    margin: 0;
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    word-break: break-word;
  }

  .info {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
  }

  /* Options disclosure */
  .options-details {
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
  }

  .options-toggle {
    display: block;
    padding: var(--spacing-03) var(--spacing-04);
    cursor: pointer;
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    background: var(--layer-01);
    user-select: none;
    list-style: none;
  }
  .options-toggle::-webkit-details-marker {
    display: none;
  }
  .options-toggle::before {
    content: '+ ';
    font-weight: 400;
  }
  .options-details[open] .options-toggle::before {
    content: '- ';
  }

  .options-body {
    padding: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    background: var(--layer-01);
    border-top: 1px solid var(--border-subtle-01);
  }

  .option-row {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .option-row label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  select,
  .number-input {
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    width: max-content;
    font-size: var(--type-body-compact-01-size);
  }

  .number-input {
    width: 12em;
  }

  .password-row {
    gap: var(--spacing-02);
  }

  .password-field {
    display: flex;
    gap: var(--spacing-02);
    align-items: center;
  }

  .password-field input {
    flex: 1;
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
  }

  .toggle-pw {
    padding: var(--spacing-02) var(--spacing-03);
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    border: 1px solid var(--interactive);
    border-radius: var(--radius-md);
    flex: 0 0 auto;
  }

  .pw-note {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-03);
    margin-top: var(--spacing-01);
  }

  .copy-pw {
    flex: 0 0 auto;
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
  }

  .pw-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    flex: 1;
  }

  /* Action buttons */
  .actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  button {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
  }

  .primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }
  .primary:hover {
    filter: brightness(1.1);
  }

  .attach-btn {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .attach-btn:hover {
    background: var(--layer-04, var(--layer-03));
    filter: brightness(0.95);
  }

  .cancel-btn {
    color: var(--text-secondary);
  }
  .cancel-btn:hover {
    color: var(--text-primary);
    background: var(--layer-03);
  }
</style>
