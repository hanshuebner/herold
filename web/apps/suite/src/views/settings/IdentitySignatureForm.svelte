<script lang="ts">
  /**
   * Per-identity signature editor — REQ-SET-03 (plain text in v1; HTML
   * signatures cut to phase 2). Uses RFC 8621 §6 `textSignature`
   * via Identity/set.
   */
  import { jmap, strict } from '../../lib/jmap/client';
  import { mail } from '../../lib/mail/store.svelte';
  import { Capability, type Invocation } from '../../lib/jmap/types';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';

  interface Props {
    identity: Identity;
  }
  let { identity }: Props = $props();

  // Local edit buffer; resets when the prop changes (the $effect below
  // mirrors the prop into both fields once on mount and again on prop swap).
  let draft = $state('');
  let savedValue = $state('');
  let saving = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    draft = identity.textSignature ?? '';
    savedValue = identity.textSignature ?? '';
  });

  let dirty = $derived(draft !== savedValue);

  async function save(): Promise<void> {
    if (!dirty || saving) return;
    const accountId = mail.mailAccountId;
    if (!accountId) {
      error = t('settings.identity.signatureNoAccount');
      return;
    }
    saving = true;
    error = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Identity/set',
          {
            accountId,
            update: {
              [identity.id]: { textSignature: draft },
            },
          },
          [Capability.Submission],
        );
      });
      strict(responses);

      const result = invocationArgs<{
        notUpdated?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      const failure = result.notUpdated?.[identity.id];
      if (failure) {
        throw new Error(failure.description ?? failure.type);
      }

      // Mirror back into the cache so the form's "dirty" state clears.
      const next = new Map(mail.identities);
      const cur = next.get(identity.id);
      if (cur) next.set(identity.id, { ...cur, textSignature: draft });
      mail.identities = next;
      savedValue = draft;
      toast.show({ message: t('settings.identity.signatureSaved'), timeoutMs: 3000 });
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  function revert(): void {
    draft = savedValue;
    error = null;
  }

  function invocationArgs<T>(inv: Invocation | undefined): T {
    if (!inv) throw new Error('Expected method invocation, got undefined');
    return inv[1] as T;
  }
</script>

<form
  class="form"
  onsubmit={(e) => {
    e.preventDefault();
    void save();
  }}
>
  <div class="head">
    <span class="who">
      {identity.name ? `${identity.name} <${identity.email}>` : identity.email}
    </span>
  </div>

  <label class="textarea-label">
    <span class="label-text">{t('settings.identity.signatureLabel')}</span>
    <textarea
      bind:value={draft}
      rows="4"
      placeholder="—&#10;Best,&#10;{identity.name ?? identity.email}"
      disabled={saving}
    ></textarea>
  </label>

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  <div class="actions">
    <button type="button" class="ghost" onclick={revert} disabled={!dirty || saving}>
      {t('common.revert')}
    </button>
    <button type="submit" class="primary" disabled={!dirty || saving}>
      {saving ? t('common.saving') : t('common.save')}
    </button>
  </div>
</form>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .who {
    font-weight: 600;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    word-break: break-all;
  }

  .textarea-label {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .label-text {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  textarea {
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: var(--spacing-03);
    resize: vertical;
    min-height: 6rem;
  }
  textarea:focus {
    outline: none;
    border-color: var(--focus);
    box-shadow: 0 0 0 1px var(--focus);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
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
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }
</style>
