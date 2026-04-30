<script lang="ts">
  /**
   * Privacy settings panel: "Remember recently-used addresses" toggle
   * (REQ-SET-15, REQ-MAIL-11m).
   *
   * Persists the `seen_addresses_enabled` flag to the server via
   * PATCH /api/v1/principals/{pid} so the setting is cross-device.
   *
   * When the toggle flips to false:
   *   1. PATCH the server flag.
   *   2. On success the server purges all SeenAddress rows and stops
   *      seeding. The client also clears the local store immediately so
   *      the autocomplete dropdown stops showing them without waiting for
   *      the EventSource push.
   *
   * When the toggle flips to true:
   *   1. PATCH the server flag.
   *   2. No local store action needed — seeding resumes and entries will
   *      accumulate from then on.
   */
  import { auth } from '../../lib/auth/auth.svelte';
  import { seenAddresses } from '../../lib/contacts/seen-addresses.svelte';
  import { patch, ApiError } from '../../lib/api/client';
  import {
    avatarEmailMetadataEnabled,
    setAvatarEmailMetadataEnabled,
    clearAvatarCache,
  } from '../../lib/mail/avatar-resolver.svelte';

  let saving = $state(false);
  let error = $state<string | null>(null);

  // Optimistic local state; initialised from the server's current value when
  // the form mounts. The PATCH endpoint echoes the updated flags array back;
  // we read seen_addresses_enabled from it.
  let seenAddressesEnabled = $state(true);
  let loaded = $state(false);

  // Principal DTO shape returned by GET /api/v1/principals/{pid}.
  interface PrincipalDTO {
    id: string;
    seen_addresses_enabled: boolean;
    [key: string]: unknown;
  }

  // Load the current value from the server on mount.
  $effect(() => {
    const pid = auth.principalId;
    if (!pid || loaded) return;
    import('../../lib/api/client')
      .then(({ get }) => get<PrincipalDTO>(`/api/v1/principals/${pid}`))
      .then((p) => {
        // Default to true when the field is absent (pre-feature server builds).
        seenAddressesEnabled = p.seen_addresses_enabled ?? true;
        loaded = true;
      })
      .catch(() => {
        // Non-fatal: default to true if the fetch fails.
        seenAddressesEnabled = true;
        loaded = true;
      });
  });

  // ── Avatar lookup toggle ────────────────────────────────────────────────

  // Local reactive state mirrors the persisted localStorage value.
  let avatarLookupEnabled = $state(avatarEmailMetadataEnabled());

  // The privacy confirm dialog fires only on OFF -> ON.
  let confirmOpen = $state(false);
  // Pending value waiting for confirm dialog resolution.
  let pendingAvatarValue = $state<boolean | null>(null);

  function handleAvatarToggle(value: boolean): void {
    if (value && !avatarLookupEnabled) {
      // OFF -> ON: show privacy confirm first.
      pendingAvatarValue = value;
      confirmOpen = true;
    } else {
      // ON -> OFF: no confirm needed.
      applyAvatarToggle(value);
    }
  }

  function applyAvatarToggle(value: boolean): void {
    setAvatarEmailMetadataEnabled(value);
    avatarLookupEnabled = value;
    if (!value) {
      // Clear cached Gravatar results so the next render shows initials.
      clearAvatarCache();
    }
    confirmOpen = false;
    pendingAvatarValue = null;
  }

  function confirmAvatarEnable(): void {
    if (pendingAvatarValue !== null) applyAvatarToggle(pendingAvatarValue);
  }

  function cancelAvatarConfirm(): void {
    confirmOpen = false;
    pendingAvatarValue = null;
  }

  // ── Seen-addresses toggle ───────────────────────────────────────────────

  async function toggle(value: boolean): Promise<void> {
    const pid = auth.principalId;
    if (!pid) return;
    saving = true;
    error = null;
    const prev = seenAddressesEnabled;
    seenAddressesEnabled = value; // optimistic
    try {
      await patch<PrincipalDTO>(`/api/v1/principals/${pid}`, {
        seen_addresses_enabled: value,
      });
      if (!value) {
        // Clear the local seen-address store so the autocomplete stops
        // showing them immediately (REQ-MAIL-11m).
        seenAddresses.clear();
      }
    } catch (err) {
      seenAddressesEnabled = prev; // revert
      error = err instanceof ApiError ? err.message : String(err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="row">
  <div class="label-group">
    <span class="label">Remember recently-used addresses</span>
    <p class="hint">
      Herold keeps a per-account history of addresses you have corresponded
      with to supplement the recipient autocomplete. Turning this off purges
      the history immediately and stops new entries from being added.
    </p>
  </div>
  <label class="switch" aria-label="Remember recently-used addresses">
    <input
      type="checkbox"
      checked={seenAddressesEnabled}
      disabled={saving || !loaded}
      onchange={(e) => void toggle((e.currentTarget as HTMLInputElement).checked)}
    />
    <span class="track" aria-hidden="true"></span>
  </label>
</div>

{#if error}
  <p class="form-error" role="alert">{error}</p>
{/if}

<div class="row">
  <div class="label-group">
    <span class="label">Look up sender avatars from email metadata (Gravatar / X-Face / Face)</span>
    <p class="hint">
      When enabled, the suite contacts Gravatar with a one-way hash of each
      sender's email address to fetch their picture. Disable to keep all
      sender lookups local-only.
    </p>
  </div>
  <label class="switch" aria-label="Look up sender avatars from email metadata">
    <input
      type="checkbox"
      checked={avatarLookupEnabled}
      onchange={(e) => handleAvatarToggle((e.currentTarget as HTMLInputElement).checked)}
    />
    <span class="track" aria-hidden="true"></span>
  </label>
</div>

{#if confirmOpen}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="confirm-modal"
    role="dialog"
    aria-modal="true"
    aria-label="Look up sender pictures from the public web?"
    tabindex="-1"
    onkeydown={(e) => { if (e.key === 'Escape') cancelAvatarConfirm(); }}
  >
    <p class="confirm-title">Look up sender pictures from the public web?</p>
    <p class="confirm-body">
      When enabled, the suite contacts Gravatar with a one-way hash of each
      sender's email address to fetch their picture. The sender does not see
      this lookup, but Gravatar's logs do. The sender's email never leaves
      your device in plaintext. You can turn this off any time.
    </p>
    <div class="confirm-actions">
      <button type="button" class="btn-primary" onclick={confirmAvatarEnable}>
        Enable lookups
      </button>
      <button type="button" class="btn-secondary" onclick={cancelAvatarConfirm}>
        Keep local-only
      </button>
    </div>
  </div>
{/if}

<style>
  .row {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-04);
    padding: var(--spacing-03) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .label-group {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .switch {
    position: relative;
    display: inline-flex;
    width: 44px;
    height: 24px;
    flex-shrink: 0;
    cursor: pointer;
    margin-top: 2px;
  }

  .switch input {
    position: absolute;
    inset: 0;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }

  .switch input:disabled {
    cursor: not-allowed;
  }

  .switch .track {
    width: 100%;
    height: 100%;
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    position: relative;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .switch .track::before {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 20px;
    height: 20px;
    background: var(--text-on-color);
    border-radius: var(--radius-pill);
    transition: transform var(--duration-fast-02) var(--easing-productive-enter);
  }

  .switch input:checked + .track {
    background: var(--interactive);
  }

  .switch input:checked + .track::before {
    transform: translateX(20px);
  }

  .switch input:disabled + .track {
    opacity: 0.5;
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
</style>
