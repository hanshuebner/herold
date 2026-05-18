<script lang="ts">
  /**
   * Per-identity signature editor — REQ-SET-03 (plain text in v1; HTML
   * signatures cut to phase 2). Uses RFC 8621 §6 `textSignature`
   * via Identity/set.
   *
   * Item 7: autosaves on blur — no Save button. Save feedback is the
   * editor page's shared `AutosaveController` indicator.
   */
  import { jmap, strict } from '../../lib/jmap/client';
  import { mail } from '../../lib/mail/store.svelte';
  import { Capability, type Invocation } from '../../lib/jmap/types';
  import { t } from '../../lib/i18n/i18n.svelte';
  import type { Identity } from '../../lib/mail/types';
  import type { AutosaveController } from './autosave.svelte';

  interface Props {
    identity: Identity;
    /** Shared autosave indicator controller owned by the editor page. */
    autosave: AutosaveController;
  }
  let { identity, autosave }: Props = $props();

  // Local edit buffer; resets when the prop changes.
  let draft = $state('');
  let savedValue = $state('');

  $effect(() => {
    draft = identity.textSignature ?? '';
    savedValue = identity.textSignature ?? '';
  });

  async function persist(value: string): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) {
      throw new Error(t('settings.identity.signatureNoAccount'));
    }
    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          update: {
            [identity.id]: { textSignature: value },
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

    // Mirror back into the cache so compose / reply flows see the change.
    const next = new Map(mail.identities);
    const cur = next.get(identity.id);
    if (cur) next.set(identity.id, { ...cur, textSignature: value });
    mail.identities = next;
  }

  /** Commit on blur when the signature changed. */
  async function commit(): Promise<void> {
    if (draft === savedValue) return;
    const value = draft;
    const ok = await autosave.run(() => persist(value));
    if (ok) savedValue = value;
  }

  function invocationArgs<T>(inv: Invocation | undefined): T {
    if (!inv) throw new Error('Expected method invocation, got undefined');
    return inv[1] as T;
  }
</script>

<div class="form">
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
      onblur={() => void commit()}
      data-testid="identity-signature-input"
    ></textarea>
  </label>
</div>

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
</style>
