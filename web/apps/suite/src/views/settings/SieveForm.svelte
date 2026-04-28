<script lang="ts">
  /**
   * Raw Sieve script editor per RFC 9007. Phase 1 server ships one
   * singleton-per-principal script, so the form loads the first row
   * (if any), downloads its blob, and lets the user edit it as text.
   * Save uploads the current text via /jmap/upload, then dispatches
   * SieveScript/set { create | update: { ...: {blobId, isActive: true} } }.
   */
  import { jmap, strict } from '../../lib/jmap/client';
  import { Capability } from '../../lib/jmap/types';
  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';

  interface SieveScript {
    id: string;
    name: string;
    blobId: string;
    isActive: boolean;
    createdAt: string;
  }

  type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

  let status = $state<LoadStatus>('idle');
  let error = $state<string | null>(null);
  let saving = $state(false);

  let scriptId = $state<string | null>(null);
  let scriptName = $state('default');
  let body = $state('');
  let validationErrors = $state<{ line: number; column: number; message: string }[]>(
    [],
  );

  $effect(() => {
    if (status === 'idle') void load();
  });

  async function load(): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) {
      status = 'error';
      error = 'No Mail account on this session';
      return;
    }
    status = 'loading';
    error = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'SieveScript/get',
          { accountId, ids: null },
          [Capability.Sieve],
        );
      });
      strict(responses);
      const args = responses[0]![1] as { list: SieveScript[] };
      const active = args.list.find((s) => s.isActive) ?? args.list[0] ?? null;
      if (!active) {
        // No script yet — keep the form blank and let Save create one.
        scriptId = null;
        body = '';
        status = 'ready';
        return;
      }
      scriptId = active.id;
      scriptName = active.name;
      const url = jmap.downloadUrl({
        accountId,
        blobId: active.blobId,
        type: 'application/sieve',
        name: active.name,
      });
      if (!url) throw new Error('No download URL');
      const res = await fetch(url, { credentials: 'include' });
      if (!res.ok) throw new Error(`Sieve blob fetch: HTTP ${res.status}`);
      body = await res.text();
      status = 'ready';
    } catch (err) {
      status = 'error';
      error = err instanceof Error ? err.message : 'Failed to load Sieve script';
    }
  }

  async function save(): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) return;
    saving = true;
    validationErrors = [];
    try {
      // Upload the script text to obtain a fresh blobId.
      const blob = new Blob([body], { type: 'application/sieve' });
      const uploadResult = await jmap.uploadBlob({
        accountId,
        body: blob,
        type: 'application/sieve',
      });
      // Dispatch SieveScript/set with create or update.
      const setArgs: Record<string, unknown> = { accountId };
      const tempId = 'editor';
      if (scriptId) {
        setArgs.update = {
          [scriptId]: {
            blobId: uploadResult.blobId,
            isActive: true,
            name: scriptName,
          },
        };
      } else {
        setArgs.create = {
          [tempId]: {
            name: scriptName || 'default',
            blobId: uploadResult.blobId,
            isActive: true,
          },
        };
      }
      const { responses } = await jmap.batch((b) => {
        b.call('SieveScript/set', setArgs, [Capability.Sieve]);
      });
      strict(responses);
      const result = responses[0]![1] as {
        created?: Record<string, SieveScript>;
        updated?: Record<string, SieveScript | null>;
        notCreated?: Record<
          string,
          {
            type: string;
            description?: string;
            sieveValidationErrors?: { line: number; column: number; message: string }[];
          }
        >;
        notUpdated?: Record<
          string,
          {
            type: string;
            description?: string;
            sieveValidationErrors?: { line: number; column: number; message: string }[];
          }
        >;
      };
      const failure = scriptId
        ? result.notUpdated?.[scriptId]
        : result.notCreated?.[tempId];
      if (failure) {
        if (failure.sieveValidationErrors) {
          validationErrors = failure.sieveValidationErrors;
        }
        toast.show({
          message: failure.description ?? `Save failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return;
      }
      const created = result.created?.[tempId];
      const updated = result.updated?.[scriptId ?? ''];
      if (created) scriptId = created.id;
      else if (updated) scriptId = updated.id;
      toast.show({ message: 'Sieve script saved' });
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : 'Save failed',
        kind: 'error',
        timeoutMs: 6000,
      });
    } finally {
      saving = false;
    }
  }
</script>

{#if status === 'loading' || status === 'idle'}
  <p class="hint">Loading…</p>
{:else if status === 'error'}
  <p class="error" role="alert">{error}</p>
  <button type="button" onclick={() => void load()}>Retry</button>
{:else}
  <p class="hint">
    Sieve scripts run server-side on incoming mail. Phase 1 ships a single
    active script per account; the editor below is the raw RFC 5228 / 9007
    source. See <a
      href="https://datatracker.ietf.org/doc/html/rfc5228"
      target="_blank"
      rel="noopener noreferrer">RFC 5228</a
    > for the language.
  </p>

  <div class="row vertical">
    <span class="label">Name</span>
    <input
      type="text"
      bind:value={scriptName}
      placeholder="default"
      autocomplete="off"
      spellcheck="false"
    />
  </div>

  <div class="row vertical">
    <span class="label">Script</span>
    <textarea
      class="sieve-body"
      bind:value={body}
      rows="12"
      spellcheck="false"
      autocomplete="off"
      placeholder={'require ["fileinto"];\n# example rule:\n# if address :is "From" "list@example.com" {\n#   fileinto "INBOX/list";\n# }'}
    ></textarea>
  </div>

  {#if validationErrors.length > 0}
    <div class="validation" role="alert">
      <p>Sieve validation errors:</p>
      <ul>
        {#each validationErrors as e}
          <li>
            <span class="loc">{e.line}:{e.column}</span> — {e.message}
          </li>
        {/each}
      </ul>
    </div>
  {/if}

  <div class="row">
    <span class="label"></span>
    <button type="button" class="primary" onclick={() => void save()} disabled={saving}>
      {saving ? 'Saving…' : 'Save'}
    </button>
  </div>
{/if}

<style>
  .row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .row.vertical {
    flex-direction: column;
    align-items: stretch;
    gap: var(--spacing-02);
  }
  .label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    flex: 0 0 auto;
    min-width: 12em;
  }
  .row.vertical .label {
    min-width: 0;
  }

  input[type='text'],
  textarea {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    font-family: inherit;
    font-size: var(--type-body-01-size);
  }
  textarea.sieve-body {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    min-height: 240px;
    resize: vertical;
    line-height: 1.5;
  }

  .primary {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: progress;
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .hint a {
    color: var(--interactive);
  }
  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .validation {
    background: var(--layer-01);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }
  .validation p {
    margin: 0 0 var(--spacing-02);
    font-weight: 600;
  }
  .validation ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .validation .loc {
    font-family: var(--font-mono);
    font-weight: 600;
  }
</style>
